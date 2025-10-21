#!/bin/sh
set -eu

ROLE="${1:-node}"           # "seed" or "node"
PORT="${PORT:-9000}"
BOOTSTRAP_HOST="${BOOTSTRAP_HOST:-}"

# Figure out a routable IPv4 (not 127.0.0.1)
IP=""
if command -v getent >/dev/null 2>&1; then
  IP="$(getent hosts "$HOSTNAME" | awk '{print $1; exit}')"
fi
if [ -z "${IP:-}" ] || [ "$IP" = "127.0.0.1" ]; then
  if command -v ip >/dev/null 2>&1; then
    IP="$(ip -o -4 addr show scope global | awk '{print $4}' | cut -d/ -f1 | head -n1)"
  else
    IP="$(hostname -i | tr ' ' '\n' | grep -v '^127\.' | head -n1)"
  fi
fi

ADDR="${IP}:${PORT}"

if [ "$ROLE" = "seed" ]; then
  echo "[entrypoint] role=seed addr=$ADDR"
  exec /app/kcli -addr "$ADDR"
else
  : "${BOOTSTRAP_HOST:?set BOOTSTRAP_HOST for node role}"
  BOOTSTRAP="${BOOTSTRAP_HOST}:${PORT}"

  # Wait for bootstrap host to resolve
  until getent hosts "$BOOTSTRAP_HOST" >/dev/null 2>&1 || nslookup "$BOOTSTRAP_HOST" >/dev/null 2>&1; do
    echo "[entrypoint] waiting for bootstrap host $BOOTSTRAP_HOST ..."
    sleep 0.5
  done

  echo "[entrypoint] role=node addr=$ADDR bootstrap=$BOOTSTRAP"
  exec /app/kcli -addr "$ADDR" -bootstrap "$BOOTSTRAP"
fi
