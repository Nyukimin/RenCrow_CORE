# SuperAgent Harness Reference DeerFlow 仕様

## 1. 目的

本仕様は、ByteDance の DeerFlow を参考に、RenCrow へ取り込むべき SuperAgent Harness 設計原則を定義する。

DeerFlow は、long-horizon task を扱う open-source SuperAgent harness として公開されており、filesystem、memory、skills、sandbox-aware execution、sub-agent spawn などを備える構成として説明されている。

RenCrow では、DeerFlow をそのまま移植しない。

RenCrow は、人格、記憶、ローカル LLM、音声、Viewer、Workstream、収益化支援を重視する独自システムである。

したがって、本仕様では DeerFlow のうち、RenCrow に有用な設計要素だけを抽出し、RenCrow の既存仕様へ接続する。

---

## 2. DeerFlow から参考にする主要要素

RenCrow が参考にする要素は以下である。

```text
1. SuperAgent Harness という考え方
2. Lead Agent + Subagent 実行モデル
3. Sandbox + File System
4. Progressive Skill Loading
5. Context Engineering
6. Long-term Memory
7. Tool-call Recovery
8. Message Gateway / IM Channels
9. Tracing / Observability
10. Embedded Client / External Control
```

重要なのは、DeerFlow の実装や依存技術をそのまま採用することではない。

RenCrow が取り込むべきなのは、Agent に対して作業環境、記憶、隔離実行、Skill、Subagent、Trace を与える Harness 設計である。

---

## 3. RenCrow との違い

DeerFlow と RenCrow は、目指す方向が近いが、中心が異なる。

```text
DeerFlow:
  汎用 SuperAgent harness。
  research / coding / creation を長時間タスクとして処理する。

RenCrow:
  人格・記憶・関係性・ローカル LLM・Workstream・音声 / Viewer を重視する個人 AI 作業環境。
```

DeerFlow は、実装済みの汎用 Harness として参考になる。

RenCrow は、それをそのまま採用するのではなく、以下の RenCrow 固有設計と統合する。

```text
Chat / Worker / Coder / Heavy / Wild
Workstream Operating Loop
Tool Harness
DCI
Sandbox Promotion Gate
Agent Skill Governance
Persona Lore / Mutual Observation
Revenue Operating Principles
```

---

## 4. 全体構成

RenCrow における SuperAgent Harness 構成は以下とする。

```text
User / Chat
  ↓
Workstream Router
  ↓
Lead Agent
  ├─ Task Planner
  ├─ Skill Selector
  ├─ Context Builder
  ├─ Subagent Manager
  ├─ Sandbox Manager
  ├─ Tool Harness
  ├─ Memory / Vault
  └─ Result Synthesizer
        ↓
Subagents
  ├─ ResearchAgent
  ├─ CoderAgent
  ├─ DebugAgent
  ├─ UIAgent
  ├─ DataAgent
  ├─ RevenueAgent
  └─ ObserverAgent
        ↓
Artifacts / Reports / Patches / Memory Updates
        ↓
Human Review / Promotion Gate
```

---

## 5. SuperAgent Harness 原則

### 5.1 Harness とは何か

Harness は、単一の AI モデルそのものではない。

Harness は、AI エージェントが仕事を進めるための実行基盤である。

```text
Harness:
  Agent を起動し、
  Task を分解し、
  Tool を渡し、
  Memory を渡し、
  Sandbox を用意し、
  Subagent を管理し、
  結果を統合し、
  安全ゲートを通すもの。
```

RenCrow では、単なるマルチ LLM 会話ではなく、AI が実際に仕事をする Harness として設計する。

---

## 6. Lead Agent

### 6.1 目的

Lead Agent は、Workstream 内の作業全体を統括する。

RenCrow では、通常 Chat または Mio が Lead Agent の入口となる。

ただし、実行計画や Subagent 管理は Worker 側へ委譲してもよい。

### 6.2 責務

```text
- ユーザー意図を受け取る
- Workstream を特定する
- Goal Contract を確認する
- 必要な Skill を選ぶ
- Subagent が必要か判断する
- Sandbox が必要か判断する
- 作業を分解する
- 結果を統合する
- Human approval が必要な境界を判定する
```

### 6.3 Lead Agent が直接行わないこと

```text
- 破壊的操作
- main branch 直接変更
- 正式 DB への直接 write
- 外部送信
- Human approval が必要な判断の代行
```

---

## 7. Subagent Execution Model

### 7.1 目的

