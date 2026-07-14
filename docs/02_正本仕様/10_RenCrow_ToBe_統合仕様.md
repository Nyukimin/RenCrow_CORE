# RenCrow To-Be 統合仕様

- status: canonical
- canonical_path: `docs/02_正本仕様/10_RenCrow_ToBe_統合仕様.md`
- promoted_at: 2026-07-14

## 1. 目的

本仕様は、RenCrow が到達すべき全体像と、現行 `RenCrow_CORE` が満たすべき責務、契約、不変条件を定義する。

対象は次の4領域である。

```text
1. RenCrow CORE 全体責務
2. 稼ぐための Economic Objective / Revenue Loop
3. Codex などの外部エージェントを Advisor として扱う設計
4. Knowledge / Memory / Relation / Recall の軽量横断想起
```

本仕様は、実装判断に必要な責務境界、domain model、DB schema、API 契約を定義する。実装 Phase、テスト、移行、受入条件は `11_RenCrow_ToBe_統合実装仕様.md` を正とする。

## 2. 前提

### 2.1 既存正本

本仕様は以下を前提にする。

- `docs/refs/10_新仕様/04_Chat_Worker_Coder仕様.md`
- `docs/refs/10_新仕様/09_Memory_SourceRegistry仕様.md`
- `docs/refs/10_新仕様/13_実装項目インベントリ.md`
- `docs/refs/10_新仕様/18_知識記憶システム構想.md`
- `docs/refs/10_新仕様/20_Tool_Harness_Contract_Mediation仕様.md`
- `docs/refs/10_新仕様/22_Revenue_Operating_Principles仕様.md`
- `docs/refs/10_新仕様/23_Workstream_Operating_Loop仕様.md`
- `docs/refs/10_新仕様/24_Agent_Skill_Governance仕様.md`
- `docs/refs/10_新仕様/29_Sandbox_Promotion_Gate仕様.md`
- `docs/refs/10_新仕様/90_Runtime_Topology_Config仕様.md`

### 2.2 実装上の前提

- CORE は制御面と状態管理面を持つ runtime である。
- LLM / TTS / STT / Vision / Game 本体は CORE に抱え込まない。
- Coder は `plan` / `patch` / `proposal` を生成する。
- Worker は実行、適用、安全確認、ログ、検証を担当する。
- 外部検索結果、Advisor 出力、Revenue 候補は無審査で確定知識にしない。
- 公開、送信、契約、請求、価格決定、個人情報利用は Human approval 必須とする。

## 3. To-Be 全体像

RenCrow は次の三層で整理する。

```text
RenCrow
├─ Agents
│  ├─ Mio
│  ├─ Shiro
│  ├─ Aka
│  ├─ Ao
│  ├─ Gin
│  ├─ Kin
│  ├─ Kuro
│  └─ Midori
│
├─ Advisors
│  ├─ Codex
│  ├─ Claude Code
│  ├─ Gemini CLI
│  ├─ Cursor Agent
│  └─ Local Specialist
│
└─ Tools
   ├─ Git
   ├─ Shell
   ├─ Browser
   ├─ Web Gather
   ├─ File
   ├─ Image
   └─ Runtime module servers
```

### 3.1 Agent

Agent は RenCrow 内部の主体である。

Agent は次を持つ。

```text
Agent
├─ Role
├─ Capability
├─ Goal
├─ Motivation
├─ Autonomy
├─ Economic Objective
├─ Utility
├─ Trust
├─ Reputation
├─ Memory
└─ Knowledge Affinity
```

Agent は意思決定に関与できる。ただし権限は `Autonomy Envelope` により制限される。

### 3.2 Advisor

Advisor は外部専門家である。

Advisor ができること:

- 調査
- 設計案
- patch 案
- テスト案
- risk 指摘
- alternative 比較

Advisor がしてはいけないこと:

- 最終判断
- 直接実行
- Memory 直接更新
- Revenue 確定
- 外部公開
- git push
- production DB write
- approval / sandbox bypass

### 3.3 Tool

Tool は手段である。

Tool は意思を持たない。Tool 呼び出しは Worker、Tool Harness、Command Gate、Sandbox Guard の管理下に置く。

## 4. CORE の責務

### 4.1 CORE に含めるもの

```text
Chat runtime
Agent selection / delegation
Worker execution control
Coder plan / patch proposal control
Advisor request / result control
LLM / TTS / STT / Vision module server connection
Viewer / Channel API
Memory lifecycle
Knowledge registration / recall
Source Registry / staging / validation
Revenue candidate / approval / delivery state
Scheduler / Heartbeat / Ops
Tool Harness / Sandbox / Governance
Logs / Health / Runtime topology
```

### 4.2 CORE に含めないもの

```text
LLM 推論本体
TTS 生成本体
STT 推論本体
Vision 推論本体
Game 世界本体
大量 Knowledge data 本体
X Bookmark 収集データ本体
映画 / 音楽 / 小説などの大規模外部DB本体
```

CORE はデータを抱える製品ではなく、データを管理し、探し、Agent へ渡す runtime とする。

## 5. 機能台帳

### 5.1 状態区分

各機能は次の状態で管理する。

| 状態 | 意味 |
| --- | --- |
| `canonical` | 責務、境界、不変条件が確定済み |
| `contracted` | `modules/*` などで公開契約化済み |
| `implemented` | production code に実装済み |
| `facade_only` | feature 入口や facade はあるが本体は legacy-body 側 |
| `legacy_body` | 現役実装だが新しい module 境界へ未整理 |
| `concept` | 構想または仕様のみ |
| `out_of_scope` | 現時点ではCORE本線対象外 |

