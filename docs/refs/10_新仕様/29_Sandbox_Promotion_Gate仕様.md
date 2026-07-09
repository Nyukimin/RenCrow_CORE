# Sandbox Promotion Gate 仕様

## 1. 目的

本仕様は、RenCrow において、Worker / Coder / Subagent / DCI / Tool Harness が安全に試行錯誤できる隔離環境と、その成果を正式環境へ昇格するためのルールを定義する。

この仕組みを **Sandbox Promotion Gate** と呼ぶ。

Sandbox の目的は、AI エージェントが本体環境、main branch、共有環境、正式 DB を壊さずに、調査、試作、検証、patch proposal、artifact 生成を行えるようにすることである。

ただし、Sandbox 内で成功したことは、そのまま正式環境へ適用してよいことを意味しない。

正式環境へ出すには、必ず Promotion Gate を通す。

```text
Sandbox = 試す場所
Promotion Gate = 採用してよいか判断する場所
Main / Official Workspace = 採用済みだけが入る場所
```

## 2. 基本原則

RenCrow では、以下を基本原則とする。

```text
1. AI は本体環境を直接変更しない。
2. 試作、調査、修正案は Sandbox または worktree で行う。
3. Sandbox 内でも危険操作は Command Gate を通す。
4. Sandbox の成果は diff / report / test result として提出する。
5. 正式環境への適用は Human approval 後に行う。
6. Sandbox 内の成功を、正式環境での安全性と同一視しない。
7. 共有環境、認証情報、venv、site-packages、CUDA、OS 設定などは原則変更しない。
8. 正式環境へ昇格する前に、rollback plan を用意する。
```

## 3. 位置づけ

本仕様は、以下の仕様と接続する。

```text
20_Tool_Harness_Contract_Mediation仕様
  tool call を検証、修復、安全実行する。

21_AI_Native_Engineering_Workflow仕様
  worktree、Project Init、AI 開発環境を扱う。

23_Workstream_Operating_Loop仕様
  Goal、Artifact、Human approval を扱う。

24_Agent_Skill_Governance仕様
  Skill、PR、Human diff approval を扱う。

29_Sandbox_Promotion_Gate仕様
  Sandbox 内の試行結果を正式環境へ昇格する条件を定義する。
```

## 4. Sandbox の定義

Sandbox とは、AI エージェントが安全に試行錯誤するための隔離作業領域である。

RenCrow では、以下を Sandbox として扱う。

```text
- Git worktree
- 一時 workspace
- Docker / container
- temp directory
- isolated test project
- copy of target files
- generated artifact directory
- local preview environment
```

Sandbox は、本体環境と明確に分離する。

## 5. Sandbox の種類

### 5.1 Code Sandbox

コード修正、patch proposal、テスト実行のための Sandbox。

```text
用途:
- バグ修正案
- リファクタ案
- 性能改善案
- テスト追加
- 仕様実装の試作
```

推奨形:

```text
git worktree
```

### 5.2 Artifact Sandbox

HTML、Markdown、PDF、スライド、CSV、画像、LP などの成果物を作る Sandbox。

```text
用途:
- index.html
- LP
- UI mock
- 診断ツール
- note 下書き
- Kindle 章
- Slidev 資料
```

### 5.3 Data Sandbox

staging データ、サンプルデータ、API trace、変換処理を試す Sandbox。

```text
用途:
- JSONL 変換
- Parquet 生成
- API candidate 検証
- schema inference
- customer_voice 分類
- market research 集計
```

正式 DB へ直接 write してはいけない。

### 5.4 Browser Sandbox

Web 操作、Browser Trace、UI 確認、DOM 検証のための Sandbox。

```text
用途:
- local preview
- Viewer 確認
- Storybook 確認
- index.html 確認
- Browser Trace 取得
```

ログイン済みサービスを扱う場合は、追加の Safety Gate を必要とする。

### 5.5 Execution Sandbox

shell、script、test、benchmark を実行する Sandbox。

```text
用途:
- pytest
- npm test
- build
- read-only script
- benchmark fixture
```

共有環境に副作用を出す操作は禁止する。

## 6. Sandbox 内で許可する操作

Sandbox 内では、通常環境より広い試行錯誤を許可できる。

ただし、Command Gate は常に有効とする。

