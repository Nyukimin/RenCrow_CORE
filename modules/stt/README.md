# STT Module

Owns the RenCrow_CORE side of speech-to-text integration boundaries.
RenCrow_STT owns concrete transcription processing and runtime operation.
CORE uses the RenCrow_STT Gateway base URL, its `POST /v1/audio/transcriptions`
contract for the HTTP routes, and its audio stream (`GET /v1/audio/stream`,
protocol `stt.stream.v1`) for `/stt`. Viewer and PORTAL WebSocket input terminates
at the CORE same-origin `/stt` route, which relays to that stream: CORE does not
detect speech, accumulate utterances or call the recognition target for `/stt`.

Responsibilities:

- STT provider contracts.
- RenCrow_STT Gateway URL normalization and fixed transcription endpoint construction.
- CORE-side timeout, busy policy, and debug planning.
- Busy policy normalization and execution-mode planning.
- WebSocket handler selection, route paths, and text/binary frame classification rules.
- WebSocket control-message parsing (`start` / `stop` / `cancel`, `utterance_id`) and WAV detection (`/stt` takes raw PCM16 only).
- The `/stt` relay rules (`relay.go`): which client control becomes which stream message, the accepted upstream event types, `session_ready` validation, frame splitting, and `RelayTable`, the bookkeeping that keeps "every announced utterance gets exactly one terminal event" true at the relay boundary (duplicate terminals dropped, upstream `seq` checked, the watchdog deadline, which terminals are recorded).
- The `utterance.v1` client event builders (`session_info`, `ready`, coded `error`, event stamping with `utterance_id` and CORE's own `seq`) and the `stt.stream` health report derived from the Gateway `/health`.
- The stream URL of a Gateway base URL (`GatewayStreamURL`).
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
CORE same-origin WebSocket input policy, control-message and relay rules, the
terminal-event invariant of the relay, request copy semantics, normalized results,
HTTP result/envelope policy, and reusable Viewer input health/reporting rules.
RenCrow_STT owns target selection and target-specific provider configuration,
and everything about where an utterance ends (silence detection, minimum and
maximum length, idle timeout, phrase filtering).
Chat/Viewer integration owns microphone UI rendering, final transcript
injection, concrete handler wiring, channel/goroutine execution details, and
filesystem writes.

Design references:

- [../DESIGN.md](../DESIGN.md)
- [../CURRENT_MAP.md](../CURRENT_MAP.md)
- [../DEPENDENCY_RULES.md](../DEPENDENCY_RULES.md)
