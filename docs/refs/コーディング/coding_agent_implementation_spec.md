# coding_agent_implementation_spec.md

# RenCrow コーディングエージェント実装仕様書

## 1. 概要

このドキュメントは、`coding_agent_modes.md` で定義した RenCrow のコーディングエージェント運用モードを、実際にコードとして実装するための仕様書である。

`coding_agent_modes.md` では、RenCrow のコーディングAIを次の2形態に分けている。

| モード | 目的 | 主な対象 | 優先する価値 |
|---|---|---|---|
| Safe Build Mode | 既存システムを壊さずに変更を積み上げる | RenCrow本体、既存DB、既存環境、設定、運用系 | 安全性、再現性、影響範囲の把握 |
| Tool Build Mode | れんが作ってほしい道具を素早く形にする | 新規ツール、小物スクリプト、補助アプリ、実験用CLI | 速度、明確な入出力、使いやすさ |

また、Worker / Coder はモードそのものではなく、各モード内で動く役割である。

- Worker: 考える、調べる、設計する、検証する、判断する。
- Coder: 読む、書く、直す、テストする、差分を出す。

本実装仕様書では、この運用ルールをコードへ落とすために、v0.1 の最小実装範囲、実装順、ディレクトリ構成、モジュール責務、データ構造、設定ファイル、テンプレート、CLI、テスト、完了条件を定義する。

### 1.1 v0.1 で作るもの

v0.1 では、自律的にコードを書き換えるAIは作らない。

まず作るのは、次の機能を持つ **コーディングAIの運用判断エンジン** である。

- 依頼文から Safe Build Mode / Tool Build Mode を判定する。
- 判定理由を返す。
- 禁止操作、許可操作、要確認操作を判定する。
- Worker / Coder 用プロンプトを生成する。
- 作業ログJSONの雛形を生成する。
- memory candidate を生成する。
- CLIから手動確認できるようにする。
- 代表ケースのテストを通す。

### 1.2 v0.1 で作らないもの

v0.1 では、次は作らない。

- 実際のコード自動編集
- Git操作の自動実行
- DBへの正式保存
- confirmed memory / pinned memory への昇格
- LangGraph接続
- Web UI
- 自律エージェント実行
- 外部検索
- Source Registry更新

### 1.3 実装のゴール

v0.1 のゴールは、れんの依頼文を入力すると、次の「実装判断パック」を生成できる状態にすることである。

```text
入力:
  JSONLの形式チェックツールを作りたい

出力:
  mode: tool_build
  reason: 新規小物ツールで、既存本体に直接接続しないため
  guardrails: allowed
  worker_prompt: 要望整理、最小仕様、入出力定義
  coder_prompt: /home/nyukimi/RenCrow/RenCrow_Tools/tools/jsonl_validator/ で新規実装、README、サンプル、テスト
  worklog: event_id付きの作業ログ雛形
  memory_candidates: 必要に応じて tool_template / recent_tool_patterns 候補
```

---

## 2. v0.1 のスコープ

### 2.1 実装する必須機能

| 機能 | モジュール | 役割 |
|---|---|---|
| Mode Selector | `mode_selector.py` | 依頼文から Safe Build Mode / Tool Build Mode を判定する。 |
| Guardrails Checker | `guardrails.py` | 禁止操作、許可範囲、要確認操作を判定する。 |
| Prompt Builder | `prompt_builder.py` | Worker / Coder 用プロンプトを生成する。 |
| Worklog Generator | `worklog.py` | 作業ログJSONの雛形を生成する。 |
| Memory Candidate Generator | `memory_candidate.py` | repo_memory、failure_pattern、accepted_pattern、environment_constraint などの記憶候補を生成する。 |
| CLI | `cli.py` | 手動で依頼文を入力し、判定結果・プロンプト・ログ雛形を確認できる。 |
| Tests | `tests/coding_agent/` | 代表ケースのユニットテストを用意する。 |

### 2.2 実装しないもの

| 対象 | v0.1 で見送る理由 |
|---|---|
| 実際のコード自動編集 | ガードレールとログ設計が安定してから接続する。 |
| Git操作の自動実行 | 誤commit、誤branch、誤pushを防ぐため後回しにする。 |
| DBへの正式保存 | official DB を壊さないため、v0.1ではファイル出力までにする。 |
| confirmed / pinned memory 昇格 | validator または れんの確認が必要なため、自動化しない。 |
| LangGraph接続 | モジュール単体で動作確認してから接続する。 |
| Web UI | CLIで仕様を固めてから作る。 |
| 自律エージェント実行 | v0.1 は判断エンジンであり、自律実行基盤ではない。 |
| 外部検索 | コーディング判断エンジンの初期範囲外とする。 |
| Source Registry更新 | 無審査追加を防ぐため、候補生成までにする。 |

---

## 3. 全体アーキテクチャ

v0.1 の処理フローは次の通りである。

```text
User Task
  ↓
Task Analyzer
  ↓
Mode Selector
  ↓
Guardrails Checker
  ↓
Recall Context Placeholder
  ↓
Prompt Builder
  ├─ Worker Prompt
  └─ Coder Prompt
  ↓
Worklog Generator
  ↓
Memory Candidate Generator
  ↓
CLI Output / JSON Output
```

### 3.1 各ステップの役割

| ステップ | 役割 | v0.1での扱い |
|---|---|---|
| User Task | れんの依頼文、対象ファイル、想定コマンドなど | CLI引数から受け取る。 |
| Task Analyzer | 依頼文から対象、意図、危険語、出力先候補を抽出する | ルールベースの軽い解析にする。 |
| Mode Selector | Safe Build / Tool Build を判定する | 迷ったら Safe Build Mode。 |
| Guardrails Checker | 禁止・許可・要確認を判定する | 破壊的操作は blocked。 |
| Recall Context Placeholder | 将来の記憶想起差し込み位置 | v0.1では空または手動入力。 |
| Prompt Builder | Worker / Coder プロンプトを生成する | Markdownテンプレートを読み込む。 |
| Worklog Generator | 作業ログJSON雛形を生成する | event_id を生成する。 |
| Memory Candidate Generator | 記憶候補を生成する | status は必ず candidate。 |
| CLI Output / JSON Output | 標準出力またはJSONファイルに出す | `--json` と `--write-output` を用意する。 |

