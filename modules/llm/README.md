# LLM Module

Owns the RenCrow_CORE side of language-model integration boundaries. The
RenCrow_LLM repository owns the central Gateway, Host Node, physical target
mapping, capacity, and concrete runtime adapter implementation.

Responsibilities:

- CORE-side request/response contracts for RenCrow_LLM.
- Request metadata for Agent, Execution Role, alias, trace, and cancellation.
- Legacy/development-only local provider planning for provider kind, base URL,
  model, timeout, concurrency, Ollama `num_ctx`, fallback, and warmup.
- Conversation summary and embedding provider plan construction.
- Coder provider validation/planning for provider kind, required credentials, required base URLs, and local OpenAI timeout.
- OpenAI-compatible ThinkingBridge request flags, provider-option filtering, and leaked-reasoning cleanup policy.
- Model routing policy adapters.
- Prompt-facing request/response normalization.
- Request copy semantics for mutable provider-facing fields such as message parts and provider options.
- Health and capability interpretation for LLM providers, including local health-check enablement and role-name normalization.

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
normalization, and diagnostics. Production Agent execution sends only the
logical Execution Role/alias to RenCrow_LLM. Local provider selection,
`num_ctx`, concrete base URLs, warmup, and direct provider construction are
legacy/development compatibility and must not become the production mapping
source of truth.

Design references:

- [../DESIGN.md](../DESIGN.md)
- [../CURRENT_MAP.md](../CURRENT_MAP.md)
- [../DEPENDENCY_RULES.md](../DEPENDENCY_RULES.md)
