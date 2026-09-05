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

internal/application/superagent, internal/adapter/viewer/superagent_handler.go, cmd/rencrow/runtime_background_jobs.go

## Current Route Boundary

- `/viewer/superagent`
- `/viewer/superagent/runs/pause`
- `/viewer/superagent/runs/resume`
- `/viewer/superagent/message-channels`
