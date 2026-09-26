# LLM Ops Feature

## Owner

Ops / Maintenance (LLM Ops / Hardware Capability, llmfit observation)

## Inputs

Viewer LLM Ops request (node overview, hardware profile, model fit listing, model fit matrix), manual refresh request

## Outputs

node hardware profile, model fit assessment (fit level, 4 scores, runtime, quant, native/usable/evaluated context, estimated vs measured TPS, estimate confidence), per-node status (online / offline / stale / disabled), refresh result

## Side Effects

read-only observation of llmfit (HTTP `GET /health`, `/api/v1/system`, `/api/v1/models/top`, `/api/v1/models`; or `llmfit system --json` / `llmfit recommend --json`). Refresh only re-reads llmfit; it never changes llmfit settings, loads models, or runs benchmarks

## Persistence

in-memory TTL cache only (system 5m, models 15m, health 30s by default; configured in `llm_capability.llmfit`). No files or databases

## Logs

node_id, operation, error kind, skipped partial entries, CLI stderr excerpt (never forwarded to the viewer verbatim)

## Error Contract

one node's failure never affects another node or any other CORE feature. A failing node reports `offline` (no data) or `stale` (last value + `collected_at`) with an error reason. Invalid query -> 400, unknown node -> 404, wrong method -> 405, service not wired -> 503. Responses are RenCrow domain projections, never raw llmfit JSON

## Current Main Files

internal/domain/llmops/*.go, internal/infrastructure/llmfit/*.go, internal/application/llmops/*.go, internal/adapter/viewer/llmops_handler.go, cmd/rencrow/runtime_llmops.go

## Migration Boundary

This feature package is a registrar/facade entry point only. LLM Ops viewer route registration is owned by `internal/features/llmops/registrar.go`; provider, service, and handler implementations stay in the listed current files.
