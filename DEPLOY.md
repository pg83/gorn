# Deploying gorn

This guide walks through bringing up a small gorn install on a single Linux
host with N SSH-reachable workers. It is intentionally generic — no specific
distro, init system, or supervisor is assumed. For homelab/lab-style multi-host
HA see [`README.md`](README.md); the design supports it but this document does
not.

A companion script lives at [`dev/deploy.py`](dev/deploy.py): it reads a small
JSON deploy spec, pushes the `gorn` binary to all workers, materialises the
`config.json` that `gorn` itself consumes, and runs `serve` / `control` / `web`
(and optionally `prom`) as foreground subprocesses.

## Topology

```
                this host (master)
   ┌─────────────────────────────────────────────┐
   │  gorn serve     ── leader, dispatcher       │
   │  gorn control   ── HTTP API (ignite/web)    │
   │  gorn web       ── HTML dashboard           │
   │  gorn prom      ── /metrics (optional)      │
   └──────┬──────────────────┬───────────────────┘
          │                  │
   ssh ──►│                  │◄── http
   worker │                  │  ignite/web/curl
          ▼                  ▼
   ┌──────────────┐   ┌──────────────┐
   │  peer #1     │   │  peer #N     │
   │  user@host   │ … │  user@host   │
   │  /path/to/wd │   │  /path/to/wd │
   │  gorn wrap   │   │  gorn wrap   │
   └──────┬───────┘   └──────┬───────┘
          │                  │
          ▼                  ▼
            ┌─────────────────┐
            │ etcd  (queue)   │  external
            │ S3    (results) │  external
            └─────────────────┘
```

Master and workers are completely separate. The master only opens outbound
SSH to workers; workers never call back. State lives in etcd (queue) and S3
(stdout, stderr, `result.json`) — `gorn` itself is stateless.

## Prerequisites

### On the master host

- Linux, glibc or musl. Tested with stock distro userland.
- Go 1.22+ to build the binary, or a prebuilt `gorn` binary copied in.
- `ssh` client in `$PATH`. `gorn serve` shells out to `/usr/bin/ssh`
  (or `/bin/ssh`, `/usr/local/bin/ssh`, in that order) — `golang.org/x/crypto/ssh`
  is **not** used, by design. Process isolation lets a stuck dispatch be killed
  on the worker without touching the daemon.
- Network reach to etcd and S3.
- Python 3.9+ if you intend to use `dev/deploy.py`.

### Worker peers (each)

- A Unix user dedicated to gorn (one per endpoint slot — see below).
- Writable working directory (`endpoint.path` in the config); `gorn wrap`
  runs there.
- The `gorn` binary itself, reachable via the user's `$PATH` over SSH. The
  simplest layout is `~/gorn` in the user's home, since `gorn wrap` is
  invoked as a bare command and the SSH login shell's PATH usually includes
  `$HOME`. `/usr/local/bin/gorn` works too.
- **Unprivileged user namespaces enabled.** `gorn wrap` re-execs through
  `unshare -r -U -m` to put the task's cwd behind a per-run tmpfs in its own
  user+mount namespace. On Debian-family distros check
  `sysctl kernel.unprivileged_userns_clone` (must be `1`). On RHEL-family,
  `sysctl user.max_user_namespaces` should be > 0. If the worker is a
  container, the host must allow this for unprivileged users.
- `/bin/unshare` (util-linux) and a POSIX `sh`. Nothing else.
- SSH access from the master to `user@host[:port]` using a key. Password auth
  is disabled in the dispatcher (`BatchMode=yes`).

### etcd

- Any etcd 3.x cluster reachable from the master. A single-node etcd is
  fine for a non-HA install.
- The dispatcher does many `Watch`-based reads but only puts/deletes on
  outcome — load is small. A tmpfs/memory etcd is the typical homelab choice
  because the queue is reconstructible from S3 idempotency anyway.

### S3-compatible object storage

- Any S3 endpoint reachable from both the master and the workers. MinIO is
  the usual pick. AWS S3 itself works.
- One bucket. `gorn` does **not** create the bucket — pre-create it.
- Credentials with `s3:GetObject`, `s3:PutObject`, `s3:HeadObject`,
  `s3:ListBucket` (the last only for human debugging; not used by the
  daemon). Workers need the same credentials, because each `gorn wrap`
  invocation talks to S3 directly — the daemon does **not** proxy.

## Build

```sh
git clone git@github.com:pg83/gorn.git
cd gorn
go build ./...
```

