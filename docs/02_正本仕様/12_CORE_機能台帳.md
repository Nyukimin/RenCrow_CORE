# CORE 機能台帳

- status: canonical
- canonical_path: `docs/02_正本仕様/12_CORE_機能台帳.md`
- promoted_at: 2026-07-14

## 1. 目的

本台帳は、RenCrow_CORE に存在する機能を、次の観点で混同しないための索引である。

```text
確定済みの責務
公開 contract
production 実装
feature facade
legacy-body
構想
対象外
```

詳細な実装証跡、E2E 状態、個別補足は `docs/refs/10_新仕様/13_実装項目インベントリ.md` を参照する。本台帳はそれを置き換えない。

## 2. 状態定義

| State | 意味 | 完了条件 |
| --- | --- | --- |
| `canonical` | 責務、境界、不変条件が正本仕様として確定している | 正本仕様または新仕様で責務境界が明文化されている |
| `contracted` | `modules/*` に公開 contract / DTO / policy / health 境界がある | module package と tests が存在する |
| `implemented` | production code と主要 wiring が存在する | domain / application / infrastructure / adapter のいずれかで通常利用できる |
| `facade_only` | `internal/features/*` に入口はあるが、本体は既存 legacy-body 側に残る | registrar / ports / README があり、実装本体は別 package |
| `legacy_body` | 現役実装だが、新しい module / feature 境界へ未整理 | runtime で使われるが責務分離が途中 |
| `concept` | 仕様または構想のみ | docs 上の作業仕様はあるが production package は未作成 |
| `out_of_scope` | CORE 本線対象外 | 外部 server / 外部 data / 別 repo が正本 |

## 3. 状態判断ルール

- `modules/<id>` がある機能は `contracted` を基本状態とする。
- `internal/features/<id>` だけがある機能は `facade_only` または `implemented` とする。
- domain / application / persistence があり、runtime route / service wiring がある機能は `implemented` とする。
- 実装本体が `cmd/rencrow`、`internal/application`、`internal/infrastructure`、`internal/adapter` に残るものは、必要に応じて `legacy_body` を併記する。
- `10_RenCrow_ToBe_統合仕様.md` で新たに定義した `advisor`、`agent_profile`、`knowledge_relation`、`economic_objective` は、MVP 実装後に `implemented` とし、未接続の runtime / Viewer / batch は備考に残す。
- CORE が接続先を管理するだけで、本体が外部 repo / server / data store にあるものは `out_of_scope` または接続機能として台帳化する。

## 4. 公開 module contract 台帳

| Feature | State | Owner package | Runtime / bridge | 根拠 |
| --- | --- | --- | --- | --- |
| `core` | `contracted` | `modules/core` | `cmd/rencrow/module_*`, `internal/adapter/modulebridge` | module manifest / health / lifecycle |
| `chat` | `contracted` | `modules/chat` | `internal/adapter/modulebridge`, `internal/application/orchestrator` | dialogue / route decision / Viewer send |
| `worker` | `contracted` | `modules/worker` | `internal/adapter/modulebridge`, `internal/application/service`, `internal/infrastructure/tools` | execution / proposal / failure classification |
| `llm` | `contracted` | `modules/llm` | `internal/infrastructure/llm`, `cmd/rencrow/runtime_llm_*` | provider planning / role diagnostics |
| `tts` | `contracted` | `modules/tts` | `internal/infrastructure/tts`, `cmd/rencrow/tts_*` | synthesis / playback state / provider policy |
| `stt` | `contracted` | `modules/stt` | `internal/infrastructure/stt`, `cmd/rencrow/stt_*` | transcription / Viewer input / WebSocket planning |
| `voicechat` | `contracted` | `modules/voicechat` | `internal/features/voice`, runtime voice handlers | Viewer voice-direct / VDS bridge |
| `browseractor` | `contracted` | `modules/browseractor` | `internal/infrastructure/browseractor`, `internal/features/web` | browser operation contract / risk policy |
| `webgather` | `contracted` | `modules/webgather` | `internal/application/webgather`, `internal/infrastructure/webgather` | search / fetch / extract / staging |

