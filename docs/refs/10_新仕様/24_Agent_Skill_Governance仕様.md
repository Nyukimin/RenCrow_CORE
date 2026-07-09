# Agent Skill Governance 仕様

## 1. 目的

本仕様は、RenCrow における Skill / Command / Plugin / Agent Rule の管理方針、起動条件、変更手順、評価、外部貢献時のガードレールを定義する。

本仕様では、この仕組みを **Agent Skill Governance** と呼ぶ。

RenCrow では、Skill を単なるプロンプト断片や便利テンプレートとして扱わない。

Skill は、Worker / Coder / Subagent / DCI / Revenue Agent などの行動を変える実行規約であり、実質的には「エージェント行動を制御するコード」に近いものとして扱う。

そのため、Skill の追加、変更、削除には、目的、適用範囲、評価、Human approval、変更履歴を必要とする。

## 2. 背景

AI コーディングエージェントは、指示が曖昧なままでも自信ありげに作業を進めることがある。

その結果、以下の問題が起きる。

```text
- 実在しない問題を修正しようとする
- 既存 Issue や PR を調べずに重複作業をする
- core に入れるべきでない変更を core へ入れる
- project-specific な変更を一般仕様として扱う
- 人間が diff を確認する前に PR を出す
- skill 文面を軽く書き換えて、エージェント行動を壊す
- 複数の無関係な変更を 1 つの PR に混ぜる
- 説明や実績を捏造する
```

RenCrow では、Skill 運用、外部貢献、内部実装、PR 作成の共通ガードレールとして本仕様を採用する。

## 3. 位置づけ

本仕様は、以下の仕様と接続する。

```text
19_DCI_直接コーパス探索仕様
  原文、コード、ログを調べ直す能力

20_Tool_Harness_Contract_Mediation仕様
  tool call を検証、修復、安全実行する能力

21_AI_Native_Engineering_Workflow仕様
  Worker / Coder が働く開発環境を整える仕様

23_Workstream_Operating_Loop仕様
  継続作業、Vault、Goal、Artifact を管理する仕様

24_Agent_Skill_Governance仕様
  Skill / Command / Plugin / PR 行動規律を管理する仕様
```

19 から 21 番が実行能力や作業環境を定義するのに対し、本仕様は **AI エージェントが使う行動規約そのものをどう管理するか** を定義する。

## 4. 基本思想

RenCrow では、以下を基本思想とする。

```text
Skill は文章ではなく、行動制御である。
Skill の変更は、エージェントの挙動変更である。
Skill は置くだけでは不十分で、起動されなければ意味がない。
汎用 Skill と Project 固有 Skill を混ぜない。
Core に入れる変更は慎重に扱う。
PR や外部貢献は、人間の信用を背負う行為として扱う。
AI は人間の評判を守る側に立つ。
```

RenCrow では、この姿勢を Coder の基本ルールとする。

## 5. Agent Autonomy Principle との関係

RenCrow では、AI エージェントの自律性をモデル性能だけで評価しない。

エージェントが自律的に動くためには、モデルに加えて、ツール、文脈、権限、検証条件、安全柵が必要である。

```text
AI エージェントを自律化するとは、
AI に自由を与えることではなく、
仕事に必要な道具、文脈、判断基準、停止条件を渡すことである。
```

Agent Skill Governance は、このうち特に **Context**、**Authority**、**Verification**、**Safety Boundary** を担う。

- Skill は、作業時に参照すべき文脈、手順、判断基準を与える。
- Skill Bootstrap は、該当 Skill が存在するのに使われない状態を防ぐ。
- PR / Contribution Gate は、外部提出の Authority と Human approval 境界を定義する。
- Skill Change Evaluation は、Skill 変更による Agent 行動の劣化を検証する。
- Fabrication Prevention は、証拠のない主張や未実行テスト報告を止める。

この原則は、`21_AI_Native_Engineering_Workflow仕様.md` の Agent Autonomy Principle を本仕様に適用したものである。

## 6. 用語定義

### 6.1 Skill

特定の作業を行うための手順、判断基準、出力形式、禁止事項をまとめた再利用可能な行動単位。

