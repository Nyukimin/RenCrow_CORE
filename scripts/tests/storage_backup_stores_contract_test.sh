#!/usr/bin/env bash
# Contract for the Task store and the Action store in the daily storage backup.
#
# The two stores live under workspace_dir (outside backup.core_source) and were
# never part of the backup. This test holds the whole path: the runner archives
# them, the restore check extracts the archive into a scratch workspace and runs
# the owner CLIs (`rencrow tasks verify`, `rencrow actions verify`) on the copy,
# and anything short of a verified copy keeps the cohort from being published.
#
# It needs a `rencrow` binary: RENCROW_TEST_BINARY, or it builds ./cmd/rencrow
# with the Go toolchain. The runner itself needs systemd, docker and a mounted
# backup medium, so its archive step is a function that this test extracts from
# the script and runs for real.
set -Eeuo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
checker=${repo_root}/scripts/rencrow-storage-restore-check
backup_runner=${repo_root}/scripts/rencrow-storage-backup
test_tmp_root=${repo_root}/Tmp/test-runtime
mkdir -p "${test_tmp_root}"
test_root=$(mktemp -d "${test_tmp_root}/storage-backup-stores.XXXXXX")
cleanup_test_root() {
  chmod -R u+w "${test_root}" 2>/dev/null || true
  rm -rf -- "${test_root}"
}
trap cleanup_test_root EXIT

bash -n "${checker}"
bash -n "${backup_runner}"

native_path() {
  case ${OSTYPE:-} in
    msys*|cygwin*|mingw*) cygpath -m "$1" ;;
    *) printf '%s\n' "$1" ;;
  esac
}

contract_failures=()
assert_contains() {
  if ! grep -Fq -- "$2" "$1"; then
    contract_failures+=("$3")
  fi
}
assert_not_contains() {
  if grep -Fq -- "$2" "$1"; then
    contract_failures+=("$3")
  fi
}
line_of() {
  { grep -nF -- "$2" "$1" || true; } | head -n 1 | cut -d: -f1
}

# ---- the runner's source contract -------------------------------------------
assert_contains "${backup_runner}" \
	'if (( ${#backup_config[@]} != 25 )); then' \
	"backup-values must carry the workspace, both store roots, and CharImage history path (25 values)"
assert_contains "${backup_runner}" \
  'workspace_dir=${backup_config[21]}' \
  "the workspace root must come from the CORE config, not from a literal"
assert_contains "${backup_runner}" \
  'task_store_root=${backup_config[22]}' \
  "the Task store root must come from the CORE config"
assert_contains "${backup_runner}" \
  'action_store_root=${backup_config[23]}' \
  "the Action store root must come from the CORE config"
assert_contains "${backup_runner}" \
  'archive_workspace_stores() {' \
  "the store archive must be one function that the contract test can run"
assert_contains "${backup_runner}" \
  '--exclude="${task_store_relative}/index"' \
  "the Task store index is derived from the log and stays out of the backup"
assert_not_contains "${backup_runner}" \
  '--exclude="${task_store_relative}/.writer.lock"' \
  "the Task store writer lock holds the writer generation and must be backed up"
assert_contains "${backup_runner}" \
  'workspace_stores_archive=${workspace_stores_name}' \
  "the manifest must name the store archive"
assert_contains "${backup_runner}" \
  '[OK] workspace stores verified' \
  "a cohort with stores is published only after the restore check says it verified them"
assert_contains "${backup_runner}" \
  'RENCROW_BINARY="${rencrow_binary}"' \
  "the restore check must run the same rencrow binary that read the config"
assert_contains "${backup_runner}" \
  'workspace stores are missing or outside workspace_dir' \
  "store locations must be validated before CORE is stopped"

trap_line=$(line_of "${backup_runner}" 'trap cleanup EXIT INT TERM')
mask_line=$(line_of "${backup_runner}" 'systemctl --user mask --runtime --now rencrow.service')
stores_line=$(line_of "${backup_runner}" 'archive_workspace_stores "${workspace_dir}"')
restart_line=$(line_of "${backup_runner}" 'systemctl --user start rencrow.service')
preflight_line=$(line_of "${backup_runner}" 'workspace stores are missing or outside workspace_dir')
for number in "${trap_line}" "${mask_line}" "${stores_line}" "${restart_line}" "${preflight_line}"; do
  if [[ ! ${number} =~ ^[0-9]+$ ]]; then
    contract_failures+=("store backup ordering markers are missing from the runner")
    break
  fi