補足:

- `modules/*` は公開契約、DTO、pure policy、state ownership metadata を置く場所である。
- 実装本体がすべて `modules/*` に移動済みという意味ではない。
- 既存 runtime 実装は `legacy_body` として段階移行中である。

## 5. Feature facade 台帳

| Feature | State | Feature facade | 主な実装 / owner package | 備考 |
| --- | --- | --- | --- | --- |
| `agent` | `facade_only` | `internal/features/agent` | `internal/domain/agent`, `internal/domain/agentprofile`, `cmd/rencrow/runtime_agents.go` | Agent identity / role / persona 境界。Agent profile は静的 catalog MVP 実装済み |
| `aiworkflow` | `implemented` | `internal/features/aiworkflow` | `internal/application/aiworkflow`, `internal/domain/aiworkflow`, `internal/infrastructure/persistence/aiworkflow` | workflow trace / operation API |
| `avatar` | `facade_only` | `internal/features/avatar` | `internal/application/characterruntime`, Viewer / TTS runtime | emotion / lipsync / display 境界 |
| `backlog` | `facade_only` | `internal/features/backlog` | `internal/domain/backlog`, Viewer handlers | backlog intake / runner status |
| `channels` | `implemented` | `internal/features/channels` | `internal/application/channel`, channel adapters | inbound envelope / external channel boundary |
| `chat` | `implemented` | `internal/features/chat` | `internal/application/orchestrator`, `internal/domain/routing`, `modules/chat` | route / response / Viewer send |
| `core` | `implemented` | `internal/features/core` | `modules/core`, `cmd/rencrow/module_*`, runtime composition | process health / manifest / topology |
| `distributed` | `facade_only` | `internal/features/distributed` | `internal/domain/transport`, `internal/infrastructure/transport` | remote agent / delivery boundary |
| `games` | `facade_only` | `internal/features/games` | Bridge handlers / external `RenCrow_GAMES` | game world 本体は CORE 外 |
| `governance` | `implemented` | `internal/features/governance` | `internal/application/skillgovernance`, `internal/domain/skillgovernance`, persistence | skill governance / change gate |
| `heartbeat` | `implemented` | `internal/features/heartbeat` | `internal/application/heartbeat`, workstream / revenue triggers | due run / draft launch |
| `idlechat` | `implemented` | `internal/features/idlechat` | `internal/application/idlechat`, `modules/chat`, `modules/tts` | idle session / topic / TTS trigger |
| `knowledge` | `implemented` | `internal/features/knowledge` | `internal/application/knowledge`, `internal/application/knowledgememory`, `internal/application/knowledgerelation`, persistence | import / wiki / glossary。relation layer MVP 実装済み、importer / batch 接続は後続 |
| `llm` | `implemented` | `internal/features/llm` | `modules/llm`, `internal/infrastructure/llm`, runtime providers | role provider / health / diagnostics |
| `memory` | `implemented` | `internal/features/memory` | `internal/domain/conversation`, `internal/domain/memory`, persistence/conversation | Memory lifecycle / RecallPack / layers |
| `ops` | `implemented` | `internal/features/ops` | health / package validation / repair / OTEL packages | health / doctor / cleanup / export |
| `repair` | `facade_only` | `internal/features/repair` | `internal/application/historyrepair`, Viewer handlers | repair request / repair event |
| `reports` | `implemented` | `internal/features/reports` | `internal/application/verification`, `internal/domain/verification` | evidence / verification summary |
| `revenue` | `implemented` | `internal/features/revenue` | `internal/domain/revenue`, `internal/application/revenue`, persistence/revenue | product / revenue event / human gate。Economic Objective MVP 実装済み、scheduler / Viewer 接続は後続 |
| `sandbox` | `implemented` | `internal/features/sandbox` | `internal/domain/sandbox`, `internal/application/sandbox`, persistence/sandbox | promotion gate / rollback |
| `scheduler` | `implemented` | `internal/features/scheduler` | `internal/domain/scheduler`, `internal/application/scheduler`, persistence/scheduler | due jobs / run log |
| `security` | `facade_only` | `internal/features/security` | `internal/domain/security`, `internal/infrastructure/security` | policy / guard / rollback boundary |
| `source` | `implemented` | `internal/features/source` | `internal/application/sourcefetcher`, Viewer source registry handlers | Source Registry / staging / validation |
| `stt` | `implemented` | `internal/features/stt` | `modules/stt`, `internal/infrastructure/stt`, runtime STT handlers | transcription / Viewer input |
| `superagent` | `implemented` | `internal/features/superagent` | `internal/application/superagent`, `internal/domain/superagent`, persistence/superagent | run queue / trace |
| `tts` | `implemented` | `internal/features/tts` | `modules/tts`, `internal/infrastructure/tts`, runtime TTS handlers | synthesis / playback / chunks |
| `viewer` | `implemented` | `internal/features/viewer` | `internal/adapter/viewer`, `cmd/rencrow/routes.go` | Viewer shell / SSE / visible-state errors |
| `voice` | `implemented` | `internal/features/voice` | voice input / audio router / VDS bridge handlers | voice input/output grouping |
| `web` | `implemented` | `internal/features/web` | `internal/application/webgather`, `internal/application/browsertrace`, browser actor infra | WebGather / BrowserTrace / BrowserActor |
| `worker` | `implemented` | `internal/features/worker` | `modules/worker`, `internal/application/service`, `internal/infrastructure/tools` | execution / command/tool boundary |
| `workstream` | `implemented` | `internal/features/workstream` | `internal/domain/workstream`, `internal/application/heartbeat`, persistence/workstream | goal / artifact / vault / steering |

