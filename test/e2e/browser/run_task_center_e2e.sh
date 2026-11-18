#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../../.." && pwd)"
build_dir="/Users/yukimi/RenCrow/Tmp/task-center-implementation-20261117"
binary_path="$build_dir/task-center-rencrow-static"
expected_source_manifest="$build_dir/task-center-static-source.sha256"
lifecycle_tmp="$(printenv TMPDIR 2>/dev/null || true)"
if [[ -z "$lifecycle_tmp" ]]; then
  lifecycle_tmp="$repo_root/Tmp/test-runtime/manual"
fi
reservations="$lifecycle_tmp/playwright/to-be-ops-live-e2e/reservations"
artifact_root="$build_dir/task-center-live-e2e"
playwright_browsers_path="$HOME/Library/Caches/ms-playwright"
run_id="$(date -u +%Y%m%dT%H%M%SZ)-$$"
run_dir="$artifact_root/task-center-$run_id"
seed_binary="$run_dir/seed-task-center-e2e"
unavailable_port="28791"
populated_port="28792"
server_pid=""
server_port=""
lock_28791_owned="false"
lock_28792_owned="false"

source_manifest() {
  (cd "$repo_root" && python3 test/e2e/browser/task_center_source_manifest.py)
}

port_is_free() {
  if lsof -nP -iTCP:"$1" -sTCP:LISTEN >/dev/null 2>&1; then
    return 1
  fi
  return 0
}

release_reservation() {
  local port="$1"
  local lock_dir="$reservations/$port.lock"
  local owner_file="$reservations/$port.owner"
  if [[ -f "$owner_file" ]] && grep -Fxq "run_id=$run_id" "$owner_file"; then
    rm -f "$owner_file"
  fi
  if [[ -f "$lock_dir/owner.pid" ]] && [[ "$(cat "$lock_dir/owner.pid")" == "$$" ]]; then
    rm -f "$lock_dir/owner.pid"
    rmdir "$lock_dir" 2>/dev/null || true
  fi
}

stop_server() {
  if [[ -n "$server_pid" ]] && kill -0 "$server_pid" 2>/dev/null; then
    kill -TERM "$server_pid" 2>/dev/null || true
    for _ in $(seq 1 50); do
      if ! kill -0 "$server_pid" 2>/dev/null; then
        break
      fi
      sleep 0.2
    done
    if kill -0 "$server_pid" 2>/dev/null; then
      kill -KILL "$server_pid" 2>/dev/null || true
    fi
    wait "$server_pid" 2>/dev/null || true
  fi
  if [[ -n "$server_port" ]]; then
    local pid_file="$reservations/$server_port.pid"
    local owner_file="$reservations/$server_port.owner"
    if [[ -f "$pid_file" ]] && [[ "$(cat "$pid_file")" == "$server_pid" ]]; then
      rm -f "$pid_file"
    fi
    if [[ -f "$owner_file" ]] && grep -Fxq "run_id=$run_id" "$owner_file"; then
      rm -f "$owner_file"
    fi
  fi
  server_pid=""
  server_port=""
}

