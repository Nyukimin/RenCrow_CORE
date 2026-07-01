# Memory Feature

## Owner

Memory

## Inputs

conversation event, memory candidate, user memory action, recall request

## Outputs

memory record, recall pack, lifecycle status, prompt injection candidate

## Side Effects

memory store writes and lifecycle maintenance through existing runtime code

## Persistence

conversation/memory SQLite stores and memory ledger records

## Logs

memory_id, user_id, state, transition, status, error kind

## Error Contract

confirmed/pinned promotion requires explicit review boundary

## Current Main Files

internal/domain/memory, internal/domain/conversation, internal/infrastructure/persistence/memory, internal/adapter/viewer/memory_*.go

## Current Route Boundary

- `/viewer/memory/snapshot`
- `/viewer/memory/layers`
- `/viewer/memory/events`
- `/viewer/memory/state`
- `/viewer/memory/promote`
- `/viewer/memory/user`
- `/viewer/memory/user/state`
- `/viewer/memory/user/forget`
- `/viewer/memory/user/supersede`
- `/viewer/memory/recall-pack`
- `/viewer/recall/traces`

## Migration Boundary

This feature package is a registrar/facade entry point only. Existing implementation stays in the listed current files until contract tests and caller handoff are added for the relevant phase. The registrar owns route registration and dependency handoff only.
