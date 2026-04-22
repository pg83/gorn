#!/bin/bash
set -u

if [ $# -eq 0 ]; then
    echo "usage: $0 host [host ...]" >&2
    echo "tails gorn serve/control/worker logs on each host in parallel" >&2
    exit 2
fi

HOSTS=("$@")

PATHS=(
    /var/run/gorn/std/current
    /var/run/gorn_ctl/std/current
)

for x in 0 1 2 3 4; do
    PATHS+=("/var/run/gorn_${x}/std/current")
    PATHS+=("/var/run/gorn_${x}/std/home/.gorn-wrap.log")
done

SSH_OPTS=(-o BatchMode=yes -o ConnectTimeout=10 -o ServerAliveInterval=30 -o StrictHostKeyChecking=accept-new)

# Build the remote tail command with properly shell-quoted paths.
paths_q=$(printf ' %q' "${PATHS[@]}")
remote_cmd="tail -F -n 20 --${paths_q} 2>/dev/null"

pids=()

cleanup() {
    for pid in "${pids[@]}"; do
        kill "$pid" 2>/dev/null || true
    done
    wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

for host in "${HOSTS[@]}"; do
    (
        ssh "${SSH_OPTS[@]}" "$host" "$remote_cmd" 2>&1 | sed -u "s#^#${host}\t#"
    ) &
    pids+=($!)
done

wait
