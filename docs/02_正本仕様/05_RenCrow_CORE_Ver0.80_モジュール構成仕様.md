# RenCrow_CORE Ver0.80 モジュール構成仕様

作成日: 2026-07-01
対象: `RenCrow_CORE`
位置づけ: RenCrow_CORE Ver0.80 の機能モジュール構成に関する正本仕様

## RenCrow_CORE Public repo 起点化の前提

この仕様は、`RenCrow_CORE` 現ブランチを RenCrow_CORE Ver0.80 の seed / staging source として扱う。

`RenCrow_CORE` で既存機能を削らずにモジュール境界を整理し、すべて push した HEAD を、新規 Public repository `RenCrow_CORE` の Ver0.80 起点にする。

したがって、この仕様でいう構成変更は、既存機能を捨てるための整理ではない。未整理の実装も、次のいずれかに必ず配置して扱う。

- `modules/*` の contract / DTO / event / pure policy。
- `internal/features/*` の facade / ports / registrar / feature-local glue。
- `internal/adapter/*` の external adapter / compatibility adapter。
- `internal/domain`、`internal/application`、`internal/infrastructure` の段階移行中 `legacy-body`。

`legacy-body` は削除予定物ではなく、RenCrow_CORE Ver0.80 の初期状態で既存機能を保持するための段階移行中実体である。

## 目的

RenCrow_CORE Ver0.80 では、既存機能を落とさず、Chat、IdleChat、Viewer、Agent、Voice、Ops などを機能単位で閉じ込める。

この仕様でいう「モジュール化」は、単なるファイル移動ではない。各機能が次を説明できる状態をいう。

- 入力
- 出力
- 副作用
- 永続化
- ログ
- エラー契約
- 差し替え時に守る contract / DTO / event / adapter / port

## 既存仕様との関係

この仕様は、次の既存仕様と調査結果を前提にする。

| 種別 | 参照 | 採用する内容 |
| --- | --- | --- |
| システム概要 | `docs/01_理解/01_システム概要.md` | RenCrow は入口、セッション、Viewer、各 RenCrow module server への橋渡しを担う。 |
| Agent 責務 | `docs/01_理解/02_キャラクター・エージェント仕様.md` | Mio / Shiro / Aka / Ao / Gin / Kin / Kuro / Midori の 8 Agent 体系。 |
| 実装正本 | `docs/02_正本仕様/02_実装仕様.md` | Viewer `to` 契約、Chat/Worker/Coder 安全境界、runtime 経路。 |
| Runtime Config | `docs/02_正本仕様/03_Runtime_Config.md` | Viewer / rencrow は LLM / TTS / STT backend を直接呼ばない。 |
| Topology | `docs/refs/10_新仕様/90_Runtime_Topology_Config仕様.md` | `~/.rencrow/config.yaml` を runtime topology の設計図として扱う。 |
| 現状検証 | `docs/調査/20260701_170923_RenCrow_CORE_Ver0.80_現状モジュール検証.md` | 現在の集中点、既存テスト状態、移行順。 |
| Agent / 機能整理 | `docs/調査/20260701_RenCrow_CORE_Ver0.80_AgentList_機能リスト整理.md` | AgentList 本体、補助ロール、機能リスト。 |
| 既存 module 設計 | `modules/DESIGN.md`, `modules/CURRENT_MAP.md`, `modules/DEPENDENCY_RULES.md` | `modules/*` contract package と依存方向の既存方針。 |

## 不変条件

Ver0.80 のモジュール化では、次を壊してはいけない。

- LINE / 外部 channel の既定入口は `CHAT(Mio)`。
- Viewer 通常チャットは `to=mio|shiro|kuro|midori` だけを送る。Viewer は route / model alias に変換しない。
- Coder は破壊的操作を直接実行しない。実行、採用、ログ記録、安全確認は Worker が担当する。
- LLM / TTS / STT backend は RenCrow module server 経由で呼ぶ。
- `~/.rencrow/config.yaml` が runtime topology の正本であり、repo 内 config は暗黙 fallback ではない。
- STT、TTS、表示本文、口パク、ログを同じ状態契約として扱わない。
- Heartbeat は定期運用、Workstream、Viewer active-control、SSE を区別する。
- 外部情報は discovery と source read と browser evidence を区別する。検索結果だけを確定知識にしない。

