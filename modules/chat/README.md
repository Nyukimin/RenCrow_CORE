# CHAT Module

Owns user-facing conversation behavior.

Responsibilities:

- User intent handling.
- Dialogue and routing decisions.
- Route name and route-decision normalization for module adapters.
- Route report construction and route-service unavailable message.
- IdleChat forecast provider plan construction, Coder label lookup, explicit external-provider gating, labels, and speaker LLM option normalization.
- IdleChat topic category contracts, daily seed DTOs, seed/candidate/judge DTOs, deterministic topic validation, seed selection/labels, prompt text construction, JSON parsing, and recent-topic similarity checks.
- Response presentation contracts.
- Viewer-facing text policy.

Non-responsibilities:

- Command execution.
- Direct file edits.
- Direct TTS/STT engine implementation.
- Direct model server ownership.

Current high-impact areas:

- `internal/application/orchestrator`
- `internal/application/idlechat`
- Viewer-facing handlers in `internal/adapter/viewer`

Design references:

- [../DESIGN.md](../DESIGN.md)
- [../CURRENT_MAP.md](../CURRENT_MAP.md)
- [../MIGRATION_PLAN.md](../MIGRATION_PLAN.md)