```text
許可:
- ファイル読み取り
- Sandbox 内ファイル作成
- Sandbox 内ファイル編集
- grep / rg / fd / jq
- git diff
- test 実行
- build 実行
- preview 生成
- artifact 生成
- patch proposal 生成
- 一時ファイル作成
```

## 7. Sandbox 内でも禁止する操作

Sandbox 内であっても、以下は禁止する。

```text
- rm -rf などの破壊的削除
- 共有環境の変更
- OS 設定変更
- 権限変更
- chmod / chown
- git push
- main branch への直接 commit
- production DB への write
- user:<uid> memory への直接 upsert
- Source Registry への無審査 promote
- secrets 読み取り
- 認証情報の保存
- 外部への無承認送信
- npm install / pip install の無断実行
- CUDA / Python / venv / site-packages の共有環境変更
```

必要な場合は、Human approval と rollback plan を必須とする。

## 8. Sandbox Workspace 構成

Sandbox は、Workstream ごとに分離する。

```text
sandbox/
  {workstream_id}/
    README.md
    input/
    workspace/
    output/
    reports/
    logs/
    diff/
    tests/
```

### 8.1 input/

元データ、コピー元、対象ファイルのスナップショットを置く。

### 8.2 workspace/

AI が実際に作業する場所。

### 8.3 output/

生成物を置く。

### 8.4 reports/

調査レポート、検証結果、リスク評価を置く。

### 8.5 logs/

実行ログ、tool log、test log を置く。

### 8.6 diff/

正式環境へ昇格候補となる diff を置く。

### 8.7 tests/

追加テスト、検証スクリプト、benchmark fixture を置く。

## 9. Sandbox 開始フロー

```text
1. Workstream / Goal を確認する
2. Sandbox が必要か判定する
3. Sandbox を作成する
4. 対象ファイル、対象データをコピーまたは worktree 化する
5. Sandbox metadata を記録する
6. AI エージェントが作業する
7. 結果を report / diff / artifact として出す
```

## 10. Sandbox Metadata

```json
{
  "sandbox_id": "sbx_20260518_000001",
  "workstream_id": "ws_rencrow_001",
  "goal_id": "goal_tool_harness_001",
  "type": "code",
  "path": "sandbox/ws_rencrow_001/sbx_20260518_000001",
  "base_ref": "main@abc123",
  "created_by": "Worker",
  "status": "active",
  "created_at": "2026-05-18T12:00:00Z"
}
```

## 11. Sandbox 成果物

Sandbox 作業は、以下のいずれかを成果物として出す。

```text
- Sandbox Report
- Diff
- Patch Proposal
- Test Result
- Benchmark Result
- Artifact
- Risk Assessment
- Rollback Plan
```

## 12. Sandbox Report 形式

```markdown
# Sandbox Report

## Sandbox
- sandbox_id:
- workstream_id:
- goal_id:
- type:
- base_ref:

## Purpose
何を試したか。

## Changes
何を変更したか。

## Result
何が分かったか。

## Diff
対象 diff の場所。

## Tests
実行したテスト。

## Risks
正式環境へ適用する場合のリスク。

## Rollback Plan
戻し方。

## Recommendation
昇格する / 保留する / 破棄する。
```

## 13. Promotion Gate

Promotion Gate は、Sandbox 内の成果を正式環境へ適用してよいか判定するゲートである。

```text
Sandbox Output
  ↓
Promotion Gate
  ↓
Human Approval
  ↓
Apply to Worktree / Official Workspace
  ↓
Post-apply Verification
```

## 14. Promotion 条件

Sandbox 外へ適用するには、以下を満たす必要がある。

```text
- 変更内容の diff がある
- 変更理由が説明されている
- 対象ファイルが明示されている
- Goal Contract の成功条件に合っている
- 既存テストまたは最低限の検証が通っている
- 必要なら追加テストがある
- 破壊的操作を含まない
- secrets / env / 認証情報に触れていない
- 共有環境を変更していない
- rollback plan がある
- Human approval がある
```

## 15. Promotion 禁止条件

以下に該当する場合、正式環境へ適用してはいけない。

```text
- diff がない
- 変更理由がない
- テスト結果がない
- Goal と関係ない変更が混ざっている
- 複数目的が混ざっている
- 依存関係を勝手に追加している
- 環境変数や共有環境を変更している
- secrets に触れている
- 認証情報を保存している
- generated file と手書きコードの区別が曖昧
- Sandbox 内でしか動作確認していない
- rollback plan がない
- Human approval がない
```

