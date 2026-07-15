---
generated_at: "2026-07-15T17:09:40+09:00"
run_id: run_20260715_170940
phase: 1
step: "1"
profile: RenCrow_CORE_20260715_refined
artifact: overview_draft
---

# RenCrow_CORE 全体概要ドラフト

## 概要

RenCrow_COREは、HTTP/Viewer/外部channelを入口に、Chat・Worker・Coder・Advisorをrouteし、LLM/STT/TTS/Tools/Memoryを接続するGo中心のruntimeである。Ver0.80ではmodules contract、feature facade、legacy-bodyを段階的に分離している。

## プロジェクト情報

| 項目 | 値 |
| --- | --- |
| module | github.com/Nyukimin/RenCrow_CORE |
| Go toolchain | go1.26.2（go.modはGo module正本） |
| packages | 212 |
| Go files | 1,490、256,578行（test含む単純wc） |
| Viewer JS | 32 files、18,709行 |
| Python | 68 files、18,655行 |
| 主entry | cmd/rencrow/main.go |
| 補助entry | cmd/rencrow-agent、kb-admin、glossary、test CLI群 |

## ディレクトリ構成

~~~mermaid
graph TD
  ROOT[RenCrow_CORE] --> CMD[cmd/rencrow process]
  ROOT --> MOD[modules public contracts]
  ROOT --> FEAT[internal/features facade]
  ROOT --> ADP[internal/adapter HTTP and channel]
  ROOT --> APP[internal/application use cases]
  ROOT --> DOM[internal/domain contracts]
  ROOT --> INF[internal/infrastructure providers and stores]
  ROOT --> DATA[rencrow-data Python CLI]
  ROOT --> DOC[docs canonical specs]
  CMD --> FEAT
  FEAT --> APP
  FEAT --> ADP
  APP --> DOM
  ADP --> APP
  INF --> DOM
  APP --> INF
  MOD --> MOD
~~~

## エントリポイントと初期化フロー

~~~mermaid
flowchart TD
  MAIN[main] --> DISPATCH{CLI command}
  DISPATCH -->|run| LOAD[LoadConfig]
  LOAD --> BUILD[buildDependencies]
  BUILD --> RUNTIME[LLM Tools Memory Advisor Stores]
  RUNTIME --> ROUTES[registerFeatureRoutes]
  ROUTES --> SERVER[http.Server ListenAndServe]
  DISPATCH -->|status doctor chat etc| CLI[CLI facade]
  SIGNAL[SIGTERM or SIGINT] --> STOP[Dependencies.Shutdown]
~~~

## 初期アーキテクチャ仮説

- modulesは公開contractとpure policy、internal/featuresはroute/facade、legacy-bodyはapplication/adapter/infrastructureに残る。
- cmd/rencrowはcomposition rootだが、runtime_dependencies.goとroutes.goに多くのfeature wiringが集中する。
- Viewer SSEとMonitorStoreが観測面、conversation managerとL1 SQLiteがMemory面、ToolRunnerが副作用境界を担う。
- rencrow-dataは同repo内の独立Python pipelineで、Viewer investment表示からread-onlyに参照される。

## 関連ドキュメント

- モジュール一覧.md
- アーキテクチャ総合.md
- refs_mapping.md
