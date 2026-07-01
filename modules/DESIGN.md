# RenCrow Module Design

Status: design complete for module boundaries. This document defines the target module split and the rules for migrating existing code without moving implementation in this step.

## 1. Goal

RenCrow is organized around six local modules:

```text
core
chat
worker
llm
tts
stt
```

The module design does not create separate repositories. All modules live inside this repository/worktree and must remain compatible with the existing Clean Architecture layout:

```text
internal/domain
internal/application
internal/infrastructure
internal/adapter
cmd/picoclaw
```

`modules/` is the design and ownership boundary. Actual Go packages may stay in the existing `internal/...` tree until a dedicated migration phase moves or wraps them.

## 2. Dependency Direction

Allowed dependency flow:

```text
adapter/cmd
  -> chat / worker / core application services
  -> domain contracts
  -> infrastructure providers through interfaces
```

Module-level allowed calls:

```text
chat   -> core, llm, tts, stt
worker -> core, llm
llm    -> core
tts    -> core
stt    -> core
core   -> no module-specific dependency
```

Disallowed calls:

```text
llm -> tts
llm -> stt
tts -> llm
tts -> stt
stt -> llm
stt -> tts
worker -> tts
worker -> stt
core -> chat / worker / llm / tts / stt
```

Rationale:

- LLM produces text or structured decisions, never audio or transcription.
- TTS turns accepted text into audio, never generates text.
- STT turns audio into text, never routes or answers.
- Chat owns user-facing routing and presentation policy.
- Worker owns execution side effects.
- Core owns shared contracts and state ownership rules.

## 3. Module Responsibilities

### core

Owns shared contracts and lifecycle rules.

Examples:

- session/request/response/utterance/chunk identity rules
- cross-module event contracts
- state ownership policy
- lifecycle cleanup rules

### chat

Owns user-facing conversation and routing.

Examples:

- Chat/IdleChat dialogue flow
- persona-facing response policy
- Viewer-facing text selection
- route decisions into Worker/LLM/TTS/STT

### worker

Owns command and file execution.

Examples:

- shell command execution
- file edits and patch application
- test/build/restart jobs
- execution logs and reports

### llm

Owns LLM provider contracts and routing adapters.

Examples:

- local/external LLM provider factories
- OpenAI-compatible client contracts
- response normalization before Chat display
- provider health/capability interpretation

### tts

Owns text-to-speech integration contracts.

Examples:

- emotion prefix and speech text policy
- voice mapping
- synthesis request payloads
- audio chunk payloads for Viewer playback

TTS does not own playback ACK completion or IdleChat pending state. Those remain Chat/Core integration concerns.

### stt

Owns speech-to-text integration contracts.

Examples:

- STT provider health/readiness
- audio upload/transcription payloads
- transcription result normalization
- local/remote STT provider adapters

STT does not own Viewer microphone UI state or Chat input routing after final transcript delivery.

## 4. State Ownership

State must have a single owner:

| State | Owner module | Notes |
| --- | --- | --- |
| User-visible conversation text | chat | TTS chunk text is not display truth. |
| Execution job status | worker | Chat only presents result. |
| LLM provider health | llm | Runtime health may aggregate it. |
| TTS synthesis health | tts | Playback ACK state is not owned by TTS. |
| STT provider readiness | stt | Viewer mic state is not owned by STT. |
| Session/request/response identity rules | core | Shared by all modules. |
| Viewer active audio owner | chat/core integration | Do not let TTS provider consume pending directly. |

## 5. Runtime Boundary

`cmd/picoclaw` remains the composition root. It may wire modules together but should not become a business-logic owner.

Composition root responsibilities:

- load config
- create providers
- wire application services
- expose HTTP routes
- start/stop runtime services

If runtime code grows module-specific policy, move that policy into the corresponding module package and keep `cmd/picoclaw` as wiring.

## 6. Migration Policy

Module migration must be incremental:

1. Add or update module-level contract/tests.
2. Create thin adapter/wrapper packages if needed.
3. Move implementation only when imports and ownership are clear.
4. Run package-local tests plus impacted integration tests.
5. Keep behavior and logs stable unless the migration explicitly changes them.

Avoid big-bang file moves. Existing package paths are part of the current API surface for tests and runtime wiring.

## 7. Completion Criteria For Design

The module design is complete when the repository contains:

- module list and responsibilities
- dependency direction rules
- state ownership table
- current-code ownership map
- migration plan with validation gates
- README files for all modules

Implementation migration is a separate goal.
