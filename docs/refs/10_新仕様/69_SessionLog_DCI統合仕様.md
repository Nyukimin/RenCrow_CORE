# 69 セッションログ + DCI統合仕様

## 概要

RenCrow に「会話セッション単位のターンログ」を追加し、それを DCI（直接コーパス探索）で
検索可能にする。また同じ仕組みで Codex・Claude Code のセッションログも参照できる。

---

## 1. RenCrow セッションログ

### 1.1 書き込み先

```
~/.rencrow/logs/sessions/{YYYY-MM}/session_{YYYY-MM-DD}_{session_id}.jsonl
```

### 1.2 エントリ形式

```json
{"ts":"2026-06-06T12:00:00Z","session_id":"viewer","channel":"line","role":"user","content":"質問内容"}
{"ts":"2026-06-06T12:00:02Z","session_id":"viewer","channel":"line","role":"assistant","route":"CHAT","job_id":"job_...","content":"回答内容"}
```

### 1.3 実装

- `internal/infrastructure/logging/session_log_writer.go`
  - `SessionLogWriter.WriteUser()` / `WriteAssistant()`
  - セッションIDは安全な文字列に正規化（64文字上限）
- `MessageOrchestrator.SetSessionTurnLogger(l SessionTurnLogger)` で注入
- `cmd/rencrow/runtime_orchestrator.go` で DI 配線済み
- ProcessMessage の直後に WriteUser、完了後に WriteAssistant を呼ぶ

---

## 2. DCI セッションログ統合

### 2.1 対応ソース

| ソース名 | パス | フォーマット |
|----------|------|-------------|
| rencrow  | `~/.rencrow/logs/sessions/` | `rencrow` |
| codex    | `~/.codex/sessions/` | `codex` |
| claude   | `~/.claude/projects/-home-nyukimi-rencrow-multiLLM/` | `claude` |

### 2.2 実装

- `internal/infrastructure/persistence/dci/session_log_candidate_provider.go`
  - `SessionLogCandidateProvider.CandidateFiles()` — 直近90日のJSONLをスキャン
  - `scoreSessionFile()` — クエリ・タームの出現頻度でスコアリング（先頭200行）
  - フォーマット別パーサ: RenCrow `content`、Codex `payload.content`、Claude `message.content[]`
- `cmd/rencrow/runtime_dependencies.go`
  - `buildSessionLogSources()` — 設定があればそれを使用、なければデフォルト3ソース
  - セッションログソースのパスは CorpusAllowlist に自動追加（DCI がファイルを grep できる）
  - `dciapp.WithSourceCandidateProvider()` で DCI Explorer に登録

### 2.3 設定（オプション）

`rencrow.yml` で明示設定もできる:

```yaml
dci:
  session_log_sources:
    - name: rencrow
      path_dir: "${HOME}/.rencrow/logs/sessions"
      format: rencrow
    - name: codex
      path_dir: "${HOME}/.codex/sessions"
      format: codex
    - name: claude
      path_dir: "${HOME}/.claude/projects/-home-nyukimi-rencrow-multiLLM"
      format: claude
```

設定が空の場合は上記のデフォルトが自動適用される。

---

## 3. 各ソースのJSONL実フォーマット

### 3.1 RenCrow（`rencrow`）

本実装で新規追加。1ターン1行。

```json
{"ts":"2026-06-06T12:00:00Z","session_id":"viewer","channel":"line","role":"user","content":"質問内容"}
{"ts":"2026-06-06T12:00:02Z","session_id":"viewer","channel":"line","role":"assistant","route":"CHAT","job_id":"job_...","content":"回答内容"}
```

抽出キー: `content`

### 3.2 Claude Code（`claude`）

`~/.claude/projects/{project-slug}/{uuid}.jsonl`

```json
{"type":"user","message":{"content":"serena 起動した？"},"sessionId":"...","timestamp":"..."}
{"type":"assistant","message":{"content":[{"type":"text","text":"応答テキスト"}]},"sessionId":"...","timestamp":"..."}
```

- `message.content` は **文字列**（ユーザー発話）または **`[{type,text}]` 配列**（アシスタント応答）のどちらもある
- `type=tool_use` / `type=tool_result` は会話テキストを持たないためスキップ
- 抽出キー: `message.content`（文字列 or 配列の `text` フィールドを結合）

### 3.3 Codex（`codex`）