## 16. Promotion 判定レポート

```markdown
# Promotion Gate Report

## Decision
- approve
- reject
- needs_review
- needs_more_tests

## Reason
判定理由。

## Checklist
- [ ] diff あり
- [ ] 変更理由あり
- [ ] Goal と一致
- [ ] test あり
- [ ] rollback plan あり
- [ ] secrets なし
- [ ] destructive operation なし
- [ ] Human approval あり

## Required Before Promotion
追加で必要なこと。

## Apply Target
適用先。

## Post-apply Verification
適用後に行う確認。
```

### 16.1 Promotion diff 実適用

Promotion diff の実適用は、`sandbox.promotion.apply_root` が明示されている場合に限り有効とする。

```text
条件:
- Promotion Gate が approve
- human_approved=true
- post_apply_verification_path がある
- diff_path が Sandbox root 配下にある
- apply_root が明示されている
- patch 対象が apply_root 配下の既存ファイルである
```

MVP で実適用してよい diff は、既存テキストファイルへの unified diff に限定する。

以下は拒否する。

```text
- absolute path
- path traversal
- .env / *.pem / *.key
- .git / secrets / private
- rename
- new file
- delete
- binary diff
- hunk の context / delete が一致しない diff
```

post-apply verification が失敗した場合、`promotion_applied` gate log と completed artifact を保存してはいけない。

`sandbox.promotion.apply_root` 未設定時の `/viewer/sandbox/promotions/apply` は checkpoint-only とし、正式適用完了とは扱わない。

## 17. Human Approval

正式環境への昇格は Human approval を必要とする。

Human approval なしで昇格してよいのは、以下のような低リスク成果物に限る。

```text
- reports/ への保存
- sandbox output の整理
- draft artifact の追加
- staging candidate の作成
```

以下は必ず Human approval が必要。

```text
- source code 変更
- config 変更
- DB schema 変更
- 本番運用に影響する変更
- 依存関係追加
- 公開物の変更
- 顧客向け文面
- memory / meta profile の確定更新
```

## 18. Apply 先

Sandbox 成果の適用先は、原則として以下の順に制限する。

```text
1. 別 worktree
2. feature branch
3. staging workspace
4. official workspace
5. main branch
```

main branch への直接適用は禁止する。

main branch へ入れる場合は、PR / review / test を通す。

## 19. Post-apply Verification

正式環境へ適用した後、必ず再検証を行う。

```text
- git diff 確認
- 既存テスト実行
- 追加テスト実行
- build 確認
- artifact 表示確認
- Goal Contract 再確認
- rollback 可能性確認
```

Sandbox 内で通ったテストだけでは不十分な場合がある。

正式環境での再検証を必須とする。

## 20. Rollback Plan

Promotion 前に rollback plan を用意する。

```markdown
# Rollback Plan

## 対象
変更対象。

## 戻し方
git revert / patch reverse / file restore など。

## 影響範囲
戻した場合に影響するもの。

## 確認方法
rollback 後に確認するテスト。
```

## 21. Worktree との関係

コード変更を伴う Sandbox は、原則として git worktree で作成する。

```text
main:
  直接変更禁止

worktree:
  AI 作業用

promotion:
  diff を review して PR 化
```

## 22. Artifact との関係

HTML、Markdown、PDF、Slides などの成果物は、Sandbox 内で生成し、Artifact Review Surface で確認する。

```text
Sandbox Artifact
  ↓
Artifact Review
  ↓
Annotation / Steering
  ↓
Revised Artifact
  ↓
Promotion Gate
```

## 23. Staging との関係

Data Sandbox で生成されたデータは、正式 DB へ直接投入しない。

```text
Sandbox data
  ↓
staging
  ↓
validator
  ↓
Source Registry / official DB
```

## 24. Tool Harness との関係

Sandbox 内の tool call も、必ず Tool Harness を通す。

```text
Agent
  ↓
Tool Harness
  ↓
Command Gate
  ↓
Sandbox Tool Execution
```

Sandbox だからといって、安全ゲートを省略してはいけない。

## 25. Command Gate 追加ルール

Sandbox 内では、一部 write 操作を許可できる。