### 3.2 v0.1 の設計原則

- 既存DBや既存コードを自動変更しない。
- 実行ではなく、判断と指示生成を行う。
- 実体の Worker / Coder 接続前に、判定・ログ・記憶候補の形を固定する。
- 迷った場合は安全側へ倒す。
- Tool Build Mode は軽快にするが、既存本体へ踏み込んだ時点で Safe Build Mode に切り替える。

---

## 4. 推奨ディレクトリ構成

```text
rencrow/
  coding_agent/
    __init__.py
    models.py
    mode_selector.py
    guardrails.py
    prompt_builder.py
    worklog.py
    memory_candidate.py
    task_analyzer.py
    cli.py

config/
  coding_agent_modes.yaml
  guardrails.yaml
  memory_types.yaml

templates/
  coding_agent/
    safe_worker.md
    safe_coder.md
    tool_worker.md
    tool_coder.md
    worklog_summary.md

schemas/
  worklog.schema.json
  memory_candidate.schema.json
  mode_decision.schema.json

tests/
  coding_agent/
    test_mode_selector.py
    test_guardrails.py
    test_prompt_builder.py
    test_worklog.py
    test_memory_candidate.py
    test_cli.py

docs/
  coding_agent_modes.md
  coding_agent_implementation_spec.md
```

### 4.1 ファイル責務

| ファイル | 責務 |
|---|---|
| `rencrow/coding_agent/__init__.py` | パッケージ初期化。バージョンや公開APIを定義する。 |
| `models.py` | CodingMode、AgentRole、ModeDecision、GuardrailResult、PromptBundle、WorkLog、MemoryCandidate を定義する。 |
| `task_analyzer.py` | 依頼文から対象、意図、危険語、候補ファイル、候補コマンドを抽出する。 |
| `mode_selector.py` | Safe Build / Tool Build を判定し、理由と信頼度を返す。 |
| `guardrails.py` | 禁止操作、許可操作、要確認操作を判定する。 |
| `prompt_builder.py` | テンプレートを読み込み、Worker / Coder 用プロンプトを生成する。 |
| `worklog.py` | event_id生成、作業ログ雛形生成、JSON化を担当する。 |
| `memory_candidate.py` | 作業内容から memory candidate を生成する。 |
| `cli.py` | CLIエントリーポイント。入力から全処理を実行する。 |
| `config/coding_agent_modes.yaml` | モード判定ルール、参照記憶、許可出力先を定義する。 |
| `config/guardrails.yaml` | 禁止操作、要確認操作、許可操作、破壊的キーワードを定義する。 |
| `config/memory_types.yaml` | コーディング用記憶型を定義する。 |
| `templates/coding_agent/*.md` | Worker / Coder / worklog 用テンプレートを格納する。 |
| `schemas/*.schema.json` | JSON出力の検証スキーマを定義する。 |
| `tests/coding_agent/*.py` | 各モジュールのユニットテストを格納する。 |
| `docs/coding_agent_modes.md` | 運用モード仕様。既存仕様を置く。 |
| `docs/coding_agent_implementation_spec.md` | 本実装仕様書。 |

---

## 5. データモデル

v0.1 では、データモデルは Pydantic または dataclass で定義する。外部バリデーションやJSON Schema出力を見据えるなら Pydantic が扱いやすい。

### 5.1 CodingMode

```python
from enum import Enum

class CodingMode(str, Enum):
    SAFE_BUILD = "safe_build"
    TOOL_BUILD = "tool_build"
```

### 5.2 AgentRole

```python
class AgentRole(str, Enum):
    WORKER = "worker"
    CODER = "coder"
```

### 5.3 ModeDecision

```python
from pydantic import BaseModel, Field

class ModeDecision(BaseModel):
    mode: CodingMode
    confidence: float = Field(ge=0.0, le=1.0)
    reason: str
    matched_rules: list[str] = []
    requires_human_review: bool = False
```

| フィールド | 内容 |
|---|---|
| `mode` | `safe_build` または `tool_build`。 |
| `confidence` | 判定信頼度。0.0〜1.0。 |
| `reason` | 判定理由。人間が読める文。 |
| `matched_rules` | 適用されたルール名。 |
| `requires_human_review` | 人間確認が必要か。迷った場合は true。 |

### 5.4 GuardrailResult

```python
class GuardrailResult(BaseModel):
    allowed: bool
    blocked: bool
    needs_review: bool
    violations: list[str] = []
    warnings: list[str] = []
```

| フィールド | 内容 |
|---|---|
| `allowed` | そのまま進められるか。 |
| `blocked` | 禁止事項に該当するか。 |
| `needs_review` | 人間確認が必要か。 |
| `violations` | 禁止事項一覧。 |
| `warnings` | 注意事項一覧。 |

### 5.5 PromptBundle

```python
class PromptBundle(BaseModel):
    worker_prompt: str
    coder_prompt: str
    mode: CodingMode
    task_summary: str
    guardrail_notes: list[str] = []
```

### 5.6 WorkLog

```python
class WorkLog(BaseModel):
    event_id: str
    mode: CodingMode
    worker_id: str | None = None
    coder_id: str | None = None
    repo: str | None = None
    branch: str | None = None
    task_summary: str
    files_read: list[str] = []
    files_changed: list[str] = []
    commands_run: list[str] = []
    test_results: dict = {}
    errors: list[str] = []
    retry_count: int = 0
    failure_reason: str | None = None
    next_action: str
    memory_candidates: list[dict] = []
```

### 5.7 MemoryCandidate

