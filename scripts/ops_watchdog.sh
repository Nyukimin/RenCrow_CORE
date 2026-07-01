#!/usr/bin/env bash
set -u

# RenCrow Viewer watchdog:
# - Verify local RenCrow health.
# - Keep Tailscale Serve pointed at the local Viewer runtime.
# - Do not touch Tailscale Funnel, webhook ports, LLM servers, or LINE state.

expand_home() {
  local path="$1"
  case "$path" in
    "~") printf "%s" "$HOME" ;;
    "~/"*) printf "%s/%s" "$HOME" "${path#~/}" ;;
    *) printf "%s" "$path" ;;
  esac
}

PICO_HOME="$(expand_home "${PICO_HOME:-$HOME/.picoclaw}")"
LOG_DIR="$(expand_home "${PICOCLAW_WATCHDOG_LOG_DIR:-$PICO_HOME/logs}")"
LOG_FILE="$(expand_home "${PICOCLAW_WATCHDOG_LOG_FILE:-$LOG_DIR/ops-watchdog.log}")"

REN_CROW_URL="${PICOCLAW_WATCHDOG_RENCROW_URL:-http://127.0.0.1:18790}"
HEALTH_URL="${PICOCLAW_WATCHDOG_HEALTH_URL:-${REN_CROW_URL}/health}"
TAILSCALE_HOST="${PICOCLAW_WATCHDOG_TAILSCALE_HOST:-fujitsu-ubunts.tailb07d8d.ts.net}"
VIEWER_URL="${PICOCLAW_WATCHDOG_VIEWER_URL:-https://${TAILSCALE_HOST}/viewer?tab=timeline}"
SERVE_TARGET="${PICOCLAW_WATCHDOG_SERVE_TARGET:-${REN_CROW_URL}}"
LOCAL_TIMEOUT_SEC="${PICOCLAW_WATCHDOG_LOCAL_TIMEOUT_SEC:-3}"
EXTERNAL_TIMEOUT_SEC="${PICOCLAW_WATCHDOG_EXTERNAL_TIMEOUT_SEC:-8}"
CHECK_INTERVAL_SEC="${PICOCLAW_WATCHDOG_INTERVAL_SEC:-60}"

mkdir -p "$LOG_DIR"
touch "$LOG_FILE"

log() {
  local level="$1"
  local target="$2"
  local result="$3"
  local action="$4"
  local detail="${5:-}"
  local ts
  ts="$(TZ=Asia/Tokyo date '+%Y-%m-%d %H:%M:%S %Z')"
  printf '[%s] level=%s target=%s result=%s action=%s detail="%s"\n' \
    "$ts" "$level" "$target" "$result" "$action" "$detail" >> "$LOG_FILE"
}

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    log "ERROR" "watchdog" "NG" "missing_command" "$1"
    return 127
  fi
}

http_code() {
  local timeout_sec="$1"
  local url="$2"
  curl -k -sS -o /dev/null -w "%{http_code}" --max-time "$timeout_sec" "$url" || echo "000"
}

check_rencrow_health() {
  local code
  code="$(http_code "$LOCAL_TIMEOUT_SEC" "$HEALTH_URL")"
  if [[ "$code" == "200" ]]; then
    log "INFO" "rencrow_health" "OK" "none" "http_code=${code} url=${HEALTH_URL}"
    return 0
  fi
  log "ERROR" "rencrow_health" "NG" "blocked" "http_code=${code} url=${HEALTH_URL}"
  return 1
}

serve_status_has_target() {
  local status_json="$1"
  STATUS_JSON="$status_json" python3 - "$SERVE_TARGET" <<'PY'
import json
import os
import sys

target = sys.argv[1].rstrip("/")
try:
    payload = json.loads(os.environ.get("STATUS_JSON", ""))
except Exception:
    sys.exit(1)

web = payload.get("Web", {})
for host_cfg in web.values():
    handlers = host_cfg.get("Handlers", {})
    for handler in handlers.values():
        proxy = str(handler.get("Proxy", "")).rstrip("/")
        if proxy == target:
            sys.exit(0)
sys.exit(1)
PY
}

configure_serve() {
  tailscale serve --bg --yes "$SERVE_TARGET" >/dev/null
}

check_tailscale_serve() {
  local status_json
  status_json="$(tailscale serve status --json 2>/dev/null || true)"
  if serve_status_has_target "$status_json"; then
    log "INFO" "tailscale_serve" "OK" "none" "target=${SERVE_TARGET}"
  else
    log "WARN" "tailscale_serve" "NG" "configure" "target=${SERVE_TARGET}"
    if ! configure_serve; then
      log "ERROR" "tailscale_serve" "NG" "configure_failed" "target=${SERVE_TARGET}"
      return 1
    fi
  fi

  status_json="$(tailscale serve status --json 2>/dev/null || true)"
  if ! serve_status_has_target "$status_json"; then
    log "ERROR" "tailscale_serve" "NG" "verify_config_failed" "target=${SERVE_TARGET}"
    return 1
  fi

  local code
  code="$(http_code "$EXTERNAL_TIMEOUT_SEC" "$VIEWER_URL")"
  if [[ "$code" == "200" ]]; then
    log "INFO" "viewer_https" "OK" "none" "http_code=${code} url=${VIEWER_URL}"
    return 0
  fi
  log "ERROR" "viewer_https" "NG" "unreachable" "http_code=${code} url=${VIEWER_URL}"
  return 1
}

run_once() {
  require_cmd curl || return 127
  require_cmd tailscale || return 127
  require_cmd python3 || return 127

  log "INFO" "watchdog" "OK" "start" "interval=${CHECK_INTERVAL_SEC}s"
  check_rencrow_health || return 1
  check_tailscale_serve || return 1
  log "INFO" "watchdog" "OK" "end" "run complete"
}

if [[ "${1:-once}" == "loop" ]]; then
  while true; do
    run_once || true
    sleep "$CHECK_INTERVAL_SEC"
  done
else
  run_once
fi