done
if [[ ${trap_line} =~ ^[0-9]+$ && ${mask_line} =~ ^[0-9]+$ && ${stores_line} =~ ^[0-9]+$ ]]; then
  # The restart in cleanup() is the first `systemctl --user start rencrow.service`;
  # the second is the normal path after the archives. The store archive must sit
  # after the trap (every failure then restarts CORE) and inside the stop window.
  second_restart_line=$({ grep -nF -- 'systemctl --user start rencrow.service' "${backup_runner}" || true; } | sed -n 2p | cut -d: -f1)
  if (( trap_line >= stores_line )) || (( mask_line >= stores_line )) || \
    [[ ! ${second_restart_line} =~ ^[0-9]+$ ]] || (( stores_line >= second_restart_line )); then
    contract_failures+=("the store archive must run after the cleanup trap and before CORE is restarted")
  fi
  if (( preflight_line >= mask_line )); then
    contract_failures+=("store locations must be validated before CORE is masked")
  fi
fi
if ! grep -B3 -F 'archive_workspace_stores "${workspace_dir}"' "${backup_runner}" | grep -Fq 'core_export'; then
  contract_failures+=("CORE-only migration export must not carry the stores (its cohort is fixed)")
fi

assert_contains "${checker}" \
  'workspace_stores_archive=$(manifest_value workspace_stores_archive)' \
  "the restore check must read the store archive from the manifest"
assert_contains "${checker}" \
  '[OK] workspace stores verified' \
  "the restore check must say that it verified the stores"
assert_contains "${checker}" \
  'RENCROW_STORE_VERIFY_TIMEOUT_SEC' \
  "store verification must have a bounded deadline"
assert_not_contains "${checker}" \
  '--check-runs' \
  "orphaned Runs are legitimate once Tasks are retired, so the restore check does not ask for them"

