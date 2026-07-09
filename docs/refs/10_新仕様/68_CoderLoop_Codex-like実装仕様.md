# 68 CoderLoop — Codex-like 多ターンエージェントループ実装仕様

**作成日**: 2026-06-06  
**ステータス**: 設計確定・実装待ち  
**ブランチ**: feature/RenCrow_Start

---

## 1. 目的と背景

### 1.1 現状の問題

RenCrow の現行コーディングパスは「1ショット生成」である。

```
Coder.GenerateProposal()   ← リポジトリを読まずにパッチを生成
  ↓
Worker.ExecuteProposal()   ← パッチを即時実行
  ↓
終わり
```

Coder はリポジトリを見ずにパッチを作るため、既存コードとの不整合・テスト失敗・依存関係見落としが多発しやすい。

### 1.2 目標

Codex CLI のエージェントループ思想を RenCrow の Chat/Worker/Coder 構成に移植し、  
**「読む → 計画 → パッチ → テスト → 修正 → 報告」の多ターンループ**を実現する。

### 1.3 設計原則

- **Coder は読むだけ・計画するだけ・パッチ案を出すだけ。破壊的操作は Worker のみ。**
- **既存の 1ショットパス（GenerateProposal）は壊さない。** 新ループは別ルートで追加する。
- **git が内部で使えるため、リポジトリ読み取りは shell_command（git grep / git show 等）で実現。** 専用ファイル読み取りツールは不要。
- **コンテキストは追記のみ。** 観測結果を Coder の会話履歴に追加していく（書き換えなし）。
- **観測結果は 2KB 上限でトリム。** コンテキスト爆発を防ぐ。

---

## 2. ループ仕様

### 2.1 全体フロー

```
[入力] Task（ユーザー依頼）
  │
  ▼
┌─────────────────────────────────────────┐
│  CoderLoop（ループコントローラ）           │
│                                          │
│  turn 1: Coder  → read_request          │
│  turn 1: Worker → observation           │
│  turn 2: Coder  → plan                  │
│  turn 3: Coder  → patch_proposal        │
│  turn 3: Worker → observation（実行結果）│
│  turn 4: Coder  → test_request          │
│  turn 4: Worker → observation（テスト）  │
│  turn N: Coder  → final_report          │ ← ループ終了
└─────────────────────────────────────────┘
  │
  ▼
[出力] FinalReport → Mio → ユーザー
```

### 2.2 ループ終了条件

| 条件 | 動作 |
|---|---|
| Coder が `final_report` を出力した | 正常終了 |
| ターン数が上限（デフォルト 8）に達した | 強制終了・partial_report として返す |
| Worker 実行が連続 2 回失敗した | エラー終了・失敗内容を含めて返す |
| Coder が `final_report` 以外の未知の type を出力した | エラー終了 |

---

## 3. CoderMessage 型仕様

Coder は以下の 6 種類の JSON メッセージを出力する。  
各ターンで **1 つの type のみ** 出力する。

### 3.1 read_request — リポジトリ読み取り依頼

```json
{
  "type": "read_request",
  "actions": [
    { "action": "shell_command", "target": "git grep 'GenerateProposal' --include='*.go'" },
    { "action": "shell_command", "target": "git show HEAD:internal/domain/proposal/proposal.go" },
    { "action": "shell_command", "target": "find internal/application -name '*.go' | head -30" }
  ]
}
```

### 3.2 plan — 作業計画

```json
{
  "type": "plan",
  "task_summary": "CoderAgent に GenerateWithContext メソッドを追加する",
  "steps": [
    "internal/domain/agent/coder.go に GenerateWithContext を追加",
    "引数は []llm.Message（会話履歴）",
    "既存 GenerateProposal との互換性を維持"
  ],
  "risk": [
    "llm.GenerateRequest の Messages 構造変更が必要な可能性",
    "既存テストへの影響確認が必要"
  ]
}
```

### 3.3 patch_proposal — パッチ案

```json
{
  "type": "patch_proposal",
  "intent": "GenerateWithContext を CoderAgent に追加する",
  "patch": "[{\"type\":\"file_edit\",\"action\":\"update\",\"target\":\"internal/domain/agent/coder.go\",\"content\":\"...\"}]",
  "tests": [
    "go test ./internal/domain/agent/...",
    "go build ./..."
  ]
}
```

