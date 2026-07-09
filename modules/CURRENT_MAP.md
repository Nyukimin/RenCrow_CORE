# Current Code Ownership Map

This file maps the current repository layout to the target module design. It is an ownership map, not a command to move files immediately.

## Summary

Current module implementation status:

- `modules/core`, `modules/llm`, `modules/tts`, `modules/stt`, `modules/worker`, and `modules/chat` define stable contracts.
- `modules/core` owns module inventory descriptors and runtime module endpoint constants.
- `modules/core` owns generic module HTTP method validation and JSON response wiring helpers; `cmd/rencrow/module_*.go` keeps only endpoint wiring and request decoding.
- `modules/core` owns generic provider health normalization for nil providers, module names, and `checked_at`.
- `modules/chat`, `modules/llm`, `modules/tts`, `modules/stt`, and `modules/worker` own their module-specific adapter health report builders; `internal/adapter/modulebridge` only supplies current runtime availability/provider data.
- `modules/core` owns module health snapshot construction from health reports.
- `modules/core` owns runtime module health report ordering and aggregate snapshot construction across the current runtime module providers.
- `modules/llm`, `modules/tts`, `modules/stt`, and `modules/worker` own their diagnostics policy/tool metadata instead of keeping that metadata in `cmd/rencrow`.
- `modules/worker` owns external Coder policy construction so automatic CODE routing keeps external providers explicit-only.
- `modules/worker` owns Coder setup planning for slot name normalization, slot lookup, disabled slots, and shared LightMemory initialization defaults.
- `internal/adapter/modulebridge` adapts the current LLM/TTS/STT providers to the module contracts.
- `modules/llm` and `modules/stt` own request copy semantics for mutable provider-facing fields before compatibility adapters call current providers.
- `internal/adapter/modulebridge` builds the LLM module role provider map from the current domain providers.
- `internal/adapter/modulebridge` owns runtime TTS/STT module adapter constructors so `cmd/rencrow` does not know adapter output prefixes.
- `internal/adapter/modulebridge` owns runtime Chat service and Worker executor module constructors.
- `modules/llm` owns standard runtime role names, role provider map construction, and module generate response construction.
- `modules/tts` owns module synthesis result construction from provider audio output.
- `modules/tts` owns provider emotion reason metadata construction; `internal/adapter/modulebridge` only adapts it into the current provider DTO.
- `modules/tts` owns runtime TTS provider priority, provider-plan enumeration/first-selection, provider selection log policy, playback command normalization, voice selection, and Irodori provider option planning; `cmd/rencrow` keeps concrete `internal/infrastructure/tts` provider construction.
- `modules/tts` owns Irodori defaults, voice/style resolution, synthesis endpoint URL construction, simple synthesis payload construction, Gradio run-generation/uploaded-audio payload construction, audio URL parsing, download URL resolution, and loopback file URL rewrite rules; current infrastructure providers keep HTTP execution and thin adapter code.
- `modules/tts` owns SBV2 voice alias resolution, voice URL construction, editor API URL construction, editor request payload construction, TTS punctuation, and audio prefix sanitization rules; current infrastructure providers keep HTTP execution and thin adapter code.
- `modules/tts` owns local audio relative-path and Viewer audio URL rules; `cmd/rencrow` keeps HTTP file serving and provider/runtime wiring.
- `modules/tts` owns RenCrow synthesis payload construction, provider parameter validation, voice fallback, and request ID header rules; current infrastructure bridges keep HTTP execution and thin adapter code.
- `modules/tts` owns RenCrow bridge defaults, session start normalization, speech text validation, and synthesis audio-output validation; current infrastructure bridges keep HTTP execution, session map storage, and sink callbacks.
- `modules/tts` owns RenCrow synthesis error parsing, retry policy, transport retry classification, and retry backoff rules; current infrastructure bridges keep HTTP execution and context-aware sleep.
- `modules/stt` owns transcription result construction, segment duration normalization, and STT provider health report normalization.
- `modules/stt` owns runtime STT provider option planning and defaults for provider, model, language, timeout, busy policy, timeout error classification, WebSocket session event payloads, and external HTTP URL; `cmd/rencrow` keeps concrete `internal/infrastructure/stt` provider construction and HTTP execution.
- `modules/stt` owns busy policy normalization and execution-mode planning; `internal/infrastructure/stt` keeps channel/goroutine execution.
- `modules/stt` owns HTTP transcription result normalization, error-status mapping, and ChatInput envelope construction; current infrastructure handlers keep multipart decoding and JSON response wiring.
- `modules/llm` owns the generic health-checked provider wrapper; `cmd/rencrow` only adapts local-openai health checks into that wrapper.
- `modules/llm` owns external health-check status normalization, local LLM health-check enablement policy, and role-name normalization.
- `modules/llm` owns role-status collection for runtime LLM diagnostics.
- `modules/llm` owns role-qualified health report collection for runtime LLM providers.
- `modules/llm` owns primary Chat/Worker/Heavy/Wild provider plan construction, including local-vs-legacy mode selection, legacy Ollama worker-model fallback, local/legacy Ollama `num_ctx`, and local warmup timeout selection.
- `modules/llm` owns conversation summary and embedding provider plan construction; `cmd/rencrow` keeps concrete provider/embedder construction and API key handling.
- `modules/llm` owns Coder provider validation/planning for provider kind, required credentials, required base URLs, and local OpenAI timeout; `internal/infrastructure/llm/factory` keeps concrete provider construction.
- `modules/llm` owns OpenAI-compatible ThinkingBridge request flags, provider-option filtering, and leaked-reasoning cleanup policy; current OpenAI provider keeps HTTP execution and compatibility wrappers.
- `modules/llm`, `modules/tts`, `modules/stt`, and `modules/worker` own diagnostics snapshot construction and diagnostics unavailable messages.
- `modules/core` owns manifest snapshot construction.
- `modules/chat` owns route-decision report construction and route-service unavailable message.
- `modules/chat` owns runtime route-name normalization, route-decision reason fallback, and Viewer input default normalization.
- `modules/chat` owns IdleChat forecast provider plan construction, Coder label lookup, explicit external-provider gating, provider labels, and speaker LLM option normalization; `cmd/rencrow` keeps concrete provider construction and Worker-local runtime fallback wiring.
- `modules/chat` owns IdleChat topic category contracts, daily seed DTOs, topic seed/candidate/judge DTOs, deterministic topic validation, seed selection/labels, prompt text construction, JSON parsing, judge thresholds, and recent-topic similarity checks; `internal/application/idlechat` now keeps only the remaining orchestration and source-fetching glue around the module contract.
- `modules/tts` owns playback-state health/report construction.
- `modules/stt` owns Viewer-input report construction.
- `internal/adapter/modulebridge` also adapts `WorkerExecutionService` to the `modules/worker` executor contract.
- `internal/adapter/modulebridge` also adapts the existing orchestrator processor to the `modules/chat` service contract.
- `cmd/rencrow` holds module contract services/providers/executors in the composition root and exposes `/viewer/modules/manifest`, `/viewer/modules/health`, and `/viewer/modules/chat/route`.
- `cmd/rencrow` exposes LLM module role/provider diagnostics through `/viewer/modules/llm/diagnostics` without executing generation.
- `cmd/rencrow` exposes Worker module execution contract diagnostics through `/viewer/modules/worker/diagnostics`.
- `cmd/rencrow` exposes TTS provider diagnostics through `/viewer/modules/tts/diagnostics` without synthesizing audio.
- `cmd/rencrow` exposes STT provider diagnostics through `/viewer/modules/stt/diagnostics` without transcribing audio.
- `/viewer/modules/health` returns both aggregate `status`/`ready` from `modules/core.BuildRuntimeHealthSnapshot` and per-module reports.
- `cmd/rencrow` now delegates feature HTTP route registration to `internal/features/*/registrar.go` for Viewer base, IdleChat, Ops, Voice/STT/TTS, Web/browser, Knowledge/Memory/Source, Reports/Governance/Sandbox/SuperAgent/AIWorkflow, and Channels. Handler bodies and provider/runtime implementations remain in the current legacy-body files.
- `modules/tts` separates synthesis provider contracts from `PlaybackStateObserver`.
- `modules/tts` owns pending playback snapshot construction, including deterministic ID ordering and copy semantics.
- `modules/tts` owns the pending playback store, including wait channels, response lookup, topic gates, and completion/clear action contracts.
- `modules/tts` owns playback-state snapshot composition, health classification, and endpoint messages from pending state and public TTS session routing state.
- `modules/tts` owns public playback snapshot construction, fixed-width response number parsing/formatting, and IdleChat public session prefix classification.
- `modules/tts` owns public session route values and route-level stale/timeout/message resolution rules.
- `modules/tts` owns public chunk and response ID resolution DTOs.
- `modules/tts` owns public session route store state, including stale generation tracking and chunk/response sequence counters.
- `modules/tts` owns TTS bridge audio chunk/session-completed event payload contracts and Viewer/IdleChat playback event routing values; `cmd/rencrow` keeps event emission and concrete bridge wiring.
- `modules/tts` owns shared non-empty selection and trimmed equality helpers used by TTS provider policies.
- `modules/tts` owns Viewer active audio/input owner state and playback ACK normalization rules while `cmd/rencrow` keeps HTTP decoding and pending consumption application.
- `cmd/rencrow` exposes Viewer playback/pending state through `/viewer/modules/tts/playback-state`.
- `modules/stt` separates transcription provider contracts from `ViewerInputObserver`.
- `modules/stt` owns Viewer-input snapshot construction, including endpoint/path defaults, URL normalization, and configured flags.
- `modules/stt` owns Viewer STT debug artifact defaults and archive filename construction.
- `cmd/rencrow` exposes Viewer microphone/transcript injection state through `/viewer/modules/stt/viewer-input`.
- `cmd/rencrow` registers module endpoints through `registerModuleRoutes`, and tests verify manifest endpoints match registered module routes.
- `cmd/rencrow` module route tests verify every registered `/viewer/modules/*` endpoint returns JSON with module-contract dependencies and rejects wrong HTTP methods.
- `modules/dependency_rules_test.go` enforces module import direction and key implementation boundary rules.
- `modules/dependency_rules_test.go` prevents `internal/adapter/modulebridge` from owning module health report literals; health report policy belongs under `modules/*`.
- `modules/dependency_rules_test.go` prevents `internal/adapter/modulebridge` from owning Worker result literals or TTS emotion provider reason keys; those adapter policies belong under `modules/*`.
- `modules/dependency_rules_test.go` ensures `internal/adapter/modulebridge` uses module-owned request copy helpers before handing mutable LLM/STT request data to current providers.
- `modules/dependency_rules_test.go` prevents `cmd/rencrow` from owning TTS provider priority enumeration; runtime provider plan enumeration belongs under `modules/tts`.
- `modules/dependency_rules_test.go` prevents `cmd/rencrow` from owning TTS provider selection log policy; that policy belongs under `modules/tts`.
- `modules/dependency_rules_test.go` prevents `cmd/rencrow` from owning IdleChat forecast provider gating/enumeration policy; forecast provider planning belongs under `modules/chat`.
- `modules/dependency_rules_test.go` prevents `cmd/rencrow` from owning LLM Ollama `num_ctx` constants; those runtime values belong under `modules/llm`.
- `modules/dependency_rules_test.go` prevents `cmd/rencrow` from owning LLM local health-check enablement and role-name normalization policy; that policy belongs under `modules/llm`.
- `modules/dependency_rules_test.go` prevents `cmd/rencrow` from owning external Coder policy map construction; that policy belongs under `modules/worker`.
- `modules/dependency_rules_test.go` prevents `cmd/rencrow` from owning Coder setup planning and LightMemory default policy; that policy belongs under `modules/worker`.
- `modules/dependency_rules_test.go` also prevents `cmd/rencrow/module_*.go` from owning `module*Request`/`module*Response` contracts or direct JSON response wiring; request/response/report/snapshot contracts and generic module HTTP response helpers belong under `modules/*`.
- Existing runtime wiring still has provider/composition code under `internal/...`, but TTS speech filtering, emotion planning, prefix policy, playback/session state, route planning, chunking, and runtime provider option planning are now owned by `modules/tts`.
- Provider extraction is therefore started, not complete.