if (( ${#contract_failures[@]} > 0 )); then
  printf '[RED] storage backup stores contract violations:\n' >&2
  printf ' - %s\n' "${contract_failures[@]}" >&2
  exit 1
fi

# ---- a rencrow binary -------------------------------------------------------
exe_suffix=
case ${OSTYPE:-} in
  msys*|cygwin*|mingw*) exe_suffix=.exe ;;
esac
rencrow_binary=${RENCROW_TEST_BINARY:-}
if [[ -z ${rencrow_binary} ]]; then
  if ! command -v go >/dev/null 2>&1; then
    echo "[NG] set RENCROW_TEST_BINARY to a rencrow binary, or install the Go toolchain" >&2
    exit 2
  fi
  mkdir -p "${test_root}/bin"
  rencrow_binary=${test_root}/bin/rencrow${exe_suffix}
  (cd "${repo_root}" && go build -o "$(native_path "${rencrow_binary}")" ./cmd/rencrow)
fi
if [[ ! -x ${rencrow_binary} ]]; then
  echo "[NG] rencrow binary is not executable: ${rencrow_binary}" >&2
  exit 2
fi
python_bin=${RENCROW_TEST_PYTHON:-python3}

# flock(1) is util-linux: the runner has it (it is one of its required commands),
# macOS and Git Bash do not. Without it, a stand-in takes the same OS lock through
# Python's fcntl, so the function under test runs unchanged. Where Python has no
# fcntl (Windows) the stand-in cannot lock; it succeeds, and the held-lock case
# below is skipped.
real_lock=no
if "${python_bin}" -I -c 'import fcntl' >/dev/null 2>&1; then
  real_lock=yes
fi
if ! command -v flock >/dev/null 2>&1; then
  mkdir -p "${test_root}/shim"
  if [[ ${real_lock} == yes ]]; then
    printf '#!%s\n' "$(command -v "${python_bin}")" > "${test_root}/shim/flock"
    cat >> "${test_root}/shim/flock" <<'PY'
import fcntl
import sys

args = sys.argv[1:]
if len(args) != 2 or args[0] != "-n":
    raise SystemExit(64)
try:
    fcntl.flock(int(args[1]), fcntl.LOCK_EX | fcntl.LOCK_NB)
except OSError:
    raise SystemExit(1)
PY
  else
    printf '#!/usr/bin/env bash\nexit 0\n' > "${test_root}/shim/flock"
  fi
  chmod +x "${test_root}/shim/flock"
  PATH=${test_root}/shim:${PATH}
fi

# ---- stores in the layout the runtime writes --------------------------------
cat > "${test_root}/make_stores.py" <<'PY'
"""Builds a Task store and an Action store in the layout-v2 shape the runtime writes.

Segment 0 is frozen (read-only, sizes recorded in LAYOUT), one day segment sits
under logs/, each segment has a WAL, and the Task store has its writer-generation
file and a derived index. IDs are UUIDv5, so two runs give the same bytes.
"""
import hashlib
import json
import os
import sys
import uuid

workspace = sys.argv[1]
NS = uuid.UUID("6f1b3f0e-8d52-5d0e-9c2a-0a1b2c3d4e5f")


def ident(prefix, label):
    return "%s_%s" % (prefix, uuid.uuid5(NS, label))


def write(path, data, mode):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "wb") as handle:
        handle.write(data)
    os.chmod(path, mode)


def lines(records):
    return b"".join(json.dumps(r, separators=(",", ":")).encode() + b"\n" for r in records)


def wal(files):
    out = []
    for name, payload in files:
        entry = {
            "name": name, "offset": 0, "append_length": len(payload),
            "prefix_sha256": hashlib.sha256(b"").hexdigest(), "payload_sha256": hashlib.sha256(payload).hexdigest(),
        }
        tx = hashlib.sha256(name.encode()).hexdigest()[:32]
        out.append(json.dumps({"version": 1, "kind": "prepare", "tx_id": tx, "files": [entry]}, separators=(",", ":")))
        out.append(json.dumps({"version": 1, "kind": "commit", "tx_id": tx}, separators=(",", ":")))
    return ("\n".join(out) + "\n").encode()


def task(n, status, at):
    return {
        "task_id": ident("tsk", "task-%d" % n), "title": "fixture task %d" % n, "route": "GENERAL",
        "status": status, "priority": "normal", "interrupt_policy": "notify_on_done_or_blocked",
        "read_only": False, "created_at": at, "updated_at": at,
    }


def action(n, status, at, done=None):
    value = {
        "action_id": ident("act", "action-%d" % n), "task_id": ident("tsk", "task-%d" % (n % 2)),
        "run_id": ident("run", "run-%d" % n), "kind": "tool", "name": "fixture-%d" % n, "status": status,
        "current_attempt_id": ident("att", "attempt-%d" % n), "created_at": at, "updated_at": done or at,
    }
    if done:
        value["completed_at"] = done
        value["summary"] = "done"
    return value


def attempt(n, status, at, done=None):
    value = {
        "attempt_id": ident("att", "attempt-%d" % n), "action_id": ident("act", "action-%d" % n),
        "start_reason": "first", "status": status, "started_at": at,
    }
    if done:
        value["completed_at"] = done
        value["summary"] = "done"
    return value


# ---- Task store
tasks = os.path.join(workspace, "tasks")
order = ["task_state.jsonl", "task_run.jsonl", "task_context.jsonl", "task_notifications.jsonl", "task_operation_receipts.jsonl", "task_execution_fences.jsonl"]
seg0 = {name: b"" for name in order}
seg0["task_state.jsonl"] = lines([task(0, "queued", "2026-10-12T10:00:00Z"), task(1, "queued", "2026-10-12T10:00:01Z"), task(2, "queued", "2026-10-12T10:00:02Z")])
for name in order:
    write(os.path.join(tasks, name), seg0[name], 0o444)
write(os.path.join(tasks, ".jsonlbatch.wal"), wal([("task_state.jsonl", seg0["task_state.jsonl"])]), 0o444)
write(os.path.join(tasks, ".jsonlbatch.lock"), b"", 0o600)
write(os.path.join(tasks, ".writer.lock"), b"1\n2\n", 0o600)
layout = "tasks-layout/v2\n" + "".join("frozen %s %d\n" % (name, len(seg0[name])) for name in order) + "end\n"
write(os.path.join(tasks, "LAYOUT"), layout.encode(), 0o644)
day = os.path.join(tasks, "logs", "20261013-01")
day_state = lines([task(3, "queued", "2026-10-13T08:00:03Z"), task(0, "succeeded", "2026-10-13T08:00:04Z")])
for name in order:
    write(os.path.join(day, name), day_state if name == "task_state.jsonl" else b"", 0o644)
write(os.path.join(day, ".jsonlbatch.wal"), wal([("task_state.jsonl", day_state)]), 0o600)
write(os.path.join(day, ".jsonlbatch.lock"), b"", 0o600)
write(os.path.join(tasks, "index", "tasks.tsi"), b"derived-index-that-the-backup-leaves-out", 0o600)

# ---- Action store
actions = os.path.join(workspace, "state", "actions")
a_state = lines([action(0, "open", "2026-10-13T01:00:00Z"), action(0, "succeeded", "2026-10-13T01:00:00Z", "2026-10-13T01:00:01Z")])
a_attempt = lines([attempt(0, "running", "2026-10-13T01:00:00Z"), attempt(0, "succeeded", "2026-10-13T01:00:00Z", "2026-10-13T01:00:01Z")])
write(os.path.join(actions, "action_state.jsonl"), a_state, 0o444)
write(os.path.join(actions, "action_attempt.jsonl"), a_attempt, 0o444)
write(os.path.join(actions, ".jsonlbatch.wal"), wal([("action_state.jsonl", a_state), ("action_attempt.jsonl", a_attempt)]), 0o444)
write(os.path.join(actions, ".jsonlbatch.lock"), b"", 0o600)
layout = "actions-layout/v2\nfrozen action_state.jsonl %d\nfrozen action_attempt.jsonl %d\nend\n" % (len(a_state), len(a_attempt))
write(os.path.join(actions, "LAYOUT"), layout.encode(), 0o644)
day = os.path.join(actions, "logs", "20261013-02")
d_state = lines([action(1, "open", "2026-10-13T08:00:00Z")])
d_attempt = lines([attempt(1, "running", "2026-10-13T08:00:00Z")])
write(os.path.join(day, "action_state.jsonl"), d_state, 0o644)
write(os.path.join(day, "action_attempt.jsonl"), d_attempt, 0o644)
write(os.path.join(day, ".jsonlbatch.wal"), wal([("action_state.jsonl", d_state), ("action_attempt.jsonl", d_attempt)]), 0o600)
write(os.path.join(day, ".jsonlbatch.lock"), b"", 0o600)
PY

workspace=${test_root}/workspace
"${python_bin}" -I "${test_root}/make_stores.py" "${workspace}"

# The owner CLIs read workspace_dir from a config: the smallest one that loads.
write_config() {
  local config=$1 workspace_dir=$2
  printf 'workspace_dir: "%s"\nserver:\n  port: 18790\n' "$(native_path "${workspace_dir}")" > "${config}"
}
run_cli() {
  local config=$1
  shift
  HOME="${test_root}/home" USERPROFILE="$(native_path "${test_root}/home")" RENCROW_CONFIG="$(native_path "${config}")" "${rencrow_binary}" "$@"
}
mkdir -p "${test_root}/home"
write_config "${test_root}/live.yaml" "${workspace}"
if ! run_cli "${test_root}/live.yaml" tasks verify --compact >"${test_root}/live-tasks.out" 2>&1 || \
  ! grep -Fq '"layout":"v2"' "${test_root}/live-tasks.out"; then
  cat "${test_root}/live-tasks.out" >&2
  echo "[NG] the layout-v2 Task store fixture is not a store that rencrow tasks verify accepts" >&2
  exit 1
fi
if ! run_cli "${test_root}/live.yaml" actions verify --compact >"${test_root}/live-actions.out" 2>&1 || \
  ! grep -Fq '"layout":"v2"' "${test_root}/live-actions.out"; then
  cat "${test_root}/live-actions.out" >&2
  echo "[NG] the layout-v2 Action store fixture is not a store that rencrow actions verify accepts" >&2
  exit 1
fi

# ---- the runner's archive step, run for real --------------------------------
extract_function() {
  awk -v name="$1" '$0 == name "() {" {on = 1} on {print} on && $0 == "}" {exit}' "$2"
}
archive_function=$(extract_function archive_workspace_stores "${backup_runner}")
if [[ -z ${archive_function} ]]; then
  echo "[NG] archive_workspace_stores is not defined in the runner" >&2
  exit 1
fi
eval "${archive_function}"

stores_archive=${test_root}/workspace-stores.tar.gz
archive_workspace_stores "${workspace}" "${stores_archive}" tasks state/actions
members=$(tar -tzf "${stores_archive}")
for expected in \
  tasks/LAYOUT tasks/.writer.lock tasks/.jsonlbatch.wal tasks/task_state.jsonl \
  tasks/logs/20261013-01/task_state.jsonl tasks/logs/20261013-01/.jsonlbatch.wal \
  state/actions/LAYOUT state/actions/.jsonlbatch.wal state/actions/action_state.jsonl \
  state/actions/logs/20261013-02/action_attempt.jsonl; do
  if ! grep -Fxq -- "${expected}" <<<"${members}"; then
    echo "[NG] the store archive is missing ${expected}" >&2
    exit 1
  fi
done
if grep -Eq '^tasks/index(/|$)' <<<"${members}"; then
  echo "[NG] the derived Task store index is in the archive" >&2
  exit 1
fi
# A frozen segment keeps its read-only mode (an older binary must still fail to
# open it for writing) wherever the file system keeps modes at all.
case ${OSTYPE:-} in
  msys*|cygwin*|mingw*)
    echo "[SKIP] frozen segment mode comparison (file modes are emulated on this OS)"
    ;;
  *)
    source_mode=$(ls -l "${workspace}/tasks/task_state.jsonl" | cut -c1-10)
    archive_mode=$(tar -tvzf "${stores_archive}" | grep -F ' tasks/task_state.jsonl' | awk '{print $1}')
    if [[ ${source_mode} != "${archive_mode}" || ${source_mode} != -r--r--r-- ]]; then
      echo "[NG] frozen segment mode ${source_mode} became ${archive_mode} in the archive" >&2
      exit 1
    fi
    ;;
