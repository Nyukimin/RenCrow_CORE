# To-Be: GPT-OSS・Runtime Flow

- status: active
- lifecycle: canonical child
- owner: RenCrow_CORE
- parent_spec: `../10_RenCrow_ToBe_統合仕様.md`
- source_spec: `../10_RenCrow_ToBe_統合仕様.md`の2026-07-15分割前章
- last_reviewed: 2026-07-15
- scope: GPT-OSS-120Bの位置づけ、runtime flow、関連実装文書

## 10. GPT-OSS-120B の位置づけ

GPT-OSS-120B は人格 Agent ではなく、Worker / Coder / Advisor の裏側で使われる model capability とする。

```text
Mio
  user-facing route / report

Shiro
  execution owner

GPT-OSS-120B Worker context
  opportunity analysis / planning / production

GPT-OSS-120B Coder context
  tool implementation / automation / productization

Kuro
  risk / profitability / contract / quality audit
```

同じ model でも context、role、permission、prompt を分ける。

## 11. Runtime flow

### 11.1 通常開発タスク

```text
User
  ↓
Mio
  ↓
Shiro
  ↓
Need code advice?
  ↓ yes
AdvisorService -> Codex
  ↓
AdviceResult(plan, patch, tests, risks)
  ↓
Shiro decision
  ↓
Worker execution
  ↓
Tests / logs
  ↓
Mio final report
```

### 11.2 Knowledge recall

```text
User query
  ↓
Mio / Worker detects recall need
  ↓
Exact / tag / entity search
  ↓
Relation 1-2 hop
  ↓
Vector fallback only if needed
  ↓
RecallPack budget / rerank
  ↓
Prompt injection with trace
```

### 11.3 Revenue task

```text
Heartbeat / User request
  ↓
Opportunity discovery
  ↓
Economic evaluation
  ↓
Draft task / Workstream
  ↓
Artifact
  ↓
Human Approval Gate
  ↓
Delivery / Publish
  ↓
Revenue Event
  ↓
Reflection
```


## 12. 関連する実装文書

- 統合実装仕様: `docs/02_正本仕様/11_RenCrow_ToBe_統合実装仕様.md`
- CORE 機能台帳: `docs/02_正本仕様/12_CORE_機能台帳.md`
- Advisor / AgentProfile 接続: `docs/04_構築指標/03_Advisor_AgentProfile接続実装仕様.md`
- Knowledge Relation 接続: `docs/04_構築指標/04_KnowledgeRelation接続実装仕様.md`
- Economic Objective 接続: `docs/04_構築指標/05_EconomicObjective接続実装仕様.md`
- To-Be Ops 表示: `docs/04_構築指標/06_ToBe_Ops表示実装仕様.md`
