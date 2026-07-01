# RenCrow Modules

This directory defines the local module boundaries for RenCrow inside this worktree.

For RenCrow_CORE Ver0.80, `modules/*` contains public contracts, DTOs, events, pure policy, and state ownership metadata. It does not require every implementation body to live under `modules/*` yet. Existing runtime implementations may remain in `cmd/picoclaw`, `internal/application`, `internal/infrastructure`, or `internal/adapter` as `legacy-body` while the contract boundary is made explicit.

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
- `llm`: language model clients, provider routing, local/external model contracts, and prompt-facing adapters.
- `tts`: text-to-speech contracts, synthesis clients, voice/emotion mapping, playback-facing payload rules.
- `stt`: speech-to-text contracts, transcription clients, microphone/audio ingestion boundaries.
- `voicechat`: Viewer voice-direct route, VDS bridge, runtime URL, and WebSocket planning contracts.
- `webgather`: web discovery, source fetch, extraction, staging, and search contract boundaries.

Module-specific health report builders belong inside each module. Adapter packages provide only current runtime provider/service availability and must not construct module health literals directly.

Do not place source under `.git/worktrees/*`; that path is Git metadata, not a tracked source tree.

## Design Documents

- [DESIGN.md](DESIGN.md): module goal, ownership, dependency direction, and state ownership.
- [CURRENT_MAP.md](CURRENT_MAP.md): current code ownership map from existing `internal/...` and `cmd/...` packages.
- [DEPENDENCY_RULES.md](DEPENDENCY_RULES.md): allowed and forbidden module dependencies.
- [MIGRATION_PLAN.md](MIGRATION_PLAN.md): incremental migration phases and validation gates.

## Implementation Status

This directory now contains module contract packages for `core`, `chat`, `worker`, `llm`, `tts`, `stt`, `voicechat`, `browseractor`, and `webgather`.
`modules/core.CurrentModuleDescriptors()` also exposes virtual state-observer descriptors such as `tts.playback` and `stt.viewer_input`; those are manifest entries, not separate source directories.
The first compatibility adapters live in `internal/adapter/modulebridge` so existing Chat orchestration, providers, and Worker execution can be exercised through the module contracts without a big-bang move.
Runtime module metadata is exposed at `/viewer/modules/manifest`.
Runtime module health is exposed at `/viewer/modules/health` and includes a core aggregate `status`/`ready` result plus per-module reports.
LLM provider role diagnostics are exposed at `/viewer/modules/llm/diagnostics` without executing generation.
Worker execution contract diagnostics are exposed at `/viewer/modules/worker/diagnostics`.
TTS provider diagnostics are exposed at `/viewer/modules/tts/diagnostics` without executing synthesis.
STT provider diagnostics are exposed at `/viewer/modules/stt/diagnostics` without executing transcription.

Feature HTTP route registration now enters through `internal/features/*/registrar.go` for the Ver0.80 feature groups. This registrar layer is a dependency-handoff boundary only; the existing module contracts, adapter bridges, handler bodies, providers, CLI commands, and runtime jobs are not moved by that handoff.

This does not mean every implementation file has moved into module-named packages.
Implementation migration remains incremental so behavior, logs, Viewer/TTS/STT state, and tests can remain stable.

## RenCrow_CORE Ver0.80 Public Seed Notes

The Public repo seed must keep this README, `CURRENT_MAP.md`, and `DEPENDENCY_RULES.md` aligned with the actual module directory set. If a feature is not represented as a `modules/<id>` package yet, it must remain visible in `internal/features/<id>` and in the Feature Module Catalog rather than being silently omitted.
