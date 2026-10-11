#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ROTATE_SCRIPT="${ROOT_DIR}/scripts/rencrow_log_rotate.sh"
SERVICE_UNIT="${ROOT_DIR}/systemd/user/rencrow-log-rotate.service"
TIMER_UNIT="${ROOT_DIR}/systemd/user/rencrow-log-rotate.timer"
TRACEBACK_DROPIN="${ROOT_DIR}/systemd/user/rencrow.service.d/10-panic-stack.conf"
TMP_DIR="$(mktemp -d)"
BASH_BIN="$(command -v bash)"
REAL_FLOCK_BIN="$(command -v flock || true)"
LOCK_HOLDER_PID=""

cleanup() {
  if [[ -n "${LOCK_HOLDER_PID}" ]]; then
    kill "${LOCK_HOLDER_PID}" 2>/dev/null || true
    wait "${LOCK_HOLDER_PID}" 2>/dev/null || true
  fi
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

make_test_path() {
  local test_path="$1"
  local omit="${2:-}"
  local command_name source_path
  mkdir -p "${test_path}"
  for command_name in bash date find gzip flock mv rm seq chmod dirname mkdir; do
    [[ "${command_name}" == "${omit}" ]] && continue
    source_path="$(command -v "${command_name}")"
    ln -s "${source_path}" "${test_path}/${command_name}"
  done
  if [[ "${omit}" != "uname" ]]; then
    ln -s "$(command -v uname)" "${test_path}/uname"
  fi
}

run_archive_with() {
  local test_path="$1"
  local archive_dir="$2"
  local lock_file="$3"
  local journalctl_bin="$4"
  local retention="${5:-7}"
  local archive_date="${6:-2026-07-18}"
  PATH="${test_path}" \
  RENCROW_LOG_ARCHIVE_DIR="${archive_dir}" \
  RENCROW_LOG_ROTATE_LOCK_FILE="${lock_file}" \
  RENCROW_LOG_RETENTION_DAYS="${retention}" \
  RENCROW_LOG_ARCHIVE_DATE="${archive_date}" \
  RENCROW_JOURNALCTL_BIN="${journalctl_bin}" \
    "${BASH_BIN}" "${ROTATE_SCRIPT}"
}

run_archive() {
  run_archive_with "${PATH}" "${ARCHIVE_DIR}" "${TMP_DIR}/rotate.lock" "${MOCK_JOURNALCTL}"
}

echo "[1/4] panic and all-goroutine stack are preserved in the daily archive"
run_archive
today_archive="${ARCHIVE_DIR}/rencrow-2026-07-18.log.gz"
[[ -f "${today_archive}" ]] || fail "current daily archive is missing"
gzip -cd "${today_archive}" | grep -Fq 'panic: runtime error' || fail "panic line is missing"
gzip -cd "${today_archive}" | grep -Fq 'goroutine 24 [select]:' || fail "all-goroutine stack is missing"
[[ "$(stat -c '%a' "${ARCHIVE_DIR}")" == "700" ]] || fail "archive directory is not owner-only"
[[ "$(stat -c '%a' "${today_archive}")" == "600" ]] || fail "archive file is not owner-only"

echo "[2/4] archives older than seven days are deleted and recent archives remain"
old_archive="${ARCHIVE_DIR}/rencrow-2000-01-01.log.gz"
recent_archive="${ARCHIVE_DIR}/rencrow-2026-07-13.log.gz"
printf 'old' | gzip -c > "${old_archive}"
printf 'recent' | gzip -c > "${recent_archive}"
recent_before_hash="$(sha256sum "${recent_archive}" | awk '{print $1}')"
run_archive
[[ ! -e "${old_archive}" ]] || fail "archive older than seven days was retained"
[[ -e "${recent_archive}" ]] || fail "archive within seven days was deleted"
recent_after_hash="$(sha256sum "${recent_archive}" | awk '{print $1}')"
[[ "${recent_before_hash}" == "${recent_after_hash}" ]] || fail "completed recent archive was rewritten"

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

echo "[5/9] missing flock fails unavailable before archive or lock mutation"
NO_FLOCK_PATH="${TMP_DIR}/no-flock-bin"
make_test_path "${NO_FLOCK_PATH}" flock
missing_flock_archive="${TMP_DIR}/missing-flock/archive"
missing_flock_lock="${TMP_DIR}/missing-flock/logs/rotate.lock"
if output="$(run_archive_with "${NO_FLOCK_PATH}" "${missing_flock_archive}" "${missing_flock_lock}" "${MOCK_JOURNALCTL}" 2>&1)"; then
  fail "missing flock should fail"
else
  status=$?
fi
[[ "${status}" -ne 0 ]] || fail "missing flock returned success"
grep -Fq 'unavailable' <<<"${output}" || fail "missing flock did not report unavailable: ${output}"
[[ "${output}" != *'another RenCrow log archive is already running'* ]] || fail "missing flock was misreported as contention"
[[ ! -e "${missing_flock_archive}" && ! -e "$(dirname "${missing_flock_lock}")" ]] || fail "missing flock mutated archive or lock paths"

echo "[6/9] unsupported operating system fails unavailable before mutation"
UNSUPPORTED_PATH="${TMP_DIR}/unsupported-bin"
make_test_path "${UNSUPPORTED_PATH}"
rm -f "${UNSUPPORTED_PATH}/uname"
cat > "${UNSUPPORTED_PATH}/uname" <<'EOF'
#!/bin/bash
printf 'Darwin\n'
EOF
chmod +x "${UNSUPPORTED_PATH}/uname"
unsupported_archive="${TMP_DIR}/unsupported/archive"
unsupported_lock="${TMP_DIR}/unsupported/logs/rotate.lock"
if output="$(run_archive_with "${UNSUPPORTED_PATH}" "${unsupported_archive}" "${unsupported_lock}" "${MOCK_JOURNALCTL}" 2>&1)"; then
  fail "unsupported OS should fail"
else
  status=$?
fi
[[ "${status}" -ne 0 ]] || fail "unsupported OS returned success"
grep -Fq 'unsupported platform' <<<"${output}" || fail "unsupported OS diagnostic is missing: ${output}"
[[ ! -e "${unsupported_archive}" && ! -e "$(dirname "${unsupported_lock}")" ]] || fail "unsupported OS mutated archive or lock paths"

echo "[7/9] missing GNU date and configured journalctl fail before mutation"
NO_GNU_DATE_PATH="${TMP_DIR}/no-gnu-date-bin"
make_test_path "${NO_GNU_DATE_PATH}"
rm -f "${NO_GNU_DATE_PATH}/date"
cat > "${NO_GNU_DATE_PATH}/date" <<'EOF'
#!/bin/bash
exit 1
EOF
chmod +x "${NO_GNU_DATE_PATH}/date"
no_date_archive="${TMP_DIR}/no-gnu-date/archive"
no_date_lock="${TMP_DIR}/no-gnu-date/logs/rotate.lock"
if output="$(run_archive_with "${NO_GNU_DATE_PATH}" "${no_date_archive}" "${no_date_lock}" "${MOCK_JOURNALCTL}" 7 invalid-date 2>&1)"; then
  fail "missing GNU date support should fail"
else
  status=$?
fi
[[ "${status}" -ne 0 ]] || fail "missing GNU date support returned success"
grep -Fq 'GNU date' <<<"${output}" || fail "missing GNU date was not distinguished from invalid input: ${output}"
[[ "${output}" != *'RENCROW_LOG_ARCHIVE_DATE must be YYYY-MM-DD'* ]] || fail "missing GNU date was reported as invalid input"
[[ ! -e "${no_date_archive}" && ! -e "$(dirname "${no_date_lock}")" ]] || fail "missing GNU date mutated archive or lock paths"
missing_journal_archive="${TMP_DIR}/missing-journalctl/archive"
missing_journal_lock="${TMP_DIR}/missing-journalctl/logs/rotate.lock"
if output="$(run_archive_with "${PATH}" "${missing_journal_archive}" "${missing_journal_lock}" "${TMP_DIR}/missing-journalctl-bin" 2>&1)"; then
  fail "missing configured journalctl should fail"
else
  status=$?
fi
[[ "${status}" -ne 0 ]] || fail "missing configured journalctl returned success"
grep -Fq 'journalctl' <<<"${output}" || fail "missing configured journalctl diagnostic is missing: ${output}"
[[ ! -e "${missing_journal_archive}" && ! -e "$(dirname "${missing_journal_lock}")" ]] || fail "missing journalctl mutated archive or lock paths"
directory_journalctl="${TMP_DIR}/journalctl-directory"
mkdir -p "${directory_journalctl}"
directory_journal_archive="${TMP_DIR}/directory-journalctl/archive"
directory_journal_lock="${TMP_DIR}/directory-journalctl/logs/rotate.lock"
if output="$(run_archive_with "${PATH}" "${directory_journal_archive}" "${directory_journal_lock}" "${directory_journalctl}" 2>&1)"; then
  fail "directory configured as journalctl should fail unavailable"
else
  status=$?
fi
[[ "${status}" -ne 0 ]] || fail "directory configured as journalctl returned success"
grep -Fq 'unavailable' <<<"${output}" || fail "directory configured as journalctl was not rejected as unavailable: ${output}"
[[ ! -e "${directory_journal_archive}" && ! -e "$(dirname "${directory_journal_lock}")" ]] || fail "directory configured as journalctl mutated archive or lock paths"

echo "[8/9] non-contention flock errors fail without touching archives"
FAILING_FLOCK_PATH="${TMP_DIR}/failing-flock-bin"
make_test_path "${FAILING_FLOCK_PATH}"
rm -f "${FAILING_FLOCK_PATH}/flock"
cat > "${FAILING_FLOCK_PATH}/flock" <<'EOF'
#!/bin/bash
echo 'injected flock I/O failure' >&2
exit 42
EOF
chmod +x "${FAILING_FLOCK_PATH}/flock"
injected_lock_archive="${ARCHIVE_DIR}/rencrow-2000-01-01.log.gz"
printf 'preserve-on-lock-error' | gzip -n -c > "${injected_lock_archive}"
injected_before_hash="$(sha256sum "${injected_lock_archive}" | awk '{print $1}')"
if output="$(run_archive_with "${FAILING_FLOCK_PATH}" "${ARCHIVE_DIR}" "${TMP_DIR}/injected.lock" "${MOCK_JOURNALCTL}" 2>&1)"; then
  fail "non-contention flock failure should fail"
else
  status=$?
fi
[[ "${status}" -ne 0 ]] || fail "non-contention flock failure returned success"
[[ "${output}" != *'another RenCrow log archive is already running'* ]] || fail "non-contention flock failure was misreported as contention"
grep -Fq 'flock' <<<"${output}" || fail "non-contention flock diagnostic is missing: ${output}"
injected_after_hash="$(sha256sum "${injected_lock_archive}" | awk '{print $1}')"
[[ "${injected_before_hash}" == "${injected_after_hash}" ]] || fail "non-contention flock failure changed an existing archive"

echo "[9/9] real flock contention returns busy success without archive mutation"
[[ -n "${REAL_FLOCK_BIN}" ]] || fail "real Linux flock is required for contention acceptance"
contention_lock="${TMP_DIR}/contention.lock"
contention_ready="${TMP_DIR}/contention.ready"
contention_archive="${TMP_DIR}/contention-archive"
mkdir -m 700 "${contention_archive}"
contention_sentinel="${contention_archive}/rencrow-2026-07-17.log.gz"
printf 'keep-contention-archive' | gzip -n -c > "${contention_sentinel}"
contention_before_hash="$(sha256sum "${contention_sentinel}" | awk '{print $1}')"
(
  exec 8>"${contention_lock}"
  "${REAL_FLOCK_BIN}" -n 8
  : > "${contention_ready}"
  sleep 10
) &
LOCK_HOLDER_PID=$!
for attempt in $(seq 1 100); do
  [[ -f "${contention_ready}" ]] && break
  sleep 0.05
done
[[ -f "${contention_ready}" ]] || fail "real flock holder did not acquire the lock"
if output="$(run_archive_with "${PATH}" "${contention_archive}" "${contention_lock}" "${MOCK_JOURNALCTL}" 2>&1)"; then
  status=0
else
  status=$?
fi
[[ "${status}" -eq 0 ]] || fail "real flock contention should be successful no-op: ${output}"
grep -Fq 'another RenCrow log archive is already running' <<<"${output}" || fail "real contention did not report busy: ${output}"
contention_after_hash="$(sha256sum "${contention_sentinel}" | awk '{print $1}')"
[[ "${contention_before_hash}" == "${contention_after_hash}" ]] || fail "real flock contention changed an existing archive"
[[ ! -e "${contention_archive}/rencrow-2026-07-18.log.gz" ]] || fail "real contention created a daily archive"
kill "${LOCK_HOLDER_PID}" 2>/dev/null || true
wait "${LOCK_HOLDER_PID}" 2>/dev/null || true
LOCK_HOLDER_PID=""

echo "PASS: log retention tests"