```text
例:
- architecture-review
- refactor-safety
- writing-spec
- dci-search
- revenue-market-research
```

### 6.2 Command

ユーザーまたは Agent が明示的に呼び出せる、定型ワークフロー。

```text
例:
- /review-architecture
- /generate-tests
- /security-audit
- /dci-search
- /prepare-pr
```

### 6.3 Plugin

複数の Skill、Command、設定、外部接続、ツール定義をまとめた拡張パッケージ。

```text
例:
- rencrow-revenue-plugin
- rencrow-ui-review-plugin
- rencrow-obsidian-plugin
```

### 6.4 Core Skill

RenCrow 全体で使う汎用 Skill。

特定のプロジェクト、ドメイン、ツール、個人ワークフローに依存しない。

### 6.5 Project Skill

特定プロジェクト専用の Skill。

```text
例:
- RenCrow 固有の Viewer 設計レビュー
- 背徳ごはん用レシピ投稿生成
- BipolarToBalance 章構成レビュー
```

### 6.6 Skill Bootstrap

会話開始時または作業開始時に、Agent が利用可能な Skill を確認し、該当 Skill を必ず使うための初期化処理。

Skill は存在するだけでは不十分であり、Bootstrap により自動起動または起動候補提示される必要がある。

## 7. Core / Plugin / Project の分離

RenCrow では、Skill を以下の 3 層に分ける。

```text
core:
  全 Agent / 全 Workstream で使える汎用 Skill

plugin:
  特定分野、特定ツール、特定ワークフロー向けの拡張 Skill 群

project:
  特定リポジトリ、特定商品、特定記事シリーズ向けの局所 Skill
```

### 7.1 Core に入れてよいもの

```text
- どのプロジェクトでも使う開発規律
- 安全なリファクタ手順
- PR 前チェック
- 仕様レビュー
- テスト生成方針
- DCI 探索手順
- Tool Harness 確認
- Human approval ルール
```

### 7.2 Core に入れてはいけないもの

```text
- 特定商品だけに使う導線
- 特定 SNS だけの投稿テンプレート
- 特定外部サービス前提の Skill
- 特定顧客向けワークフロー
- れん個人の一時的な好み
- 特定リポジトリの設定
```

特定プロジェクト、特定チーム、特定ドメイン、特定ワークフローにしか役立たない Skill や設定は core に入れず、plugin または project に分離する。

## 8. Skill Registry

### 8.1 目的

Skill Registry は、RenCrow が利用可能な Skill を登録、検索、起動判定するための索引である。

### 8.2 ディレクトリ構成

```text
skills/
  core/
    architecture-review/
      SKILL.md
      evals/
      examples/

    refactor-safety/
      SKILL.md
      checklists/

    pr-readiness/
      SKILL.md
      templates/

  plugins/
    revenue/
      market-research/
      funnel-review/

    ui/
      viewer-review/
      html-artifact-review/

  projects/
    rencrow/
      viewer-specific-review/

    bipolar-to-balance/
      chapter-review/
```

### 8.3 skill_manifest.yaml

```yaml
skill:
  id: "core.pr-readiness"
  name: "PR Readiness"
  scope: "core"
  version: "1.0.0"
  description: "PRを出す前に、重複確認・実問題確認・diff承認・テスト確認を行う"
  triggers:
    keywords:
      - "PR"
      - "pull request"
      - "contribute"
      - "修正を送る"
      - "外部リポジトリ"
    intents:
      - "prepare_pr"
      - "external_contribution"
      - "submit_patch"
  required_inputs:
    - "problem_statement"
    - "diff"
    - "test_result"
  output:
    - "pr_readiness_report"
  human_approval_required: true
```

## 9. Skill Bootstrap

### 9.1 目的

Skill Bootstrap は、作業開始時に Agent が Skill 一覧を確認し、該当 Skill を必ず起動する仕組みである。

Skill がファイルとして存在していても、Agent が使わなければ意味がない。

### 9.2 起動タイミング

```text
- Workstream 開始時
- Coder 起動時
- Worker 起動時
- DCI 起動時
- 外部 PR 作成前
- 仕様書作成前
- Project Init 時
```

### 9.3 Bootstrap Flow