複雑な作業を単一 Agent にすべて抱え込ませず、専門 Subagent へ分解する。

RenCrow では、Lead Agent が必要に応じて Subagent を起動し、各 Subagent は scoped context、tools、termination condition を持つ。

Lead Agent は Subagent の生ログを抱え込まず、結果だけを統合する。

### 7.2 Subagent 種別

```text
ResearchAgent:
  調査、資料収集、比較

CoderAgent:
  コード調査、patch proposal

DebugAgent:
  ログ調査、原因分析

UIAgent:
  Viewer / HTML / UI artifact 確認

DataAgent:
  CSV / JSONL / DB / schema 分析

RevenueAgent:
  市場調査、商品導線、顧客の声分析

ObserverAgent:
  ユーザー観測、日報、Meta 更新候補

DocAgent:
  仕様書、README、報告書作成
```

### 7.3 Subagent の入力

```json
{
  "subagent_id": "sub_20260518_000001",
  "type": "ResearchAgent",
  "task": "競合LPを10件分析し、共通する訴求を抽出する",
  "scope": [
    "staging/market/",
    "vault/workstreams/revenue/"
  ],
  "tools": [
    "readFile",
    "rg",
    "browser_trace"
  ],
  "termination_condition": "分析レポートを作成したら終了",
  "output_format": "Subagent Report"
}
```

### 7.4 Subagent Report

```markdown
# Subagent Report

## Task
依頼された作業。

## Scope
見た範囲。

## Findings
- 発見1
- 発見2

## Evidence
- source_id / file_path / line / url

## Risks
- 不確実な点

## Recommendation
次に何をすべきか。

## Confidence
0.0 - 1.0
```

### 7.5 ルール

```text
- Subagent は必要最小限の context だけを持つ
- Subagent は scope 外を勝手に広げない
- Subagent は結果だけを Lead Agent へ返す
- Subagent の生ログを main context へ全量流さない
- Subagent は Tool Harness と Sandbox Gate を通す
```

---

## 8. Sandbox + File System

### 8.1 目的

Agent が試行錯誤するための隔離 workspace を用意する。

RenCrow では、Sandbox の詳細は `29_Sandbox_Promotion_Gate仕様.md` に従う。

### 8.2 Workspace 構成

```text
workspace/
  uploads/
  working/
  outputs/
  reports/
  logs/
  artifacts/
```

### 8.3 RenCrow での方針

```text
- 各 Workstream に workspace を持つ
- 各 Subagent に必要なら一時 workspace を持たせる
- sandbox 外への write は禁止
- workspace 成果物は Promotion Gate を通す
- outputs は Artifact Review Surface で確認する
```

---

## 9. Progressive Skill Loading

### 9.1 目的

すべての Skill を常時 context に入れず、必要な Skill だけを読み込む。

RenCrow では、`24_Agent_Skill_Governance仕様.md` に従って、Skill Registry と Skill Bootstrap を使う。

### 9.2 Skill Loading Flow

```text
1. Task intent を判定する
2. Skill Registry を検索する
3. 必須 Skill を選ぶ
4. Skill 概要だけ先に読む
5. 必要な詳細セクションだけ読む
6. 作業完了後、使用 Skill をログする
```

### 9.3 Skill 読み込み原則

```text
- Core Skill は必要時のみ展開する
- Project Skill は該当 Project だけで使う
- Skill 全文を常時プロンプトに入れない
- Skill 使用履歴を EventId で記録する
```

---

## 10. Context Engineering

### 10.1 目的

長時間・多段階タスクで context window を破裂させない。

RenCrow では、作業中の raw tool result、調査ログ、候補一覧、長文出力を main context に抱え込まず、File System、Vault、DB、Artifact へ逃がす。

### 10.2 Context Offload

```text
Main Context:
  今必要な Goal、直近判断、選択中の Evidence だけ

File System:
  中間メモ、調査ログ、候補一覧、長い出力

Vault:
  長期的に残す判断、状態、Open Loop

DB:
  検索・集計・再利用する構造化データ
```

### 10.3 Context Pack

Lead Agent は、Subagent や Worker に渡す context を最小化する。

```json
{
  "goal": "Tool HarnessのMVPを実装する",
  "relevant_specs": [
    "20_Tool_Harness_Contract_Mediation仕様",
    "29_Sandbox_Promotion_Gate仕様"
  ],
  "constraints": [
    "main branch直接変更禁止",
    "writeFile contentを自動改変しない"
  ],
  "current_artifacts": [
    "sandbox/ws_tool_harness/reports/scan.md"
  ]
}
```