esac

# A writer that holds the Task store lock keeps the archive from being taken; once
# it lets go, the same archive step goes through (the lock is not left behind).
if [[ ${real_lock} == yes ]]; then
  "${python_bin}" -I - "${workspace}/tasks/.writer.lock" "${test_root}/holder.ready" "${test_root}/holder.release" <<'PY' &
import fcntl
import os
import sys
import time

fd = os.open(sys.argv[1], os.O_RDWR)
fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
open(sys.argv[2], "w").close()
deadline = time.time() + 60
while not os.path.exists(sys.argv[3]) and time.time() < deadline:
    time.sleep(0.1)
PY
  holder_pid=$!
  for _ in $(seq 1 100); do
    [[ -e ${test_root}/holder.ready ]] && break
    sleep 0.1
  done
  if [[ ! -e ${test_root}/holder.ready ]]; then
    echo "[NG] the lock holder did not start" >&2
    exit 1
  fi
  if archive_workspace_stores "${workspace}" "${test_root}/held.tar.gz" tasks state/actions 2>"${test_root}/held.err"; then
    : > "${test_root}/holder.release"
    wait "${holder_pid}" || true
    echo "[NG] the stores were archived while a Task store writer held its lock" >&2
    exit 1
  fi
  : > "${test_root}/holder.release"
  wait "${holder_pid}" || true
  grep -Fq 'the Task store writer lock is held' "${test_root}/held.err"
  archive_workspace_stores "${workspace}" "${test_root}/released.tar.gz" tasks state/actions