ただし、許可範囲は Sandbox root 配下に限定する。

```text
write_safe:
  sandbox root 配下のみ許可

write_outside_sandbox:
  拒否

destructive:
  拒否または Human approval 必須
```

## 26. Path Guard

すべての書き込み先は、Sandbox root 配下でなければならない。

拒否例:

```text
../
../../
~/
C:\Users\
/etc/
/usr/
/System/
.env
secrets/
```

## 27. DB 設計

### 27.1 sandbox_registry

```sql
CREATE TABLE IF NOT EXISTS sandbox_registry (
  sandbox_id TEXT PRIMARY KEY,
  workstream_id TEXT,
  goal_id TEXT,
  sandbox_type TEXT NOT NULL,
  path TEXT NOT NULL,
  base_ref TEXT,
  created_by TEXT,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL,
  closed_at TEXT
);
```

### 27.2 sandbox_artifact

```sql
CREATE TABLE IF NOT EXISTS sandbox_artifact (
  artifact_id TEXT PRIMARY KEY,
  sandbox_id TEXT NOT NULL,
  artifact_type TEXT NOT NULL,
  file_path TEXT NOT NULL,
  title TEXT,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL
);
```

### 27.3 sandbox_promotion_request

```sql
CREATE TABLE IF NOT EXISTS sandbox_promotion_request (
  promotion_id TEXT PRIMARY KEY,
  sandbox_id TEXT NOT NULL,
  workstream_id TEXT,
  goal_id TEXT,
  requested_by TEXT,
  target_path TEXT,
  diff_path TEXT,
  test_result_path TEXT,
  risk_level TEXT,
  rollback_plan_path TEXT,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL,
  reviewed_at TEXT
);
```

### 27.4 promotion_gate_log

```sql
CREATE TABLE IF NOT EXISTS promotion_gate_log (
  event_id TEXT PRIMARY KEY,
  promotion_id TEXT NOT NULL,
  gate_status TEXT NOT NULL,
  reason TEXT,
  human_approval_status TEXT,
  applied_at TEXT,
  post_apply_verification TEXT,
  created_at TEXT NOT NULL
);
```

## 28. 設定ファイル案

```yaml
sandbox:
  enabled: true

  root: "sandbox/"

  default:
    require_promotion_gate: true
    require_human_approval: true
    require_rollback_plan: true

  types:
    code:
      use_worktree: true
      allow_write: true
      require_tests: true

    artifact:
      allow_write: true
      require_review: true

    data:
      allow_write: true
      promote_to_staging_only: true

    browser:
      allow_trace: true
      require_redaction: true

    execution:
      allow_tests: true
      allow_benchmark: false

  safety:
    deny_outside_sandbox_write: true
    deny_secret_paths: true
    deny_shared_environment_change: true
    deny_dependency_install_without_approval: true

  promotion:
    require_diff: true
    require_reason: true
    require_goal_match: true
    require_test_result: true
    require_human_approval: true
    require_post_apply_verification: true
    apply_root: "../worktrees/rencrow-feature"
```

## 29. EventId

```text
sandbox_created
sandbox_closed
sandbox_file_written
sandbox_test_started
sandbox_test_completed
sandbox_report_created
promotion_requested
promotion_gate_started
promotion_gate_approved
promotion_gate_rejected
promotion_applied
post_apply_verification_started
post_apply_verification_completed
rollback_plan_created
rollback_executed
```

## 30. MVP 実装順

### 30.1 Phase 1: Sandbox Registry

- sandbox root 作成
- sandbox_registry DB 作成
- sandbox 作成 / close
- sandbox metadata 保存

### 30.2 Phase 2: Path Guard

- Sandbox root 外への write 禁止
- denylist 適用
- path traversal 拒否

### 30.3 Phase 3: Sandbox Report

- report template 作成
- diff / test / risk / rollback plan 出力

### 30.4 Phase 4: Promotion Gate

- promotion request 作成
- checklist 判定
- Human approval 状態管理

### 30.5 Phase 5: Worktree 連携

- code sandbox を worktree 化
- main 直接変更禁止
- feature branch 運用

### 30.6 Phase 6: Post-apply Verification

- 適用後テスト
- diff 確認
- rollback plan 確認

## 30.7 実装状況

2026-05-18 時点で、MVP のうち以下は production code へ着手済みである。

