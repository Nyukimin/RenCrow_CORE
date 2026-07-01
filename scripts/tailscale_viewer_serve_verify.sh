#!/usr/bin/env bash
set -euo pipefail

TAILSCALE_HOST="${TAILSCALE_HOST:-fujitsu-ubunts.tailb07d8d.ts.net}"
REN_CROW_URL="${REN_CROW_URL:-http://127.0.0.1:18790}"
EXPECTED_STT_STREAM="${EXPECTED_STT_STREAM:-wss://${TAILSCALE_HOST}/stt}"
EXPECTED_STT_BASE="${EXPECTED_STT_BASE:-http://192.168.1.207:8766}"
EXPECTED_TTS_BASE="${EXPECTED_TTS_BASE:-http://192.168.1.207:7870}"

log() {
  printf '[tailscale-viewer] %s\n' "$*"
}

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    log "missing command: $1"
    exit 127
  fi
}

http_code() {
  curl -k -sS -o /dev/null -w '%{http_code}' "$1"
}

verify_runtime_config() {
  local url="$1"
  python3 - "$url" "$EXPECTED_STT_STREAM" "$EXPECTED_STT_BASE" "$EXPECTED_TTS_BASE" <<'PY'
import json
import sys
import urllib.request

url, expected_stream, expected_stt, expected_tts = sys.argv[1:5]
with urllib.request.urlopen(url, timeout=5) as resp:
    payload = json.load(resp)

actual = {
    "stt_stream_url": payload.get("stt_stream_url", ""),
    "stt_base_url": payload.get("stt_base_url", ""),
    "tts_base_url": payload.get("tts_base_url", ""),
}
expected = {
    "stt_stream_url": expected_stream,
    "stt_base_url": expected_stt,
    "tts_base_url": expected_tts,
}
if actual != expected:
    print("runtime-config mismatch", file=sys.stderr)
    print("actual:", actual, file=sys.stderr)
    print("expected:", expected, file=sys.stderr)
    sys.exit(1)
print(json.dumps(actual, ensure_ascii=False))
PY
}

verify_websocket_handshake() {
  local url="$1"
  local output
  output="$(mktemp)"
  curl -k -i --http1.1 -N --max-time 3 \
    -H 'Connection: Upgrade' \
    -H 'Upgrade: websocket' \
    -H 'Sec-WebSocket-Version: 13' \
    -H 'Sec-WebSocket-Key: SGVsbG8sIHdvcmxkIQ==' \
    -H "Origin: https://${TAILSCALE_HOST}" \
    "$url" >"$output" 2>&1 || true
  if ! grep -a -q '101 Switching Protocols' "$output"; then
    cat "$output" >&2
    rm -f "$output"
    log "websocket handshake failed: $url"
    exit 1
  fi
  rm -f "$output"
}

require_cmd tailscale
require_cmd curl
require_cmd python3

if systemctl status picoclaw-funnel.service --no-pager >/dev/null 2>&1; then
  if systemctl is-active --quiet picoclaw-funnel.service; then
    log "blocked: picoclaw-funnel.service is active. Disable it before Serve verification:"
    log "sudo systemctl disable --now picoclaw-funnel.service"
    exit 2
  fi
fi

log "checking local RenCrow health: ${REN_CROW_URL}/health"
curl -fsS "${REN_CROW_URL}/health" >/dev/null

log "configuring Tailscale Serve for ${REN_CROW_URL}"
tailscale serve --bg --yes "${REN_CROW_URL}"

status_json="$(tailscale serve status --json)"
if grep -q '"AllowFunnel"' <<<"$status_json"; then
  log "blocked: Tailscale status still contains AllowFunnel"
  printf '%s\n' "$status_json" >&2
  exit 2
fi

viewer_url="https://${TAILSCALE_HOST}/viewer?tab=timeline"
runtime_url="https://${TAILSCALE_HOST}/viewer/runtime-config"
health_url="https://${TAILSCALE_HOST}/health"
stt_url="https://${TAILSCALE_HOST}/stt"

log "checking Viewer: ${viewer_url}"
viewer_code="$(http_code "$viewer_url")"
if [[ "$viewer_code" != "200" ]]; then
  log "viewer returned HTTP ${viewer_code}"
  exit 1
fi

log "checking runtime-config: ${runtime_url}"
verify_runtime_config "$runtime_url"

log "checking non-Viewer route guard: ${health_url}"
health_code="$(http_code "$health_url")"
if [[ "$health_code" != "404" ]]; then
  log "expected /health to be blocked with 404, got HTTP ${health_code}"
  exit 1
fi

log "checking STT WebSocket handshake: ${stt_url}"
verify_websocket_handshake "$stt_url"

log "ok"
