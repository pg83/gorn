#!/bin/bash
set -u

if [ $# -eq 0 ]; then
    echo "usage: $0 host [host ...]" >&2
    exit 2
fi

BIN="$(cd "$(dirname "$0")" && pwd)/gorn"

if [ ! -x "$BIN" ]; then
    echo "no executable gorn at $BIN" >&2
    exit 2
fi

SSH_OPTS=(-o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new)

for host in "$@"; do
    echo "=== $host ==="

    if ! ssh "${SSH_OPTS[@]}" "$host" 'cat > gorn.new && chmod +x gorn.new && mv gorn.new /bin/gorn && (pkill -x gorn || true)' < "$BIN"; then
        echo "  install failed" >&2
        continue
    fi

    echo "  ok"
done