Produces a single `./gorn` binary. Run `go test ./...` for the unit tests
(pure logic — no etcd/S3/SSH required).

The same binary is used everywhere: master, workers, and `ignite` clients.
On workers it's invoked as `gorn wrap`; on the master, `gorn serve` /
`gorn control` / `gorn web` / `gorn prom`.

## Subcommands

One binary, eight subcommands. The ones that are servers:

| Subcommand | Where it runs | What it does | Config blocks read |
|------------|---------------|--------------|--------------------|
| `serve`    | master | Campaigns for leadership, dispatches queued tasks over SSH | `endpoints`, `hosts`, `etcd`, `s3`, `ssh_key_path` |
| `control`  | master | HTTP JSON API in front of etcd + S3 (enqueue / list / state / output) | `endpoints` (for `/v1/endpoints`), `etcd`, `s3`, `control.listen` |
| `web`      | master | Read-only Bootstrap dashboard, polls `control` over HTTP | `web.api`, `web.listen` |
| `prom`     | master | Prometheus `/metrics` (queue depth, oldest age, endpoint count) | `etcd`, `endpoints`, `prom.listen` |
| `wrap`     | worker | Invoked via SSH by `serve`; reads task context from stdin, runs the script in a user+mount ns, uploads outputs to S3 | reads stdin JSON, not config |
| `wrap_lower` | worker | Internal helper: chdir + tmpfs mount inside the new ns, then execs the script | — |
| `ignite`   | anywhere | Thin HTTP client to `control` (enqueue + optional `--wait`) | `--api` URL or `$GORN_API` |
| `exec`     | anywhere | Debug helper: same env-resolution as `wrap` would do | — |

All four servers take `--config <path>` (JSON). The same JSON file is fine
for all of them — each reads the blocks it needs and ignores the rest.

### `serve`

```
gorn serve --config /etc/gorn/config.json
```

- Connects to etcd, calls `concurrency.NewElection`, blocks on `Campaign`.
  When elected, starts the per-endpoint dispatcher loop.
- On any error in dispatch, OR on session expiry from etcd, OR on SIGINT/SIGTERM,
  the daemon exits non-zero. The expected pattern is to run it under a
  supervisor that restarts on exit (`Restart=always` in systemd). On
  leadership loss it just exits — restart and re-campaign.
- For a non-HA install you can still run it standalone; election succeeds
  instantly because there's no competitor.

### `control`

```
gorn control --config /etc/gorn/config.json
```

HTTP API on `control.listen`. Stateless — enqueue is an etcd
`CreateRevision==0` txn, so multiple `control` instances behind a load
balancer work fine if you want it. Endpoints:

- `POST /v1/tasks` — body `{guid?, script, env?, descr?, root?, slots?, retry_on_error?}`. Returns `{guid}`. 409 if guid already queued.
- `GET /v1/tasks` — `{tasks: [{guid, env, descr, slots, enqueued_at, create_revision}]}`. FIFO order by `create_revision`.
- `GET /v1/tasks/<guid>?root=<r>` — `{guid, state}` where `state ∈ {queued, done, not_found}`. `done` means the etcd key is gone AND `<r>/<guid>/result.json` exists in S3.
- `GET /v1/tasks/<guid>/queued` — `{guid, queued}`. Cheapest possible probe: one etcd existence check, no S3 fallback. For `dedup`-style callers that only need to know whether to fire a fresh task. `root` is **not** required here.
- `GET /v1/tasks/<guid>/output?root=<r>` — `{result, stdout_b64, stderr_b64}`. 404 until `result.json` lands.
- `GET /v1/tasks/<guid>/content/<name>?root=<r>` — passthrough GET of `<r>/<guid>/<name>` from S3 (e.g. `result.zstd` for molot-style produced artifacts).
- `GET /v1/endpoints` — `{endpoints: [{host, port, user, path}]}`. Static config dump.

`?root=` is **mandatory** for the state/output/content endpoints. It's the
S3 key prefix the task wrote under; without it `control` would have to read
the full Task body from etcd just to learn where `result.json` lives, which
on `ignite --wait` polls means shipping the whole script on every tick.

### `web`

```
gorn web --config /etc/gorn/config.json
```

HTML at `web.listen`. Refreshes every 2s, renders `/v1/endpoints` and
`/v1/tasks` as Bootstrap tables. Read-only; talks to `control` over HTTP
(`web.api`), never to etcd or S3 directly.

### `prom`

```
gorn prom --config /etc/gorn/config.json
```

Prometheus exposition at `prom.listen` on `/metrics`. Metrics:

