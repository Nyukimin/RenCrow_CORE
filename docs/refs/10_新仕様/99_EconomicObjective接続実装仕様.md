# Economic Objective 接続実装仕様

## 1. 目的

本仕様は、`economic_objective` MVP を Revenue / Workstream / Approval / Heartbeat へ接続し、半自律の収益候補生成を draft-only で運用できる状態にするための実装仕様である。

RenCrow における経済目的は、利益最大化ではなく次を同時に満たす。

```text
net_profit
customer_value
reuse_value
automation_rate
quality
strategic_value
risk / reputation penalty
```

## 2. 現在の実装状態

MVP 実装済み:

```text
internal/domain/revenue.Opportunity
internal/domain/revenue.EconomicTask
internal/domain/revenue.EconomicReflection
internal/application/revenue.EconomicService
internal/infrastructure/persistence/revenue
```

既存実装:

```text
internal/adapter/viewer/revenue_handler.go
internal/application/revenue/daily_routine.go
internal/application/heartbeat
internal/domain/workstream
pkg/rencrowclient
```

未接続:

- Opportunity create/list API
- Opportunity -> Workstream Goal 作成 API
- EconomicTask create/list API
- EconomicReflection create/list API
- RevenueEvent -> Reflection 作成
- Heartbeat による Opportunity discovery draft
- Viewer / Ops 表示

## 3. 実装範囲

### 3.1 含める

- Revenue store interface に Opportunity 系 method を接続
- Viewer API
- rencrowclient
- Opportunity から Workstream Goal を作る service
- approval-required task guard
- Heartbeat draft-only discovery
- RevenueEvent から EconomicReflection を作る service

### 3.2 含めない

- 実外部公開
- 実請求
- 実契約
- 実広告出稿
- 外部サービス登録
- 有料 API の自動利用
- 価格の自動確定

これらは Human approval があっても、本仕様では `audit record` までに留める。

## 4. API

### 4.1 Opportunity

```text
GET  /viewer/revenue/opportunities?limit=50
POST /viewer/revenue/opportunities
```

POST request:

```json
{
  "opportunity_id": "opp_...",
  "source_kind": "note_archive",
  "title": "ローカルLLM技術資料",
  "summary": "...",
  "target_customer": "...",
  "expected_revenue": 3000,
  "expected_cost": 800,
  "reuse_value": 0.7,
  "automation_rate": 0.4,
  "strategic_value": 0.6,
  "risk_score": 0.2,
  "approval_state": "draft"
}
```

response:

```json
{
  "opportunity": {
    "opportunity_id": "opp_...",
    "expected_profit": 2200,
    "profit_margin": 0.7333
  },
  "human_approval_required_for_publish": true
}
```

### 4.2 EconomicTask

```text
GET  /viewer/revenue/economic-tasks?limit=50
POST /viewer/revenue/economic-tasks
```

制約:

- `task_kind` が `external_publish` / `billing` / `contract` / `paid_api_use` / `github_publication` / `personal_data_use` の場合、`approval_mode=human_required` が必須。
- approval が必要な task は作成してよいが、自動実行してはいけない。

### 4.3 EconomicReflection

```text
GET  /viewer/revenue/economic-reflections?limit=50
POST /viewer/revenue/economic-reflections
POST /viewer/revenue/economic-reflections/from-revenue-event
```

`from-revenue-event` request:

```json
{
  "reflection_id": "reflection_...",
  "opportunity_id": "opp_...",
  "revenue_event_id": "rev_...",
  "outcome": "sold",
  "lessons": ["再利用価値が高い"],
  "next_actions": ["テンプレート化する"]
}
```

### 4.4 Opportunity -> Workstream Goal

```text
POST /viewer/revenue/opportunities/workstream-goal
```

request:

```json
{
  "opportunity_id": "opp_...",
  "workstream_id": "ws_revenue"
}
```

要件:

- Opportunity が存在しない場合 404。
- `expected_profit < 0` の Opportunity は high priority goal にしない。
- goal は `draft` status で作る。
- external publish / billing / contract は goal の success criteria に approval gate を含める。

## 5. Store interface

`internal/adapter/viewer/revenue_handler.go` の `RevenueStore` を拡張する。

```go
SaveOpportunity(ctx context.Context, item revenue.Opportunity) error
ListOpportunities(ctx context.Context, limit int) ([]revenue.Opportunity, error)
SaveEconomicTask(ctx context.Context, item revenue.EconomicTask) error
ListEconomicTasks(ctx context.Context, limit int) ([]revenue.EconomicTask, error)
SaveEconomicReflection(ctx context.Context, item revenue.EconomicReflection) error
ListEconomicReflections(ctx context.Context, limit int) ([]revenue.EconomicReflection, error)
```

