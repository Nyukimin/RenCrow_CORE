# Aiworkflow Feature

## Owner

SuperAgent / AI Workflow

## Inputs

workflow request, action, trace query, Viewer operation request

## Outputs

workflow status, action result, trace event

## Side Effects

workflow execution through existing viewer/runtime handlers

## Persistence

existing workflow/job logs where configured

## Logs

workflow_id, action, status, error kind

## Error Contract

workflow failure must be visible and must not be shown as stale success

## Current Main Files

internal/adapter/viewer/ai_workflow_handler.go, internal/application/superagent

## Current Route Boundary

- `/viewer/ai-workflow`
- `/viewer/ai-workflow/events`
- `/viewer/ai-workflow/project-memory`
- `/viewer/ai-workflow/worktrees`
- `/viewer/ai-workflow/commands`
- `/viewer/ai-workflow/commands/run`
- `/viewer/ai-workflow/context-usages`
- `/viewer/ai-workflow/context-budget/check`
- `/viewer/ai-workflow/external-control/check`
- `/viewer/ai-workflow/heavy-worker/evaluate`
- `/viewer/ai-workflow/heavy-worker/runtime-diagnostics`
- `/viewer/ai-workflow/project-init`
- `/viewer/ai-workflow/worktrees/create`
- `/viewer/ai-workflow/worktrees/close`

## Migration Boundary

This feature package is a registrar/facade entry point only. Existing implementation stays in the listed current files until contract tests and caller handoff are added for the relevant phase. The registrar owns route registration and dependency handoff only.