- `gorn_up` — gauge, always 1.
- `gorn_endpoint_count` — number of configured endpoints.
- `gorn_queue_depth` — current queue length.
- `gorn_queue_oldest_age_seconds` — age of the oldest queued task; absent when the queue is empty.

Each scrape issues one etcd `Range` over the queue prefix — fine, the set is
small (queue depth grows with backlog, not with fleet size).

## Configuration

Single JSON file. See [`dev/config.example.json`](dev/config.example.json)
for a starting point and the table below for the full schema.

```jsonc
{
  "endpoints": [                       // worker fleet
    {
      "host": "n1.lan",                // required: hostname or IP
      "port": 22,                      // optional: SSH port (default 22)
      "user": "gorn-w1",               // required: SSH user; one task at a time per user
      "path": "/srv/gorn/w1",          // required: cwd on the worker; gorn wrap chdirs here
      "ssh_key": "-----BEGIN…",        // optional: per-endpoint key body (PEM)
      "log_path": "/srv/gorn/w1/.log"  // optional: append-only diagnostic log on the worker
    }
  ],
  "hosts": {                           // required: per-host knobs
    "n1.lan": { "cpus_per_slot": 2 }   // CPUs scheduled per slot for MOLOT_CPUS env injection
  },
  "cpu_overcommit": 1.25,              // optional: multiplier on cpus_per_slot when computing MOLOT_CPUS (default 1.25)

  "etcd": {
    "endpoints": ["http://etcd:2379"]  // host:port or scheme://host:port; v3 client takes either
  },

  "s3": {
    "endpoint": "http://minio:9000",   // empty for AWS default
    "region": "us-east-1",
    "bucket": "gorn",
    "access_key": "…",
    "secret_key": "…",
    "use_path_style": true             // true for MinIO; false for AWS
  },

  "ssh_key_path": "/etc/gorn/ssh_key", // private key the daemon uses for SSH; per-endpoint ssh_key overrides

  "control": { "listen": "127.0.0.1:7878" },
  "web":     { "api": "http://127.0.0.1:7878", "listen": "127.0.0.1:7979" },
  "prom":    { "listen": "127.0.0.1:7280" }
}
```

### Required blocks per subcommand

| Block         | serve | control | web | prom |
|---------------|:-----:|:-------:|:---:|:----:|
| `endpoints`   |  ✓    |   ✓     |     |  ✓   |
| `hosts`       |  ✓    |         |     |      |
| `etcd`        |  ✓    |   ✓     |     |  ✓   |
| `s3`          |  ✓    |   ✓     |     |      |
| `ssh_key_path` or per-endpoint `ssh_key` | ✓ | | | |
| `control.listen` |    |   ✓     |     |      |
| `web.api`     |       |         |  ✓  |      |
| `web.listen`  |       |         |  ✓  |      |
| `prom.listen` |       |         |     |  ✓   |

### `${VAR}` expansion

The raw config text is passed through a substitution pass before JSON
parsing. Every `${NAME}` is replaced with `os.Getenv("NAME")`. Pattern is
`\$\{[A-Za-z_][A-Za-z0-9_]*\}` — no default-value syntax, no `$NAME` without
braces. Unset variable → loud error.

```jsonc
{
  "s3": {
    "endpoint": "${MINIO_URL}",
    "access_key": "${MINIO_ACCESS_KEY}",
    "secret_key": "${MINIO_SECRET_KEY}"
  }
}
```

### Env overlays

After JSON parse, three env vars override config when set (env wins):

- `ETCDCTL_ENDPOINTS` — comma-separated, replaces `etcd.endpoints[]`.
- `AWS_ACCESS_KEY_ID` — overrides `s3.access_key`.
- `AWS_SECRET_ACCESS_KEY` — overrides `s3.secret_key`.

These are a convenience for deployments that already inject creds via AWS or
etcdctl env conventions. The `${VAR}` mechanism is more general; pick whichever.

### SSH keys

- `ssh_key_path` is a path on the master to a PEM private key. Read at
  startup; passed to `ssh -i`. Standard OpenSSH key formats work.
- Per-endpoint `ssh_key` (PEM body, not path) overrides it. The body is
  loaded into an anonymous `memfd_create` file fchmod'd `0600` and kept open
  by the daemon. `ssh` reads it via `-i /proc/<gorn-pid>/fd/<N>` — same-UID
  access is enough; key material never touches the filesystem.
- The public half goes into the worker user's `~/.ssh/authorized_keys`. The
  daemon does not push it; sysadmin job.

