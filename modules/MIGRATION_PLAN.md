# Module Migration Plan

This plan turns the design boundary into implementation without a big-bang move.

## Phase 0: Design Baseline

Status: complete.

Deliverables:

- `modules/README.md`
- `modules/DESIGN.md`
- `modules/CURRENT_MAP.md`
- `modules/DEPENDENCY_RULES.md`
- module README files

Validation:

- documentation is present
- `git diff --check`
- current focused tests still pass

## Phase 1: Contract Packages

Status: complete.

Goal: create stable contracts before moving implementation.

Actions:

- Add core contract packages for shared IDs/events where needed.
- Add llm/tts/stt provider contract seams if current interfaces are too infrastructure-specific.
- Add import boundary tests for new module packages.

Validation:

- package-local tests
- `go test ./modules/...`
- `go test ./internal/adapter/modulebridge`

## Phase 2: Provider Module Extraction

Status: in progress.

Goal: move provider-specific code behind module contracts.

Actions:

- Wrap `internal/domain/llm` provider contracts with `modules/llm`.
- Wrap `internal/infrastructure/tts` provider contracts with `modules/tts`.
- Wrap `internal/infrastructure/stt` provider contracts with `modules/stt`.
- Keep `cmd/rencrow` as composition root only.

Current deliverable:

- `internal/adapter/modulebridge` provides LLM/TTS/STT compatibility adapters.
- `internal/adapter/modulebridge` owns construction of the LLM module role provider map from existing domain providers.
- `internal/adapter/modulebridge` owns runtime TTS/STT module adapter constructors, including TTS diagnostics output naming.
- `internal/adapter/modulebridge` owns runtime Chat service and Worker executor module constructors.
- `cmd/rencrow` wires module contract providers into runtime state.
- `modules/llm` owns standard runtime role names, role provider map construction, and module generate response construction.
- `modules/tts` owns module synthesis result construction from provider audio output.
- `modules/tts` owns runtime TTS provider priority, playback command normalization, voice selection, and Irodori provider option planning; `cmd/rencrow` keeps concrete provider construction.
- `modules/tts` owns Irodori defaults, voice/style resolution, synthesis endpoint URL construction, Gradio run-generation payload ordering, audio URL parsing, download URL resolution, and loopback file URL rewrite rules; `internal/infrastructure/tts` keeps HTTP execution and thin adapter code.
- `modules/tts` owns SBV2 voice alias resolution, voice URL construction, editor API URL construction, TTS punctuation, and audio prefix sanitization rules; `internal/infrastructure/tts` keeps HTTP execution and thin adapter code.
- `modules/stt` owns transcription result construction, segment duration normalization, and STT provider health report normalization.
- `modules/stt` owns runtime STT provider option planning and defaults for provider, model, language, timeout, busy policy, and external HTTP URL; `cmd/rencrow` keeps concrete provider construction.
- `modules/llm` owns the generic health-checked provider wrapper used by runtime diagnostics.
- `modules/llm` owns external health-check status normalization and local LLM health-check role-name normalization.
- `modules/core` owns the current module inventory, runtime endpoint constants, contracts, and state ownership descriptors.
- `/viewer/modules/manifest` exposes the current module inventory, contracts, endpoints, and state ownership metadata from `modules/core`.
- `/viewer/modules/health` exposes the current module-contract health view by passing runtime providers into `modules/core` health aggregation.
- `modules/core` owns generic provider health normalization for nil providers, module names, and `checked_at`.
- `modules/core` owns module health snapshot construction from health reports.
- `modules/core` owns runtime module health report ordering and aggregate snapshot construction across Chat, Worker, TTS, TTS playback, STT, and STT Viewer input.
- `modules/llm` owns the generation diagnostics policy; `cmd/rencrow` only exposes it over HTTP.
- `modules/llm` owns role-status collection for LLM diagnostics; `cmd/rencrow` only provides the provider map and encodes the response.
- `modules/llm` owns role-qualified health report collection for LLM providers.
- `modules/llm` owns primary Chat/Worker/Heavy/Wild provider plan construction, including local-vs-legacy mode selection, legacy Ollama worker-model fallback, and local warmup timeout selection; `cmd/rencrow` keeps concrete provider construction, API keys, middleware wrapping, warmup goroutines, and process wiring.
- `modules/llm` owns conversation summary and embedding provider plan construction; `cmd/rencrow` keeps concrete provider/embedder construction and API key handling.
- `modules/llm` owns Coder provider validation/planning for provider kind, required credentials, required base URLs, and local OpenAI timeout; `internal/infrastructure/llm/factory` keeps concrete provider construction.
- `modules/llm` owns OpenAI-compatible ThinkingBridge request flags, provider-option filtering, and leaked-reasoning cleanup policy; current OpenAI provider keeps HTTP execution and compatibility wrappers.
- `modules/llm`, `modules/tts`, `modules/stt`, and `modules/worker` own diagnostics snapshot construction; `cmd/rencrow` only invokes and encodes snapshots.
- `modules/core` owns manifest snapshot construction; `cmd/rencrow` only provides descriptors and exposes them over HTTP.
- `/viewer/modules/llm/diagnostics` exposes LLM role/provider health and generation contract metadata without executing generation.

