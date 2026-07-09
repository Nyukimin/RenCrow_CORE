---
type: concept
status: active
owner: core
canonical_source: docs/10_新仕様/05_Viewer仕様.md
source:
  - docs/10_新仕様/05_Viewer仕様.md
  - docs/10_新仕様/02_モジュール構成仕様.md
related:
  - docs/wiki/modules/picoclaw-multillm.md
  - docs/wiki/modules/rencrow-cmd.md
updated: 2026-06-25
---

# Viewer API

Viewer API は RenCrow の操作面と観測面の HTTP 契約である。

Viewer は静的 UI ではなく、HTTP API、SSE event、event log、history、monitor、Memory / Source Registry、IdleChat、STT/TTS の状態を投影する。

## 代表 route

- `/viewer`
- `/viewer/send`
- `/viewer/status`
- `/viewer/jobs`
- `/viewer/logs`
- `/viewer/runtime-config`
- `/viewer/memory/*`
- `/viewer/source-registry`
- `/viewer/recall/traces`
- `/viewer/idlechat/*`
- `/viewer/tts/audio`
- `/viewer/llm-ops/*`

## 境界

Viewer API では、表示本文、SSE event、event log、history、audio trigger、lipsync trigger、runtime config を混同しない。

RenCrow_CMD は Viewer の CLI 版として、picoclaw_multiLLM サーバを起動し、この Viewer API と同じ口へ command を送る。