```python
class MemoryCandidate(BaseModel):
    memory_id: str
    type: str
    summary: str
    evidence_event_ids: list[str] = []
    status: str = "candidate"
    proposed_by: str
    needs_review_by: str = "worker_or_ren"
```

`status` は v0.1 では必ず `candidate` にする。`confirmed` や `pinned` は生成しない。

---

## 6. 設定ファイル仕様

### 6.1 `config/coding_agent_modes.yaml`

役割:

- Safe Build Mode にする条件を定義する。
- Tool Build Mode にする条件を定義する。
- 判定に迷った場合の方針を定義する。
- モードごとの参照記憶、許可出力先、禁止操作を定義する。

例:

```yaml
version: 1

default_mode: safe_build
fallback:
  when_uncertain: safe_build
  requires_human_review: true

modes:
  safe_build:
    description: "既存システムを壊さずに変更を積み上げるモード"
    triggers:
      keywords:
        - 既存
        - 修正
        - 変更
        - DB
        - スキーマ
        - migration
        - Source Registry
        - validator
        - LLMサーバ
        - OpenAI互換API
        - config
        - venv
        - CUDA
        - モデル配置
      target_patterns:
        - "rencrow/**"
        - "config/**"
        - "schemas/**"
        - "memory/**"
        - "source_registry/**"
    memory_types:
      - coding_rule
      - repo_memory
      - environment_constraint
      - failure_pattern
      - accepted_pattern
      - command_history
      - previous_diff
      - known_risk
      - worklog
    allowed_outputs:
      - staging/
      - reports/
      - logs/
    forbidden_actions:
      - official_db_write
      - user_memory_direct_upsert
      - memory_confirm
      - source_registry_unreviewed_add

  tool_build:
    description: "れんが作ってほしい道具を素早く形にするモード"
    triggers:
      keywords:
        - ツール
        - 小物
        - 変換
        - viewer
        - validator
        - CLI
        - 作りたい
        - 生成
        - 整形
      target_patterns:
        - "/home/nyukimi/RenCrow/RenCrow_Tools/**"
        - "experiments/**"
        - "sandbox/**"
    memory_types:
      - preferred_stack
      - tool_template
      - recent_tool_patterns
      - coding_rule
      - accepted_pattern
    allowed_outputs:
      - /home/nyukimi/RenCrow/RenCrow_Tools/
      - experiments/
      - sandbox/
      - reports/
      - logs/
    forbidden_actions:
      - official_db_write
      - source_registry_update
      - existing_config_change
```

### 6.2 `config/guardrails.yaml`

役割:

- 禁止操作を定義する。
- 要確認操作を定義する。
- 許可操作を定義する。
- 破壊的操作キーワードを定義する。
- 触ると危険な対象を定義する。
- Safe Build Mode 強制切り替え条件を定義する。

例:

```yaml
version: 1

blocked:
  actions:
    - official_db_write
    - user_uid_direct_upsert
    - memory_confirm_or_pin
    - source_registry_unreviewed_add
    - bulk_search_ingest
    - shared_environment_change
    - venv_rebuild_without_review
    - cuda_change_without_review
    - model_file_move_without_review
    - large_delete
    - unnecessary_rename
  command_keywords:
    - "rm -rf"
    - "Remove-Item -Recurse"
    - "Move-Item"
    - "del /s"
    - "DROP TABLE"
    - "TRUNCATE"
    - "git reset --hard"
    - "git clean -fd"

needs_review:
  actions:
    - db_migration
    - config_change
    - existing_api_change
    - log_delete
    - dependency_update
    - env_var_change
    - model_placement_change
  file_patterns:
    - "config/**"
    - "schemas/**"
    - "memory/**"
    - "*.env"
    - "requirements.txt"
    - "pyproject.toml"

allowed:
  output_dirs:
    - "staging/"
    - "reports/"
    - "logs/"
    - "/home/nyukimi/RenCrow/RenCrow_Tools/"
    - "experiments/"
    - "sandbox/"
  actions:
    - memory_candidate_create
    - validator_test
    - report_write
    - log_write
    - new_tool_create

force_safe_build:
  keywords:
    - "既存DB"
    - "本番"
    - "Source Registry"
    - "confirmed memory"
    - "pinned"
    - "migration"
    - "OpenAI互換API"
```

### 6.3 `config/memory_types.yaml`

役割:

- コーディング用記憶型を定義する。
- 各記憶型の意味、保存候補条件、レビュー要否を定義する。

例:

```yaml
version: 1

memory_types:
  coding_rule:
    description: "常に守る開発ルール"
    default_status: candidate
    review_required: true

  repo_memory:
    description: "リポジトリ固有の構造や重要ファイル"
    default_status: candidate
    review_required: true

  failure_pattern:
    description: "過去に失敗した作業パターン"
    default_status: candidate
    review_required: true

  accepted_pattern:
    description: "成功した実装パターン"
    default_status: candidate
    review_required: true

  environment_constraint:
    description: "OS、venv、モデル配置、PowerShell運用などの制約"
    default_status: candidate
    review_required: true

  command_history:
    description: "実行したコマンドと結果"
    default_status: observed
    review_required: false

  previous_diff:
    description: "直近の差分"
    default_status: observed
    review_required: false

  known_risk:
    description: "触ると壊れやすい場所"
    default_status: candidate
    review_required: true

  worklog:
    description: "作業記録"
    default_status: observed
    review_required: false

  preferred_stack:
    description: "ツール作成時に好む技術スタック"
    default_status: candidate
    review_required: true

  tool_template:
    description: "ツール作成時に再利用できる雛形"
    default_status: candidate
    review_required: true

  recent_tool_patterns:
    description: "最近作ったツールの成功パターン"
    default_status: candidate
    review_required: true
```

---

## 7. Mode Selector 仕様

### 7.1 役割

`mode_selector.py` は、依頼文や作業対象から Safe Build Mode / Tool Build Mode を判定する。

### 7.2 入力

