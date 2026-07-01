# CORE Module

Owns shared contracts and lifecycle boundaries across RenCrow modules.

Responsibilities:

- Cross-module data contracts.
- Aggregate module health status rules.
- Module descriptors for runtime manifest output.
- Generic module HTTP method and JSON response helpers.
- Session and lifecycle ownership rules.
- State ownership boundaries.
- Integration policy between Chat, Worker, LLM, TTS, and STT.

Non-responsibilities:

- Direct model inference.
- Direct audio synthesis or transcription.
- Viewer-specific rendering.

Design references:

- [../DESIGN.md](../DESIGN.md)
- [../DEPENDENCY_RULES.md](../DEPENDENCY_RULES.md)
- [../CURRENT_MAP.md](../CURRENT_MAP.md)
