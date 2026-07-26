# Feature Registrars

This directory owns feature route registration and dependency handoff. Handler bodies move here when a concrete change benefits from feature-level change isolation; compatibility shims may remain in their former adapter package.

## Ver0.80 Registrar Status

Product HTTP route registration has been handed off from `cmd/rencrow/routes.go` to feature registrars, including Chat send, Core health/module routes, Viewer base, Avatar Character Runtime, IdleChat, Ops, Voice/STT/TTS, Web/browser, Knowledge/Memory/Source, Reports/Governance/Sandbox/SuperAgent/AIWorkflow, and Channels. Process-local pprof registration remains in the composition root.

Character Runtime is the first handler body moved behind its feature owner while retaining a Viewer compatibility shim. Other stable handler bodies, providers, stores, runtime background jobs, and CLI implementations stay in their current packages until a concrete change-isolation or reuse benefit justifies moving them. `security` and `distributed` record ownership boundaries but do not currently own direct HTTP routes.

## Inventory

- `agent`: Agent identity, role, capability, display name, and persona-facing contracts.
- `aiworkflow`: AI workflow operation APIs and trace-facing workflow state boundary.
- `avatar`: Emotion, lipsync trigger, and character runtime display boundary.
- `backlog`: Backlog item intake, runner status, and Viewer operation API boundary.
- `channels`: Inbound envelope, channel policy, response adapter, and external channel boundary.
- `chat`: User dialogue, route decisions, final response, and Viewer send contract.
- `core`: Process-level manifest, health, topology handoff, and shared lifecycle boundaries.
- `distributed`: Transport, remote agent availability, and delivery boundary.
- `governance`: Skill governance, trigger logs, change gates, and external PR audit boundary.
- `heartbeat`: Heartbeat due run, workstream trigger, and draft report launch boundary.
- `idlechat`: IdleChat session lifecycle, topic generation, speakers, stop, and TTS trigger boundary.
- `knowledge`: Knowledge import, wiki index, vocabulary, glossary, and review-ready knowledge artifacts.
- `llm`: Role provider selection, health, diagnostics, and runtime provider planning.
- `memory`: Observed, candidate, validated, promoted, recall, and prompt injection state boundary.
- `ops`: Health, doctor, cleanup, package validation, history repair, and OTEL export boundary.
- `repair`: Out-of-band repair request, repair job event, and Viewer repair operation boundary.
- `reports`: Evidence item, verification summary, report status, and evidence CLI boundary.
- `revenue`: Daily routine, draft, and policy decision boundary.
- `sandbox`: Post-apply verification, promotion gate, and rollback-facing operation boundary.
- `scheduler`: In-app due jobs, run log, and status boundary.
- `security`: Security policy result, channel policy, promotion gates, and rollback guard boundary.
- `source`: Source Registry entries, source fetcher, staging validation, and review boundary.
- `stt`: Transcription contracts, Viewer input observation, WebSocket plan, and provider readiness.
- `superagent`: SuperAgent run queue, subagent task, and trace event boundary.
- `tts`: Synthesis contracts, provider planning, playback ACK boundary, and audio chunking rules.
- `viewer`: Viewer shell, SSE events, tab APIs, and visible-state errors.
- `voice`: VoiceChat, VDS bridge, AudioRouter, and voice input/output route grouping.
- `web`: BrowserActor, WebGather, Webwright, and BrowserTrace entry grouping.
- `worker`: Execution results, proposal application, command/tool execution, and failure classification.
- `workstream`: Goal, artifact, steering, and vault update ownership boundary.
