# Chat Feature

## Owner

Chat

## Inputs

HTTP chat payload, Viewer to value, channel envelope, accepted transcript

## Outputs

final response, route decision, Viewer timeline event, channel response

## Side Effects

conversation write, event emission, optional Worker handoff through existing orchestrator

## Persistence

conversation stores and existing chat history logs

## Logs

session_id, route, to, status, error kind

## Error Contract

invalid recipient and runtime-unavailable errors must not silently fall back to Mio

## Current Main Files

modules/chat, internal/application/orchestrator, internal/adapter/viewer/handler_send.go

## Migration Boundary

This feature package is a registrar/facade entry point only. Existing implementation stays in the listed current files until contract tests and caller handoff are added for the relevant phase.