## AgentList 本体

人格 Agent は次の 8 体で固定する。

| Agent | 役割 | 主責務 |
| --- | --- | --- |
| Mio | Chat | ユーザー対話、ルーティング判断、結果返却、統合 |
| Shiro | Worker | 実行、ツール呼び出し、patch / command 適用、ログ記録 |
| Aka | Coder1 | 仕様設計、アーキテクチャ設計、方針整理 |
| Ao | Coder2 | 実装、テストコード作成、既存コードへの適合 |
| Gin | Coder3 | 高品質推論、複雑作業、難解な実装、最適化 |
| Kin | Coder4 | 補助 Coder、レビュー、仕上げ、代替案検討 |
| Kuro | Heavy | 深い分析、根本原因調査、最終技術レビュー、安全ゲート |
| Midori | Wild | 創作、画像プロンプト、視覚解釈、横方向探索 |

SuperAgent、Subagent、Heartbeat Worker、BrowserActor、Distributed remote agent、ChatWorker provider role、ToolHarness / DCI 実行ロールは人格 Agent ではない。補助ロールとして扱い、AgentList 本体へ混ぜない。

## 目標フォルダ構成

Ver0.80 の目標は、横断層を保ちながら機能単位の入口を作ることである。

```text
cmd/
  rencrow/
    main.go
    runtime_*.go
    routes.go
    feature_registrars.go     # feature registrar を呼ぶだけに寄せる

modules/
  core/
  agent/
  chat/
  idlechat/
  viewer/
  worker/
  llm/
  tts/
  stt/
  voicechat/
  browseractor/
  webgather/
  ops/
  knowledge/
  security/
  distributed/

internal/
  features/
    agent/
    chat/
    idlechat/
    viewer/
    backlog/
    heartbeat/
    scheduler/
    workstream/
    revenue/
    repair/
    voice/
    web/
    knowledge/
    memory/
    reports/
    governance/
    distributed/
    channels/

  adapter/
    modulebridge/
    viewer/                  # shell / common adapter / static asset を中心に縮小
    channels/
    health/
    config/
    chrome/
    entry/

  domain/
  application/
  infrastructure/
```

## モジュールTree図

Ver0.80 の構成変更は、既存機能を削らず、次の tree に沿って入口と owner を明確にする。

凡例:

- `[process]`: 起動、設定読込、依存注入、HTTP server 起動を担当する process 境界。
- `[contract]`: 公開 contract、DTO、event、純粋 policy、state ownership を置く境界。
- `[feature]`: feature facade、ports、registrar、legacy 実装の束ねを置く境界。
- `[adapter]`: HTTP、Viewer、channel、module bridge など外部接続や互換接続を置く境界。
- `[legacy-body]`: 段階移行中の既存実装本体。削除せず、feature / contract 経由へ順に寄せる。