## 6. Legacy-body 台帳

次の領域は現役実装であり、削除対象ではない。ただし新しい module / feature 境界へすべて移動済みではない。

| Area | State | 現在の主な場所 | 移行方針 |
| --- | --- | --- | --- |
| composition root | `legacy_body` | `cmd/rencrow/runtime_*`, `cmd/rencrow/module_*`, `cmd/rencrow/routes.go` | policy を `modules/*` または `internal/application/*` へ出し、cmd は wiring に寄せる |
| Viewer adapter | `legacy_body` | `internal/adapter/viewer` | visible-state / HTTP adapter として維持し、domain logic は外へ出す |
| modulebridge | `legacy_body` | `internal/adapter/modulebridge` | existing runtime と `modules/*` contract の compatibility bridge として維持 |
| orchestrator | `legacy_body` | `internal/application/orchestrator` | Chat / Worker / LLM / TTS 境界を段階分離する |
| providers | `legacy_body` | `internal/infrastructure/llm`, `internal/infrastructure/tts`, `internal/infrastructure/stt` | provider HTTP 実行は infrastructure に残し、policy は module 側へ出す |
| tools | `legacy_body` | `internal/infrastructure/tools` | Worker / Tool Harness 管理下で維持。横断新規ツールは `RenCrow_Tools` |
| persistence | `legacy_body` | `internal/infrastructure/persistence/*` | 各 domain / application と対応を明記し、schema 追加は小さく行う |

## 7. To-Be 追加概念台帳

