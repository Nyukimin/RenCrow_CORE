---
generated_at: "2026-07-15T17:22:00+09:00"
run_id: run_20260715_170940
phase: 2
step: "6-4"
profile: RenCrow_CORE_20260715_refined
artifact: module
module_group_id: feature_facades
---

# Feature facade / registrar

## 概要

Ver0.80の段階移行境界。feature単位のports、route、background ownerを小さく定義し、legacy-bodyを非破壊で束ねる。

## 役割と責務（Why）

- cmdへfeature固有path/policyを積み増さない。
- modules contractとlegacy application/adapterを接続する。
- provider詳細やDB state transitionを置かない。

## ナビゲーション

| 状態 | 読み方 |
| --- | --- |
| active registrar | 現在のroute ownership |
| no-op registrar | migration未完了の予約境界 |
| ports.go | featureが必要とする依存 |
| registrar_test.go | path contract |

## モジュール間の関係

~~~mermaid
graph LR
  CMD --> FEATURE
  FEATURE --> MODULES
  FEATURE --> ADAPTER
  FEATURE --> APPLICATION
  FEATURE -.ports.-> INFRA
~~~

## モジュール内データフロー

~~~mermaid
flowchart TD
  Dependencies --> Registrar --> Route --> Handler --> UseCase
~~~

## 大関数の構造マップ（50行超の関数のみ）

ops registrarが最大で、複数featureのroute bundleを持つ。ほかは小さな宣言的registrar。

## 状態遷移図

~~~mermaid
stateDiagram-v2
  [*] --> Reserved
  Reserved --> FacadeOnly : ports and registrar
  FacadeOnly --> Routed : cmd calls registrar
  Routed --> Implemented : usecase and tests connected
~~~

## 落とし穴・注意点

- implemented / facade_only / legacy_bodyを単一booleanに潰さない。
- no-opを機能欠落と即断せず台帳とruntime routeを併読する。
- Revenue/Agent/Heartbeat等はroute/background ownerの移行が未完了。

## 設計意図

大規模移動を避け、contractとregistration boundaryから固定する。

## 初期化

~~~mermaid
sequenceDiagram
  participant CMD
  participant Feature
  participant Mux
  CMD->>Feature: RegisterRoutes(deps)
  Feature->>Mux: Handle(path, handler)
~~~

## 関連ドキュメント

- feature_facades/ファイル解析.md
- ../ギャップ分析.md
