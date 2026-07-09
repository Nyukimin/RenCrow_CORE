# BROWSERACTOR Module

Owns browser automation contracts and safety classification for browser-side work.

Responsibilities:

- Browser run request and response DTOs.
- Action validation and unsupported-action errors.
- Risk classification for read-only, draft input, navigation, and external-effect actions.
- Artifact and doctor response contracts.
- URL origin normalization for browser automation boundaries.

Non-responsibilities:

- Playwright or browser process execution.
- Persistent browser profile management.
- Knowledge or memory promotion.
- Viewer display policy.

Current high-impact areas:

- `modules/browseractor`
- Browser automation callers under `cmd/rencrow`
- Browser trace and web gathering application packages

Boundary note:

`modules/browseractor` defines the safety and DTO contract only. Runtime execution, browser profile persistence, trace storage, and external side effects stay in application, infrastructure, or tool-specific adapters until a later migration phase.

Design references:

- [../DESIGN.md](../DESIGN.md)
- [../CURRENT_MAP.md](../CURRENT_MAP.md)
- [../DEPENDENCY_RULES.md](../DEPENDENCY_RULES.md)
