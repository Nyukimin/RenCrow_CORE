# Current Code Ownership Map

This document records the current ownership boundaries inside RenCrow_CORE.

## Runtime direction

All runtime requests enter CORE and cross the matching module boundary:

```text
LLM    RenCrow_CORE -> RenCrow_LLM -> configured target
TTS    RenCrow_CORE -> RenCrow_TTS -> configured target
STT    RenCrow_CORE -> RenCrow_STT -> configured target
Vision RenCrow_CORE -> RenCrow_Vision -> Wild -> RenCrow_Vision -> RenCrow_CORE
Image  RenCrow_CORE -> RenCrow_Image -> configured generator
Games  CORE Agent/LLM -> RenCrow_GAMES -> Observer -> user
```

CORE selects the Agent, execution role, logical alias, and orchestration policy.
Physical model names, provider credentials, backend URLs, ports, and process
placement belong to the corresponding module.

## Package ownership

- `modules/core` owns module inventory, aggregate health, shared request and
  response wiring, and runtime endpoint constants.
- `modules/llm` owns logical role names, request/response contracts, provider
  health normalization, diagnostics, and generation policy.
- `modules/chat` owns recipient and route contracts, IdleChat provider ordering,
  topic generation contracts, and route diagnostics.
- `modules/worker` owns Coder slot identity, capability planning, LightMemory
  setup, execution contracts, and worker diagnostics.
- `modules/tts` owns synthesis, playback, session, retry, audio-path, and TTS
  diagnostics contracts.
- `modules/stt` owns transcription, busy policy, Viewer input, session event,
  debug artifact, and STT diagnostics contracts.
- `internal/adapter/modulebridge` adapts concrete runtime services to module
  contracts.
- `internal/features/*/registrar.go` registers feature HTTP routes.
- `cmd/rencrow` is the composition root. It constructs configured modules,
  connects handlers, schedules runtime work, and owns process lifecycle.

## Public runtime surfaces

- `/viewer/modules/manifest` exposes the active module inventory.
- `/viewer/modules/health` exposes aggregate and per-module health.
- `/viewer/modules/llm/diagnostics` exposes logical LLM role diagnostics.
- `/viewer/modules/worker/diagnostics` exposes Worker execution diagnostics.
- `/viewer/modules/tts/diagnostics` and `/viewer/modules/tts/playback-state`
  expose TTS state without starting synthesis.
- `/viewer/modules/stt/diagnostics` exposes STT state without starting
  transcription.
- `/viewer/modules/chat/route` exposes the current Chat routing decision.

## Invariants

- CORE never selects a physical LLM provider or model endpoint.
- Viewer requests cannot provide a physical LLM URL, model, or route override.
- Vision media crosses `RenCrow_Vision`; image generation crosses
  `RenCrow_Image`.
- GAMES owns world state and deterministic execution. CORE owns Agent intent
  and LLM decisions.
- `RenCrow_CMD` is a CLI/client surface for CORE, not a runtime fork.
