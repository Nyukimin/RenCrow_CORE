# RenCrow Reasoning Prompt MVP 実装仕様

## 目的

RenCrow に、ユーザー入力や作業種別に応じて思考プロンプトを自動選択・適用する仕組みを追加する。

今回の実装範囲は、最小構成として以下の 4 要素に限定する。

- Task Classifier
- Policy Engine
- Skill Loader
- Prompt Builder

Output Gate、自動再生成、Verifier 差し戻しは今回の範囲外とし、後続フェーズで実装する。

## 基本方針

5 つの思考スキルを、すべて常時プロンプトに入れない。

常駐させるのは、タスク分類とスキル選択の仕組みだけにする。

実際の思考スキル本文は、必要な時だけ Prompt Builder が差し込む。

これにより、通常会話を重くせず、コードレビュー・仕様検証・要約・意思決定・文体統一などの場面だけ、適切な思考モードを起動する。

## 対象となる 5 つの思考スキル

| Skill | 用途 |
|---|---|
| Semi-Formal Reasoning | コードレビュー、仕様レビュー、パッチ検証、破壊的変更確認 |
| Verbalized Confidence | 事実確認、最新情報、数値、API 仕様、不確実性の分離 |
| Town Hall Debate Prompting | 設計判断、方針決定、複数案比較、プロダクト判断 |
| Reference Class Priming | 文体統一、記事量産、仕様書生成、Qwen 系の出力揺れ対策 |
| Deliberate Over-Instruction | 要約、圧縮、議事録、仕様整理、重要情報の欠落防止 |

## 今回のスコープ

### 実装する

- ユーザー入力からタスク種別を分類する
- タスク分類結果から必要な思考スキルを選ぶ
- スキル本文を Markdown ファイルから読み込む
- 選択されたスキルだけをプロンプトに差し込む
- スキル選択理由を metadata として返す
- 基本的なユニットテストを追加する

### 実装しない

- Output Gate
- confidence gate
- compression loss check
- Verifier node
- 自動再生成
- LangGraph node への本格統合
- 外部検索の自動起動
- 実ファイル変更や破壊的操作

## 想定ディレクトリ構成

この構成は論理構成である。RenCrow 本体へ実装する場合は、既存の Go 実装・`internal/` 配下の責務境界に合わせて配置する。

```text
rencrow/
  configs/
    reasoning_policy.yaml

  prompts/
    reasoning_skills/
      semi_formal_reasoning.md
      verbalized_confidence.md
      town_hall_debate.md
      reference_class_priming.md
      deliberate_over_instruction.md

  src/
    reasoning/
      __init__.py
      types.py
      task_classifier.py
      policy_engine.py
      skill_loader.py
      prompt_builder.py

  tests/
    reasoning/
      test_task_classifier.py
      test_policy_engine.py
      test_skill_loader.py
      test_prompt_builder.py
```

## コンポーネント設計

## 1. Task Classifier

### 役割

ユーザー入力と現在の作業コンテキストから、タスク種別・複雑度・リスク・必要な出力条件を判定する。

### 入力

```python
user_message: str
context: dict | None
```

### 出力

```python
TaskClassification
```

### 型定義

```python
@dataclass
class TaskClassification:
    task_type: str
    domain: str | None
    complexity: int
    risk_flags: list[str]
    needs_freshness: bool
    needs_citation: bool
    needs_style_consistency: bool
    is_summarization: bool
    is_decision: bool
    is_code_or_spec_review: bool
    requires_reference_example: bool
    recommended_skills: list[str]
```

### `task_type` 候補

```text
chat
writing
summarization
code_review
spec_review
patch_validation
architecture_decision
product_decision
strategy
research
factual_check
implementation_plan
unknown
```

### `risk_flags` 候補

```text
factual_uncertainty
hallucination_risk
destructive_change
compatibility
security
regression
external_dependency
legal_or_policy
cost
performance
privacy
data_loss
user_memory
architecture
style_drift
compression_loss
```

### 初期分類ルール

