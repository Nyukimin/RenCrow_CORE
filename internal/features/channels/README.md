# Channels Feature

## Owner

Channels

## Inputs

LINE/Slack/Discord/Telegram envelope, channel config, media payload

## Outputs

normalized inbound request, response adapter payload, channel status

## Side Effects

channel API send/read calls through existing adapters

## Persistence

channel logs and existing media artifacts where configured

## Logs

channel, user_id, request_id, status, error kind

## Error Contract

signature failure, unsupported payload, and send failure remain explicit

## Current Main Files

internal/adapter/line, internal/adapter/channels, internal/application/channel, cmd/rencrow/runtime_channels.go

## Current Route Boundary

- `/webhook`
- `/webhook/telegram`
- `/webhook/discord`
- `/webhook/slack`
- `/entry`
- `/chrome/bridge`
- `/chrome/bridge/status`
- `/chrome/bridge/events`

## Migration Boundary

This feature package is a registrar/facade entry point only. Existing implementation stays in the listed current files until contract tests and caller handoff are added for the relevant phase. The registrar owns route registration and dependency handoff only.