```text
RenCrow_CORE Ver0.80
├── cmd/rencrow [process]
│   ├── main.go
│   ├── runtime_*.go
│   ├── routes.go
│   └── feature_registrars.go
│       └── internal/features/* の RegisterRoutes / StartBackground を呼ぶ
│
├── modules [contract]
│   ├── core
│   │   ├── manifest
│   │   ├── health
│   │   └── state ownership
│   ├── agent
│   │   └── Agent ID / role / capability / display name
│   ├── chat
│   │   ├── Viewer to=mio|shiro|kuro|midori contract
│   │   ├── route decision
│   │   └── final response contract
│   ├── worker
│   │   ├── proposal / patch
│   │   ├── execution result
│   │   └── failure classification
│   ├── llm
│   │   ├── role provider
│   │   ├── health / diagnostics
│   │   └── runtime selection plan
│   ├── tts
│   │   ├── synthesis contract
│   │   ├── playback state / ACK
│   │   └── chunking / audio event contract
│   ├── stt
│   │   ├── transcription contract
│   │   ├── viewer input observer
│   │   └── websocket plan
│   ├── voicechat
│   │   ├── VDS bridge plan
│   │   ├── runtime URL plan
│   │   └── voice websocket contract
│   ├── browseractor
│   │   ├── browser run request / response
│   │   ├── risk classification
│   │   └── artifact / doctor contract
│   ├── webgather
│   │   ├── discovery / search
│   │   ├── source fetch / extraction
│   │   └── staging contract
│   ├── ops
│   │   └── status / cleanup result / visible-state error
│   ├── knowledge
│   │   └── source registry / import / wiki index contract
│   ├── security
│   │   └── policy result / promotion gate / rollback contract
│   └── distributed
│       └── transport / remote agent availability / delivery contract
│
└── internal
    ├── features [feature]
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
    │
    ├── adapter [adapter]
    │   ├── modulebridge
    │   │   └── 既存実装と modules/* contract の互換接続
    │   ├── viewer
    │   │   └── shell / SSE / common adapter / static asset
    │   ├── channels
    │   │   └── Slack / Discord / Telegram など
    │   ├── line
    │   │   └── LINE webhook / media / sender
    │   ├── config
    │   ├── health
    │   ├── chrome
    │   └── entry
    │
    ├── domain [legacy-body]
    │   └── 外部技術に依存しない値、契約、validation
    ├── application [legacy-body]
    │   └── 既存 usecase / orchestration / background job
    └── infrastructure [legacy-body]
        └── provider / persistence / transport / tool runner / external integration
```

この tree は配置目標であり、即時の削除・移動指示ではない。実体移動は `06_RenCrow_CORE_Ver0.80_モジュール化実装仕様.md` の Phase 9 条件を満たした feature だけで行う。RenCrow_CORE Public repo 起点化でも、`legacy-body` に残る既存機能は削除せず、feature catalog から落とさない。

`internal/features/*` は新しい巨大 service 置き場ではない。各 feature の ports、facade、registrar、feature-local DTO、application glue を置く。既存 `internal/domain`、`internal/application`、`internal/infrastructure` は段階移行中の実体として残してよい。

## フォルダ責務

| フォルダ | 置くもの | 置かないもの |
| --- | --- | --- |
| `cmd/rencrow` | process 起動、config 読込、依存注入、feature registrar 呼び出し、HTTP server 起動 | feature policy、handler 本体、provider 詳細、DB state 遷移 |
| `modules/<id>` | 公開 contract、純粋 policy、DTO、event、state ownership、README、単体テスト | `internal/*` import、HTTP handler、DB 実装、provider HTTP 実行 |
| `internal/features/<id>` | feature facade、ports、registrar、legacy 実装の束ね、feature 単位の依存注入 | provider 固有 HTTP、Viewer static asset の詳細、横断 utility |
| `internal/adapter/modulebridge` | 既存実装と `modules/*` contract の互換アダプタ | module policy、health literal、business DTO の正本 |
| `internal/adapter/viewer` | Viewer shell、SSE hub、共通 adapter、static asset | feature usecase、永続化 state 遷移、LLM/STT/TTS provider |
| `internal/domain` | 外部技術に依存しない値、契約、validation | HTTP、DB、provider、Viewer DOM |
| `internal/application` | 既存 usecase / orchestration。段階移行の実体 | provider HTTP、Viewer static asset、DB schema |
| `internal/infrastructure` | provider、persistence、security、transport、tool runner、external integration | route 決定、Chat 表示 policy、Viewer state contract |

## 依存方向

原則の依存方向は次とする。

```text
cmd/rencrow
  -> internal/features/*
  -> internal/adapter/*
  -> modules/*

internal/features/*
  -> modules/*
  -> internal/domain
  -> internal/application legacy service
  -> internal/infrastructure through ports or explicit construction

modules/*
  -> modules/core only where needed

internal/infrastructure/*
  -> modules/* contract
  -> internal/domain

internal/adapter/viewer
  -> internal/features/* facade or feature handler
  -> modules/* DTO
```

禁止する依存は次である。

- `modules/*` から `cmd/*` または `internal/*` への import。
- `modules/core` から module-specific package への import。
- provider 実装から Viewer 表示 state への直接依存。
- Viewer JS / CSS の都合を application / domain contract に持ち込むこと。
- Worker 実行経路から STT / TTS / Viewer state を直接所有すること。
- Chat / IdleChat の表示本文を TTS chunk だけから復元すること。
- `service` / `manager` / `helper` / `util` という名前で新しい依存集中点を作ること。

