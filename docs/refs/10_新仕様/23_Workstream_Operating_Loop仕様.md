# Workstream Operating Loop 仕様

## 1. 目的

本仕様は、RenCrow において、会話、記憶、作業、成果物、定期監視を、単発のチャットではなく継続する作業単位として運用するための仕組みを定義する。

この仕組みを **Workstream Operating Loop** と呼ぶ。

RenCrow は、ユーザーからの 1 回の指示に 1 回だけ答える AI ではなく、重要な仕事ごとに専用の文脈、記憶、成果物、監視条件、次アクションを持つ作業ループとして動作する。

重要なのは、AI が単発回答を返すことではなく、仕事が途切れず継続する場所を作ることである。

## 2. 位置づけ

本仕様は、以下の仕様群の上位運用にあたる。

```text
19_DCI_直接コーパス探索仕様
  原文を調べ直す能力

20_Tool_Harness_Contract_Mediation仕様
  ツール呼び出しを検証、修復、安全実行する能力

21_AI_Native_Engineering_Workflow仕様
  Worker / Coder が働く開発環境を整える仕様

22_Revenue_Operating_Principles仕様
  RenCrow が収益化のためにどう動くかを定義する上位指針

23_Workstream_Operating_Loop仕様
  仕事ごとの継続スレッド、記憶、成果物、監視、再開を定義する仕様
```

19 から 21 番が作業能力や作業環境を定義し、22 番が収益化の上位方針を定義するのに対し、本仕様は **仕事が途切れず継続する単位** を定義する。

## 3. 基本思想

RenCrow では、重要な活動を単発タスクではなく Workstream として扱う。

```text
Workstream:
  継続的な目的を持つ作業単位。
  専用スレッド、専用記憶、成果物、未完了事項、定期監視、次アクションを持つ。
```

RenCrow における基本思想は以下である。

```text
1 回の会話ではなく、継続する仕事の流れを扱う。
会話履歴だけに頼らず、重要な記憶をファイル化する。
作業結果は成果物として残す。
成果物は人間が確認、注釈、修正指示できる形にする。
作業には成功条件を持たせる。
定期的に確認すべき仕事は Heartbeat で再起動する。
ユーザーは途中で方向修正できる。
長期スレッドが壊れても、Vault に記録が残るようにする。
```

## 4. Agent Autonomy Principle との関係

RenCrow では、AI エージェントの自律性をモデル性能だけで評価しない。

エージェントが自律的に動くためには、モデルに加えて、ツール、文脈、権限、検証条件、安全柵が必要である。

```text
AI エージェントを自律化するとは、
AI に自由を与えることではなく、
仕事に必要な道具、文脈、判断基準、停止条件を渡すことである。
```

Workstream Operating Loop は、このうち特に **Context**、**Verification**、**Safety Boundary** を担う。

- Workstream は、作業ごとの文脈、記憶、成果物、次アクションを保持する。
- Goal Contract は、成功条件と検証方法を明示する。
- Vault と Human review は、重要記憶や成果物の無審査確定を防ぐ。
- Steering Queue と Heartbeat は、継続作業を安全な checkpoint で前進させる。

この原則は、`21_AI_Native_Engineering_Workflow仕様.md` の Agent Autonomy Principle を本仕様に適用したものである。

## 5. Workstream の例

RenCrow では、以下のような単位を Workstream として扱う。

```text
RenCrow 開発
収益化
X 投稿
note 記事
Kindle 本
BipolarToBalance
AI の血脈
市場調査
顧客の声分析
商品設計
LP 作成
プロモーション
ローカル LLM 運用
UI / Viewer 改善
```

各 Workstream は、それぞれ専用の状態、記憶、成果物を持つ。

## 6. 全体構成