| 入力に含まれる語 | task_type | 追加フラグ |
|---|---|---|
| レビュー、壊れてない、差分、パッチ | code_review または spec_review | compatibility, regression |
| 仕様、実装してよい、設計として | spec_review | architecture |
| 要約、まとめて、圧縮、短く | summarization | compression_loss |
| どっち、方針、比較、採用、決めたい | architecture_decision | cost, architecture |
| 最新、調べて、本当、確認、API、価格、法律 | factual_check または research | factual_uncertainty, external_dependency |
| 同じ文体、この感じ、このトーン、量産 | writing | style_drift |
| 実装プラン、実装順、作業計画 | implementation_plan | architecture |

### `complexity` 判定

最小実装では 1 から 5 の整数で返す。

```text
1: 通常会話
2: 軽い変換・短文作成
3: 要約・調査・小さな設計
4: 仕様レビュー・実装計画・複数観点の判断
5: 破壊的変更、長期設計、重大な意思決定
```

## 2. Policy Engine

### 役割

Task Classifier の結果を受け取り、適用する reasoning skill を選択する。

### 入力

```python
classification: TaskClassification
policy_config: ReasoningPolicy
```

### 出力

```python
list[SkillSelection]
```

### 型定義

```python
@dataclass
class SkillSelection:
    skill_name: str
    reason: str
    priority: int
    source: str
```

### 選択ルール

#### Semi-Formal Reasoning

選択条件:

- `task_type` が `code_review` / `spec_review` / `patch_validation` / `implementation_plan`
- または `risk_flags` に `destructive_change` / `compatibility` / `security` / `regression` / `data_loss` / `architecture` が含まれる

#### Verbalized Confidence

選択条件:

- `task_type` が `factual_check` / `research` / `spec_review`
- または `risk_flags` に `factual_uncertainty` / `hallucination_risk` / `external_dependency` / `legal_or_policy` が含まれる
- または `user_message` に「最新」「本当」「確認」「API」「価格」「法律」「いつ」などが含まれる

#### Town Hall Debate

選択条件:

- `task_type` が `architecture_decision` / `product_decision` / `strategy` / `implementation_plan`
- `complexity` が 3 以上
- 方針比較や複数案検討が必要

#### Reference Class Priming

選択条件:

- `task_type` が `writing`
- `needs_style_consistency` が true
- `requires_reference_example` が true
- または `risk_flags` に `style_drift` が含まれる

#### Deliberate Over-Instruction

選択条件:

- `task_type` が `summarization`
- `is_summarization` が true
- または `risk_flags` に `compression_loss` が含まれる

### 選択数の制限

```text
通常会話: 最大1個
通常タスク: 最大3個
高リスクタスク: 最大3個
```

複数該当時の優先順位:

```text
1. semi_formal_reasoning
2. deliberate_over_instruction
3. verbalized_confidence
4. town_hall_debate
5. reference_class_priming
```

## 3. Skill Loader

### 役割

`prompts/reasoning_skills/` 配下の Markdown ファイルから、選択された思考スキル本文を読み込む。

### 入力

```python
skill_name: str
```

### 出力

```python
str
```

### 対応するファイル

```text
semi_formal_reasoning -> prompts/reasoning_skills/semi_formal_reasoning.md
verbalized_confidence -> prompts/reasoning_skills/verbalized_confidence.md
town_hall_debate -> prompts/reasoning_skills/town_hall_debate.md
reference_class_priming -> prompts/reasoning_skills/reference_class_priming.md
deliberate_over_instruction -> prompts/reasoning_skills/deliberate_over_instruction.md
```

### 仕様

- 存在しない `skill_name` が渡された場合は例外を返す
- 空ファイルの場合は例外を返す
- 読み込み結果をキャッシュしてよい
- ファイル内容はそのまま返す
- この時点でプロンプト結合はしない

## 4. Prompt Builder

### 役割

Persona、Recall Pack、Task Prompt、選択された Reasoning Skills を統合し、最終的な LLM 入力プロンプトを生成する。

### 入力

```python
base_prompt: str
persona_prompt: str | None
recall_pack: str | None
task_prompt: str
selected_skills: list[SkillSelection]
skill_texts: dict[str, str]
output_format: str | None
metadata: dict | None
```

### 出力

```python
PromptBuildResult
```

### 型定義

