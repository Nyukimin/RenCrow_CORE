#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PORT="${STT_E2E_PORT:-18791}"
WAV_PATH="${STT_E2E_WAV:-tmp/client_stt_input_latest.wav}"
CFG="$(mktemp /tmp/picoclaw-stt-e2e-XXXX.yaml)"
LOG="$(mktemp /tmp/picoclaw-stt-e2e-XXXX.log)"

cleanup() {
  if [[ -n "${PID:-}" ]]; then
    kill "$PID" >/dev/null 2>&1 || true
  fi
  rm -f "$CFG"
}
trap cleanup EXIT

cd "$ROOT_DIR"

cat > "$CFG" <<YAML
server:
  host: "127.0.0.1"
  port: ${PORT}
ollama:
  base_url: "http://localhost:11434"
  model: "picoclaw-v1"
session:
  storage_dir: "./data/sessions"
tts:
  enabled: false
stt:
  enabled: true
  provider: "mock"
  language: "ja"
  model: "mock"
  timeout_ms: 3000
YAML

PICOCLAW_CONFIG="$CFG" go run ./cmd/picoclaw run > "$LOG" 2>&1 &
PID=$!

for _ in $(seq 1 40); do
  if curl -fsS "http://127.0.0.1:${PORT}/stt/health" >/dev/null 2>&1; then
    break
  fi
  sleep 0.5
done

curl -fsS "http://127.0.0.1:${PORT}/stt/health"
python3 scripts/stt_e2e_probe.py \
  --wav "$WAV_PATH" \
  --provider-url "http://127.0.0.1:${PORT}/stt/file" \
  --chat-input-url "http://127.0.0.1:${PORT}/stt/chat-input" \
  --ws-url "ws://127.0.0.1:${PORT}/stt" \
  --provider-rounds 1 \
  --ws-rounds 1 \
  --ws-wait 4
