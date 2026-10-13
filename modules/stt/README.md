# STT Module

Owns the RenCrow_CORE side of speech-to-text integration boundaries.
RenCrow_STT owns concrete transcription processing and runtime operation.
CORE uses the RenCrow_STT Gateway base URL and its
`POST /v1/audio/transcriptions` contract. Viewer WebSocket input terminates at
the CORE same-origin `/stt` route.

Responsibilities:

- STT provider contracts.
- RenCrow_STT Gateway URL normalization and fixed transcription endpoint construction.
- CORE-side timeout, busy policy, and debug planning.
- Busy policy normalization and execution-mode planning.
- WebSocket handler selection, route paths, and text/binary frame classification rules.
- WebSocket control-message parsing (`start` / `stop` / `cancel`, `utterance_id`), PCM16/WAV payload normalization, and the utterance rules: `UtteranceBuffer` (accumulation, max length, voiced-length minimum, WAV output), `UtteranceLimits`, whole-utterance noise-phrase filtering, transcript classification, and the `utterance.v1` session event payloads (`final`, `error`, `cancelled`, `speech_start`, `ready`, event stamping with `utterance_id` and `seq`).
- Timeout error classification and adaptive timeout/cooldown state rules.
- Diagnostics snapshot policy and provider unavailable message.
- Audio input and transcription request payloads.
- Request copy semantics for mutable audio buffers before provider calls.
- Transcription result normalization.
- HTTP transcription result normalization, error-status mapping, and ChatInput envelope construction.
- STT health and readiness interpretation.
- Viewer microphone/input state reporting and debug artifact path defaults.

Non-responsibilities:

- Viewer microphone UI state ownership.
- LLM response generation.
- TTS synthesis.
- Chat memory ownership.

Current high-impact areas:

- `internal/infrastructure/stt`
- `cmd/rencrow/stt_*`
- `internal/adapter/viewer/stt_*`

Boundary note:

STT owns Gateway readiness, Gateway URL normalization, busy policy planning,
CORE same-origin WebSocket input policy, audio/control-message normalization
rules, utterance accumulation and final/error/cancelled event rules, request copy semantics, normalized results,
HTTP result/envelope policy, and reusable Viewer input health/reporting rules.
RenCrow_STT owns target selection and target-specific provider configuration.
Chat/Viewer integration owns microphone UI rendering, final transcript
injection, concrete handler wiring, channel/goroutine execution details, and
filesystem writes.

Design references:

- [../DESIGN.md](../DESIGN.md)
- [../CURRENT_MAP.md](../CURRENT_MAP.md)
- [../DEPENDENCY_RULES.md](../DEPENDENCY_RULES.md)
