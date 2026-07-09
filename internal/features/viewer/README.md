# Viewer Feature

## Owner

Viewer

## Inputs

HTTP viewer requests, SSE subscription, browser state, Viewer control events

## Outputs

HTML/assets, JSON tab state, SSE events, visible error state

## Side Effects

event log read/write and browser-facing state updates through existing adapter code

## Persistence

event log store and existing viewer state stores

## Logs

request path, session_id where present, status, visible error

## Error Contract

status API failure must be visible and must not be rendered as stale success

## Current Main Files

internal/adapter/viewer, cmd/rencrow/routes.go, cmd/rencrow/runtime_viewer_handlers.go

## Migration Boundary

This feature package is a registrar/facade entry point only. Viewer base/static route registration is owned by `internal/features/viewer/registrar.go`; existing handler implementations stay in the listed current files until contract tests and caller handoff are added for the relevant phase.
