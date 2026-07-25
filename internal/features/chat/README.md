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

## Ownership Boundary

This feature owns `/viewer/send` route registration. The existing Viewer handler remains an adapter implementation until a concrete change-isolation benefit justifies moving it.
