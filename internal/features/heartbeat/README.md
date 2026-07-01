# Heartbeat Feature

## Owner

Heartbeat

## Inputs

schedule tick, heartbeat config, manual trigger

## Outputs

run log, draft report request, workstream/revenue trigger

## Side Effects

background job execution and report creation through existing heartbeat service

## Persistence

heartbeat run logs and related job records

## Logs

job_id, due_at, trigger, status, error kind

## Error Contract

background job failure is recorded and must not be hidden by Viewer display

## Current Main Files

internal/application/heartbeat, cmd/picoclaw/runtime_heartbeat.go, cmd/picoclaw/main_heartbeat_test.go

## Migration Boundary

This feature package is a registrar/facade entry point only. Existing implementation stays in the listed current files until contract tests and caller handoff are added for the relevant phase.
