#!/usr/bin/env bash
set -euo pipefail

PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

RENCROW_BASE="${RENCROW_BASE:-http://192.168.1.204:18790}"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/stt40-live-assets.XXXXXX")"

cleanup() {
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

fail() {
  printf 'NG: %s\n' "$*" >&2
  exit 1
}

ok() {
  printf 'OK: %s\n' "$*"
}

fetch() {
  local url="$1"
  local out="$2"
  curl -fsS --max-time 10 "$url" > "$out"
}

require_file_contains() {
  local file="$1"
  local pattern="$2"
  local label="$3"
  if ! rg -q "$pattern" "$file"; then
    fail "$label"
  fi
  ok "$label"
}

html="$TMP_DIR/viewer.html"
css="$TMP_DIR/viewer.css"
js="$TMP_DIR/viewer.js"
runtime="$TMP_DIR/runtime-config.json"
debug="$TMP_DIR/debug-system.json"

printf 'STT Streaming 40 live asset audit\n'
printf 'base=%s\n\n' "$RENCROW_BASE"

fetch "$RENCROW_BASE/viewer" "$html"
fetch "$RENCROW_BASE/viewer/runtime-config" "$runtime"
fetch "$RENCROW_BASE/viewer/debug/system" "$debug"

css_path="$(rg -o '/viewer/assets/css/viewer\.css[^"]*' "$html" | head -1)"
js_path="$(rg -o '/viewer/assets/js/viewer\.js[^"]*' "$html" | head -1)"

if [[ -z "$css_path" ]]; then
  fail "viewer.css asset path not found in served HTML"
fi
if [[ -z "$js_path" ]]; then
  fail "viewer.js asset path not found in served HTML"
fi

fetch "$RENCROW_BASE$css_path" "$css"
fetch "$RENCROW_BASE$js_path" "$js"

python3 - "$runtime" "$debug" <<'PY'
import json
import sys

runtime = json.load(open(sys.argv[1], encoding="utf-8"))
debug = json.load(open(sys.argv[2], encoding="utf-8"))
audio = debug.get("audio") or {}

expected = {
    "stt_stream_url": "ws://192.168.1.207:8766/stt",
    "stt_base_url": "http://192.168.1.207:8766",
    "tts_base_url": "http://192.168.1.207:7870",
}
for key, value in expected.items():
    actual = runtime.get(key)
    if actual != value:
        raise SystemExit(f"NG: runtime {key}={actual!r}, expected {value!r}")
    print(f"OK: runtime {key}={actual}")

for key in ("stt_ok", "tts_live_ok", "tts_ready_ok"):
    if audio.get(key) is not True:
        raise SystemExit(f"NG: debug audio.{key} is not true")
    print(f"OK: debug audio.{key}=true")
PY

require_file_contains "$html" 'id="micBtn"' 'HTML has #micBtn'
require_file_contains "$html" 'id="sttCaption"' 'HTML has #sttCaption'
require_file_contains "$html" 'id="sttConnState"' 'HTML has #sttConnState'
require_file_contains "$html" 'id="sttSessionState"' 'HTML has #sttSessionState'

require_file_contains "$css" '\.audio-btn#micBtn\.has-level' 'CSS has mic input level style'
require_file_contains "$css" '\.stt-caption\.draft' 'CSS has draft caption style'
require_file_contains "$css" '\.stt-caption\.final' 'CSS has final caption style'
require_file_contains "$css" '\.stt-caption\.error' 'CSS has error caption style'

require_file_contains "$js" 'chunkSamples: 1600' 'JS uses chunkSamples=1600'
require_file_contains "$js" 'navigator\.mediaDevices\.getUserMedia' 'JS captures microphone'
require_file_contains "$js" 'createScriptProcessor\(4096, 1, 1\)' 'JS uses mono ScriptProcessor'
require_file_contains "$js" 'resampleToPCM16\(pcm, sttState\.inputSampleRate \|\| 48000, 16000\)' 'JS resamples to 16kHz PCM16'
require_file_contains "$js" 'calculateSTTInputLevel\(pcm16\)' 'JS computes outgoing PCM input level'
require_file_contains "$js" 'new WebSocket\(sttState\.voiceBridgeURL\)' 'JS opens configured STT WebSocket'
require_file_contains "$js" 'sendSTTStartControl\(\)' 'JS sends start control on WS open'
require_file_contains "$js" "type: 'start'" 'JS start control type is start'
require_file_contains "$js" "format: 'pcm_s16le'" 'JS start control declares pcm_s16le'
require_file_contains "$js" 'sttState\.ws\.send\(chunk\.buffer\)' 'JS sends raw binary PCM chunks'
require_file_contains "$js" 'STT_STOP_TAIL_SILENCE_MS = 1000' 'JS defines 1s stop silence tail'
require_file_contains "$js" "JSON\\.stringify\\(\\{ type: 'stop' \\}\\)" 'JS sends stop control'
require_file_contains "$js" "msg\\.type === 'draft' \\|\\| msg\\.type === 'partial'" 'JS handles partial/draft separately'
require_file_contains "$js" "setCaption\\('暫定文字列', partialText, 'stt-caption has-text draft'\\)" 'JS renders provisional caption'
require_file_contains "$js" "setCaption\\('確定文字列', finalText, 'stt-caption has-text final'\\)" 'JS renders final caption'
require_file_contains "$js" 'sttState\.finalReceived = true' 'JS tracks finalReceived'
require_file_contains "$js" 'handleSTTFinalText\(sttState\.lastRecognitionText\)' 'JS connects final text to chat flow'
require_file_contains "$js" 'inp\.value = finalText' 'JS writes only final text to chat input'
require_file_contains "$js" 'send\(\)' 'JS can call normal send()'
require_file_contains "$js" 'Error ignored after final' 'JS ignores error after final'
require_file_contains "$js" 'timed out waiting for final' 'JS reports final wait timeout'
require_file_contains "$js" 'renderSTTDebugPanelsSafely' 'JS wraps debug panel rendering'

printf '\nLive asset audit passed. Source tree and source-level tests are still required for completion.\n'
