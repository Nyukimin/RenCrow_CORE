#!/usr/bin/env bash
set -euo pipefail

PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

ROOT="${1:-${STT_STREAMING_REPO:-/Users/yukimi/GenerativeAI/RenCrow_Code}}"

fail() {
  printf 'NG: %s\n' "$*" >&2
  exit 1
}

printf 'STT Streaming 40 source-tree audit\n'
printf 'root=%s\n\n' "$ROOT"

[ -d "$ROOT" ] || fail "source root does not exist: $ROOT"

if [ -d "$ROOT/.git" ]; then
  if ! git -C "$ROOT" rev-parse --verify HEAD >/dev/null 2>&1; then
    fail "git repository has no checked-out HEAD: $ROOT"
  fi
fi

required_files=(
  "internal/adapter/viewer/assets/js/viewer.js"
  "internal/adapter/viewer/viewer.html"
  "internal/adapter/viewer/assets/css/viewer.css"
  "cmd/rencrow/stt_runtime_websocket.go"
  "cmd/rencrow/main_stt_gateway_test.go"
  "internal/adapter/viewer/viewer_stt_https.test.mjs"
  "scripts/stt_e2e_probe.py"
  "scripts/stt_e2e_probe_test.py"
  "scripts/stt_viewer_browser_e2e.js"
)

missing=0
for rel in "${required_files[@]}"; do
  if [ -s "$ROOT/$rel" ]; then
    printf 'OK: %s\n' "$rel"
  else
    printf 'NG: missing %s\n' "$rel" >&2
    missing=1
  fi
done

if [ "$missing" -ne 0 ]; then
  cat >&2 <<'EOF'

The source tree is not ready for 40_STT_Streaming implementation.
Provide the RenCrow / RenCrow Viewer Go repository that contains the files above,
or set STT_STREAMING_REPO=/path/to/repo and rerun this audit.
EOF
  exit 1
fi

printf '\nOK: source tree has all files required by 40_STT_Streaming実装作業仕様.md\n'
