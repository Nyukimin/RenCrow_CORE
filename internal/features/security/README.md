# Security Feature

## Owner

Security

## Inputs

channel envelope, tool request, promotion request, sandbox verification result

## Outputs

policy result, allow/deny decision, promotion gate result

## Side Effects

none in this scaffold; existing security checks stay in current domain/application code

## Persistence

existing security and gate records where configured

## Logs

policy, actor, target, decision, error kind

## Error Contract

denied and blocked decisions must remain explicit

## Current Main Files

internal/domain/security, internal/application/sandbox, internal/adapter/viewer/sandbox_handler.go

## Current Route Boundary

Security owns policy and gate semantics, but it has no direct Viewer route registrar in this step. Current security-facing Viewer routes are exposed through `internal/features/sandbox` and retain their existing handler bodies.

## Migration Boundary

This feature package is a registrar/facade entry point only. Existing implementation stays in the listed current files until contract tests and caller handoff are added for the relevant phase.
