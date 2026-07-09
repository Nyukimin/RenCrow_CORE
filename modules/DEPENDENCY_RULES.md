# Module Dependency Rules

These rules define the target dependency graph for future module migration.

## Allowed Import Direction

```text
core
  no module imports

browseractor
  no module imports

llm
  -> core

tts
  -> core

stt
  -> core

voicechat
  no module imports

webgather
  no module imports

worker
  -> core
  -> llm

chat
  -> core
  -> llm
  -> tts
  -> stt
  -> worker

cmd / adapter
  -> all modules for wiring only
```

## Forbidden Dependencies

- `core` importing any other module.
- `browseractor` importing runtime, provider, or feature modules.
- `llm` importing `tts`, `stt`, `chat`, or `worker`.
- `tts` importing `llm`, `stt`, `chat`, or `worker`.
- `stt` importing `llm`, `tts`, `chat`, or `worker`.
- `voicechat` importing `stt`, `tts`, `chat`, `llm`, or runtime implementations.
- `webgather` importing `browseractor`, `knowledge`, `memory`, or runtime implementations.
- `worker` importing `tts` or `stt`.
- Provider packages owning Viewer display state, playback ACK, or IdleChat pending state.

## Boundary Tests

`modules/dependency_rules_test.go` enforces the current import boundaries. Minimum checks:

```text
modules/core must not import modules/chat, modules/worker, modules/llm, modules/tts, modules/stt
modules/llm must not import modules/tts or modules/stt
modules/tts must not import modules/llm or modules/stt
modules/stt must not import modules/llm or modules/tts
modules/worker must not import modules/tts or modules/stt
modules/* contract packages must not import internal/* or cmd/*
internal/adapter/modulebridge must not import cmd/rencrow
internal/infrastructure/tts must not import stt/chat/llm/worker module ownership
internal/infrastructure/stt must not import tts/chat/llm/worker module ownership
internal/application/service must not import TTS/STT provider or Viewer implementations
```

`modules/` now contains contract packages and dependency tests. Provider implementations may stay under `internal/...` while they are wrapped by `internal/adapter/modulebridge` and gradually moved or stabilized behind module contracts.

## State Boundary Rules

Use these invariants when deciding module ownership:

- Display truth belongs to Chat/Viewer-facing state, not TTS audio chunks.
- Audio synthesis truth belongs to TTS provider state, not Chat history.
- Playback completion truth belongs to active Viewer ACK and Core/Chat lifecycle state, not the TTS provider.
- Transcript truth belongs to STT result objects until Chat accepts the transcript as input.
- Execution truth belongs to Worker execution reports, not Chat summaries.
- Provider health truth belongs to the provider module, while aggregate health status rules belong to `modules/core` and are applied by composition/runtime.
