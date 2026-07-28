# RenCrow Modules

This directory defines RenCrow_CORE-internal contract package boundaries.

`modules/*` contains public contracts, DTOs, events, pure policy, and state
ownership metadata. Concrete composition remains in `cmd/rencrow`, with
application, infrastructure, and adapter implementations under `internal/*`.

These local Go packages are not the implementation bodies of the independent
RenCrow_LLM, RenCrow_STT, RenCrow_TTS, RenCrow_Tools, or other sibling repositories.
External module ownership is defined by
[`docs/01_システム概要.md`](../docs/01_システム概要.md). The local packages only
hold CORE-side contracts, pure policy, request planning, and projections
needed to connect those modules.

## Layout

```text
modules/
  browseractor/
  core/
  chat/
  worker/
  llm/
  tts/
  stt/
  voicechat/
  webgather/
```

## Boundaries

- `browseractor`: browser automation request/response contracts, safety risk classification, and artifact metadata.
- `core`: shared contracts, orchestration glue, lifecycle rules, and cross-module state ownership.
- `chat`: user-facing dialogue, intent handling, routing decisions, and response presentation.
- `worker`: command execution, file operations, test/build execution, and operational jobs.
- `llm`: CORE-side inference contracts, request planning, response normalization, and diagnostic projections.
- `tts`: CORE-side text-to-speech contracts, voice/emotion policy, and playback-facing payload rules.
- `stt`: CORE-side speech-to-text contracts, transcription normalization, and microphone/audio ingestion boundaries.
- `voicechat`: Viewer voice-direct route, VDS bridge, runtime URL, and WebSocket planning contracts.
- `webgather`: web discovery, source fetch, extraction, staging, and search contract boundaries.

Module-specific health report builders belong inside each module. Adapter packages provide only current runtime provider/service availability and must not construct module health literals directly.

Do not place source under `.git/worktrees/*`; that path is Git metadata, not a tracked source tree.

## Design Documents

- [DESIGN.md](DESIGN.md): module goal, ownership, dependency direction, and state ownership.
- [CURRENT_MAP.md](CURRENT_MAP.md): current code ownership map from existing `internal/...` and `cmd/...` packages.
- [DEPENDENCY_RULES.md](DEPENDENCY_RULES.md): allowed and forbidden module dependencies.

## Implementation Status

This directory now contains module contract packages for `core`, `chat`, `worker`, `llm`, `tts`, `stt`, `voicechat`, `browseractor`, and `webgather`.
`modules/core.CurrentModuleDescriptors()` also exposes virtual state-observer descriptors such as `tts.playback` and `stt.viewer_input`; those are manifest entries, not separate source directories.
`internal/adapter/modulebridge` connects Chat orchestration, module providers,
and Worker execution to these contracts.
Runtime module metadata is exposed at `/viewer/modules/manifest`.
Runtime module health is exposed at `/viewer/modules/health` and includes a core aggregate `status`/`ready` result plus per-module reports.
LLM provider role diagnostics are exposed at `/viewer/modules/llm/diagnostics` without executing generation.
Worker execution contract diagnostics are exposed at `/viewer/modules/worker/diagnostics`.
TTS provider diagnostics are exposed at `/viewer/modules/tts/diagnostics` without executing synthesis.
STT provider diagnostics are exposed at `/viewer/modules/stt/diagnostics` without executing transcription.

Feature HTTP route registration enters through
`internal/features/*/registrar.go`. This registrar layer is the HTTP
dependency-handoff boundary; module contracts, adapter bridges, handlers, CLI
commands, and runtime jobs keep their owners listed in `CURRENT_MAP.md`.

## RenCrow_CORE Ver0.80 Public Seed Notes

The Public repo seed must keep this README, `CURRENT_MAP.md`, and `DEPENDENCY_RULES.md` aligned with the actual module directory set. If a feature is not represented as a `modules/<id>` package yet, it must remain visible in `internal/features/<id>` and in the Feature Module Catalog rather than being silently omitted.
