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

internal/infrastructure/vtuber, cmd/rencrow/vtuber_bridge.go, internal/adapter/viewer/live2d_*.go

## Migration Boundary

This feature package is a registrar/facade entry point only. Existing implementation stays in the listed current files until contract tests and caller handoff are added for the relevant phase.
