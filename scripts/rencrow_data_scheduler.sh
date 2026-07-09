#!/usr/bin/env bash
set -euo pipefail

MODE="${1:-daily}"
SCRIPT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ROOT_DIR="${RENCROW_REPO_DIR:-$SCRIPT_ROOT}"
LOCK_DIR="${RENCROW_DATA_LOCK_DIR:-$HOME/.rencrow/locks}"
LOG_DIR="${RENCROW_DATA_LOG_DIR:-$HOME/.rencrow/logs}"
LOCK_FILE="$LOCK_DIR/rencrow-data-${MODE}.lock"
LOG_FILE="$LOG_DIR/rencrow-data-${MODE}.log"
RUN_MAKE_STATUS=0

mkdir -p "$LOCK_DIR" "$LOG_DIR"
touch "$LOG_FILE"

exec 9>"$LOCK_FILE"
if ! flock -n 9; then
  printf '[%s] already running mode=%s\n' "$(date -u +%FT%TZ)" "$MODE" >> "$LOG_FILE"
  exit 0
fi

log() {
  printf '[%s] %s\n' "$(date -u +%FT%TZ)" "$*" >> "$LOG_FILE"
}

run_make() {
  log "run: $*"
  (cd "$ROOT_DIR" && make "$@") >> "$LOG_FILE" 2>&1
  RUN_MAKE_STATUS=0
}

run_make_allow_status() {
  local ok_status="$1"
  shift
  log "run allow_status=$ok_status: $*"
  set +e
  (cd "$ROOT_DIR" && make "$@") >> "$LOG_FILE" 2>&1
  local status=$?
  set -e
  RUN_MAKE_STATUS="$status"
  if [[ "$status" == "0" || "$status" == "$ok_status" ]]; then
    return 0
  fi
  return "$status"
}

notify_viewer() {
  local phase="$1"
  local status="$2"
  local message="${3:-}"
  local source="${4:-rencrow-data}"
  local payload
  payload="$(PHASE="$phase" STATUS="$status" MESSAGE="$message" SOURCE="$source" MODE_NAME="$MODE" python3 - <<'PY'
import json
import os
print(json.dumps({
  "phase": os.environ.get("PHASE", "refresh"),
  "status": os.environ.get("STATUS", "ok"),
  "message": os.environ.get("MESSAGE", ""),
  "source": os.environ.get("SOURCE", "rencrow-data"),
  "meta": {"mode": os.environ.get("MODE_NAME", "daily")},
}, ensure_ascii=False))
PY
)"
  curl -sS -X POST -H 'Content-Type: application/json' --data "$payload" "http://127.0.0.1:18790/viewer/investment/notify" >> "$LOG_FILE" 2>&1 || true
}

case "$MODE" in
  daily)
    run_make rencrow-data-init
    run_make_allow_status 2 rencrow-data-market-online DATA_START_DATE="${DATA_START_DATE:-}" DATA_END_DATE="${DATA_END_DATE:-}" \
      DATA_LOOKBACK_DAYS="${DATA_MARKET_LOOKBACK_DAYS:-7}"
    [[ "$RUN_MAKE_STATUS" == "2" ]] && notify_viewer market partial "daily market increment partial" || notify_viewer market success "daily market increment"
    run_make_allow_status 2 rencrow-data-macro-online DATA_START_DATE="${DATA_START_DATE:-}" DATA_END_DATE="${DATA_END_DATE:-}" \
      DATA_LOOKBACK_DAYS="${DATA_MACRO_LOOKBACK_DAYS:-30}"
    [[ "$RUN_MAKE_STATUS" == "2" ]] && notify_viewer macro partial "daily macro increment partial" || notify_viewer macro success "daily macro increment"
    run_make rencrow-data-features
    notify_viewer features success "daily feature refresh"
    run_make rencrow-data-events
    notify_viewer events success "daily event refresh"
    run_make_allow_status 2 rencrow-data-validate SNAPSHOT_DATE="${SNAPSHOT_DATE:-today}"
    notify_viewer validate success "daily data validation"
    ;;
  weekly)
    run_make rencrow-data-init
    run_make_allow_status 2 rencrow-data-market-online DATA_START_DATE="${DATA_START_DATE:-}" DATA_END_DATE="${DATA_END_DATE:-}" \
      DATA_LOOKBACK_DAYS="${DATA_MARKET_LOOKBACK_DAYS:-14}"
    [[ "$RUN_MAKE_STATUS" == "2" ]] && notify_viewer market partial "weekly market increment partial" || notify_viewer market success "weekly market increment"
    run_make_allow_status 2 rencrow-data-macro-online DATA_START_DATE="${DATA_START_DATE:-}" DATA_END_DATE="${DATA_END_DATE:-}" \
      DATA_LOOKBACK_DAYS="${DATA_MACRO_LOOKBACK_DAYS:-45}"
    [[ "$RUN_MAKE_STATUS" == "2" ]] && notify_viewer macro partial "weekly macro increment partial" || notify_viewer macro success "weekly macro increment"
    run_make rencrow-data-weekly-research SNAPSHOT_DATE="${SNAPSHOT_DATE:-$(date -u +%F)}"
    notify_viewer research success "weekly research flow"
    if [[ -f "${DATA_APPROVAL_FILE:-rencrow-data/approvals/latest.yml}" ]]; then
      run_make rencrow-data-paper-trade DATA_APPROVAL_FILE="${DATA_APPROVAL_FILE:-rencrow-data/approvals/latest.yml}"
      notify_viewer paper_trade success "weekly paper trade recorded"
      run_make rencrow-data-audit-report
      notify_viewer audit success "weekly paper audit refreshed"
    else
      log "skip paper trade: approval file not found path=${DATA_APPROVAL_FILE:-rencrow-data/approvals/latest.yml}"
      notify_viewer paper_trade skipped "approval file not found"
    fi
    ;;
  *)
    log "error unknown mode=$MODE"
    exit 2
    ;;
esac

log "done mode=$MODE"
