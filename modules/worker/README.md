# WORKER Module

Owns execution work requested by Chat or orchestration.

Responsibilities:

- Command execution.
- File operations.
- Test, build, restart, and operational jobs.
- Execution result reporting.
- Local/distributed worker-agent availability and reply-routing rules.
- Autonomous execution route classification, capability selection, execution step labels, failure classification, retry prompt construction, and attempt verification policy.
- Worker proposal execution failure classification and retryability policy.
- Worker result construction for failed actions and patch execution summaries.
- Unavailable Worker executor health/result policy.
- Coder capability plan construction from detected LLM capabilities, configured coder slots, and quality overrides.
- External Coder policy construction so automatic CODE routing keeps external providers explicit-only.
- Coder setup planning for slot name normalization, slot lookup, disabled slots, and shared LightMemory initialization defaults.

Non-responsibilities:

- User-facing dialogue policy.
- LLM model serving.
- TTS/STT engine implementation.

Current high-impact areas:

- `internal/application/service/worker_execution_*`
- `internal/infrastructure/tools`
- execution-related domain/application packages

Boundary note:

`modules/worker` owns reusable execution contracts, worker-agent routing rules, Worker result construction, unavailable executor health/result policy, Coder capability planning, external Coder policy construction, Coder slot name/index policy, Coder setup planning, autonomous execution classification rules, proposal execution failure classification, and route/contract attempt verification policy. Runtime code still owns transport registration, goroutine loops, concrete WorkerExecutionService calls, concrete provider construction, persona file loading, conversion from internal domain types, and message delivery.

Design references:

- [../DESIGN.md](../DESIGN.md)
- [../CURRENT_MAP.md](../CURRENT_MAP.md)
- [../DEPENDENCY_RULES.md](../DEPENDENCY_RULES.md)