Validation:

- LLM provider tests
- TTS chunk/provider tests
- STT provider tests
- runtime config tests

Current validation:

- `go test ./modules/... ./internal/adapter/modulebridge`
- `go test ./internal/domain/llm ./internal/infrastructure/tts ./internal/infrastructure/stt ./internal/adapter/modulebridge ./modules/...`
- `go test ./cmd/rencrow ./internal/adapter/modulebridge ./modules/...`
- `go test ./...`
- `go test ./modules -run TestModuleHTTPHandlersDoNotOwnResponseContracts -count=1`

## Phase 3: Chat / Worker Split

Status: in progress.

Goal: separate user-facing orchestration from execution side effects.

Actions:

- Keep `internal/application/service/worker_execution_*` under worker ownership.
- Extract Chat-facing orchestration policies from mixed orchestrator files.
- Keep Coder/Worker execution routes from importing TTS/STT.

Current deliverable:

- `internal/adapter/modulebridge` exposes the existing orchestrator processor as `modules/chat.Service`.
- `internal/adapter/modulebridge` exposes a runtime Chat service constructor that wires the Mio route policy.
- `modules/chat.RoutePolicy` is the module contract for route decision.
- `modules/chat` owns route-decision report construction; `cmd/rencrow` only decodes the HTTP request, invokes the service, and encodes the report.
- `modules/chat` owns route-name normalization from runtime route labels into module routes.
- `modules/chat` owns Viewer input default normalization for channel and user identity.
- `modules/chat` owns IdleChat forecast provider policy and speaker LLM option normalization; `cmd/rencrow` still creates concrete providers and logs selection.
- `modules/chat` owns IdleChat topic category contracts, daily seed DTOs, topic seed/candidate/judge DTOs, deterministic topic validation, RSS seed item parsing, seed selection/labels, prompt text construction, JSON parsing, judge thresholds, and recent-topic similarity checks; `internal/application/idlechat` now keeps only the remaining orchestration and source-fetching glue around the module contract.
- `internal/adapter/modulebridge` adapts the current Mio route decision implementation to `modules/chat.RoutePolicy`.
- `internal/adapter/modulebridge` exposes `WorkerExecutionService` as `modules/worker.Executor`.
- `internal/adapter/modulebridge` exposes a runtime Worker executor constructor.
- `modules/worker` owns Worker action tool validation, proposal patch argument extraction, and patch execution result mapping.
- `modules/worker` owns Worker proposal execution failure classification and retryability policy; `internal/application/service` keeps concrete execution and result mutation.
- `modules/worker` owns local/distributed worker-agent availability rules, unavailable reason formatting, and local coder reply target selection; `cmd/rencrow` passes concrete transport/adapter availability and still runs loops/delivery.
- `modules/worker` owns Coder capability plan construction from detected LLM capabilities, configured coder slots, and quality overrides; `cmd/rencrow` converts app/domain DTOs.
- `modules/worker` owns autonomous execution route classification, capability selection, execution step labels, failure classification, retry prompt construction, and route/contract attempt verification policy; `internal/application/orchestrator` keeps conversion from internal contract/result types and executor invocation.
- `/viewer/modules/health` includes Chat service and Worker executor status.
- `/viewer/modules/chat/route` exposes Chat route decisions through the module contract.
- `/viewer/modules/worker/diagnostics` exposes Worker executor health and supported tools without executing actions.
- `modules/llm` owns local LLM role/alias resolution for provider kind, base URL, model, timeout, and concurrency; `cmd/rencrow` keeps concrete provider construction and middleware wiring.
- `modules/llm` owns primary provider planning for Chat/Worker/Heavy/Wild; `cmd/rencrow` converts the plan into Ollama/OpenAI providers and keeps API keys, middleware, warmup goroutines, and process wiring.
- `modules/llm` owns conversation summary and embedding provider planning; `cmd/rencrow` converts those plans into concrete conversation providers.
- `modules/llm` owns Coder provider validation/planning; `internal/infrastructure/llm/factory` converts validated plans into concrete provider instances.
- `modules/llm` owns OpenAI-compatible ThinkingBridge request/response cleanup policy; `internal/infrastructure/llm/providers/openai` applies those rules around concrete HTTP calls.

