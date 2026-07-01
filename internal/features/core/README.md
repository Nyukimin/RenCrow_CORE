# Core Feature

## Owner

Core / runtime composition

## Inputs

runtime config, module descriptors, health providers, process lifecycle events

## Outputs

manifest data, aggregate health, readiness state, registrar wiring points

## Side Effects

none in this scaffold; existing process wiring remains in cmd/picoclaw

## Persistence

none; topology is read from ~/.picoclaw/config.yaml by existing runtime code

## Logs

runtime status, health, readiness, and startup logs from existing cmd/picoclaw code

## Error Contract

visible health/readiness errors; no silent fallback from repo-local config

## Current Main Files

modules/core, cmd/picoclaw/module_*.go, cmd/picoclaw/runtime_*.go

## Migration Boundary

This feature package is a registrar/facade entry point only. Existing implementation stays in the listed current files until contract tests and caller handoff are added for the relevant phase.