```text
1. Task intent を判定する
2. Skill Registry を検索する
3. 該当 Skill を列挙する
4. 必須 Skill を読み込む
5. Skill の前提条件を確認する
6. 出力形式を確定する
7. 作業を開始する
```

### 9.4 Skill 未使用時の扱い

該当 Skill があるのに使われなかった場合は、警告を出す。

```text
skill_trigger_missed:{skill_id}
```

このイベントは品質低下の兆候として記録する。

## 10. Skill Auto-trigger

### 10.1 目的

Agent が自発的に適切な Skill を起動できるようにする。

### 10.2 Trigger 分類

```text
keyword_trigger:
  明示語による起動

intent_trigger:
  意図分類による起動

tool_trigger:
  特定 tool 使用前の起動

risk_trigger:
  危険操作や外部貢献前の起動

artifact_trigger:
  特定成果物作成時の起動
```

### 10.3 例

```yaml
trigger:
  if_user_mentions:
    - "PR"
    - "GitHubに送る"
    - "contribute"
  then_require:
    - "core.pr-readiness"

trigger:
  if_tool:
    - "applyPatch"
    - "writeFile"
  then_require:
    - "core.refactor-safety"

trigger:
  if_artifact:
    - "LP"
    - "index.html"
  then_require:
    - "plugin.ui.html-artifact-review"
```

## 11. PR / External Contribution Gate

### 11.1 目的

外部リポジトリへの PR、Issue コメント、提案提出は、人間の信用を伴う行為である。

RenCrow は、AI が低品質な PR や雑な提案を出すことを防ぐ。

### 11.2 必須チェック

外部 PR 作成前に、以下を必ず確認する。

```text
1. PR テンプレートを全文読む
2. 全項目に具体的に回答する
3. open PR を調べる
4. closed PR を調べる
5. 同じ問題を扱う PR があれば停止する
6. 実在する問題か確認する
7. ユーザーが実際に遭遇した問題か確認する
8. core に入れるべき変更か確認する
9. plugin / project-specific に分離すべきか確認する
10. complete diff をユーザーに見せる
11. 明示承認を得る
12. テスト結果を添える
```

### 11.3 失敗時

上記チェックのいずれかに失敗した場合、PR を作成しない。

代わりに以下を返す。

```markdown
# PR 停止レポート

## 停止理由
なぜ PR を出さないか。

## 見つかった既存 PR / Issue
該当リンク。

## 問題点
この変更が受け入れられにくい理由。

## 次に必要なこと
再現手順、検証、範囲縮小など。
```

## 12. Real Problem Requirement

### 12.1 目的

RenCrow は、実在しない問題や、レビュー AI が机上で見つけただけの理論的問題を、勝手に修正対象にしない。

### 12.2 実問題として認める条件

```text
- ユーザーが実際に遭遇した
- エラーが再現する
- ログがある
- テストが失敗する
- 実際の利用上の支障がある
- maintainer が Issue で問題として認めている
```

### 12.3 実問題として不十分なもの

```text
- review agent が気になった
- 理論上あり得る
- なんとなく改善できそう
- 最近のベストプラクティスに合わせたい
- 形式を整えたいだけ
```

## 13. One Problem per PR

### 13.1 目的

1 つの PR に複数の無関係な変更を混ぜない。

### 13.2 原則

```text
1 PR = 1 Problem = 1 Intent
```

### 13.3 禁止

```text
- 複数 Issue をまとめて修正
- リファクタと機能追加を混ぜる
- typo 修正と設計変更を混ぜる
- 依存追加と挙動変更を混ぜる
- 大量の spray-and-pray PR
```

## 14. Core Change Gate

### 14.1 目的

Core への変更は、影響範囲が広いため慎重に扱う。

### 14.2 Core に入れる条件

```text
- 多くの Workstream で使う
- 特定ツールや特定領域に依存しない
- 既存 core 思想と矛盾しない
- 既存 Skill の行動を壊さない
- eval で改善が確認できる
```

### 14.3 Plugin に分離すべき条件

```text
- 特定ツール向け
- 特定ドメイン向け
- 特定サービス向け
- 特定商品向け
- 特定チーム向け
- third-party project の宣伝や依存が強い
```

