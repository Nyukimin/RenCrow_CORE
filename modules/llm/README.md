# LLM Module

Owns language model integration boundaries.

Responsibilities:

- Local and external LLM provider contracts.
- Local LLM role/alias runtime resolution for provider kind, base URL, model, timeout, concurrency, and Ollama `num_ctx`.
- Primary Chat/Worker/Heavy/Wild provider plan construction, including local-vs-legacy mode, legacy Ollama worker fallback, role `num_ctx`, and warmup timeout selection.
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
- `cmd/picoclaw/llm_*`
- `cmd/picoclaw/runtime_llm_providers.go`

Boundary note:

`modules/llm` owns role/alias selection rules, primary provider planning, role `num_ctx`, conversation summary/embedder provider planning, Coder provider validation/planning, local health-check policy, ThinkingBridge request/response cleanup policy, request copy semantics, and provider-facing request/response contracts. Runtime code still owns concrete provider construction, API keys, middleware wrapping, warmup execution, and process wiring.

Design references:

- [../DESIGN.md](../DESIGN.md)
- [../CURRENT_MAP.md](../CURRENT_MAP.md)
- [../DEPENDENCY_RULES.md](../DEPENDENCY_RULES.md)
