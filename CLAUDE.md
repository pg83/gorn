# gorn — context for Claude

Small Go service. HA queue over etcd; leader dispatches tasks over SSH to worker endpoints; a wrapper on the worker runs the command and uploads logs/result to S3.

See [`README.md`](README.md) for the full picture and [`STYLE.md`](STYLE.md) for code conventions.

## Non-negotiable rules

- **Error handling goes through `Throw` / `Try`** (see `throw.go`). The forbidden shape is pure pass-through — `if err != nil { return err }` that re-types the bubble without doing anything. Use `Throw2(fn())`, `Throw(err)`, `ThrowFmt(...)`; catch at boundaries (`main`, goroutine entries, filter loops) with `Try`. Returning an `error` from your own function *is* fine when the error is substantive — part of the contract, to be branched on by the caller (interface obligations, domain signals like `ErrNotFound`). Details in `STYLE.md`.
- **Blank lines around every `if`/`for`/`switch`/`select` and before every `return`**, unless the statement is the first/last inside `{}`. Consecutive `Throw*`/`:=`/`=` that form one logical step stay together; split logical steps with a blank line. See `STYLE.md` for examples.
- **Config is JSON. Never YAML.** Not as an example, not as an alternative — just don't.
- **Files live in the repo root.** Don't create `internal/`, `cmd/`, `pkg/` — this project is intentionally flat.
- **Never truncate text for "readability".** Don't clip stdout/stderr, logs, error messages, diagnostics, payloads, anything. No helper like `truncate(s, n)`, no `...(truncated)`, no `head -c`. If output is long, let it be long — the reader will scroll. This project does not do length-based prettification of any kind.
- **Library choices**: S3 via `aws-sdk-go-v2` (works against MinIO), etcd via `go.etcd.io/etcd/client/v3`. For SSH we **shell out** to the `ssh` binary — process isolation lets a stuck connection be killed on the target host without touching the daemon. Outcome is read as JSON from the wrap's stdout, not from ssh's exit code.

## Subcommands

One binary, three subcommands:

- `gorn serve --config X` — HA daemon. Campaigns for leadership; when elected, runs a per-endpoint goroutine pool that dispatches tasks via SSH. On leadership loss: `os.Exit(0)` (let systemd restart).
- `gorn wrap` — run on the worker via SSH. Reads all context (guid, cmd, env, user, s3 creds) from stdin JSON. Checks `HEAD gorn/<guid>/result.json` for idempotency, kills stale procs of the endpoint user, execs the command, uploads logs + result.json to S3, prints one final JSON line to stdout.
- `gorn ignite [--etcd-endpoints a,b,c] --guid G [--env K=V] -- cmd args...` — enqueue. Takes etcd endpoints directly (flag or `$ETCDCTL_ENDPOINTS`), no JSON config. Dedup via etcd txn on `CreateRevision == 0`.

## Important invariants

- **Client-supplied GUID.** Clients pick the GUID. It's the etcd key suffix and the S3 prefix. Duplicate ignite → error.
- **Tasks are assumed idempotent.** Split-brain and retries can cause re-execution. Wrap handles this by killing any stale processes owned by the endpoint's user before running.
- **Outcome classification is based on the last JSON line of the SSH session stdout**, not the SSH exit code. See `classify` in `runner.go`.
  - `already-done` or `completed + exit==0` → success (delete from queue).
  - `completed + exit!=0` → non-retriable (delete from queue).
  - no finish JSON / transport error / wrapper crash → retriable (leave in queue).
- **Fenced writes.** Any etcd write by the leader goes through a txn gated by `leader.FenceCompare()` (`CreateRevision(election.Key()) == election.Rev()`). If it has silently lost leadership, the write aborts.

## Build / test

```
go build ./...        # produces ./gorn
go test ./...         # unit tests only; no etcd/S3/SSH required
```

Integration testing means actually running `serve` against a real etcd/S3/SSH environment — there are no mocks.

## What's not done

Host-key verification (currently `InsecureIgnoreHostKey`), log streaming (single PUT at the end), CLI subcommands `list` / `show` / `cancel` / `logs` / `leader`.
