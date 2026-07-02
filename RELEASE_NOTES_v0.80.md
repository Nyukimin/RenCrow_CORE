# RenCrow_CORE Ver0.80

Release date: 2026-07-02
Repository: https://github.com/Nyukimin/RenCrow_CORE

## Scope

RenCrow_CORE Ver0.80 is the first Public repository seed cut from the `picoclaw_multiLLM` staging branch. It establishes the public core runtime, module contracts, feature registrars, Viewer adapter, and Ver0.80 canonical docs without importing private local runtime state.

## Included

- Public root README, MIT license, module tree, build/test/run instructions.
- `modules/*` contracts and `modules/CURRENT_MAP.md` ownership map.
- `internal/features/*` registrar / ports / README scaffold.
- Gradual `cmd/picoclaw` feature registrar handoff.
- Viewer Chat contract for `to=mio|shiro|kuro|midori`.
- LLM Ops route ownership handoff to `internal/features/llm` while keeping handler bodies in the existing Viewer adapter legacy body.
- Public GitHub Actions CI for representative Go tests.
- Local RenCrow_Tools path defaults generalized through `RENCROW_TOOLS_ROOT` or `$HOME/RenCrow/RenCrow_Tools`.

## CI gate

The initial Public CI intentionally runs the same representative checks used for clean clone verification:

```bash
go test ./modules/...
go test ./cmd/picoclaw ./internal/features/... ./internal/adapter/viewer ./modules/...
```

Full `go test ./...` remains a broader local validation because some e2e packages can require runtime services or local fixtures.

## Go module path

Ver0.80 intentionally keeps the legacy Go module path:

```text
github.com/Nyukimin/picoclaw_multiLLM
```

Renaming the module path to a `RenCrow_CORE` import path is deferred to Ver0.81 or later because it affects all imports, downstream users, staging sync, docs, and migration guidance.

## Public export exclusions

The Public seed excludes local/private/runtime material such as:

- `config.yaml` / `.env` / secrets / private keys.
- runtime logs, DBs, caches, generated artifacts, large binaries.
- local agent / IDE / MCP metadata such as `.agents/`, `.claude/`, `.codex/`, `.cursor/`, `.serena/`, `.mcp.json`.
- private/reference/investigation docs from the staging repo export.

## Known legacy-body areas

The release preserves existing functionality instead of deleting or rewriting it during the public cut. Handler bodies, providers, stores, CLI internals, background jobs, and some adapter implementations remain in their existing packages as `legacy-body` until later feature-group migrations.

## Verification evidence

- Public repo CI `Go Test` succeeded on the release commit.
- Clean clone verification passed before release creation.
- Public blacklist path scan, secret pattern scan, and files-over-10MB scan passed during the initial public seed creation.
