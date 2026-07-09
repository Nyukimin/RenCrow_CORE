# STT Streaming 40 Implementation Checklist

Objective: implement `40_STT_Streaming実装作業仕様.md`.

Use `40_STT_Streaming実装作業仕様.md` as the source of truth. Use
`39_STT_Streaming暫定確定字幕仕様.md` only as protocol/behavior reference.

This checklist is ready to use once the missing Picoclaw / RenCrow Viewer Go
source tree is available and
`ops/macbook207/audit_stt_streaming_40_source_tree.sh /path/to/repo` passes.

## Required Source Tree

The implementation repository must contain:

- `internal/adapter/viewer/assets/js/viewer.js`
- `internal/adapter/viewer/viewer.html`
- `internal/adapter/viewer/assets/css/viewer.css`
- `cmd/picoclaw/stt_runtime_websocket.go`
- `cmd/picoclaw/main_stt_gateway_test.go`
- `internal/adapter/viewer/viewer_stt_https.test.mjs`
- `scripts/stt_e2e_probe.py`
- `scripts/stt_e2e_probe_test.py`
- `scripts/stt_viewer_browser_e2e.js`

## Prompt To Artifact Checklist

| 40 Requirement | Target Artifact | Required Evidence |
| --- | --- | --- |
| Viewer gets microphone stream | `internal/adapter/viewer/assets/js/viewer.js` | Browser gate shows `getUserMedia` path starts capture |
| Viewer resamples mono mic input to 16kHz PCM16 little-endian | `viewer.js` | Unit/browser result reports PCM16 bytes and seconds |
| Viewer sends `start` control before audio | `viewer.js`, `viewer_stt_https.test.mjs` | Test observes `sent_start=true` before first binary frame |
| Viewer sends PCM16 raw binary chunks, not WAV bytes | `viewer.js`, `stt_viewer_browser_e2e.js` | Browser/probe evidence includes binary frames and no WAV header |
| Viewer uses `chunkSamples=1600` baseline | `viewer.js` | Contract test or browser JSON reports chunk framing |
| Viewer renders mic input level from outgoing PCM RMS | `viewer.js`, `viewer.css`, `viewer.html` | Browser/manual evidence shows level changes during input |
| `partial`/`draft` display as provisional captions | `viewer.js`, `viewer.html`, `viewer.css` | Test verifies caption text and no chat send |
| `partial`/`draft` never go to Chat as fallback | `viewer.js`, `viewer_stt_https.test.mjs` | Test with partial-only stream has no `/viewer/send` |
| `final.text` displays as confirmed caption | `viewer.js`, `viewer.html`, `viewer.css` | Test verifies final caption text |
| `final.text` alone connects to normal chat input | `viewer.js`, `viewer_stt_https.test.mjs`, `stt_viewer_browser_e2e.js` | Browser gate shows `recv_final=true`, `chat_send_observed=true`, non-empty `send_message` |
| Viewer only sends STT final to normal chat, not IdleChat | `viewer.js` | Test covers active normal chat tab requirement |
| Stop flushes residual PCM, sends 1s silence tail, then `{ "type": "stop" }` | `viewer.js`, browser gate | Sent frame sequence shows binary tail before `sent_stop=true` |
| Stop waits for `final` / `error` / timeout / close | `viewer.js` | Tests cover final, error, timeout, close branches |
| Final wait timeout shows `STT final unavailable: timed out waiting for final` | `viewer.js`, `viewer.css` | Test/browser JSON shows no `/viewer/send` on timeout |
| `finalReceived=true` suppresses repeated stop control | `viewer.js` | Test verifies no duplicate stop after final |
| Error before final shows error caption and no Chat send | `viewer.js`, `viewer_stt_https.test.mjs` | Test verifies error caption and no `/viewer/send` |
| Error after final does not overwrite confirmed caption/chat input | `viewer.js`, test | Test verifies final remains authoritative |
| Debug panel render exceptions do not stop STT handling | `viewer.js`, test | Test injects render exception and still processes final |
| RenCrow STT bridge forwards Viewer text frames unchanged | `cmd/picoclaw/stt_runtime_websocket.go`, Go test | Go test compares upstream text frame payload |
| RenCrow STT bridge forwards Viewer binary frames unchanged | `stt_runtime_websocket.go`, Go test | Go test compares upstream binary bytes |
| RenCrow STT bridge forwards STT server text frames unchanged | `stt_runtime_websocket.go`, Go test | Go test observes downstream final frame |
| RenCrow STT bridge does not interpret `start` / `stop` / `final_pending` | `stt_runtime_websocket.go`, Go test | Go test verifies JSON controls are byte-preserved |
| Go fallback WS is not counted as success | docs/tests | E2E scripts fail without real upstream final |
| Probe decodes WAV with `wave` and sends PCM16 raw only | `scripts/stt_e2e_probe.py`, `scripts/stt_e2e_probe_test.py` | Unit test rejects WAV header on WS binary payload |
| Probe sends `start` before audio | probe/test | Unit test observes start frame first |
| Probe sends binary chunks and optional realtime sleep/silence tail | probe/test | Unit test validates chunk order and tail |
| Probe sends `stop` at end | probe/test | Unit test observes stop frame |
| Probe `--require-ws-final` fails without final | probe/test/runtime | Unit test and runtime failure exit non-zero |
| Browser gate observes WS frames and `/viewer/send` | `scripts/stt_viewer_browser_e2e.js` | JSON includes `sent_start`, `sent_stop`, `sent_binary`, `recv_final`, `chat_send_observed`, `send_message` |
| Fake microphone is diagnostic only | browser script/docs | Script output and docs state it is not real-mic evidence |
| Real mic headed gate supports human stop | browser script | `--real-mic --headed` flow waits for mic off, final, send |
| Docs record implementation evidence | docs/ops artifact | Updated evidence includes commands and outcomes |

