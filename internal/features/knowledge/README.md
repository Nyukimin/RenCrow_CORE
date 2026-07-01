# Knowledge Feature

## Owner

Knowledge

## Inputs

validated source item, import request, wiki page/index request, glossary input

## Outputs

knowledge item, wiki index, import report, glossary state

## Side Effects

knowledge store writes and wiki artifact updates through existing services

## Persistence

knowledge DB/wiki stores and existing L1 records

## Logs

source_id, item_id, domain, import status, error kind

## Error Contract

unreviewed discovery must not be treated as confirmed knowledge

## Current Main Files

internal/application/knowledge, internal/application/knowledgememory, internal/adapter/viewer/knowledge_memory_handler.go, cmd/picoclaw/cli_knowledge.go

## Current Route Boundary

- `/viewer/glossary/recent`
- `/viewer/knowledge-memory`
- `/viewer/knowledge-memory/personal-archive`
- `/viewer/knowledge-memory/creative-knowledge`
- `/viewer/knowledge-memory/news-knowledge`
- `/viewer/knowledge-memory/daily-intake-rules`
- `/viewer/knowledge-memory/temporal-markers`
- `/viewer/knowledge-memory/review`
- `/viewer/knowledge-memory/dream-runs`
- `/viewer/knowledge-memory/dream-runs/propose`
- `/viewer/knowledge-memory/dream-runs/review`

## Migration Boundary

This feature package is a registrar/facade entry point only. Existing implementation stays in the listed current files until contract tests and caller handoff are added for the relevant phase. The registrar owns route registration and dependency handoff only.
