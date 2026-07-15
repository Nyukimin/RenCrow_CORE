---
generated_at: "2026-07-15T17:26:00+09:00"
run_id: run_20260715_170940
phase: 2
step: "6-6"
profile: RenCrow_CORE_20260715_refined
artifact: module
module_group_id: docs_config_data
---

# Canonical docs / Config / operational data

## 概要

実装の意味、runtime入力、運用データを所有する非Go surface。正本・Config・prompt・schema・Python pipelineを区別する。

## 役割と責務（Why）

- docs/02_正本仕様がbehavior/implementationの意味を所有する。
- Configはendpointとfeature flagを選ぶ。
- rencrow-dataは再現可能なresearch/audit pipelineを所有する。

## ナビゲーション

| 目的 | path |
| --- | --- |
| 正本選択 | docs/02_正本仕様/00_正本仕様Tree.md |
| 現行実装 | docs/02_正本仕様/02_実装仕様.md |
| runtime topology | 03_Runtime_Config.md |
| module移行 | 05、06、12 |
| 投資pipeline | rencrow-data/README.md、src |

## モジュール間の関係

~~~mermaid
graph LR
  Docs --> Developer
  Config --> Runtime
  Prompts --> Agents
  Schemas --> Validators
  DataCLI --> SQLite
  SQLite --> Viewer
~~~

## モジュール内データフロー

~~~mermaid
flowchart TD
  Spec --> Implementation --> Test --> RuntimeEvidence
  SourceData --> Validation --> Snapshot --> Risk --> Approval --> Audit
~~~

## 大関数の構造マップ（50行超の関数のみ）

rencrow-dataのaudit/backtest/dbが大きい。docsはcanonical indexから責務別子文書へ分割済み。

## 状態遷移図

~~~mermaid
stateDiagram-v2
  [*] --> Draft
  Draft --> Canonical : reviewed and indexed
  Canonical --> Updated : behavior changes
  Updated --> Canonical : code test docs synchronized
~~~

## 落とし穴・注意点

- Config sampleを実運用fallbackにしない。
- generated outputとsource dataを混同しない。
- 未コミット正本変更は履歴判定上、HEADより新しいworktree stateとして扱う。

## 設計意図

仕様、入力、運用証跡をrepo-native assetとして残し、後続agentが再利用できるようにする。

## 初期化

~~~mermaid
sequenceDiagram
  participant User
  participant Config
  participant Runtime
  participant Data
  User->>Config: select RENCROW_CONFIG
  Runtime->>Config: load and validate
  Runtime->>Data: open configured stores
~~~

## 関連ドキュメント

- docs_config_data/ファイル解析.md
- ../refs_mapping.md
- ../トレーサビリティマトリクス.md
