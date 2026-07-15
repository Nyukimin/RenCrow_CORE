# To-Be: Economic Objective・Revenue

- status: active
- lifecycle: canonical child
- owner: RenCrow_CORE
- parent_spec: `../10_RenCrow_ToBe_統合仕様.md`
- source_spec: `../10_RenCrow_ToBe_統合仕様.md`の2026-07-15分割前章
- last_reviewed: 2026-07-15
- scope: Economic Objective、Revenue loop、Workstream、人間承認

## 9. Economic Objective / Revenue Autonomy

### 9.1 目的

RenCrow は、収益機会を発見し、提案し、制作し、検証し、学習する。ただし金銭・公開・契約・請求に関わる確定行為は Human approval 必須とする。

### 9.2 Revenue loop

```text
Discover
  ↓
Evaluate
  ↓
Produce Draft
  ↓
Human Approval
  ↓
Publish / Deliver
  ↓
Revenue Event
  ↓
Reflection
  ↓
Next Opportunity
```

### 9.3 Domain model

既存 `internal/domain/revenue` を主担当にし、新規 package を乱立させない。

追加候補:

```go
type Opportunity struct {
    ID              string
    SourceKind      string
    Title           string
    Summary         string
    TargetCustomer  string
    ExpectedRevenue Money
    ExpectedCost    Money
    ExpectedProfit  Money
    ProfitMargin    float64
    ReuseValue      float64
    AutomationRate  float64
    StrategicValue  float64
    RiskScore       float64
    ApprovalState   string
    CreatedAt       time.Time
    UpdatedAt       time.Time
}

type EconomicTask struct {
    ID            string
    OpportunityID string
    WorkstreamID  string
    AgentID       string
    TaskKind      string
    Status        string
    ExpectedValue float64
    Risk          float64
    Cost          float64
    ApprovalMode  string
    CreatedAt     time.Time
    UpdatedAt     time.Time
}

type EconomicReflection struct {
    ID             string
    OpportunityID  string
    RevenueEventID string
    Outcome        string
    NetProfit      Money
    Lessons        []string
    NextActions    []string
    CreatedAt      time.Time
}
```

### 9.4 Utility

```text
utility =
  0.35 * net_profit
+ 0.20 * customer_value
+ 0.15 * reuse_value
+ 0.10 * automation_rate
+ 0.10 * quality
+ 0.10 * strategic_value
- risk_penalty
- reputation_penalty
```

### 9.5 自動実行可能

```text
市場候補の整理
既存素材の棚卸し
企画案作成
試作品作成
原価計算
競合比較
商品説明文の下書き
コードや資料の作成
テスト
収益レポート
```

### 9.6 Approval required

```text
公開
価格決定
顧客への送信
契約
有料API利用
広告出稿
外部サービス登録
請求
GitHub公開
個人情報の利用
```

### 9.7 Forbidden

```text
無断契約
虚偽表示
スパム営業
他者著作物の無断販売
アカウント貸与
秘密情報の公開
損失上限のない投資
成功保証
規約違反の自動化
```

### 9.8 責務配置 package

```text
internal/domain/revenue
internal/application/revenue
internal/infrastructure/persistence/revenue
internal/features/revenue
internal/application/workstream
```

Revenue と Workstream を分離しつつ、Opportunity から Workstream を作れるようにする。

```text
Opportunity
  -> Workstream Goal
  -> Artifact
  -> Approval
  -> Delivery
  -> RevenueEvent
  -> EconomicReflection
```
