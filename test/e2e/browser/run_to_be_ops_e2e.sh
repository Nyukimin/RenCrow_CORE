#!/usr/bin/env bash
set -uo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
unavailable_port="${RENCROW_E2E_UNAVAILABLE_PORT:-${RENCROW_E2E_PORT:-28791}}"
populated_port="${RENCROW_E2E_POPULATED_PORT:-28792}"
browsers_csv="${RENCROW_E2E_BROWSERS:-firefox,chromium}"
run_id="$(date -u +%Y%m%dT%H%M%SZ)-$$"
artifact_root="${RENCROW_E2E_ARTIFACT_ROOT:-${TMPDIR:-${repo_root}/Tmp/test-runtime/manual}/playwright/to-be-ops-live-e2e}"
run_dir="${artifact_root}/${run_id}"
binary_path="${run_dir}/rencrow-e2e"
seed_path="${run_dir}/seed-to-be-ops"
server_pid=""
reservation_pid_file=""
reservation_owner_file=""
reservation_lock_dir=""
reservation_task_id=""
old_instance_id=""
reservation_error_code=""
suite_status=0

IFS=',' read -r -a browsers <<<"${browsers_csv}"
mkdir -p "${run_dir}/browser"

stop_server() {
  if [[ -n "${server_pid}" ]] && kill -0 "${server_pid}" 2>/dev/null; then
    kill -TERM "${server_pid}" 2>/dev/null || true
    wait "${server_pid}" 2>/dev/null || true
  fi
  if [[ -n "${reservation_pid_file}" && -f "${reservation_pid_file}" ]]; then
    local recorded_pid
    recorded_pid="$(cat "${reservation_pid_file}" 2>/dev/null || true)"
    if [[ "${recorded_pid}" == "${server_pid}" || -z "${server_pid}" ]]; then
      rm -f "${reservation_pid_file}"
      [[ -n "${reservation_owner_file}" ]] && rm -f "${reservation_owner_file}"
    fi
  fi
  server_pid=""
  reservation_pid_file=""
  reservation_owner_file=""
  if [[ -n "${reservation_lock_dir}" ]]; then
    rm -f "${reservation_lock_dir}/owner.pid"
    rmdir "${reservation_lock_dir}" 2>/dev/null || true
  fi
  reservation_lock_dir=""
  reservation_task_id=""
  old_instance_id=""
  reservation_error_code=""
}
trap stop_server EXIT INT TERM

validate_reserved_port() {
  local port="$1"
  [[ "${port}" =~ ^[0-9]+$ ]] && [[ "${port}" -ge 1024 ]] && [[ "${port}" -le 65535 ]]
}

port_is_free() {
  local port="$1"
  ! (exec 3<>"/dev/tcp/127.0.0.1/${port}") 2>/dev/null
}

