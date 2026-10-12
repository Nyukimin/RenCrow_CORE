#!/usr/bin/env bash
set -Eeuo pipefail

current_bash_executable=${BASH:-}
if [[ ${current_bash_executable} != /* || ! -x ${current_bash_executable} ]]; then
  echo "[NG] selected Bash executable must be an absolute executable path" >&2
  exit 2
fi
case ${OSTYPE:-} in
  msys*|cygwin*|mingw*)
    if ! current_bash_executable=$(cygpath -aw "${current_bash_executable}"); then
      echo "[NG] selected Bash executable could not be converted to a native Windows path" >&2
      exit 2
    fi
    if [[ ${current_bash_executable} != *.exe ]]; then
      current_bash_executable=${current_bash_executable}.exe
    fi
    ;;
esac

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
checker=${repo_root}/scripts/rencrow-storage-restore-check
backup_runner=${repo_root}/scripts/rencrow-storage-backup
unit_file=${repo_root}/systemd/user/rencrow-storage-backup.service
timer_file=${repo_root}/systemd/user/rencrow-storage-backup.timer
makefile=${repo_root}/Makefile
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

assert_not_contains() {
  local file=$1
  local unexpected=$2
  local description=$3
  if grep -Fq -- "${unexpected}" "${file}"; then
    contract_failures+=("${description}")
  fi
}

assert_no_restore_scratch() {
  local snapshot=$1
  local description=$2
  if find "${snapshot}" -maxdepth 1 -type d -name '.restore.*' -print -quit | grep -q .; then
    echo "[NG] restore scratch directory remains after ${description}: ${snapshot}" >&2
    exit 1
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
  'snapshot_format_version=4' \
  "ordinary backups must retain the Common Raw cohort format"
assert_contains "${backup_runner}" \
  '"format_version=${snapshot_format_version}"' \
  "backup manifest must publish the selected cohort format"
assert_not_contains "${backup_runner}" \
  'rencrow-thread-migrate' \
  "CORE export must not depend on the retired ThreadID migration CLI"
assert_not_contains "${backup_runner}" \
  'threadmigration' \
  "CORE export must not retain threadmigration receipt/config helpers"
assert_not_contains "${checker}" \
  'rencrow-thread-migrate' \
  "restore check must not depend on the retired ThreadID migration CLI"
assert_not_contains "${checker}" \
  'threadmigration' \
  "restore check must not retain threadmigration receipt validation"
assert_not_contains "${makefile}" \
  'THREAD_MIGRATION_' \
  "storage backup build/install must not define ThreadID migration variables"
assert_not_contains "${makefile}" \
  'rencrow-thread-migrate' \
  "storage backup build/install must not build or install the retired CLI"
assert_contains "${backup_runner}" \
  '--exclude="${core_name}/staging"' \
  "CORE snapshot must exclude externally written, re-fetchable staging artifacts"
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
assert_contains "${backup_runner}" \
  'core-export)' \
  "the owner runner must expose a fixed CORE-only migration export mode"
assert_contains "${backup_runner}" \
  'migration_output_dir=${2:-}' \
  "CORE migration export must accept only the Workspace staging output directory"
assert_contains "${backup_runner}" \
  'if [[ ${core_export} != true ]]; then' \
  "CORE-only export must keep non-CORE durable module handling outside its execution path"
assert_contains "${backup_runner}" \
  '"${migration_packager}" --snapshot-dir "${candidate_dir}" --output-dir "${migration_output_dir}"' \
  "CORE migration export must package only the restore-checked candidate"
assert_contains "${backup_runner}" \
  'CORE migration state package verified' \
  "CORE migration export must emit a bounded success marker"
assert_contains "${makefile}" \
  'install -m 0755 $(CORE_MIGRATION_PACKAGER) $(CORE_MIGRATION_PACKAGER_DST)' \
  "storage backup deployment must install the CORE migration packager"
assert_contains "${backup_runner}" \
  "find \"\${durable_manifest_dir}\" -maxdepth 1 -type f -name 'RenCrow_*.json'" \
  "backup must discover the installed non-CORE durable module catalog"
assert_contains "${backup_runner}" \
  '"${durable_snapshot_binary}" plan --manifest' \
  "backup must consume the canonical manifest planner"
assert_contains "${backup_runner}" \
  '"${durable_snapshot_binary}" create --manifest' \
  "backup must create and scratch-verify module snapshots through the owner-neutral CLI"
assert_contains "${backup_runner}" \
  'systemctl --user mask --runtime --now "${writer}.service"' \
  "backup must reject writer restarts during each module snapshot window"
assert_contains "${backup_runner}" \
  'systemctl --user unmask --runtime "${writer}.service"' \
  "backup cleanup must remove every writer runtime mask"
assert_contains "${backup_runner}" \
  'module_snapshot_root=${backup_mount}/snapshots/modules' \
  "module snapshots must stay on the dedicated backup medium"
assert_contains "${backup_runner}" \
  'database and backup roots must use distinct mounted devices' \
  "module backups must reject a destination on the live database device"
assert_contains "${backup_runner}" \
  'mktemp -d "${core_snapshot_root}/recent/.${timestamp}.partial.XXXXXX"' \
  "CORE snapshot copy must remain hidden on the destination filesystem"
assert_contains "${backup_runner}" \
  'mv "${core_snapshot_partial_parent}/snapshot" "${snapshot_dir}"' \
  "CORE snapshot final name must be exposed only by a same-filesystem rename"
assert_contains "${checker}" \
  'RENCROW_SQLITE_INTEGRITY_TIMEOUT_SEC' \
  "SQLite restore integrity must have a configurable bounded deadline"
assert_not_contains "${checker}" \
  'timeout --signal=' \
  "restore checking must not depend on GNU timeout"
assert_contains "${checker}" \
  'SQLite integrity check timed out' \
  "SQLite integrity timeout must be distinct from corruption"
assert_contains "${checker}" \
  'current_bash_executable=${BASH:-}' \
  "restore supervisor must inherit the selected Bash executable"
assert_contains "${checker}" \
  'cygpath -aw' \
  "native Windows Bash must use the selected MSYS executable path"
assert_contains "${checker}" \
  'os.path.isfile(current_bash_executable)' \
  "native supervisor must validate the supplied Bash executable"
assert_contains "${checker}" \
  'CREATE_SUSPENDED | CREATE_NEW_PROCESS_GROUP | EXTENDED_STARTUPINFO_PRESENT' \
  "native worker must be created suspended with its separate console process group"
assert_contains "${checker}" \
  'AssignProcessToJobObject(job_handle, process_info.hProcess)' \
  "native worker must enter its private Job Object before it can run"
assert_contains "${checker}" \
  'ResumeThread(process_info.hThread)' \
  "native worker must resume through the distinct primary-thread handle"
assert_contains "${checker}" \
  'JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE' \
  "native worker Job Object must kill owned processes when its private handle closes"
assert_contains "${checker}" \
  'QueryInformationJobObject' \
  "native worker teardown must query Job Object active-process accounting"
assert_contains "${checker}" \
  'inherited_handles = (HANDLE * 3)' \
  "native worker must inherit only the three configured standard handles"
assert_contains "${checker}" \
  'sqlite_supervision_marker' \
  "restore scratch must be retained when owned-worker teardown is unconfirmed"
assert_not_contains "${checker}" \
  'CREATE_BREAKAWAY_FROM_JOB' \
  "native worker must not opt out of Job Object ownership"
assert_not_contains "${checker}" \
  'taskkill' \
  "native worker cleanup must not fall back to PID-tree enumeration"
assert_not_contains "${checker}" \
  '"bash.exe"' \
  "native restore supervisor must not rediscover Bash by bare name"
native_create_line=$(grep -nF 'created = kernel.CreateProcessW(' "${checker}" | head -n 1 | cut -d: -f1)
native_assign_line=$(grep -nF 'if not kernel.AssignProcessToJobObject(job_handle, process_info.hProcess):' "${checker}" | head -n 1 | cut -d: -f1)
native_resume_line=$(grep -nF 'previous_suspend_count = kernel.ResumeThread(process_info.hThread)' "${checker}" | head -n 1 | cut -d: -f1)
if [[ ! ${native_create_line} =~ ^[0-9]+$ || ! ${native_assign_line} =~ ^[0-9]+$ || ! ${native_resume_line} =~ ^[0-9]+$ ]] || \
  (( native_create_line >= native_assign_line || native_assign_line >= native_resume_line )); then
  contract_failures+=("native worker creation, Job assignment, and primary-thread resume must stay in that order")
fi
assert_contains "${checker}" \
  'def cleanup_interrupted_scratch(' \
  "native interruption cleanup must stay inside the existing restore checker"
assert_contains "${checker}" \
  'interrupted restore scratch cleanup failed:' \
  "interruption cleanup failures must be diagnosed and fail closed"

if (( ${#contract_failures[@]} > 0 )); then
  printf '[RED] storage backup contract violations:\n' >&2
  printf ' - %s\n' "${contract_failures[@]}" >&2
  exit 1
fi

python3 - "${checker}" "${test_root}/portable-scratch" <<'PY'
import ast
import os
import stat
import sys
from unittest.mock import patch

checker_path, test_root = sys.argv[1:]
with open(checker_path, encoding="utf-8") as handle:
    checker_source = handle.read()
command_start = checker_source.index('python3 - "${current_bash_executable}"')
body_start = checker_source.index("<<'PY'\n", command_start) + len("<<'PY'\n")
body_end = checker_source.index("\nPY\n", body_start)
supervisor = ast.parse(checker_source[body_start:body_end])
functions = [
    node for node in supervisor.body
    if isinstance(node, ast.FunctionDef)
    and node.name in {"capture_owned_scratch_identity", "cleanup_interrupted_scratch"}
]
if {node.name for node in functions} != {"capture_owned_scratch_identity", "cleanup_interrupted_scratch"}:
    raise SystemExit("[NG] portable scratch tests could not load the owned cleanup implementation")
scope = {"os": os, "stat": stat}
exec(compile(ast.Module(body=functions, type_ignores=[]), checker_path, "exec"), scope)
capture_identity = scope["capture_owned_scratch_identity"]
cleanup_scratch = scope["cleanup_interrupted_scratch"]

os.makedirs(test_root)
snapshot = os.path.join(test_root, "snapshot")
os.mkdir(snapshot)
sibling = os.path.join(snapshot, ".restore.sibling")
os.mkdir(sibling)
with open(os.path.join(sibling, "keep.txt"), "w", encoding="ascii") as handle:
    handle.write("sibling retained\n")

outside = os.path.join(test_root, "outside")
os.mkdir(outside)
target = os.path.join(outside, "keep.txt")
with open(target, "w", encoding="ascii") as handle:
    handle.write("link target retained\n")

root = os.path.join(snapshot, ".restore.exact-owned-root")
os.mkdir(root)
root_identity = capture_identity(root)
snapshot_identity = capture_identity(snapshot)
marker_path = os.path.join(root, ".sqlite-integrity-supervision-active")
with open(marker_path, "x", encoding="ascii") as handle:
    handle.write("owned worker\n")
    marker_stat = os.fstat(handle.fileno())
marker_identity = (marker_stat.st_dev, marker_stat.st_ino)
os.makedirs(os.path.join(root, "state", "nested"))
with open(os.path.join(root, "state", "nested", "worker-input.py"), "w", encoding="ascii") as handle:
    handle.write("scratch\n")
link_path = os.path.join(root, "outside-link")
try:
    os.symlink(target, link_path)
except (NotImplementedError, OSError):
    print("[SKIP] portable external-link target case is unsupported by this OS")
    link_path = None

status, diagnostic = cleanup_scratch(
    root, snapshot, root_identity, snapshot_identity, marker_path, marker_identity,
)
if status != 0 or diagnostic or os.path.lexists(root):
    raise SystemExit(f"[NG] exact owned scratch was not removed: status={status}, diagnostic={diagnostic!r}")
if not os.path.isfile(target) or not os.path.isfile(os.path.join(sibling, "keep.txt")):
    raise SystemExit("[NG] interruption cleanup removed the snapshot sibling or link target")
if link_path is not None and os.path.lexists(link_path):
    raise SystemExit("[NG] interruption cleanup did not unlink the external symlink itself")

replacement_root = os.path.join(snapshot, ".restore.root-substitution")
os.mkdir(replacement_root)
replacement_identity = capture_identity(replacement_root)
replacement_marker = os.path.join(replacement_root, ".sqlite-integrity-supervision-active")
with open(replacement_marker, "x", encoding="ascii") as handle:
    handle.write("owned worker\n")
    marker_stat = os.fstat(handle.fileno())
replacement_marker_identity = (marker_stat.st_dev, marker_stat.st_ino)
replacement_moved = replacement_root + ".owned"
os.rename(replacement_root, replacement_moved)
replacement_moved_marker = os.path.join(replacement_moved, os.path.basename(replacement_marker))
os.mkdir(replacement_root)
replacement_file = os.path.join(replacement_root, "keep.txt")
with open(replacement_file, "w", encoding="ascii") as handle:
    handle.write("replacement retained\n")
status, diagnostic = cleanup_scratch(
    replacement_root, snapshot, replacement_identity, snapshot_identity,
    replacement_marker, replacement_marker_identity,
)
if status != 125 or not os.path.isfile(replacement_file) or not os.path.isfile(replacement_moved_marker):
    raise SystemExit("[NG] replaced restore root was not refused with the owned evidence retained")

reparse_root = os.path.join(snapshot, ".restore.reparse-substitution")
os.mkdir(reparse_root)
reparse_identity = capture_identity(reparse_root)
reparse_marker = os.path.join(reparse_root, ".sqlite-integrity-supervision-active")
with open(reparse_marker, "x", encoding="ascii") as handle:
    handle.write("owned worker\n")
    marker_stat = os.fstat(handle.fileno())
reparse_marker_identity = (marker_stat.st_dev, marker_stat.st_ino)
reparse_moved = reparse_root + ".owned"
os.rename(reparse_root, reparse_moved)
reparse_moved_marker = os.path.join(reparse_moved, os.path.basename(reparse_marker))
try:
    os.symlink(outside, reparse_root, target_is_directory=True)
except (NotImplementedError, OSError):
    print("[SKIP] reparse-root refusal case is unsupported by this OS")
    os.mkdir(reparse_root)
    with open(os.path.join(reparse_root, "keep.txt"), "w", encoding="ascii") as handle:
        handle.write("replacement retained\n")
status, diagnostic = cleanup_scratch(
    reparse_root, snapshot, reparse_identity, snapshot_identity,
    reparse_marker, reparse_marker_identity,
)
if status != 125 or not os.path.isfile(target) or not os.path.isfile(reparse_moved_marker):
    raise SystemExit("[NG] reparse restore root was not refused with its target protected")

final_root = os.path.join(snapshot, ".restore.bash-finished-empty-root")
os.mkdir(final_root)
final_identity = capture_identity(final_root)
final_marker = os.path.join(final_root, ".sqlite-integrity-supervision-active")
with open(final_marker, "x", encoding="ascii") as handle:
    handle.write("owned worker\n")
    marker_stat = os.fstat(handle.fileno())
final_marker_identity = (marker_stat.st_dev, marker_stat.st_ino)
real_unlink = os.unlink
real_rmdir = os.rmdir

def finish_root_after_marker_unlink(path, *args, **kwargs):
    result = real_unlink(path, *args, **kwargs)
    if os.path.abspath(path) == final_marker:
        real_rmdir(final_root)
    return result

with patch.object(os, "unlink", finish_root_after_marker_unlink):
    status, diagnostic = cleanup_scratch(
        final_root, snapshot, final_identity, snapshot_identity,
        final_marker, final_marker_identity,
    )
if status != 0 or os.path.lexists(final_root):
    raise SystemExit(f"[NG] final empty-root race was not accepted: {status}, {diagnostic!r}")

marker_root = os.path.join(snapshot, ".restore.marker-ownership")
os.mkdir(marker_root)
marker_root_identity = capture_identity(marker_root)
marker_root_marker = os.path.join(marker_root, ".sqlite-integrity-supervision-active")
with open(marker_root_marker, "x", encoding="ascii") as handle:
    handle.write("owned marker\n")
    owned_marker_stat = os.fstat(handle.fileno())
owned_marker_identity = (owned_marker_stat.st_dev, owned_marker_stat.st_ino)
staged_marker = marker_root_marker + ".replacement"
with open(staged_marker, "x", encoding="ascii") as handle:
    handle.write("replacement marker\n")
    replacement_marker_stat = os.fstat(handle.fileno())
replacement_identity = (replacement_marker_stat.st_dev, replacement_marker_stat.st_ino)
os.replace(staged_marker, marker_root_marker)
with open(marker_root_marker, "rb") as handle:
    marker_data = handle.read()
status, diagnostic = cleanup_scratch(
    marker_root, snapshot, marker_root_identity, snapshot_identity,
    marker_root_marker, owned_marker_identity,
)
after_marker_stat = os.lstat(marker_root_marker)
with open(marker_root_marker, "rb") as handle:
    marker_after = handle.read()
if (
    status != 125
    or (after_marker_stat.st_dev, after_marker_stat.st_ino) != replacement_identity
    or marker_after != marker_data
):
    raise SystemExit("[NG] substituted supervision marker was not preserved as unowned evidence")

external_marker = os.path.join(outside, ".sqlite-integrity-supervision-active")
status, diagnostic = cleanup_scratch(
    marker_root, snapshot, marker_root_identity, snapshot_identity,
    external_marker, owned_marker_identity,
)
if status != 125 or os.path.lexists(external_marker) or not os.path.isfile(marker_root_marker):
    raise SystemExit("[NG] out-of-root marker path was touched during cleanup recovery")

failure_root = os.path.join(snapshot, ".restore.remove-failure")
os.mkdir(failure_root)
failure_identity = capture_identity(failure_root)
failure_marker = os.path.join(failure_root, ".sqlite-integrity-supervision-active")
with open(failure_marker, "x", encoding="ascii") as handle:
    handle.write("owned worker\n")
    marker_stat = os.fstat(handle.fileno())
failure_marker_identity = (marker_stat.st_dev, marker_stat.st_ino)
failure_file = os.path.join(failure_root, "remove-me.txt")
with open(failure_file, "w", encoding="ascii") as handle:
    handle.write("injected remove failure\n")
real_unlink = os.unlink

def injected_unlink(path, *args, **kwargs):
    if os.path.abspath(path) == failure_file:
        raise PermissionError("injected remove failure")
    return real_unlink(path, *args, **kwargs)

with patch.object(os, "unlink", injected_unlink):
    status, diagnostic = cleanup_scratch(
        failure_root, snapshot, failure_identity, snapshot_identity,
        failure_marker, failure_marker_identity,
    )
if status != 125 or "injected remove failure" not in diagnostic:
    raise SystemExit(f"[NG] injected remove failure did not fail closed: {status}, {diagnostic!r}")
if not os.path.isfile(failure_marker) or not os.path.isfile(failure_file):
    raise SystemExit("[NG] delete failure did not retain its marker and scratch evidence")
print("[OK] portable interruption scratch ownership and failure boundaries verified")
PY

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

if ! "${checker}" "${test_root}/snapshot" >"${test_root}/success.out" 2>&1; then
  cat "${test_root}/success.out" >&2
  echo "[NG] valid format_version=4 snapshot was rejected" >&2
  exit 1
fi
grep -Fq '[OK] snapshot is checksum-valid, extractable, and restorable:' "${test_root}/success.out"
assert_no_restore_scratch "${test_root}/snapshot" "successful restore validation"

v5_snapshot=${test_root}/format5-unsupported
mkdir -p "${v5_snapshot}"
cp "${test_root}/snapshot/rencrow-state.tar.gz" "${v5_snapshot}/rencrow-state.tar.gz"
cp "${test_root}/snapshot/SHA256SUMS" "${v5_snapshot}/SHA256SUMS"
sed 's/^format_version=4$/format_version=5/' \
  "${test_root}/snapshot/manifest.txt" > "${v5_snapshot}/manifest.txt"
if "${checker}" "${v5_snapshot}"; then
  echo "[NG] unsupported format_version=5 manifest was accepted" >&2
  exit 1
fi
assert_no_restore_scratch "${v5_snapshot}" "unsupported-format rejection"

for invalid_timeout in 0 3601 invalid; do
  if RENCROW_SQLITE_INTEGRITY_TIMEOUT_SEC=${invalid_timeout} \
    "${checker}" "${test_root}/snapshot" >"${test_root}/invalid-timeout.out" 2>&1; then
    echo "[NG] invalid SQLite deadline ${invalid_timeout} was accepted" >&2
    exit 1
  else
    invalid_status=$?
  fi
  if (( invalid_status != 2 )); then
    cat "${test_root}/invalid-timeout.out" >&2
    echo "[NG] invalid SQLite deadline ${invalid_timeout} returned ${invalid_status}, expected 2" >&2
    exit 1
  fi
  if grep -Fq 'timed out' "${test_root}/invalid-timeout.out"; then
    echo "[NG] invalid SQLite deadline ${invalid_timeout} was mislabeled as a timeout" >&2
    exit 1
  fi
  assert_no_restore_scratch "${test_root}/snapshot" "invalid-deadline rejection"
done

failed_python=${test_root}/failed-python
cat > "${failed_python}" <<'SH'
#!/usr/bin/env bash
if [[ ${1:-} == - && ${2:-} == *l1.db && ${3:-} == *l2.db ]]; then
  echo "injected SQLite worker failure" >&2
  exit "${RENCROW_TEST_WORKER_EXIT:-23}"
fi
exec python3 "$@"
SH
chmod +x "${failed_python}"
for worker_error_code in 23 124; do
  if RENCROW_TEST_PYTHON="${failed_python}" \
    RENCROW_TEST_WORKER_EXIT=${worker_error_code} \
    "${checker}" "${test_root}/snapshot" >"${test_root}/worker-error.out" 2>&1; then
    echo "[NG] non-timeout SQLite worker failure unexpectedly succeeded" >&2
    exit 1
  else
    worker_error_status=$?
  fi
  if (( worker_error_status != worker_error_code )); then
    cat "${test_root}/worker-error.out" >&2
    echo "[NG] non-timeout SQLite worker failure returned ${worker_error_status}, expected ${worker_error_code}" >&2
    exit 1
  fi
  if grep -Fq 'timed out' "${test_root}/worker-error.out" || \
    grep -Fq '[OK] snapshot is checksum-valid' "${test_root}/worker-error.out"; then
    echo "[NG] non-timeout SQLite worker failure was mislabeled or promoted" >&2
    exit 1
  fi
  assert_no_restore_scratch "${test_root}/snapshot" "non-timeout worker failure ${worker_error_code}"
done

survivor_python=${test_root}/survivor.py
cat > "${survivor_python}" <<'PY'
import os
import sys
import time

marker, pid_file = sys.argv[1:]

def publish_pid(path, pid):
    temporary = path + "." + str(os.getpid()) + ".tmp"
    with open(temporary, "x", encoding="ascii") as handle:
        handle.write(str(pid) + "\n")
    os.replace(temporary, path)

publish_pid(pid_file, os.getpid())
parent_pid_file = os.environ.get("RENCROW_TEST_ROOT_EXIT_PARENT_PID_FILE")
if parent_pid_file:
    publish_pid(parent_pid_file, os.getppid())
release_file = os.environ.get("RENCROW_TEST_SURVIVOR_RELEASE_FILE")
if release_file:
    while not os.path.isfile(release_file):
        time.sleep(0.02)
else:
    time.sleep(5)
with open(marker, "w", encoding="ascii") as handle:
    handle.write("survived\n")
PY

process_liveness_checker=${test_root}/process-liveness.py
cat > "${process_liveness_checker}" <<'PY'
import ctypes
import os
import sys

pid = int(sys.argv[1])
if os.name != "nt":
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        print("dead")
    except PermissionError:
        print("alive")
    else:
        print("alive")
else:
    from ctypes import wintypes

    kernel = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel.OpenProcess.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
    kernel.OpenProcess.restype = wintypes.HANDLE
    kernel.WaitForSingleObject.argtypes = [wintypes.HANDLE, wintypes.DWORD]
    kernel.WaitForSingleObject.restype = wintypes.DWORD
    kernel.CloseHandle.argtypes = [wintypes.HANDLE]
    kernel.CloseHandle.restype = wintypes.BOOL
    handle = kernel.OpenProcess(0x00100000, False, pid)  # SYNCHRONIZE
    if not handle:
        error = ctypes.get_last_error()
        if error == 87:  # ERROR_INVALID_PARAMETER: PID no longer exists
            print("dead")
        else:
            raise SystemExit(f"cannot inspect native worker PID {pid} (WinError {error})")
    else:
        try:
            state = kernel.WaitForSingleObject(handle, 0)
        finally:
            kernel.CloseHandle(handle)
        if state == 0:
            print("dead")
        elif state == 0x00000102:  # WAIT_TIMEOUT
            print("alive")
        else:
            raise SystemExit(f"cannot confirm native worker PID {pid} exit (wait={state})")
PY

slow_python=${test_root}/slow-python
cat > "${slow_python}" <<'SH'
#!/usr/bin/env bash
if [[ ${1:-} == - && ${2:-} == *l1.db && ${3:-} == *l2.db ]]; then
  python3 "${RENCROW_TEST_SURVIVOR_SCRIPT}" \
    "${RENCROW_TEST_SURVIVOR_MARKER}" "${RENCROW_TEST_SURVIVOR_PID_FILE}" &
  wait
fi
exec python3 "$@"
SH
chmod +x "${slow_python}"
timeout_marker=${test_root}/timeout-survivor.marker
timeout_child_pid_file=${test_root}/timeout-survivor.pid
timeout_started=${SECONDS}
if RENCROW_TEST_PYTHON="${slow_python}" \
  RENCROW_TEST_SURVIVOR_MARKER="${timeout_marker}" \
  RENCROW_TEST_SURVIVOR_PID_FILE="${timeout_child_pid_file}" \
  RENCROW_TEST_SURVIVOR_SCRIPT="${survivor_python}" \
  RENCROW_SQLITE_INTEGRITY_TIMEOUT_SEC=1 \
  "${checker}" "${test_root}/snapshot" >"${test_root}/timeout.out" 2>&1; then
  echo "[RED] delayed SQLite integrity check unexpectedly succeeded" >&2
  exit 1
else
  timeout_status=$?
fi
timeout_elapsed=$((SECONDS - timeout_started))
if (( timeout_status == 0 || timeout_elapsed >= 5 )); then
  cat "${test_root}/timeout.out" >&2
  echo "[NG] SQLite deadline was not enforced promptly (status=${timeout_status}, elapsed=${timeout_elapsed}s)" >&2
  exit 1
fi
grep -Fq 'SQLite integrity check timed out' "${test_root}/timeout.out"
if grep -Fq '[OK] snapshot is checksum-valid' "${test_root}/timeout.out"; then
  echo "[NG] timed-out SQLite validation emitted the final success marker" >&2
  exit 1
fi
assert_no_restore_scratch "${test_root}/snapshot" "SQLite deadline expiry"
sleep 5.2
if [[ -e ${timeout_marker} ]]; then
  echo "[NG] delayed SQLite worker descendant survived timeout and touched scratch state" >&2
  exit 1
fi
if [[ ! -f ${timeout_child_pid_file} ]]; then
  echo "[NG] delayed SQLite worker did not record its child process" >&2
  exit 1
fi
timeout_child_pid=$(cat "${timeout_child_pid_file}")
if ! timeout_child_state=$(python3 "${process_liveness_checker}" "${timeout_child_pid}"); then
  echo "[NG] could not inspect delayed SQLite worker PID ${timeout_child_pid}" >&2
  exit 1
fi
if [[ ${timeout_child_state} != dead ]]; then
  echo "[NG] delayed SQLite worker child process ${timeout_child_pid} survived timeout" >&2
  exit 1
fi
echo "[OK] SQLite worker deadline and descendant cleanup verified in ${timeout_elapsed}s"

root_exit_python=${test_root}/root-exit-python
cat > "${root_exit_python}" <<'SH'
#!/usr/bin/env bash
if [[ ${1:-} == - && ${2:-} == *l1.db && ${3:-} == *l2.db ]]; then
  exec python3 "${RENCROW_TEST_ROOT_EXIT_FIXTURE_SCRIPT}" \
    "${RENCROW_TEST_ROOT_EXIT_METADATA_FILE}" \
    "${RENCROW_TEST_SURVIVOR_SCRIPT}" \
    "${RENCROW_TEST_SURVIVOR_MARKER}" \
    "${RENCROW_TEST_SURVIVOR_PID_FILE}" \
    "${RENCROW_TEST_ROOT_EXIT_PARENT_PID_FILE}" \
    "${RENCROW_TEST_ROOT_EXIT_RELEASE_FILE}" \
    "${RENCROW_TEST_SURVIVOR_RELEASE_FILE}" \
    "${RENCROW_TEST_WORKER_EXIT}"
fi
exec python3 "$@"
SH
chmod +x "${root_exit_python}"
root_exit_fixture_python=${test_root}/root-exit-fixture.py
cat > "${root_exit_fixture_python}" <<'PY'
import json
import os
import subprocess
import sys
import time

metadata_file, survivor_script, marker, child_pid_file, child_parent_pid_file, \
    root_release_file, child_release_file, exit_status_text = sys.argv[1:]

def publish_root_metadata(path, root_pid, child_launch_pid):
    temporary = path + "." + str(os.getpid()) + ".tmp"
    with open(temporary, "x", encoding="ascii") as handle:
        json.dump({"root_pid": root_pid, "child_launch_pid": child_launch_pid}, handle)
        handle.write("\n")
    os.replace(temporary, path)

child_environment = os.environ.copy()
child_environment["RENCROW_TEST_ROOT_EXIT_PARENT_PID_FILE"] = child_parent_pid_file
child_environment["RENCROW_TEST_SURVIVOR_RELEASE_FILE"] = child_release_file
survivor = subprocess.Popen(
    [sys.executable, survivor_script, marker, child_pid_file],
    env=child_environment,
    shell=False,
)
publish_root_metadata(metadata_file, os.getpid(), survivor.pid)
while not os.path.isfile(root_release_file):
    time.sleep(0.02)
raise SystemExit(int(exit_status_text))
PY
python3 - "${current_bash_executable}" "${checker}" "${test_root}/snapshot" \
  "${root_exit_python}" "${root_exit_fixture_python}" "${survivor_python}" "${process_liveness_checker}" <<'PY'
import ctypes
import json
import os
import signal
import subprocess
import sys
import time

def bash_path(value):
    if os.name != "nt":
        return value
    if len(value) > 2 and value[1] == ":" and value[2] in "\\/":
        return "/" + value[0].lower() + "/" + value[3:].replace("\\", "/")
    if value.startswith("\\\\"):
        return value.replace("\\", "/")
    return value

def inspect_posix_pid(pid, liveness_checker):
    result = subprocess.run(
        [sys.executable, liveness_checker, str(pid)],
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        shell=False,
        timeout=3.0,
        check=False,
        text=True,
    )
    if result.returncode != 0:
        raise SystemExit(f"[NG] could not inspect worker PID {pid}: {result.stderr.strip()}")
    return result.stdout.strip() == "alive"

current_bash_executable, checker, snapshot, worker, root_exit_fixture_script, survivor_script, liveness_checker = sys.argv[1:]
windows = os.name == "nt"
kernel = None
held_handles = []
if windows:
    from ctypes import wintypes
    kernel = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel.OpenProcess.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
    kernel.OpenProcess.restype = wintypes.HANDLE
    kernel.WaitForSingleObject.argtypes = [wintypes.HANDLE, wintypes.DWORD]
    kernel.WaitForSingleObject.restype = wintypes.DWORD
    kernel.CloseHandle.argtypes = [wintypes.HANDLE]
    kernel.CloseHandle.restype = wintypes.BOOL
    kernel.TerminateProcess.argtypes = [wintypes.HANDLE, wintypes.UINT]
    kernel.TerminateProcess.restype = wintypes.BOOL
    kernel.IsProcessInJob.argtypes = [wintypes.HANDLE, wintypes.HANDLE, ctypes.POINTER(wintypes.BOOL)]
    kernel.IsProcessInJob.restype = wintypes.BOOL
    kernel.FreeConsole.argtypes = []
    kernel.FreeConsole.restype = wintypes.BOOL
    kernel.AllocConsole.argtypes = []
    kernel.AllocConsole.restype = wintypes.BOOL
    kernel.GetConsoleProcessList.argtypes = [ctypes.POINTER(wintypes.DWORD), wintypes.DWORD]
    kernel.GetConsoleProcessList.restype = wintypes.DWORD

for worker_exit, expected_status in (("0", 125), ("23", 23)):
    case_root = os.path.join(os.path.dirname(snapshot), f"root-exit-{worker_exit}")
    marker = case_root + ".marker"
    child_pid_file = case_root + ".child-pid"
    child_parent_pid_file = case_root + ".child-parent-pid"
    root_metadata_file = case_root + ".root-metadata.json"
    release_file = case_root + ".release"
    child_release_file = case_root + ".child-release"
    environment = os.environ.copy()
    environment.update({
        "RENCROW_TEST_PYTHON": bash_path(worker),
        "RENCROW_TEST_ROOT_EXIT_FIXTURE_SCRIPT": bash_path(root_exit_fixture_script),
        "RENCROW_TEST_SURVIVOR_SCRIPT": bash_path(survivor_script),
        "RENCROW_TEST_SURVIVOR_MARKER": bash_path(marker),
        "RENCROW_TEST_SURVIVOR_PID_FILE": bash_path(child_pid_file),
        "RENCROW_TEST_SURVIVOR_RELEASE_FILE": bash_path(child_release_file),
        "RENCROW_TEST_ROOT_EXIT_PARENT_PID_FILE": bash_path(child_parent_pid_file),
        "RENCROW_TEST_ROOT_EXIT_METADATA_FILE": bash_path(root_metadata_file),
        "RENCROW_TEST_ROOT_EXIT_RELEASE_FILE": bash_path(release_file),
        "RENCROW_TEST_WORKER_EXIT": worker_exit,
        "RENCROW_SQLITE_INTEGRITY_TIMEOUT_SEC": "30",
    })
    command = [current_bash_executable, bash_path(checker), bash_path(snapshot)]
    process = None
    root_pid = None
    child_pid = None
    console_attached = False
    cleanup_errors = []
    try:
        if windows:
            if not kernel.FreeConsole():
                raise SystemExit(f"[NG] could not detach root-exit fixture from inherited console (WinError {ctypes.get_last_error()})")
            if not kernel.AllocConsole():
                raise SystemExit(f"[NG] could not allocate an owned root-exit fixture console (WinError {ctypes.get_last_error()})")
            console_attached = True
        process = subprocess.Popen(
            command,
            env=environment,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            shell=False,
            start_new_session=not windows,
            creationflags=0x00000200 if windows else 0,
            text=True,
        )
        deadline = time.monotonic() + 8.0
        while time.monotonic() < deadline and process.poll() is None:
            if os.path.isfile(root_metadata_file) and os.path.isfile(child_pid_file) and os.path.isfile(child_parent_pid_file):
                break
            time.sleep(0.02)
        if not (os.path.isfile(root_metadata_file) and os.path.isfile(child_pid_file) and os.path.isfile(child_parent_pid_file)):
            raise SystemExit("[NG] root-exit worker fixture did not record its native process relationship")
        with open(root_metadata_file, encoding="ascii") as handle:
            root_metadata = json.load(handle)
        root_pid = int(root_metadata["root_pid"])
        child_launch_pid = int(root_metadata["child_launch_pid"])
        child_pid = int(open(child_pid_file, encoding="ascii").read().strip())
        child_parent_pid = int(open(child_parent_pid_file, encoding="ascii").read().strip())
        if child_launch_pid != child_pid:
            raise SystemExit(
                f"[NG] native root Popen child PID {child_launch_pid} did not match survivor PID {child_pid}"
            )
        if child_parent_pid != root_pid:
            raise SystemExit(
                f"[NG] root-exit child parent PID {child_parent_pid} did not match fixture root PID {root_pid}"
            )
        if windows:
            console_process_ids = (wintypes.DWORD * 256)()
            console_process_count = kernel.GetConsoleProcessList(console_process_ids, len(console_process_ids))
            console_members = console_process_ids[:console_process_count]
            if console_process_count == 0 or console_process_count > len(console_process_ids) or \
                    os.getpid() not in console_members or process.pid not in console_members:
                raise SystemExit("[NG] controller and checker do not share the fixture-owned root-exit console")
            for pid in (root_pid, child_pid):
                handle = kernel.OpenProcess(0x00100401, False, pid)  # SYNCHRONIZE | PROCESS_QUERY_INFORMATION | PROCESS_TERMINATE
                if not handle:
                    raise SystemExit(f"[NG] could not hold native root-exit process PID {pid} (WinError {ctypes.get_last_error()})")
                held_handles.append(handle)
            membership = []
            for handle in held_handles:
                in_job = wintypes.BOOL()
                if not kernel.IsProcessInJob(handle, None, ctypes.byref(in_job)):
                    raise SystemExit(f"[NG] could not inspect native Job membership (WinError {ctypes.get_last_error()})")
                membership.append(bool(in_job.value))
            if membership != [True, True]:
                raise SystemExit(f"[NG] root and child were not both in a native Job before root exit: {membership}")
            for pid, handle in zip((root_pid, child_pid), held_handles):
                wait_result = kernel.WaitForSingleObject(handle, 0)
                if wait_result != 0x00000102:
                    raise SystemExit(f"[NG] native root-exit fixture PID {pid} was not live before root release (wait={wait_result})")
        elif not inspect_posix_pid(root_pid, liveness_checker) or not inspect_posix_pid(child_pid, liveness_checker):
            raise SystemExit("[NG] root-exit fixture root and child must both be live before root release")
        with open(release_file, "w", encoding="ascii") as handle:
            handle.write("root may exit\n")
        output, _ = process.communicate(timeout=8.0)
        if process.returncode != expected_status:
            raise SystemExit(
                f"[NG] worker root exit {worker_exit} returned {process.returncode}, expected {expected_status}\n{output}"
            )
        if "[OK] snapshot is checksum-valid" in output:
            raise SystemExit("[NG] root-exit worker fixture emitted the final success marker")
        if windows:
            expected_diagnostic = f"worker root exited with status {worker_exit} while its owned Job still had "
            if expected_diagnostic not in output:
                raise SystemExit(
                    f"[NG] root-exit worker {worker_exit} omitted its owned-Job cleanup diagnostic\n{output}"
                )
        with open(child_release_file, "w", encoding="ascii") as handle:
            handle.write("release only after checker completion\n")
        time.sleep(0.1)
        if os.path.exists(marker):
            raise SystemExit(f"[NG] root-exit worker child {child_pid} survived and touched scratch state")
        if windows:
            for pid, handle in zip((root_pid, child_pid), held_handles):
                wait_result = kernel.WaitForSingleObject(handle, 0)
                if wait_result != 0:
                    raise SystemExit(f"[NG] held native root-exit process PID {pid} is still active (wait={wait_result})")
            print(
                f"[OK] root-exit Job cleanup root_pid={root_pid} child_pid={child_pid} "
                f"child_parent_pid={child_parent_pid} any_job_membership=confirmed"
            )
        else:
            reap_deadline = time.monotonic() + 3.0
            while time.monotonic() < reap_deadline and (
                inspect_posix_pid(root_pid, liveness_checker) or inspect_posix_pid(child_pid, liveness_checker)
            ):
                time.sleep(0.05)
            if inspect_posix_pid(root_pid, liveness_checker) or inspect_posix_pid(child_pid, liveness_checker):
                raise SystemExit(f"[NG] root-exit process group still contains root {root_pid} or child {child_pid}")
            print(
                f"[OK] root-exit process-group cleanup root_pid={root_pid} child_pid={child_pid} "
                f"child_parent_pid={child_parent_pid}"
            )
    finally:
        for path in (release_file, child_release_file):
            try:
                if not os.path.exists(path):
                    with open(path, "w", encoding="ascii") as handle:
                        handle.write("release during fixture cleanup\n")
            except OSError as error:
                cleanup_errors.append(f"could not release root-exit fixture through {path}: {error}")
        if process is not None and process.poll() is None:
            try:
                if windows:
                    process.send_signal(signal.CTRL_BREAK_EVENT)
                else:
                    os.killpg(process.pid, signal.SIGTERM)
            except (OSError, ValueError) as error:
                cleanup_errors.append(f"could not signal the owned root-exit checker: {error}")
            try:
                process.communicate(timeout=8.0)
            except subprocess.TimeoutExpired:
                cleanup_errors.append("owned root-exit checker did not exit after its supported interruption")
                if windows:
                    for pid, handle in zip((root_pid, child_pid), held_handles):
                        if kernel.WaitForSingleObject(handle, 0) == 0x00000102:
                            if not kernel.TerminateProcess(handle, 1):
                                cleanup_errors.append(
                                    f"could not terminate held root-exit process PID {pid} "
                                    f"(WinError {ctypes.get_last_error()})"
                                )
                            elif kernel.WaitForSingleObject(handle, 5000) != 0:
                                cleanup_errors.append(f"held root-exit process PID {pid} did not exit")
                    try:
                        process.kill()
                        process.wait(timeout=3.0)
                    except (OSError, subprocess.TimeoutExpired) as error:
                        cleanup_errors.append(f"could not reap the owned root-exit checker: {error}")
                else:
                    try:
                        os.killpg(process.pid, signal.SIGKILL)
                        process.wait(timeout=3.0)
                    except (OSError, subprocess.TimeoutExpired) as error:
                        cleanup_errors.append(f"could not reap the root-exit process group: {error}")
        for handle in held_handles:
            if not kernel.CloseHandle(handle):
                cleanup_errors.append(f"could not close a held root-exit process handle (WinError {ctypes.get_last_error()})")
        held_handles.clear()
        if console_attached and not kernel.FreeConsole():
            cleanup_errors.append(f"could not release the owned root-exit console (WinError {ctypes.get_last_error()})")
        if cleanup_errors:
            message = "[NG] root-exit fixture cleanup was not confirmed: " + "; ".join(cleanup_errors)
            if sys.exc_info()[0] is None:
                raise SystemExit(message)
            print(message, file=sys.stderr)
PY
assert_no_restore_scratch "${test_root}/snapshot" "root-exit worker cleanup"

interrupt_python=${test_root}/interrupt-python
cat > "${interrupt_python}" <<'SH'
#!/usr/bin/env bash
if [[ ${1:-} == - && ${2:-} == *l1.db && ${3:-} == *l2.db ]]; then
  python3 "${RENCROW_TEST_SURVIVOR_SCRIPT}" \
    "${RENCROW_TEST_SURVIVOR_MARKER}" "${RENCROW_TEST_SURVIVOR_PID_FILE}" &
  wait
fi
exec python3 "$@"
SH
chmod +x "${interrupt_python}"
interrupt_marker=${test_root}/interrupt-survivor.marker
interrupt_pid_file=${test_root}/interrupt-survivor.pid
python3 - "${current_bash_executable}" "${checker}" "${test_root}/snapshot" "${interrupt_python}" \
  "${interrupt_marker}" "${interrupt_pid_file}" "${survivor_python}" \
  "${process_liveness_checker}" <<'PY'
import ctypes
import os
import signal
import subprocess
import sys
import time

def bash_path(value):
    if os.name != "nt":
        return value
    if len(value) > 2 and value[1] == ":" and value[2] in "\\/":
        return "/" + value[0].lower() + "/" + value[3:].replace("\\", "/")
    if value.startswith("\\\\"):
        return value.replace("\\", "/")
    return value

def process_is_alive(pid, liveness_checker):
    result = subprocess.run(
        [sys.executable, liveness_checker, str(pid)],
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        shell=False,
        timeout=3.0,
        check=False,
        text=True,
    )
    if result.returncode != 0:
        raise SystemExit(f"[NG] could not inspect interrupted worker PID {pid}: {result.stderr.strip()}")
    return result.stdout.strip() == "alive"

def emit_scratch_diagnostics(snapshot, checker_status, output, force=False):
    max_scratch_dirs = 32
    max_worker_inputs = 8
    max_output_bytes = 8192
    scratch_dirs = []
    more_scratch_dirs = False
    try:
        with os.scandir(snapshot) as entries:
            for entry in entries:
                if not entry.name.startswith(".restore.") or not entry.is_dir(follow_symlinks=False):
                    continue
                if len(scratch_dirs) == max_scratch_dirs:
                    more_scratch_dirs = True
                    break
                scratch_dirs.append(entry.name)
    except OSError as error:
        print(f"[DIAG] restore scratch directory listing failed: {error}", file=sys.stderr)
        return False

    if not scratch_dirs and not more_scratch_dirs and not force:
        return False

    print(
        f"[DIAG] interrupted checker status={checker_status} "
        f"status_u32=0x{checker_status & 0xFFFFFFFF:08x}",
        file=sys.stderr,
    )
    print(
        f"[DIAG] direct restore scratch dirs={scratch_dirs!r} "
        f"additional_dirs={more_scratch_dirs}",
        file=sys.stderr,
    )
    for scratch_name in scratch_dirs:
        scratch_path = os.path.join(snapshot, scratch_name)
        try:
            marker_present = os.path.exists(
                os.path.join(scratch_path, ".sqlite-integrity-supervision-active")
            )
        except OSError:
            marker_present = "unavailable"
        worker_inputs = []
        more_worker_inputs = False
        try:
            with os.scandir(scratch_path) as entries:
                for entry in entries:
                    if (
                        not entry.name.startswith(".sqlite-integrity-worker-")
                        or not entry.name.endswith(".py")
                        or not entry.is_file(follow_symlinks=False)
                    ):
                        continue
                    if len(worker_inputs) == max_worker_inputs:
                        more_worker_inputs = True
                        break
                    worker_inputs.append(entry.name)
        except OSError:
            more_worker_inputs = "unavailable"
        print(
            f"[DIAG] scratch={scratch_name!r} supervision_marker={marker_present} "
            f"worker_inputs={worker_inputs!r} additional_worker_inputs={more_worker_inputs}",
            file=sys.stderr,
        )

    output_bytes = output.encode("utf-8", errors="replace")
    print("[DIAG] captured checker output (maximum 8192 bytes):", file=sys.stderr)
    sys.stderr.write(output_bytes[:max_output_bytes].decode("utf-8", errors="replace"))
    if len(output_bytes) > max_output_bytes:
        sys.stderr.write("\n[DIAG] captured checker output truncated\n")
    elif output_bytes and not output_bytes.endswith(b"\n"):
        sys.stderr.write("\n")
    return bool(scratch_dirs or more_scratch_dirs)

current_bash_executable, checker, snapshot, worker, marker, pid_file, survivor_script, liveness_checker = sys.argv[1:]
windows = os.name == "nt"
if (
    not os.path.isabs(current_bash_executable)
    or not os.path.isfile(current_bash_executable)
    or (windows and not current_bash_executable.lower().endswith(".exe"))
    or (not windows and not os.access(current_bash_executable, os.X_OK))
):
    raise SystemExit("[NG] selected Bash executable is not an absolute executable file")
environment = os.environ.copy()
environment.update({
    "RENCROW_TEST_PYTHON": bash_path(worker),
    "RENCROW_TEST_SURVIVOR_MARKER": bash_path(marker),
    "RENCROW_TEST_SURVIVOR_PID_FILE": bash_path(pid_file),
    "RENCROW_TEST_SURVIVOR_SCRIPT": bash_path(survivor_script),
    "RENCROW_SQLITE_INTEGRITY_TIMEOUT_SEC": "30",
})
checker_command = [current_bash_executable, bash_path(checker), bash_path(snapshot)]
kernel = None
console_attached = False
child_handle = None
if windows:
    from ctypes import wintypes

    kernel = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel.OpenProcess.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
    kernel.OpenProcess.restype = wintypes.HANDLE
    kernel.WaitForSingleObject.argtypes = [wintypes.HANDLE, wintypes.DWORD]
    kernel.WaitForSingleObject.restype = wintypes.DWORD
    kernel.TerminateProcess.argtypes = [wintypes.HANDLE, wintypes.UINT]
    kernel.TerminateProcess.restype = wintypes.BOOL
    kernel.CloseHandle.argtypes = [wintypes.HANDLE]
    kernel.CloseHandle.restype = wintypes.BOOL
    kernel.FreeConsole.argtypes = []
    kernel.FreeConsole.restype = wintypes.BOOL
    kernel.AllocConsole.argtypes = []
    kernel.AllocConsole.restype = wintypes.BOOL
    kernel.GetConsoleProcessList.argtypes = [ctypes.POINTER(wintypes.DWORD), wintypes.DWORD]
    kernel.GetConsoleProcessList.restype = wintypes.DWORD
    kernel.GenerateConsoleCtrlEvent.argtypes = [wintypes.DWORD, wintypes.DWORD]
    kernel.GenerateConsoleCtrlEvent.restype = wintypes.BOOL
    if not kernel.FreeConsole():
        raise SystemExit(f"[NG] could not detach interruption fixture from inherited console (WinError {ctypes.get_last_error()})")
    if not kernel.AllocConsole():
        raise SystemExit(f"[NG] could not allocate an owned interruption console (WinError {ctypes.get_last_error()})")
    console_attached = True
process = None
child_pid = None
try:
    process = subprocess.Popen(
        checker_command,
        env=environment,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        start_new_session=not windows,
        creationflags=0x00000200 if windows else 0,  # CREATE_NEW_PROCESS_GROUP on the owned console
        text=True,
    )
    deadline = time.monotonic() + 8.0
    child_pid = None
    pid_record_ready = False
    pid_record_detail = "worker descendant PID record is not available"
    console_members = []
    console_count = None
    console_ready = not windows
    expected_console_members = (("controller", os.getpid()), ("checker", process.pid))
    while time.monotonic() < deadline:
        if process.poll() is not None:
            break
        if not pid_record_ready and os.path.isfile(pid_file):
            try:
                with open(pid_file, encoding="ascii") as handle:
                    pid_record = handle.read()
            except OSError as error:
                pid_record_detail = f"worker descendant PID record could not be read: {error}"
            else:
                if pid_record.endswith("\n"):
                    pid_text = pid_record[:-1]
                    if not pid_text or not pid_text.isascii() or not pid_text.isdigit():
                        raise SystemExit(f"[NG] worker descendant PID record is invalid: {pid_record!r}")
                    child_pid = int(pid_text)
                    if not 0 < child_pid <= 0xFFFFFFFF:
                        raise SystemExit(f"[NG] worker descendant PID is outside native DWORD range: {child_pid}")
                    pid_record_ready = True
                    pid_record_detail = "worker descendant PID record is complete"
                else:
                    pid_record_detail = "worker descendant PID record is incomplete"
        if windows:
            process_ids = (wintypes.DWORD * 256)()
            console_count = kernel.GetConsoleProcessList(process_ids, len(process_ids))
            if console_count == 0:
                raise SystemExit(
                    f"[NG] GetConsoleProcessList failed during interruption readiness "
                    f"(WinError {ctypes.get_last_error()}, count=0, "
                    f"expected={[f'{label}={pid}' for label, pid in expected_console_members]})"
                )
            if console_count > len(process_ids):
                raise SystemExit(
                    f"[NG] GetConsoleProcessList exceeded interruption readiness capacity "
                    f"(count={console_count}, capacity={len(process_ids)}, WinError {ctypes.get_last_error()}, "
                    f"expected={[f'{label}={pid}' for label, pid in expected_console_members]})"
                )
            console_members = list(process_ids[:console_count])
            console_ready = all(pid in console_members for _, pid in expected_console_members)
        if pid_record_ready and console_ready:
            break
        time.sleep(0.05)
    checker_status = process.poll()
    if checker_status is not None:
        missing_console_members = [
            f"{label}={pid}" for label, pid in expected_console_members
            if windows and pid not in console_members
        ]
        raise SystemExit(
            f"[NG] interruption fixture checker exited before readiness "
            f"(status={checker_status}, pid_ready={pid_record_ready}, pid_detail={pid_record_detail}, "
            f"missing_console_members={missing_console_members}, console_members={console_members})"
        )
    if not pid_record_ready:
        raise SystemExit(f"[NG] interruption fixture readiness timed out: {pid_record_detail}")
    if windows and not console_ready:
        missing_console_members = [
            f"{label}={pid}" for label, pid in expected_console_members
            if pid not in console_members
        ]
        raise SystemExit(
            f"[NG] interruption fixture readiness timed out waiting for console members "
            f"{missing_console_members}; count={console_count}"
        )
    if windows:
        child_handle = kernel.OpenProcess(0x00100001, False, child_pid)  # SYNCHRONIZE | PROCESS_TERMINATE
        if not child_handle:
            raise SystemExit(f"[NG] could not hold the native interrupted worker PID {child_pid} (WinError {ctypes.get_last_error()})")
        child_state = kernel.WaitForSingleObject(child_handle, 0)
        if child_state != 0x00000102:  # WAIT_TIMEOUT: the held child must still be alive before interruption
            raise SystemExit(
                f"[NG] held native interrupted worker PID {child_pid} was not live before CTRL_BREAK "
                f"(wait={child_state}, WinError={ctypes.get_last_error()})"
            )
        if process.poll() is not None:
            raise SystemExit(f"[NG] checker exited before CTRL_BREAK (status={process.returncode})")
        child_state = kernel.WaitForSingleObject(child_handle, 0)
        if child_state != 0x00000102:
            raise SystemExit(
                f"[NG] held native interrupted worker PID {child_pid} exited before CTRL_BREAK "
                f"(wait={child_state}, WinError={ctypes.get_last_error()})"
            )
        process_ids = (wintypes.DWORD * 256)()
        console_count = kernel.GetConsoleProcessList(process_ids, len(process_ids))
        if console_count == 0:
            raise SystemExit(
                f"[NG] GetConsoleProcessList failed before CTRL_BREAK "
                f"(WinError {ctypes.get_last_error()}, count=0, "
                f"expected={[f'{label}={pid}' for label, pid in expected_console_members]})"
            )
        if console_count > len(process_ids):
            raise SystemExit(
                f"[NG] GetConsoleProcessList exceeded capacity before CTRL_BREAK "
                f"(count={console_count}, capacity={len(process_ids)}, WinError {ctypes.get_last_error()}, "
                f"expected={[f'{label}={pid}' for label, pid in expected_console_members]})"
            )
        console_members = list(process_ids[:console_count])
        missing_console_members = [
            f"{label}={pid}" for label, pid in expected_console_members
            if pid not in console_members
        ]
        if missing_console_members:
            raise SystemExit(
                f"[NG] interruption fixture lost console membership before CTRL_BREAK "
                f"for {missing_console_members}; count={console_count}, members={console_members}"
            )
        if process.poll() is not None:
            raise SystemExit(f"[NG] checker exited before CTRL_BREAK (status={process.returncode})")
        if not kernel.GenerateConsoleCtrlEvent(1, process.pid):  # CTRL_BREAK_EVENT
            raise SystemExit(f"[NG] could not deliver CTRL_BREAK to attached checker group (WinError {ctypes.get_last_error()})")
    else:
        os.killpg(process.pid, signal.SIGTERM)
    output, _ = process.communicate(timeout=8.0)
    if process.returncode == 0:
        emit_scratch_diagnostics(snapshot, process.returncode, output, force=True)
        raise SystemExit("[NG] checker succeeded after its supervisor process group was interrupted\n" + output)
    time.sleep(5.2)
    if os.path.exists(marker):
        raise SystemExit("[NG] interrupted SQLite worker descendant survived and touched scratch state")
    if windows:
        child_state = kernel.WaitForSingleObject(child_handle, 0)
        if child_state == 0x00000102:  # WAIT_TIMEOUT
            raise SystemExit(f"[NG] interrupted SQLite worker descendant {child_pid} survived")
        if child_state != 0:
            raise SystemExit(f"[NG] cannot confirm native interrupted worker PID {child_pid} exit (wait={child_state})")
    elif process_is_alive(child_pid, liveness_checker):
        raise SystemExit(f"[NG] interrupted SQLite worker descendant {child_pid} survived")
    if "[OK] snapshot is checksum-valid" in output:
        emit_scratch_diagnostics(snapshot, process.returncode, output, force=True)
        raise SystemExit("[NG] interrupted SQLite validation emitted the final success marker")
    emit_scratch_diagnostics(snapshot, process.returncode, output)
finally:
    if process is not None and process.poll() is None:
        if windows:
            system_root = os.environ.get("SystemRoot", "")
            taskkill = os.path.join(system_root, "System32", "taskkill.exe")
            if not os.path.isabs(system_root) or not os.path.isfile(taskkill):
                raise SystemExit("[NG] cannot prove fixed SystemRoot taskkill.exe for owned checker cleanup")
            subprocess.run([taskkill, "/PID", str(process.pid), "/T", "/F"],
                           stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                           stderr=subprocess.DEVNULL, shell=False, timeout=4.0, check=False)
            try:
                process.wait(timeout=4.0)
            except subprocess.TimeoutExpired:
                raise SystemExit("[NG] owned checker process tree survived bounded taskkill cleanup")
        else:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            process.wait(timeout=3.0)
    if child_pid is not None:
        if windows and child_handle:
            child_state = kernel.WaitForSingleObject(child_handle, 0)
            if child_state == 0x00000102:  # WAIT_TIMEOUT: terminate the held process object, not a reused PID
                if not kernel.TerminateProcess(child_handle, 1):
                    raise SystemExit(f"[NG] could not terminate held interrupted worker PID {child_pid} (WinError {ctypes.get_last_error()})")
                child_state = kernel.WaitForSingleObject(child_handle, 5000)
                if child_state != 0:
                    raise SystemExit(f"[NG] held interrupted worker PID {child_pid} was not reaped (wait={child_state})")
        elif not windows and process_is_alive(child_pid, liveness_checker):
            os.kill(child_pid, signal.SIGKILL)
    if child_handle:
        kernel.CloseHandle(child_handle)
    if console_attached:
        kernel.FreeConsole()
PY
assert_no_restore_scratch "${test_root}/snapshot" "supervisor interruption"
echo "[OK] supervisor interruption reaped the SQLite worker tree"

legacy_snapshot=${test_root}/legacy-v3
mkdir -p "${legacy_snapshot}"
cp "${test_root}/snapshot/rencrow-state.tar.gz" "${legacy_snapshot}/rencrow-state.tar.gz"
sed -e 's/^format_version=4$/format_version=3/' -e '/^raw_source_relative=/d' "${test_root}/snapshot/manifest.txt" > "${legacy_snapshot}/manifest.txt"
(cd "${legacy_snapshot}" && sha256sum rencrow-state.tar.gz > SHA256SUMS)
"${checker}" "${legacy_snapshot}"
assert_no_restore_scratch "${legacy_snapshot}" "legacy format_version=3 success"

legacy_v2_snapshot=${test_root}/legacy-v2
mkdir -p "${legacy_v2_snapshot}"
cp "${test_root}/snapshot/rencrow-state.tar.gz" "${legacy_v2_snapshot}/rencrow-state.tar.gz"
sed -e 's/^format_version=4$/format_version=2/' -e '/^raw_source_relative=/d' "${test_root}/snapshot/manifest.txt" > "${legacy_v2_snapshot}/manifest.txt"
(cd "${legacy_v2_snapshot}" && sha256sum rencrow-state.tar.gz > SHA256SUMS)
"${checker}" "${legacy_v2_snapshot}"
assert_no_restore_scratch "${legacy_v2_snapshot}" "legacy format_version=2 success"

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
  assert_no_restore_scratch "${bad_root}" "${mode} Common Raw rejection"
}

make_bad_snapshot missing
make_bad_snapshot tampered
echo "[OK] storage backup contract test passed"