`patch` フィールドは既存の `ParsePatch()` が解析できる形式（JSON 配列または Markdown コードブロック）。

### 3.4 test_request — テスト実行依頼

```json
{
  "type": "test_request",
  "actions": [
    { "action": "shell_command", "target": "go test ./internal/domain/agent/..." },
    { "action": "shell_command", "target": "go build ./..." }
  ]
}
```

### 3.5 revision_request — 修正依頼（テスト失敗時）

```json
{
  "type": "revision_request",
  "reason": "go build が失敗。llm.GenerateRequest に Messages フィールドが存在しない",
  "actions": [
    { "action": "shell_command", "target": "git show HEAD:internal/domain/llm/provider.go" }
  ]
}
```

### 3.6 final_report — 完了報告

```json
{
  "type": "final_report",
  "summary": "GenerateWithContext を CoderAgent に追加した",
  "changed_files": [
    "internal/domain/agent/coder.go"
  ],
  "tests_run": [
    "go test ./internal/domain/agent/... → PASS"
  ],
  "remaining_risks": [
    "llm.Message の Content が string 固定のため、将来的な multimodal 対応で変更が必要"
  ]
}
```

---

## 4. Observation 型仕様（Worker → Coder 返却）

```json
{
  "type": "observation",
  "turn": 1,
  "results": [
    {
      "action": "shell_command",
      "target": "git grep 'GenerateProposal' --include='*.go'",
      "status": "ok",
      "output": "internal/domain/agent/coder.go:94:func (c *CoderAgent) GenerateProposal..."
    },
    {
      "action": "shell_command",
      "target": "git show HEAD:internal/domain/proposal/proposal.go",
      "status": "ok",
      "output": "package proposal\n\ntype Proposal struct ..."
    }
  ]
}
```

**output サイズ制限**: 1 アクションあたり 2,048 文字でトリム（末尾に `...[truncated]` を付加）。

---

## 5. Worker の観測実行仕様

### 5.1 観測アクションの種類

| action | 実行内容 | 実装 |
|---|---|---|
| `shell_command` | bash -lc で実行 | 既存 `executeShellCommand` を流用 |

**観測フェーズで許可するコマンド**（read-only 系）:

```
git grep, git show, git log, git diff, git ls-files
cat, find, head, tail, wc
go test ./..., go build ./...
```

**観測フェーズで禁止するコマンド**:

```
rm, mv, cp（ファイル破壊）
git commit, git reset, git checkout
chmod, chown
```

禁止コマンドが含まれる場合は実行せずエラーを返す。

### 5.2 ExecuteObservation（新規追加）

既存の `ExecuteProposal` とは別に、**読み取り専用実行**のメソッドを追加する。

```go
// WorkerExecutionService に追加
ExecuteObservation(ctx context.Context, actions []ObservationAction) (*ObservationResult, error)
```

内部的には既存の `executeShellCommand` を呼ぶが、  
禁止コマンドチェックを先に行う点が異なる。

---

## 6. Coderコンテキスト管理仕様

### 6.1 会話履歴の構造

```
turn 0: system prompt（codex_like.md）
turn 0: user message（ユーザー依頼 + AGENTS.md 内容）
turn 1: assistant（read_request JSON）
turn 1: user（observation JSON）
turn 2: assistant（plan JSON）
turn 3: assistant（patch_proposal JSON）
turn 3: user（observation JSON）
...
turn N: assistant（final_report JSON）
```

### 6.2 コンテキスト追記ルール

- 履歴は**追記のみ**。書き換えなし。
- 各 observation は 2KB 上限でトリム済みのものを追記する。
- 1 ターン内に複数アクションがある場合は結合して 1 つの user message にまとめる。
- ループ全体のコンテキスト上限: 32,000 トークン相当（超過した場合は古い observation から削除）。

### 6.3 GenerateWithContext（CoderAgent に追加）

```go
// internal/domain/agent/coder.go に追加
func (c *CoderAgent) GenerateWithContext(
    ctx context.Context,
    messages []llm.Message,
) (string, error)
```

既存の `GenerateProposal` は 1 ショット用として残す。  
`GenerateWithContext` はループの各ターンで呼ばれる多ターン用。

---

## 7. AGENTS.md 自動注入仕様

