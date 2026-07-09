---
generated_at: "2026-07-01T13:19:25+09:00"
run_id: run_20260701_131925
phase: 2
step: "10"
profile: RenCrow_CORE
artifact: module
module_group_id: cmd_runtime
---

## 概要

`cmd_runtime`は`rencrow`プロセスの入口、HTTP routeの集約、runtime dependencyの組み立てを担う。実装上は`cmd/rencrow/main.go`と`cmd/rencrow/routes.go`が全体の交通整理を行い、各featureの実処理は`internal/application`、`internal/adapter/viewer`、`modules/*`へ委譲される。

## 関連ドキュメント

- [../アーキテクチャ総合.md](../アーキテクチャ総合.md)
- [../結合ポイントマップ.md](../結合ポイントマップ.md)
- [../ユースケース逆引き.md](../ユースケース逆引き.md)

## モジュール名: 起動・HTTPルーティング・runtime配線

### 役割と責務（Why）

- `rencrow`バイナリの起動、config読込、HTTP server公開、runtime依存注入の境界を提供する。
- Viewer / STT / VoiceChat / entry / chrome bridge / health / module routesを1つの`http.ServeMux`へ登録する。
- 個別featureの業務ロジックは保持せず、依存構築とroute配線に寄せる。

### ナビゲーション

| ファイル | 役割 | 読むべき場面 |
|---|---|---|
| `cmd/rencrow/main.go` | `main`、config path、server起動、local pprof guard | 起動順、port、pprof公開条件を確認する時 |
| `cmd/rencrow/routes.go` | `/viewer/*`、`/health`、`/entry`、IdleChatなどのroute登録 | APIがどのhandlerへ届くか調べる時 |
| `cmd/rencrow/runtime_*.go` | LLM provider、Viewer handlers、background jobs、STT/TTS/voice runtimeの組み立て | featureがどのstore/serviceを使うか追う時 |
| `cmd/rencrow/module_routes.go` | `modules/*` bridge APIの登録 | module surfaceと本体runtimeの接続を見る時 |
| `cmd/rencrow-agent/` | 分散実行用agent standalone entry | remote worker/agent側を確認する時 |
| `pkg/rencrowclient/` | 外部/CLI facade向けHTTP client | `RenCrow_CMD`や他clientとのHTTP契約を見る時 |

### モジュール間の関係

- **依存先**: `cmd/rencrow` -> `internal/adapter/viewer`。`routes.go`がViewer handlerを大量登録する。
- **依存先**: `cmd/rencrow` -> `internal/application/orchestrator`。会話・repair・voice directなどのruntime入口を接続する。
- **依存先**: `cmd/rencrow` -> `internal/infrastructure/persistence/*`。Viewer用status/evidence/memory/storeを構築する。
- **依存先**: `cmd/rencrow` -> `modules/*`。STT/TTS/Chat/Worker/WebGatherのmodule bridge routeを登録する。
- **依存元**: `RenCrow_CMD`など外部client -> `pkg/rencrowclient` / `/viewer/*` HTTP API。仕様責任はこのrepo側にある。

### 大関数の構造マップ（50行超の関数のみ）

| 関数名 | 行数 | 構造 | 行範囲の目安 |
|---|---:|---|---|
| `main()` | 180行級 | config読込 -> runtime構築 -> route登録 -> server起動 -> signal待機 | `cmd/rencrow/main.go:27`以降 |
| `registerViewerDynamicRoutes()` | 380行級 | Viewer dynamic endpointsを機能別に連続登録。memory/evidence/sandbox/revenue/persona/browsertrace/superagent/ai-workflow/knowledge-memory等 | `cmd/rencrow/routes.go:140`以降 |
| `startBackgroundJobs()`系 | 100行超の複数関数 | ticker/queue/schedulerを起動しevent hubへ通知 | `cmd/rencrow/runtime_background_jobs.go` |

### 落とし穴・注意点

- `routes.go`はroute一覧の正本に近いが、個別handlerの責務までは持たない。修正時にここへ業務分岐を足すと肥大化しやすい。
- `/viewer/*`は`registerViewerBaseRoutes`と`registerViewerDynamicRoutes`に分かれる。APIの所在確認では両方を見る必要がある。
- systemd運用では`rencrow.service`のWorkingDirectoryが実行repoを決める。service再起動を伴う作業ではAGENTSの停止ルールに従う必要がある。
- `cmd/test-*`は検証用entryであり本番serverの責務とは分けて読む。

### 設計意図

- 起動層は「依存を束ねるcomposition root」として動き、Clean Architectureの内側へHTTP事情を流し込まないために存在する。
- route登録はViewerの増殖に合わせて長くなっているが、handler/store/serviceは別packageに残すことで直接循環を避けている。

### 初期化

- **module_init() 登録**: なし。Goの`main`起点。
- **優先度**: process起動時にconfig -> dependencies -> routes -> HTTP server。
- **注意点**: STT/VoiceChat/LLM/background jobsはconfigとruntime dependencyの両方に依存するため、routeだけを見ても実行可否は判断できない。