| Current path | Target module | Notes |
| --- | --- | --- |
| `internal/domain/llm` | llm/core | Provider interfaces belong to llm; shared identity contracts may move to core. |
| `internal/infrastructure/llm` | llm | Provider factory, middleware, raw logs, budget middleware. |
| `cmd/rencrow/llm_*` | llm + composition root | Provider creation should be llm-owned; process wiring stays in `cmd/rencrow`. |
| `internal/infrastructure/tts` | tts | Synthesis providers, bridges, chunk planning, audio path/url. |
| `cmd/rencrow/tts_*`, `cmd/rencrow/idlechat_tts_*` | tts + chat/core integration | Provider options are tts; playback ACK/pending lifecycle is chat/core integration. |
| `internal/infrastructure/stt` | stt | STT provider contracts and HTTP client/server integration. |
| `internal/adapter/modulebridge` | composition/adapter | Temporary bridge from existing provider contracts to `modules/*` contracts during migration. |
| `cmd/rencrow/stt_*` | stt + composition root | Provider wiring and HTTP/WebSocket runtime setup. |
| `cmd/rencrow/module_health.go` | composition/core | Runtime module health view built from module contracts and core aggregate health rules. |
| `cmd/rencrow/module_llm_diagnostics.go` | composition/llm | Runtime LLM role diagnostics built from module provider contracts without calling Generate. |
| `modules/core/manifest.go` | core | Module inventory descriptors, runtime module endpoint constants, and state ownership metadata. |
| `cmd/rencrow/module_manifest.go` | composition/core | Runtime module manifest HTTP view built from core module descriptors. |
| `cmd/rencrow/module_worker_diagnostics.go` | composition/worker | Runtime Worker module diagnostics built from the Worker executor contract without executing actions. |
| `cmd/rencrow/module_chat_route.go` | composition/chat | Runtime Chat route decision view built from `modules/chat.Service`. |
| `cmd/rencrow/module_tts_diagnostics.go` | composition/tts | Runtime TTS provider diagnostics built from the TTS provider contract without calling Synthesize. |
| `cmd/rencrow/module_tts_playback_state.go` | composition/tts/core | Runtime playback/pending state view built from `modules/tts.PlaybackStateObserver`. |
| `cmd/rencrow/module_stt_diagnostics.go` | composition/stt | Runtime STT provider diagnostics built from the STT provider contract without calling Transcribe. |
| `cmd/rencrow/module_stt_viewer_input.go` | composition/stt/core | Runtime Viewer STT input view built from `modules/stt.ViewerInputObserver`. |
| `internal/application/orchestrator` | chat/worker/core/llm/tts | Split by responsibility; currently mixed orchestration. |
| `internal/adapter/modulebridge/chat.go` | composition/adapter | Temporary bridge from existing orchestrator processor to `modules/chat`. |
| `internal/application/idlechat` | chat/llm/tts/core | Dialogue flow is chat; topic generation uses llm; TTS wait/pending crosses chat/core/tts. |
| `internal/application/service/worker_execution_*` | worker | Worker execution side effects. |
| `internal/adapter/modulebridge/worker.go` | composition/adapter | Temporary bridge from existing Worker execution service to `modules/worker`. |
| `internal/infrastructure/tools` | worker | Tool runners and execution adapters. |
| `internal/domain/task`, `internal/domain/execution` | worker/core | Job/task value objects and execution reports. |
| `internal/adapter/viewer` | chat/core/stt/tts | UI adapter; ownership depends on endpoint/event. |
| `internal/adapter/config` | core/composition | Config schema spans modules; module-specific subtrees should be owned by modules. |
| `cmd/rencrow/runtime_*` | composition root | Wiring only; move policy out over time. |

