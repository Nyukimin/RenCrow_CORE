# To-Be Ops 表示実装仕様

- status: active
- document_type: implementation_spec
- path: `docs/04_構築指標/06_ToBe_Ops表示実装仕様.md`
- source_spec: `docs/02_正本仕様/10_RenCrow_ToBe_統合仕様.md`
- last_reviewed: 2026-07-14

## 1. 目的

本仕様は、Advisor / AgentProfile / Knowledge Relation / Economic Objective の状態を Viewer / Ops から確認できるようにするための表示・API・client・E2E 実装仕様である。

目的は、内部状態を人間が安全に確認できることに限定する。Viewer から外部公開、Advisor 実行、請求、Relation full backfill などの副作用を直接起動しない。

## 2. 対象

表示対象:

```text
Advisor runs
Advisor scores
Agent profiles
Agent policy decisions
Knowledge relation summary
Knowledge relation recall trace
Opportunities
Economic tasks
Approval queue
Economic reflections
```

既存表示と接続する対象:

```text
/viewer/revenue
/viewer/recall/traces
/viewer/debug/system
/viewer/status
Viewer Ops tab
pkg/rencrowclient
```

## 3. 表示原則

### 3.1 初期表示

初期表示は要約だけにする。

```text
1. Advisor / Agent
2. Knowledge Relation
3. Economic Objective
4. Approval Queue
5. Recent Trace
```

各 block は 3-5 件の summary に絞る。

### 3.2 details に閉じるもの

- raw trace
- long summary
- error detail
- score calculation detail
- relation evidence
- run/adoption records

### 3.3 表示してはいけないもの

- Advisor prompt 本文
- Advisor raw output 本文
- secret / token
- personal data の生値
- external channel destination の詳細
- 未承認の外部送信用本文

## 4. API 境界

本仕様の Viewer は read-first とする。

read-only:

```text
GET /viewer/advisors
GET /viewer/advisors/runs
GET /viewer/advisors/scores
GET /viewer/agents/profiles
GET /viewer/agents/policy-decisions
GET /viewer/knowledge-relations/summary
GET /viewer/knowledge-relations
GET /viewer/revenue/opportunities
GET /viewer/revenue/economic-tasks
GET /viewer/revenue/economic-reflections
```

既存 write API:

```text
POST /viewer/revenue/human-decision-gate/review
```

本仕様で新規に write API を追加する場合は、`05_EconomicObjective接続実装仕様.md` の Opportunity / EconomicTask / EconomicReflection 作成に限定する。

## 5. Ops summary response

`/viewer/status` または既存 Ops summary に追加する場合の read model:

```json
{
  "to_be": {
    "advisor": {
      "enabled": true,
      "advisor_count": 1,
      "recent_run_count": 3,
      "failed_run_count": 0,
      "score_snapshot_count": 1
    },
    "agent_profile": {
      "profile_count": 8,
      "policy_decision_count": 5,
      "forbidden_decision_count": 0
    },
    "knowledge_relation": {
      "enabled": false,
      "entity_count": 0,
      "relation_count": 0,
      "max_hops": 2,
      "last_build_status": "not_run"
    },
    "economic_objective": {
      "enabled": false,
      "draft_only": true,
      "opportunity_count": 0,
      "pending_approval_task_count": 0,
      "reflection_count": 0
    }
  }
}
```

欠損時の扱い:

- store 未設定は `enabled=false` または `status=unavailable`
- 500 にしない
- reason を `warnings` に入れる

## 6. Viewer UI

### 6.1 追加位置

既存 Ops / System 系タブに `To-Be` section を追加する。

新規 top-level tab は作らない。理由は、運用確認のための状態であり日常作業の主画面ではないため。

### 6.2 Layout

desktop:

```text
To-Be Summary
├─ Advisor / Agent
├─ Knowledge Relation
├─ Economic Objective
├─ Approval Queue
└─ Recent Trace
```

mobile:

```text
1 column
cards are full-width
long ids wrap
details are collapsed by default
```

### 6.3 Card rules

- nested card 禁止
- table は初期表示しない
- raw JSON は details に閉じる
- status は `ok` / `warning` / `blocked` / `unavailable` の 4 種に正規化
- count は 0 を明示する
- `enabled=false` はエラー扱いしない

## 7. Client validation

`pkg/rencrowclient` に追加する。

```go
func (c *Client) AdvisorsStatus(ctx context.Context, limit int) (AdvisorsStatus, error)
func (c *Client) AgentProfilesStatus(ctx context.Context) (AgentProfilesStatus, error)
func (c *Client) KnowledgeRelationsStatus(ctx context.Context, limit int) (KnowledgeRelationsStatus, error)
func (c *Client) RevenueOpportunities(ctx context.Context, limit int) (RevenueOpportunitiesStatus, error)
```

validation:

- count は負数不可
- advisor run は `run_id` / `advisor_id` / `status` 必須
- score は 0-1
- agent profile は 8 件未満なら warning
- relation `hop` は 1 または 2
- economic task の approval-required kind は `approval_mode=human_required`
- external action applied を claimed する record は post verification evidence 必須

## 8. Browser verification

Viewer UI を変更する場合は browser 確認を必須にする。

確認幅:

```text
desktop: 1440x900
mobile: 390x844
```

確認項目:

- 初期表示が要約 3-5 block に収まる
- details は閉じている
- long id / URL / error が横にはみ出さない
- button / details / approval UI が重ならない
- `enabled=false` が赤エラー表示にならない
- prompt / raw output / secret が表示されない

## 9. 実装手順

### Task 1: read-only handlers

対象:

```text
internal/adapter/viewer
cmd/rencrow/routes.go
cmd/rencrow/runtime_dependencies.go
```

確認:

```bash
go test ./internal/adapter/viewer ./cmd/rencrow
```

### Task 2: client DTO / validation

対象:

```text
pkg/rencrowclient
```

確認:

```bash
go test ./pkg/rencrowclient
```

### Task 3: Viewer UI summary

対象:

```text
internal/adapter/viewer
viewer assets used by the current Ops tab
```

確認:

```bash
go test ./internal/adapter/viewer
```

必要に応じて Node / browser test を追加する。

### Task 4: browser E2E

前提:

- live service が既に起動している場合のみ read-only 確認する。
- service restart は本仕様の作業に含めない。

確認:

```text
GET /viewer/status
GET /viewer/revenue
GET /viewer/recall/traces
```

ブラウザ:

- desktop screenshot
- mobile screenshot
- computed style で overflow / pointer-events / z-index を確認

## 10. 失敗系テスト

必須:

- malformed advisor status を client が reject
- advisor raw output が response に含まれたら reject
- relation hop=3 を client が reject
- negative count を reject
- economic task の approval mismatch を reject
- store unavailable は API 500 ではなく warning response
- mobile 幅で long id が layout overflow しない

## 11. 完了条件

- Ops から To-Be state を要約確認できる。
- raw trace / raw output は初期表示されない。
- Approval queue は pending 件数と対象だけを出す。
- read-only status API が client validation を通る。
- desktop / mobile で表示が崩れない。
- 外部副作用を起こす操作が Viewer から追加されていない。
- `go test ./...` が通る。

## 12. 停止条件

次は別仕様へ切り出す。

- Advisor 実行ボタン
- Relation build 実行ボタン
- Opportunity discovery 実行ボタン
- 実外部送信 / 実公開 / 実請求 UI
- 大規模 graph visualization