### 10.4 Context 膨張時の処理

```text
- subtask 完了分を summary へ圧縮
- raw tool result は file へ保存
- main context には参照だけ残す
- Artifact 化できるものは Artifact へ逃がす
- 長期化したら Workstream Vault へ反映する
```

---

## 11. Long-term Memory

### 11.1 目的

Workstream やユーザーに関する安定した情報を、session をまたいで保持する。

RenCrow では、既存の記憶設計を優先する。

```text
user:<uid>
char:<persona>
workstream:<id>
kb:<domain>
source_registry
vault
```

### 11.2 DeerFlow から取り込む点

```text
- session をまたいだ memory
- preference / workflow / recurring context の保存
- duplicate fact の抑制
- local control
```

### 11.3 RenCrow 追加方針

```text
- 記憶候補は staging へ置く
- Source Registry / validator を通す
- user memory と character memory を分ける
- 観測者プロファイルは Human Review 必須
```

---

## 12. Tool-call Recovery

### 12.1 目的

LLM の tool-call sequence や provider 差異によって実行が壊れるのを防ぐ。

RenCrow では、Tool-call Recovery は `20_Tool_Harness_Contract_Mediation仕様.md` に統合する。

### 12.2 RenCrow 方針

```text
- tool call sequence を記録する
- dangling tool call を検出する
- provider 互換性エラーを分類する
- 修復可能なら placeholder result を返す
- 修復したことをログする
- 実ツール実行結果と placeholder を区別する
```

### 12.3 Event 例

```text
tool_call_sequence_recovered
dangling_tool_call_detected
placeholder_tool_result_inserted
provider_tool_protocol_error
```

---

## 13. Message Gateway / IM Channels

### 13.1 目的

Slack、Discord、LINE、メール、Web UI、Mobile など複数の入口から RenCrow を呼び出す。

RenCrow では、これを Message Gateway として設計する。

### 13.2 Gateway 構成

```text
IM / Web / Mobile / Voice
  ↓
Message Gateway
  ↓
Authentication / Channel Policy
  ↓
Workstream Router
  ↓
Chat / Lead Agent
```

### 13.3 Channel ごとの扱い

```text
Web:
  標準 UI

Mobile:
  remote control / approval / steering

Slack / Discord:
  Workstream 通知、壁打ち、軽い確認

Voice:
  思考入力、steering、日報素材

Email:
  draft 生成、返信候補

LINE:
  収益化・顧客導線では慎重に扱う
```

### 13.4 禁止

```text
- Human approval なしに外部送信
- 顧客への自動販売メッセージ
- 認証情報の露出
- private channel 内容の無分類 memory 化
```

---

## 14. Tracing / Observability

### 14.1 目的

RenCrow 内の Agent run、LLM call、tool execution、Subagent、Sandbox、Promotion を追跡可能にする。

RenCrow では、EventId 中心の観測性を標準とする。

### 14.2 追跡対象

```text
- Workstream run
- Lead Agent run
- Subagent run
- Skill trigger
- Tool call
- DCI trace
- Sandbox operation
- Promotion Gate
- Memory update
- Artifact update
```

### 14.3 EventId 連携

すべての trace は EventId を持つ。

```json
{
  "event_id": "evt_agent_20260518_000001",
  "parent_event_id": "evt_workstream_20260518_000001",
  "agent": "ResearchAgent",
  "tool_calls": 12,
  "tokens": 18320,
  "status": "completed"
}
```

### 14.4 外部 Observability 連携

将来、以下を検討する。

```text
- Langfuse 互換 export
- OpenTelemetry
- local trace viewer
- Workstream timeline
- Cost / token dashboard
```

---

## 15. Embedded Client / External Control

### 15.1 目的

外部スクリプトや CLI から RenCrow の Agent run を開始・監視・制御できるようにする。

RenCrow でも、将来的に Client API を持つ。

### 15.2 RenCrow Client 用途

```text
- Workstream 起動
- Subagent 起動
- Artifact 登録
- Heartbeat 登録
- Sandbox 作成
- Promotion request 作成
- Status 取得
```

### 15.3 API 例

```python
from rencrow_client import RenCrow

rc = RenCrow("http://localhost:8080")

run = rc.workstream.start(
    workstream_id="ws_revenue",
    goal="昨日のX投稿反応を分析して改善案を出す"
)

print(run.status)
```

---

## 16. Security Boundary

### 16.1 基本認識

