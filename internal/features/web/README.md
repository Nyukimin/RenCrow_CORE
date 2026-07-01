# Web Feature

## Owner

Web

## Inputs

search request, fetch request, browser actor request, trace discovery request

## Outputs

search result, fetch artifact, browser evidence, trace artifact

## Side Effects

network fetches, browser runs, artifact writes through existing web infrastructure

## Persistence

web gather cache, browser trace artifacts, staging records

## Logs

run_id, source_id, URL, provider, status, error kind

## Error Contract

discovery, source read, browser evidence, and promotion errors stay separate

## Current Main Files

modules/browseractor, modules/webgather, internal/application/webgather, internal/application/browsertrace, internal/infrastructure/webgather

## Current Route Boundary

- `/viewer/browser-trace-api`
- `/viewer/browser-trace-api/discover`
- `/viewer/browser-trace-api/validations`
- `/viewer/browser-trace-api/fetcher-proposals`
- `/viewer/complexity-hotspots`
- `/viewer/complexity-hotspots/scan`
- `/viewer/complexity-hotspots/proposals`
- `/viewer/complexity-hotspots/concrete-diffs`
- `/viewer/complexity-hotspots/coder-diffs`

## Migration Boundary

This feature package is a registrar/facade entry point only. Existing implementation stays in the listed current files until contract tests and caller handoff are added for the relevant phase. The registrar owns route registration and dependency handoff only.
