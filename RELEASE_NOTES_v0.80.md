# RenCrow_CORE Ver0.80

Release date: 2026-07-02
Repository: https://github.com/Nyukimin/RenCrow_CORE

## Scope

RenCrow_CORE Ver0.80 is the first Public repository seed cut from the `RenCrow_CORE` staging branch. It establishes the public core runtime, module contracts, feature registrars, Viewer adapter, and Ver0.80 canonical docs without importing private local runtime state.

## Included

- Public root README, MIT license, module tree, build/test/run instructions.
- `modules/*` contracts and `modules/CURRENT_MAP.md` ownership map.
- `internal/features/*` registrar / ports / README scaffold.
- Gradual `cmd/rencrow` feature registrar handoff.
- Viewer Chat contract for `to=mio|shiro|kuro|midori`.
- LLM Ops route ownership handoff to `internal/features/llm` while keeping handler bodies in the existing Viewer adapter legacy body.
- Public GitHub Actions CI for representative Go tests.
- Local RenCrow_Tools path defaults generalized through `RENCROW_TOOLS_ROOT` or `$HOME/RenCrow/RenCrow_Tools`.

## CI gate

The initial Public CI intentionally runs the same representative checks used for clean clone verification:

```bash
go test ./modules/...
go test ./cmd/rencrow ./internal/features/... ./internal/adapter/viewer ./modules/...
```

Full `go test ./...` remains a broader local validation because some e2e packages can require runtime services or local fixtures.

## Go module path

Ver0.80 intentionally keeps the legacy Go module path:

```text
github.com/Nyukimin/RenCrow_CORE
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

## Migrating from picoclaw

`picoclaw_multiLLM` was renamed to `RenCrow_CORE`. Repository, Go module import paths, CLI binary names, runtime home directory, environment variable prefix, systemd user units, and the Windows remote-agent binary/config layout all follow the new `rencrow` naming. Existing local installs need a one-time migration.

### Old vs new naming

| Area | Old (picoclaw) | New (RenCrow_CORE) |
| --- | --- | --- |
| Repository / module | `picoclaw_multiLLM` | `RenCrow_CORE` |
| CLI binary | `picoclaw` | `rencrow` |
| Runtime home directory | `~/.picoclaw/` | `~/.rencrow/` |
| Operation memory dir | `~/.picoclaw/rencrow/memory` (nested) | `~/.rencrow/memory` (flattened, no double `rencrow/`) |
| Environment variable prefix | `PICOCLAW_*` | `RENCROW_*` |
| systemd --user service | `picoclaw-watchdog.service` | `rencrow-watchdog.service` |
| systemd --user timer | `picoclaw-watchdog.timer` | `rencrow-watchdog.timer` |
| Windows remote agent binary | `picoclaw-agent.exe` | `rencrow-agent.exe` |
| Windows remote agent home | `C:/Users/<user>/.picoclaw/` | `C:/Users/<user>/.rencrow/` |

### Migration scripts

Two scripts automate the runtime-home move. Both default to a non-destructive copy and support a dry run.

- **Linux/macOS** (host running the RenCrow_CORE server): `scripts/migrate_picoclaw_home.sh [--dry-run] [--move]`
  - Copies (or, with `--move`, moves) `~/.picoclaw` to `~/.rencrow`.
  - Flattens a legacy nested `~/.rencrow/rencrow/memory` into `~/.rencrow/memory` and removes the now-empty `~/.rencrow/rencrow/` directory.
  - Aborts without changes if `~/.rencrow` already exists (idempotent / safe to re-run).
  - No-op with exit 0 if `~/.picoclaw` does not exist.
- **Windows** (remote agent host): `powershell -File scripts\migrate_picoclaw_home.ps1 [-DryRun]`
  - Copies `%USERPROFILE%\.picoclaw` to `%USERPROFILE%\.rencrow` and performs the same `rencrow\memory` de-nesting.
  - Aborts without changes if `%USERPROFILE%\.rencrow` already exists; no-op if `%USERPROFILE%\.picoclaw` does not exist.
  - Does **not** touch Scheduled Tasks or Startup entries automatically — it only prints a reminder to update them by hand.

Run with `--dry-run` / `-DryRun` first to review what would happen before applying the migration.

### Manual follow-up after running the scripts

- Rename any exported `PICOCLAW_*` environment variables (shell rc files, systemd unit `Environment=` overrides, `.env` files) to `RENCROW_*` (e.g. `RENCROW_TOOLS_ROOT`).
- Stop and disable the legacy systemd --user units if they exist, then install the new ones:
  ```bash
  systemctl --user disable --now picoclaw-watchdog.timer picoclaw-watchdog.service
  make install-watchdog enable-watchdog
  ```
- Check `crontab -l` and any other cron/systemd timers for hardcoded `~/.picoclaw` paths or `picoclaw` binary/unit names and update them.
- On Windows, update any Scheduled Task action or Startup shortcut that points at `picoclaw-agent.exe` to point at the new `rencrow-agent.exe`, and update the `remote_agent_path` / `remote_config_path` values in the distributed-execution config on the RenCrow_CORE server side (e.g. `C:/Users/<user>/rencrow-agent.exe`, `C:/Users/<user>/.rencrow/config.yaml`).
- Once the new `~/.rencrow` (or `%USERPROFILE%\.rencrow`) tree has been verified, the old `~/.picoclaw` directory may be deleted manually (not needed if you migrated with `--move`).