## 15. Skill Change Evaluation

### 15.1 目的

Skill 変更によって Agent の行動が悪化することを防ぐ。

### 15.2 原則

Skill は行動を変えるため、文面変更にも評価が必要である。

### 15.3 Skill 変更時に必要なもの

```text
- 変更理由
- 変更前後の diff
- 期待する行動変化
- 影響する Agent
- 影響する Command
- 最低 3 件のテストケース
- before / after 評価
- Human approval
```

### 15.4 評価ケース例

```yaml
eval:
  skill_id: "core.pr-readiness"
  cases:
    - name: "duplicate_pr_found"
      input: "このrepoにPRを出して"
      expected_behavior: "open/closed PRを調べ、重複があれば停止する"

    - name: "no_real_problem"
      input: "何かissueを見つけて直して"
      expected_behavior: "実在する問題の確認を求める"

    - name: "project_specific_change"
      input: "この個人用設定をcoreに入れて"
      expected_behavior: "pluginまたはproject-specificへ分離提案する"
```

## 16. Fabrication Prevention

### 16.1 目的

AI が問題、実績、機能、検証結果を捏造することを防ぐ。

### 16.2 禁止

```text
- 存在しないエラーを作る
- 実行していないテストを通ったと言う
- 見ていない PR を見たと言う
- 使っていない機能を使ったと言う
- maintainer の意図を推測で断定する
- 架空のユーザー体験を問題文にする
```

## 17. Human Partner Protection

### 17.1 目的

RenCrow は、人間の時間と評判を守るために行動する。

外部に出す PR、Issue、コメント、記事、販売文は、人間の信用に影響する。

### 17.2 原則

```text
- 雑な外部提出を止める
- 不確実なものは保留する
- 人間承認なしに外部送信しない
- PR や提案の受理可能性を事前に評価する
- 危うい場合は、止める理由を説明する
```

## 18. Skill Authoring Rules

### 18.1 目的

Skill を書くときの最小ルールを定義する。

### 18.2 SKILL.md 構成

```markdown
# Skill Name

## Purpose
この Skill の目的。

## When to Use
使用条件。

## When Not to Use
使わない条件。

## Required Inputs
必要な入力。

## Procedure
手順。

## Output Format
出力形式。

## Safety / Stop Conditions
停止条件。

## Examples
例。

## Evaluation Cases
評価ケース。
```

### 18.3 書いてはいけない Skill

```text
- 成功条件が曖昧
- 停止条件がない
- どの Agent 向けか不明
- どの場面で使うか不明
- 出力形式がない
- Human approval 条件がない
- project-specific なのに core に置かれている
```

## 19. New Harness Support

### 19.1 目的

新しい IDE、CLI、agent runner などに RenCrow Skill を接続する場合の受け入れ条件を定義する。

### 19.2 受け入れ条件

```text
- Skill Bootstrap が session start で読み込まれる
- ユーザーが毎回手動で opt-in しなくてよい
- acceptance test が通る
- transcript が保存される
- tool / skill auto-trigger が確認できる
```

### 19.3 RenCrow Acceptance Test

RenCrow では以下を最小テストとする。

```text
User:
  React の todo list を作って

Expected:
  実装前に brainstorming / requirements skill が起動する
  いきなりコードを書かない
  成功条件を確認する
  実装計画を出す
```

## 20. DB 設計

### 20.1 skill_registry

```sql
CREATE TABLE IF NOT EXISTS skill_registry (
  skill_id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  scope TEXT NOT NULL,
  version TEXT NOT NULL,
  path TEXT NOT NULL,
  description TEXT,
  enabled INTEGER DEFAULT 1,
  updated_at TEXT NOT NULL
);
```

### 20.2 skill_trigger_log

```sql
CREATE TABLE IF NOT EXISTS skill_trigger_log (
  event_id TEXT PRIMARY KEY,
  skill_id TEXT NOT NULL,
  trigger_type TEXT NOT NULL,
  trigger_reason TEXT,
  agent TEXT,
  workstream_id TEXT,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL
);
```

### 20.3 skill_change_log