## Detailed Ownership Notes

### Chat

Current primary owners:

- `internal/application/orchestrator/message_orchestrator_*`
- `internal/application/idlechat/orchestrator_*`
- `internal/application/idlechat/dialogue_*`
- `internal/application/idlechat/topic_*`
- Viewer display endpoints under `internal/adapter/viewer`

Split rule:

- user-facing text and route decisions are chat
- LLM call construction belongs to llm-facing adapter seams
- TTS playback state/pending must not become provider-owned state
- Current module bridge exposes the existing orchestrator as `modules/chat.Service`.
- `modules/chat` owns the `RoutePolicy` contract.
- `DecideRoute` uses the existing Mio route decision policy through a `modulebridge` compatibility adapter, while module route normalization, route-decision reason fallback, and Viewer input defaults live in `modules/chat`.
- `modules/chat` owns IdleChat forecast provider policy, including Coder candidate planning, Coder label lookup, explicit external-provider enablement, provider/model labels, and speaker LLM option normalization.
- `modules/chat` owns IdleChat topic category policy, daily seed DTOs, topic seed/candidate/judge DTOs, deterministic category validation, RSS seed item parsing, seed selection/labels, prompt text construction, JSON parsing, judge score thresholds, and recent-topic similarity checks. `internal/application/idlechat` still owns concrete network source fetching, cache mutexes, prompt file loading, LLM calls, and session flow.
- Runtime health includes the Chat service as `chat`.