`~/.codex/sessions/{YYYY}/{MM}/{DD}/rollout-{datetime}-{uuid}.jsonl`

```json
{"timestamp":"...","type":"session_meta","payload":{"id":"...","cwd":"...","base_instructions":{"text":"..."}}}
```

- 現状確認できる rollout ファイルは `session_meta` 型のみ（会話テキストは `payload.base_instructions.text` に含まれる場合がある）
- ユーザー発話・アシスタント応答が別エントリとして記録されるかはバージョン依存
- `payload.content` / `payload.text` / `payload.message` を順にフォールバック取得する実装で最大限対応
- **実質的にプロジェクト設定・指示文の参照ソースとして機能する**

---

## 4. DCI 検索フロー

```
ユーザー: "前回CoderLoopのバグどう直した？"
  │
  ▼
DCI.ShouldTrigger() → ExplicitKeywords にマッチ or CoderLoop/セッション関連キーワード
  │
  ▼
SessionLogCandidateProvider.CandidateFiles()
  ├─ ~/.rencrow/logs/sessions/**/*.jsonl （直近90日）
  ├─ ~/.claude/projects/.../**/*.jsonl    （直近90日）
  └─ ~/.codex/sessions/**/*.jsonl         （直近90日）
  　　各ファイルを先頭200行スキャン → クエリ/タームの出現頻度でスコアリング
  │
  ▼
上位ファイルを CorpusAllowlist 経由で DCI Explorer が cat/grep
  │
  ▼
Evidence として Coder/Chat に渡される
```

---

## 5. 既存ログとの関係・使い分け

| ログ | 場所 | 用途 |
|------|------|------|
| **セッションログ（本機能）** | `~/.rencrow/logs/sessions/` | 会話ターン単位。DCI で検索可能 |
| rencrow.log | `~/.rencrow/logs/rencrow.log` | システム全体の構造化ログ（接続状態・エラー等） |
| chat_raw.log | `~/.rencrow/logs/chat_raw.log` | LLM への生リクエスト/レスポンス（デバッグ用） |
| coder_transcript_log.jsonl | `~/.rencrow/workspace/logs/skill_governance/` | Coder の plan/patch 単位の詳細トランスクリプト |
| orchestrator_event_log.jsonl | `~/.rencrow/workspace/` | ルーティング・実行イベント（Viewer 表示用） |

セッションログは「会話の文脈を後から検索する」目的に特化。デバッグや監査には既存の各ログを使う。

---

## 6. 保持期間・ログローテーション

- セッションログは月別ディレクトリ（`YYYY-MM/`）に自動整理される
- 既存の `~/.rencrow/bin/log-rotate.sh`（cron 毎日04:00）の対象外のため、長期運用では手動またはスクリプト追加が必要
- DCI の候補スキャンは **直近90日** に限定しているため、古いファイルが残っていても検索性能に影響しない

---

## 7. 既知の制限事項

- **Distributed Mode 非対応**: v3 Local Mode の `MessageOrchestrator` のみにロガーが注入される。v4 Distributed Mode（`DistributedOrchestrator`）は未対応
- **エラー応答は記録されない**: `ProcessMessage` がエラーを返した場合、`WriteAssistant` は呼ばれない
- **Codex フォーマット不確定**: Codex のセッションファイル構造はバージョンにより変わる可能性がある。テキストが取れない場合は候補スコアが0になり静かにスキップされる
- **セッションログへの書き込み失敗はサイレント**: ディスクフルやパーミッションエラーが発生しても本体処理に影響しない（ログなし）

---

## 8. 効果

- 「前回どう直した？」「この問題は解決済み？」という質問に DCI が自動でセッション履歴を参照して回答できる
- Codex・Claude Code のセッション履歴も同一の検索経路で参照可能
- CoderLoop の観察ステップ（`read_request`）から `git grep` と同様にセッション履歴を参照可能

---

## 9. 関連ファイル

- `internal/infrastructure/logging/session_log_writer.go`
- `internal/infrastructure/persistence/dci/session_log_candidate_provider.go`
- `internal/adapter/config/config_types.go` — `DCIConfig.SessionLogSources`
- `internal/application/orchestrator/message_orchestrator.go` — `SessionTurnLogger` インターフェース
- `cmd/rencrow/runtime_orchestrator.go` — DI 配線
- `cmd/rencrow/runtime_dependencies.go` — `buildSessionLogSources()`
