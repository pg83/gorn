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
gorn serve  --config path    # daemon, run on every HA node
gorn wrap                    # invoked on workers via ssh, reads stdin JSON
gorn ignite --config path --guid G [--env K=V ...] -- cmd args...
```

`serve` needs etcd, S3, and SSH keys. `ignite` only touches etcd. `wrap` only touches S3 and `/proc` and `exec`.

## Configuration

JSON. Example: [`config.example.json`](config.example.json). Path is passed via `--config`; the orchestration system will supply it.

Fields:

- `endpoints[]`: list of `{host, user, path}`. Optional `ssh_key`: PEM body of the private key for this endpoint; overrides the global `ssh_key_path`. The body is held in an anonymous memfd — not written to the filesystem — and passed to `ssh` via an inherited fd. Combine with `${VAR}` expansion to inject from env.
- `etcd.endpoints[]`: etcd cluster URLs.
- `s3`: `{endpoint, region, bucket, access_key, secret_key, use_path_style}`. `endpoint` empty means AWS default. `use_path_style=true` for MinIO.
- `ssh_key_path`: private key the daemon uses to connect to endpoints. Optional if every endpoint provides its own `ssh_key`.

## Build

```
go build ./...
```

Produces a single `gorn` binary that holds all subcommands. Deploy the same binary to HA nodes (for `serve` / `ignite`) and to workers (for `wrap` — must be in the endpoint user's `PATH`).

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