## Standard Verification Commands

Live served Viewer asset audit available from the Mac:

```bash
/Users/yukimi/GenerativeAI/ops/macbook207/audit_stt_streaming_40_live_assets.sh
```

This only checks the currently served Viewer runtime assets. It does not replace
source-level tests from the implementation repository.

Run from the implementation repository root:

```bash
node --test internal/adapter/viewer/viewer_stt_https.test.mjs
node --test internal/adapter/viewer/viewer_memory_panel.test.mjs
python3 -m py_compile scripts/stt_e2e_probe.py scripts/stt_e2e_probe_test.py
python3 -m unittest scripts/stt_e2e_probe_test.py
GOCACHE=/tmp/picoclaw-gocache go test ./cmd/picoclaw ./internal/adapter/viewer ./internal/infrastructure/stt -count=1
git diff --check
```

Runtime probes, after the service is installed/restarted.

固定 WAV は **`docs/STT_TTS/STT_ゴールデンテストデータセット仕様.md`** を参照。デフォルト入力: `golden_25s_v1`（`tmp/stt_inputs/client_stt_input_20260609_140311.wav`）。

```bash
python3 scripts/stt_e2e_probe.py \
  --wav tmp/stt_inputs/client_stt_input_20260609_140311.wav \
  --provider-rounds 0 \
  --ws-rounds 1 \
  --ws-wait 70 \
  --ws-realtime \
  --ws-tail-silence-ms 1000 \
  --require-ws-final \
  --ws-url ws://127.0.0.1:18790/stt
```

旧 probe 入力（参照のみ）:

```bash
python3 scripts/stt_e2e_probe.py \
  --wav tmp/stt_inputs/client_stt_input_20260521_084443.wav \
  --provider-rounds 0 \
  --ws-rounds 1 \
  --ws-wait 70 \
  --ws-realtime \
  --ws-tail-silence-ms 1000 \
  --require-ws-final \
  --ws-url ws://127.0.0.1:18790/stt
```

Repeat for:

- `wss://fujitsu-ubunts.tailb07d8d.ts.net/stt`

Gateway direct URLs such as `ws://192.168.1.207:8766/stt` are diagnostic targets for the STT server itself. They are not the normal Viewer connection target; Viewer should use the RenCrow `/stt` endpoint on the same origin.

Browser fake-mic diagnostic gate:

```bash
node scripts/stt_viewer_browser_e2e.js \
  --wav tmp/stt_inputs/client_stt_input_20260521_084443.wav \
  --speak-ms 20000 \
  --partial-timeout-ms 30000 \
  --final-timeout-ms 90000
```

STT start -> TTS interrupt browser gate:

```bash
node scripts/stt_viewer_browser_e2e.js \
  --wav /tmp/rencrow_stt_tts_interrupt_e2e.wav \
  --speak-ms 2600 \
  --no-require-final \
  --no-require-send \
  --require-tts-interrupt \
  --partial-timeout-ms 1000 \
  --final-timeout-ms 1000
```

Success additionally requires:

- `tts_interrupt_before.playing=true`
- `tts_interrupted_on_stt_start=true`
- `tts_stale_chunk_dropped=true`

Real microphone gate:

```bash
node scripts/stt_viewer_browser_e2e.js \
  --real-mic \
  --headed \
  --partial-timeout-ms 30000 \
  --final-timeout-ms 70000
```

Success requires all of:

- `recv_final=true`
- `chat_send_observed=true`
- `send_message` non-empty
- operator confirms real mic, not fake mic

## Stop Conditions

Do not mark 40 complete if any of these are true:

- target source tree is missing
- `/stt` proxy is using fallback inference as the success path
- WebSocket probe sends WAV bytes instead of PCM16 raw
- Viewer sends `partial` / `draft` to Chat
- final is missing and latest partial is treated as success
- browser gate lacks `/viewer/send`
- real mic E2E has not been recorded when claiming real mic success

## Current Status

Blocked. See `STT_STREAMING_40_IMPLEMENTATION_BLOCKER.md`.