### Worker

Current primary owners:

- `internal/application/service/worker_execution_*`
- `internal/infrastructure/tools`
- `internal/application/execution`
- execution-related domain types

Split rule:

- Worker can call LLM for planning/coding support only through llm contracts
- Worker must not own TTS/STT or Viewer display state
- Current module bridge exposes proposal patch execution as `modules/worker.ToolProposalPatch`.
- `modules/worker` owns local/distributed worker-agent availability, unavailable reason formatting, and local coder reply target rules; `cmd/rencrow` keeps concrete transport maps and goroutine loops.
- `modules/worker` owns Coder capability plan construction from detected LLM capabilities, configured coder slots, and quality overrides; `cmd/rencrow` converts app/domain DTOs.
- `modules/worker` owns external Coder policy construction from configured coder slots so external providers require explicit CODE1/CODE2/CODE3/CODE4 routing.
- `modules/worker` owns Coder setup planning from configured coder slots, including slot name/index policy, disabled-slot entries, and shared LightMemory default max-turn handling; `cmd/rencrow` keeps concrete provider construction and persona loading.
- `modules/worker` owns autonomous execution route classification, capability selection, execution step labels, failure classification, retry prompt construction, and route/contract attempt verification policy; `internal/application/orchestrator` still owns conversion from internal contract/result types and executor invocation.
- `modules/worker` owns Worker action tool validation, proposal patch argument extraction, failed result construction, unavailable executor health/result policy, and patch execution result mapping.
- `modules/worker` owns Worker proposal execution failure classification and retryability policy; `internal/application/service` still owns concrete execution and result mutation.
- Runtime health includes the Worker executor as `worker`.
- Runtime diagnostics expose supported Worker module tools without executing them.