SuperAgent Harness は、Tool、Sandbox、Message Channel、MCP、外部実行、Memory を扱うため、通常の Chat UI より強い安全境界が必要になる。

### 16.2 RenCrow 方針

```text
- Tool Harness を必ず通す
- Sandbox 外 write 禁止
- Promotion Gate 必須
- External message 送信は Human approval
- Secrets は読まない・保存しない
- Source Registry へ無審査昇格しない
- User memory へ無審査 upsert しない
- Channel ごとに権限を分ける
```

---

## 17. RenCrow への実装優先度

### 17.1 Phase 1: Lead Agent / Subagent 基盤

```text
- Subagent task format
- Subagent Report
- Lead Agent result synthesis
- EventId tracking
```

### 17.2 Phase 2: Sandbox Workspace

```text
- Workstream workspace
- uploads / working / outputs
- Sandbox Promotion Gate 連携
```

### 17.3 Phase 3: Progressive Skill Loading

```text
- Skill Registry
- Skill Bootstrap
- 必要 Skill のみ読み込み
```

### 17.4 Phase 4: Context Engineering

```text
- Context Pack
- Context offload to files
- Subtask summary
```

### 17.5 Phase 5: Message Gateway

```text
- Web
- Mobile
- Slack / Discord
- Voice
```

### 17.6 Phase 6: Observability

```text
- Event timeline
- Agent run trace
- Token / tool / sandbox dashboard
```

---

## 18. DB 設計

### 18.1 agent_run

```sql
CREATE TABLE IF NOT EXISTS agent_run (
  run_id TEXT PRIMARY KEY,
  workstream_id TEXT,
  parent_run_id TEXT,
  agent_type TEXT NOT NULL,
  goal TEXT,
  status TEXT NOT NULL,
  started_at TEXT NOT NULL,
  completed_at TEXT,
  summary TEXT
);
```

### 18.2 subagent_task

```sql
CREATE TABLE IF NOT EXISTS subagent_task (
  subagent_id TEXT PRIMARY KEY,
  parent_run_id TEXT NOT NULL,
  agent_type TEXT NOT NULL,
  task TEXT NOT NULL,
  scope TEXT,
  tools TEXT,
  termination_condition TEXT,
  output_path TEXT,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL,
  completed_at TEXT
);
```

### 18.3 context_pack

```sql
CREATE TABLE IF NOT EXISTS context_pack (
  context_pack_id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL,
  workstream_id TEXT,
  summary TEXT,
  included_sources TEXT,
  token_estimate INTEGER,
  created_at TEXT NOT NULL
);
```

### 18.4 message_channel

```sql
CREATE TABLE IF NOT EXISTS message_channel (
  channel_id TEXT PRIMARY KEY,
  channel_type TEXT NOT NULL,
  name TEXT,
  auth_scope TEXT,
  allowed_actions TEXT,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL
);
```

### 18.5 trace_event

```sql
CREATE TABLE IF NOT EXISTS trace_event (
  event_id TEXT PRIMARY KEY,
  parent_event_id TEXT,
  run_id TEXT,
  event_type TEXT NOT NULL,
  actor TEXT,
  payload_summary TEXT,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL
);
```

---

## 19. 設定ファイル案

```yaml
superagent_harness:
  enabled: true

  lead_agent:
    default: "Chat"
    allow_delegation: true

  subagents:
    enabled: true
    max_parallel: 4
    require_scope: true
    require_termination_condition: true
    return_summary_only: true

  sandbox:
    required_for_write: true
    workspace_root: "workspace/"
    promotion_gate_required: true

  skills:
    progressive_loading: true
    require_bootstrap: true

  context:
    max_context_pack_tokens: 3000
    offload_tool_results_to_file: true
    summarize_completed_subtasks: true

  message_gateway:
    enabled: true
    require_channel_policy: true
    external_send_requires_approval: true

  observability:
    event_id_required: true
    trace_agent_run: true
    trace_tool_call: true
    trace_subagent: true

  security:
    require_tool_harness: true
    deny_sandbox_escape: true
    deny_secret_access: true
    require_human_approval_for_external_effects: true
```

---

## 20. EventId

```text
superagent_run_started
superagent_run_completed
lead_agent_started
lead_agent_completed
subagent_spawned
subagent_completed
subagent_failed
context_pack_created
context_offloaded
skill_progressively_loaded
message_gateway_received
message_gateway_sent_draft
trace_event_recorded
tool_sequence_recovered
```

---

## 21. 禁止事項