```text
User / Chat
  ↓
Workstream Router
  ↓
Workstream Thread
  ├─ Current Goal
  ├─ Workstream Memory
  ├─ Open Loops
  ├─ Artifact List
  ├─ Heartbeats
  ├─ Steering Queue
  └─ Status Dashboard
        ↓
Worker / Coder / Subagent
        ↓
Tool Harness / DCI / CLI / Browser / File
        ↓
Artifact Review Surface
        ↓
Human Annotation / Approval
        ↓
Vault Memory Update
        ↓
Next Loop
```

## 7. Workstream Thread

### 7.1 目的

Workstream Thread は、特定の継続作業に紐づく長期スレッドである。

重要な作業ごとに pinned thread に相当する継続スレッドを持ち、数か月にわたって履歴、好み、過去の判断、未完了事項を蓄積する。

### 7.2 Workstream Thread の役割

```text
- 作業の目的を保持する
- 現在の状態を保持する
- 重要な判断を参照する
- 未完了事項を追跡する
- 成果物に接続する
- 定期監視を持つ
- 中断後に再開できるようにする
```

### 7.3 Workstream Thread の例

```yaml
workstream:
  id: "ws_revenue_001"
  name: "収益化"
  description: "X / note / 商品設計 / 導線改善を継続管理する"
  owner: "ren"
  primary_agent: "Chat"
  support_agents:
    - "Market Research Agent"
    - "Content Strategy Agent"
    - "Product Design Agent"
    - "Metrics Analyst Agent"
  status: "active"
```

## 8. Workstream Vault

### 8.1 目的

Workstream Vault は、スレッド内で得られた重要情報を、会話履歴ではなくファイルとして保存する場所である。

RenCrow では、長期作業の記憶を会話履歴に閉じ込めず、Vault に書き出す。Vault は Git 管理し、差分を人間が確認できるようにする。

### 8.2 Vault 構成

```text
vault/
  TODO.md
  daily/
  people/
  projects/
  workstreams/
  revenue/
  rencrow/
  decisions/
  open_loops/
  artifacts/
  notes/
  agent/
```

### 8.3 Workstream ごとの構成

```text
vault/workstreams/{workstream_id}/
  README.md
  STATUS.md
  TODO.md
  DECISIONS.md
  OPEN_LOOPS.md
  ARTIFACTS.md
  HEARTBEATS.md
  NOTES.md
  MEMORY.md
```

### 8.4 STATUS.md

```markdown
# STATUS

## Current Goal
現在のゴール。

## Current State
現在の状態。

## Last Progress
最後に進んだこと。

## Blockers
詰まっていること。

## Next Action
次にやること。

## Last Updated
2026-05-18
```

### 8.5 DECISIONS.md

```markdown
# DECISIONS

## 2026-05-18
### Decision
決定事項。

### Reason
理由。

### Alternatives
検討した代替案。

### Impact
影響範囲。
```

### 8.6 OPEN_LOOPS.md

```markdown
# OPEN_LOOPS

## 未完了事項

- [ ] 市場調査 50 件を完了する
- [ ] 低単価商品の初稿を作る
- [ ] LP の CTA 文言を比較する
- [ ] 顧客アンケート項目を作る
```

### 8.7 Memory Diff Review

Vault は Git 管理する。AI が Vault を更新した場合、ユーザーは diff を確認できる。

```text
Agent updates vault
  ↓
git diff
  ↓
Human review
  ↓
accept / edit / reject
```

目的は、AI が会話履歴の中で曖昧に「覚えたつもり」になることを防ぐことである。

## 9. Voice Capture

### 9.1 目的

Voice Capture は、ユーザーの未整理な思考を、そのまま Workstream に取り込むための入力経路である。

RenCrow では、音声入力を単なる STT ではなく、編集前の思考素材の取得手段として扱う。

### 9.2 Voice Capture の用途

```text
- 作業方針の粗い説明
- 思いつき
- 違和感
- 途中の方向修正
- 記事や商品のアイデア
- 顧客との会話メモ
- 市場調査中の気づき
```