### TTS Playback State

Current primary owners:

- `cmd/rencrow/tts_playback_ack.go`
- `cmd/rencrow/idlechat_tts_pending.go`
- `cmd/rencrow/tts_public_session.go`

Split rule:

- TTS provider owns synthesis and audio paths.
- Viewer playback ACK, active audio/input ownership, IdleChat pending, topic gates, and public session routing are playback/session state, not provider state.
- `modules/tts.PlaybackStateObserver` is the current contract for observing that state without moving provider code.
- Pending snapshot DTOs, deterministic ordering, mutable pending maps, wait channel closing, response lookup, topic gates, and pending completion/clear actions live in `modules/tts.PendingPlaybackStore`.
- `cmd/rencrow` keeps the HTTP adapter for existing call sites and applies public-session cleanup returned by the store.
- Public session route map, stale generation tracking, and sequence counters now live in `modules/tts.PublicSessionStore`; `cmd/rencrow` keeps the HTTP adapter for existing call sites.
- Timeout consumption matching for public session routes now returns `modules/tts.PlaybackTimeoutConsumption`; `cmd/rencrow` applies the matched internal session IDs to pending cleanup.
- Viewer active audio/input owner state and active-audio owner matching now live in `modules/tts.ViewerActiveControlStore`; `cmd/rencrow` keeps the active-control HTTP endpoint and TTS playback ACK handler.
- Deprecated playback ACK normalization, pending-consumption eligibility, and ACK receipt construction are module contract rules; pending cleanup application remains in `cmd/rencrow`.
- IdleChat TTS speech/display text cleanup, topic-announcement formatting, event type classification, character ID normalization, voice profile mapping, and TTS session/payload plan construction now live in `modules/tts`; `cmd/rencrow` converts runtime `TimelineEvent` values into module inputs and applies the plan to the bridge/runtime state.
- Route-based TTS session metadata planning for message and distributed orchestrator lifecycles now lives in `modules/tts`; `internal/application/orchestrator` converts route decisions into module inputs and applies the plan to the existing bridge contract.
- TTS bridge audio chunk/session-completed event payload construction and Viewer/IdleChat playback event routing now live in `modules/tts`; `cmd/rencrow/tts_client_bridge.go` emits orchestrator events with module-built payloads.
- Shared non-empty selection and trimmed equality helpers used by TTS provider policies now live in `modules/tts`; `internal/infrastructure/tts` keeps provider HTTP execution.
- TTS text chunking rules and streaming chunk pending/emitted state now live in `modules/tts`; orchestrator stream forwarders ask the module chunker for chunks before applying them to TTS and VTuber bridges.
- TTS runtime provider priority, provider-plan enumeration/first-selection, playback command normalization, voice selection, and Irodori provider option planning now live in `modules/tts`; `cmd/rencrow` converts runtime config into module DTOs and applies the selected plan to concrete provider construction.
- Irodori defaults, voice/style resolution, synthesis endpoint URL construction, simple synthesis payload construction, Gradio run-generation/uploaded-audio payload construction, audio URL parsing, download URL resolution, and loopback file URL rewrite rules now live in `modules/tts`; `internal/infrastructure/tts` keeps HTTP execution and thin adapter code.
- SBV2 voice alias resolution, voice URL construction, editor API URL construction, editor request payload construction, TTS punctuation, and audio prefix sanitization rules now live in `modules/tts`; `internal/infrastructure/tts` keeps HTTP execution and thin adapter code.
- Local audio path normalization, output-dir containment checks, and Viewer audio URL construction now live in `modules/tts`; `cmd/rencrow` and current infrastructure bridges apply those rules when serving or emitting chunks.
- RenCrow synthesis request payload construction, provider_params validation, emotion voice fallback, and request ID header construction now live in `modules/tts`; `internal/infrastructure/tts` keeps HTTP calls and thin adapter code while provider extraction continues.
- RenCrow bridge defaulting, session start normalization, speech text validation, and synthesis audio-output validation now live in `modules/tts`; `internal/infrastructure/tts` keeps HTTP calls, session map storage, and sink callbacks while provider extraction continues.
- RenCrow synthesis error parsing, retry decisions, transport retry classification, and retry backoff now live in `modules/tts`; `internal/infrastructure/tts` keeps HTTP calls and context-aware sleep while provider extraction continues.

