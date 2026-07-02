# Llm Feature

## Owner

LLM

## Inputs

runtime config, role request, model capability, prompt payload

## Outputs

provider plan, diagnostics, health report, generation result through existing providers

## Side Effects

LLM HTTP calls and warmup through existing infrastructure

## Persistence

provider logs and existing conversation/model diagnostics where already written

## Logs

role, provider, model, status, latency, error kind

## Error Contract

missing credentials, disabled provider, and health failure remain explicit

## Current Main Files

modules/llm, internal/infrastructure/llm, cmd/picoclaw/runtime_llm_*.go

## Migration Boundary

This feature package owns the LLM route registrar boundary. Viewer LLM Ops route registration is handled here, while handler bodies and provider/runtime implementation stay in the listed current files until later handler-body migration phases.