## Feature Module Catalog

Ver0.80 で落としてはいけない機能は次である。

| Feature | Module / Feature root | 現在の主な実体 | 最初に固定する contract |
| --- | --- | --- | --- |
| Core | `modules/core`, `internal/features/core` | `modules/core`, `cmd/rencrow/module_*` | manifest、health、state ownership |
| Agent | `modules/agent`, `internal/features/agent` | `internal/domain/agent`, `workspace/persona`, `docs/01_理解/02_キャラクター・エージェント仕様.md` | Agent ID、role、capability、display name |
| Chat | `modules/chat`, `internal/features/chat` | `modules/chat`, `internal/application/orchestrator`, `internal/adapter/viewer/handler_send.go` | `to` 解決、route policy、final response |
| Worker / Coder | `modules/worker`, `internal/features/worker` | `modules/worker`, `internal/application/service/worker_execution_*` | proposal、patch、execution result、failure classification |
| IdleChat | `modules/idlechat`, `internal/features/idlechat` | `internal/application/idlechat`, `modules/chat`, `modules/tts` | session lifecycle、topic、speaker、stop、TTS trigger |
| Viewer | `modules/viewer`, `internal/features/viewer` | `internal/adapter/viewer`, `cmd/rencrow/routes.go` | shell、SSE event、tab API、visible-state error |
| LLM | `modules/llm`, `internal/features/llm` | `modules/llm`, `internal/infrastructure/llm`, `cmd/rencrow/runtime_llm_*` | role provider、health、diagnostics、runtime selection |
| STT | `modules/stt`, `internal/features/stt` | `modules/stt`, `internal/infrastructure/stt`, `cmd/rencrow/stt_*` | transcription、viewer input、websocket plan |
| TTS | `modules/tts`, `internal/features/tts` | `modules/tts`, `internal/infrastructure/tts`, `cmd/rencrow/tts_*` | synthesis、playback state、ACK、chunking |
| VoiceChat | `modules/voicechat`, `internal/features/voice` | `modules/voicechat`, `cmd/rencrow/voice_chat_*` | VDS bridge、runtime URL、websocket plan |
| Avatar | `internal/features/avatar` | `internal/infrastructure/vtuber`, Viewer assets | emotion、lipsync trigger、character runtime display |
| Backlog | `internal/features/backlog` | `internal/domain/backlog`, `internal/adapter/viewer/backlog_*`, `cmd/rencrow/runtime_dependencies.go` | intake item、runner、status |
| Heartbeat | `internal/features/heartbeat` | `internal/application/heartbeat`, `cmd/rencrow/runtime_heartbeat.go` | due run、workstream trigger、draft report |
| Scheduler | `internal/features/scheduler` | `internal/application/scheduler`, `internal/domain/scheduler`, `internal/infrastructure/persistence/scheduler` | due job、run log、status |
| Workstream | `internal/features/workstream` | `internal/domain/workstream`, `internal/infrastructure/persistence/workstream` | goal、artifact、steering、vault update |
| Revenue | `internal/features/revenue` | `internal/application/revenue`, `internal/domain/revenue`, `internal/infrastructure/persistence/revenue` | daily routine、draft、human gate |
| Repair | `internal/features/repair` | `internal/application/autonomous`, Viewer repair handlers | out-of-band repair request、job event |
| Web | `modules/browseractor`, `modules/webgather`, `internal/features/web` | `modules/browseractor`, `modules/webgather`, `internal/application/webgather`, `internal/application/browsertrace` | discovery、fetch、browser evidence |
| Source / Knowledge | `internal/features/source`, `internal/features/knowledge` | `internal/application/sourcefetcher`, `internal/application/knowledge`, `internal/application/knowledgememory` | source registry、import、review、wiki index |
| Memory | `internal/features/memory` | `internal/domain/memory`, `internal/domain/conversation`, persistence conversation stores | observed/candidate/validated/promoted state |
| Reports / Evidence | `internal/features/reports` | `internal/application/verification`, `internal/domain/verification`, evidence CLI | evidence item、summary、status |
| Security / Sandbox | `modules/security`, `internal/features/security`, `internal/features/sandbox` | `internal/domain/security`, `internal/infrastructure/security`, `internal/application/sandbox` | policy result、promotion gate、rollback |
| Governance | `internal/features/governance` | `internal/application/skillgovernance`, `internal/domain/skillgovernance` | trigger log、change gate、external PR audit |
| SuperAgent / AI Workflow | `internal/features/superagent`, `internal/features/aiworkflow` | `internal/application/superagent`, `internal/application/aiworkflow` | run queue、subagent task、trace event |
| Distributed | `modules/distributed`, `internal/features/distributed` | `cmd/rencrow-agent`, `internal/domain/transport`, `internal/infrastructure/transport` | transport, remote agent availability, delivery |
| Channels | `internal/features/channels` | `internal/adapter/line`, `internal/adapter/channels/*`, `internal/adapter/entry` | inbound envelope、channel policy、response adapter |
| Ops / Maintenance | `internal/features/ops` | health, doctor, package validation, artifact cleanup, history repair, OTEL export | status, cleanup result, visible-state error |

