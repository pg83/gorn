#!/usr/bin/env python3
"""
deploy.py — orchestrate a single-host gorn master + N ssh worker peers.

Reads a small JSON deploy spec (--spec), materialises the gorn config.json
that `gorn` itself consumes, pushes the gorn binary to each peer over SSH,
and runs `gorn serve` / `control` / `web` (and optionally `prom`) on this
host as foreground subprocesses.

External requirements (deploy.py does NOT bring these up):
  - etcd v3 cluster reachable from this host
  - S3-compatible endpoint reachable from both this host and the peers,
    with the target bucket already created
  - SSH key + authorized_keys already in place on each peer

Subcommands:
  push    upload ./gorn to every peer's remote path
  config  emit the gorn JSON config (no daemons started)
  check   verify etcd / S3 / SSH reachability
  up      push + config + run serve/control/web (+prom) in foreground

See gorn/DEPLOY.md for the full setup.
"""

import argparse
import json
import os
import signal
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path


def die(msg, code=1):
    print(f"deploy.py: {msg}", file=sys.stderr)
    sys.exit(code)


def load_spec(path):
    raw = Path(path).read_text()

    try:
        spec = json.loads(raw)
    except json.JSONDecodeError as e:
        die(f"invalid JSON in {path}: {e}")

    for k in ("endpoints", "etcd", "s3", "ssh_key_path", "gorn_bin"):
        if k not in spec:
            die(f"{path}: missing required key {k!r}")

    if not isinstance(spec["endpoints"], list) or not spec["endpoints"]:
        die(f"{path}: endpoints[] must be a non-empty list")

    for i, ep in enumerate(spec["endpoints"]):
        for k in ("host", "user", "path"):
            if k not in ep:
                die(f"{path}: endpoints[{i}] missing {k!r}")

    return spec


def gorn_config_from_spec(spec):
    eps = []

    for ep in spec["endpoints"]:
        e = {"host": ep["host"], "user": ep["user"], "path": ep["path"]}

        if "port" in ep:
            e["port"] = ep["port"]

        if "log_path" in ep:
            e["log_path"] = ep["log_path"]

        if "ssh_key" in ep:
            e["ssh_key"] = ep["ssh_key"]

        eps.append(e)

    cpus_per_slot = spec.get("cpus_per_slot", 1)
    hosts = {ep["host"]: {"cpus_per_slot": cpus_per_slot} for ep in eps}

    cfg = {
        "endpoints": eps,
        "hosts": hosts,
        "etcd": spec["etcd"],
        "s3": spec["s3"],
        "ssh_key_path": spec["ssh_key_path"],
    }

    if "cpu_overcommit" in spec:
        cfg["cpu_overcommit"] = spec["cpu_overcommit"]

    if "control" in spec:
        cfg["control"] = spec["control"]

    if "web" in spec:
        cfg["web"] = spec["web"]

    if "prom" in spec:
        cfg["prom"] = spec["prom"]

    if "remote_wrap_path" in spec:
        cfg["remote_wrap_path"] = spec["remote_wrap_path"]

    return cfg


def ssh_opts(spec, peer):
    port = peer.get("port", 22)

    return [
        "-i", spec["ssh_key_path"],
        "-p", str(port),
        "-o", "BatchMode=yes",
        "-o", "StrictHostKeyChecking=accept-new",
        "-o", "ConnectTimeout=15",
    ]


def unique_peers(endpoints):
    seen = set()
    out = []

    for ep in endpoints:
        key = (ep["host"], ep["user"], ep.get("port", 22))

        if key in seen:
            continue

        seen.add(key)
        out.append(ep)

    return out


