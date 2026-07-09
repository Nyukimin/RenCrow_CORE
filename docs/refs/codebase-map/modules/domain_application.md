---
generated_at: "2026-07-01T13:19:25+09:00"
run_id: run_20260701_131925
phase: 2
step: "10"
profile: RenCrow_CORE
artifact: module
module_group_id: domain_application
---

## 概要

`domain_application`はRenCrowの判断・会話・実行・記憶・job制御の中心である。`internal/domain`が値・契約・役割モデルを持ち、`internal/application`がorchestratorやfeature serviceとしてuse caseを組み立てる。

## 関連ドキュメント

- [../アーキテクチャ総合.md](../アーキテクチャ総合.md)
- [../結合ポイントマップ.md](../結合ポイントマップ.md)
- [../ユースケース逆引き.md](../ユースケース逆引き.md)

## モジュール名: Domain / Application層

### 役割と責務（Why）

- Chat/Worker/Coder/Heavy/Wildの役割、routing decision、task、proposal、execution report、memoryなどの中核概念を定義する。
- `MessageOrchestrator`と`DistributedOrchestrator`が、入力をroutingし、必要なagent/provider/tool/storeへ委譲する。
- Viewerや外部channelに依存せず、アプリケーションの業務フローを提供する。

### ナビゲーション

| ファイル/ディレクトリ | 役割 | 読むべき場面 |
|---|---|---|
| `internal/domain/agent/` | Mio/Shiro/Coder/Heavy/Wildとpersona/memory補助 | 役割ごとの責務や出力契約を見る時 |
| `internal/domain/routing/` | route/category/decisionの値 | 分類と委譲条件を見る時 |
| `internal/application/orchestrator/message_orchestrator*.go` | 通常会話経路、slash command、routing、session、TTS/VTuber連携 | Viewer/entryから会話がどう処理されるか追う時 |
| `internal/application/orchestrator/distributed_orchestrator*.go` | 分散/parallel/agent transport経路 | multi-agent実行やremote agentを見る時 |
| `internal/application/idlechat/` | IdleChat、forecast、story、topic、quality/watchdog | IdleChatの開始/停止/会話品質を見る時 |
| `internal/application/knowledge*` | Knowledge Wiki / knowledge memory | Wiki indexや知識記憶を扱う時 |
| `internal/application/*` | featureごとのservice | Viewer dynamic routeの裏側を見る時 |

### モジュール間の関係

- **依存先**: `internal/application/orchestrator` -> `internal/domain/agent/routing/task/session`。会話と実行の契約をdomainから受ける。
- **依存先**: `internal/application/*` -> `internal/domain/*`。feature serviceはdomain entity/valueを入出力に使う。
- **依存元**: `cmd/rencrow` -> orchestrator/service constructors。runtime配線はcmd側で行う。
- **依存元**: `internal/adapter/viewer` -> application service/store interface。Viewer handlerは薄いHTTP adapterとして呼び出す。
- **依存先**: infrastructure interfaceはdomain/application側では抽象に寄せ、実装は`internal/infrastructure`で受ける。

### 大関数の構造マップ（50行超の関数のみ）

| 関数名 | 行数 | 構造 | 行範囲の目安 |
|---|---:|---|---|
| `NewMessageOrchestrator()` | 100行級 | collaborators/options/defaultsを束ね、通常会話runtimeを初期化 | `internal/application/orchestrator/message_orchestrator.go:211`以降 |
| `NewDistributedOrchestrator()` | 100行級 | transport/router/agents/listeners/optionsを組み立てる | `internal/application/orchestrator/distributed_orchestrator_constructor.go:11`以降 |
| `ProcessVoiceDirect`系 | 50行超 | 音声入力を通常会話経路へ直接投入し、TTS/Viewer eventへつなぐ | `internal/application/orchestrator/voice_direct.go` |
| IdleChat orchestrator loop群 | 50行超の複数関数 | topic生成、応答生成、sanitize、loop detection、watchdog | `internal/application/idlechat/orchestrator_*.go` |

### 落とし穴・注意点

- `MessageOrchestrator`と`DistributedOrchestrator`は似た責務を持つが、通常会話と分散/parallel agent実行で見るべきファイルが分かれる。
- `internal/application`はfeature数が多く、Viewerタブの追加に合わせてserviceが増えている。横断修正では対象featureを絞らないと影響範囲が膨らむ。
- `application/`と`domain/`直下にもvocabulary系の旧/別系統packageがある。`internal/*`と同じ階層責務だと即断しない。
- `pkg/agent`系の古いメモリが残っているが、現行`go list`では中核は`internal/domain`/`internal/application`である。

### 設計意図

- 正本仕様のClean Architecture 4層構造に合わせ、domainはHTTP/DB/LLM実装を知らず、applicationがuse case単位で組み合わせる。
- Chat/Worker/Coderの責務分離はdomain agentとorchestratorの両方で守る。Coderの破壊的操作は直接実行ではなくproposal/patch/evidenceの経路で扱う。

### 初期化

- **module_init() 登録**: なし。constructor注入。
- **優先度**: domain entity/value -> application service/orchestrator -> cmd runtime injection。
- **注意点**: memory、TTS、VTuber、job notificationはorchestratorの横断concernとして接続されるため、単一featureだけの変更でもイベント副作用を確認する。

