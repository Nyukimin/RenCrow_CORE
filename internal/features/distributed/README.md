# Distributed Feature

## Owner

Distributed

## Inputs

transport config, remote agent registration, delivery request

## Outputs

availability status, delivery result, transport diagnostics

## Side Effects

local/SSH transport calls through existing infrastructure

## Persistence

transport logs and existing runtime state

## Logs

agent_id, route, transport, status, error kind

## Error Contract

remote unavailable and delivery failure remain explicit

## Current Main Files

internal/domain/transport, internal/infrastructure/transport, cmd/picoclaw/runtime_distributed_mode.go, cmd/picoclaw-agent

## Current Route Boundary

Distributed owns transport and remote-agent semantics, but it has no direct HTTP route registrar in this step. Current distributed behavior remains in `cmd/picoclaw/runtime_distributed_mode.go` and `cmd/picoclaw-agent`.

## Migration Boundary

This feature package is a registrar/facade entry point only. Existing implementation stays in the listed current files until contract tests and caller handoff are added for the relevant phase.