```python
def select_mode(
    task_text: str,
    repo_context: dict | None = None,
    target_files: list[str] | None = None,
    user_hint: str | None = None,
) -> ModeDecision:
    ...
```

| 引数 | 内容 |
|---|---|
| `task_text` | れんの依頼文。 |
| `repo_context` | 任意。対象repo、既存構成、危険領域など。 |
| `target_files` | 任意。対象ファイル一覧。 |
| `user_hint` | 任意。れんが明示したモード指定。 |

### 7.3 出力

`ModeDecision` を返す。

```json
{
  "mode": "tool_build",
  "confidence": 0.86,
  "reason": "新規小物ツールであり、/home/nyukimi/RenCrow/RenCrow_Tools 配下で単体作成できるため",
  "matched_rules": ["tool_build.keyword:ツール", "tool_build.target:tools"],
  "requires_human_review": false
}
```

### 7.4 判定ルール

#### Safe Build Mode にする条件

次のいずれかに当てはまる場合、Safe Build Mode を返す。

- 既存コードを変更する。
- 既存DBに触る。
- 既存運用に影響する。
- 既存環境を変更する。
- 本番系設定に触る。
- 記憶・Source Registry・validatorに関係する。
- 失敗時の影響範囲が大きい。
- `config/`、`schemas/`、`memory/`、`source_registry/` に触る。
- `requirements.txt`、`pyproject.toml`、`.env` に触る。
- LLMサーバ、OpenAI互換API、モデル配置、venv、CUDA、MLX、Ollamaに触る。

#### Tool Build Mode にする条件

次の条件を満たす場合、Tool Build Mode を返す。

- 新しい小物ツールを作る。
- 既存本体にまだ接続しない。
- 横断ツールなら `/home/nyukimi/RenCrow/RenCrow_Tools`、実験なら `experiments/`、`sandbox/` 配下で作れる。
- 入力と出力が明確。
- 単体で動作確認できる。
- 既存DB、既存設定、Source Registry、記憶に触らない。

#### 迷った場合

判定に迷った場合は、必ず Safe Build Mode を返す。

```json
{
  "mode": "safe_build",
  "confidence": 0.52,
  "reason": "新規ツールにも見えるが、既存RenCrow本体への接続が含まれる可能性があるため安全側に倒す",
  "matched_rules": ["fallback:uncertain"],
  "requires_human_review": true
}
```

### 7.5 代表入力と出力

| 入力 | 判定 | 理由 |
|---|---|---|
| JSONLの形式チェックツールを作りたい | `tool_build` | 新規小物ツールであり、単体で作れる。 |
| 既存LLMサーバのOpenAI互換APIを直したい | `safe_build` | 既存APIとLLMサーバに影響する。 |
| RenCrowの記憶DBスキーマを変更したい | `safe_build` | 既存DBと記憶システムに触る。 |
| MarkdownからHTMLを作る小物ツールが欲しい | `tool_build` | 入出力が明確な新規ツール。 |
| Source Registryに新しいソースを追加したい | `safe_build` | Source Registry に関係する。 |
| RenCrow_Tools 配下にCSV整形ツールを作りたい | `tool_build` | RenCrow_Tools 配下の新規作成で既存本体に触らない。 |

---

## 8. Guardrails Checker 仕様

### 8.1 役割

`guardrails.py` は、依頼内容、計画、想定コマンド、想定変更先を見て、禁止・許可・要確認を判定する。

### 8.2 入力

```python
def check_guardrails(
    mode: CodingMode,
    task_text: str,
    planned_files: list[str] | None = None,
    planned_commands: list[str] | None = None,
    planned_outputs: list[str] | None = None,
) -> GuardrailResult:
    ...
```

### 8.3 出力

```json
{
  "allowed": false,
  "blocked": true,
  "needs_review": true,
  "violations": ["blocked.command:rm -rf"],
  "warnings": ["破壊的操作が含まれるため、実行せずWorkerへ戻す"]
}
```

### 8.4 検出すべき禁止事項

- official DB への直接write
- `user:<uid>` への直接upsert
- memory の直接確定
- Source Registry への無審査追加
- 検索結果の自動大量投入
- 共有環境の無断変更
- venv / CUDA / モデル配置の無断変更
- 既存ファイルの大規模削除
- 不要なリネームや移動
- `Move-Item` や `rm -rf` のような破壊的操作
- テストなしの完了報告
- 3回失敗後の自動継続

### 8.5 要確認にすべきもの

- DB migration
- config変更
- 既存API変更
- 既存ログ削除
- 依存関係更新
- 環境変数変更
- モデル配置変更
- `requirements.txt` の変更
- `pyproject.toml` の変更
- `.env` の変更

### 8.6 許可されるもの

- `staging/` への出力
- `reports/` への出力
- `logs/` への出力
- `/home/nyukimi/RenCrow/RenCrow_Tools` 配下の新規作成
- `experiments/` 配下の新規作成
- `sandbox/` 配下の新規作成
- memory candidate の作成
- validatorテスト
- README、サンプル、テストの作成

### 8.7 判定例

| 入力 | 結果 |
|---|---|
| `rm -rf data/` | blocked |
| `Move-Item .venv old_venv` | blocked |
| `write official DB` | blocked |
| `reports/coding_agent/evt.json` へ出力 | allowed |
| `/home/nyukimi/RenCrow/RenCrow_Tools/tools/csv_formatter/` を新規作成 | allowed |
| `config/guardrails.yaml` を変更 | needs_review |

---

## 9. Prompt Builder 仕様

### 9.1 役割

`prompt_builder.py` は、`ModeDecision` と `GuardrailResult` をもとに、Worker / Coder 用プロンプトを生成する。

### 9.2 入力

```python
def build_prompts(
    task_text: str,
    mode_decision: ModeDecision,
    guardrail_result: GuardrailResult,
    recall_context: str | None = None,
    repo_context: str | None = None,
) -> PromptBundle:
    ...
```

