#!/usr/bin/env bash
set -Eeuo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
checker=${repo_root}/scripts/rencrow-storage-restore-check
backup_runner=${repo_root}/scripts/rencrow-storage-backup
unit_file=${repo_root}/systemd/user/rencrow-storage-backup.service
timer_file=${repo_root}/systemd/user/rencrow-storage-backup.timer
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
assert_contains "${timer_file}" \
  'Description=RenCrow memory backup daily at 03:00 JST' \
  "backup timer must describe the canonical daily schedule"
assert_contains "${timer_file}" \
  'OnCalendar=*-*-* 03:00:00 Asia/Tokyo' \
  "backup timer must run once daily at 03:00 JST"
assert_contains "${timer_file}" \
  'RandomizedDelaySec=5m' \
  "backup timer jitter must remain within the 03:00 backup window"
if [[ $(grep -c '^OnCalendar=' "${timer_file}") -ne 1 ]]; then
  contract_failures+=("backup timer must have exactly one daily calendar expression")
fi
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
assert_contains "${backup_runner}" \
  'if (( ${#backup_config[@]} != 21 )); then' \
  "backup-values must contain the raw source cohort field"
assert_contains "${backup_runner}" \
  'core_port=${backup_config[20]}' \
  "backup must derive CORE readiness port from canonical config"
assert_contains "${backup_runner}" \
  'wait_for_core_readiness' \
  "backup must not close its stop window before CORE readiness"
assert_contains "${backup_runner}" \
  '"http://127.0.0.1:${core_port}/health/ready"' \
  "backup must verify the canonical local CORE readiness route"
assert_contains "${backup_runner}" \
  '"http://127.0.0.1:${core_port}/health/ready" 2>/dev/null' \
  "expected readiness retries must not flood the backup journal"
assert_contains "${backup_runner}" \
  'raw_source_dir=${backup_config[12]}' \
  "raw source field index must follow cold export"
assert_contains "${backup_runner}" \
  "'format_version=4'" \
  "new backups must emit the Common Raw cohort format"
assert_contains "${backup_runner}" \
  'mount "${backup_mount}"' \
  "the backup runner must mount the dedicated backup medium for its window"
assert_contains "${backup_runner}" \
  'umount "${backup_mount}"' \
  "the backup runner must unmount a medium that it mounted"
assert_contains "${backup_runner}" \
  'systemctl --user mask --runtime --now rencrow.service' \
  "the snapshot window must reject external CORE starts and restarts"
assert_contains "${backup_runner}" \
  'systemctl --user unmask --runtime rencrow.service' \
  "the snapshot cleanup must remove its runtime mask"
assert_contains "${checker}" \
  'RENCROW_SQLITE_INTEGRITY_TIMEOUT_SEC' \
  "SQLite restore integrity must have a configurable bounded deadline"
assert_contains "${checker}" \
  'SQLite integrity check timed out' \
  "SQLite integrity timeout must be distinct from corruption"

