# gorn

Small HA queue service for running shell jobs on a fleet of hosts over SSH.

Designed for a homelab: three nodes run the daemon, one is elected leader via etcd, the leader consumes a FIFO queue of jobs and dispatches each to a worker endpoint via SSH. Job stdout/stderr and exit status land in S3-compatible object storage. Jobs are assumed idempotent.

## Architecture

```
 clients ──[ignite]──► etcd /gorn/queue/<guid>
                           │
                           ▼
                  ┌────────────────┐
                  │ leader (1 of 3)│ ◄── concurrency.NewElection
                  │    serve       │
                  └────────┬───────┘
                           │ per-endpoint goroutines
                           ▼
           ssh user@host 'cd path && gorn wrap'
                           │ stdin: JSON context
                           ▼
                       gorn wrap
                           │
                           ▼
                      S3 /gorn/<guid>/{stdout,stderr,result.json}
```

- **Queue**: `etcd` keys under `/gorn/queue/<guid>`. FIFO by `CreateRevision`. Client-supplied GUID gives dedup and stable log keys.
- **Leader election**: `concurrency.NewElection` with a session lease. On loss the daemon calls `os.Exit(0)` — systemd restarts it, it re-campaigns.
- **Fencing**: every write the leader makes to etcd is in a txn gated by `CreateRevision(election.Key()) == election.Rev()`. If the leader has been replaced, the txn aborts.
- **Endpoint**: tuple `(host, user, path)`. Each user is the unit of isolation and concurrency: one task per endpoint at a time. Hosts are sliced into N endpoints by distinct users.
- **Wrapper (`gorn wrap`)**: the command run over SSH. Reads all context from stdin JSON, does an idempotency check against S3 (`HEAD gorn/<guid>/result.json`), kills stale processes owned by the endpoint's user, runs the command, uploads logs and a result record to S3, prints one final JSON line to stdout.
- **Outcome classification** (by the leader): the leader parses the last JSON line of the SSH session stdout.
  - `outcome=already-done` or `outcome=completed` with `exit=0` → **success** (delete from queue).
  - `outcome=completed` with non-zero `exit` → **non-retriable failure** (delete from queue).
  - anything else (no JSON, transport error, wrapper crash) → **retriable** (leave in queue).
- **Split-brain**: if the old leader is still running ssh when a new leader takes over, the new leader's wrap will `pkill -u <user>` on its endpoint before starting. Tasks are assumed idempotent, so duplicate execution is tolerated.

## Subcommands

```
gorn serve   --config path                                             # daemon, runs on every HA node; elects leader, dispatches
gorn control --config path                                             # HTTP JSON RPC in front of etcd + S3; used by ignite
gorn wrap                                                              # invoked on workers via ssh, reads stdin JSON
gorn ignite  --api URL [--guid G] [--env K=V ...] [--wait] -- cmd args...
```

`serve` needs etcd, S3, and SSH keys — it does leader election and dispatches tasks. `control` takes the same JSON config and exposes an HTTP API for enqueueing and querying tasks; it does not participate in dispatch. `ignite` is a thin HTTP client: it posts to the `control` endpoint and has no etcd or S3 knowledge. `wrap` only touches S3, `/proc`, and `exec`.

### Control API

`control` listens on `control.listen` and speaks JSON.

- `POST /v1/tasks` with body `{"guid": "...", "cmd": [...], "env": {...}}`. `guid` is optional — the server generates a UUIDv4 if missing. Returns `{"guid": "..."}` on 200, or 409 if the GUID is already queued.
- `GET /v1/tasks/<guid>` → `{"guid": "...", "state": "queued" | "done" | "not_found"}`. `queued` means the etcd key still exists (waiting or retrying); `done` means the key is gone and `result.json` is in S3; `not_found` means neither.
- `GET /v1/tasks/<guid>/output` → `{"result": {...}, "stdout_b64": "...", "stderr_b64": "..."}` on 200, or 404 if `result.json` is not yet in S3. `result` is the parsed `result.json`; `stdout_b64` / `stderr_b64` are base64-encoded full streams (never truncated).

Since enqueue is a compare-revision-zero etcd txn, any `control` instance can serve `POST /v1/tasks` — leadership is not required.

### ignite

```
gorn ignite --api http://localhost:7878 --guid mytask -- echo hi       # enqueue, print guid, exit
gorn ignite --api http://localhost:7878 --wait -- false                # enqueue, wait, print stdout/stderr, exit with task exit code
```