### 9.3 取り込み形式

```json
{
  "event_id": "evt_voice_20260518_000001",
  "workstream_id": "ws_revenue_001",
  "raw_transcript": "さっき見た投稿、AI副業じゃなくて導線設計の話にした方が刺さりそう...",
  "summary": "AI副業一般ではなく、導線設計を切り口にした方がよいという気づき",
  "candidate_actions": [
    "投稿案を作る",
    "Positioning Sheetを更新する"
  ],
  "created_at": "2026-05-18T12:00:00Z"
}
```

## 10. Steering Queue

### 10.1 目的

Steering Queue は、Agent 実行中にユーザーが追加した方向修正や次アクションを蓄積する仕組みである。

RenCrow では、これを Workstream 単位のキューとして扱う。

### 10.2 実行フロー

```text
Agent working
  ↓
User adds steering
  ↓
Steering Queue
  ↓
Next safe checkpoint
  ↓
Agent incorporates steering
  ↓
Queue item resolved
```

### 10.3 Steering Queue Item

```json
{
  "steering_id": "stq_20260518_000001",
  "workstream_id": "ws_lp_001",
  "target_artifact": "lp/index.html",
  "instruction": "ファーストビューの見出しをもう少し具体的にする",
  "priority": "normal",
  "status": "pending",
  "created_at": "2026-05-18T12:00:00Z"
}
```

### 10.4 適用タイミング

Steering は、以下の安全なタイミングで適用する。

```text
- tool call 完了後
- artifact 保存前
- patch proposal 作成前
- heartbeat 実行開始時
- agent がユーザー判断待ちになった時
```

破壊的操作の途中に割り込んで状態を壊してはいけない。

## 11. Thread-local Heartbeats

### 11.1 目的

Heartbeat は、Workstream Thread が定期的に再起動し、必要な確認や更新を行う仕組みである。

RenCrow では、Heartbeat を Workstream 単位に持たせる。

### 11.2 Heartbeat の例

```yaml
heartbeat:
  id: "hb_revenue_daily"
  workstream_id: "ws_revenue_001"
  schedule: "daily 08:00"
  task: "昨日の投稿反応、商品導線、顧客の声を確認し、今日の改善案を作る"
  status: "active"
```

### 11.3 用途

```text
X 投稿スレッド:
  毎朝、昨日の反応を確認して改善案を出す。

収益化スレッド:
  毎日、投稿反応、商品導線、購入者の声を整理する。

RenCrow 開発スレッド:
  Issue、PR、未完了タスクを確認する。

市場調査スレッド:
  競合投稿や新商品を定期確認する。

Kindle 本スレッド:
  章ごとの進捗と未完了箇所を確認する。
```

### 11.4 Heartbeat の制限

Heartbeat は自動実行できるが、以下を行ってはいけない。

```text
- ユーザー承認なしに商品を公開する
- ユーザー承認なしに投稿する
- ユーザー承認なしにメールを送る
- ユーザー承認なしに課金、返金処理をする
- ユーザー承認なしに本番 DB を破壊的変更する
```

Heartbeat は、原則として確認、要約、下書き、提案までとする。

## 12. Goal Contract

### 12.1 目的

Goal Contract は、Workstream 内の作業に対して、成功条件と検証方法を定義する仕組みである。

RenCrow では、重要な作業に必ず Goal Contract を付与する。

### 12.2 Goal Contract 形式

```yaml
goal:
  id: "goal_lp_001"
  workstream_id: "ws_revenue_001"
  title: "低単価商品のLPを作る"
  description: "AIエージェント活用テンプレ販売用のLPを作成する"
  success_criteria:
    - "誰向けの商品かがファーストビューで分かる"
    - "提供物が明記されている"
    - "価格が明記されている"
    - "CTAが3箇所ある"
    - "スマホ表示で崩れない"
  verification:
    - "HTMLをViewerで確認する"
    - "CTAリンクを確認する"
    - "チェックリストでレビューする"
  status: "active"
```