`modules/<id>` をすぐ増やせない機能は、まず `internal/features/<id>` で facade / registrar / ports を作る。純粋 policy が固まった時点で `modules/<id>` へ昇格する。

## 状態所有ルール

| 状態 | Owner | 非 owner |
| --- | --- | --- |
| ユーザー向け最終表示本文 | Chat / Viewer | TTS, STT, LLM provider |
| Worker 実行結果 | Worker | Chat, Viewer |
| Coder proposal / patch | Coder contract / Worker handoff | Chat, Viewer |
| Agent identity / role | Agent | Viewer static text |
| Runtime endpoint / topology | Runtime Config / Core | Viewer, provider implementation |
| LLM provider health | LLM | Chat, Viewer |
| STT transcript before acceptance | STT | Chat history |
| accepted transcript | Chat | STT provider |
| TTS synthesis result | TTS | Chat history |
| playback ACK / active audio owner | TTS playback state / Viewer integration | TTS provider |
| Backlog item | Backlog | Heartbeat |
| Heartbeat execution log | Heartbeat | Viewer display |
| Workstream artifact / steering | Workstream | Heartbeat |
| Source Registry validation state | Source / Knowledge | Chat prompt injection |
| Sandbox promotion decision | Security / Sandbox | Worker execution only |

## Feature Registrar

各 feature は HTTP route、background job、Viewer API を直接 `cmd/rencrow` に積み増ししない。

標準形は次とする。

```go
// internal/features/<feature>/registrar.go
type Dependencies struct {
    // feature が必要とする ports / stores / services だけを書く
}

func RegisterRoutes(mux *http.ServeMux, deps Dependencies)
func StartBackground(ctx context.Context, deps Dependencies) error
```

`cmd/rencrow` は `Dependencies` を構築し、`RegisterRoutes` と `StartBackground` を呼ぶだけに寄せる。feature 固有の policy、DTO、store state transition は `cmd/rencrow` に置かない。

現ブランチの Ver0.80 seed では、HTTP route 登録は `internal/features/*/registrar.go` へ寄せた状態を正とする。ただし handler 本体、provider、store、CLI、background job、module endpoint は既存位置に残る legacy-body であり、削除対象ではない。

## 完了条件

Ver0.80 のモジュール構成は、次を満たした時点で完了とする。

- `go test ./...` と `go vet ./...` が通る。
- `modules/dependency_rules_test.go` が現存する全 `modules/*` を対象にする。
- 現存する全 `modules/*` に README がある。
- `cmd/rencrow` に feature 固有 policy が増えず、feature registrar 呼び出しへ寄っている。
- Viewer 通常チャットは `to=mio|shiro|kuro|midori` を送る contract test を持つ。
- `model_alias` / `route_prefix` は通常 Chat の新経路では legacy 互換として隔離される。
- Backlog、Heartbeat、Scheduler、Workstream、Revenue、VoiceChat、BrowserActor、WebGather、Source Registry、Knowledge、Reports、Security、SuperAgent、Distributed、Channels が機能一覧から落ちていない。
- 新しい feature は、入力、出力、副作用、永続化、ログ、エラー契約、テストを持つ。