`--api` can also come from `$GORN_API`. With `--wait` ignite polls `/v1/tasks/<guid>` every 500ms until state is `done`, then fetches `/output`, writes stdout/stderr to its own fds, and exits with the task's `exit_code`.

## Configuration

JSON. Example: [`config.example.json`](config.example.json). Path is passed via `--config`; the orchestration system will supply it.

Fields:

- `endpoints[]`: list of `{host, user, path}`. Each endpoint may also carry:
  - `ssh_key`: the PEM body of the private key to use for this endpoint. When set, it overrides the global `ssh_key_path`. The body is loaded into an anonymous **memfd** (`memfd_create` with `MFD_CLOEXEC`), `fchmod`'d to `0600`, and kept open by the daemon. `ssh` reads it via `-i /proc/<gorn-pid>/fd/<N>` — the path of the daemon's own fd in procfs. Same-UID access (ssh runs as the same user as the daemon) is sufficient; no fd inheritance is relied upon. Key material never touches the filesystem.
  - `log_path`: path on the **worker** where `gorn wrap` will append per-task diagnostic lines (start, idempotency check, command exit, S3 upload, finish emit, any error). The path is sent to the worker as part of the wrap stdin JSON; the worker `gorn wrap` opens it `O_APPEND|O_CREATE|O_WRONLY` mode `0600`. If unset, no log is written. Useful for debugging cases where the daemon sees no finish message — the worker log shows whether `wrap` ran at all.
- `etcd.endpoints[]`: etcd cluster URLs. Accepts `host:port` or `scheme://host:port` — the etcd v3 client handles both.
- `s3`: `{endpoint, region, bucket, access_key, secret_key, use_path_style}`. `endpoint` empty means AWS default. `use_path_style=true` for MinIO.
- `control.listen`: address for `gorn control` to bind its HTTP JSON RPC, e.g. `"127.0.0.1:7878"`. Required only for `control`; `serve` ignores it.
- `ssh_key_path`: private key the daemon uses to connect to endpoints. Optional if every endpoint provides its own `ssh_key`.

### `${VAR}` expansion

Before the JSON is parsed, the raw text is passed through a substitution pass that replaces every occurrence of `${NAME}` with `os.Getenv("NAME")`. The pattern is `\$\{[A-Za-z_][A-Za-z0-9_]*\}` — no default-value syntax, no `$NAME` without braces. If a referenced variable is unset, `LoadConfig` throws (typos fail loudly rather than turning into empty strings). The substitution is a plain string replace: if the env value contains characters that need JSON escaping (quotes, backslashes, raw newlines), you must pre-escape them in the env value.

Example:

```json
{
  "s3": {
    "endpoint": "${MINIO_URL}",
    "access_key": "${MINIO_ACCESS_KEY}",
    "secret_key": "${MINIO_SECRET_KEY}"
  },
  "endpoints": [
    {"host": "n1.home.local", "user": "gorn-w1", "path": "/srv/gorn/w1", "ssh_key": "${GORN_W1_SSH_KEY}"}
  ]
}
```

### Env overlays

After the JSON is parsed, three standard environment variables override the corresponding config fields when set (env wins over JSON):

- `ETCDCTL_ENDPOINTS` — comma-separated, whitespace-trimmed, replaces `etcd.endpoints[]`. Lets you reuse the same env var `etcdctl` reads.
- `AWS_ACCESS_KEY_ID` — overrides `s3.access_key`.
- `AWS_SECRET_ACCESS_KEY` — overrides `s3.secret_key`.

These are a convenience for deployments that already inject credentials via the AWS/etcdctl env conventions, so the JSON can stay credential-free. The `${VAR}` mechanism above is more general; use whichever fits.

## Build

```
go build ./...
```

Produces a single `gorn` binary that holds all subcommands. Deploy the same binary to HA nodes (for `serve` / `control`) and to workers (for `wrap` — must be in the endpoint user's `PATH`). `ignite` can run anywhere that can reach a `control` endpoint.

## Test

```
go test ./...
```

Unit tests only — they exercise pure logic (classification of SSH output, parsers, etc.). Integration testing against real etcd / S3 / SSH is done by running the daemon.

## Status

MVP scaffold. Missing: host-key checking (currently `InsecureIgnoreHostKey`), log streaming (current impl uploads one PUT at end), CLI commands `list` / `show` / `cancel` / `logs` / `leader`.

## See also

- [`STYLE.md`](STYLE.md) — code style and error-handling rules.
- [`CLAUDE.md`](CLAUDE.md) — brief context for Claude Code sessions.
