# RenCrow_CORE Ver0.80

RenCrow_CORE is the public-ready core runtime for RenCrow, staged from `picoclaw_multiLLM` as the Ver0.80 seed.

Ver0.80 focuses on preserving existing behavior while making module and feature ownership explicit. It is not a feature-removal branch. Existing implementations that are not yet moved remain as `legacy-body` under `internal/domain`, `internal/application`, `internal/infrastructure`, `internal/adapter`, or `cmd/picoclaw` until their feature contracts and registrars are ready.

## Goals

- Keep existing Chat, Worker, Coder, Viewer, Voice, Ops, Web, Knowledge, Memory, Governance, Distributed, and Channel behavior intact.
- Define stable `modules/*` contracts for reusable DTOs, events, pure policy, and state ownership.
- Define `internal/features/*` registrar/facade boundaries for feature-group route registration and dependency handoff.
- Keep `cmd/picoclaw` as the process composition root: config load, dependency assembly, feature registrar calls, and server startup.
- Prepare this pushed HEAD as the initial source for the new Public repository `RenCrow_CORE`.

## Architecture Tree

```text
RenCrow_CORE Ver0.80
├── cmd/picoclaw                 # process composition root
│   ├── main.go
│   ├── routes.go                # legacy route grouping retained during migration
│   └── feature_registrars.go    # calls feature-group registrars
├── modules                      # public contracts and pure policy
│   ├── core
│   ├── chat
│   ├── worker
│   ├── llm
│   ├── tts
│   ├── stt
│   ├── voicechat
│   ├── browseractor
│   └── webgather
├── internal/features            # feature facades, ports, registrars
│   ├── core
│   ├── agent
│   ├── chat
│   ├── worker
│   ├── idlechat
│   ├── viewer
│   ├── llm
│   ├── tts
│   ├── stt
│   ├── voice
│   ├── avatar
│   ├── backlog
│   ├── heartbeat
│   ├── scheduler
│   ├── workstream
│   ├── revenue
│   ├── repair
│   ├── web
│   ├── source
│   ├── knowledge
│   ├── memory
│   ├── reports
│   ├── security
│   ├── sandbox
│   ├── governance
│   ├── superagent
│   ├── aiworkflow
│   ├── distributed
│   ├── channels
│   └── ops
├── internal/adapter             # external adapters and compatibility adapters
├── internal/domain              # legacy-body plus domain values and validation
├── internal/application         # legacy-body use cases and orchestration
└── internal/infrastructure      # legacy-body providers, persistence, transport, tools
```

## Module Contracts

Current module packages:

| Module | Owns |
| --- | --- |
| `modules/core` | module descriptors, health aggregation, state ownership metadata, module endpoint constants |
| `modules/chat` | Viewer recipient contract, route policy, final response and IdleChat topic policy |
| `modules/worker` | proposal / patch / execution result / failure classification contracts |
| `modules/llm` | role provider contracts, runtime provider planning, diagnostics, health policy |
| `modules/tts` | synthesis, provider planning, playback state, audio chunk and ACK contracts |
| `modules/stt` | transcription, viewer input observer, busy policy, websocket planning contracts |
| `modules/voicechat` | VoiceChat / VDS bridge / websocket route planning contracts |
| `modules/browseractor` | browser automation request / response, risk classification, artifact contract |
| `modules/webgather` | discovery, fetch, extraction, staging, and search contract boundary |

See `modules/README.md`, `modules/CURRENT_MAP.md`, and `modules/DEPENDENCY_RULES.md` for the current ownership map and dependency rules.

## Feature Catalog

Feature registrars live under `internal/features/*`. They own route registration and dependency handoff only. Handler bodies, providers, stores, background jobs, and CLI implementations remain in their existing legacy-body files unless a later migration phase explicitly moves them.

Current feature inventory:

```text
core, agent, chat, worker, idlechat, viewer, llm, tts, stt, voice, avatar,
backlog, heartbeat, scheduler, workstream, revenue, repair, web, source,
knowledge, memory, reports, security, sandbox, governance, superagent,
aiworkflow, distributed, channels, ops
```

See `internal/features/README.md` and each feature README for inputs, outputs, side effects, persistence, logs, error contract, and current main files.

## Viewer Chat Contract

Viewer normal chat uses `to=mio|shiro|kuro|midori` as the recipient / character selection contract.

- `to=mio`: normal Mio chat.
- `to=shiro`: Shiro as the visible recipient / speaker; this is not an OPS or Worker execution route.
- `to=kuro`: Kuro analysis-oriented chat.
- `to=midori`: Midori creative / exploratory chat.

`model_alias`, `route_prefix`, and old route aliases are legacy compatibility paths and must not become the primary normal Chat contract.

## Build and Test

The Ver0.80 seed intentionally keeps the legacy Go module path `github.com/Nyukimin/picoclaw_multiLLM`. Renaming the Go module path to `RenCrow_CORE` is a later compatibility migration, not part of the initial Public seed.

```bash
# Module contracts
GOCACHE=/tmp/picoclaw-gocache go test ./modules/...

# Composition root, feature registrars, Viewer adapter, module contracts
GOCACHE=/tmp/picoclaw-gocache go test ./cmd/picoclaw ./internal/features/... ./internal/adapter/viewer ./modules/...

# Full repository
GOCACHE=/tmp/picoclaw-gocache go test ./...
GOCACHE=/tmp/picoclaw-gocache go vet ./...

# Build / install local runtime
make build
make install
```

## Run

The local service is normally installed as `~/.local/bin/picoclaw` and run through the user service `picoclaw.service`.

```bash
make install
systemctl --user start picoclaw.service
curl http://127.0.0.1:18790/health
```

Before restarting an existing local runtime, stop it cleanly:

```bash
systemctl --user stop picoclaw.service
pgrep -a picoclaw || true
ss -ltnp | rg ':18790' || true
curl -fsS -m 2 http://127.0.0.1:18790/health || true
```

## Configuration and Secrets

Do not commit local secrets or machine-local configuration.

Use environment variables or local files outside the public repository for API keys and private runtime settings. `.env`, private keys, runtime DBs, logs, caches, generated artifacts, and local `config.yaml` files are not part of the Public repo seed.

When exporting from this staging repository into a new Public `RenCrow_CORE` repository, use `.rencrow-core-exportignore` as the export exclusion manifest. It is not a deletion list for this staging repo.

## Public Repo Seed Docs

Canonical Ver0.80 docs:

- `docs/02_正本仕様/05_RenCrow_CORE_Ver0.80_モジュール構成仕様.md`
- `docs/02_正本仕様/06_RenCrow_CORE_Ver0.80_モジュール化実装仕様.md`
- `docs/02_正本仕様/07_RenCrow_CORE_Ver0.80_組み換え実装作業資料.md`
- `docs/02_正本仕様/08_RenCrow_CORE_Ver0.80_Public_Repo起点化仕様.md`

## License and Attribution

RenCrow_CORE is distributed under the MIT License. See `LICENSE`.

PicoClaw / RenCrow work is heavily inspired by and based on `nanobot` by HKUDS. The existing attribution is retained in `LICENSE`.
