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

This feature package is a registrar/facade entry point only. Existing implementation stays in the listed current files until contract tests and caller handoff are added for the relevant phase.
