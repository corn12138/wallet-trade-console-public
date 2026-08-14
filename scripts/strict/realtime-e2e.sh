#!/usr/bin/env bash
# Strict zero-Nest realtime e2e runner (ADR 0006).
#
# Builds + starts the test-only realtime harness (services/api-go/test/realtimeserver)
# and runs the real socket.io-client@4.8.3 suite
# (services/api-go/test/realtime-e2e/client.e2e.js) against it: websocket +
# polling transports, subscribe/unsubscribe ACK parity, room fan-out, guard
# rejection, reconnect, token-events, and the REST fallback contract.
#
# No DB/Redis required. Cleanup kills ONLY the server PID this script started —
# it never lsof|xargs-kills a port, so an unrelated local listener is safe.
#
# Usage: bash scripts/strict/realtime-e2e.sh   (REALTIME_E2E_PORT overrides 8099)

set -uo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
API_GO="$REPO/services/api-go"
WEB_NM="$REPO/apps/web/node_modules"
PORT="${REALTIME_E2E_PORT:-8099}"
ADDR="127.0.0.1:${PORT}"
BASE="http://${ADDR}"

command -v go   >/dev/null 2>&1 || { echo "FAIL: go not found" >&2; exit 2; }
command -v node >/dev/null 2>&1 || { echo "FAIL: node not found" >&2; exit 2; }
if [[ ! -d "$WEB_NM/socket.io-client" ]]; then
  echo "FAIL: socket.io-client@4 not installed at apps/web/node_modules (run: pnpm install)" >&2
  exit 2
fi

# Refuse to start if the port is already taken — never adopt/kill someone else's
# listener.
if (exec 3<>"/dev/tcp/127.0.0.1/${PORT}") 2>/dev/null; then
  exec 3>&- 3<&-
  echo "FAIL: port ${PORT} already in use; set REALTIME_E2E_PORT to a free port." >&2
  exit 2
fi

TMP="$(mktemp -d)"
BIN="$TMP/realtimeserver"
LOG="$TMP/server.log"

echo "==> building realtime harness"
if ! ( cd "$API_GO" && go build -o "$BIN" ./test/realtimeserver ); then
  echo "FAIL: harness build failed" >&2
  exit 1
fi

echo "==> starting harness on ${ADDR}"
ADDR="$ADDR" "$BIN" >"$LOG" 2>&1 &
SRV_PID=$!
cleanup() { kill "$SRV_PID" 2>/dev/null; wait "$SRV_PID" 2>/dev/null; rm -rf "$TMP"; }
trap cleanup EXIT

# Readiness (the harness has no DB/compile delay, so this is quick).
ready=0
for _ in $(seq 1 50); do
  if curl -fsS "${BASE}/healthz" >/dev/null 2>&1; then ready=1; break; fi
  sleep 0.2
done
if [[ "$ready" -ne 1 ]]; then
  echo "FAIL: harness did not become ready" >&2
  tail -20 "$LOG" >&2
  exit 1
fi

echo "==> running socket.io-client@4.8.3 e2e"
WEB_NM="$WEB_NM" BASE="$BASE" node "$API_GO/test/realtime-e2e/client.e2e.js"
RC=$?

if [[ "$RC" -ne 0 ]]; then
  echo "--- harness server log (tail) ---" >&2
  tail -20 "$LOG" >&2
fi
exit "$RC"