### 9.3 出力

`PromptBundle` を返す。

```json
{
  "mode": "safe_build",
  "task_summary": "既存LLMサーバのOpenAI互換API修正",
  "guardrail_notes": ["既存API変更のため要レビュー"],
  "worker_prompt": "...",
  "coder_prompt": "..."
}
```

### 9.4 テンプレート

読み込むテンプレートは次の4種類である。

- `templates/coding_agent/safe_worker.md`
- `templates/coding_agent/safe_coder.md`
- `templates/coding_agent/tool_worker.md`
- `templates/coding_agent/tool_coder.md`

### 9.5 Safe Build Mode の Worker Prompt

含める内容:

- 目的
- 既存仕様を読む
- 影響範囲を出す
- 既存ルール・記憶・過去失敗例を確認する
- テスト観点を作る
- Coder に渡す小さい作業単位を作る
- 禁止操作を明示する
- 迷った場合は作業を止める

### 9.6 Safe Build Mode の Coder Prompt

含める内容:

- 関連ファイルを読む
- 指定範囲だけ変更する
- 差分を小さく保つ
- 既存挙動を壊さない
- テストを実行する
- ログを残す
- memory candidate を出す
- 3回失敗したらWorkerへ戻す

### 9.7 Tool Build Mode の Worker Prompt

含める内容:

- 要望整理
- 最小仕様
- 入力と出力
- 保存先ディレクトリ
- README構成
- 将来のRenCrow連携案
- 既存本体へ接続しない初期方針

### 9.8 Tool Build Mode の Coder Prompt

含める内容:

- 新規ディレクトリで作る
- READMEを書く
- サンプル入力と実行例を付ける
- テストを書く
- 既存RenCrow本体に直接接続しない
- 必要なら連携口を後付け可能にする

---

## 10. Worklog Generator 仕様

### 10.1 役割

`worklog.py` は、作業開始時または作業終了時に、作業ログJSONを生成する。

v0.1 では、作業開始用の雛形生成を中心にする。

### 10.2 入力

```python
def create_worklog_stub(
    task_text: str,
    mode_decision: ModeDecision,
    prompt_bundle: PromptBundle,
    repo: str | None = None,
    branch: str | None = None,
) -> WorkLog:
    ...
```

### 10.3 event_id 形式

```text
evt_YYYYMMDD_HHMMSS_xxxxxx
```

例:

```text
evt_20260515_174512_a1b2c3
```

### 10.4 必須項目

- `event_id`
- `mode`
- `task_summary`
- `retry_count`
- `memory_candidates`
- `next_action`

### 10.5 JSON例

```json
{
  "event_id": "evt_20260515_174512_a1b2c3",
  "mode": "tool_build",
  "worker_id": null,
  "coder_id": null,
  "repo": "RenCrow",
  "branch": null,
  "task_summary": "JSONLの形式チェックツールを作成する",
  "files_read": [],
  "files_changed": [],
  "commands_run": [],
  "test_results": {},
  "errors": [],
  "retry_count": 0,
  "failure_reason": null,
  "next_action": "Workerが最小仕様と入出力を整理する",
  "memory_candidates": []
}
```

---

## 11. Memory Candidate Generator 仕様

### 11.1 役割

`memory_candidate.py` は、作業内容から今後使えそうな記憶候補を生成する。

v0.1では、`confirmed` / `pinned` には昇格しない。`status` は必ず `candidate` にする。

### 11.2 入力

```python
def generate_memory_candidates(
    task_text: str,
    mode_decision: ModeDecision,
    guardrail_result: GuardrailResult,
    worklog: WorkLog,
    errors: list[str] | None = None,
    test_results: dict | None = None,
) -> list[MemoryCandidate]:
    ...
```

### 11.3 出力

`MemoryCandidate[]` を返す。

```json
[
  {
    "memory_id": "memcand_20260515_174512_a1b2c3",
    "type": "tool_template",
    "summary": "JSONL validator は /home/nyukimi/RenCrow/RenCrow_Tools 配下に独立ツールとして作ると本体に影響しにくい",
    "evidence_event_ids": ["evt_20260515_174512_a1b2c3"],
    "status": "candidate",
    "proposed_by": "coding_agent",
    "needs_review_by": "worker_or_ren"
  }
]
```

### 11.4 生成する候補例

| type | 生成条件の例 |
|---|---|
| `repo_memory` | 対象repoの構造や重要ファイルが明らかになった。 |
| `failure_pattern` | 同じ種類の失敗や禁止操作に近い計画が出た。 |
| `accepted_pattern` | 安全な手順や再利用できる進め方が見つかった。 |
| `environment_constraint` | OS、venv、PowerShell、モデル配置などの制約が出た。 |
| `tool_template` | 新規ツールの雛形として再利用できる構成が出た。 |
| `recent_tool_patterns` | 最近のツール作成に使えるパターンが出た。 |

### 11.5 注意

- 一度の観測だけで確定しない。
- れんまたはvalidatorの確認を待つ。
- sensitive な情報は候補化しない、または要レビューにする。
- `confirmed` や `pinned` は出力しない。

---

## 12. CLI 仕様

### 12.1 目的

`cli.py` は、手動で依頼文を入れて、モード判定、ガードレール、Worker / Coder プロンプト、作業ログ雛形、記憶候補を確認できるようにする。

### 12.2 コマンド例

```bash
python -m rencrow.coding_agent.cli "JSONLの形式チェックツールを作りたい"
```

### 12.3 オプション

| オプション | 内容 |
|---|---|
| `--repo RenCrow` | 対象repo名。 |
| `--target-file path/to/file.py` | 対象ファイル。複数指定可。 |
| `--planned-command "pytest tests/"` | 想定コマンド。複数指定可。 |
| `--planned-output reports/coding_agent/evt.json` | 想定出力先。複数指定可。 |
| `--json` | JSON形式で出力する。 |
| `--write-output reports/coding_agent/evt_xxx.json` | JSON出力をファイルに保存する。 |

