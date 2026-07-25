# Core Feature

## Owner

Core / runtime composition

## Inputs

runtime config, module descriptors, health providers, process lifecycle events

## Outputs

manifest data, aggregate health, readiness state, registrar wiring points

## Side Effects

none; process wiring remains in cmd/rencrow

## Persistence

none; topology is read from ~/.rencrow/config.yaml by existing runtime code

## Logs

runtime status, health, readiness, and startup logs from existing cmd/rencrow code

## Error Contract

visible health/readiness errors; no silent fallback from repo-local config

## Current Main Files

modules/core, cmd/rencrow/module_*.go, cmd/rencrow/runtime_*.go

## Ownership Boundary

This feature owns process health, readiness, manifest, and module diagnostic route registration. Handler construction and process lifecycle remain composition-root responsibilities.
