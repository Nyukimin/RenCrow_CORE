# Stt Feature

## Owner

STT

## Inputs

audio payload, websocket frame, Viewer microphone state, STT config

## Outputs

transcript draft/final, viewer-input snapshot, diagnostics, chat input envelope

## Side Effects

provider HTTP/WebSocket calls and debug artifact writes through existing runtime code

## Persistence

STT debug captures and existing runtime state where configured

## Logs

session_id, request_id, provider, status, timing, error kind

## Error Contract

empty audio, timeout, busy, provider error, and viewer input failure remain distinct

## Current Main Files

modules/stt, internal/infrastructure/stt, cmd/rencrow/stt_runtime_*.go

## Current Route Boundary

- `/viewer/stt/log`
- `/viewer/stt/wav`
- `/viewer/stt/wav/raw`
- `/viewer/stt/autotest`
- `/viewer/stt/admin/restart`
- `/stt/health`
- `/stt/file`
- `/stt/chat-input`
- `/stt`
- `/stt-ws`
- `/ws`

## Migration Boundary

This feature package is a registrar/facade entry point only. Existing implementation stays in the listed current files until contract tests and caller handoff are added for the relevant phase. The registrar owns route registration and dependency handoff only.