### 12.4 標準出力例

```text
Mode: tool_build
Confidence: 0.88
Reason: 新規小物ツールであり、RenCrow_Tools 配下に単体作成できるため
Requires Human Review: false

Guardrails:
- allowed: true
- blocked: false
- needs_review: false

Worker Prompt:
---
JSONL validator の最小仕様、入力、出力、README構成を整理してください...

Coder Prompt:
---
/home/nyukimi/RenCrow/RenCrow_Tools/tools/jsonl_validator/ 配下に新規ツールを作成してください...

WorkLog:
- event_id: evt_20260515_174512_a1b2c3
- next_action: Workerが最小仕様と入出力を整理する

Memory Candidates:
- なし
```

### 12.5 JSON出力例

```json
{
  "mode_decision": {
    "mode": "tool_build",
    "confidence": 0.88,
    "reason": "新規小物ツールであり、/home/nyukimi/RenCrow/RenCrow_Tools 配下に単体作成できるため",
    "matched_rules": ["tool_build.keyword:ツール"],
    "requires_human_review": false
  },
  "guardrails": {
    "allowed": true,
    "blocked": false,
    "needs_review": false,
    "violations": [],
    "warnings": []
  },
  "prompts": {
    "worker_prompt": "...",
    "coder_prompt": "..."
  },
  "worklog": {
    "event_id": "evt_20260515_174512_a1b2c3",
    "mode": "tool_build",
    "task_summary": "JSONLの形式チェックツールを作成する",
    "retry_count": 0,
    "memory_candidates": [],
    "next_action": "Workerが最小仕様と入出力を整理する"
  },
  "memory_candidates": []
}
```

---

## 13. テンプレート仕様

### 13.1 共通プレースホルダ

| プレースホルダ | 内容 |
|---|---|
| `{{task_text}}` | 入力された依頼文。 |
| `{{mode}}` | `safe_build` または `tool_build`。 |
| `{{reason}}` | モード判定理由。 |
| `{{guardrail_notes}}` | 禁止・要確認・注意事項。 |
| `{{recall_context}}` | 将来の記憶想起差し込み。v0.1では空でもよい。 |
| `{{repo_context}}` | repo構造や対象ファイルの情報。 |
| `{{output_requirements}}` | 出力条件。 |

### 13.2 `safe_worker.md`

含める内容:

```text
あなたは RenCrow の Safe Build Mode で動く Worker です。

目的:
{{task_text}}

判定理由:
{{reason}}

ガードレール:
{{guardrail_notes}}

作業:
1. 既存仕様を読む。
2. 影響範囲を出す。
3. 既存ルール、記憶、過去失敗例を確認する。
4. テスト観点を作る。
5. Coderに渡す小さい作業単位を作る。

出力:
- 影響範囲
- リスク
- 読むべきファイル
- 変更しないもの
- テスト観点
- Coderへの作業指示
```

### 13.3 `safe_coder.md`

含める内容:

```text
あなたは RenCrow の Safe Build Mode で動く Coder です。

目的:
{{task_text}}

ガードレール:
{{guardrail_notes}}

作業:
1. 関連ファイルを読む。
2. 指定範囲だけ変更する。
3. 差分を小さく保つ。
4. 既存挙動を壊さない。
5. テストを実行する。
6. ログを残す。
7. memory candidate を出す。

禁止:
- official DBへ直接writeしない。
- memoryを直接確定しない。
- Source Registryを無審査で追加しない。
- 破壊的操作をしない。
```

### 13.4 `tool_worker.md`

含める内容:

```text
あなたは RenCrow の Tool Build Mode で動く Worker です。

目的:
{{task_text}}

作業:
1. 要望を整理する。
2. 最小仕様を決める。
3. 入力と出力を決める。
4. 保存先ディレクトリを決める。
5. README構成を作る。
6. 将来のRenCrow連携案を整理する。

出力:
- 最小仕様
- 入力
- 出力
- ディレクトリ案
- README構成
- Coderへの作業指示
```

### 13.5 `tool_coder.md`

含める内容:

```text
あなたは RenCrow の Tool Build Mode で動く Coder です。

目的:
{{task_text}}

作業:
1. 新規ディレクトリで作る。
2. READMEを書く。
3. サンプル入力と実行例を付ける。
4. テストを書く。
5. 既存RenCrow本体に直接接続しない。

出力:
- 作成したファイル
- 使い方
- 実行例
- テスト結果
- 今後のRenCrow連携案
```

### 13.6 `worklog_summary.md`

含める内容:

```text
# Worklog Summary

- Event ID: {{event_id}}
- Mode: {{mode}}
- Task: {{task_summary}}
- Next Action: {{next_action}}

## Guardrails
{{guardrail_notes}}

## Memory Candidates
{{memory_candidates}}
```

---

## 14. JSON Schema 仕様

### 14.1 `schemas/mode_decision.schema.json`

必須フィールド:

- `mode`
- `confidence`
- `reason`
- `matched_rules`
- `requires_human_review`

制約:

- `mode`: enum `safe_build` / `tool_build`
- `confidence`: number, 0.0〜1.0
- `reason`: string, 1文字以上
- `matched_rules`: string array
- `requires_human_review`: boolean

### 14.2 `schemas/worklog.schema.json`

必須フィールド:

- `event_id`
- `mode`
- `task_summary`
- `retry_count`
- `memory_candidates`
- `next_action`

制約:

- `event_id`: `^evt_\d{8}_\d{6}_[a-zA-Z0-9]{6}$`
- `mode`: enum `safe_build` / `tool_build`
- `retry_count`: integer, 0以上
- `memory_candidates`: array

### 14.3 `schemas/memory_candidate.schema.json`

必須フィールド:

- `memory_id`
- `type`
- `summary`
- `evidence_event_ids`
- `status`
- `proposed_by`
- `needs_review_by`

制約:

