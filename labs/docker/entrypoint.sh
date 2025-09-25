#!/bin/sh
set -eu

ROLE="${1:-node}"            # "seed" or "node"
PORT="${PORT:-9000}"
BOOTSTRAP_HOST="${BOOTSTRAP_HOST:-}"

IP="$(getent hosts "$HOSTNAME" | awk '{print $1}' | head -n1)"
[ -z "${IP:-}" ] && IP="$(hostname -i | awk '{print $1}')"

ADDR="${IP}:${PORT}"

if [ "$ROLE" = "seed" ]; then
  echo "[entrypoint] role=seed addr=$ADDR"
  exec /app/kcli -addr "$ADDR"
else
  [ -n "$BOOTSTRAP_HOST" ] || { echo "BOOTSTRAP_HOST must be set for node role" >&2; exit 1; }
  BOOTSTRAP="${BOOTSTRAP_HOST}:${PORT}"
  until getent hosts "$BOOTSTRAP_HOST" >/dev/null 2>&1; do
    echo "[entrypoint] waiting for bootstrap host $BOOTSTRAP_HOST ..."
    sleep 0.5
  done
  echo "[entrypoint] role=node addr=$ADDR bootstrap=$BOOTSTRAP"
  exec /app/kcli -addr "$ADDR" -bootstrap "$BOOTSTRAP"
fi
