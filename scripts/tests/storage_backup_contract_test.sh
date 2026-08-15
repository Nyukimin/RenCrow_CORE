#!/usr/bin/env bash
set -Eeuo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
checker=${repo_root}/scripts/rencrow-storage-restore-check
backup_runner=${repo_root}/scripts/rencrow-storage-backup
unit_file=${repo_root}/systemd/user/rencrow-storage-backup.service
test_tmp_root=${repo_root}/Tmp/test-runtime
mkdir -p "${test_tmp_root}"
test_root=$(mktemp -d "${test_tmp_root}/storage-backup-contract.XXXXXX")
trap 'rm -rf -- "${test_root}"' EXIT

bash -n "${checker}"
bash -n "${backup_runner}"

contract_failures=()
assert_contains() {
  local file=$1
  local expected=$2
  local description=$3
  if ! grep -Fq -- "${expected}" "${file}"; then
    contract_failures+=("${description}")
  fi
}

assert_contains "${unit_file}" \
  'Environment=RENCROW_CONFIG=%h/.rencrow/config/core.yaml' \
  "backup service must pin the current CORE config"
assert_contains "${backup_runner}" \
  'config_file=${RENCROW_CONFIG:-${HOME}/.rencrow/config/core.yaml}' \
  "backup runner must default to the current CORE config"
assert_contains "${backup_runner}" \
  'if ! timeout 20 stat "${path}" >/dev/null 2>&1; then' \
  "backing mount stat failures must be handled explicitly"
assert_contains "${backup_runner}" \
  'echo "[NG] required backing mount is inaccessible for ${path}" >&2' \
  "backing mount stat failures must report the requested path"
assert_contains "${backup_runner}" \
  '${mount_target} == /' \
  "the root filesystem must remain rejected as a backup mount"
assert_contains "${backup_runner}" \
  '! mountpoint -q "${mount_target}"' \
  "unmounted paths must remain rejected"

if (( ${#contract_failures[@]} > 0 )); then
  printf '[RED] storage backup contract violations:\n' >&2
  printf ' - %s\n' "${contract_failures[@]}" >&2
  exit 1
fi

mkdir -p \
  "${test_root}/source/state/sessions" \
  "${test_root}/source/state/memory" \
  "${test_root}/source/state/exports/parquet" \
  "${test_root}/source/external-memory/redis" \
  "${test_root}/source/external-memory/qdrant" \
  "${test_root}/snapshot"

"${RENCROW_TEST_PYTHON:-python3}" - "${test_root}/source/state/l1.db" "${test_root}/source/state/l2.db" <<'PY'
import sqlite3
import sys

for path in sys.argv[1:]:
    connection = sqlite3.connect(path)
    connection.execute("CREATE TABLE memory (id INTEGER PRIMARY KEY, value TEXT NOT NULL)")
    connection.execute("INSERT INTO memory(value) VALUES ('kept')")
    connection.commit()
    connection.close()
PY

printf 'REDIS0011-test-rdb' > "${test_root}/source/external-memory/redis/dump.rdb"
printf 'qdrant-snapshot-test' > "${test_root}/source/external-memory/qdrant/full.snapshot"
qdrant_sha256=$(sha256sum "${test_root}/source/external-memory/qdrant/full.snapshot" | cut -d' ' -f1)
tar -C "${test_root}/source" -czf "${test_root}/snapshot/rencrow-state.tar.gz" state external-memory
(
  cd "${test_root}/snapshot"
  sha256sum rencrow-state.tar.gz > SHA256SUMS
)
cat > "${test_root}/snapshot/manifest.txt" <<EOF
format_version=3
core_name=state
session_relative=sessions
operation_memory_relative=memory
cold_export_relative=exports/parquet
conversation_l1_relative=l1.db
conversation_archive_relative=l2.db
redis_export=external-memory/redis/dump.rdb
qdrant_export=external-memory/qdrant/full.snapshot
qdrant_sha256=${qdrant_sha256}
EOF

"${checker}" "${test_root}/snapshot"
echo "[OK] storage backup contract test passed"
