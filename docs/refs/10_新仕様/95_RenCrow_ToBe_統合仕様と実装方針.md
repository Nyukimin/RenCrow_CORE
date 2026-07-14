# RenCrow To-Be 統合仕様と実装方針

## 1. 目的

本仕様は、RenCrow が到達すべき全体像と、現行 `RenCrow_CORE` からそこへ到達するための実装方針を定義する。

対象は次の4領域である。

```text
1. RenCrow CORE 全体責務
2. 稼ぐための Economic Objective / Revenue Loop
3. Codex などの外部エージェントを Advisor として扱う設計
4. Knowledge / Memory / Relation / Recall の軽量横断想起
```

本仕様は、実装者がそのまま作業へ入れるように、責務境界、追加 package、DB schema、API、テスト、Phase gate を含めて定義する。

## 2. 前提

### 2.1 既存正本

本仕様は以下を前提にする。

- `04_Chat_Worker_Coder仕様.md`
- `09_Memory_SourceRegistry仕様.md`
- `13_実装項目インベントリ.md`
- `18_知識記憶システム構想.md`
- `20_Tool_Harness_Contract_Mediation仕様.md`
- `22_Revenue_Operating_Principles仕様.md`
- `23_Workstream_Operating_Loop仕様.md`
- `24_Agent_Skill_Governance仕様.md`
- `29_Sandbox_Promotion_Gate仕様.md`
- `90_Runtime_Topology_Config仕様.md`

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

### 5.3 実装先

最小実装は Markdown 台帳から開始する。