```text
- Lead Agent がすべてを単独で抱え込む
- Subagent に無制限の文脈を渡す
- Subagent に無制限のツール権限を渡す
- Sandbox なしで write 作業を行う
- Promotion Gate なしで正式環境へ適用する
- Skill を常時全投入して context を汚染する
- External channel へ Human approval なしに送信する
- Trace なしに長時間 Agent run を実行する
- Secrets を message gateway や sandbox へ露出する
```

---

## 21.1 実装状況

MVP として、SuperAgent Harness の運用台帳は実装済み。

実装済み:

- `internal/domain/superagent` に `AgentRun` / `SubagentTask` / `ContextPack` / `MessageChannel` / `TraceEvent` と validation を追加。
- `SubagentTask` は `scope` と `termination_condition` を必須化。
- `ContextPack` は `max_context_pack_tokens` を超える記録を拒否。
- `internal/infrastructure/persistence/superagent` に JSONL store と SQLite store を追加。
- `superagent_harness.*` config を追加し、`return_summary_only` と `trace_agent_run` を有効時の必須条件にした。
- `superagent_harness.storage` / `sqlite_path` により、runtime で JSONL / SQLite store を切り替えられる。
- `/viewer/superagent` と各作成 API を追加。
- Viewer Ops に `SuperAgent Harness` summary を追加。
- external control Go client は SuperAgent / AI Workflow / Workstream Artifact / Sandbox / Promotion API に接続済み。
- external control から正式環境変更を行う場合は `SubmitPromotionWorkflow` を使い、Promotion Gate approve、明示 apply intent、Human approval、post-apply verification path が揃う場合だけ apply へ進む。
- `/viewer/ai-workflow/external-control/check` と `ai_workflow.external_control_*` policy により、actor / channel / action 単位で external control を判定できる。
- `SubmitPromotionWorkflow` は `external_control` 指定時、policy が `allowed` を返さない限り promotion request / apply へ進まない。
- `internal/application/superagent.RunController` により、local / distributed Lead Agent run の context を `run_id` ごとに登録できる。
- `/viewer/superagent/runs/pause` は台帳状態を `paused` に更新し、実行中 run がある場合は context cancellation を要求する。
- `/viewer/superagent/runs/resume` は pause marker を解除する bookkeeping として扱い、停止済み goroutine を自動再起動しない。

未実装 / 残作業:

- queue / scheduler レベルでの高度な再開制御。
- completed subtask のさらなる context offload / summary-only 圧縮。
- Worktree / Project Init Pack / Project Memory index との追加的な統合改善。

---

## 22. 成功指標

```text
subagent_task_success_rate
subagent_parallel_efficiency
context_pack_token_reduction
sandbox_usage_rate
promotion_gate_pass_rate
tool_sequence_recovery_count
skill_loading_precision
long_task_completion_rate
human_review_acceptance_rate
trace_coverage_rate
```

重要指標。

```text
- 長時間タスクの完了率
- Subagent 結果の採用率
- main context の肥大化抑制
- Sandbox 外 write 事故ゼロ
- Human approval なし外部送信ゼロ
```

---

## 23. 設計上の結論

DeerFlow から RenCrow が学ぶべき本質は、単一の高性能 Agent ではなく、**Harness が Agent に作業環境を与える**という設計である。

RenCrow では、以下を中核にする。

```text
Lead Agent が作業を統括する
Subagent が専門調査・実装・分析を分担する
Sandbox が安全な試行錯誤の場になる
Skill は必要時にだけ読み込む
Context はファイル・Vault・DB へ逃がす
Memory はセッションをまたいで残す
Message Gateway で複数入口を持つ
Trace で全作業を追跡する
```

RenCrow は DeerFlow をコピーしない。

DeerFlow の SuperAgent Harness 設計を参考にしつつ、RenCrow 固有の人格、記憶、Workstream、ローカル LLM、音声、Viewer、収益化支援へ統合する。

---

## 24. 参考リンク

- https://github.com/bytedance/deer-flow
- https://github.com/bytedance/deer-flow/blob/main/backend/README.md

---

## 25. まとめ

本仕様は、DeerFlow を参考にした RenCrow の SuperAgent Harness 設計を定義する。

カテゴリは以下である。

```text
SuperAgent Harness
Lead Agent
Subagent
Sandbox
Skill
Context Engineering
Memory
Message Gateway
Tracing
Security Boundary
```

最終原則は以下である。

```text
RenCrow は、単一の AI に全部やらせるのではなく、
Agent が働ける環境を Harness として整える。

強いモデルではなく、強い作業環境を作る。
```
