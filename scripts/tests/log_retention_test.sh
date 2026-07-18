#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ROTATE_SCRIPT="${ROOT_DIR}/scripts/rencrow_log_rotate.sh"
SERVICE_UNIT="${ROOT_DIR}/systemd/user/rencrow-log-rotate.service"
TIMER_UNIT="${ROOT_DIR}/systemd/user/rencrow-log-rotate.timer"
TRACEBACK_DROPIN="${ROOT_DIR}/systemd/user/rencrow.service.d/10-panic-stack.conf"
TMP_DIR="$(mktemp -d)"

cleanup() {
  rm -rf "${TMP_DIR}"
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

[[ -f "${ROTATE_SCRIPT}" ]] || fail "archive script is missing"
[[ -f "${SERVICE_UNIT}" ]] || fail "archive service unit is missing"
[[ -f "${TIMER_UNIT}" ]] || fail "archive timer unit is missing"
[[ -f "${TRACEBACK_DROPIN}" ]] || fail "panic stack drop-in is missing"

ARCHIVE_DIR="${TMP_DIR}/archive"
JOURNAL_FIXTURE="${TMP_DIR}/journal.log"
mkdir -p "${ARCHIVE_DIR}"
cat > "${JOURNAL_FIXTURE}" <<'EOF'
2026-07-18T09:52:03+0000 rencrow[100]: panic: runtime error: invalid memory address
2026-07-18T09:52:03+0000 rencrow[100]: goroutine 23 [running]:
2026-07-18T09:52:03+0000 rencrow[100]: main.run()
2026-07-18T09:52:03+0000 rencrow[100]:     /workspace/main.go:42 +0x1a4
2026-07-18T09:52:03+0000 rencrow[100]: goroutine 24 [select]:
EOF

MOCK_JOURNALCTL="${TMP_DIR}/journalctl"
cat > "${MOCK_JOURNALCTL}" <<'EOF'
#!/usr/bin/env bash
cat "${MOCK_JOURNAL_FIXTURE}"
EOF
chmod +x "${MOCK_JOURNALCTL}"
export MOCK_JOURNAL_FIXTURE="${JOURNAL_FIXTURE}"

run_archive() {
  RENCROW_LOG_ARCHIVE_DIR="${ARCHIVE_DIR}" \
  RENCROW_LOG_ROTATE_LOCK_FILE="${TMP_DIR}/rotate.lock" \
  RENCROW_LOG_RETENTION_DAYS=7 \
  RENCROW_LOG_ARCHIVE_DATE=2026-07-18 \
  RENCROW_JOURNALCTL_BIN="${MOCK_JOURNALCTL}" \
    bash "${ROTATE_SCRIPT}"
}

echo "[1/4] panic and all-goroutine stack are preserved in the daily archive"
run_archive
today_archive="${ARCHIVE_DIR}/rencrow-2026-07-18.log.gz"
[[ -f "${today_archive}" ]] || fail "current daily archive is missing"
gzip -cd "${today_archive}" | grep -Fq 'panic: runtime error' || fail "panic line is missing"
gzip -cd "${today_archive}" | grep -Fq 'goroutine 24 [select]:' || fail "all-goroutine stack is missing"

echo "[2/4] archives older than seven days are deleted and recent archives remain"
old_archive="${ARCHIVE_DIR}/rencrow-2000-01-01.log.gz"
recent_archive="${ARCHIVE_DIR}/rencrow-2026-07-13.log.gz"
printf 'old' | gzip -c > "${old_archive}"
printf 'recent' | gzip -c > "${recent_archive}"
run_archive
[[ ! -e "${old_archive}" ]] || fail "archive older than seven days was retained"
[[ -e "${recent_archive}" ]] || fail "archive within seven days was deleted"

echo "[3/4] invalid retention is rejected without replacing an existing archive"
before_hash="$(sha256sum "${today_archive}" | awk '{print $1}')"
if RENCROW_LOG_ARCHIVE_DIR="${ARCHIVE_DIR}" \
  RENCROW_LOG_ROTATE_LOCK_FILE="${TMP_DIR}/invalid.lock" \
  RENCROW_LOG_RETENTION_DAYS=0 \
  RENCROW_LOG_ARCHIVE_DATE=2026-07-18 \
  RENCROW_JOURNALCTL_BIN="${MOCK_JOURNALCTL}" \
  bash "${ROTATE_SCRIPT}"; then
  fail "zero-day retention should fail"
fi
after_hash="$(sha256sum "${today_archive}" | awk '{print $1}')"
[[ "${before_hash}" == "${after_hash}" ]] || fail "invalid configuration replaced the archive"

echo "[4/4] systemd contract enables hourly archives and full panic stacks"
grep -Fq 'RENCROW_LOG_RETENTION_DAYS=7' "${SERVICE_UNIT}" || fail "service retention is not seven days"
grep -Fq 'OnCalendar=hourly' "${TIMER_UNIT}" || fail "timer is not hourly"
grep -Fq 'Persistent=true' "${TIMER_UNIT}" || fail "timer is not persistent"
grep -Fq 'GOTRACEBACK=all' "${TRACEBACK_DROPIN}" || fail "full Go traceback is not enabled"
grep -Fq 'StandardOutput=journal' "${TRACEBACK_DROPIN}" || fail "stdout is not sent to journal"
grep -Fq 'StandardError=journal' "${TRACEBACK_DROPIN}" || fail "stderr is not sent to journal"
grep -Fq 'LogRateLimitIntervalSec=0' "${TRACEBACK_DROPIN}" || fail "journal rate limiting is not disabled"

echo "PASS: log retention tests"
