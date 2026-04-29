# gorn — context for Claude

Small Go service. HA queue over etcd; leader dispatches tasks over SSH to worker endpoints; a wrapper on the worker runs the command and uploads logs/result to S3.

See [`README.md`](README.md) for the full picture and [`STYLE.md`](STYLE.md) for code conventions.

## Coding conventions

- Git author: `claude <claude@users.noreply.github.com>`. Commit messages in English.

## Non-negotiable rules

- **Error handling goes through `Throw` / `Try`** (see `throw.go`). The forbidden shape is pure pass-through — `if err != nil { return err }` that re-types the bubble without doing anything. Use `Throw2(fn())`, `Throw(err)`, `ThrowFmt(...)`; catch at boundaries (`main`, goroutine entries, filter loops) with `Try`. Returning an `error` from your own function *is* fine when the error is substantive — part of the contract, to be branched on by the caller (interface obligations, domain signals like `ErrNotFound`). Details in `STYLE.md`.
- **Blank lines around every `if`/`for`/`switch`/`select` and before every `return`**, unless the statement is the first/last inside `{}`. Consecutive `Throw*`/`:=`/`=` that form one logical step stay together; split logical steps with a blank line. See `STYLE.md` for examples.
- **Config is JSON. Never YAML.** Not as an example, not as an alternative — just don't.
- **Files live in the repo root.** Don't create `internal/`, `cmd/`, `pkg/` — this project is intentionally flat.
- **Never truncate text for "readability".** Don't clip stdout/stderr, logs, error messages, diagnostics, payloads, anything. No helper like `truncate(s, n)`, no `...(truncated)`, no `head -c`. If output is long, let it be long — the reader will scroll. This project does not do length-based prettification of any kind.
- **Library choices**: S3 via `aws-sdk-go-v2` (works against MinIO), etcd via `go.etcd.io/etcd/client/v3`. For SSH we **shell out** to the `ssh` binary — process isolation lets a stuck connection be killed on the target host without touching the daemon. Outcome is read as JSON from the wrap's stdout, not from ssh's exit code.

## Subcommands

One binary, five subcommands:

- `gorn serve --config X` — HA daemon. Campaigns for leadership; when elected, runs a per-endpoint goroutine pool that dispatches tasks via SSH. On leadership loss: `os.Exit(0)` (let systemd restart).
- `gorn control --config X` — HTTP JSON RPC over etcd + S3. Listens on `control.listen`. Endpoints: `POST /v1/tasks` (enqueue, auto-GUID, stamps `enqueued_at`), `GET /v1/tasks` (queue list), `GET /v1/tasks/<guid>` (state: `queued|done|not_found`), `GET /v1/tasks/<guid>/output` (parsed `result.json` + base64 stdout/stderr), `GET /v1/endpoints` (endpoint list from config). No leader election; enqueue is an etcd `CreateRevision==0` txn, any instance can serve.
- `gorn web --config X` — Bootstrap-CSS dashboard. Reads only `web.api` and `web.listen` from the config; talks to `control` via HTTP. Renders two tables (endpoints, queue with per-task age), auto-refreshes every 2s. Read-only, never goes to etcd/S3 directly.
- `gorn wrap` — run on the worker via SSH. Reads all context (guid, cmd, env, user, s3 creds) from stdin JSON. Checks `HEAD gorn/<guid>/result.json` for idempotency, kills stale procs of the endpoint user, execs the command, uploads logs + result.json to S3, prints one final JSON line to stdout.
- `gorn ignite --api URL [--guid G] [--env K=V] [--wait] -- cmd args...` — thin HTTP client for `control`. No etcd or S3 knowledge. `--wait` polls state, fetches output on `done`, writes stdout/stderr to its own fds, exits with the task's `exit_code`. `--api` also falls back to `$GORN_API`.

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

## Misc

- Queue key prefix is schema-versioned (`/gorn/queue_v2/`). Any incompatible change to the `Task` JSON shape bumps the suffix — old entries just sit; nothing reads them. Update the changelog comment at the `queuePrefix` const when bumping.
- Task body is a script (`Task.Script`), not an argv. `wrap.go::runCmd` writes it to a `memfd_create` without `MFD_CLOEXEC`, then execs `/proc/self/fd/N` — kernel `binfmt_script` handles the shebang, so there is no ARG_MAX on the script body. `ignite` accepts the script on stdin; positional args after `--` are synthesized into a minimal `#!/bin/sh\nexec <quoted-args>` for compat.
- Scheduling is slot-aware, host-level. Each endpoint = 1 slot; per-host capacity is `len(endpoints-on-host)` and metered by `golang.org/x/sync/semaphore.Weighted`. A single scheduler goroutine scans an in-memory `QueueIndex` (initial `Get` + `Watch` from returned revision, resync on compaction) and first-fits tasks onto hosts. `Task.Slots` comes from the client; `MOLOT_SLOTS` and `MOLOT_CPUS=round(slots * cpus_per_slot * cpu_overcommit)` are injected into `task.Env` before SSH.
- Leader state (inflight, semaphores, endpoint free-list) is pure in-memory — nothing is persisted to etcd. Failover rebuilds from the live queue + idempotent `HEAD <root>/<guid>/result.json` on S3.