### 12.3 開発用 Goal 例

```yaml
goal:
  id: "goal_tool_harness_001"
  title: "Tool HarnessのMVPを実装する"
  success_criteria:
    - "optional null omission のテストが通る"
    - "json array string parse のテストが通る"
    - "writeFile content が自動改変されない"
    - "危険 shell command が拒否される"
    - "既存テストが通る"
  verification:
    - "go test"
    - "git diff review"
    - "manual safety checklist"
```

### 12.4 収益化用 Goal 例

```yaml
goal:
  id: "goal_low_ticket_001"
  title: "初回低単価商品を公開する"
  success_criteria:
    - "対象読者が明確である"
    - "悩みが1つに絞られている"
    - "購入後すぐ使える成果物がある"
    - "販売ページがある"
    - "購入後アンケートがある"
    - "成功保証表現がない"
  verification:
    - "Revenue Operating Principles checklist"
    - "Human approval"
```

## 13. Artifact Review Surface

### 13.1 目的

Artifact Review Surface は、AI が生成した成果物を、人間が確認、注釈、修正指示できる表示面である。

RenCrow では、Viewer を Artifact Review Surface として拡張する。

### 13.2 対象 Artifact

```text
Markdown:
  仕様書、note 下書き、Kindle 章、記事

HTML:
  LP、UI モック、診断ツール、小型アプリ

CSV / Spreadsheet:
  市場調査、投稿反応、商品比較、売上管理

PDF:
  配布教材、ワークシート、レポート

Slides / Slidev:
  セミナー資料、商品説明資料

Storybook:
  UI コンポーネント確認

Streamlit / Jupyter:
  データ分析、可視化
```

### 13.3 HTML 優先方針

小さな成果物は、Markdown だけでなく `index.html` として作ることを推奨する。

理由は以下である。

```text
- ブラウザで即確認できる
- 見た目を含めてレビューできる
- クリックや入力などの動作確認ができる
- LP や診断ツールに直結しやすい
- 単一ファイルならサーバ不要で耐久性が高い
```

### 13.4 Annotation

ユーザーは Artifact 上にコメントを残せる。

```json
{
  "annotation_id": "ann_20260518_000001",
  "artifact_id": "art_lp_001",
  "target": "hero_heading",
  "comment": "見出しが抽象的。誰向けかを入れる",
  "status": "open",
  "created_at": "2026-05-18T12:00:00Z"
}
```

Annotation は Steering Queue に接続できる。

## 14. Remote Control

### 14.1 目的

Remote Control は、RenCrow が作業している PC にユーザーがいなくても、スマホや別端末から確認、承認、方向修正できる仕組みである。

RenCrow では、Tailscale や Web UI を前提に、Remote Control を設計する。

### 14.2 基本形

```text
Worker PC / Mac:
  実作業、ファイル、ローカル LLM、ツール実行

Mobile / Browser:
  状態確認、承認、追加指示、Artifact 確認
```

### 14.3 許可する操作

```text
- Workstream status 確認
- Artifact 確認
- Steering 追加
- Heartbeat 確認
- Human approval
- task pause / resume
- report 確認
```

### 14.4 禁止する操作

```text
- モバイルからの危険コマンド直接実行
- 認証情報表示
- destructive operation の即時承認
- 未確認ファイルの公開
```

## 15. Workstream Status Dashboard

### 15.1 目的

Workstream Status Dashboard は、各 Workstream の状態を一覧する画面である。

### 15.2 表示項目

```text
Workstream 名
現在の Goal
最終更新
未完了タスク数
次アクション
Heartbeat 状態
未確認 Artifact 数
Human approval 待ち
リスク
```

### 15.3 例

