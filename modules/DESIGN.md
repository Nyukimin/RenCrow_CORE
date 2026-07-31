# RenCrow Module Design

Status: active design for RenCrow_CORE-internal contract packages. Product-level
module ownership is defined by `docs/01_システム概要.md` and takes precedence.

## 1. Goal

RenCrow_CORE currently exposes the following local contract packages:

```text
browseractor
core
chat
worker
llm
tts
stt
voicechat
webgather
```

These packages do not replace the independent RenCrow_LLM, RenCrow_STT,
RenCrow_TTS, RenCrow_Tools, and other sibling repositories. They contain the
CORE-facing DTOs, events, pure policy, request planning, and state
projections needed by this repository. External module implementation bodies
remain in their owning repositories.

The local contract packages use the CORE layout:

```text
internal/domain
internal/application
internal/infrastructure
internal/adapter
cmd/rencrow
```

`modules/` is a stable contract and pure-policy boundary inside CORE. Concrete
HTTP clients, process wiring, secrets, and external runtime lifecycle do not
move here merely to mirror the sibling repository names. Existing CORE
implementations stay in `internal/...`; composition and process lifecycle stay
in `cmd/rencrow`.

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

Owns CORE-side LLM contracts and pure request/response policy.

Examples:

- request plans targeting RenCrow_LLM execution aliases
- RenCrow_LLM logical alias and request contracts
- response normalization before Chat display
- provider health/capability interpretation

RenCrow_LLM owns RenCrow LLM Gateway, RenCrow LLM Runtime, Backend/Model
mapping, capacity, and provider adapter implementation.

### tts

Owns CORE-side text-to-speech integration contracts.

Examples:

- emotion prefix and speech text policy
- voice mapping
- synthesis request payloads
- audio chunk payloads for Viewer playback

TTS does not own playback ACK completion or IdleChat pending state. Those remain Chat/Core integration concerns.
RenCrow_TTS owns the public TTS API and concrete synthesis gateway; engine and
model implementation remain outside CORE.

### stt

Owns CORE-side speech-to-text integration contracts.

Examples:

- STT provider health/readiness
- audio upload/transcription payloads
- transcription result normalization
- local/remote STT provider adapters

STT does not own Viewer microphone UI state or Chat input routing after final transcript delivery.
RenCrow_STT owns concrete transcription processing and runtime operation.

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

`cmd/rencrow` remains the composition root. It may wire modules together but should not become a business-logic owner.

Composition root responsibilities:

- load config
- create providers
- wire application services
- expose HTTP routes
- start/stop runtime services

If CORE runtime code grows reusable pure policy, move that policy into the
corresponding local contract package when doing so reduces retest scope.
External runtime implementation still belongs to its sibling repository.

## 6. Completion Criteria For Design

The module design is complete when the repository contains:

- module list and responsibilities
- dependency direction rules
- state ownership table
- current-code ownership map
- README files for all modules

Physical package movement is performed only when a concrete reuse,
change-isolation, or testability benefit exists.
