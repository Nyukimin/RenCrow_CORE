---
generated_at: "2026-07-15T17:26:00+09:00"
run_id: run_20260715_170940
phase: 2
step: "6-5"
profile: RenCrow_CORE_20260715_refined
artifact: module
module_group_id: infrastructure_persistence
---

# Infrastructure / Persistence / provider

## 概要

外部副作用と永続化を所有する。LLM/STT/TTS/Web/transport/ToolRunnerをdomain contractへ変換し、監査可能なstoreへ保存する。

## 役割と責務（Why）

- network、DB、filesystem、subprocessを隔離する。
- timeout、size、secret、sandbox等のboundary guardを適用する。
- routeや表示policyを所有しない。

## ナビゲーション

| 目的 | path |
| --- | --- |
| Tool | internal/infrastructure/tools |
| LLM | internal/infrastructure/llm |
| Memory DB | persistence/conversation |
| feature DB | persistence/advisor、revenue、workstream等 |
| remote | transport |
| web | webgather、browseractor |

## モジュール間の関係

~~~mermaid
graph LR
  Application --> Infrastructure
  Infrastructure --> Domain
  Infrastructure --> Modules
  Infrastructure --> External
  CMD --> Infrastructure
~~~

## モジュール内データフロー

~~~mermaid
flowchart TD
  PortCall --> Validate --> TimeoutOrPolicy --> IO --> Parse --> DomainResult --> Audit
~~~

## 大関数の構造マップ（50行超の関数のみ）

- persistence schema/migrationとmulti-store query。
- provider request/stream parser。
- browser/webwright artifact pipeline。

## 状態遷移図

~~~mermaid
stateDiagram-v2
  [*] --> Requested
  Requested --> Allowed : policy pass
  Requested --> Denied : policy fail
  Allowed --> Running
  Running --> Persisted : success
  Running --> Failed
~~~

## 落とし穴・注意点

- 外部module本体の責務をCORE providerへ取り込まない。
- store追加時はclose、migration、test、auditを同時に確認する。
- streamingはclient timeoutなしでもcontext cancellationを必須にする。

## 設計意図

副作用を明示portの背後へ置き、Worker/approval/sandboxから制御可能にする。

## 初期化

~~~mermaid
sequenceDiagram
  participant CMD
  participant Config
  participant Store
  participant Provider
  participant App
  CMD->>Config: resolve endpoint and secret refs
  CMD->>Store: open
  CMD->>Provider: construct clients
  CMD->>App: inject ports
~~~

## 関連ドキュメント

- infrastructure_persistence/ファイル解析.md
- ../リスク分析.md