```text
収益化:
  Goal: 初回低単価商品を作る
  Next: 競合商品30件の分析
  Open loops: 5
  Heartbeat: daily 08:00
  Approval: none

RenCrow 開発:
  Goal: Tool Harness MVP
  Next: readFile defaults test
  Open loops: 3
  Heartbeat: weekly
  Approval: patch proposal review

X 投稿:
  Goal: 30日投稿ネタ
  Next: 反応分析
  Open loops: 2
  Heartbeat: daily 07:30
  Approval: 投稿文確認
```

## 16. Memory Update Policy

### 16.1 更新対象

Workstream 内で以下が発生した場合、Vault 更新候補にする。

```text
- 重要な決定
- 未完了ループの追加、完了
- 人に関する安定情報
- プロジェクト状態の変化
- 顧客の声
- 成果物の完成
- Goal の変更
- 今後の注意点
```

### 16.2 直接更新してよいもの

```text
- TODO.md
- OPEN_LOOPS.md
- STATUS.md
- ARTIFACTS.md
```

ただし、変更は diff で確認できるようにする。

### 16.3 Human Review が必要なもの

```text
- DECISIONS.md
- MEMORY.md
- people/
- customer_voice
- 収益化に関わる実績
- 顧客の声
- 個人情報を含む記憶
```

## 17. Browser / Computer Use

### 17.1 目的

Browser / Computer Use は、Web surface や GUI アプリ上の作業を RenCrow が確認、操作するための拡張機能である。

RenCrow では、以下のように扱う。

```text
browser:
  ローカル HTML、Viewer、Storybook、LP、UI 確認

authenticated_browser:
  ログインが必要なサービス確認

computer:
  GUI でしか操作できない作業
```

### 17.2 安全原則

```text
- ログイン済みサービスでは読み取り、下書きを優先
- 投稿、送信、購入、返金は Human approval 必須
- GUI 操作ログを保存する
- 操作前後の状態を記録する
```

## 18. Workstream Lifecycle

### 18.1 状態

```text
draft:
  作成直後

active:
  稼働中

paused:
  一時停止

waiting:
  ユーザー判断待ち

heartbeat:
  定期監視中

completed:
  完了

archived:
  アーカイブ済み
```

### 18.2 状態遷移

```text
draft -> active
active -> waiting
active -> paused
active -> heartbeat
waiting -> active
paused -> active
active -> completed
completed -> archived
```

## 19. DB 設計

### 19.1 workstream

```sql
CREATE TABLE IF NOT EXISTS workstream (
  workstream_id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  description TEXT,
  status TEXT NOT NULL,
  primary_agent TEXT,
  vault_path TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT
);
```

### 19.2 workstream_goal

```sql
CREATE TABLE IF NOT EXISTS workstream_goal (
  goal_id TEXT PRIMARY KEY,
  workstream_id TEXT NOT NULL,
  title TEXT NOT NULL,
  description TEXT,
  success_criteria TEXT,
  verification TEXT,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL,
  completed_at TEXT
);
```

### 19.3 steering_queue

```sql
CREATE TABLE IF NOT EXISTS steering_queue (
  steering_id TEXT PRIMARY KEY,
  workstream_id TEXT NOT NULL,
  target_artifact_id TEXT,
  instruction TEXT NOT NULL,
  priority TEXT DEFAULT 'normal',
  status TEXT NOT NULL,
  created_at TEXT NOT NULL,
  applied_at TEXT
);
```

### 19.4 heartbeat_schedule

```sql
CREATE TABLE IF NOT EXISTS heartbeat_schedule (
  heartbeat_id TEXT PRIMARY KEY,
  workstream_id TEXT NOT NULL,
  schedule_text TEXT NOT NULL,
  task TEXT NOT NULL,
  status TEXT NOT NULL,
  last_run_at TEXT,
  next_run_at TEXT,
  created_at TEXT NOT NULL
);
```

### 19.5 artifact

