# Ops Feature

## Owner

Ops / Maintenance

## Inputs

ops CLI command, Viewer ops request, health/readiness probe, cleanup request

## Outputs

status, cleanup result, doctor report, visible-state error, export artifact

## Side Effects

cleanup, validation, export, and repair jobs through existing ops handlers

## Persistence

job logs, event logs, history repair artifacts, OTEL export artifacts

## Logs

job_id, operation, status, error kind

## Error Contract

maintenance failure must be visible and must not mask runtime feature health

## Current Main Files

cmd/picoclaw/health_*.go, cmd/picoclaw/cli_operations.go, internal/adapter/viewer/*_handler.go

## Migration Boundary

This feature package is a registrar/facade entry point only. Ops/job, repair, backlog, scheduler, workstream, and revenue route registration is owned by `internal/features/ops/registrar.go`; existing handler, store, and runtime implementations stay in the listed current files until contract tests and caller handoff are added for the relevant phase.
