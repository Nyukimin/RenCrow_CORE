# 75 Viewer 音声直結 LLM Streaming 実装チェックリスト

Objective: implement `75_Viewer音声直結LLM_Streaming実装作業仕様.md` Phase 1.

| 正本 | 用途 |
| --- | --- |
| `75_Viewer音声直結LLM_Streaming実装作業仕様.md` | 実装・検証の source of truth |
| `74_Viewer音声直結LLM_Streaming仕様.md` | 設計・モード・フェーズ |
| `74_Viewer音声直結LLM_WS契約.md` | event / field / timeout |

---

## Required Source Tree

実装前に以下が存在すること:

**RenCrow_CORE**

- `cmd/rencrow/stt_runtime_websocket.go`（参照パターン）
- `cmd/rencrow/routes.go`
- `internal/adapter/viewer/assets/js/viewer.js`
- `internal/adapter/viewer/debug_system_handler.go`
- `internal/application/orchestrator/message_orchestrator.go`
- `modules/stt/`（module 分割の参照）

**RenCrow_LLM**

- `src/llm_server/alias_proxy.py`（multimodal / stream 参照）
- `src/llm_server/servers.py`

---

## PR-L1: RenCrow_LLM

| Requirement | Target | Evidence |
| --- | --- | --- |
| WS `/v1/chat/audio/sessions` | `audio_session_server.py` | curl/ws probe connects |
| session.start → session.ready | server + test | contract test pass |
| PCM binary → session.progress | server + test | progress duration increases |
| session.commit → llm.delta* → llm.final | server + test | non-empty final text |
| session.cancel aborts infer | test | no llm.final after cancel |
| UTTERANCE_TOO_SHORT | test | error_code matches 74 |
| final 後 no contradictory error | test | same as STT final contract |
| Chat role only | test | Worker model rejected |
| LLM_BUSY on concurrent infer | test | second commit while inferring |
| docs sync | `RenCrow_LLM/docs/audio_session_ws.md` | matches 74 WS §5 |

```bash
cd RenCrow_LLM
python3 -m pytest tests/test_audio_session_contract.py -v
```

---

## PR-P1: rencrow bridge

| Requirement | Target | Evidence |
| --- | --- | --- |
| VOICE_CHAT_ENABLED gate | `voice_chat_runtime_config.go` | disabled returns error |
| /voice-chat route | `routes.go` | registered |
| /voice-chat-ws alias | `modules/voicechat/contracts.go` | same handler |
| inferVoiceChatGatewayURL | `voice_chat_runtime_config.go` | unit test |
| Viewer frame 透過 | `voice_chat_runtime_bridge.go` | Go test byte-preserving |
| no HTTP input_audio fallback on commit | bridge | code review + test |
| no duplicate final synthesis | bridge | code review |
| runtime-config fields | `debug_system_handler.go` | JSON has voice_chat_* |

```bash
cd RenCrow_CORE
go test ./cmd/rencrow -run VoiceChat -count=1 -v
go test ./internal/adapter/viewer -run RuntimeConfig -count=1 -v
```

---

## PR-P2: orchestrator

| Requirement | Target | Evidence |
| --- | --- | --- |
| ProcessVoiceDirectRequest DTO | `voice_direct.go` | compiles |
| route=CHAT voice_direct | orchestrator | routing.decision event |
| stream hook first_token | existing lifecycle | metrics.latency |
| response_complete metric | existing lifecycle | metrics.latency |
| agent.response on complete | orchestrator | SSE test |
| no STT provider call | test | mock asserts |
| no IdleChat route | test | mock asserts |
| tmp WAV cleanup | voice_direct.go | defer remove |

```bash
go test ./internal/application/orchestrator -run VoiceDirect -count=1 -v
```

---

## PR-V1: Viewer

| Requirement | Target | Evidence |
| --- | --- | --- |
| load voice_chat_stream_url | `viewer.js` | runtime-config fetch |
| default stt_primary | `viewer.js` | unknown mode → stt |
| vds_sub opens /voice-chat only | `viewer.js` | test: no /stt WS |
| session.start before binary | `viewer.js` | contract test |
| session.commit on mic off | `viewer.js` | contract test |
| no /viewer/send on vds success | `viewer.js` | **critical gate** |
| barge-in session.cancel | `viewer.js` | test |
| stt_primary unchanged | `viewer_stt_https.test.mjs` | all pass |
| Chat UI single source (SSE) | `viewer.js` | no double bubble |

```bash
node --test internal/adapter/viewer/viewer_vds_https.test.mjs
node --test internal/adapter/viewer/viewer_stt_https.test.mjs
```

---

## PR-T1: E2E / 計測

| Requirement | Target | Evidence |
| --- | --- | --- |
| vds_e2e_probe.py | `scripts/` | PCM16 + start/commit |
| require-final flag | probe | non-zero without final |
| golden 25s metrics | `tmp/vds_e2e_golden_25s.json` | commit→final ≤ 25s warm |
| jfk 11s metrics | `tmp/vds_e2e_jfk.json` | recorded |
| STT regression | `scripts/stt_e2e_probe.py` | still pass |
| summary MD | `tmp/vds_vs_stt_bench_summary.md` | A/B table |

```bash
cd RenCrow_CORE
python3 -m py_compile scripts/vds_e2e_probe.py scripts/vds_e2e_probe_test.py
python3 -m unittest scripts/vds_e2e_probe_test.py
python3 scripts/vds_e2e_probe.py \
  --wav tmp/stt_inputs/client_stt_input_20260609_140311.wav \
  --require-final
```

---

## Phase 1 Merge Gate（すべて必須）

- [ ] PR-L1 merged and LLM server deployed to 207
- [ ] PR-P1 + PR-P2 merged, `VOICE_CHAT_ENABLED=true` on staging
- [ ] PR-V1 merged, `voice_input_mode=vds_sub` manual only
- [ ] PR-T1 golden 25s commit→final ≤ 25s（warm, 3 runs median）
- [ ] STT primary E2E non-regression
- [ ] No `/viewer/send` on vds_sub success path
- [ ] Production default remains `stt_primary` + `VOICE_CHAT_ENABLED=false`

---

## Explicit Non-Goals for Phase 1 Merge

- [ ] parallel_caption（Phase 1b）
- [ ] incremental audio infer（Phase 2）
- [ ] STT auto fallback（Phase 3）
- [ ] Viewer WAV attachment tray
- [ ] Replacing Phase 0 CLI `--audio-direct`

---

## Rollback

1. Set `VOICE_CHAT_ENABLED=false`
2. Set `voice_input_mode=stt_primary`
3. Redeploy rencrow only（LLM WS 残存は無害）
