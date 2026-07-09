#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WATCHDOG_SCRIPT="${ROOT_DIR}/scripts/ops_watchdog.sh"
TMP_DIR="$(mktemp -d)"
MOCK_BIN="${TMP_DIR}/mockbin"
LOG_DIR="${TMP_DIR}/logs"
SERVE_COUNT_FILE="${TMP_DIR}/serve_count"
mkdir -p "${MOCK_BIN}" "${LOG_DIR}"
echo 0 > "${SERVE_COUNT_FILE}"

cleanup() {
  rm -rf "${TMP_DIR}"
}
trap cleanup EXIT

write_mock() {
  local name="$1"
  local body="$2"
  cat > "${MOCK_BIN}/${name}" <<EOF
#!/usr/bin/env bash
${body}
EOF
  chmod +x "${MOCK_BIN}/${name}"
}

write_mock "curl" '
url="${@: -1}"
if [[ "$url" == *"/health" ]]; then
  printf "%s" "${MOCK_HEALTH_CODE:-200}"
  exit 0
fi
if [[ "$url" == https://*"/viewer?tab=timeline" ]]; then
  printf "%s" "${MOCK_VIEWER_CODE:-200}"
  exit 0
fi
printf "000"
'

write_mock "tailscale" '
if [[ "${1:-}" == "serve" && "${2:-}" == "status" ]]; then
  count_file="${MOCK_SERVE_COUNT_FILE}"
  count=0
  [[ -f "$count_file" ]] && count="$(cat "$count_file")"
  if [[ "${MOCK_SERVE_OK:-1}" == "1" || "$count" -gt 0 ]]; then
    cat <<JSON
{"Web":{"fujitsu-ubunts.tailb07d8d.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:18790"}}}}}
JSON
  else
    echo "{}"
  fi
  exit 0
fi
if [[ "${1:-}" == "serve" && "${2:-}" == "--bg" ]]; then
  count_file="${MOCK_SERVE_COUNT_FILE}"
  count=0
  [[ -f "$count_file" ]] && count="$(cat "$count_file")"
  count=$((count + 1))
  echo "$count" > "$count_file"
  [[ "${MOCK_SERVE_CONFIGURE_OK:-1}" == "1" ]] && exit 0 || exit 1
fi
if [[ "${1:-}" == "funnel" ]]; then
  echo "unexpected funnel call" >&2
  exit 99
fi
exit 0
'

export PATH="${MOCK_BIN}:$PATH"
export RENCROW_HOME="${TMP_DIR}/rencrowhome"
export RENCROW_WATCHDOG_LOG_DIR="${LOG_DIR}"
export RENCROW_WATCHDOG_LOG_FILE="${LOG_DIR}/ops-watchdog.log"
export MOCK_SERVE_COUNT_FILE="${SERVE_COUNT_FILE}"

assert_eq() {
  local actual="$1"
  local expected="$2"
  local message="$3"
  if [[ "$actual" != "$expected" ]]; then
    echo "FAIL: ${message} (actual=${actual}, expected=${expected})"
    exit 1
  fi
}

echo "[1/4] healthy Serve config is left untouched"
MOCK_HEALTH_CODE=200 MOCK_SERVE_OK=1 MOCK_VIEWER_CODE=200 \
  bash "${WATCHDOG_SCRIPT}" once
assert_eq "$(cat "${SERVE_COUNT_FILE}")" "0" "healthy Serve should not be reconfigured"

echo "[2/4] missing Serve config is restored"
MOCK_HEALTH_CODE=200 MOCK_SERVE_OK=0 MOCK_VIEWER_CODE=200 \
  bash "${WATCHDOG_SCRIPT}" once
assert_eq "$(cat "${SERVE_COUNT_FILE}")" "1" "missing Serve should be configured"

echo "[3/4] RenCrow health failure blocks Serve mutation"
if MOCK_HEALTH_CODE=503 MOCK_SERVE_OK=0 MOCK_VIEWER_CODE=200 bash "${WATCHDOG_SCRIPT}" once; then
  echo "FAIL: health failure should fail watchdog"
  exit 1
fi
assert_eq "$(cat "${SERVE_COUNT_FILE}")" "1" "health failure should not configure Serve"

echo "[4/4] viewer HTTPS failure is reported"
if MOCK_HEALTH_CODE=200 MOCK_SERVE_OK=1 MOCK_VIEWER_CODE=000 bash "${WATCHDOG_SCRIPT}" once; then
  echo "FAIL: viewer HTTPS failure should fail watchdog"
  exit 1
fi
assert_eq "$(cat "${SERVE_COUNT_FILE}")" "1" "viewer failure should not use Funnel fallback"

echo "PASS: watchdog mock tests"
