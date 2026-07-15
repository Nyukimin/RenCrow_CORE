---
generated_at: "2026-07-15T17:22:00+09:00"
run_id: run_20260715_170940
phase: 2
step: "6-3"
profile: RenCrow_CORE_20260715_refined
artifact: module
module_group_id: adapter_viewer_channels
---

# Adapter / Viewer / public module contracts

## 概要

外部protocolをCORE内部contractへ変換する境界。Viewer表示、SSE、channel、modules public contract、legacy modulebridgeを所有する。

## 役割と責務（Why）

- HTTP/channel payloadをvalidationしてapplicationへ渡す。
- modulesに公開DTO/pure policyを置く。
- provider実装やbusiness state transitionは所有しない。

## ナビゲーション

| 目的 | path |
| --- | --- |
| Viewer send | internal/adapter/viewer/handler_send.go |
| SSE | hub.go、handler_sse.go |
| projection | monitor*.go |
| channel | internal/adapter/channels、line |
| public contracts | modules/* |
| compatibility | internal/adapter/modulebridge |

## モジュール間の関係

~~~mermaid
graph LR
  User --> Adapter
  Adapter --> Application
  Adapter --> Modules
  Infrastructure --> Modules
  Feature --> Adapter
~~~

## モジュール内データフロー

~~~mermaid
flowchart TD
  Request --> Validate --> DTO --> UseCase --> Event --> Projection --> SSE
~~~

## 大関数の構造マップ（50行超の関数のみ）

- Viewer handler群はinput validationとJSON projectionを担当。
- modules/chat topic_policyは純粋validationを集中。
- modules/ttsはruntime planとchunk/playback contractを細分化。

## 状態遷移図

~~~mermaid
stateDiagram-v2
  [*] --> Accepted
  Accepted --> Processing
  Processing --> Visible
  Processing --> Failed
  Visible --> Replayed : durable event
  Visible --> Transient : audio or live event
~~~

## 落とし穴・注意点

- Viewer JS都合をdomain contractへ逆流させない。
- event historyとdurable audit logは別。
- external module本体の正本をCOREへ複製しない。

## 設計意図

modules contractを安定点にし、Viewer/legacy runtimeの移行を段階化する。

## 初期化

~~~mermaid
sequenceDiagram
  participant CMD
  participant Feature
  participant Adapter
  participant Hub
  CMD->>Hub: NewEventHub
  CMD->>Adapter: build handlers
  CMD->>Feature: RegisterRoutes
  Feature->>Adapter: bind handler
~~~

## 関連ドキュメント

- adapter_viewer_channels/ファイル解析.md
- ../結合ポイントマップ.md