prepare_reserved_port() {
  local port="$1"
  local task_id="$2"
  local pid_file="${artifact_root}/reservations/${port}.pid"
  local owner_file="${artifact_root}/reservations/${port}.owner"
  local lock_dir="${artifact_root}/reservations/${port}.lock"
  local lock_owner=""
  local previous_pid=""
  local command_line=""
  local recorded_task=""
  local recorded_start=""
  local current_start=""
  mkdir -p "$(dirname "${pid_file}")"
  reservation_task_id="${task_id}"
  reservation_owner_file="${owner_file}"
  reservation_error_code=""

  if ! mkdir "${lock_dir}" 2>/dev/null; then
    lock_owner="$(cat "${lock_dir}/owner.pid" 2>/dev/null || true)"
    if [[ "${lock_owner}" =~ ^[0-9]+$ ]] && kill -0 "${lock_owner}" 2>/dev/null; then
      echo "[NG] LIFECYCLE_BUSY: reserved E2E port ${port} is managed by pid=${lock_owner}" >&2
      reservation_error_code="LIFECYCLE_BUSY"
      return 1
    fi
    rm -f "${lock_dir}/owner.pid"
    rmdir "${lock_dir}" 2>/dev/null || true
    if ! mkdir "${lock_dir}" 2>/dev/null; then
      echo "[NG] LIFECYCLE_BUSY: could not acquire reserved E2E port ${port}" >&2
      reservation_error_code="LIFECYCLE_BUSY"
      return 1
    fi
  fi
  printf '%s\n' "$$" >"${lock_dir}/owner.pid"
  reservation_lock_dir="${lock_dir}"

  if port_is_free "${port}"; then
    rm -f "${pid_file}"
    rm -f "${owner_file}"
    return 0
  fi
  if [[ -f "${pid_file}" ]]; then
    previous_pid="$(cat "${pid_file}" 2>/dev/null || true)"
  fi
  if [[ "${previous_pid}" =~ ^[0-9]+$ ]] && kill -0 "${previous_pid}" 2>/dev/null; then
    command_line="$(ps -ww -p "${previous_pid}" -o command= 2>/dev/null || true)"
  fi
  if [[ "${command_line}" != *"${artifact_root}"* || "${command_line}" != *"/rencrow-e2e run"* ]]; then
    echo "[NG] PORT_OWNERSHIP_CONFLICT: reserved E2E port ${port} is occupied by an unknown task" >&2
    reservation_error_code="PORT_OWNERSHIP_CONFLICT"
    return 1
  fi
  if [[ -f "${owner_file}" ]]; then
    recorded_task="$(sed -n 's/^task_id=//p' "${owner_file}" | head -1)"
    recorded_start="$(sed -n 's/^process_start_identity=//p' "${owner_file}" | head -1)"
    current_start="$(ps -ww -p "${previous_pid}" -o lstart= 2>/dev/null | sed 's/^[[:space:]]*//' || true)"
    if [[ "${recorded_task}" != "${task_id}" || -z "${recorded_start}" || "${recorded_start}" != "${current_start}" ]]; then
      echo "[NG] PORT_OWNERSHIP_CONFLICT: owner record does not match reserved E2E port ${port}" >&2
      reservation_error_code="PORT_OWNERSHIP_CONFLICT"
      return 1
    fi
    old_instance_id="$(sed -n 's/^instance_id=//p' "${owner_file}" | head -1)"
  fi

  echo "[INFO] replacing prior Viewer E2E task pid=${previous_pid} on reserved port ${port}"
  kill -TERM "${previous_pid}" 2>/dev/null || true
  for _ in $(seq 1 50); do
    port_is_free "${port}" && break
    sleep 0.2
  done
  if ! port_is_free "${port}"; then
    echo "[NG] PORT_RELEASE_TIMEOUT: reserved E2E port ${port} was not released" >&2
    reservation_error_code="PORT_RELEASE_TIMEOUT"
    return 1
  fi
  rm -f "${pid_file}"
  rm -f "${owner_file}"
}

audit_lifecycle() {
  local scenario="$1"
  local port="$2"
  local outcome="$3"
  local code="$4"
  local config_revision="$5"
  printf '{"time":"%s","operation_id":"%s-%s","task_id":"%s","old_instance_id":"%s","new_instance_id":"%s-%s","host_identity":"%s","transport":"tcp","bind_host":"127.0.0.1","reserved_port":%s,"config_revision":"%s","action":"replace","outcome":"%s","code":"%s"}\n' \
    "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "${run_id}" "${scenario}" "${reservation_task_id}" \
    "${old_instance_id}" "${run_id}" "${scenario}" "$(hostname -f 2>/dev/null || hostname)" \
    "${port}" "${config_revision}" "${outcome}" "${code}" >>"${artifact_root}/reservations/lifecycle.jsonl"
}

if ! validate_reserved_port "${unavailable_port}" || ! validate_reserved_port "${populated_port}"; then
  echo "[NG] reserved E2E ports must be distinct integers from 1024 through 65535" >&2
  exit 2
fi
if [[ "${unavailable_port}" == "${populated_port}" ]]; then
  echo "[NG] reserved E2E ports must be distinct" >&2
  exit 2
fi

cd "${repo_root}"
if ! go build -o "${binary_path}" ./cmd/rencrow; then
  echo "[NG] failed to build RenCrow E2E server" >&2
  exit 2
fi
if ! go build -o "${seed_path}" ./test/e2e/browser/seed_to_be_ops; then
  echo "[NG] failed to build To-Be fixture seeder" >&2
  exit 2
fi

