# Avatar Feature

## Owner

Avatar

## Inputs

emotion signal, TTS playback event, character runtime config

## Outputs

avatar state, lipsync trigger, character runtime display data

## Side Effects

Viewer event emission and VTuber bridge calls through existing runtime code

## Persistence

existing character runtime state where already stored

## Logs

character_id, emotion, event type, status, error kind

## Error Contract

avatar/bridge failure must not rewrite Chat display text

## Current Main Files

internal/features/avatar/character_runtime_handler.go, internal/infrastructure/vtuber, cmd/rencrow/vtuber_bridge.go, internal/adapter/viewer/live2d_*.go

## Ownership Boundary

This feature owns Character Runtime handler behavior and route registration. `internal/adapter/viewer/character_runtime_handler.go` is a compatibility shim. Live2D and VTuber bridge bodies remain in their current adapter/infrastructure packages.