```python
@dataclass
class PromptBuildResult:
    prompt: str
    selected_skills: list[SkillSelection]
    event_id: str
    metadata: dict
```

### 結合順序

```text
1. Base Prompt
2. Persona Prompt
3. Recall Pack
4. Task Prompt
5. Selected Reasoning Skill Prompts
6. Output Format
```

### 注意

- 選択されていないスキルは差し込まない
- スキル本文をすべて常時入れない
- reasoning trace を出力せよ、とは書かない
- 最終回答に必要な形式だけを指示する
- `event_id` を metadata に含める

## `reasoning_policy.yaml`

初期設定として以下を追加する。

```yaml
skills:
  semi_formal_reasoning:
    enabled: true
    trigger:
      task_type:
        - code_review
        - spec_review
        - patch_validation
        - architecture_decision
        - implementation_plan
      risk:
        - destructive_change
        - compatibility
        - security
        - regression
        - data_loss
        - architecture
    mode: internal_first
    expose_full_trace: false

  verbalized_confidence:
    enabled: true
    trigger:
      task_type:
        - factual_check
        - research
        - spec_review
        - architecture_decision
      risk:
        - factual_uncertainty
        - hallucination_risk
        - external_dependency
        - legal_or_policy
      contains:
        - 最新
        - 本当
        - 確認
        - 調べて
        - 仕様
        - API
        - 価格
        - 数値
        - 法律
        - いつ
        - どれ
    threshold:
      verify_below: 70
      caution_below: 85
      block_below: 50

  town_hall_debate:
    enabled: true
    trigger:
      task_type:
        - architecture_decision
        - product_decision
        - strategy
        - implementation_plan
      complexity_min: 3
      risk:
        - cost
        - architecture
        - performance
        - privacy
        - compatibility
    personas:
      - proposer
      - critic
      - operator
      - cost_owner
      - verifier
    final_style: integrated_decision
    simple_vote: false

  reference_class_priming:
    enabled: true
    trigger:
      task_type:
        - writing
        - report
        - spec_generation
        - social_post
      needs_style_consistency: true
      requires_reference_example: true
      risk:
        - style_drift
    reference_sources:
      - docs/reference_examples/
      - styleguides/
      - templates/

  deliberate_over_instruction:
    enabled: true
    trigger:
      task_type:
        - summarization
        - report
        - spec_review
        - review_summary
      is_summarization: true
      risk:
        - compression_loss
    preserve_fields:
      - assumptions
      - scope
      - exceptions
      - objections
      - alternatives
      - risks
      - unknowns
      - tests
      - rollback_conditions

defaults:
  max_skills_per_turn: 3
  normal_chat_max_skills: 1
  expose_skill_names_to_user: false
  log_skill_selection: true
  fail_closed_on_policy_error: false

priority:
  - semi_formal_reasoning
  - deliberate_over_instruction
  - verbalized_confidence
  - town_hall_debate
  - reference_class_priming
```

## Reasoning Skill Markdown

以下の 5 ファイルを作成する。

### `semi_formal_reasoning.md`

```markdown
# Semi-Formal Reasoning

Use semi-formal reasoning before answering.

Internal steps:
1. List assumptions and confirmed facts.
2. Separate confirmed facts, inferred claims, and unknowns.
3. Trace concrete execution paths, dependencies, branches, side effects, and failure paths.
4. Derive only conclusions supported by the above.
5. Mark unsupported claims as unknown.

For code/spec review, check:
- compatibility
- destructive changes
- missing tests
- exception paths
- rollback conditions
- affected modules
- data migration risks
- public API changes

Do not expose the full internal reasoning trace.

Expose only:
- conclusion
- key evidence
- risks
- unknowns
- next checks
```

### `verbalized_confidence.md`

```markdown
# Verbalized Confidence

For each major claim, assign confidence from 0 to 100 internally.

Confidence is not proof.
Confidence is only a signal for deciding whether additional verification is needed.

Rules:
- confidence >= 85: may be stated normally if supported.
- 70 <= confidence < 85: state with caution.
- 50 <= confidence < 70: mark as needs verification.
- confidence < 50: do not use as a conclusion.

For claims below 70, identify:
- what is uncertain
- what source, test, log, file, command, or primary document should verify it
- whether the answer should be marked uncertain or escalated

Final output should not list confidence for every sentence unless the user asks.

Instead, expose uncertainty as:
- confirmed
- likely
- uncertain
- needs verification
```