cleanup() {
  stop_server
  if [[ "$lock_28791_owned" == "true" ]]; then
    release_reservation "$unavailable_port"
  fi
  if [[ "$lock_28792_owned" == "true" ]]; then
    release_reservation "$populated_port"
  fi
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if ! command -v lsof >/dev/null 2>&1; then
  echo "[NG] lsof is required for exact reserved-port ownership checks" >&2
  exit 2
fi
if [[ ! -x "$binary_path" ]]; then
  echo "[NG] rebuilt TaskCenter E2E binary is missing: $binary_path" >&2
  exit 2
fi
if [[ ! -f "$expected_source_manifest" ]]; then
  echo "[NG] static source manifest is missing: $expected_source_manifest" >&2
  exit 2
fi

mkdir -p "$run_dir" "$reservations"
source_manifest >"$run_dir/source.before.sha256"
if ! cmp -s "$expected_source_manifest" "$run_dir/source.before.sha256"; then
  echo "[NG] source changed after static validation/binary rebuild; rebuild and refresh the source manifest before E2E" >&2
  diff -u "$expected_source_manifest" "$run_dir/source.before.sha256" >&2 || true
  exit 2
fi
shasum -a 256 "$binary_path" >"$run_dir/binary.sha256"

reserve_port() {
  local port="$1"
  local lock_dir="$reservations/$port.lock"
  if [[ -e "$lock_dir" || -e "$reservations/$port.pid" || -e "$reservations/$port.owner" ]]; then
    echo "[NG] reserved port $port has existing lifecycle ownership records; leaving them untouched" >&2
    return 1
  fi
  if ! port_is_free "$port"; then
    echo "[NG] reserved port $port is occupied; leaving its owner untouched" >&2
    lsof -nP -iTCP:"$port" -sTCP:LISTEN >&2 || true
    return 1
  fi
  if ! mkdir "$lock_dir"; then
    echo "[NG] could not reserve port $port" >&2
    return 1
  fi
  printf '%s\n' "$$" >"$lock_dir/owner.pid"
  printf 'task_id=task_center_live_e2e\nrun_id=%s\nport=%s\n' "$run_id" "$port" >"$reservations/$port.owner"
  if [[ "$port" == "$unavailable_port" ]]; then
    lock_28791_owned="true"
  else
    lock_28792_owned="true"
  fi
}

if ! reserve_port "$unavailable_port"; then
  exit 2
fi
if ! reserve_port "$populated_port"; then
  exit 2
fi

write_profile() {
  local scenario="$1"
  local port="$2"
  local workstream_enabled="$3"
  local runtime_dir="$run_dir/$scenario/runtime"
  local local_user_id="task_center_e2e_user"
  local token_path="$runtime_dir/local-agent-ops.token"
  mkdir -p \
    "$runtime_dir/home" \
    "$runtime_dir/tmp" \
    "$runtime_dir/worker" \
    "$runtime_dir/workspace/logs" \
    "$runtime_dir/workspace/vault" \
    "$runtime_dir/workspace/memory" \
    "$runtime_dir/workspace/sessions" \
    "$runtime_dir/workspace/exports"
  chmod 700 "$runtime_dir" "$runtime_dir/home" "$runtime_dir/tmp"
  local token
  token="$(openssl rand -hex 32)"
  printf '%s' "$token" >"$token_path"
  chmod 600 "$token_path"
  printf '%s' "$local_user_id" >"$runtime_dir/local-agent-ops.user-id"
  chmod 600 "$runtime_dir/local-agent-ops.user-id"

  cat >"$runtime_dir/task-execution-policy.yaml" <<EOF
schema_version: 1
task_kinds:
  movie.catalog.lookup:
    allowed_agents:
      mode: all_registered
EOF

  cat >"$runtime_dir/config.yaml" <<EOF
server:
  host: "127.0.0.1"
  port: $port
  tls:
    enabled: false

storage:
  memory:
    session_dir: "$runtime_dir/workspace/sessions"
    operation_memory_dir: "$runtime_dir/workspace/memory"
    cold_export_dir: "$runtime_dir/workspace/exports"
  databases:
    conversation_l1: ""
    conversation_archive: "$runtime_dir/workspace/logs/conversation-archive.db"
    event_store: "$runtime_dir/workspace/logs/event-store.db"
    tool_registry: ""
    glossary: "$runtime_dir/workspace/logs/glossary.db"
    movie_catalog: "$runtime_dir/workspace/logs/movie_catalog.sqlite"
    hobby_graph: "$runtime_dir/workspace/logs/hobby-graph.db"
    advisor: "$runtime_dir/workspace/logs/advisor.db"
    sandbox: "$runtime_dir/workspace/logs/sandbox.db"
    dci: "$runtime_dir/workspace/logs/dci.db"
    skill_governance: "$runtime_dir/workspace/logs/skill-governance.db"
    workstream: "$runtime_dir/workspace/logs/workstream.db"
    revenue: "$runtime_dir/workspace/logs/revenue.db"
    persona_architecture: "$runtime_dir/workspace/logs/persona-architecture.db"
    browser_trace_to_api: "$runtime_dir/workspace/logs/browser-trace-to-api.db"
    complexity_hotspot: "$runtime_dir/workspace/logs/complexity-hotspot.db"
    super_agent_harness: "$runtime_dir/workspace/logs/super-agent-harness.db"
    ai_workflow: "$runtime_dir/workspace/logs/ai-workflow.db"
    knowledge_memory: "$runtime_dir/workspace/logs/knowledge-memory.db"
    durable_store_workflow: ""
    charimage_history: "$runtime_dir/workspace/logs/charimage-history.db"
  host:
    mode: "local"

workspace_dir: "$runtime_dir/workspace"
self_source_dir: "$repo_root"
prompts_dir: "$repo_root/prompts"

task_execution_policy_file: "$runtime_dir/task-execution-policy.yaml"
local_agent_ops:
  enabled: true
  auth_token_file: "$token_path"
  user_id: "$local_user_id"

workstream:
  enabled: $workstream_enabled
  storage: "sqlite"
  log_path: "$runtime_dir/workspace/logs/workstream"
  vault_root: "$runtime_dir/workspace/vault"
  require_success_criteria: true
  require_verification: true

movie_catalog:
  crawler_url: ""
person_related_catalog:
  provider_url: ""
  summary_worker:
    enabled: false
  identity_mapping:
    enabled: false

llm_gateway:
  base_url: "http://127.0.0.1:1"
  api_key_env: "RENCROW_E2E_NO_LLM_CREDENTIAL"
  timeout_sec: 5
stt:
  enabled: false
tts:
  enabled: false

worker:
  auto_commit: false
  workspace: "$runtime_dir/worker"
  stop_on_error: true
  action_on_protected: "error"
  protected_patterns:
    - ".env*"
    - "*credentials*"
    - "*.key"
    - "*.pem"

idle_chat:
  enabled: false
conversation:
  enabled: false
heartbeat:
  enabled: false
distributed:
  enabled: false
codex:
  enabled: false
trade:
  enabled: false
line:
  enabled: false
telegram:
  enabled: false
discord:
  enabled: false
slack:
  enabled: false
webwright_fetch:
  enabled: false
browser_actor:
  enabled: false
security:
  enabled: false
sandbox:
  enabled: false
tool_harness:
  enabled: false
dci:
  enabled: false
skill_governance:
  enabled: false
persona_architecture:
  enabled: false
browser_trace_to_api:
  enabled: false
complexity_hotspot:
  enabled: false
superagent_harness:
  enabled: false
ai_workflow:
  enabled: false
knowledge_memory:
  enabled: false
knowledge_relation:
  enabled: false
economic_objective:
  enabled: false
revenue:
  enabled: false
games:
  observer_url: "http://127.0.0.1:1"
  auto_play:
    enabled: false
llm_capability:
  llmfit:
    enabled: false
EOF
}

start_server() {
  local scenario="$1"
  local port="$2"
  local runtime_dir="$run_dir/$scenario/runtime"
  local config_path="$runtime_dir/config.yaml"
  local server_log="$run_dir/$scenario/server.log"
  local home_dir="$runtime_dir/home"
  local base_url="http://127.0.0.1:$port"
  local gateway_config="$runtime_dir/llm-gateway-config-must-not-exist.yaml"

  if [[ -e "$gateway_config" ]]; then
    echo "[NG] gateway config guard path unexpectedly exists: $gateway_config" >&2
    return 1
  fi

  if ! port_is_free "$port"; then
    echo "[NG] reserved port $port became occupied before startup; leaving its owner untouched" >&2
    lsof -nP -iTCP:"$port" -sTCP:LISTEN >&2 || true
    return 1
  fi
  server_port="$port"
  (
    cd "$runtime_dir"
    exec env -i \
      PATH="$PATH" \
      HOME="$home_dir" \
      TMPDIR="$runtime_dir/tmp" \
      TZ="UTC" \
      LANG="C.UTF-8" \
      RENCROW_CONFIG="$config_path" \
      RENCROW_LLM_CONFIG="$gateway_config" \
      RENCROW_E2E_RUNTIME="$runtime_dir" \
      RENCROW_E2E_REPO="$repo_root" \
      RENCROW_ENABLE_SERENA_MCP="false" \
      "$binary_path" run
  ) >"$server_log" 2>&1 &
  server_pid=$!
  printf '%s\n' "$server_pid" >"$reservations/$port.pid"
  {
    printf 'task_id=task_center_live_e2e\n'
    printf 'run_id=%s\n' "$run_id"
    printf 'pid=%s\n' "$server_pid"
    printf 'binary=%s\n' "$binary_path"
    printf 'bind_host=127.0.0.1\n'
    printf 'port=%s\n' "$port"
  } >"$reservations/$port.owner"

  local ready="false"
  local status=""
  for _ in $(seq 1 100); do
    status="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 1 "$base_url/viewer/task-center" 2>/dev/null || true)"
    if [[ "$status" == "200" ]]; then
      ready="true"
      break
    fi
    if ! kill -0 "$server_pid" 2>/dev/null; then
      break
    fi
    sleep 0.2
  done
  if [[ "$ready" != "true" ]]; then
    echo "[NG] isolated CORE server did not become ready for $scenario" >&2
    tail -n 100 "$server_log" >&2 || true
    return 1
  fi
  printf 'scenario=%s\nport=%s\npid=%s\nbase_url=%s\nruntime_dir=%s\nserver_log=%s\ngateway_config_guard=%s\n' \
    "$scenario" "$port" "$server_pid" "$base_url" "$runtime_dir" "$server_log" "$gateway_config" >"$run_dir/$scenario/run.txt"
}

verify_isolated_startup() {
  local scenario="$1"
  local server_log="$run_dir/$scenario/server.log"
  local runtime_dir="$run_dir/$scenario/runtime"
  if rg -n "RenCrow_LLM Gateway process exited|RenCrow_LLM Gateway auto-started|RenCrow_LLM local Gateway (start failed|was started)" "$server_log"; then
    echo "[NG] local RenCrow_LLM Gateway process was started or attempted in $scenario" >&2
    return 1
  fi
  if rg -n "Default news sources ready|All-feed source registry sweep|Due source registry sweep|Memory lifecycle (job enabled|maintenance complete)|Daily intake sweep complete" "$server_log"; then
    echo "[NG] background source or lifecycle job started in $scenario" >&2
    return 1
  fi
  if ! rg -Fq "RENCROW_LLM_CONFIG does not exist: $runtime_dir/llm-gateway-config-must-not-exist.yaml" "$server_log"; then
    echo "[NG] isolated Gateway auto-start guard was not confirmed in $scenario" >&2
    return 1
  fi
}

verify_server_stopped() {
  local scenario="$1"
  local pid
  pid="$(awk -F= '$1 == "pid" {print $2}' "$run_dir/$scenario/run.txt")"
  if [[ -z "$pid" ]]; then
    echo "[NG] missing server PID record for $scenario" >&2
    return 1
  fi
  local command
  command="$(ps -p "$pid" -o command= 2>/dev/null || true)"
  if [[ "$command" == *"$binary_path run"* ]]; then
    echo "[NG] own isolated CORE server PID $pid is still alive after cleanup" >&2
    return 1
  fi
}

stop_server
mkdir -p "$run_dir/unavailable" "$run_dir/populated"
write_profile "unavailable" "$unavailable_port" "false"
write_profile "populated" "$populated_port" "true"

if ! (cd "$repo_root" && go build -o "$seed_binary" ./test/e2e/browser/seed_task_center_e2e); then
  echo "[NG] failed to build isolated TaskCenter fixture seeder" >&2
  exit 2
fi
if ! start_server "unavailable" "$unavailable_port"; then
  exit 1
fi
verify_isolated_startup "unavailable"
env -i \
  PATH="$PATH" \
  HOME="$run_dir/unavailable/runtime/home" \
  TMPDIR="$run_dir/unavailable/runtime/tmp" \
  PLAYWRIGHT_BROWSERS_PATH="$playwright_browsers_path" \
  RENCROW_E2E_SCENARIO="unavailable" \
  RENCROW_E2E_BASE_URL="http://127.0.0.1:$unavailable_port" \
  RENCROW_E2E_RUNTIME="$run_dir/unavailable/runtime" \
  node "$repo_root/test/e2e/browser/task_center_live_e2e.mjs"
stop_server
verify_server_stopped "unavailable"
if ! env -i \
  PATH="$PATH" \
  HOME="$run_dir/populated/runtime/home" \
  TMPDIR="$run_dir/populated/runtime/tmp" \
  RENCROW_E2E_RUNTIME="$run_dir/populated/runtime" \
  "$seed_binary"; then
  echo "[NG] failed to seed isolated Workstream and MovieCatalog fixtures" >&2
  exit 1
fi
if ! start_server "populated" "$populated_port"; then
  exit 1
fi
verify_isolated_startup "populated"
for browser in chromium firefox; do
  browser_artifacts="$run_dir/populated/browser/$browser"
  mkdir -p "$browser_artifacts"
  env -i \
    PATH="$PATH" \
    HOME="$run_dir/populated/runtime/home" \
    TMPDIR="$run_dir/populated/runtime/tmp" \
    PLAYWRIGHT_BROWSERS_PATH="$playwright_browsers_path" \
    RENCROW_E2E_SCENARIO="populated" \
    RENCROW_E2E_BROWSER="$browser" \
    RENCROW_E2E_BASE_URL="http://127.0.0.1:$populated_port" \
    RENCROW_E2E_RUNTIME="$run_dir/populated/runtime" \
    RENCROW_E2E_ARTIFACT_DIR="$browser_artifacts" \
    node "$repo_root/test/e2e/browser/task_center_live_e2e.mjs"
done
stop_server
verify_server_stopped "populated"
source_manifest >"$run_dir/source.after.sha256"
if ! cmp -s "$expected_source_manifest" "$run_dir/source.after.sha256"; then
  echo "[NG] source changed during isolated TaskCenter E2E; review source.after.sha256 and rerun static validation/build" >&2
  diff -u "$expected_source_manifest" "$run_dir/source.after.sha256" >&2 || true
  exit 2
fi
if ! cmp -s "$run_dir/source.before.sha256" "$run_dir/source.after.sha256"; then
  echo "[NG] source manifest changed between E2E start and finish" >&2
  diff -u "$run_dir/source.before.sha256" "$run_dir/source.after.sha256" >&2 || true
  exit 2
fi
printf '[OK] TaskCenter live browser E2E artifacts: %s\n' "$run_dir"
