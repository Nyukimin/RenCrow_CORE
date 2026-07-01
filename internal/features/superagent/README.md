# Superagent Feature

## Owner

SuperAgent / AI Workflow

## Inputs

run queue item, subagent task, workflow action, steering context

## Outputs

run status, trace event, queue update, workflow result

## Side Effects

queue claims and processor execution through existing superagent service

## Persistence

superagent run queue and workstream-linked records

## Logs

queue_id, run_id, workstream_id, action, status, error kind

## Error Contract

queue processor unavailable and run failure remain explicit

## Current Main Files

internal/application/superagent, internal/adapter/viewer/superagent_handler.go, cmd/picoclaw/runtime_background_jobs.go

## Current Route Boundary

- `/viewer/superagent`
- `/viewer/superagent/runs`
- `/viewer/superagent/runs/pause`
- `/viewer/superagent/runs/resume`
- `/viewer/superagent/run-queue`
- `/viewer/superagent/run-queue/claim`
- `/viewer/superagent/run-queue/complete`
- `/viewer/superagent/subagent-tasks`
- `/viewer/superagent/context-packs`
- `/viewer/superagent/message-channels`
- `/viewer/superagent/trace-events`

## Migration Boundary

This feature package is a registrar/facade entry point only. Existing implementation stays in the listed current files until contract tests and caller handoff are added for the relevant phase. The registrar owns route registration and dependency handoff only.
