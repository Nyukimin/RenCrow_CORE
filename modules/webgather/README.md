# WEBGATHER Module

Owns web discovery, fetch, extraction, and staging contracts.

Responsibilities:

- Fetch, search, and search-and-fetch DTOs.
- Fetch policy defaults and normalization.
- Provider interfaces for fetch, extract, search, and staging writes.
- Fetch response status, error, and diagnostics fields.
- Search result and staging record contracts.

Non-responsibilities:

- HTTP transport implementation.
- Browser execution implementation.
- Source Registry review decisions.
- Knowledge or memory promotion.

Current high-impact areas:

- `modules/webgather`
- `internal/application/webgather`
- `internal/infrastructure/webgather`
- `cmd/rencrow/cli_web_gather.go`

Boundary note:

`modules/webgather` keeps discovery and fetch contracts separate from evidence review and Knowledge/Memory promotion. Search results are discovery inputs only; source read, browser evidence, and promotion remain separate feature responsibilities.

Design references:

- [../DESIGN.md](../DESIGN.md)
- [../CURRENT_MAP.md](../CURRENT_MAP.md)
- [../DEPENDENCY_RULES.md](../DEPENDENCY_RULES.md)
