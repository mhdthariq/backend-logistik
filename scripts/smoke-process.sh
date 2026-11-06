#!/usr/bin/env bash
# No database or application .env: verify the built process, restart and SIGTERM.
set -euo pipefail
cd "$(dirname "$0")/.."
binary="$PWD/bin/api-linux"
port=${SMOKE_PORT:-58081}
log=$(mktemp /tmp/novalogistik-process.XXXXXX)
pid=''
cleanup() {
  if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
    kill -KILL "$pid"
    wait "$pid" || true
  fi
  rm -f "$log"
}
trap cleanup EXIT
for attempt in 1 2; do
  env -i HOST=127.0.0.1 PORT="$port" SHUTDOWN_TIMEOUT=2s "$binary" > "$log" 2>&1 &
  pid=$!
  ready=false
  for ((i=0; i<50; i++)); do
    if ! kill -0 "$pid" 2>/dev/null; then cat "$log"; exit 1; fi
    if curl --max-time 1 -fsS "http://127.0.0.1:$port/health/live" > /dev/null 2>&1; then ready=true; break; fi
    sleep 0.1
  done
  [[ "$ready" == true ]]
  [[ $(curl --max-time 2 -fsS "http://127.0.0.1:$port/api") == '{"message":"Hello, world!"}' ]]
  kill -TERM "$pid"
  # Bound the smoke itself as well as the server's configured drain timeout.
  for ((i=0; i<50; i++)); do
    if ! kill -0 "$pid" 2>/dev/null; then break; fi
    sleep 0.1
  done
  if kill -0 "$pid" 2>/dev/null; then cat "$log"; exit 1; fi
  wait "$pid"
  pid=''
done
echo 'Process start/restart and SIGTERM smoke passed.'
