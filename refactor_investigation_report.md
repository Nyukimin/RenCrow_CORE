# Refactor Investigation Report

Date: 2026-06-12

## Baseline

Baseline files present in the workspace:

- `baseline_git_status.log`
- `baseline_build.log`
- `baseline_test.log`
- `baseline_coverage.log`
- `baseline_domain_coverage.log`
- `baseline_vet.log`
- `baseline_mod_verify.log`
- `baseline_health.log`
- `baseline_doctor.log`

Baseline build, test, coverage, vet, and module verification logs exist. `baseline_health.log` and `baseline_doctor.log` show config-load failure because `./config.yaml` was not present, so final runtime verification must be rerun with an explicit config path.

## Archive Code

Search:

```bash
rg -n "complex_story_mode|parquet_export_job" --glob '*.go' --glob '!internal/application/**/archive/**' .
```

Result:

- No active Go reference to `complex_story_mode` was found outside archive. Keep `internal/application/idlechat/archive/complex_story_mode/` as archive.
- `internal/application/archive/parquet_export_job.go` is used via `archiveapp` from `cmd/picoclaw/runtime_background_jobs.go` when `RENCROW_PARQUET_EXPORT_DIR` is set. It is not safe to delete as unused archive code.

## Legacy Tool Runner

Search:

```bash
rg -n "NewLegacyRunner|LegacyRunner|ChatLegacy|WorkerLegacy" . -g '*.go'
```

Result after migration:

- No Go references remain.
- `agent.ToolRunner` uses the V2 structured contract.
- Runtime wiring passes `ChatRuntimeRunnerV2` and `WorkerRuntimeRunnerV2` directly to agents.
- Migration guide: `docs/tools/manifest_migration.md`.

## Deprecated Config Fields

Search:

```bash
rg -n "Ollama\\.ChatModel|Ollama\\.WorkerModel|ollama\\.chat_model is deprecated|deprecated: Model" internal/adapter/config cmd
```

Result after migration:

- Deprecated `OllamaConfig.ChatModel` and `OllamaConfig.WorkerModel` were removed.
- The old compatibility mapping in `setDefaults` was removed.
- One-time migration script: `scripts/migrate_config_v3_to_v4.sh`.
- Migration guide: `docs/config/config_migration.md`.

## Database Schema

Search:

```bash
rg -n "CREATE TABLE|ALTER TABLE|CREATE INDEX|schema" internal/infrastructure/persistence -g '*.go' -g '*.sql'
find internal/infrastructure/persistence -name '*.sql' -o -name '*schema*' -print
find . -name '*.db' -o -name '*.duckdb' | rg -v '\\.serena|vendor|node_modules'
```

Findings:

- Conversation L1 schema is code-owned in `internal/infrastructure/persistence/conversation/l1_sqlite_schema.go`.
- Conversation L2/archive schema is code-owned in `internal/infrastructure/persistence/conversation/duckdb_store.go`.
- Existing code already performs idempotent `ALTER TABLE` additions for `l1_daily_digest.digest_slot` and source registry fetch status fields.
- No repository-local `.db` or `.duckdb` file was found by the current scan.
- Current Phase 1 edits did not add new SQLite or DuckDB columns, so no DB migration script is needed for the changes made so far.

## Conversation Memory Flow

Current evidence from tests and code:

- L0 active thread and turn lifecycle are covered in `internal/infrastructure/persistence/conversation/engine_impl_test.go`.
- L1 SQLite staging, validation, promotion, memory state, user memory, source registry, news, knowledge, and vector sink sync are covered in `internal/infrastructure/persistence/conversation/l1_sqlite_store_test.go`.
- L2 DuckDB thread summaries and L1 archive export are covered in `duckdb_store_test.go`.
- L3 Qdrant/vector store behavior has unit coverage and integration tests that skip when Qdrant is unavailable.
- `test/e2e/memory_system_test.go` now covers the requested 15-message daily conversation flow: L1 observed events, staging validation, promotion to confirmed memory, L2 DuckDB summary, L3 vector-search contract, and role-filtered RecallPack assembly.

## Phase 1 Status

Completed in this pass:

- TODO retry classification implemented and tested.
- Deprecated Ollama config fields removed, migration script and guide added.
- Legacy domain tool runner removed; agents now consume the V2 runner contract.
- Archive investigation corrected: `complex_story_mode` retained, parquet export job retained because runtime uses it.
- Memory-system E2E added in `test/e2e/memory_system_test.go`.
- Config type definitions split by responsibility: `config_types.go` now keeps the central aggregate and cross-cutting groups, `config_runtime_types.go` owns runtime/LLM/Web sidecar config types, and `config_audio_agent_types.go` owns TTS/STT/VTuber/AudioRouter/ViewerLog/Coder config types.
- Domain coverage remediation completed to the stated Domain target with focused tests for agent routing/builders/error metadata, memory cutover/namespace rules, coderloop message parsing and observation truncation, LLM stream callback context, verification validation, sandbox promotion apply/rollback gates, contract required sections, validation contracts, tool harness behavior, and conversation constructors.
- Viewer responsibility separation started by moving complexity scan candidate-pattern derivation from `internal/adapter/viewer/complexity_hotspot_handler.go` into `internal/application/complexity/candidate_patterns.go`, with application-level tests.

Still required:

