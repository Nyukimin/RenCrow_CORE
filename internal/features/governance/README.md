# Governance Feature

## Owner

Governance

## Inputs

skill change, manifest, coder evidence, external PR request

## Outputs

gate decision, trigger log, audit report, bootstrap result

## Side Effects

governance store writes and external PR preparation through existing services

## Persistence

skill governance persistence store

## Logs

skill_id, change_id, gate, status, error kind

## Error Contract

unreviewed external contributions must not bypass governance gates

## Current Main Files

internal/application/skillgovernance, internal/domain/skillgovernance, internal/infrastructure/persistence/skillgovernance, internal/adapter/viewer/skill_governance_handler.go

## Current Route Boundary

- `/viewer/tool-harness/recent`
- `/viewer/dci/recent`
- `/viewer/dci/search`
- `/viewer/skill-governance/recent`
- `/viewer/skill-governance/bootstrap`
- `/viewer/skill-governance/contribution-gate`
- `/viewer/skill-governance/skill-changes`
- `/viewer/skill-governance/skill-change-evals`
- `/viewer/skill-governance/external-pr-submit`
- `/viewer/persona-observation`
- `/viewer/persona-observation/discomforts`
- `/viewer/persona-observation/triggers`
- `/viewer/persona-observation/canonical-responses`
- `/viewer/persona-observation/observations`
- `/viewer/persona-observation/aggregate`
- `/viewer/persona-observation/meta-updates`
- `/viewer/persona-observation/meta-updates/review`
- `/viewer/persona-observation/sessions`

## Migration Boundary

This feature package is a registrar/facade entry point only. Existing implementation stays in the listed current files until contract tests and caller handoff are added for the relevant phase. The registrar owns route registration and dependency handoff only.
