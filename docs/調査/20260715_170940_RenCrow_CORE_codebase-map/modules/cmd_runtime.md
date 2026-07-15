---
generated_at: "2026-07-15T17:18:00+09:00"
run_id: run_20260715_170940
phase: 2
step: "6-1"
profile: RenCrow_CORE_20260715_refined
artifact: module
module_group_id: cmd_runtime
---

# 起動・CLI・runtime composition

## 概要

RenCrow processの起動、依存注入、route、停止、CLIを所有する。business policyは所有せず、feature/application/infrastructureを接続する境界である。

## 役割と責務（Why）

- Configからproduction graphを一意に組み立てる。
- server/CLI/remote-agentのprocess lifecycleを所有する。
- feature固有policyやDB state transitionは下位層へ委譲する。

## ナビゲーション

| 読む場面 | path |
| --- | --- |
| 起動・停止 | cmd/rencrow/main.go |
| 全依存 | cmd/rencrow/runtime_dependencies.go |
| API route | cmd/rencrow/routes.go、feature_registrars.go |
| background | runtime_background_jobs.go、runtime_heartbeat.go |
| 外部CLI client | pkg/rencrowclient/client.go |

## モジュール間の関係

~~~mermaid
graph LR
  CMD[cmd_runtime] --> FEAT[feature_facades]
  CMD --> APP[domain_application]
  CMD --> ADP[adapter/modules]
  CMD --> INF[infrastructure]
  CLIENT[RenCrow_CMD or user CLI] --> CMD
~~~

## モジュール内データフロー

~~~mermaid
flowchart TD
  Config --> Factory --> Dependencies --> Registrars --> HTTP
  Dependencies --> Background
  Signal --> Shutdown --> Stores
~~~

## 大関数の構造マップ（50行超の関数のみ）

- buildDependencies: runtime全体のDI。
- registerViewerBaseRoutes / registerOpsRoutes: handler bundle。
- pkg/rencrowclient/client.go: 多数のViewer endpoint methodを単一clientへ集約。

## 状態遷移図

~~~mermaid
stateDiagram-v2
  [*] --> LoadingConfig
  LoadingConfig --> Building : valid
  LoadingConfig --> Failed : invalid
  Building --> Serving
  Serving --> Stopping : SIGTERM or SIGINT
  Stopping --> Stopped
~~~

## 落とし穴・注意点

- composition rootがlegacy-bodyのため、移行はfeature単位で行う。
- server stopはHTTP drainを伴わない。
- repo外Configへ黙ってfallbackしない。

## 設計意図

Ver0.80は非破壊移行を優先し、既存runtimeを残しつつregistrar呼び出しへ寄せる。

## 初期化

~~~mermaid
sequenceDiagram
  participant Main
  participant Config
  participant DI as Dependencies
  participant Feature
  participant Server
  Main->>Config: LoadConfig
  Main->>DI: buildDependencies
  Main->>Feature: registerFeatureRoutes
  Main->>Server: ListenAndServe
~~~

## 関連ドキュメント

- cmd_runtime/ファイル一覧.md
- cmd_runtime/関数一覧.md
- ../アーキテクチャ総合.md
