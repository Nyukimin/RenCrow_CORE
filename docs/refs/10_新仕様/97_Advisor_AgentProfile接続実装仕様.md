# Advisor / AgentProfile 接続実装仕様

## 1. 目的

本仕様は、`95_RenCrow_ToBe_統合仕様と実装方針.md` と `96_CORE_機能台帳.md` で MVP 実装済みとなった `advisor` と `agent_profile` を、runtime の判断材料として安全に接続するための実装仕様である。

対象は次である。

```text
1. Advisor 実行結果の永続化
2. Advisor score / registry
3. AgentProfile の runtime policy 反映
4. Shiro の Advisor 利用判断の明示化
5. Viewer / Ops 表示へ渡す read model
```

本仕様は、Codex や外部 Advisor に実行責任を渡すものではない。Advisor は助言者であり、実行責任は Shiro / Worker に残る。

## 2. 現在の実装状態

MVP 実装済み:

```text
internal/domain/advisor
internal/application/advisor
internal/domain/agentprofile
internal/application/agentprofile
internal/domain/agent/shiro.go
cmd/rencrow/runtime_agents.go
modules/worker/diagnostics.go
```

現在できること:

- `codex.enabled` かつ `codex.run` が登録されている場合、Shiro が `AdvisorService` 経由で Codex advice を取得できる。
- `codex.run` の直接 fallback は互換のため残っている。
- 8 人格の静的 AgentProfile と AutonomyEnvelope が取得できる。

未接続:

- AdviceResult の永続化
- Advisor score の集計
- Advisor registry の一覧 API
- AgentProfile を実行判断に反映する policy service
- Viewer / Ops 表示

## 3. 実装範囲

### 3.1 含める

- `AdviceRunRecord` の domain model 追加
- `AdvisorScoreSnapshot` の domain model 追加
- Advisor registry read model 追加
- JSONL / SQLite persistence
- `RecordingAdvisorService`
- `AdvisorScoreService`
- `AgentPolicyService`
- Shiro の `ask_advisor` 判定を AgentProfile 経由にする
- read-only Viewer API
- `pkg/rencrowclient` の read client

### 3.2 含めない

- Codex 以外の外部 Advisor 実行 adapter
- Advisor による直接 patch apply / git push / external publish
- Advisor output の Memory 直接書き込み
- Advisor output の Knowledge 確定登録
- LLM provider 選択ロジックの置き換え
- `modules/advisor` の公開 contract 化

`modules/advisor` は、少なくとも複数 Advisor adapter と score 更新運用が安定するまで作らない。

## 4. 責務境界

| 層 | 責務 |
| --- | --- |
| `internal/domain/advisor` | Advisor request / result / run record / score の pure model と validation |
| `internal/application/advisor` | Advisor selection、recording、score aggregation |
| `internal/infrastructure/persistence/advisor` | JSONL / SQLite 永続化 |
| `internal/domain/agentprofile` | AutonomyEnvelope / Utility / Trust / Reputation の pure model |
| `internal/application/agentprofile` | Profile catalog と runtime policy 判定 |
| `internal/domain/agent` | Shiro が policy 判定後に AdvisorService を呼ぶ |
| `cmd/rencrow` | config と store の wiring のみ |
| `internal/adapter/viewer` | read-only status API |
| `pkg/rencrowclient` | Viewer API contract client validation |

## 5. Domain model

### 5.1 AdviceRunRecord

`internal/domain/advisor` に追加する。

```go
type AdviceRunRecord struct {
    RunID            string
    RequestID        string
    TaskID           string
    RequestedByAgent string
    AdvisorID        AdvisorID
    Purpose          string
    PromptHash       string
    RiskClass        string
    ApprovalMode     string
    Status           AdviceStatus
    Summary          string
    OutputHash       string
    Error            string
    StartedAt        time.Time
    FinishedAt       time.Time
    LatencyMillis    int64
}
```

validation:

- `run_id` 必須
- `advisor_id` 必須
- `requested_by_agent` 必須
- `approval_mode` は `advice_only` / `human_required` のみ
- `status` は `completed` / `failed` / `unavailable` / `rejected`
- prompt / output の本文は保存しない。hash と summary だけ保存する。

### 5.2 AdvisorScoreSnapshot

