# Revenue Feature

## Owner

Revenue

## Inputs

daily routine trigger, source data, human decision

## Outputs

draft, routine status, decision gate result

## Side Effects

draft/store writes through existing revenue service

## Persistence

revenue persistence store

## Logs

routine_id, draft_id, decision, status, error kind

## Error Contract

human gate denial and routine failure remain explicit

## Current Main Files

internal/application/revenue, internal/domain/revenue, internal/infrastructure/persistence/revenue, internal/adapter/viewer/revenue_handler.go

## Migration Boundary

This feature package is a registrar/facade entry point only. Existing implementation stays in the listed current files until contract tests and caller handoff are added for the relevant phase.
