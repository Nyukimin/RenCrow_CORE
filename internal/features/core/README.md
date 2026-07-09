# Core Feature

## Owner

Core / runtime composition

## Inputs

runtime config, module descriptors, health providers, process lifecycle events

## Outputs

manifest data, aggregate health, readiness state, registrar wiring points

## Side Effects

none in this scaffold; existing process wiring remains in cmd/rencrow

## Persistence

none; topology is read from ~/.rencrow/config.yaml by existing runtime code

## Logs

runtime status, health, readiness, and startup logs from existing cmd/rencrow code

## Error Contract

visible health/readiness errors; no silent fallback from repo-local config

## Current Main Files

modules/core, cmd/rencrow/module_*.go, cmd/rencrow/runtime_*.go

## Migration Boundary

This feature package is a registrar/facade entry point only. Existing implementation stays in the listed current files until contract tests and caller handoff are added for the relevant phase.
