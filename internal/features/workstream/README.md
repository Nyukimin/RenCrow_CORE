# Workstream Feature

## Owner

Workstream

## Inputs

goal, artifact, steering update, heartbeat trigger

## Outputs

workstream status, artifact record, steering decision, vault update

## Side Effects

store writes and artifact/vault updates through existing workstream code

## Persistence

workstream persistence store and artifact records

## Logs

workstream_id, artifact_id, action, status, error kind

## Error Contract

heartbeat may trigger workstream but must not own workstream state

## Current Main Files

internal/domain/workstream, internal/infrastructure/persistence/workstream, internal/adapter/viewer/workstream_handler.go

## Migration Boundary

This feature package is a registrar/facade entry point only. Existing implementation stays in the listed current files until contract tests and caller handoff are added for the relevant phase.