### 7.1 発見ロジック

Codex の AGENTS.md 発見方式を踏襲する。

1. `w.config.Workspace`（= プロジェクトルート）から `AGENTS.md` を読む
2. ループ開始時に Coder の最初の user message に内容を追加する

### 7.2 AGENTS.md の配置

```
/home/nyukimi/RenCrow_CORE/AGENTS.md   ← 新規作成
```

内容: RenCrow のアーキテクチャ概要・主要ディレクトリ・禁止事項・テストコマンド。

---

## 8. 実装対象一覧

### 8.1 新規作成

| ファイル | 内容 | 規模 |
|---|---|---|
| `internal/domain/coderloop/message.go` | CoderMessageType 定義・JSON パーサ | 小 |
| `internal/domain/coderloop/observation.go` | ObservationAction / ObservationResult 型 | 小 |
| `internal/application/orchestrator/code_executor_loop.go` | CoderLoop 多ターンループ本体 | 中〜大 |
| `prompts/coder/codex_like.md` | Coder 向け多ターンループ指示プロンプト | 小 |
| `prompts/worker/safe_executor.md` | Worker 観測/実行境界の指示 | 小 |
| `schemas/agent_message.schema.json` | CoderMessage の JSON Schema | 小 |
| `AGENTS.md` | プロジェクトルートの Coder 向け文脈ファイル | 小 |

### 8.2 既存改修

| ファイル | 改修内容 | 規模 |
|---|---|---|
| `internal/domain/agent/coder.go` | `GenerateWithContext(messages []llm.Message)` 追加 | 小 |
| `internal/application/service/worker_execution_service.go` | `ExecuteObservation()` インターフェース追加 | 小 |
| `internal/application/service/worker_execution_observation.go` | `ExecuteObservation` 実装（新ファイル） | 小 |
| `internal/application/orchestrator/code_executor_proposal.go` | ループパスへの分岐追加 | 小 |
| `internal/application/orchestrator/message_orchestrator.go` | `CoderAgentWithLoop` インターフェース追加 | 小 |

### 8.3 変更不要（既存流用）

| 既存 | 理由 |
|---|---|
| `TypeShellCommand` + `executeShellCommand` | git grep / git show 等の観測コマンドをそのまま実行 |
| `TypeFileEdit` + `executeFileEdit` | patch_proposal のパッチ適用はそのまま使える |
| `TypeGitOperation` + `autoCommitChanges` | 実行後の auto-commit はそのまま使える |
| `ParsePatch()` | patch_proposal の patch フィールド解析はそのまま使える |
| `CoderProposalEvidence` | 証跡保存はそのまま使える |
| `PatchExecutionResult` | Worker 実行結果の型はそのまま流用 |

---

## 9. 実装順序

```
Step 1  internal/domain/coderloop/message.go
        internal/domain/coderloop/observation.go
        schemas/agent_message.schema.json
        （型定義を先に固める）

Step 2  internal/application/service/worker_execution_observation.go
        （ExecuteObservation 実装）

Step 3  internal/domain/agent/coder.go
        （GenerateWithContext 追加）

Step 4  internal/application/orchestrator/code_executor_loop.go
        （ループ本体 ← 核心）

Step 5  prompts/coder/codex_like.md
        prompts/worker/safe_executor.md
        AGENTS.md
        （プロンプト・文脈ファイル）

Step 6  internal/application/orchestrator/code_executor_proposal.go
        internal/application/orchestrator/message_orchestrator.go
        （既存パスに分岐追加・既存動作を壊さず統合）
```

Step 6 まで既存の 1 ショットパスは一切壊れない。  
新ループは `CODE_LOOP` ルートまたは設定フラグで切り替える。

---

## 10. 関連仕様・参照

- `docs/01_正本仕様/実装仕様.md` — 全体アーキテクチャ一次参照
- `docs/10_新仕様/04_Chat_Worker_Coder仕様.md` — Chat/Worker/Coder 責務分離原則
- `internal/domain/patch/command.go` — PatchCommand 型定義
- `internal/domain/proposal/proposal.go` — Proposal 値オブジェクト
- `internal/application/service/worker_execution_service.go` — WorkerExecutionService
- `internal/application/orchestrator/code_executor_proposal.go` — 現行 1 ショットパス