- `status`: v0.1では `candidate` のみ
- `type`: `repo_memory`、`failure_pattern`、`accepted_pattern`、`environment_constraint`、`tool_template`、`recent_tool_patterns` など
- `summary`: string, 1文字以上
- `evidence_event_ids`: string array

---

## 15. 実装順

### Phase 0: 仕様固定

作業:

- `coding_agent_modes.md` を `docs/` に置く。
- `coding_agent_implementation_spec.md` を作る。
- v0.1 スコープを固定する。
- v0.1 でやらないことを明記する。

完了条件:

- 実装対象と非対象が明確になっている。
- 「実際のコード自動編集はしない」と明記されている。
- 既存DBや記憶を勝手に変更しない前提が明確である。

### Phase 1: データモデル作成

作業:

- `rencrow/coding_agent/models.py` を作る。
- `CodingMode` を定義する。
- `AgentRole` を定義する。
- `ModeDecision` を定義する。
- `GuardrailResult` を定義する。
- `PromptBundle` を定義する。
- `WorkLog` を定義する。
- `MemoryCandidate` を定義する。

完了条件:

- データモデルの単体テストが通る。
- `ModeDecision` が `safe_build` / `tool_build` を表現できる。
- `MemoryCandidate.status` が `candidate` を保持できる。

### Phase 2: 設定ファイル作成

作業:

- `config/coding_agent_modes.yaml` を作る。
- `config/guardrails.yaml` を作る。
- `config/memory_types.yaml` を作る。
- YAML読み込みユーティリティを用意する。

完了条件:

- YAMLを読み込める。
- 必須キーが存在する。
- キー不足時に明確なエラーを出せる。

### Phase 3: Mode Selector 実装

作業:

- `mode_selector.py` を作る。
- Safe / Tool 判定を実装する。
- 判定理由を生成する。
- 迷ったら Safe にする。
- `requires_human_review` を適切に立てる。

完了条件:

- 代表例4件で期待モードを返す。
- Source Registry、DB、config、既存APIに関係する依頼は Safe になる。
- RenCrow_Tools 配下の新規小物ツールは Tool になる。

### Phase 4: Guardrails Checker 実装

作業:

- `guardrails.py` を作る。
- 禁止操作を検出する。
- 許可操作を検出する。
- 要確認操作を検出する。
- planned_files / planned_commands / planned_outputs を評価する。

完了条件:

- `rm -rf` を blocked にできる。
- `Move-Item` を blocked にできる。
- official DB write を blocked にできる。
- `/home/nyukimi/RenCrow/RenCrow_Tools` 配下新規作成を allowed にできる。
- config変更を needs_review にできる。

### Phase 5: Prompt Builder 実装

作業:

- `prompt_builder.py` を作る。
- 4種類のテンプレートを読み込む。
- Worker Prompt を生成する。
- Coder Prompt を生成する。
- guardrail warning をプロンプトに反映する。

完了条件:

- Safe Build Mode で Worker / Coder Prompt が生成できる。
- Tool Build Mode で Worker / Coder Prompt が生成できる。
- 禁止事項や注意事項がプロンプト内に入る。

### Phase 6: Worklog Generator 実装

作業:

- `worklog.py` を作る。
- event_id を生成する。
- 作業ログ雛形を生成する。
- JSON出力できるようにする。

完了条件:

- WorkLog JSON が schema に合う。
- event_id が `evt_YYYYMMDD_HHMMSS_xxxxxx` 形式になる。
- 必須項目が欠けない。

### Phase 7: Memory Candidate Generator 実装

作業:

- `memory_candidate.py` を作る。
- memory candidate を作成する。
- status を必ず `candidate` にする。
- confirmed / pinned を出力しない。

完了条件:

- candidate status の記憶候補だけを生成する。
- failure_pattern 候補を生成できる。
- tool_template 候補を生成できる。

### Phase 8: CLI 実装

作業:

- `cli.py` を作る。
- 入力文から全処理を実行する。
- `--repo`、`--target-file`、`--planned-command`、`--planned-output`、`--json`、`--write-output` を実装する。

完了条件:

- コマンド1つで mode / prompts / worklog / memory_candidates が表示される。
- JSON出力できる。
- `reports/` 配下へ出力できる。

### Phase 9: テスト整備

作業:

- `tests/coding_agent/` 配下にユニットテストを作る。
- Mode Selector テストを作る。
- Guardrails テストを作る。
- Prompt Builder テストを作る。
- Worklog テストを作る。
- Memory Candidate テストを作る。
- CLI テストを作る。

完了条件:

- 代表ケースが通る。
- `pytest` が成功する。

### Phase 10: v0.1 完了判定

作業:

- READMEまたはdocsに使い方を書く。
- サンプル入力と出力を保存する。
- `reports/` に出力できることを確認する。

完了条件:

- 手動で依頼文を入れ、実装判断パックを生成できる。
- 既存DBや既存コードを自動変更しない。
- v0.1の非対象機能に踏み込んでいない。

---

## 16. テスト仕様

### 16.1 Mode Selector テスト

| 入力 | 期待結果 |
|---|---|
| JSONLの形式チェックツールを作りたい | Tool Build Mode |
| 既存LLMサーバのOpenAI互換APIを直したい | Safe Build Mode |
| RenCrowの記憶DBスキーマを変更したい | Safe Build Mode |
| MarkdownからHTMLを作る小物ツールが欲しい | Tool Build Mode |
| Source Registryに新しいソースを追加したい | Safe Build Mode |
| RenCrow_Tools 配下にCSV整形ツールを作りたい | Tool Build Mode |

### 16.2 Guardrails テスト

| 入力 | 期待結果 |
|---|---|
| `rm -rf` を含むコマンド | blocked |
| `Move-Item` で既存環境を移動 | blocked |
| official DB write | blocked |
| `staging/` への出力 | allowed |
| `/home/nyukimi/RenCrow/RenCrow_Tools` 配下の新規作成 | allowed |
| config変更 | needs_review |