run_scenario() {
  local scenario="$1"
  local config_name="$2"
  local port="$3"
  local base_url="http://127.0.0.1:${port}"
  local scenario_dir="${run_dir}/${scenario}"
  local runtime_dir="${scenario_dir}/runtime"
  local server_log="${scenario_dir}/server.log"
  mkdir -p "${runtime_dir}/worker" "${runtime_dir}/workspace/logs" "${scenario_dir}"

  local task_id="viewer_e2e_${scenario}"
  local config_revision
  config_revision="$(sha256sum "${binary_path}" "${repo_root}/test/e2e/browser/${config_name}" | sha256sum | awk '{print $1}')"
  if ! prepare_reserved_port "${port}" "${task_id}"; then
    audit_lifecycle "${scenario}" "${port}" "failed" "${reservation_error_code:-PORT_OWNERSHIP_CONFLICT}" "${config_revision}"
    stop_server
    suite_status=1
    return
  fi

  export RENCROW_E2E_PORT="${port}"
  export RENCROW_E2E_REPO="${repo_root}"
  export RENCROW_E2E_RUNTIME="${runtime_dir}"
  export RENCROW_E2E_BASE_URL="${base_url}"
  export RENCROW_ENABLE_SERENA_MCP="false"
  export RENCROW_CONFIG="${repo_root}/test/e2e/browser/${config_name}"

  if [[ "${scenario}" == "populated" ]]; then
    if ! "${seed_path}"; then
      echo "[NG] failed to seed ${scenario}" >&2
      audit_lifecycle "${scenario}" "${port}" "failed" "TASK_START_FAILED" "${config_revision}"
      stop_server
      suite_status=1
      return
    fi
  fi

  HOME="${runtime_dir}/home" "${binary_path}" run >"${server_log}" 2>&1 &
  server_pid=$!
  reservation_pid_file="${artifact_root}/reservations/${port}.pid"
  printf '%s\n' "${server_pid}" >"${reservation_pid_file}"
  process_start_identity="$(ps -ww -p "${server_pid}" -o lstart= 2>/dev/null | sed 's/^[[:space:]]*//' || true)"
  {
    printf 'task_id=%s\n' "${task_id}"
    printf 'instance_id=%s-%s\n' "${run_id}" "${scenario}"
    printf 'pid=%s\n' "${server_pid}"
    printf 'process_start_identity=%s\n' "${process_start_identity}"
    printf 'executable=%s\n' "${binary_path}"
    printf 'config_revision=%s\n' "${config_revision}"
    printf 'host_identity=%s\n' "$(hostname -f 2>/dev/null || hostname)"
    printf 'bind_host=127.0.0.1\n'
    printf 'port=%s\n' "${port}"
  } >"${reservation_owner_file}"
  local ready="false"
  local viewer_status=""
  for _ in $(seq 1 100); do
    viewer_status="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 1 "${base_url}/viewer?tab=ops" 2>/dev/null || true)"
    if [[ "${viewer_status}" == "200" ]]; then
      ready="true"
      break
    fi
    if ! kill -0 "${server_pid}" 2>/dev/null; then
      break
    fi
    sleep 0.2
  done
  if [[ "${ready}" != "true" ]]; then
    echo "[NG] isolated RenCrow E2E server did not become ready for ${scenario}" >&2
    tail -n 120 "${server_log}" >&2 || true
    suite_status=1
    audit_lifecycle "${scenario}" "${port}" "failed" "TASK_START_FAILED" "${config_revision}"
    stop_server
    return
  fi
  audit_lifecycle "${scenario}" "${port}" "succeeded" "OK" "${config_revision}"

  for browser in "${browsers[@]}"; do
    browser="$(echo "${browser}" | xargs)"
    [[ -z "${browser}" ]] && continue
    export RENCROW_E2E_BROWSER="${browser}"
    export RENCROW_E2E_SCENARIO="${scenario}"
    export RENCROW_E2E_ARTIFACT_DIR="${run_dir}/browser/${scenario}/${browser}"
    export RENCROW_E2E_FAULT_MATRIX="0"
    if [[ "${scenario}" == "populated" && "${browser}" == "firefox" ]]; then
      export RENCROW_E2E_FAULT_MATRIX="1"
    fi
    mkdir -p "${RENCROW_E2E_ARTIFACT_DIR}"
    if ! node test/e2e/browser/to_be_ops_e2e.mjs; then
      echo "[NG] ${scenario}/${browser}" >&2
      suite_status=1
    else
      echo "[OK] ${scenario}/${browser}"
    fi
  done
  stop_server
}

run_scenario "unavailable" "config.yaml" "${unavailable_port}"
run_scenario "populated" "config_populated.yaml" "${populated_port}"

export RENCROW_E2E_RUN_DIR="${run_dir}"
export RENCROW_E2E_EXPECTED_REPORTS="$((${#browsers[@]} * 2))"
if ! node test/e2e/browser/build_tracker.mjs; then
  suite_status=1
fi

if [[ "${suite_status}" -eq 0 ]]; then
  echo "[OK] browser E2E tracker: ${run_dir}/tracker.json"
else
  echo "[NG] browser E2E completed with failures: ${run_dir}" >&2
fi
exit "${suite_status}"
