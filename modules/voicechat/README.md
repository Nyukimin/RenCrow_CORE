# VOICECHAT Module

Owns Viewer voice-direct contracts for VDS bridge planning and WebSocket route shape.

Responsibilities:

- VoiceChat route constants and WebSocket route list.
- Voice input mode normalization.
- Runtime URL and gateway planning.
- VDS bridge availability planning.
- VoiceChat error and event contract names.

Non-responsibilities:

- STT provider execution.
- TTS provider execution.
- LLM session execution.
- Viewer DOM or audio playback state.

Current high-impact areas:

- `modules/voicechat`
- `cmd/picoclaw/voice_chat_runtime_*`
- `internal/application/orchestrator/voice_direct*`

Boundary note:

`modules/voicechat` owns reusable VoiceChat contracts and planning rules. Existing WebSocket handlers, runtime session wiring, VDS gateway calls, STT/TTS providers, and Viewer event emission remain in their current runtime packages until the voice feature registrar is introduced.

Design references:

- [../DESIGN.md](../DESIGN.md)
- [../CURRENT_MAP.md](../CURRENT_MAP.md)
- [../DEPENDENCY_RULES.md](../DEPENDENCY_RULES.md)