### `town_hall_debate.md`

```markdown
# Town Hall Debate Prompting

Evaluate the problem through multiple internal roles.

Roles:
1. Proposer:
   Argues for the plan and identifies the value.
2. Critic:
   Searches for failure modes, hidden assumptions, and weak points.
3. Operator:
   Checks implementation burden, maintenance burden, workflow impact, and operational risks.
4. Cost Owner:
   Checks cost, time, opportunity loss, and resource constraints.
5. Verifier:
   Separates confirmed facts, assumptions, and unknowns.

Procedure:
1. Each role raises up to 3 key points.
2. Each role may challenge or refine one other role's point.
3. Verifier classifies the final claims into confirmed / inferred / unknown.
4. Do not end with a simple vote.
5. Integrate the arguments into a conditional decision.

Final output:
- conclusion
- reasons to adopt
- biggest risks
- alternatives
- unverified assumptions
- required checks
- decision: adopt / conditional adopt / hold / reject
```

### `reference_class_priming.md`

```markdown
# Reference Class Priming

Use the provided reference example as the quality anchor.

Match:
- tone
- structure
- granularity
- paragraph length
- reader distance
- level of explanation
- heading style
- amount of detail
- use of examples
- ending style

Do not copy the content.
Do not reuse facts from the reference unless they are relevant to the new task.
Apply the same quality standard to the new input.

Before final output, check:
- structure matches the reference
- granularity has not become shallow
- tone has not drifted
- task-specific facts are reflected
- no irrelevant phrasing was copied from the reference

If no reference example is available, fall back to the matching styleguide and template.
```

### `deliberate_over_instruction.md`

```markdown
# Deliberate Over-Instruction

When summarizing or compressing, do not remove these fields:

- assumptions
- scope
- prerequisites
- exceptions
- boundary cases
- objections
- alternatives
- risks
- unknowns
- tests or verification gaps
- rollback conditions if applicable

Short output is allowed.
Loss of these fields is not allowed.

If the original text lacks one of these fields, write "not stated" rather than inventing it.

For technical summaries, always preserve:
- compatibility constraints
- destructive change risks
- public API changes
- data migration risks
- error paths
- untested paths

Final output should remain concise, but these fields must remain visible.
```

## 実装手順

## Step 1. 型定義を追加

`src/reasoning/types.py` を作成する。

実装する型:

- `TaskClassification`
- `SkillSelection`
- `PromptBuildResult`

必要なら以下も追加する。

- `ReasoningPolicy`
- `SkillLoadResult`

## Step 2. Skill Loader を実装

`src/reasoning/skill_loader.py` を作成する。

機能:

- `skill_name` から Markdown ファイルパスを解決
- ファイルを読み込む
- 空ファイルや存在しないファイルを検出
- 読み込んだ本文を返す

## Step 3. Task Classifier を実装

`src/reasoning/task_classifier.py` を作成する。

初期版はルールベースでよい。

機能:

- キーワードから `task_type` を分類
- `risk_flags` を付与
- `complexity` を判定
- `needs_freshness` / `needs_style_consistency` / `is_summarization` などを設定
- `recommended_skills` を仮設定してもよい

## Step 4. Policy Engine を実装

`src/reasoning/policy_engine.py` を作成する。

機能:

- `configs/reasoning_policy.yaml` を読み込む
- `TaskClassification` と照合する
- 条件に合う skill を選択
- 優先順位で並べる
- 最大 3 件までに制限する
- 選択理由を `SkillSelection.reason` に入れる

## Step 5. Prompt Builder を実装

`src/reasoning/prompt_builder.py` を作成する。

機能:

- `base_prompt` / `persona_prompt` / `recall_pack` / `task_prompt` を受け取る
- `selected_skills` に対応する `skill_texts` だけを差し込む
- `event_id` を生成または受け取る
- `PromptBuildResult` を返す

## Step 6. `__init__.py` を整える

`src/reasoning/__init__.py` から主要関数・型を import できるようにする。

