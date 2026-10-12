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
assert_not_contains "${checker}" \
  '"bash.exe"' \
  "native restore supervisor must not rediscover Bash by bare name"

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
with open(pid_file, "w", encoding="ascii") as handle:
    handle.write(str(os.getpid()) + "\n")
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
    if windows:
        process_ids = (wintypes.DWORD * 256)()
        process_count = kernel.GetConsoleProcessList(process_ids, len(process_ids))
        console_members = process_ids[:process_count]
        if process_count == 0 or process_count > len(process_ids) or \
                os.getpid() not in console_members or process.pid not in console_members:
            raise SystemExit("[NG] controller and checker do not share the fixture-owned console")
    deadline = time.monotonic() + 8.0
    while time.monotonic() < deadline and process.poll() is None and not os.path.isfile(pid_file):
        time.sleep(0.05)
    if not os.path.isfile(pid_file):
        raise SystemExit("[NG] interrupted SQLite worker did not record its descendant PID")
    with open(pid_file, encoding="ascii") as handle:
        child_pid = int(handle.read().strip())
    if windows:
        child_handle = kernel.OpenProcess(0x00100001, False, child_pid)  # SYNCHRONIZE | PROCESS_TERMINATE
        if not child_handle:
            raise SystemExit(f"[NG] could not hold the native interrupted worker PID {child_pid} (WinError {ctypes.get_last_error()})")
        if not kernel.GenerateConsoleCtrlEvent(1, process.pid):  # CTRL_BREAK_EVENT
            raise SystemExit(f"[NG] could not deliver CTRL_BREAK to attached checker group (WinError {ctypes.get_last_error()})")
    else:
        os.killpg(process.pid, signal.SIGTERM)
    output, _ = process.communicate(timeout=8.0)
    if process.returncode == 0:
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
        raise SystemExit("[NG] interrupted SQLite validation emitted the final success marker")
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
