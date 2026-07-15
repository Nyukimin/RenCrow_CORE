---
generated_at: "2026-07-15T17:18:00+09:00"
run_id: run_20260715_170940
phase: 2
step: "6-2"
profile: RenCrow_CORE_20260715_refined
artifact: module
module_group_id: domain_application
---

# Domain / Application / Glossary

## 概要

RenCrowの意味とuse caseを所有する。Chat/Worker/Coderの責務、route、Memory、Advisor、Heartbeat、Economic Objectiveを外部I/Oから分離する。

## 役割と責務（Why）

- domainは値・validation・policy contractを所有する。
- applicationはorchestrationとuse caseを所有する。
- provider HTTP、DB schema、Viewer DOMは所有しない。

## ナビゲーション

| 目的 | path |
| --- | --- |
| 会話全体 | internal/application/orchestrator |
| Agent | internal/domain/agent |
| Memory | internal/domain/conversation、application/knowledge* |
| IdleChat | internal/application/idlechat |
| 定期運用 | internal/application/heartbeat |
| Advisor/Profile | application/advisor、agentprofile |
| Revenue | domain/revenue、application/revenue |

## モジュール間の関係

~~~mermaid
graph LR
  ADAPTER --> APP[domain_application]
  APP --> DOMAIN[domain contracts]
  APP --> MODULES[public contracts]
  APP --> PORTS[infrastructure ports]
  CMD --> APP
~~~

## モジュール内データフロー

~~~mermaid
flowchart TD
  Input --> Orchestrator --> Route --> Agent
  Memory --> RecallPack --> Agent
  Agent --> ProposalOrResult --> Verification --> Final
  Heartbeat --> DraftJobs --> Worker
~~~

## 大関数の構造マップ（50行超の関数のみ）

- MessageOrchestrator.ProcessMessage
- HeartbeatService.tick / RunBacklogIntake / RunDueWorkstreamHeartbeats
- RecallPack budget/filter/projection
- IdleChat session orchestration

## 状態遷移図

~~~mermaid
stateDiagram-v2
  [*] --> Idle
  Idle --> Routing : input
  Routing --> Chatting
  Routing --> Working
  Working --> AwaitingApproval : high risk
  Working --> Completed
  AwaitingApproval --> Working : granted
  AwaitingApproval --> Cancelled : denied
  Chatting --> Completed
~~~

## 落とし穴・注意点

- Worker実行とShiro通常会話を分離する。
- Coder/Advisorはproposal/adviceであり直接副作用を持たない。
- To-Be typeが存在してもproduction wiring完了とは限らない。

## 設計意図

Clean Architectureを段階採用し、legacy-bodyを削除せずcontractから順に固定している。

## 初期化

~~~mermaid
sequenceDiagram
  participant CMD
  participant Store
  participant Service
  participant Agent
  participant Orch
  CMD->>Store: build stores
  CMD->>Service: construct use cases
  CMD->>Agent: inject tools/advisor/memory
  CMD->>Orch: inject agents and recorders
~~~

## 関連ドキュメント

- domain_application/ファイル解析.md
- ../アーキテクチャ総合.md
- ../ユースケース逆引き.md