fi

# A directory that is not there is an error, never an empty archive.
if archive_workspace_stores "${workspace}" "${test_root}/missing.tar.gz" tasks state/missing 2>/dev/null; then
  echo "[NG] archiving a missing store succeeded" >&2
  exit 1
fi
echo "[OK] store archive holds both stores, the writer generation and the WALs, and leaves the derived index out"

# ---- a cohort the restore check can read ------------------------------------
build_cohort() {
  local cohort=$1 with_stores=$2
  local source=${cohort}.src
  mkdir -p "${source}/state/sessions" "${source}/state/memory" "${source}/state/exports/parquet" \
    "${source}/state/raw-source/objects/sha256" "${cohort}"
  "${python_bin}" -I - "${source}/state/l1.db" "${source}/state/l2.db" <<'PY'
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
    connection.commit()
    connection.close()
PY
  tar -C "${source}" -czf "${cohort}/rencrow-state.tar.gz" state
  cat > "${cohort}/manifest.txt" <<EOF
format_version=4
core_name=state
session_relative=sessions
operation_memory_relative=memory
cold_export_relative=exports/parquet
raw_source_relative=raw-source
conversation_l1_relative=l1.db
conversation_archive_relative=l2.db
redis_export=disabled
qdrant_export=disabled
qdrant_sha256=disabled
EOF
  if [[ ${with_stores} == yes ]]; then
    cp "${stores_archive}" "${cohort}/workspace-stores.tar.gz"
    printf '%s\n' \
      'workspace_stores_archive=workspace-stores.tar.gz' \
      'task_store_relative=tasks' \
      'action_store_relative=state/actions' >> "${cohort}/manifest.txt"
    (cd "${cohort}" && sha256sum rencrow-state.tar.gz workspace-stores.tar.gz > SHA256SUMS)
  else
    (cd "${cohort}" && sha256sum rencrow-state.tar.gz > SHA256SUMS)
  fi
  rm -rf -- "${source}"
}

