#!/usr/bin/env python3
"""
Smoke test: copy ./gorn to the remote via ssh (cat > gorn), enqueue one
trivial task, and run `gorn serve` locally.

Usage:
    ./smoke.py <user@host> <port>

Required env:
    ETCDCTL_ENDPOINTS   comma-separated, e.g. http://etcd:2379
    MC_HOST_minio       http(s)://ACCESS:SECRET@host:port

Optional env:
    GORN_SSH_KEY        private key (default: ~/.ssh/id_ed25519)
    GORN_BUCKET         S3 bucket (default: gorn)
    GORN_REGION         S3 region (default: us-east-1)

The binary is dropped into the ssh login directory (remote $HOME) and
endpoint.path is set to the same directory.
"""

import argparse
import json
import os
import re
import subprocess
import sys
import tempfile
import uuid
from pathlib import Path


def die(msg, code=1):
    print(msg, file=sys.stderr)
    sys.exit(code)


def require_env(name, hint=""):
    v = os.environ.get(name)

    if not v:
        die(f"need {name}" + (f" ({hint})" if hint else ""))

    return v


def parse_mc_host(s):
    m = re.match(r"^(https?)://([^:@]+):([^@]+)@(.+)$", s)

    if not m:
        die(f"MC_HOST_minio must be http(s)://ACCESS:SECRET@host:port, got: {s}")

    return m.group(1), m.group(2), m.group(3), m.group(4)


def ssh_base(key, port):
    return [
        "-i", str(key),
        "-p", str(port),
        "-o", "BatchMode=yes",
        "-o", "StrictHostKeyChecking=accept-new",
    ]


def ssh_capture(userhost, ssh_opts, remote_cmd):
    r = subprocess.run(
        ["ssh", *ssh_opts, userhost, remote_cmd],
        check=True, capture_output=True, text=True,
    )

    return r.stdout.strip()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("userhost", help="user@host")
    ap.add_argument("port", type=int)
    args = ap.parse_args()

    if "@" not in args.userhost:
        die("first argument must look like user@host")

    user, host = args.userhost.split("@", 1)

    etcd = [e.strip() for e in require_env("ETCDCTL_ENDPOINTS", "comma-separated").split(",") if e.strip()]

    scheme, ak, sk, s3_hostport = parse_mc_host(require_env("MC_HOST_minio", "http(s)://ACCESS:SECRET@host:port"))
    s3_endpoint = f"{scheme}://{s3_hostport}"

    key = Path(os.environ.get("GORN_SSH_KEY", Path.home() / ".ssh" / "id_ed25519"))

    if not key.is_file():
        die(f"ssh key not found: {key} (set GORN_SSH_KEY)")

    bucket = os.environ.get("GORN_BUCKET", "gorn")
    region = os.environ.get("GORN_REGION", "us-east-1")

    repo = Path(__file__).resolve().parent
    gorn_bin = repo / "gorn"

    if not (gorn_bin.is_file() and os.access(gorn_bin, os.X_OK)):
        die(f"{gorn_bin} not found or not executable — build it first")

    ssh_opts = ssh_base(key, args.port)

    remote_dir = ssh_capture(args.userhost, ssh_opts, "pwd")

    if not remote_dir:
        die("could not resolve remote login directory")

    print(f">>> uploading {gorn_bin} to {args.userhost}:{remote_dir}/gorn via ssh cat")
    with gorn_bin.open("rb") as f:
        subprocess.run(
            ["ssh", *ssh_opts, args.userhost, "cat > gorn && chmod +x gorn"],
            check=True, stdin=f,
        )

    workdir_local = Path(tempfile.mkdtemp(prefix="gorn-smoke-"))
    cfg_path = workdir_local / "config.json"

    config = {
        "endpoints": [
            {"host": host, "port": args.port, "user": user, "path": remote_dir},
        ],
        "etcd": {"endpoints": etcd},
        "s3": {
            "endpoint": s3_endpoint,
            "region": region,
            "bucket": bucket,
            "access_key": ak,
            "secret_key": sk,
            "use_path_style": True,
        },
        "ssh_key_path": str(key),
    }

    cfg_path.write_text(json.dumps(config, indent=2) + "\n")

    print(">>> config:")
    print(cfg_path.read_text())

    mc_bin = os.environ.get("GORN_MC", "minio-client")

    print(f">>> ensuring bucket minio/{bucket} exists ({mc_bin} mb; errors are fine if it already exists)")
    try:
        subprocess.run([mc_bin, "mb", f"minio/{bucket}"], check=False)
    except FileNotFoundError:
        print(f"WARNING: '{mc_bin}' not found in PATH — set GORN_MC, or create bucket '{bucket}' manually", file=sys.stderr)

    guid = str(uuid.uuid4())

    print(f">>> enqueueing task {guid}")
    subprocess.run(
        [
            str(gorn_bin), "ignite",
            "--etcd-endpoints", ",".join(etcd),
            "--guid", guid,
            "--",
            "sh", "-c",
            'echo "hello from $(id -un) on $(hostname) at $(date -u +%FT%TZ)"; sleep 2; echo done',
        ],
        check=True,
    )

    print(f">>> task in etcd — starting 'gorn serve' (Ctrl-C to stop)")
    print(f"    result JSON will appear at s3://{bucket}/gorn/{guid}/result.json")
    print()

    os.execv(str(gorn_bin), [str(gorn_bin), "serve", "--config", str(cfg_path)])


if __name__ == "__main__":
    main()
