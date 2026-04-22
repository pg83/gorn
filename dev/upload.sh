#!/bin/bash
set -u

if [ $# -eq 0 ]; then
    echo "usage: $0 host [host ...]" >&2
    exit 2
fi

BIN="${PWD}/gorn"

if [ ! -x "$BIN" ]; then
    echo "no executable gorn at $BIN" >&2
    exit 2
fi

SSH_OPTS=(-o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new)

INSTALL_CMD='cat > gorn.new && chmod +x gorn.new && mv gorn.new /bin/gorn && (pkill -x gorn || true)'

# Truncate daemon/control/worker log files after install so each redeploy starts fresh.
# Guard each with [ -f ] so missing files are silently skipped (no shell redirection errors).
TRUNCATE_CMD='
for p in /var/run/gorn/std/current /var/run/gorn_ctl/std/current; do
    [ -f "$p" ] && : > "$p"
done
for x in 0 1 2 3 4; do
    f="/var/run/gorn_${x}/std/current"
    [ -f "$f" ] && : > "$f"
    f="/var/run/gorn_${x}/std/home/gorn-wrap.log"
    [ -f "$f" ] && : > "$f"
done
true
'

for host in "$@"; do
    echo "=== $host ==="

    if ! ssh "${SSH_OPTS[@]}" "$host" "$INSTALL_CMD; $TRUNCATE_CMD" < "$BIN"; then
        echo "  install failed" >&2
        continue
    fi

    echo "  ok"
done
