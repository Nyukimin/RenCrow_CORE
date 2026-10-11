#!/usr/bin/env bash
set -euo pipefail

umask 077

RENCROW_HOME="${RENCROW_HOME:-${HOME}/.rencrow}"
ARCHIVE_DIR="${RENCROW_LOG_ARCHIVE_DIR:-${RENCROW_HOME}/logs/archive}"
RETENTION_DAYS="${RENCROW_LOG_RETENTION_DAYS:-7}"
SERVICE_UNIT="${RENCROW_LOG_SERVICE_UNIT:-rencrow.service}"
ANCHOR_DATE="${RENCROW_LOG_ARCHIVE_DATE:-}"
LOCK_FILE="${RENCROW_LOG_ROTATE_LOCK_FILE:-${RENCROW_HOME}/logs/.rencrow-log-rotate.lock}"
JOURNALCTL_BIN="${RENCROW_JOURNALCTL_BIN:-journalctl}"

unavailable() {
  echo "[UNAVAILABLE] $*" >&2
  exit 69
}

if ! host_os="$(uname -s 2>/dev/null)"; then
  unavailable "cannot determine operating system; log retention requires Linux"
fi
if [[ "${host_os}" != "Linux" ]]; then
  unavailable "unsupported platform ${host_os}; log retention requires Linux"
fi

for required_tool in date flock gzip find seq chmod mv rm dirname mkdir; do
  if ! command -v "${required_tool}" >/dev/null 2>&1; then
    unavailable "required command is unavailable: ${required_tool}"
  fi
done

if ! date -u -d '2000-01-01' +%F >/dev/null 2>&1; then
  unavailable "GNU date with -d support is unavailable"
fi

if [[ "${JOURNALCTL_BIN}" == */* ]]; then
  if [[ ! -f "${JOURNALCTL_BIN}" || ! -x "${JOURNALCTL_BIN}" ]]; then
    unavailable "configured journalctl executable is unavailable: ${JOURNALCTL_BIN}"
  fi
elif ! command -v "${JOURNALCTL_BIN}" >/dev/null 2>&1; then
  unavailable "configured journalctl executable is unavailable: ${JOURNALCTL_BIN}"
fi

if [[ -z "${ANCHOR_DATE}" ]]; then
  ANCHOR_DATE="$(date -u +%F)"
fi

if [[ ! "${RETENTION_DAYS}" =~ ^[1-9][0-9]*$ ]]; then
  echo "[NG] RENCROW_LOG_RETENTION_DAYS must be a positive integer: ${RETENTION_DAYS}" >&2
  exit 2
fi

if ! date -u -d "${ANCHOR_DATE}" +%F >/dev/null 2>&1; then
  echo "[NG] RENCROW_LOG_ARCHIVE_DATE must be YYYY-MM-DD: ${ANCHOR_DATE}" >&2
  exit 2
fi

mkdir -p "$(dirname "${LOCK_FILE}")"

exec 9>"${LOCK_FILE}"
if flock -E 75 -n 9; then
  :
else
  lock_status=$?
  if [[ "${lock_status}" -eq 75 ]]; then
    echo "[WARN] another RenCrow log archive is already running"
    exit 0
  fi
  echo "[NG] flock failed while acquiring log rotation lock (status=${lock_status})" >&2
  exit "${lock_status}"
fi

mkdir -p "${ARCHIVE_DIR}"
chmod 700 "${ARCHIVE_DIR}"

find "${ARCHIVE_DIR}" -maxdepth 1 -type f -name '.rencrow-*.log.gz.tmp' -delete

keep_dates="|"
for offset in $(seq 0 $((RETENTION_DAYS - 1))); do
  day="$(date -u -d "${ANCHOR_DATE} - ${offset} days" +%F)"
  keep_dates+="${day}|"
  next_day="$(date -u -d "${day} + 1 day" +%F)"
  archive="${ARCHIVE_DIR}/rencrow-${day}.log.gz"

  # 完了済みの過去日は不変なので再出力せず、当日分だけ毎時更新する。
  if (( offset > 0 )) && [[ -f "${archive}" ]]; then
    continue
  fi

  tmp_archive="${ARCHIVE_DIR}/.rencrow-${day}.log.gz.tmp"
  "${JOURNALCTL_BIN}" --user -u "${SERVICE_UNIT}" \
    --since "${day} 00:00:00 UTC" \
    --until "${next_day} 00:00:00 UTC" \
    --no-pager -o short-iso-precise |
    gzip -n -c > "${tmp_archive}"
  chmod 600 "${tmp_archive}"
  mv -f -- "${tmp_archive}" "${archive}"
done

shopt -s nullglob
for archive in "${ARCHIVE_DIR}"/rencrow-????-??-??.log.gz; do
  archive_name="${archive##*/}"
  archive_day="${archive_name#rencrow-}"
  archive_day="${archive_day%.log.gz}"
  if [[ "${keep_dates}" != *"|${archive_day}|"* ]]; then
    rm -f -- "${archive}"
  fi
done
shopt -u nullglob

echo "[OK] ${SERVICE_UNIT} journal archived hourly; retention=${RETENTION_DAYS} days"