```go
type AdvisorScoreSnapshot struct {
    SnapshotID        string
    AdvisorID         AdvisorID
    CapabilityID      string
    WindowStart       time.Time
    WindowEnd         time.Time
    RequestCount      int
    CompletedCount    int
    FailedCount       int
    UnavailableCount  int
    AdoptedCount      int
    SuccessCount      int
    AvgLatencyMillis  int64
    AvgRevisionCount  float64
    Score             float64
    CreatedAt         time.Time
}
```

score 計算の初期式:

```text
completion_rate = completed_count / request_count
adoption_rate   = adopted_count / completed_count
success_rate    = success_count / adopted_count
latency_penalty = min(avg_latency_ms / 600000, 1.0) * 0.10

score =
  0.35 * completion_rate
+ 0.35 * success_rate
+ 0.20 * adoption_rate
+ 0.10 * max(0, 1 - avg_revision_count / 5)
- latency_penalty
```

分母が 0 の場合は、その項目を 0 とする。

### 5.3 AdvisorAdoptionRecord

Advisor output が Shiro に採用されたかを別 record として保存する。

```go
type AdvisorAdoptionRecord struct {
    AdoptionID     string
    RunID          string
    TaskID         string
    AdvisorID      AdvisorID
    AdoptedByAgent string
    Adopted        bool
    Outcome        string
    RevisionCount  int
    Reason         string
    CreatedAt      time.Time
}
```

`Outcome` は `success` / `partial` / `failed` / `not_run`。

### 5.4 AgentPolicyDecision

AgentProfile を runtime policy に使った結果を、必要最小限の trace として保存できるようにする。

```go
type AgentPolicyDecision struct {
    DecisionID string
    AgentID    string
    Action     string
    Decision   string // allowed / approval_required / forbidden
    Reason     string
    CreatedAt  time.Time
}
```

## 6. Persistence

新規 package:

```text
internal/infrastructure/persistence/advisor
```

実装する store interface:

```go
type Store interface {
    SaveAdviceRun(ctx context.Context, item advisor.AdviceRunRecord) error
    ListAdviceRuns(ctx context.Context, limit int) ([]advisor.AdviceRunRecord, error)
    SaveAdvisorAdoption(ctx context.Context, item advisor.AdvisorAdoptionRecord) error
    ListAdvisorAdoptions(ctx context.Context, limit int) ([]advisor.AdvisorAdoptionRecord, error)
    SaveAdvisorScoreSnapshot(ctx context.Context, item advisor.AdvisorScoreSnapshot) error
    ListAdvisorScoreSnapshots(ctx context.Context, limit int) ([]advisor.AdvisorScoreSnapshot, error)
}
```

SQLite tables:

```sql
advisor_run(run_id TEXT PRIMARY KEY, created_at TEXT, payload TEXT NOT NULL)
advisor_adoption(adoption_id TEXT PRIMARY KEY, created_at TEXT, payload TEXT NOT NULL)
advisor_score_snapshot(snapshot_id TEXT PRIMARY KEY, created_at TEXT, payload TEXT NOT NULL)
```

JSONL files:

```text
workspace/logs/advisor/advisor_run.jsonl
workspace/logs/advisor/advisor_adoption.jsonl
workspace/logs/advisor/advisor_score_snapshot.jsonl
```

## 7. Application service

### 7.1 RecordingAdvisorService

`internal/application/advisor` に追加する。

```go
type RecordingService struct {
    inner *Service
    store Store
    now   func() time.Time
}
```

振る舞い:

1. `inner.RequestAdvice` を呼ぶ。
2. request / result から `AdviceRunRecord` を作る。
3. store がある場合は保存する。
4. 保存失敗は request 結果を失敗にしない。ただし log と Viewer status には出す。

理由:

Advisor は補助機能であり、記録の一時失敗で Shiro の既存 work path を止めない。

### 7.2 AdvisorScoreService

```go
func BuildScoreSnapshot(runs []advisor.AdviceRunRecord, adoptions []advisor.AdvisorAdoptionRecord, windowStart, windowEnd time.Time) advisor.AdvisorScoreSnapshot
```

初回は日次 batch でなく、手動 service call / unit test で動く pure service とする。

## 8. AgentProfile runtime policy

`internal/application/agentprofile` に追加する。

```go
type PolicyService struct {
    catalog *Catalog
}

func (s *PolicyService) Decide(agentID string, action string) (agentprofile.PolicyDecision, error)
```