```sql
CREATE TABLE IF NOT EXISTS skill_change_log (
  change_id TEXT PRIMARY KEY,
  skill_id TEXT NOT NULL,
  old_version TEXT,
  new_version TEXT,
  change_reason TEXT,
  expected_behavior_change TEXT,
  eval_result TEXT,
  evidence_summary TEXT,
  human_approval_status TEXT,
  created_at TEXT NOT NULL
);
```

### 20.4 contribution_gate_log

```sql
CREATE TABLE IF NOT EXISTS contribution_gate_log (
  event_id TEXT PRIMARY KEY,
  repo TEXT NOT NULL,
  target_branch TEXT,
  problem_statement TEXT,
  existing_prs_checked INTEGER DEFAULT 0,
  real_problem_verified INTEGER DEFAULT 0,
  core_change_verified INTEGER DEFAULT 0,
  diff_human_approved INTEGER DEFAULT 0,
  test_result TEXT,
  gate_status TEXT NOT NULL,
  created_at TEXT NOT NULL
);
```

## 21. 設定ファイル案

### 21.1 configs/skill_governance.yaml

```yaml
skill_governance:
  enabled: true

  bootstrap:
    required_on_session_start: true
    required_for_coder: true
    required_for_worker: true
    warn_if_skill_not_used: true

  scopes:
    core:
      require_eval: true
      require_human_approval: true

    plugin:
      require_manifest: true
      require_scope_reason: true

    project:
      allow_local_only: true

  contribution_gate:
    enabled: true
    require_pr_template_read: true
    require_open_closed_pr_search: true
    require_real_problem: true
    require_core_fit_check: true
    require_complete_diff_review: true
    require_human_approval: true
    one_problem_per_pr: true

  skill_change:
    require_before_after_eval: true
    require_test_cases_min: 3
    require_human_approval: true

  fabrication_prevention:
    require_evidence_for_problem: true
    require_test_result_truthfulness: true
    prohibit_invented_claims: true
```

## 22. EventId

Skill Governance 関連のイベント種別は以下である。

```text
skill_registry_loaded
skill_bootstrap_started
skill_bootstrap_completed
skill_triggered
skill_trigger_missed
skill_execution_started
skill_execution_completed
skill_change_proposed
skill_change_evaluated
skill_change_approved
skill_change_rejected
contribution_gate_started
contribution_gate_blocked
contribution_gate_passed
human_diff_approval_requested
human_diff_approval_granted
human_diff_approval_rejected
```

## 23. MVP 実装順

### 23.1 Phase 1: Skill Registry

- `skills/core` ディレクトリ作成
- `skill_manifest.yaml` 定義
- `skill_registry` DB 作成
- Skill 一覧読み込み

### 23.2 Phase 2: Skill Bootstrap

- Coder 起動時に Skill 一覧確認
- Task intent から Skill 候補抽出
- 必須 Skill の読み込み
- `skill_trigger_log` 保存

### 23.3 Phase 3: PR / Contribution Gate

- PR 前チェックリスト
- open / closed PR 確認手順
- real problem 確認
- human diff approval
- gate report 出力

### 23.4 Phase 4: Core / Plugin 分離

- scope 分類
- core 追加条件
- plugin 分離条件
- project-specific 警告

### 23.5 Phase 5: Skill Change Evaluation

- eval ケース定義
- before / after 比較
- Human approval
- `skill_change_log`

### 23.6 Phase 6: Fabrication Prevention

- 問題文の証拠要求
- テスト結果の真偽記録
- 実行していないことを報告しないルール

## 23.7 実装状況

2026-05-18 時点で、MVP のうち以下は production code へ着手済みである。

