# Repair Feature

## Owner

Repair

## Inputs

repair reason, target route/agent, recent event window, manual run request

## Outputs

repair job event, job notification, repair run response

## Side Effects

autonomous repair job execution through existing runner

## Persistence

job/event logs and existing autonomous repair records

## Logs

job_id, target route, target agent, status, error kind

## Error Contract

runner unavailable and start failure are visible repair errors

## Current Main Files

internal/application/autonomous, internal/adapter/viewer/repair_handler.go, cmd/picoclaw/runtime_repair.go

## Migration Boundary

This feature package is a registrar/facade entry point only. Existing implementation stays in the listed current files until contract tests and caller handoff are added for the relevant phase.
