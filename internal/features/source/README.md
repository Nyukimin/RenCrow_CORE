# Source Feature

## Owner

Source Registry

## Inputs

source registry entry, source run request, staging validation or promotion request

## Outputs

staging item, validation status, promotion candidate, source registry status

## Side Effects

source fetches, staging writes, validation/promotion writes through existing stores

## Persistence

L1 source registry and staging tables

## Logs

source_id, staging_id, validation status, promotion target, error kind

## Error Contract

search result alone is not a source read and must not be promoted automatically

## Current Main Files

internal/application/sourcefetcher, internal/adapter/viewer/source_registry_handler.go, cmd/rencrow/cli_source_registry.go

## Current Route Boundary

- `/viewer/source-registry`
- `/viewer/domain-graph/assertions`
- `/viewer/movie-catalog/domain-graph-sync`
- `/viewer/hobby-graph/domain-graph-sync`

## Migration Boundary

This feature package is a registrar/facade entry point only. Existing implementation stays in the listed current files until contract tests and caller handoff are added for the relevant phase. The registrar owns route registration and dependency handoff only.