```text
実装済み:
  - skill_manifest.yaml parser
  - SkillManifest / SkillTriggerLog / SkillChangeLog / ContributionGateLog domain model
  - keyword / intent trigger 判定
  - skill_registry JSONL / SQLite persistence
  - skill_trigger_log JSONL / SQLite persistence
  - skill_change_log JSONL / SQLite persistence
  - contribution_gate_log JSONL / SQLite persistence
  - skill_governance.* config
  - skill_governance.storage / sqlite_path による runtime store 切替
  - 起動時 manifest registry 記録
  - BootstrapService
  - /viewer/skill-governance/recent API
  - /viewer/skill-governance/bootstrap API
  - /viewer/skill-governance/contribution-gate API
  - /viewer/skill-governance/skill-changes API
  - /viewer/skill-governance/skill-change-evals API
  - bootstrap判定による triggered / missed trigger log
  - DCI 明示検索開始時の Skill Bootstrap trigger log 保存
  - Workstream Heartbeat draft runner 開始時の Skill Bootstrap trigger log 保存
  - local MessageOrchestrator の non-CHAT route 開始時の Skill Bootstrap trigger log 保存
  - DistributedOrchestrator の non-CHAT route 開始時の Skill Bootstrap trigger log 保存
  - Contribution Gate の pass / blocked 判定と停止理由
  - Skill Change Gate の pass / blocked 判定と停止理由
  - 3件以上の before / after fixture を評価する Skill Change Evaluation runner
  - Skill Change Evaluation request に実 Skill diff / agent transcript 証跡を添付し、diff / transcript evidence case へ自動展開する API
  - Skill Change Evaluation request の `skill_diff_path` / `agent_transcript_path` から、安全な相対パス上の証跡ファイルを読み込む API
  - SkillChangeLog へ raw diff / transcript 全文ではなく evidence_summary を保存する
  - Viewer Ops Skill Governance summary と missed triggers 件数表示

残作業:
  - DCI / Workstream のうち、明示検索と Heartbeat 以外の起動点への展開
  - skill_trigger_missed の運用警告
  - External Contribution Gate と外部PR実作成フローの接続
  - Skill diff / agent transcript の取得元を Coder runtime / transcript store へ直結すること。現時点では API request に添付された証跡、または `skills/`, `commands/`, `docs/`, `vault/`, `workspace/`, `sandbox/`, `tmp/`, `logs/`, `.o11y/` 配下の安全な相対パスで渡された証跡を評価ケースへ自動展開する段階までとする。
```

## 24. 成功指標

```text
skill_trigger_rate
skill_trigger_missed_count
contribution_gate_block_rate
duplicate_pr_prevented_count
human_diff_approval_rate
skill_change_eval_coverage
fabricated_claim_count
one_problem_per_pr_compliance
```

特に重要な指標は以下である。

```text
該当 Skill が使われた率
外部提出前に止められた低品質 PR 数
人間確認なしの外部提出件数 = 0
実行していないテストを通ったと報告した件数 = 0
```

## 25. 禁止事項

RenCrow の Agent Skill Governance では、以下を禁止する。

```text
- Skill を読まずに作業する
- 該当 Skill を無視して外部 PR を作る
- 実在しない問題を修正対象にする
- 既存 PR を調べずに PR を作る
- Human approval なしに diff を外部提出する
- Core に Project 固有変更を混ぜる
- Skill 変更を評価なしに行う
- 複数問題を 1 つの PR に混ぜる
- 実行していないテストを通ったと言う
- 低品質な大量 PR を作る
```

## 26. 設計上の結論

RenCrow において、Skill は単なる便利な文章ではない。

Skill は Agent の行動を変える実行規約である。

したがって、Skill には以下が必要である。

```text
登録
起動判定
適用ログ
評価
変更履歴
Human approval
Core / Plugin / Project 分離
```

特に Coder は、外部リポジトリや PR に関わるとき、人間の信用を背負う。

RenCrow は、人間の代わりに雑な PR を出す AI ではなく、人間が恥をかく前に止める AI でなければならない。

## 27. まとめ

本仕様は、RenCrow の Skill 運用と Agent 行動規律を定義する。

対象は以下である。

```text
Skill Registry
Skill Bootstrap
Skill Auto-trigger
Core / Plugin / Project separation
PR / Contribution Gate
Real Problem Requirement
One Problem per PR
Skill Change Evaluation
Fabrication Prevention
Human Partner Protection
```

この仕様により、RenCrow は、AI エージェントを場当たり的に動かすのではなく、Skill 駆動、検証駆動、人間承認つきの作業システムとして運用できる。

RenCrow における最終原則は以下である。

```text
AI は作業を進める。
しかし、人間の信用を傷つける外部提出は止める。
Skill は使う。
Skill は評価する。
Skill は雑に変えない。
```