def push_one(spec, peer, gorn_bin, remote_path):
    target = f'{peer["user"]}@{peer["host"]}'

    print(f">>> {target}: uploading {gorn_bin} -> {remote_path}")

    with open(gorn_bin, "rb") as f:
        subprocess.run(
            ["ssh", *ssh_opts(spec, peer), target,
             f"set -e; mkdir -p $(dirname {remote_path}); "
             f"install -m 0755 /dev/stdin {remote_path}"],
            check=True, stdin=f,
        )

    r = subprocess.run(
        ["ssh", *ssh_opts(spec, peer), target,
         f"{remote_path} 2>&1 | head -1"],
        capture_output=True, text=True,
    )

    print(f"    {target}: gorn says: {r.stdout.strip() or r.stderr.strip()}")


def cmd_push(args):
    spec = load_spec(args.spec)
    gorn_bin = spec["gorn_bin"]

    if not (os.path.isfile(gorn_bin) and os.access(gorn_bin, os.X_OK)):
        die(f"gorn_bin {gorn_bin!r} not found or not executable — run `go build` in the gorn repo")

    remote_path = spec.get("remote_gorn_path", "/usr/local/bin/gorn")

    for peer in unique_peers(spec["endpoints"]):
        push_one(spec, peer, gorn_bin, remote_path)


def cmd_config(args):
    spec = load_spec(args.spec)
    cfg = gorn_config_from_spec(spec)
    body = json.dumps(cfg, indent=2) + "\n"

    if args.out == "-":
        sys.stdout.write(body)
    else:
        Path(args.out).write_text(body)
        print(f">>> wrote {args.out}")


def http_probe(url, timeout=5):
    try:
        with urllib.request.urlopen(url, timeout=timeout) as resp:
            return True, f"HTTP {resp.status}"
    except urllib.error.HTTPError as e:
        return True, f"HTTP {e.code}"
    except Exception as e:
        return False, str(e)


def etcd_health_url(ep):
    if "://" not in ep:
        ep = "http://" + ep

    return ep.rstrip("/") + "/health"


def cmd_check(args):
    spec = load_spec(args.spec)
    ok = True

    for ep in spec["etcd"]["endpoints"]:
        good, msg = http_probe(etcd_health_url(ep))
        print(f"{'OK  ' if good else 'FAIL'} etcd {ep}: {msg}")
        ok = ok and good

    s3_ep = spec["s3"]["endpoint"]

    if s3_ep:
        good, msg = http_probe(s3_ep.rstrip("/") + "/")
        print(f"{'OK  ' if good else 'FAIL'} s3   {s3_ep}: {msg}")
        ok = ok and good
    else:
        print("OK   s3   (empty endpoint = AWS default)")

    for peer in unique_peers(spec["endpoints"]):
        target = f'{peer["user"]}@{peer["host"]}'
        remote_path = spec.get("remote_gorn_path", "/usr/local/bin/gorn")

        r = subprocess.run(
            ["ssh", *ssh_opts(spec, peer), target,
             f"set -e; test -x {remote_path} && {remote_path} 2>&1 | head -1; "
             f"test -w {peer['path']} || (echo 'path not writable: {peer['path']}' >&2; exit 1); "
             f"unshare -r -U -m true"],
            capture_output=True, text=True,
        )

        if r.returncode == 0:
            print(f"OK   ssh  {target}: {r.stdout.strip()}")
        else:
            print(f"FAIL ssh  {target}: rc={r.returncode} stderr={r.stderr.strip()}")
            ok = False

    sys.exit(0 if ok else 1)


