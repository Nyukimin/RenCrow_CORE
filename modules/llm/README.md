# LLM Module

Owns the RenCrow_CORE side of language-model integration boundaries. The
RenCrow_LLM repository owns the central Gateway, Host Node, physical target
mapping, capacity, and concrete runtime adapter implementation.

Responsibilities:

- CORE-side request/response contracts for RenCrow_LLM.
- Request metadata for Agent, Execution Role, alias, trace, and cancellation.
- Conversation summary and embedding logical alias planning.
- Coder logical alias and capability planning.
- RenCrow_LLM Gateway ThinkingBridge request flags, option filtering, and leaked-reasoning cleanup policy.
- Model routing policy adapters.
- Prompt-facing request/response normalization.
- Request copy semantics for mutable provider-facing fields such as message parts and provider options.
- Health and capability interpretation for RenCrow_LLM roles.

Non-responsibilities:

- Text-to-speech synthesis.
- Speech-to-text transcription.
- Worker command execution.

Current high-impact areas:

- `internal/domain/llm`
- `internal/infrastructure/llm`
- `cmd/rencrow/llm_*`
- `cmd/rencrow/runtime_llm_providers.go`

Boundary note:

`modules/llm` owns CORE-side request/response contracts, request metadata,
normalization, and diagnostics. Agent execution sends the logical Execution
Role/alias to RenCrow_LLM. RenCrow_LLM owns provider selection, physical model
configuration, process lifecycle, and backend health.

Design references:

- [../DESIGN.md](../DESIGN.md)
- [../CURRENT_MAP.md](../CURRENT_MAP.md)
- [../DEPENDENCY_RULES.md](../DEPENDENCY_RULES.md)