assert_no_restore_scratch() {
  if find "$1" -maxdepth 1 -type d -name '.restore.*' -print -quit | grep -q .; then
    echo "[NG] restore scratch directory remains after $2: $1" >&2
    exit 1
  fi
}

restore_check() {
  local cohort=$1 output=$2
  shift 2
  env RENCROW_BINARY="${rencrow_binary}" "$@" "${checker}" "${cohort}" >"${output}" 2>&1
}

expect_rejected() {
  local cohort=$1 description=$2 message=$3
  shift 3
  if restore_check "${cohort}" "${test_root}/rejected.out" "$@"; then
    cat "${test_root}/rejected.out" >&2
    echo "[NG] ${description} was accepted" >&2
    exit 1
  fi
  if ! grep -Fq -- "${message}" "${test_root}/rejected.out"; then
    cat "${test_root}/rejected.out" >&2
    echo "[NG] ${description} was rejected for another reason than: ${message}" >&2
    exit 1
  fi
  if grep -Fq '[OK] workspace stores verified' "${test_root}/rejected.out" || \
    grep -Fq '[OK] snapshot is checksum-valid' "${test_root}/rejected.out"; then
    echo "[NG] ${description} was promoted although it failed" >&2
    exit 1
  fi
  assert_no_restore_scratch "${cohort}" "${description}"
}

good=${test_root}/cohort-good
build_cohort "${good}" yes
before_sums=$(cd "${good}" && sha256sum rencrow-state.tar.gz workspace-stores.tar.gz)
if ! restore_check "${good}" "${test_root}/good.out"; then
  cat "${test_root}/good.out" >&2
  echo "[NG] a cohort with verified stores was rejected" >&2
  exit 1
fi
grep -Fq '[OK] workspace stores verified' "${test_root}/good.out"
grep -Fq '[OK] snapshot is checksum-valid, extractable, and restorable:' "${test_root}/good.out"
assert_no_restore_scratch "${good}" "successful store verification"
if [[ $(cd "${good}" && sha256sum rencrow-state.tar.gz workspace-stores.tar.gz) != "${before_sums}" ]]; then
  echo "[NG] the restore check changed the archives it checked" >&2
  exit 1
