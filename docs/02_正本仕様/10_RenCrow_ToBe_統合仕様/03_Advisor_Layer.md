# To-Be: Advisor Layer

- status: active
- lifecycle: canonical child
- owner: RenCrow_CORE
- parent_spec: `../10_RenCrow_ToBe_統合仕様.md`
- source_spec: `../10_RenCrow_ToBe_統合仕様.md`の2026-07-15分割前章
- last_reviewed: 2026-07-15
- scope: 外部Advisor、request / result、approval、score

## 7. Advisor Layer

### 7.1 目的

Codex、Claude Code、Gemini CLI、Cursor Agent などを Agent でも Tool でもなく Advisor として扱う。

### 7.2 Domain model

```go
type AdvisorID string

type AdvisorCapability struct {
    Domain      string
    Level       int
    Description string
}

type AdvisorProfile struct {
    ID           AdvisorID
    DisplayName  string
    Provider     string
    Capabilities []AdvisorCapability
    AllowedModes []string
    Disabled     bool
}

type AdviceRequest struct {
    ID               string
    TaskID           string
    RequestedByAgent string
    AdvisorID        AdvisorID
    Purpose          string
    Prompt           string
    ContextRefs      []ContextRef
    AllowedArtifacts []string
    RiskClass        string
    CostBudget       CostBudget
    TimeoutMillis    int
    ApprovalMode     string
    CreatedAt        time.Time
}

type AdviceResult struct {
    RequestID    string
    AdvisorID    AdvisorID
    Status       string
    Summary      string
    Plan         string
    Patch        string
    Tests        []string
    Risks        []string
    Artifacts    []AdvisorArtifact
    TokenUsage   *TokenUsage
    CostEstimate *CostEstimate
    StartedAt    time.Time
    CompletedAt  time.Time
}

type AdvisorScore struct {
    AdvisorID       AdvisorID
    Domain          string
    AdoptionRate    float64
    SuccessRate     float64
    AverageRework   float64
    RiskIncidentCnt int
    UpdatedAt       time.Time
}
```

### 7.3 Persistence

初期DB:

```sql
CREATE TABLE advisor_registry (
    advisor_id TEXT PRIMARY KEY,
    provider TEXT NOT NULL,
    display_name TEXT NOT NULL,
    capabilities_json TEXT NOT NULL,
    allowed_modes_json TEXT NOT NULL,
    disabled BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL
);

CREATE TABLE advisor_run (
    run_id TEXT PRIMARY KEY,
    request_id TEXT NOT NULL,
    advisor_id TEXT NOT NULL,
    requested_by_agent TEXT NOT NULL,
    purpose TEXT NOT NULL,
    status TEXT NOT NULL,
    risk_class TEXT NOT NULL,
    prompt_ref TEXT,
    result_ref TEXT,
    started_at TIMESTAMP,
    completed_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL
);

CREATE TABLE advisor_score (
    advisor_id TEXT NOT NULL,
    domain TEXT NOT NULL,
    adoption_rate DOUBLE NOT NULL DEFAULT 0,
    success_rate DOUBLE NOT NULL DEFAULT 0,
    average_rework DOUBLE NOT NULL DEFAULT 0,
    risk_incident_count INTEGER NOT NULL DEFAULT 0,
    updated_at TIMESTAMP NOT NULL,
    PRIMARY KEY (advisor_id, domain)
);
```

### 7.4 Codex integration

現状の `codex.run` は ToolRunner に存在する。

移行方針:

1. `codex.run` は互換のため残す。
2. `internal/application/advisor` に `AdvisorService` を作る。
3. `AdvisorService` の Codex adapter が内部で `codex.run` を呼ぶ。
4. Shiro は直接 `codex.run` を選ぶのではなく、`AdvisorService.RequestAdvice()` を呼ぶ。
5. 既存 test は壊さず、Advisor 経由 test を追加する。

想定 package:

```text
internal/domain/advisor
internal/application/advisor
internal/infrastructure/advisor
internal/infrastructure/persistence/advisor
internal/features/advisor
```

`modules/advisor` は安定契約が固まるまで作らない。最初は internal feature とする。

### 7.5 Worker flow

```text
Task
  ↓
Shiro observes task / logs / repo
  ↓
Need advice?
  ↓ yes
AdvisorService selects advisor
  ↓
AdviceRequest
  ↓
AdviceResult
  ↓
Shiro decides adopt / reject / ask another advisor
  ↓
Worker executes
  ↓
Result and score update
```
