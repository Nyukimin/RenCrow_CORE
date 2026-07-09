# Backlog Feature

## Owner

Backlog

## Inputs

intake item, runner request, Viewer backlog API request

## Outputs

backlog status, item list, runner result, visible error

## Side Effects

backlog store writes and runner execution through existing code

## Persistence

backlog domain/store records where currently configured

## Logs

job_id, item_id, runner, status, error kind

## Error Contract

runner unavailable and item failure remain visible state errors

## Current Main Files

internal/domain/backlog, internal/adapter/viewer/backlog_handler.go, cmd/rencrow/runtime_dependencies.go

## Migration Boundary

This feature package is a registrar/facade entry point only. Existing implementation stays in the listed current files until contract tests and caller handoff are added for the relevant phase.