fi
if grep -E 'tsk_|act_|run_|att_' "${test_root}/good.out" >/dev/null; then
  echo "[NG] the restore check printed a record ID" >&2
  exit 1
fi
echo "[OK] restore check extracted the stores and ran tasks verify and actions verify on the copy"

# A cohort from before stores were backed up is still a valid cohort, and says so.
legacy=${test_root}/cohort-legacy
build_cohort "${legacy}" no
if ! restore_check "${legacy}" "${test_root}/legacy.out"; then
  cat "${test_root}/legacy.out" >&2
  echo "[NG] a cohort without stores (made before stores were backed up) was rejected" >&2
  exit 1
fi
if grep -Fq '[OK] workspace stores verified' "${test_root}/legacy.out"; then
  echo "[NG] a cohort without stores claimed to have verified stores" >&2
  exit 1
fi
grep -Fq 'no workspace stores' "${test_root}/legacy.out"
assert_no_restore_scratch "${legacy}" "legacy cohort"

# ---- what must not pass ------------------------------------------------------
# Each case repacks the archive with one defect and refreshes SHA256SUMS, so the
# defect is caught by the store verification and not by the checksum.
repack_stores() {
  local name=$1 mutate=$2
  local cohort=${test_root}/cohort-${name}
  build_cohort "${cohort}" yes
  local work=${cohort}.work
  mkdir -p "${work}"
  tar -xzf "${cohort}/workspace-stores.tar.gz" -C "${work}"
  chmod -R u+w "${work}"
  ( cd "${work}" && eval "${mutate}" )
  tar -C "${work}" -czf "${cohort}/workspace-stores.tar.gz" tasks state
  (cd "${cohort}" && sha256sum rencrow-state.tar.gz workspace-stores.tar.gz > SHA256SUMS)
  rm -rf -- "${work}"
  printf '%s\n' "${cohort}"
}

cohort=$(repack_stores truncated-task-segment 'head -c 100 tasks/task_state.jsonl > tasks/task_state.jsonl.cut && mv tasks/task_state.jsonl.cut tasks/task_state.jsonl')
expect_rejected "${cohort}" "a Task store whose frozen segment no longer matches LAYOUT" "[NG] Task store verification failed"

cohort=$(repack_stores truncated-action-segment 'head -c 50 state/actions/action_state.jsonl > state/actions/action_state.jsonl.cut && mv state/actions/action_state.jsonl.cut state/actions/action_state.jsonl')
expect_rejected "${cohort}" "an Action store whose frozen segment no longer matches LAYOUT" "[NG] Action store verification failed"

cohort=$(repack_stores missing-action-store 'rm -rf state/actions')
expect_rejected "${cohort}" "a cohort whose manifest names an Action store that the archive lacks" "[NG] workspace store is missing from the archive"

cohort=$(repack_stores unknown-layout 'printf "tasks-layout/v9\nend\n" > tasks/LAYOUT')
expect_rejected "${cohort}" "a Task store with an unknown LAYOUT" "[NG] Task store verification failed"

# The manifest names an archive that the checksum list does not cover.
cohort=${test_root}/cohort-unlisted
build_cohort "${cohort}" yes
(cd "${cohort}" && sha256sum rencrow-state.tar.gz > SHA256SUMS)
expect_rejected "${cohort}" "a store archive that SHA256SUMS does not list" "[NG] workspace store archive is not covered by SHA256SUMS"

# Keys that name stores without the archive, or stores that contain each other.
cohort=${test_root}/cohort-incomplete-keys
build_cohort "${cohort}" no
printf 'task_store_relative=tasks\n' >> "${cohort}/manifest.txt"
expect_rejected "${cohort}" "a manifest that names a store directory but no store archive" "[NG] incomplete workspace store keys in manifest"

cohort=${test_root}/cohort-overlapping-stores
build_cohort "${cohort}" yes
sed 's#^action_store_relative=.*#action_store_relative=tasks/logs#' "${cohort}/manifest.txt" > "${cohort}/manifest.new"
mv "${cohort}/manifest.new" "${cohort}/manifest.txt"
expect_rejected "${cohort}" "a manifest whose two store directories contain each other" "[NG] workspace store directories overlap in manifest"