### 16.3 Prompt Builder テスト

- Safe Build Mode で Worker / Coder Prompt が生成される。
- Tool Build Mode で Worker / Coder Prompt が生成される。
- guardrail warning がプロンプトに反映される。
- `{{task_text}}` などの未置換プレースホルダが残らない。

### 16.4 Worklog テスト

- event_id が生成される。
- 必須項目が存在する。
- JSON Schemaに合う。
- retry_count の初期値が 0 になる。

### 16.5 Memory Candidate テスト

- status は candidate になる。
- confirmed / pinned は生成されない。
- failure_pattern 候補を生成できる。
- tool_template 候補を生成できる。

### 16.6 CLI テスト

- 通常のテキスト出力ができる。
- `--json` でJSON出力できる。
- `--write-output` でファイルに保存できる。
- blocked の場合に終了コードを非ゼロにするか、明確に blocked と表示する。

---

## 17. v0.1 の完了条件

v0.1 は、次の条件を満たしたら完了とする。

- 依頼文から Safe / Tool を判定できる。
- 判定理由を返せる。
- Worker Prompt を生成できる。
- Coder Prompt を生成できる。
- 禁止操作を検出できる。
- WorkLog JSON を生成できる。
- MemoryCandidate JSON を生成できる。
- CLIで手動確認できる。
- 代表テストが通る。
- 既存DBや既存コードを自動変更しない。
- confirmed / pinned memory を生成しない。
- Source Registry を更新しない。
- LangGraphや自律Coderへ未接続である。

---

## 18. v0.2 以降の拡張

v0.2 以降に回す内容は次の通りである。

| 拡張 | 内容 |
|---|---|
| LangGraph への接続 | Mode Selector / Guardrails / Prompt Builder を LangGraph ノード化する。 |
| 実際のWorker / Coderエージェント接続 | Worker / Coder のLLM呼び出しを接続する。 |
| Git diff 自動生成 | Coderが差分案を作れるようにする。 |
| PR作成 | GitHub連携によりPRを作る。 |
| memory candidate の validator 接続 | 候補を validator へ渡す。 |
| WorkLog のSQLite保存 | worklogをDBへ保存する。 |
| reports/ への永続出力 | 作業判断パックをレポートとして残す。 |
| Web UI | モード判定やガードレール確認を画面で見る。 |
| VS Code / Cursor / Codex 連携 | 既存開発ツールへ接続する。 |
| Repoスキャン | repo構造を自動で読み、repo_context を作る。 |
| テスト自動実行 | Coderがテストを実行し、結果をworklogへ入れる。 |
| failure_pattern 候補生成の高度化 | 失敗ログから再利用可能な失敗パターンを抽出する。 |

---

## 19. リスクと注意点

### 19.1 モード判定ミス

リスク:

Tool Build Mode でよいと思った作業が、実際には既存DBや既存本体に触る可能性がある。

対策:

- 迷った場合は Safe Build Mode にする。
- Source Registry、DB、config、既存API、記憶が含まれる場合は強制的に Safe Build Mode にする。
- 判定理由を必ず出力する。

### 19.2 Tool Build Mode から既存本体へ踏み込みすぎるリスク

リスク:

新規ツールのつもりが、途中でRenCrow本体に直接接続してしまう。

対策:

- 初期実装は `/home/nyukimi/RenCrow/RenCrow_Tools`、`experiments/`、`sandbox/` に限定する。
- 既存本体接続が出たら Safe Build Mode に切り替える。
- READMEに「本体連携は後付け」と明記する。

### 19.3 Guardrails の過検出・不足検出

リスク:

過検出により作業が止まりすぎる。逆に不足検出により危険操作を見逃す。

対策:

- `blocked`、`needs_review`、`allowed` を分ける。
- blocked は少数でも強く扱う。
- needs_review を活用して、人間確認へ逃がす。
- テストケースを増やす。

### 19.4 記憶候補の品質

リスク:

一度だけの観測を過剰に一般化してしまう。

対策:

- status は必ず `candidate` にする。
- confirmed / pinned は生成しない。
- evidence_event_ids を必須にする。
- validator または れんの確認を待つ。

### 19.5 過去の誤った記憶を参照するリスク

リスク:

古い環境制約や誤った失敗パターンを参照して、正しい作業を止めてしまう。

対策:

- v0.1では recall_context は placeholder にする。
- 将来接続時は memory status と timestamp を確認する。
- 矛盾する記憶がある場合は Worker に戻す。

### 19.6 Coderに渡すプロンプトが強すぎる／弱すぎるリスク

リスク:

強すぎると作業が進まない。弱すぎると危険操作を許してしまう。

対策:

- Safe Build Mode は慎重にする。
- Tool Build Mode は軽快さを残す。
- ガードレールはプロンプトに必ず含める。
- テンプレートをテスト対象にする。

### 19.7 自律実行を急ぎすぎるリスク

リスク:

判断エンジンが固まる前に自律Coderへ接続すると、既存環境を壊す可能性がある。

対策:

- v0.1ではコード自動編集をしない。
- v0.2以降で段階的に接続する。
- まずはCLIで十分に手動確認する。

---

## 20. 最終まとめ

v0.1 は、「自律コーディングAI」ではなく「コーディングAIの運用判断エンジン」である。

第一目標は、Safe Build Mode / Tool Build Mode の判定を安定させることである。

Worker / Coder の実体を接続する前に、次を固める。

- モード判定
- ガードレール
- Worker / Coder プロンプト
- 作業ログ
- 記憶候補
- CLIでの手動確認
- 代表テスト

この順番にすることで、RenCrow本体を壊さず、将来の自律Coderを安全に載せられる。

Safe Build Mode は、既存システムを守りながら育てるための土台である。Tool Build Mode は、れんの発想をすばやく道具にするための土台である。

v0.1 では、この2つを安全に切り替え、Worker / Coder へ渡す指示を安定して生成できる状態を完成とする。