Remaining work:

- Move route-decision policy implementation out of legacy agent/orchestrator internals once the module contract can represent the legacy policy inputs directly.
- Move remaining IdleChat topic generation orchestration, prompt file loading, network source fetching, and cache lifecycle behind Chat/LLM module seams after the pure topic policy is stable.
- Split remaining Viewer/TTS pending and playback state implementation details from provider-owned TTS concerns.

Validation:

- orchestrator tests
- worker execution tests
- route dispatch tests

Current validation:

- `go test ./internal/application/service ./internal/application/orchestrator ./cmd/rencrow ./internal/adapter/modulebridge ./modules/...`
- `go test ./...`

## Phase 4: TTS/STT Viewer Boundary Cleanup

Status: in progress.

Goal: make state ownership explicit around audio and transcript flows.

Actions:

- Split provider TTS from playback ACK/pending lifecycle.
- Keep HTTP decoding and cross-state cleanup in the composition root while moving reusable playback/session state rules into `modules/tts`.
- Keep STT provider state separate from Viewer microphone UI state.
- Move STT runtime URL inference for provider/base/gateway/WebSocket endpoints into `modules/stt`; keep environment reads, provider construction, and route registration in `cmd/rencrow`.

Current deliverable:

- `modules/tts.PlaybackStateObserver` separates playback/pending state from synthesis provider contracts.
- `modules/tts` owns playback-state report construction; `cmd/rencrow` only collects the runtime snapshot and encodes the report.
- `modules/tts` owns pending playback snapshot construction, including deterministic ID ordering and copy semantics; `cmd/rencrow` only collects current pending IDs from runtime maps.
- `modules/tts` owns the pending playback store, including pending wait channels, response lookup, topic gates, and topic route cleanup decisions; `cmd/rencrow` keeps compatibility wrapper functions and applies public-session cleanup.
- `modules/tts` owns playback-state snapshot composition from pending state and public TTS session routing state.
- `modules/tts` owns the public session route store, including route map, stale generation tracking, and chunk/response sequence counters; `cmd/rencrow` keeps compatibility wrapper functions for runtime callers.
- `modules/tts` owns timeout consumption matching for public session routes and returns `PlaybackTimeoutConsumption`; `cmd/rencrow` applies the matched internal session IDs to pending cleanup.
- `modules/tts` owns Viewer active audio/input owner state, playback ACK normalization rules, pending-consumption eligibility, and ACK receipt construction, including deprecated `fallback` ACK conversion to explicit `error` ACK metadata; `cmd/rencrow` still owns HTTP decoding and applies pending consumption.
- `modules/tts` owns IdleChat TTS speech/display text cleanup, topic-announcement formatting, event type classification, character ID normalization, voice profile mapping, and TTS session/payload plan construction; `cmd/rencrow` converts runtime `TimelineEvent` values into module inputs and applies the plan to the bridge/runtime state.
- `modules/tts` owns route-based TTS session metadata planning for message and distributed orchestrator lifecycles; `internal/application/orchestrator` converts route decisions into module inputs and applies the plan to the existing bridge contract.
- `modules/tts` owns TTS text chunking rules and streaming chunk pending/emitted state; `internal/application/orchestrator` asks the module chunker for chunks before applying them to TTS and VTuber bridges.
- `modules/tts` owns speech text filtering, emotion planning, emotion state/context types, and emotion prefix policy; the legacy app-layer wrapper has been retired.
- `modules/tts` owns TTS runtime provider priority, playback command normalization, voice selection, and Irodori provider option planning; `cmd/rencrow` converts app config into module DTOs and applies the returned plan to concrete provider construction.
- `modules/tts` owns Irodori defaults, voice/style resolution, synthesis endpoint URL construction, Gradio run-generation payload ordering, audio URL parsing, download URL resolution, and loopback file URL rewrite rules; `internal/infrastructure/tts` keeps HTTP execution and thin adapter code.
- `modules/tts` owns SBV2 voice alias resolution, voice URL construction, editor API URL construction, TTS punctuation, and audio prefix sanitization rules; `internal/infrastructure/tts` keeps HTTP execution and thin adapter code.
- `modules/tts` owns local audio path normalization, output-dir containment checks, and Viewer audio URL construction; `cmd/rencrow` keeps HTTP file serving and concrete runtime wiring.
- `modules/tts` owns RenCrow synthesis request payload construction, provider_params validation, emotion voice fallback, and request ID header construction; `internal/infrastructure/tts` keeps HTTP calls and thin adapter code while provider extraction continues.
- `modules/tts` owns RenCrow synthesis error parsing, retry decisions, transport retry classification, and retry backoff; `internal/infrastructure/tts` keeps HTTP calls and context-aware sleep while provider extraction continues.
- `modules/tts` owns the synthesis diagnostics policy; `cmd/rencrow` only exposes it over HTTP.
- `/viewer/modules/tts/diagnostics` exposes TTS provider health and synthesis contract metadata without synthesizing audio.
- `/viewer/modules/tts/playback-state` exposes IdleChat pending, topic gates, and public session routing state.
- `/viewer/modules/health` includes `tts.playback` separately from `tts`.
- `modules/stt.ViewerInputObserver` separates Viewer microphone/transcript injection state from transcription provider contracts.
- `modules/stt` owns Viewer-input report construction; `cmd/rencrow` only collects the runtime snapshot and encodes the report.
- `modules/stt` owns Viewer-input snapshot construction, including endpoint/path defaults, URL normalization, and configured flags.
- `modules/stt` owns runtime STT provider option planning and defaults; `cmd/rencrow` converts app config into module DTOs and applies the returned plan to concrete provider construction.
- `modules/stt` owns WebSocket handler selection, compatibility route paths, and text/binary frame classification rules; `cmd/rencrow` keeps concrete WebSocket handlers and proxy/provider execution.
- `modules/stt` owns WebSocket control-message parsing, PCM16/WAV payload normalization, silence detection, and adaptive timeout clamp rules; `cmd/rencrow` keeps env reads and provider/HTTP execution.
- `modules/stt` owns Viewer STT debug artifact defaults and archive filename construction; Viewer handlers still own HTTP decoding and file writes.
- `modules/stt` owns the transcription diagnostics policy; `cmd/rencrow` only exposes it over HTTP.
- `/viewer/modules/stt/viewer-input` exposes STT Viewer input endpoints, debug artifact paths, provider/gateway wiring, and transcript injection contract.
- `/viewer/modules/stt/diagnostics` exposes STT provider health and transcription contract metadata without transcribing audio.
- `/viewer/modules/health` includes `stt.viewer_input` separately from `stt`.
- Module route registration now has a single `registerModuleRoutes` entry point, and the manifest endpoints are checked against the registered module routes.
- Module route tests verify every registered `/viewer/modules/*` endpoint serves JSON with runtime contract dependencies and rejects wrong HTTP methods.

Validation:

- `internal/adapter/viewer/viewer_audio_button.test.mjs`
- `cmd/rencrow/idlechat_tts_*` tests
- STT admin/capture tests

Current validation:

- `go test ./cmd/rencrow ./internal/application/service ./internal/application/orchestrator ./internal/adapter/modulebridge ./internal/infrastructure/tts ./internal/infrastructure/stt ./internal/infrastructure/routing ./modules/...`
- `go test ./...`
- `cmd/rencrow` module manifest tests verify manifest endpoint coverage and route registration coverage.
- `cmd/rencrow` module route tests verify every registered module endpoint returns JSON when provided module-contract dependencies.

## Phase 5: Final Package Move Or Alias Stabilization

Goal: decide whether physical package moves are worth the churn.

Options:

1. Keep current package paths and enforce ownership by documentation/tests.
2. Move code into module-named packages with minimal adapter code where runtime wiring still needs it.

Decision gate:

- Choose option 2 only if import boundary tests and the remaining adapters keep behavior stable.

## Non-goals

- Do not move implementation during the design-completion phase.
- Do not create external repos for modules.
- Do not store module source under `.git/worktrees/*`.
- Do not change runtime behavior only to make package names look cleaner.