def cmd_up(args):
    spec = load_spec(args.spec)
    gorn_bin = spec["gorn_bin"]

    if not (os.path.isfile(gorn_bin) and os.access(gorn_bin, os.X_OK)):
        die(f"gorn_bin {gorn_bin!r} not found or not executable — run `go build` in the gorn repo")

    if not args.skip_push:
        remote_path = spec.get("remote_gorn_path", "/usr/local/bin/gorn")

        for peer in unique_peers(spec["endpoints"]):
            push_one(spec, peer, gorn_bin, remote_path)

    run_dir = Path(spec.get("run_dir", "./run")).resolve()
    run_dir.mkdir(parents=True, exist_ok=True)

    cfg = gorn_config_from_spec(spec)
    cfg_path = run_dir / "config.json"
    cfg_path.write_text(json.dumps(cfg, indent=2) + "\n")
    print(f">>> wrote {cfg_path}")

    daemons = ["serve"]

    if "control" in cfg:
        daemons.append("control")

    if "web" in cfg:
        daemons.append("web")

    if "prom" in cfg:
        daemons.append("prom")

    procs = {}
    log_files = {}
    env = os.environ.copy()

    try:
        for d in daemons:
            log_path = run_dir / f"{d}.log"
            lf = open(log_path, "ab", buffering=0)
            log_files[d] = lf

            print(f">>> starting gorn {d} (logs: {log_path})")

            p = subprocess.Popen(
                [gorn_bin, d, "--config", str(cfg_path)],
                stdout=lf, stderr=subprocess.STDOUT, env=env,
                start_new_session=True,
            )
            procs[d] = p
            time.sleep(0.3)

            rc = p.poll()

            if rc is not None:
                _drain(log_files)
                die(f"gorn {d} exited immediately with rc={rc}; see {log_path}")

        print()
        print(">>> servers up:")

        if "control" in cfg:
            print(f"    control api: http://{cfg['control']['listen']}")

        if "web" in cfg:
            print(f"    web ui:      http://{cfg['web']['listen']}")

        if "prom" in cfg:
            print(f"    prom:        http://{cfg['prom']['listen']}/metrics")

        print(">>> Ctrl-C to stop")
        print()

        _wait_loop(procs)

    except KeyboardInterrupt:
        print("\n>>> SIGINT received; tearing down", file=sys.stderr)
    finally:
        _teardown(procs, log_files)


def _wait_loop(procs):
    while True:
        for d, p in procs.items():
            rc = p.poll()

            if rc is not None:
                print(f"!!! gorn {d} exited with rc={rc}; tearing down", file=sys.stderr)

                return

        time.sleep(1)


def _teardown(procs, log_files):
    for d, p in procs.items():
        if p.poll() is None:
            print(f"    terminating gorn {d} (pid={p.pid})", file=sys.stderr)
            p.terminate()

    deadline = time.time() + 10

    for d, p in procs.items():
        timeout = max(0.0, deadline - time.time())

        try:
            p.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            print(f"    killing gorn {d} (pid={p.pid})", file=sys.stderr)
            p.kill()
            p.wait()

    _drain(log_files)


def _drain(log_files):
    for lf in log_files.values():
        try:
            lf.close()
        except Exception:
            pass


def main():
    ap = argparse.ArgumentParser(
        description="orchestrate a single-host gorn master + N ssh worker peers",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=__doc__,
    )
    sub = ap.add_subparsers(dest="cmd", required=True)

    p = sub.add_parser("push", help="upload gorn binary to all peers")
    p.add_argument("--spec", required=True, help="path to deploy spec JSON")
    p.set_defaults(fn=cmd_push)

    p = sub.add_parser("config", help="emit the gorn JSON config (no daemons started)")
    p.add_argument("--spec", required=True)
    p.add_argument("--out", default="-", help="output path; '-' for stdout (default)")
    p.set_defaults(fn=cmd_config)

    p = sub.add_parser("check", help="verify etcd / S3 / SSH reachability")
    p.add_argument("--spec", required=True)
    p.set_defaults(fn=cmd_check)

    p = sub.add_parser("up", help="push + config + run serve/control/web/prom in foreground")
    p.add_argument("--spec", required=True)
    p.add_argument("--skip-push", action="store_true", help="skip binary push (assume peers already have it)")
    p.set_defaults(fn=cmd_up)

    args = ap.parse_args()
    args.fn(args)


if __name__ == "__main__":
    main()
