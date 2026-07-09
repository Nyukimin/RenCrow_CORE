# Agent Feature

## Owner

Agent

## Inputs

agent IDs, role targets, persona files, runtime agent config

## Outputs

agent identity metadata, display names, capability decisions

## Side Effects

none in this scaffold; persona loading remains in existing runtime code

## Persistence

workspace/persona and existing memory stores where already used

## Logs

agent setup and runtime provider selection logs

## Error Contract

unknown agent ID and missing persona must be explicit setup errors

## Current Main Files

internal/domain/agent, workspace/persona, cmd/rencrow/runtime_agents.go

## Migration Boundary

This feature package is a registrar/facade entry point only. Existing implementation stays in the listed current files until contract tests and caller handoff are added for the relevant phase.