```sql
CREATE TABLE IF NOT EXISTS artifact (
  artifact_id TEXT PRIMARY KEY,
  workstream_id TEXT NOT NULL,
  artifact_type TEXT NOT NULL,
  file_path TEXT,
  title TEXT,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT
);
```

### 19.6 artifact_annotation

```sql
CREATE TABLE IF NOT EXISTS artifact_annotation (
  annotation_id TEXT PRIMARY KEY,
  artifact_id TEXT NOT NULL,
  target TEXT,
  comment TEXT NOT NULL,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL,
  resolved_at TEXT
);
```

### 19.7 vault_update_log

```sql
CREATE TABLE IF NOT EXISTS vault_update_log (
  update_id TEXT PRIMARY KEY,
  workstream_id TEXT NOT NULL,
  file_path TEXT NOT NULL,
  update_type TEXT,
  content_hash_before TEXT,
  content_hash_after TEXT,
  review_status TEXT NOT NULL,
  created_at TEXT NOT NULL
);
```

## 20. 設定ファイル案

### 20.1 configs/workstream.yaml

```yaml
workstream:
  enabled: true

  vault:
    root: "vault/"
    git_review_required: true
    human_review_required:
      - "DECISIONS.md"
      - "MEMORY.md"
      - "people/"
      - "customer_voice/"

  threads:
    allow_long_running: true
    compact_when_needed: true
    serialize_memory_to_vault: true

  steering:
    enabled: true
    apply_at_safe_checkpoint: true

  heartbeat:
    enabled: true
    max_without_human_review: 3
    default_action: "draft_report_only"

  goals:
    require_success_criteria: true
    require_verification: true

  artifacts:
    prefer_html_for_interactive: true
    allow_markdown: true
    allow_spreadsheet: true
    allow_pdf: true
    allow_slides: true

  remote_control:
    enabled: true
    allow_mobile_review: true
    allow_destructive_action: false
```

## 21. EventId

Workstream 関連の主なイベント種別は以下である。

```text
workstream_created
workstream_activated
workstream_paused
workstream_completed
goal_created
goal_completed
steering_added
steering_applied
heartbeat_created
heartbeat_run_started
heartbeat_run_completed
artifact_created
artifact_reviewed
annotation_added
annotation_resolved
vault_update_proposed
vault_update_approved
vault_update_rejected
remote_control_connected
human_approval_requested
human_approval_granted
human_approval_rejected
```

## 22. MVP 実装順

### 22.1 Phase 1: Workstream Thread

- workstream DB 作成
- Workstream Router
- STATUS / TODO / OPEN_LOOPS 生成
- workstream 一覧表示

### 22.2 Phase 2: Vault Memory

- vault ディレクトリ作成
- Workstream ごとの README / STATUS / TODO
- Vault 更新ログ
- git diff review

### 22.3 Phase 3: Goal Contract

- goal 作成
- success criteria
- verification
- 完了判定

### 22.4 Phase 4: Artifact Review Surface

- artifact 登録
- Markdown / HTML artifact 表示
- annotation 追加
- Steering Queue 接続

### 22.5 Phase 5: Steering Queue

- 実行中の追加指示を蓄積
- safe checkpoint で反映
- 適用ログ保存

### 22.6 Phase 6: Heartbeats

- Workstream 単位の定期処理
- draft report 生成
- human review 待ち

### 22.7 Phase 7: Remote Control

- mobile / browser から status 確認
- approval
- steering 追加
- artifact 確認

## 23. 成功指標

```text
active_workstream_count
workstream_resume_success_rate
open_loop_close_rate
vault_update_review_rate
goal_completion_rate
artifact_review_count
steering_queue_resolution_rate
heartbeat_success_rate
human_approval_turnaround_time
```

## 23.1 実装状況

2026-05-18 時点で、MVP のうち以下は production code へ着手済みである。