```text
docs/refs/10_新仕様/96_CORE_機能台帳.md
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

### 6.4 実装方針

初期実装では、Agent の判断ロジックを大きく変えない。

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

ただし初期実装では既存 field を壊さず、optional field として追加する。

### 8.7 実装 package

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

### 9.8 実装 package

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

## 12. 実装 Phase

### Phase 0: 仕様固定

目的:

- 本仕様を docs に追加する。
- 既存新仕様 README へ登録する。
- `91-94` 相当の個別仕様を移植する場合の番号衝突を避ける。

作業:

- `95_RenCrow_ToBe_統合仕様と実装方針.md` を追加する。
- `00_README.md` に本仕様を追加する。
- 実装は行わない。

完了条件:

- `git diff --check` が通る。
- docs のみの差分である。

### Phase 1: CORE 機能台帳

目的:

- 実装済み、Facade、Legacy、構想を混同しない。

作業:

- `96_CORE_機能台帳.md` を作る。
- `modules/*`、`internal/features/*`、`internal/domain/*`、`internal/application/*` を台帳化する。
- `advisor`、`knowledge_relation`、`economic_objective` を `concept` として登録する。

完了条件:

- 各 feature に owner package と state がある。
- `13_実装項目インベントリ.md` と矛盾しない。
- コード変更なし。

### Phase 2: Advisor MVP

目的:

- Codex を Tool 直呼びではなく Advisor として扱う。

作業:

- `internal/domain/advisor` を追加する。
- `AdviceRequest` / `AdviceResult` / `AdvisorProfile` / `AdvisorScore` を定義する。
- `internal/application/advisor` に `AdvisorService` を追加する。
- 既存 `codex.run` を呼ぶ `CodexAdvisorAdapter` を追加する。
- Shiro の Codex work path を AdvisorService 経由へ移す。

禁止:

- `codex.run` の互換削除。
- Codex に直接 git push / external publish を許す。
- `--dangerously-bypass-approvals-and-sandbox` を許す。

テスト:

```bash
go test ./internal/domain/advisor ./internal/application/advisor ./internal/infrastructure/tools ./internal/domain/agent
```

完了条件:

- Shiro が Advisor 経由で Codex advice を取得できる。
- 既存 `codex.run` tests が通る。
- AdvisorResult は実行ではなく助言として記録される。

### Phase 3: Agent Profile MVP

目的:

- Agent の Goal / Utility / Autonomy を runtime から読めるようにする。

作業:

- `internal/domain/agentprofile` を追加する。
- 8人格の静的 profile を定義する。
- Shiro に `ask_advisor` の Autonomy permission を付ける。
- Mio は会話、委譲、再質問に限定する。
- Kuro は risk / stop recommendation を優先する。

テスト:

```bash
go test ./internal/domain/agentprofile ./internal/application/agentprofile ./internal/domain/agent
```

完了条件:

- profile が取得できる。
- 既存 Agent 応答を破壊しない。
- AutonomyEnvelope の forbidden action が判定できる。

### Phase 4: Knowledge Relation MVP

目的:

- カテゴリ別 Knowledge を壊さず、1-2 hop の横断想起を可能にする。

作業:

- `internal/domain/knowledgerelation` を追加する。
- L1 SQLite に relation tables を追加する。
- importer 後に entity / topic / project metadata を保存する。
- scoring により `item_relations` を作る。
- RecallPack に `RelationSnippet` を optional 追加する。

テスト:

```bash
go test ./internal/domain/knowledgerelation ./internal/application/knowledgerelation ./internal/infrastructure/persistence/conversation ./internal/domain/conversation
```

完了条件:

- 同じ Entity / Project を持つ item が relation 登録される。
- 1 hop recall が RecallPack に入る。
- 2 hop 上限を超えない。
- VectorDB がなくても動く。

### Phase 5: Economic Objective MVP

目的:

- Revenue を「候補生成、評価、制作、承認、売上、振り返り」の loop として扱う。

作業:

- `Opportunity` / `EconomicTask` / `EconomicReflection` を `internal/domain/revenue` に追加する。
- persistence を追加する。
- Opportunity から Workstream Goal を作れる service を追加する。
- Human approval gate を既存 revenue approval と接続する。

テスト:

```bash
go test ./internal/domain/revenue ./internal/application/revenue ./internal/infrastructure/persistence/revenue ./internal/application/workstream
```

完了条件:

- Opportunity を登録できる。
- expected_profit を計算できる。
- approval_required action は blocked / pending になる。
- RevenueEvent から Reflection を作れる。

### Phase 6: Viewer / Ops 表示

目的:

- 人間が状態を確認できるようにする。

追加表示:

- Advisor runs
- Advisor scores
- Agent profiles
- Knowledge relation recall trace
- Opportunities
- Approval queue
- Economic reflections

テスト:

```bash
go test ./internal/adapter/viewer ./pkg/rencrowclient
```

必要に応じて browser E2E を行う。

完了条件:

- 初期表示は要約。
- 生ログや長文 trace は details に閉じる。
- mobile 幅で崩れない。

### Phase 7: Scheduler / Heartbeat 接続

目的:

- 自律候補生成を安全に動かす。

作業:

- Heartbeat で Opportunity discovery draft を作る。
- Advisor score 更新を日次で集計する。
- Knowledge relation build を import 時または夜間 batch に限定する。

禁止:

- 勝手な公開。
- 勝手な請求。
- 勝手な外部送信。
- 勝手な memory promotion。

完了条件:

- draft-only で動く。
- approval queue なしに外部副作用が発生しない。

## 13. 実装順の原則

実装順は以下を守る。

```text
Docs / Ledger
  ↓
Domain model
  ↓
Application service
  ↓
Persistence
  ↓
Feature wiring
  ↓
Viewer / CLI
  ↓
Scheduler
  ↓
Autonomous loop
```

理由:

- 最初から自律 loop を動かすと安全境界が曖昧になる。
- DB schema より先に domain model を固定する。
- Viewer は実装確認用であり、正本 state の代替ではない。
- Scheduler は最後に接続する。

## 14. テスト方針

### 14.1 必須テスト

Advisor:

```bash
go test ./internal/domain/advisor ./internal/application/advisor ./internal/infrastructure/tools ./internal/domain/agent
```

Knowledge Relation:

```bash
go test ./internal/domain/knowledgerelation ./internal/application/knowledgerelation ./internal/infrastructure/persistence/conversation ./internal/domain/conversation
```

Revenue / Economic:

```bash
go test ./internal/domain/revenue ./internal/application/revenue ./internal/infrastructure/persistence/revenue ./internal/application/workstream
```

Viewer / Client:

```bash
go test ./internal/adapter/viewer ./pkg/rencrowclient
```

最終:

```bash
go test ./...
```

### 14.2 失敗テスト

必ず失敗系を作る。

- Advisor が forbidden action を要求したら拒否される。
- Codex adapter が danger sandbox を要求したら拒否される。
- Relation 3 hop 以上は展開されない。
- VectorDB unavailable でも relation recall は成立する。
- Approval required action は自動実行されない。
- Opportunity expected_profit が負なら high priority にならない。
- 外部公開や請求は approval なしに実行されない。

## 15. Migration policy

### 15.1 既存挙動を壊さない

既存 feature を置き換えるのではなく、上位 facade または optional field として追加する。

例:

- `codex.run` は残す。
- `RecallPack` は既存 field を残し、`RelationSnippets` を optional 追加する。
- `revenue` 既存 Product / RevenueEvent は残し、Opportunity を追加する。
- Agent の既存応答は変えず、profile を読むだけから始める。

### 15.2 Feature flag

runtime 影響があるものは config で opt-in にする。

```yaml
advisor:
  enabled: false
  default_provider: codex

knowledge_relation:
  enabled: false
  max_hops: 2

economic_objective:
  enabled: false
  draft_only: true
```

### 15.3 Backfill

Knowledge relation は既存全データへ即時 backfill しない。

順序:

1. 新規 import 分だけ relation を作る。
2. 小さい sample で backfill dry-run。
3. relation 数、score 分布、Recall trace を確認。
4. full backfill は別 job とする。

## 16. Acceptance criteria

本仕様の To-Be へ到達したと判断する条件は次である。

```text
[CORE]
- 全 feature が機能台帳で state 管理されている。
- Agent / Advisor / Tool が仕様上も実装上も分かれている。

[Advisor]
- Shiro が Codex を Advisor として呼べる。
- AdviceResult は助言として保存され、実行責任は Shiro に残る。
- Advisor score が更新できる。

[Knowledge]
- Knowledge DB はカテゴリ別のまま。
- Relation layer で Entity / Topic / Project 横断 recall ができる。
- RecallPack trace に relation reason が残る。

[Economic]
- Opportunity から Workstream / Artifact / Approval / RevenueEvent / Reflection へ接続できる。
- 公開、送信、請求、契約、価格決定は approval なしに実行されない。

[Safety]
- Advisor、Revenue、Knowledge import のすべてで provenance が残る。
- 外部情報と確定知識を混同しない。
- 自律候補生成は draft-only から開始する。
```

## 17. 最初の実装タスク

最初に着手する作業単位は次とする。

### Task 1: CORE 機能台帳 docs

```text
目的:
  現状の modules / features / legacy-body / concept を台帳化する。

対象:
  docs/refs/10_新仕様/96_CORE_機能台帳.md
  docs/refs/10_新仕様/13_実装項目インベントリ.md

やること:
  - modules/* を contracted として列挙
  - internal/features/* を feature entry として列挙
  - advisor / knowledge_relation / economic_objective を concept として登録
  - 実装済みと Facade の区別を明記

やらないこと:
  - Go code 変更
  - DB migration
  - runtime wiring

確認:
  git diff --check
```

### Task 2: Advisor domain MVP

```text
目的:
  Codex を Tool ではなく Advisor として呼ぶための domain / service を作る。

対象:
  internal/domain/advisor
  internal/application/advisor
  internal/infrastructure/advisor
  internal/domain/agent

やること:
  - AdviceRequest / AdviceResult / AdvisorProfile を定義
  - AdvisorService を実装
  - CodexAdvisorAdapter を実装
  - Shiro の Codex 利用経路を AdvisorService 経由へ変更
  - 既存 codex.run tests を維持

やらないこと:
  - codex.run 削除
  - 実 git push
  - external publish
  - memory promotion

確認:
  go test ./internal/domain/advisor ./internal/application/advisor ./internal/infrastructure/tools ./internal/domain/agent
```

### Task 3: Knowledge Relation MVP

```text
目的:
  Entity / Topic / Project でカテゴリ横断 recall できるようにする。

対象:
  internal/domain/knowledgerelation
  internal/application/knowledgerelation
  internal/infrastructure/persistence/conversation
  internal/domain/conversation

やること:
  - relation schema を追加
  - relation builder を追加
  - 1 hop recall を追加
  - RecallPack に RelationSnippet を optional 追加

やらないこと:
  - Neo4j 導入
  - 3 hop 以上探索
  - 全データ即時 backfill

確認:
  go test ./internal/domain/knowledgerelation ./internal/application/knowledgerelation ./internal/infrastructure/persistence/conversation ./internal/domain/conversation
```

### Task 4: Economic Objective MVP

```text
目的:
  Revenue を Opportunity 起点の安全な収益 loop にする。

対象:
  internal/domain/revenue
  internal/application/revenue
  internal/infrastructure/persistence/revenue
  internal/application/workstream

やること:
  - Opportunity / EconomicTask / EconomicReflection を追加
  - expected_profit 計算
  - approval_required 判定
  - Opportunity から Workstream Goal 作成

やらないこと:
  - 自動公開
  - 自動請求
  - 自動契約
  - 有料APIの無承認利用

確認:
  go test ./internal/domain/revenue ./internal/application/revenue ./internal/infrastructure/persistence/revenue ./internal/application/workstream
```

## 18. 実装時の注意

- `RenCrow_CORE` に未コミット変更がある場合、その file は必ず差分を読んでから触る。
- `picoclaw_multiLLM` は本作業の操作対象にしない。
- 新規横断ツールは `RenCrow_Tools` 側に置く。`RenCrow_CORE/tools/` へ増やさない。
- 仕様だけで runtime を変えない。runtime 接続は別 Phase で行う。
- すべての自律処理は最初 draft-only にする。
- Human approval gate を先に作ってから外部副作用へ接続する。

