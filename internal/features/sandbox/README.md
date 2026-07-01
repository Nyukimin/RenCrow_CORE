# Sandbox Feature

## Owner

Sandbox

## Inputs

applied change, verification request, promotion candidate

## Outputs

verification result, promotion gate decision, rollback recommendation

## Side Effects

verification commands and artifact writes through existing sandbox service

## Persistence

verification report artifacts and existing sandbox records

## Logs

job_id, artifact_id, gate, status, error kind

## Error Contract

failed verification must block promotion until reviewed

## Current Main Files

internal/application/sandbox, internal/adapter/viewer/sandbox_handler.go

## Current Route Boundary

- `/viewer/sandbox`
- `/viewer/sandbox/promotions`
- `/viewer/sandbox/promotions/apply`
- `/viewer/sandbox/promotions/rollback`
- `/viewer/sandbox/promotions/preview`
- `/viewer/sandbox/promotions/manual-review`
- `/viewer/sandbox/worktrees/create`
- `/viewer/sandbox/worktrees/close`

## Migration Boundary

This feature package is a registrar/facade entry point only. Existing implementation stays in the listed current files until contract tests and caller handoff are added for the relevant phase. The registrar owns route registration and dependency handoff only.