例:

```python
from .types import TaskClassification, SkillSelection, PromptBuildResult
from .task_classifier import classify_task
from .policy_engine import select_reasoning_skills
from .skill_loader import load_skill
from .prompt_builder import build_prompt
```

## Step 7. テストを追加

最低限、以下をテストする。

### `test_task_classifier.py`

- 「要約して」で `summarization` になる
- 「この仕様で実装してよい？」で `spec_review` になる
- 「最新情報を確認」で `factual_check` になる
- 「同じ文体で10本」で `writing` + `style_drift` になる
- 「どっちがいい？」で decision 系になる

### `test_policy_engine.py`

- `summarization` で `deliberate_over_instruction` が選ばれる
- `spec_review` で `semi_formal_reasoning` が選ばれる
- `factual_check` で `verbalized_confidence` が選ばれる
- `architecture_decision` で `town_hall_debate` が選ばれる
- `writing` + `style_drift` で `reference_class_priming` が選ばれる
- 複数該当時に最大 3 件に制限される

### `test_skill_loader.py`

- 5 つの skill が読み込める
- 存在しない skill で例外になる
- 空ファイルで例外になる

### `test_prompt_builder.py`

- 選択された skill だけが prompt に含まれる
- 未選択 skill が prompt に含まれない
- `event_id` が metadata に入る
- persona / recall / task / skill の順で結合される

## MVP の完了条件

以下を満たしたら MVP 完了。

- `configs/reasoning_policy.yaml` が存在する
- `prompts/reasoning_skills/*.md` が 5 つ存在する
- `TaskClassification` が定義されている
- `SkillSelection` が定義されている
- `PromptBuildResult` が定義されている
- Task Classifier が最低限の分類を返せる
- Policy Engine が必要な skill を選べる
- Skill Loader が Markdown を読み込める
- Prompt Builder が選択 skill だけを差し込める
- テストが通る
- 通常会話で 5 つ全部が常時差し込まれない

## 非目標

今回の MVP では以下をやらない。

- Output Gate
- 自動再生成
- Verifier node
- 外部検索実行
- LangGraph 本格統合
- Worker による実ファイル変更
- Coder による直接実行
- UI 表示
- ログ DB 保存

ただし、`PromptBuildResult.metadata` には、後続でログ保存できる情報を含める。

## 将来拡張

次フェーズで以下を追加する。

1. Output Gate
   - `confidence_gate`
   - `compression_loss_check`
   - `decision_gate`
   - `review_gate`
2. Verifier node
   - 未確認事項の分離
   - 圧縮欠落チェック
   - 低確信主張の隔離
3. LangGraph 統合
   - Task Classifier Node
   - Policy Engine Node
   - Prompt Builder Node
   - Verifier Node
4. Event Log
   - `event_id`
   - `selected_skills`
   - `selection_reason`
   - `task_type`
   - `risk_flags`
   - `gate_result`
5. UI / Debug
   - 今回選ばれた思考スキル
   - 選択理由
   - 適用された Prompt 断片
   - 未確認事項

## 注意事項

- 5 つのスキルを System Prompt に全部入れない
- reasoning trace をユーザーにそのまま出さない
- confidence は真実判定ではなく、検証優先度として扱う
- Town Hall Debate は多数決で終わらせない
- Reference Class の内容をコピーしない
- 要約時に前提・例外・反対意見・未確認事項を落とさない
- Coder は破壊的変更を直接実行しない
- Worker が実行主体である
- 重要処理には EventId を付与する

## まとめ

この MVP では、RenCrow に思考スキルを直接埋め込むのではなく、必要な場面で必要なスキルだけを選ぶ基盤を作る。

最小構成は以下の 4 つ。

```text
Task Classifier
  ↓
Policy Engine
  ↓
Skill Loader
  ↓
Prompt Builder
```

これにより、RenCrow は自律的に以下を判断できるようになる。

```text
今は検証モードが必要
今は確信度チェックが必要
今は討論モードが必要
今は文体アンカーが必要
今は圧縮禁止が必要
```

最初の実装では、出力後の検査や再生成までは行わず、まず「必要な思考スキルを選んでプロンプトに差し込む」ところまでを安定させる。
