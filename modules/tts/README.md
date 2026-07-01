# TTS Module

Owns text-to-speech integration boundaries.

Responsibilities:

- TTS provider contracts.
- Speech text normalization, emotion planning, emotion state/context types, and emotion prefix policy.
- Provider-facing emotion reason metadata construction.
- Voice mapping and synthesis request payloads.
- IdleChat speech/display text cleanup and topic-announcement formatting.
- IdleChat character ID normalization and voice profile mapping.
- IdleChat TTS session and payload plan construction.
- Route-based TTS session metadata planning for orchestrator lifecycles.
- Audio chunk contract, text chunking rules, and streaming chunk state toward Viewer playback.
- Playback/session state contracts, snapshot composition, and health/report construction that are reusable outside the composition root.
- Viewer active audio/input owner state used to decide which playback ACK may consume pending audio.
- Playback ACK normalization and receipt construction.
- Timeout consumption matching for public session routes.
- TTS bridge audio chunk/session-completed event payloads and Viewer/IdleChat playback event routing.
- Shared non-empty selection and trimmed equality helpers used by TTS provider policies.
- Runtime provider priority, provider-plan enumeration/first-selection, provider selection log policy, playback command normalization, voice selection, and Irodori provider option planning.
- Irodori defaults, voice/style resolution, synthesis endpoint URL, simple synthesis payload, Gradio run-generation/uploaded-audio payload, audio URL parsing, and loopback file URL rewrite rules.
- SBV2 voice alias, voice URL, editor API URL, editor request payload, punctuation, and audio prefix rules.
- Local audio path, relative path, and Viewer audio URL rules.
- RenCrow synthesis payload construction, provider parameter validation, voice fallback, and request ID header rules.
- RenCrow bridge defaults, session start normalization, speech text validation, and synthesis audio-output validation.
- RenCrow synthesis error parsing, retry policy, transport retry classification, and retry backoff rules.
- Diagnostics snapshot policy and provider unavailable message.

Non-responsibilities:

- HTTP endpoint ownership for Viewer playback ACK.
- IdleChat dialogue/session orchestration.
- LLM response generation.
- STT transcription.

Current high-impact areas:

- `internal/infrastructure/tts`
- `cmd/picoclaw/tts_*`
- `cmd/picoclaw/idlechat_tts_*`

Boundary note:

TTS provider code owns synthesis and audio chunk payloads. The module also owns reusable playback/session state rules such as speech filtering, emotion planning, provider-facing emotion reason metadata, emotion prefixing, text chunking, streaming chunk state, playback state snapshot/health/report construction, playback state endpoint messages, active audio owner, public session routing, pending playback store, timeout consumption matching, ACK normalization, ACK receipt construction, bridge event payload/route construction, IdleChat TTS plan construction, shared string-selection helpers, route-based TTS session metadata planning, runtime provider priority and plan enumeration, provider selection log policy, Irodori defaults/URL/payload/response rules, SBV2 editor request payload rules, local audio path/URL rules, RenCrow bridge defaults/session/text/output rules, RenCrow synthesis payload validation, and RenCrow synthesis retry policy. Composition/orchestration code owns HTTP decoding, runtime wiring, concrete provider construction, bridge calls, file serving, and cross-state cleanup application.

Design references:

- [../DESIGN.md](../DESIGN.md)
- [../CURRENT_MAP.md](../CURRENT_MAP.md)
- [../DEPENDENCY_RULES.md](../DEPENDENCY_RULES.md)
