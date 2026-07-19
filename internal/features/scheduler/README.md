# Scheduler Feature

## Owner

Scheduler

## Inputs

schedule record, due tick, manual run request

## Outputs

due job, run log, scheduler status

## Side Effects

scheduled job execution through existing scheduler service

## Persistence

scheduler persistence store and run log

## Logs

job_id, schedule_id, due_at, status, error kind

## Error Contract

invalid schedule and run failure remain explicit status values. Resource-gated jobs use
`deferred` with a concrete retry time instead of consuming the next normal schedule.

## Current Main Files

internal/application/scheduler, internal/domain/scheduler, internal/infrastructure/persistence/scheduler, internal/adapter/viewer/scheduler_handler.go

## Migration Boundary

This feature package is a registrar/facade entry point only. Existing implementation stays in the listed current files until contract tests and caller handoff are added for the relevant phase.
