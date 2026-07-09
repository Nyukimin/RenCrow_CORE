# Tool Manifest Migration

RenCrow tool execution now uses the V2 structured runner contract at the agent boundary.

## What Changed

- `agent.ToolRunner` now requires `ExecuteV2` and `ListTools`.
- Runtime wiring passes the current V2 runner chain directly to Mio and Shiro.
- `internal/domain/tool/legacy.go` and its tests were removed.
- Shiro's public `ExecuteTool` keeps its string-returning API by converting `ToolResponse.Result` to text and returning `ToolError.Message` for errors.

## Current Tool Definitions

Built-in tools are registered as `ToolMetadata` in `internal/infrastructure/tools/runner_registration.go`.

Manifest conversion remains available through:

```go
tool.ManifestFromMetadata(meta)
```

Use this when a caller needs a `ToolManifest` view of an existing registered tool.

## Migration Rule For New Tools

New tool runners must implement:

```go
ExecuteV2(ctx context.Context, toolName string, args map[string]any) (*tool.ToolResponse, error)
ListTools(ctx context.Context) ([]tool.ToolMetadata, error)
```

Do not add new adapters that convert V2 runners back to the old `Execute/List` pair.

## Verification

```bash
grep -r "NewLegacyRunner\\|LegacyRunner" --include="*.go" .
go test ./internal/domain/agent ./internal/domain/tool ./cmd/picoclaw -v
```