if (( ${#contract_failures[@]} > 0 )); then
  printf '[RED] storage backup contract violations:\n' >&2
  printf ' - %s\n' "${contract_failures[@]}" >&2
  exit 1
fi

mkdir -p \
  "${test_root}/source/state/sessions" \
  "${test_root}/source/state/memory" \
  "${test_root}/source/state/exports/parquet" \
  "${test_root}/source/state/raw-source/objects/sha256" \
  "${test_root}/source/external-memory/redis" \
  "${test_root}/source/external-memory/qdrant" \
  "${test_root}/snapshot"

"${RENCROW_TEST_PYTHON:-python3}" - "${test_root}/source/state/l1.db" "${test_root}/source/state/l2.db" "${test_root}/source/state/raw-source" <<'PY'
import hashlib
import json
import os
import sqlite3
import sys

for path in sys.argv[1:3]:
    connection = sqlite3.connect(path)
    connection.execute("CREATE TABLE memory (id INTEGER PRIMARY KEY, value TEXT NOT NULL)")
    connection.execute("INSERT INTO memory(value) VALUES ('kept')")
    if path == sys.argv[1]:
        connection.executescript("""
            CREATE TABLE l1_raw_source_manifest (manifest_id TEXT PRIMARY KEY);
            CREATE TABLE l1_raw_record (storage_kind TEXT, inline_payload BLOB, object_ref TEXT, content_sha256 TEXT, content_size INTEGER, asset_refs_json TEXT);
            CREATE TABLE l1_raw_state_event (state_event_id TEXT PRIMARY KEY);
            CREATE TABLE l1_raw_projection_receipt (projection_receipt_id TEXT PRIMARY KEY);
        """)
        raw_root = sys.argv[3]
        object_root = os.path.join(raw_root, "objects", "sha256")
        inline = b"inline-raw-content"
        object_content = b"object-raw-content"
        asset_content = b"asset-raw-content"
        def put(content):
            digest = hashlib.sha256(content).hexdigest()
            directory = os.path.join(object_root, digest[:2])
            os.makedirs(directory, mode=0o700, exist_ok=True)
            path = os.path.join(directory, digest)
            with open(path, "wb") as handle:
                handle.write(content)
            os.chmod(path, 0o600)
            return "objects/sha256/%s/%s" % (digest[:2], digest), digest
        object_ref, object_hash = put(object_content)
        asset_ref, asset_hash = put(asset_content)
        connection.execute("INSERT INTO l1_raw_record VALUES (?, ?, ?, ?, ?, ?)", ("inline", inline, "", hashlib.sha256(inline).hexdigest(), len(inline), "[]"))
        connection.execute("INSERT INTO l1_raw_record VALUES (?, ?, ?, ?, ?, ?)", ("object", None, object_ref, object_hash, len(object_content), json.dumps([{"source_asset_id": "asset-1", "object_ref": asset_ref, "sha256": asset_hash, "size": len(asset_content), "media_type": "application/octet-stream"}])))
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
format_version=4
core_name=state
session_relative=sessions
operation_memory_relative=memory
cold_export_relative=exports/parquet
raw_source_relative=raw-source
conversation_l1_relative=l1.db
conversation_archive_relative=l2.db
redis_export=external-memory/redis/dump.rdb
qdrant_export=external-memory/qdrant/full.snapshot
qdrant_sha256=${qdrant_sha256}
EOF

"${checker}" "${test_root}/snapshot"

slow_python=${test_root}/slow-python
cat > "${slow_python}" <<'SH'
#!/usr/bin/env bash
if [[ ${1:-} == - && ${2:-} == */l1.db && ${3:-} == */l2.db ]]; then
  sleep 5
fi
exec python3 "$@"
SH
chmod +x "${slow_python}"
if RENCROW_TEST_PYTHON="${slow_python}" RENCROW_SQLITE_INTEGRITY_TIMEOUT_SEC=1 \
  "${checker}" "${test_root}/snapshot" >"${test_root}/timeout.out" 2>"${test_root}/timeout.err"; then
  echo "[RED] delayed SQLite integrity check unexpectedly succeeded" >&2
  exit 1
fi
grep -Fq 'SQLite integrity check timed out' "${test_root}/timeout.err"

legacy_snapshot=${test_root}/legacy-v3
mkdir -p "${legacy_snapshot}"
cp "${test_root}/snapshot/rencrow-state.tar.gz" "${legacy_snapshot}/rencrow-state.tar.gz"
sed -e 's/^format_version=4$/format_version=3/' -e '/^raw_source_relative=/d' "${test_root}/snapshot/manifest.txt" > "${legacy_snapshot}/manifest.txt"
(cd "${legacy_snapshot}" && sha256sum rencrow-state.tar.gz > SHA256SUMS)
"${checker}" "${legacy_snapshot}"

legacy_v2_snapshot=${test_root}/legacy-v2
mkdir -p "${legacy_v2_snapshot}"
cp "${test_root}/snapshot/rencrow-state.tar.gz" "${legacy_v2_snapshot}/rencrow-state.tar.gz"
sed -e 's/^format_version=4$/format_version=2/' -e '/^raw_source_relative=/d' "${test_root}/snapshot/manifest.txt" > "${legacy_v2_snapshot}/manifest.txt"
(cd "${legacy_v2_snapshot}" && sha256sum rencrow-state.tar.gz > SHA256SUMS)
"${checker}" "${legacy_v2_snapshot}"

make_bad_snapshot() {
  local mode=$1
  local bad_root=${test_root}/bad-${mode}
  mkdir -p "${bad_root}/source"
  tar -xzf "${test_root}/snapshot/rencrow-state.tar.gz" -C "${bad_root}/source"
  local object_path
  object_path=$(find "${bad_root}/source/state/raw-source/objects" -type f | head -n 1)
  if [[ ${mode} == missing ]]; then
    rm -f -- "${object_path}"
  else
    printf 'tampered-raw-object' > "${object_path}"
  fi
  tar -C "${bad_root}/source" -czf "${bad_root}/rencrow-state.tar.gz" state external-memory
  cp "${test_root}/snapshot/manifest.txt" "${bad_root}/manifest.txt"
  (cd "${bad_root}" && sha256sum rencrow-state.tar.gz > SHA256SUMS)
  if "${checker}" "${bad_root}"; then
    echo "[NG] ${mode} Common Raw object must be rejected" >&2
    exit 1
  fi
}

make_bad_snapshot missing
make_bad_snapshot tampered
echo "[OK] storage backup contract test passed"