- Broad `./internal/...` coverage remediation. Domain coverage now reaches the target, but the full internal aggregate is still below the instruction target because large adapter/application/infrastructure packages remain lightly covered.
- Runtime health remediation. `PICOCLAW_CONFIG=config/config.yaml.example ./build/picoclaw-linux-amd64 doctor` runs, but reports health DOWN because the example local LLM endpoints are not accepting connections.

## Post-Change Verification

Commands run:

```bash
go test ./internal/adapter/config/... ./internal/domain/agent ./internal/domain/tool ./internal/infrastructure/persistence/conversation/... ./cmd/picoclaw ./cmd/picoclaw-agent ./test/e2e -v
go test ./internal/adapter/config/... -v
go test ./test/integration -v
go test ./...
make build
go vet ./...
go mod verify
git diff --check
go test -cover ./internal/domain/...
go test -cover ./internal/...
go test ./internal/domain/... -coverprofile=/tmp/rencrow_domain.cover && go tool cover -func=/tmp/rencrow_domain.cover
go test ./internal/... -coverprofile=/tmp/rencrow_internal.cover && go tool cover -func=/tmp/rencrow_internal.cover
go test ./internal/domain/memory ./internal/domain/coderloop ./internal/domain/llm ./internal/domain/verification -cover
go test ./internal/domain/sandbox ./internal/domain/contract -cover
go test ./internal/domain/... -coverprofile=/tmp/rencrow_domain_after_slice2.cover && go tool cover -func=/tmp/rencrow_domain_after_slice2.cover
go test ./internal/domain/complexity ./internal/domain/browsertrace ./internal/domain/superagent ./internal/domain/dci ./internal/domain/knowledgememory -cover
go test ./internal/domain/... -coverprofile=/tmp/rencrow_domain_after_slice4.cover && go tool cover -func=/tmp/rencrow_domain_after_slice4.cover
go test ./internal/domain/... -coverprofile=/tmp/rencrow_domain_after_agent_memory_helpers.cover && go tool cover -func=/tmp/rencrow_domain_after_agent_memory_helpers.cover
go test ./internal/... -coverprofile=/tmp/rencrow_internal_after_gemini.cover && go tool cover -func=/tmp/rencrow_internal_after_gemini.cover
go test ./internal/application/complexity ./internal/adapter/viewer -run 'Test(DeriveCandidatePatterns|MergeCandidatePatterns|HandleComplexityHotspot)' -count=1
go test ./...
make build
go vet ./...
go mod verify
git diff --check
PICOCLAW_CONFIG=config/config.yaml.example ./build/picoclaw-linux-amd64 health
PICOCLAW_CONFIG=config/config.yaml.example ./build/picoclaw-linux-amd64 doctor
```

Results:

- Targeted package tests passed.
- Config package tests passed after the config type split.
- Integration tests passed after updating their test doubles to the V2 tool runner contract.
- `go test ./...` passed after the Viewer/application extraction and the later glossary/autonomous/MCP/Gemini coverage additions.
- `make build` passed after the Viewer/application extraction and produced `build/picoclaw-linux-amd64`.
- `go vet ./...` and `go mod verify` passed after the Viewer/application extraction; `git diff --check` passed again after the later coverage additions.
- Domain coverage target is met: `go test ./internal/domain/... -coverprofile=/tmp/rencrow_domain_after_agent_memory_helpers.cover && go tool cover -func=/tmp/rencrow_domain_after_agent_memory_helpers.cover` reports total `95.0%`.
- Full internal coverage target is not met: `go test ./internal/... -coverprofile=/tmp/rencrow_internal_after_gemini.cover && go tool cover -func=/tmp/rencrow_internal_after_gemini.cover` reports total `71.7%`.
- Current lower-coverage internal packages include Viewer `63.1%`, IdleChat application `60.1%`, moviecatalog application `47.1%`, autonomous application `29.9%`, multiple infrastructure provider packages below 70%, and several glossary packages at `0.0%`.
- Improved domain package coverage includes agent `92.3%`, memory `98.2%`, coderloop `97.3%`, llm `100.0%`, verification `98.4%`, sandbox `96.6%`, contract `100.0%`, browsertrace `99.0%`, superagent `97.4%`, dci `98.1%`, knowledgememory `93.7%`, complexity `100.0%`, revenue `99.0%`, and skillgovernance `95.4%`.
- Complexity Viewer candidate-pattern derivation extraction passes focused tests in `./internal/application/complexity` and `./internal/adapter/viewer`; application/complexity package coverage is now `78.3%`.
- Additional internal coverage remediation improved glossary packages (`feed` `100.0%`, `service` `100.0%`, `persistence` `89.1%`, `mio_adapter` `90.6%`), `internal/application/autonomous` to `88.5%`, `internal/infrastructure/mcp` to `67.0%`, and `internal/infrastructure/llm/providers/gemini` to `92.3%`.
- `health` exits with status 1 against the example config because `192.168.1.31:8081`, `:8082`, and `:8083` refused connections.
- `doctor` exits successfully and reports `[WARN] health checks report DOWN` with the hint to verify Ollama base URL/model settings.
- `go mod tidy` leaves a diff in `go.mod`/`go.sum`; the diff promotes `github.com/PuerkitoBio/goquery v1.12.0` to direct and removes stale sums.