## End-to-end setup walkthrough

1. **Pre-create the S3 bucket** (`mc mb minio/gorn`, or `aws s3 mb …`).
2. **Pre-provision worker users**, one per endpoint slot. For each:
   - `useradd` with a home directory.
   - `mkdir -p` and `chown` the endpoint `path`.
   - Drop your master's public key into `~/.ssh/authorized_keys`.
   - Verify unprivileged user namespaces work: `sudo -u <user> unshare -r -U -m true` must return 0.
3. **Push the `gorn` binary** to each worker user's home (or `/usr/local/bin/`). It must be on the SSH login shell's `$PATH`. `dev/deploy.py push` automates this.
4. **Write a `config.json`** matching your topology. `dev/deploy.py config` can emit one from a smaller deploy spec.
5. **Bring up the four servers** on the master:
   - `gorn control --config /path/to/config.json`
   - `gorn serve --config /path/to/config.json`
   - `gorn web --config /path/to/config.json`
   - `gorn prom --config /path/to/config.json` (optional)
   Under systemd, one unit each with `Restart=always`. For dev, `dev/deploy.py up` runs them as foreground subprocesses with logs in `./run/`.
6. **Smoke test** from a client (anywhere with network reach to `control`):
   ```sh
   echo 'echo hello from $(hostname); date -u +%FT%TZ' \
     | gorn ignite --api http://master:7878 --wait
   ```
   Should print the worker's hostname and exit 0.

## S3 layout

```
<bucket>/
  <root>/                              # "gorn" if --root unset on ignite
    <guid>/
      result.json                      # WrapResult: exit_code, host, user, duration_sec, …
      stdout                           # raw bytes, never truncated
      stderr                           # raw bytes, never truncated
      retry-<exit>.json                # only on retry-on-error matches
```

`HEAD <bucket>/<root>/<guid>/result.json` returning 200 is the idempotency
signal — `gorn wrap` short-circuits with `outcome=already-done` and the
leader treats that as success. If you ever need to force a re-run, delete
that one object.

## etcd layout

```
/gorn/queue_v3/<lex-sortable-id>       # Task JSON; create_revision is the FIFO key
/gorn/leader/<lease-id>                # held by the elected serve instance
```

The `_v3` suffix is bumped whenever the Task JSON schema changes
incompatibly. Old keys are left in place; nothing reads them.

## Troubleshooting

- **`no finish message` in dispatcher logs** → the SSH session returned but
  `gorn wrap` never printed the final JSON line. Check the worker's
  `log_path` (if set) and the dispatcher's own stderr for the captured
  stderr block. Usual causes: `gorn` binary not on the worker user's PATH,
  unprivileged user namespaces disabled (`unshare` fails), endpoint `path`
  not writable.
- **`already-done` for a task that hasn't finished** → there's a stale
  `result.json` in S3 from a previous (possibly failed) run with the same
  GUID. `gorn` treats `result.json` presence as authoritative by design. Delete
  the object to force re-execution.
- **Leader exits immediately on startup** → look for `session done — cancelling dispatcher` in stderr.
  etcd unreachable, session lease expired, or a panic inside the dispatcher
  (look further down the log for the trace). The serve loop intentionally
  does not auto-restart; rely on systemd / supervisor.
- **`unschedulable: slots=N > max host capacity=M`** from `control` on enqueue →
  the task asked for more slots than the largest single host has endpoints.
  Either lower the task's slot count or add more endpoints to a host.
- **Tasks queue but never dispatch** → no leader. Check that exactly one
  `gorn serve` is up and reaches etcd. `/v1/endpoints` lists endpoints but
  doesn't prove dispatch; `prom`'s `gorn_queue_oldest_age_seconds` rising
  unbounded is the signal.
- **`ssh: executable not found`** from the daemon → install `openssh-client`
  or symlink ssh into `/usr/bin`, `/bin`, or `/usr/local/bin`. `gorn` does
  not bundle ssh.

## What this guide does not cover

- Multi-master HA (run `serve` on multiple hosts pointing at the same
  etcd — leader election handles the rest). The config is identical on every
  master.
- Host-key verification. `gorn serve` currently uses
  `StrictHostKeyChecking=accept-new`, which TOFU-pins on first connect.
- TLS for the `control` / `web` HTTP listeners. Run them on `127.0.0.1` and
  front with a reverse proxy if you need TLS or auth.
- Log streaming. `gorn wrap` does one PUT at the end — partial output is not
  visible until the task finishes.