判定:

1. `AutonomyEnvelope.Act.Forbidden` にあれば `forbidden`
2. `AutonomyEnvelope.Act.ApprovalRequired` にあれば `approval_required`
3. `AutonomyEnvelope.Decide` または `AutonomyEnvelope.Act.Allowed` にあれば `allowed`
4. それ以外は `forbidden`

Shiro 接続:

- Codex work path で Advisor を使う前に `Decide("shiro", "ask_advisor")` を呼ぶ。
- `forbidden` の場合は Advisor を呼ばず通常 Worker 経路へ fallback する。
- `approval_required` の場合は自動実行せず error ではなく `needs approval` の助言不能結果を返す。

## 9. Viewer / Client API

Phase 100 の Viewer 表示仕様に接続するため、read-only API を先に定義する。

```text
GET /viewer/advisors
GET /viewer/advisors/runs?limit=50
GET /viewer/advisors/scores?limit=50
GET /viewer/agents/profiles
GET /viewer/agents/policy-decisions?limit=50
```

`/viewer/advisors` response:

```json
{
  "profiles": [],
  "recent_runs": [],
  "score_snapshots": [],
  "summary": {
    "advisor_count": 1,
    "recent_run_count": 0,
    "failed_run_count": 0
  }
}
```

禁止:

- API は prompt 本文と output 本文を返さない。
- `codex.run` raw output は `summary` だけにする。
- Viewer から Advisor を直接実行しない。

## 10. 実装手順

### Task 1: domain model

対象:

```text
internal/domain/advisor
internal/domain/agentprofile
```

作業:

- `AdviceRunRecord` / `AdvisorAdoptionRecord` / `AdvisorScoreSnapshot` を追加。
- validation と unit test を追加。
- `PolicyDecision` を追加。

確認:

```bash
go test ./internal/domain/advisor ./internal/domain/agentprofile
```

### Task 2: persistence

対象:

```text
internal/infrastructure/persistence/advisor
```

作業:

- JSONL store
- SQLite store
- list は新しい順に返す
- prompt / output 本文を保存しない test

確認:

```bash
go test ./internal/infrastructure/persistence/advisor
```

### Task 3: recording service / score service

対象:

```text
internal/application/advisor
```

作業:

- `RecordingService`
- `BuildScoreSnapshot`
- store 失敗時の graceful degradation test

確認:

```bash
go test ./internal/application/advisor
```

### Task 4: AgentProfile policy service

対象:

```text
internal/application/agentprofile
internal/domain/agent
```

作業:

- `PolicyService.Decide`
- Shiro の `ask_advisor` 前判定
- forbidden の場合 Advisor を呼ばない test

確認:

```bash
go test ./internal/application/agentprofile ./internal/domain/agent
```

### Task 5: runtime wiring

対象:

```text
cmd/rencrow/runtime_agents.go
cmd/rencrow/runtime_dependencies.go
```

作業:

- advisor store を config から作る。
- `RecordingService` を Shiro に渡す。
- `PolicyService` を Shiro に渡す。
- store 未設定なら現行動作を維持する。

確認:

```bash
go test ./cmd/rencrow ./internal/domain/agent
```

### Task 6: Viewer API / client

対象:

```text
internal/adapter/viewer
pkg/rencrowclient
cmd/rencrow/routes.go
```

作業:

- read-only API
- malformed response validation
- prompt / output 本文が出ない test

確認:

```bash
go test ./internal/adapter/viewer ./pkg/rencrowclient ./cmd/rencrow
```

## 11. 完了条件

- Shiro が Advisor を呼ぶたびに `AdviceRunRecord` が残る。
- Advisor output 本文は永続化されない。
- Advisor score を service で計算できる。
- Shiro の `ask_advisor` が AgentProfile policy を通る。
- forbidden action では Advisor が呼ばれない。
- read-only Viewer API で recent runs / scores / profiles を取得できる。
- `go test ./...` が通る。

## 12. 停止条件

次が必要になったら、この仕様の作業を止めて別仕様へ切り出す。

- Codex 以外の Advisor adapter 実装
- 実 patch 採用 / apply の自動評価
- 外部 API 実行
- git push / PR 作成との接続
- Memory / Knowledge への Advisor output 自動登録
