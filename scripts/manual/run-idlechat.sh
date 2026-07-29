#!/usr/bin/env bash
# IdleChatを手動確認するための非破壊ランチャー。

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/../.." && pwd)"
runtime_root="${repo_root}/Tmp/manual-runtime/idlechat"
log_path="${runtime_root}/rencrow.log"
pid_path="${runtime_root}/rencrow.pid"
binary_path="${RENCROW_IDLECHAT_BINARY:-${repo_root}/rencrow-test}"

if [[ ! -x "${binary_path}" ]]; then
    echo "IdleChat binary is not executable: ${binary_path}" >&2
    echo "Set RENCROW_IDLECHAT_BINARY to an explicit test binary." >&2
    exit 1
fi

mkdir -p "${runtime_root}"

echo "=== RenCrow IdleChat manual run ==="
echo "Binary: ${binary_path}"
echo "Runtime: ${runtime_root}"

"${binary_path}" >"${log_path}" 2>&1 &
pid=$!
printf '%s\n' "${pid}" >"${pid_path}"

sleep 3

if ! kill -0 "${pid}" 2>/dev/null; then
    echo "IdleChat process exited during startup." >&2
    tail -30 "${log_path}" >&2 || true
    exit 1
fi

tail -30 "${log_path}"
echo "PID: ${pid}"
echo "Log: ${log_path}"
echo "Stop only this run with: kill \$(cat '${pid_path}')"
