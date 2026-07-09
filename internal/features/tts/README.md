# Tts Feature

## Owner

TTS

## Inputs

speech text, voice profile, synthesis options, playback ACK

## Outputs

audio result, chunk event, playback state snapshot, visible TTS diagnostics

## Side Effects

provider HTTP calls, audio file writes, playback event emission through existing runtime code

## Persistence

audio artifacts, pending playback state, public session route state

## Logs

session_id, response_id, provider, voice, status, error kind

## Error Contract

provider timeout, invalid audio, and playback timeout are distinct errors

## Current Main Files

modules/tts, internal/infrastructure/tts, cmd/rencrow/tts_*.go, cmd/rencrow/idlechat_tts_*.go

## Current Route Boundary

- `/viewer/tts/audio`
- `/viewer/tts/playback-ack`

## Migration Boundary

This feature package is a registrar/facade entry point only. Existing implementation stays in the listed current files until contract tests and caller handoff are added for the relevant phase. The registrar owns route registration and dependency handoff only.