```text
実装済み:
  - PromotionRequest / PromotionGateDecision domain model
  - 必須項目不足 / human rejected / human approval granted の Gate 判定
  - sandbox root path guard
  - sandbox root 外 file_write 拒否
  - sandbox.* config
  - sandbox.storage / sqlite_path による runtime store 切替
  - sandbox registry JSONL / SQLite persistence
  - sandbox artifact JSONL / SQLite persistence
  - sandbox promotion request JSONL / SQLite persistence
  - promotion gate log JSONL / SQLite persistence
  - /viewer/sandbox API
  - /viewer/sandbox/promotions API
  - /viewer/sandbox/promotions/apply API
  - promotion request 作成時の rollback plan sandbox artifact 登録
  - promotion request 作成時の post-apply verification sandbox artifact 登録
  - promotion_gate_log.post_apply_verification の保存
  - 承認済み promotion の apply checkpoint 記録
  - apply checkpoint 記録時の completed post-apply verification artifact 登録
  - post_apply_verification_command 指定時の Worker ToolRunner 経由 verification 実行
  - verification command 結果の sandbox root 配下証跡ファイル保存
  - verification command 失敗時の apply checkpoint 成功扱い禁止
  - sandbox.promotion.apply_root 明示時の unified diff 実適用
  - promotion diff 実適用時の secret / path traversal / .git / rename / new / delete / binary diff 拒否
  - diff context mismatch 時の部分 write 防止
  - sandbox.promotion.apply_root 明示時の unified diff reverse rollback 実行
  - /viewer/sandbox/promotions/rollback API
  - /viewer/sandbox/promotions/preview API
  - promotion diff の file / hunk / row 単位 preview
  - Viewer Ops での side-by-side diff preview 表示
  - promotion diff preview での risk_flags / requires_manual_review 表示
  - 依存ファイル / DB migration diff の自動 apply / rollback 拒否
  - rename / new / delete / binary diff の preview 時 manual review 判定
  - /viewer/sandbox/promotions/manual-review API
  - high-risk promotion の Workstream Goal / pending_review Artifact / needs_review gate log への分岐
  - Viewer Ops の Manual Review 操作
  - rollback 実行時の Human approval / 証跡 path 必須化
  - rollback_executed gate log と rollback_execution artifact 保存
  - pkg/rencrowclient からの promotion apply / rollback 呼び出し
  - AI Workflow WorktreeManager と連携した code worktree sandbox 作成
  - /viewer/sandbox/worktrees/create API
  - /viewer/sandbox/worktrees/close API
  - Viewer Ops Sandbox Gate 表示

残作業:
  - Sandbox Promotion Gate MVP としての高リスク promotion 分岐は実装済み。外部 PR 実作成や migration 専用レビュー様式の詳細化は、Skill Governance / PR workflow 側の後続作業で扱う。
```

## 31. 成功指標

```text
sandbox_usage_count
sandbox_to_promotion_rate
promotion_approval_rate
promotion_rejection_rate
post_apply_test_pass_rate
rollback_count
outside_sandbox_write_block_count
main_direct_write_count
shared_environment_change_block_count
```

特に重要な指標:

```text
Sandbox 外 write 事故 = 0
main branch 直接変更 = 0
Human approval なし promotion = 0
rollback 不能な promotion = 0
共有環境破壊 = 0
```

## 32. 禁止事項

```text
- Sandbox で成功したという理由だけで正式適用する
- diff なしで昇格する
- テストなしで昇格する
- rollback plan なしで昇格する
- Human approval なしでコード変更を適用する
- main branch を直接変更する
- Sandbox root 外へ write する
- 共有環境を AI 判断だけで変更する
- secret や .env を Sandbox へコピーする
- staging を飛ばして official DB へ write する
```

## 33. 設計上の結論

RenCrow では、AI エージェントの試行錯誤を許可する。

ただし、それは隔離された Sandbox 内に限る。

Sandbox の成果を正式環境へ適用するには、diff、理由、テスト、リスク、rollback plan、人間承認を必要とする。

最終原則は以下である。

```text
AI は Sandbox で自由に試してよい。
しかし、Sandbox 外へ出すには証拠と承認が必要である。
```

この仕様により、RenCrow は AI の探索力と安全な本番運用を両立する。
