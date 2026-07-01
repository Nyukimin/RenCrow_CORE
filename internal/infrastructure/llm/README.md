# LLM Infrastructure

This directory is split by responsibility.

- `providers/`: concrete LLM provider implementations and provider-specific embedders.
- `factory/`: construction logic that maps config to a provider implementation.
- `middleware/`: provider decorators such as date/time injection and concurrency limits.

The domain-facing interfaces remain in `internal/domain/llm`.
