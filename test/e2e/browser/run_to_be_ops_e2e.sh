#!/usr/bin/env bash
set -uo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
base_port="${RENCROW_E2E_PORT:-18791}"
browsers_csv="${RENCROW_E2E_BROWSERS:-firefox,chromium}"
run_id="$(date -u +%Y%m%dT%H%M%SZ)-$$"
artifact_root="${RENCROW_E2E_ARTIFACT_ROOT:-${repo_root}/output/playwright/to-be-ops-live-e2e}"
run_dir="${artifact_root}/${run_id}"
binary_path="${run_dir}/rencrow-e2e"
seed_path="${run_dir}/seed-to-be-ops"
server_pid=""
suite_status=0

IFS=',' read -r -a browsers <<<"${browsers_csv}"
mkdir -p "${run_dir}/browser"

stop_server() {
  if [[ -n "${server_pid}" ]] && kill -0 "${server_pid}" 2>/dev/null; then
    kill -TERM "${server_pid}" 2>/dev/null || true
    wait "${server_pid}" 2>/dev/null || true
  fi
  server_pid=""
}
trap stop_server EXIT INT TERM

find_available_port() {
  local candidate="$1"
  for _ in $(seq 1 50); do
    if ! curl -sS --max-time 1 "http://127.0.0.1:${candidate}/viewer?tab=ops" >/dev/null 2>&1 &&
       ! (exec 3<>"/dev/tcp/127.0.0.1/${candidate}") 2>/dev/null; then
      echo "${candidate}"
      return 0
    fi
    candidate=$((candidate + 1))
  done
  return 1
}

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

  if curl -sS --max-time 2 "${base_url}/viewer?tab=ops" >/dev/null 2>&1; then
    echo "[NG] browser E2E port is already in use: ${base_url}" >&2
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
      suite_status=1
      return
    fi
  fi

  HOME="${runtime_dir}/home" "${binary_path}" run >"${server_log}" 2>&1 &
  server_pid=$!
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
    stop_server
    return
  fi

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

populated_port="$(find_available_port "$((base_port + 1))")"
if [[ -z "${populated_port}" ]]; then
  echo "[NG] no isolated port available for populated scenario" >&2
  exit 2
fi
run_scenario "unavailable" "config.yaml" "${base_port}"
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
