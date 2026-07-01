# STT Module

Owns speech-to-text integration boundaries.

Responsibilities:

- STT provider contracts.
- Runtime URL inference for provider, base, gateway, and WebSocket stream endpoints.
- Runtime provider option planning and defaults for provider, model, language, timeout, busy policy, and external HTTP URL.
- Busy policy normalization and execution-mode planning.
- WebSocket handler selection, compatibility route paths, and text/binary frame classification rules.
- WebSocket control-message parsing, PCM16/WAV payload normalization, silence detection, draft/final session rules, draft state update/reset rules, session event payloads, timeout error classification, and adaptive timeout/cooldown state rules.
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
- `cmd/picoclaw/stt_*`
- `internal/adapter/viewer/stt_*`

Boundary note:

STT owns transcription provider readiness, runtime URL inference, runtime provider option planning/defaults, busy policy planning, WebSocket routing policy, audio/control-message normalization rules, draft/final session rules, draft state update/reset rules, session event payloads, timeout error classification, adaptive timeout/cooldown state rules, request copy semantics for audio buffers, normalized results, HTTP result/envelope policy, and reusable Viewer input health/reporting rules and endpoint messages. Chat/Viewer integration owns microphone UI rendering, final transcript injection, concrete provider construction, provider process wiring, concrete HTTP/WebSocket handlers, environment variables, channel/goroutine execution details, and filesystem writes.

Design references:

- [../DESIGN.md](../DESIGN.md)
- [../CURRENT_MAP.md](../CURRENT_MAP.md)
- [../DEPENDENCY_RULES.md](../DEPENDENCY_RULES.md)