| Feature | State | 仕様 | 実装予定 package | 初回作業 |
| --- | --- | --- | --- | --- |
| `advisor` | `implemented` | `10_RenCrow_ToBe_統合仕様.md`, `../04_構築指標/03_Advisor_AgentProfile接続実装仕様.md` | `internal/domain/advisor`, `internal/application/advisor` | Codex を AdvisorService 経由にするMVP。Shiro wiring 済み。persistence / score集計は `03` |
| `agent_profile` | `implemented` | `10_RenCrow_ToBe_統合仕様.md`, `../04_構築指標/03_Advisor_AgentProfile接続実装仕様.md` | `internal/domain/agentprofile`, `internal/application/agentprofile` | 8人格の静的 profile と AutonomyEnvelope MVP。runtime policy 反映は `03` |
| `knowledge_relation` | `implemented` | `10_RenCrow_ToBe_統合仕様.md`, `../04_構築指標/04_KnowledgeRelation接続実装仕様.md` | `internal/domain/knowledgerelation`, `internal/application/knowledgerelation`, `persistence/conversation/l1sqlite` | `l1_knowledge_entity` / `l1_knowledge_item_entity` / `l1_knowledge_item_relation` と RecallPack relation snippet MVP。import / batch / runtime expansion は `04` |
| `economic_objective` | `implemented` | `10_RenCrow_ToBe_統合仕様.md`, `../04_構築指標/05_EconomicObjective接続実装仕様.md` | `internal/domain/revenue`, `internal/application/revenue`, `persistence/revenue` | Opportunity / EconomicTask / Reflection と approval-required task guard MVP。scheduler / approval UI 接続は `05` |

## 8. CORE 対象外 / 外部正本

| 対象 | State | CORE の責務 | 正本 |
| --- | --- | --- | --- |
| LLM 推論本体 | `out_of_scope` | provider 接続、role routing、health | `RenCrow_LLM` または外部 provider |
| TTS 生成本体 | `out_of_scope` | provider 接続、payload、playback state | `RenCrow_TTS` または外部 provider |
| STT 推論本体 | `out_of_scope` | provider 接続、Viewer input、WebSocket plan | `RenCrow_STT` または外部 provider |
| Vision 推論本体 | `out_of_scope` | 接続、成果物管理、Viewer連携 | `RenCrow_Vision` または外部 provider |
| Game 世界 / Executor / Replay | `out_of_scope` | bridge、Persona / Recall / Memory / LLM Router 接続 | `RenCrow_GAMES` |
| 横断補助ツール | `out_of_scope` | 呼び出し、検証、成果物登録 | `RenCrow_Tools` |
| 大量 Knowledge data | `out_of_scope` | staging、validation、index、recall | 外部 data store / archive |

## 9. 実装順

本台帳以降の作業順は `11_RenCrow_ToBe_統合実装仕様.md` に従う。

```text
1. Advisor MVP（MVP実装済み。persistence / score集計は後続）
2. Agent Profile MVP（静的catalog実装済み。runtime policy 反映は後続）
3. Knowledge Relation MVP（MVP実装済み。importer / batch / 1-2 hop runtime expansion は後続）
4. Economic Objective MVP（MVP実装済み。scheduler / Viewer / approval UI 接続は後続）
5. Advisor / AgentProfile 接続（`04_構築指標/03`）
6. Knowledge Relation 接続（`04_構築指標/04`）
7. Economic Objective 接続（`04_構築指標/05`）
8. Viewer / Ops 表示（`04_構築指標/06`）
```

ただし、各 Phase では次を守る。

- 既存 `modules/*` contract を壊さない。
- feature facade と legacy-body を無理に同時移動しない。
- runtime 影響があるものは最初 opt-in / draft-only にする。
- Human approval gate より先に外部副作用へ接続しない。

## 10. 完了条件

本台帳 Phase の完了条件は次である。

- 公開 module contract が列挙されている。
- `internal/features/*` が列挙されている。
- 各 feature に owner package と state がある。
- `advisor`、`agent_profile`、`knowledge_relation`、`economic_objective` が MVP 実装状態として登録され、後続接続が明記されている。
- `docs/refs/10_新仕様/13_実装項目インベントリ.md` と役割が分かれている。
- MVP 実装時は domain / application / persistence / runtime wiring の変更が対象 feature に限定されている。