### Core

Current primary owners:

- shared domain value objects
- event contracts in `internal/application/orchestrator/event.go`
- config and lifecycle policies that are not module-specific
- state ownership rules documented in `rules/common/rules_state_management.md`

Split rule:

- Core may define contracts but not concrete providers
- Core must not import module-specific implementations

### LLM

Current primary owners:

- `internal/domain/llm`
- `internal/infrastructure/llm`
- `cmd/rencrow/llm_runtime_factory.go`
- `cmd/rencrow/llm_runtime_warmup.go`
- `cmd/rencrow/llm_conversation_runtime.go`
- `cmd/rencrow/runtime_llm_providers.go`

Split rule:

- LLM returns text/structured outputs
- LLM must not synthesize audio or transcribe audio
- Runtime diagnostics expose configured LLM roles and health without executing generation.
- `modules/llm` owns local LLM role/alias resolution for provider kind, base URL, model, timeout, and concurrency; `cmd/rencrow` converts app config to module input and constructs concrete providers.
- `modules/llm` owns primary provider planning for Chat/Worker/Heavy/Wild, including Ollama `num_ctx`; `cmd/rencrow` converts the plan into Ollama/OpenAI providers and keeps API keys, middleware, warmup goroutines, and process wiring.
- `modules/llm` owns conversation summary and embedding provider planning; `cmd/rencrow` converts those plans into concrete conversation providers.
- `modules/llm` owns Coder provider validation/planning; `internal/infrastructure/llm/factory` converts validated plans into concrete provider instances.
- `modules/llm` owns OpenAI-compatible ThinkingBridge request/response cleanup policy; `internal/infrastructure/llm/providers/openai` applies those rules around concrete HTTP calls.