# The manifest names a different file than the one the runner writes.
cohort=${test_root}/cohort-renamed
build_cohort "${cohort}" yes
sed 's/^workspace_stores_archive=.*/workspace_stores_archive=..\/outside.tar.gz/' "${cohort}/manifest.txt" > "${cohort}/manifest.new"
mv "${cohort}/manifest.new" "${cohort}/manifest.txt"
expect_rejected "${cohort}" "a store archive path that leaves the cohort" "[NG] unsupported workspace store archive name"

# An unusable rencrow binary never turns into a pass.
cohort=${test_root}/cohort-no-binary
build_cohort "${cohort}" yes
if env RENCROW_BINARY="${test_root}/no-such-rencrow" "${checker}" "${cohort}" >"${test_root}/no-binary.out" 2>&1; then
  echo "[NG] stores were accepted without a rencrow binary to verify them" >&2
  exit 1
fi
grep -Fq '[NG] rencrow binary for workspace store verification is not executable' "${test_root}/no-binary.out"
assert_no_restore_scratch "${cohort}" "missing rencrow binary"

failing_binary=${test_root}/failing-rencrow
printf '#!/usr/bin/env bash\necho "injected verifier failure" >&2\nexit 7\n' > "${failing_binary}"
chmod +x "${failing_binary}"
cohort=${test_root}/cohort-failing-binary
build_cohort "${cohort}" yes
if env RENCROW_BINARY="${failing_binary}" "${checker}" "${cohort}" >"${test_root}/failing-binary.out" 2>&1; then
  echo "[NG] a failing verifier was accepted" >&2
  exit 1
fi
grep -Fq '[NG] Task store verification failed' "${test_root}/failing-binary.out"
assert_no_restore_scratch "${cohort}" "failing verifier"

# The deadline is bounded and validated like the SQLite one.
for invalid_timeout in 0 3601 invalid; do
  if restore_check "${good}" "${test_root}/invalid-timeout.out" RENCROW_STORE_VERIFY_TIMEOUT_SEC="${invalid_timeout}"; then
    echo "[NG] invalid store verification deadline ${invalid_timeout} was accepted" >&2
    exit 1
  fi
  grep -Fq 'store verification timeout must be 1..3600 seconds' "${test_root}/invalid-timeout.out"
  assert_no_restore_scratch "${good}" "invalid store deadline ${invalid_timeout}"
done

# A verifier that never returns is stopped by the deadline. The stand-in is a bash
# script, which a native Windows Python cannot start, so this case runs where
# scripts can be executed directly.
case ${OSTYPE:-} in
  msys*|cygwin*|mingw*)
    echo "[SKIP] hanging-verifier deadline case needs an executable script (native Windows Python cannot start one)"
    ;;
  *)
  hanging_binary=${test_root}/hanging-rencrow
  printf '#!/usr/bin/env bash\nexec "%s" -c "import time; time.sleep(120)"\n' "$(command -v "${python_bin}")" > "${hanging_binary}"
  chmod +x "${hanging_binary}"
  cohort=${test_root}/cohort-hanging-binary
  build_cohort "${cohort}" yes
  hang_started=$SECONDS
  if env RENCROW_BINARY="${hanging_binary}" RENCROW_STORE_VERIFY_TIMEOUT_SEC=2 "${checker}" "${cohort}" >"${test_root}/hanging.out" 2>&1; then
    echo "[NG] a verifier that never returned was accepted" >&2
    exit 1
  fi
  hang_elapsed=$((SECONDS - hang_started))
  grep -Fq 'store verification timed out after 2s' "${test_root}/hanging.out"
  if (( hang_elapsed > 60 )); then
    echo "[NG] a hanging verifier was not stopped by its deadline (${hang_elapsed}s)" >&2
    exit 1
  fi
  assert_no_restore_scratch "${cohort}" "hanging verifier"

    ;;
esac

echo "[OK] storage backup stores contract test passed"
