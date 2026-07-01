# Worker Feature

## Owner

Worker

## Inputs

Worker action, Coder proposal, patch, command request, execution context

## Outputs

execution result, failure classification, logs, artifacts

## Side Effects

file edits, command execution, tests, tool calls, and operational jobs in existing Worker services

## Persistence

workspace logs, job records, execution reports

## Logs

job_id, session_id, route, command, status, error kind

## Error Contract

denied, failed, unavailable, and retryable classifications remain explicit

## Current Main Files

modules/worker, internal/application/service/worker_execution_*, internal/infrastructure/tools

## Migration Boundary

This feature package is a registrar/facade entry point only. Existing implementation stays in the listed current files until contract tests and caller handoff are added for the relevant phase.