### TTS

Current primary owners:

- `modules/tts`
- `internal/infrastructure/tts`
- `cmd/rencrow/tts_runtime_*`
- `cmd/rencrow/tts_client_bridge.go`
- `cmd/rencrow/tts_browser_audio.go`

Cross-boundary owners:

- `cmd/rencrow/tts_playback_ack.go`
- `cmd/rencrow/idlechat_tts_pending.go`
- `cmd/rencrow/idlechat_tts_queue.go`
- `cmd/rencrow/idlechat_tts_timeout.go`

These cross-boundary files should ultimately be split so provider-specific TTS stays in tts and session/pending state stays in chat/core.
Runtime diagnostics expose the TTS provider contract separately from `tts.playback` state.

### STT

Current primary owners:

- `internal/infrastructure/stt`
- `cmd/rencrow/stt_runtime_*`
- `internal/adapter/viewer/stt_*`

Split rule:

- STT owns transcription provider health and result normalization
- Viewer microphone state and final transcript injection stay outside STT provider code
- `modules/stt` owns runtime URL inference for provider/base/gateway/WebSocket endpoints; `cmd/rencrow` converts app config and environment variables into module inputs.
- `modules/stt` owns runtime STT provider option planning and defaults; `cmd/rencrow` converts app config into module DTOs and applies the returned plan to concrete provider construction.
- `modules/stt` owns busy policy normalization and execution-mode planning; `internal/infrastructure/stt` applies the plan to concrete channel/goroutine behavior.
- `modules/stt` owns WebSocket handler selection, compatibility route paths, and text/binary frame classification rules; `cmd/rencrow` keeps concrete WebSocket handlers and proxy/provider execution.
- `modules/stt` owns WebSocket control-message parsing, PCM16/WAV payload normalization, silence detection, draft/final session rules, draft state update/reset rules, session event payloads, timeout error classification, and adaptive timeout/cooldown state rules; `cmd/rencrow` keeps env reads, WebSocket I/O, and provider/HTTP execution.
- `modules/stt` owns HTTP transcription result normalization, error-status mapping, and ChatInput envelope construction; `internal/infrastructure/stt` keeps multipart file reads, provider invocation, and HTTP response writing.
- `modules/stt.ViewerInputObserver`, `BuildViewerInputHealthReport`, and Viewer input endpoint messages are the current contracts for observing Viewer STT input and transcript injection endpoints without making them provider state.
- Viewer STT debug artifact default paths and archive filename construction live in `modules/stt`; the Viewer adapter owns request handling and writes.
- Runtime diagnostics expose the STT provider contract separately from `stt.viewer_input` state.

## Known Mixed Areas

These areas are intentionally not moved yet because they mix runtime wiring, state ownership, and provider contracts:

- `cmd/rencrow/idlechat_tts_*`
- `cmd/rencrow/tts_playback_ack.go`
- `internal/application/orchestrator/*tts*`
- `internal/adapter/viewer/viewer_audio_button.test.mjs`
- `internal/adapter/viewer/stt_capture_handler.go`

Before moving them, add focused tests proving display, playback ACK, pending drain, and transcript injection behavior.

## Boundary Tests

Current automated boundary checks live in `modules/dependency_rules_test.go`.

They verify:

- `modules/*` contract packages only import allowed module dependencies.
- `modules/*` contract packages do not import `internal/*` or `cmd/*`.
- `internal/adapter/modulebridge` does not import the `cmd/rencrow` composition root.
- TTS and STT infrastructure do not import each other or unrelated runtime modules.
- Worker service code does not import TTS/STT provider implementations, Viewer implementation, or TTS/STT modules.
- `cmd/rencrow/module_*.go` does not define `module*Response` contracts; module-facing HTTP response contracts stay under `modules/*`.