### 5.2 追加する台帳単位

最初に次の単位で台帳化する。

| Feature | 初期状態 | 備考 |
| --- | --- | --- |
| `core` | `contracted` | 既存 modules/core |
| `chat` | `contracted` | 既存 modules/chat |
| `worker` | `contracted` | 既存 modules/worker |
| `llm` | `contracted` | module server 接続 |
| `tts` | `contracted` | module server 接続 |
| `stt` | `contracted` | module server 接続 |
| `browseractor` | `contracted` | browser 操作契約 |
| `webgather` | `contracted` | discovery/fetch/extract |
| `memory` | `implemented` | contract 化途中 |
| `knowledge` | `implemented` | relation layer は未実装 |
| `source` | `implemented` | Source Registry |
| `revenue` | `implemented` | Economic Objective は未整理 |
| `workstream` | `implemented` | revenue/economic と接続予定 |
| `advisor` | `concept` | Codex.run 既存 tool を上位化 |
| `agent_profile` | `concept` | Goal / Utility / Autonomy |
| `knowledge_relation` | `concept` | item_relations 追加 |
| `economic_objective` | `concept` | Opportunity loop |

### 5.3 責務の配置先

最小構成は Markdown 台帳から開始する。

```text
docs/02_正本仕様/12_CORE_機能台帳.md
```

runtime から参照する段階になったら次を追加する。

```text
internal/domain/featureledger
internal/application/featureledger
internal/infrastructure/persistence/featureledger
internal/features/core
```

DB 化は Phase 2 以降でよい。最初から DB を増やさない。

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

## 8. Knowledge Relation / Recall

### 8.1 基本方針

Knowledge DB はカテゴリ別に分ける。

Relation はカテゴリをまたぐ索引として持つ。

```text
Knowledge DB
├─ kb:x_bookmark
├─ kb:note
├─ kb:qiita
├─ kb:github
├─ kb:paper
├─ kb:culture
└─ kb:news

Relation Layer
├─ entity
├─ topic
├─ project
├─ creator
├─ technology
├─ source
└─ related_item
```

Relation は世界を完全にモデル化するためではなく、思い出すための細い道を残すために使う。

### 8.2 Schema

```sql
CREATE TABLE knowledge_items (
    item_id TEXT PRIMARY KEY,
    source_type TEXT NOT NULL,
    domain TEXT NOT NULL,
    title TEXT NOT NULL,
    summary TEXT NOT NULL,
    source_url TEXT,
    author TEXT,
    published_at TIMESTAMP,
    embedding_id TEXT,
    metadata_json TEXT NOT NULL DEFAULT '{}',
    validation_state TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL
);

CREATE TABLE entities (
    entity_id TEXT PRIMARY KEY,
    canonical_name TEXT NOT NULL,
    entity_type TEXT NOT NULL,
    aliases_json TEXT NOT NULL DEFAULT '[]',
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL
);

CREATE TABLE item_entities (
    item_id TEXT NOT NULL,
    entity_id TEXT NOT NULL,
    relation_kind TEXT NOT NULL,
    score DOUBLE NOT NULL,
    evidence TEXT,
    created_at TIMESTAMP NOT NULL,
    PRIMARY KEY (item_id, entity_id, relation_kind)
);

CREATE TABLE item_relations (
    src_item_id TEXT NOT NULL,
    dst_item_id TEXT NOT NULL,
    relation_type TEXT NOT NULL,
    score DOUBLE NOT NULL,
    evidence TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    PRIMARY KEY (src_item_id, dst_item_id, relation_type)
);

CREATE INDEX idx_knowledge_items_domain ON knowledge_items(domain);
CREATE INDEX idx_item_entities_entity ON item_entities(entity_id);
CREATE INDEX idx_item_relations_src_score ON item_relations(src_item_id, score DESC);
CREATE INDEX idx_item_relations_dst_score ON item_relations(dst_item_id, score DESC);
```

### 8.3 Relation types

```text
same_topic
same_entity
same_project
same_author
supports
contradicts
implements
references
updates
derived_from
applies_to_project
used_together_in_conversation
```

### 8.4 Relation scoring

初期 scoring:

```text
same_entity        +3
same_project       +3
same_topic         +2
same_author        +1
vector_similarity  +0..2
conversation_pair  +1
```

保存条件:

```text
score >= 4
```

探索制限:

```text
max_hops = 2
max_related_items_per_seed = 10
max_total_relation_items = 30
```

### 8.5 Recall order

```text
1. 完全一致
2. タグ一致
3. Entity / Topic / Project 一致
4. Relation 上位
5. 足りない時だけ Vector 検索
6. Recall budget で再ランキング
```

### 8.6 RecallPack extension

`RecallPack` には次を追加する。

```go
type RelationSnippet struct {
    ItemID       string
    Title        string
    Summary      string
    SourceType   string
    RelationType string
    Score        float64
    Evidence     string
    Hop          int
}
```

追加先:

```text
internal/domain/conversation/recall_pack.go
```

ただし初期導入では既存 field を壊さず、optional field として追加する。

### 8.7 責務配置 package

```text
internal/domain/knowledgerelation
internal/application/knowledgerelation
internal/infrastructure/persistence/conversation/l1_sqlite_knowledge_relation.go
internal/application/knowledge/relation_builder.go
```

既存 `L1SQLite` / `Source Registry` / `VectorDB` と競合させない。Relation layer は L1 SQLite の追加テーブルとして始める。

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