既存 JSONL / SQLite store はこの method をすでに持つため、Viewer stub / client validation / route wiring を追随させる。

## 6. Application service

### 6.1 OpportunityService

`internal/application/revenue` に追加または既存 `EconomicService` を拡張する。

```go
func (s *EconomicService) DraftOpportunity(ctx context.Context, item revenue.Opportunity) (revenue.Opportunity, error)
func (s *EconomicService) CreateWorkstreamGoal(ctx context.Context, opportunityID, workstreamID string) (workstream.Goal, error)
func (s *EconomicService) ReflectRevenueEvent(ctx context.Context, req ReflectionFromRevenueEventRequest) (revenue.EconomicReflection, error)
```

### 6.2 Human approval connection

EconomicTask 作成時:

1. `revenue.RequiresHumanApproval(task.TaskKind)` を呼ぶ。
2. approval 必須なら `approval_mode=human_required` 以外を reject。
3. 必要なら `HumanDecisionGateRecord` を pending で作る。

初回実装では、HumanDecisionGateRecord 作成は opt-in とし、まず task validation を優先する。

## 7. Heartbeat 接続

config:

```yaml
economic_objective:
  enabled: false
  draft_only: true
  heartbeat_discovery_enabled: false
  daily_opportunity_limit: 5
```

Heartbeat task:

```text
Revenue Opportunity Discovery
```

入力:

- recent MarketResearchItem
- recent Product
- recent CustomerVoice
- recent RevenueEvent
- recent Workstream Goal

出力:

- draft Opportunity
- optional EconomicTask `task_kind=draft_report`

禁止:

- publish
- billing
- contract
- external send
- paid API use

実行条件:

- `enabled=true`
- `draft_only=true`
- `heartbeat_discovery_enabled=true`

## 8. Viewer / Ops 接続

Phase 100 の表示仕様へ渡す read model:

```json
{
  "economic_objective": {
    "opportunity_count": 0,
    "pending_approval_task_count": 0,
    "reflection_count": 0,
    "draft_only": true,
    "external_action_blocked": true
  }
}
```

## 9. 実装手順

### Task 1: viewer revenue store / handler

対象:

```text
internal/adapter/viewer/revenue_handler.go
internal/adapter/viewer/revenue_handler_test.go
cmd/rencrow/routes.go
cmd/rencrow/runtime_dependencies.go
```

確認:

```bash
go test ./internal/adapter/viewer ./cmd/rencrow
```

### Task 2: rencrowclient

対象:

```text
pkg/rencrowclient
```

確認:

```bash
go test ./pkg/rencrowclient
```

### Task 3: Workstream Goal 接続

対象:

```text
internal/application/revenue
internal/application/workstream
internal/domain/workstream
```

確認:

```bash
go test ./internal/application/revenue ./internal/domain/workstream
```

### Task 4: Reflection from RevenueEvent

対象:

```text
internal/application/revenue
internal/domain/revenue
```

確認:

```bash
go test ./internal/application/revenue ./internal/domain/revenue
```

### Task 5: Heartbeat draft-only

対象:

```text
internal/application/heartbeat
cmd/rencrow/runtime_heartbeat.go
internal/adapter/config
```

確認:

```bash
go test ./internal/application/heartbeat ./cmd/rencrow
```

## 10. 失敗系テスト

必須:

- `expected_revenue < 0` は reject
- `expected_cost < 0` は reject
- prohibited claim を含む Opportunity は reject
- `external_publish` task が `approval_mode=none` なら reject
- `billing` task が `approval_mode=none` なら reject
- Opportunity がない Workstream Goal 作成は 404
- negative expected_profit の Opportunity は high priority にならない
- Heartbeat は draft-only 以外では Opportunity discovery を実行しない

## 11. 完了条件

- Opportunity / EconomicTask / EconomicReflection を Viewer API から作成・一覧できる。
- Opportunity は expected_profit / profit_margin を保存境界で正規化する。
- approval-required task は自動実行されない。
- Opportunity から draft Workstream Goal を作れる。
- RevenueEvent から Reflection を作れる。
- Heartbeat が draft Opportunity を作れる。
- 外部公開 / 請求 / 契約 / 外部送信は発生しない。
- `go test ./...` が通る。

## 12. 停止条件

次は別仕様へ切り出す。

- 実決済 / 実請求 adapter
- 外部メール / SNS 実送信 adapter
- 価格自動決定
- 広告出稿
- 顧客個別連絡
- 外部 marketplace 登録