```text
実装済み:
  - Workstream / Goal / Artifact / SteeringItem domain model
  - ArtifactAnnotation domain model
  - HeartbeatSchedule domain model
  - VaultUpdateLog domain model
  - Goal Contract validation
  - workstream JSONL / SQLite
  - workstream_goal JSONL / SQLite
  - artifact JSONL / SQLite
  - artifact_annotation JSONL / SQLite
  - steering_queue JSONL / SQLite
  - heartbeat_schedule JSONL / SQLite
  - vault_update_log JSONL / SQLite
  - Workstream Vault initial files
  - workstream.storage / sqlite_path runtime切替を含む workstream.* config
  - /viewer/workstreams GET / POST API
  - /viewer/workstreams/goals POST API
  - /viewer/workstreams/artifacts POST API
  - /viewer/workstreams/annotations POST API
  - /viewer/workstreams/steering POST API
  - /viewer/workstreams/heartbeats POST API
  - /viewer/workstreams/vault-updates POST API
  - /viewer/workstreams/vault-updates/review POST API
  - Annotation から Steering Queue への pending item 自動作成
  - HeartbeatService から Workstream HeartbeatSchedule を draft report only で実行
  - Workstream Heartbeat draft report を workspace/workstream_heartbeats/{workstream_id}/ に保存
  - Workstream Heartbeat 結果を VaultUpdateLog review_status=pending として記録
  - VaultUpdateLog の Human Review 結果を approved / rejected として追記保存
  - VaultUpdateLog review API は proposed_content 付き approved review の場合に vault_root 配下へ実ファイル適用
  - Viewer Ops で pending VaultUpdateLog の proposed_content preview と approve / reject 操作を表示
  - Workstream Heartbeat 開始時を safe checkpoint とし、同一 Workstream の pending Steering を draft prompt に反映したうえで `applied` として追記保存
  - Vault update の review pending 件数を Viewer Ops に表示
  - `append_status` / `append_todo` / `append_open_loop` / `append_decision` / `append_note` / `append_artifact` / `append_section` の update_type は、既存 Vault ファイルを保持したまま見出し付きで構造化追記する
  - Viewer Ops Workstream summary

残作業:
  - Vault diff review の詳細差分比較UI
```

特に重要な指標は以下である。

```text
作業再開時に文脈を失わない率
未完了ループの回収率
Vault に残った有効な記憶数
成果物レビューから修正完了までの速度
Goal 成功条件の達成率
```

## 24. 禁止事項

Workstream Operating Loop では、以下を禁止する。

```text
- 会話履歴だけに重要記憶を閉じ込める
- Heartbeat で勝手に公開、送信、販売する
- Goal に検証条件を持たせず実行する
- Artifact をユーザーが確認できない形で完成扱いする
- Vault に重要決定を無審査で書き込む
- 顧客情報や個人情報を無分類で記憶する
- Remote Control から危険操作を直接実行する
```

## 25. 設計上の結論

RenCrow は、単発のチャット応答ではなく、Workstream 単位で仕事を継続する。

重要なのは、AI が回答することではなく、仕事が途切れず進むことである。

そのために、RenCrow は以下を持つ。

```text
Durable Workstream Thread
Workstream Vault
Voice Capture
Steering Queue
Thread-local Heartbeat
Goal Contract
Artifact Review Surface
Remote Control
Memory Diff Review
Status Dashboard
```

これにより、RenCrow は「会話する AI」から、「仕事が住み続ける作業環境」へ進化する。

## 26. まとめ

本仕様は、RenCrow における継続作業の基本単位を定義する。

対象は以下である。

```text
Workstream
Thread
Vault
Goal
Artifact
Heartbeat
Steering
Remote Control
Memory Diff
Status Dashboard
```

この仕様により、RenCrow は以下を実現する。

```text
作業ごとに文脈を持つ
作業ごとに記憶を持つ
作業ごとに成果物を持つ
作業ごとに定期監視を持つ
作業ごとに再開できる
```

この Workstream Operating Loop を、RenCrow の長期運用における中核単位とする。
