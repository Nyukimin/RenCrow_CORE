# To-Be: Agent Profile・Autonomy

- status: active
- lifecycle: canonical child
- owner: RenCrow_CORE
- parent_spec: `../10_RenCrow_ToBe_統合仕様.md`
- source_spec: `../10_RenCrow_ToBe_統合仕様.md`の2026-07-15分割前章
- last_reviewed: 2026-07-15
- scope: Agent Profile、Capability、Autonomy Envelope、Utility

## 6. Agent Profile / Autonomy / Utility

### 6.1 追加概念

Agent には次の profile を持たせる。

```go
type AgentProfile struct {
    ID                string
    DisplayName       string
    Role              string
    Capabilities      []Capability
    Goals             []Goal
    Motivation        []MotivationSignal
    UtilityProfile    UtilityProfile
    AutonomyEnvelope  AutonomyEnvelope
    EconomicProfile   *EconomicProfile
    KnowledgeAffinity []KnowledgeAffinity
}
```

### 6.2 Autonomy Envelope

```go
type AutonomyEnvelope struct {
    Observe          []string
    Decide           []string
    ActAllowed       []string
    ApprovalRequired []string
    Forbidden        []string
}
```

例:

```yaml
agent: shiro
autonomy:
  observe:
    - logs
    - task_state
    - health
  decide:
    - retry
    - ask_advisor
    - ask_coder
    - run_test
    - defer
  act_allowed:
    - read_file
    - run_test
    - apply_safe_patch
  approval_required:
    - restart_service
    - write_config
    - git_push
  forbidden:
    - delete_production_data
    - expose_secret
    - bypass_approval
```

### 6.3 Utility

Utility は「何を良しとするか」を定義する。

共通項目:

```text
success_rate
user_value
quality
rework_penalty
risk_penalty
reputation_penalty
reuse_value
strategic_value
```

Revenue 系では次を追加する。

```text
net_profit
customer_value
automation_rate
future_value
```

### 6.4 段階導入の制約

初期導入では、Agent の判断ロジックを大きく変えない。

順序:

1. profile 型を追加する。
2. 既存 Agent に静的 profile を付ける。
3. Viewer / Ops で profile を読めるようにする。
4. Shiro の「相談するか」判定だけ `AutonomyEnvelope` を使う。
5. Reflection / Reputation 更新は後段に回す。

想定 package:

```text
internal/domain/agentprofile
internal/application/agentprofile
internal/infrastructure/persistence/agentprofile
internal/features/agent
```

既存 `internal/domain/agent` に未コミット変更がある場合は、先に差分を確認してから分離する。
