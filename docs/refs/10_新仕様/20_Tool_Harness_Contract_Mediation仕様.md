# Tool Harness Contract Mediation 仕様

## 1. 目的

本仕様は、RenCrow における Worker / Coder / DCI / その他ツール利用エージェントが発行する tool call を、実ツールに渡す前に検証、修復、安全確認する共通層を定義する。

この層を **Tool Harness Contract Mediation Layer** と呼ぶ。日本語では **ツール契約調停層** と呼ぶ。

目的は、ローカル LLM やオープンソース LLM が出力する軽微なツール入力の揺れを、モデル能力不足として即失敗させず、スキーマに基づいて安全に回復することである。ただし、壊れていない入力は一切変更しない。破壊的操作や意味的に危険な操作は修復対象ではなく、安全ゲートで拒否する。

## 2. 背景

ローカル LLM やオープンソース LLM は、ツール呼び出しで以下のような軽微な入力ミスを起こすことがある。

- optional field に `null` を送る。
- 配列を実際の配列ではなく JSON 文字列として送る。
- 配列が期待される箇所に単一文字列を送る。
- 単一引数を `{}` で不要に包む。
- ファイルパスを Markdown link 形式にしてしまう。
- `offset` と `limit` のような関係フィールドを片方だけ送る。
- schema validator のエラーを読めず、自己修正できない。

これらは必ずしもモデルの推論能力不足ではない。多くの場合、問題は以下の境界にある。

```text
モデル出力
  ↓
ツールスキーマ
  ↓
実行ハーネス
```

RenCrow では、この境界を明示的な共通層として設計する。

## 3. 基本方針

Tool Harness Contract Mediation Layer は、以下の原則で動作する。

1. **validate-then-repair** を採用する。
2. **preprocess-then-validate** は原則禁止する。
3. 有効な入力は一切変更しない。
4. 修復は validator が指摘した issue path に限定する。
5. 修復後は必ず再 validate する。
6. 修復に成功した場合は EventId 付きでログに残す。
7. 修復に失敗した場合は、モデルが読める retry message を返す。
8. 破壊的操作は修復しない。
9. 意味が曖昧な操作は、明示的なデフォルトまたは再試行要求にする。
10. サイレントな入力改変を禁止する。

## 4. RenCrow 内での位置づけ

本層は、Chat / Worker / Coder / DCI から見た tool execution の前段に置く。

```text
Chat / Worker / Coder / DCI
  ↓
Tool Request
  ↓
Tool Harness Contract Mediation Layer
  ↓
Schema Validator
  ↓
Issue-path Repair Layer
  ↓
Relational Invariant Handler
  ↓
Command Gate / Safety Gate
  ↓
Actual Tool Execution
  ↓
Tool Result / Retry Message
```

| コンポーネント | 責務 |
| --- | --- |
| Chat | ユーザー対話、ルーティング判断、結果返却を担当する。tool call の直接実行は担当しない。 |
| Worker | 実行主体として tool call を受け取り、Tool Harness と Safety Gate を通過したものだけを実行する。 |
| Coder | 何をしたいかを計画し、plan / patch / proposal / tool call 候補を生成する。破壊的操作を直接実行しない。 |
| DCI | grep / read / shell などを使った直接コーパス探索を計画する。tool 入力修復は担当しない。 |
| Tool Harness | tool call が契約に合っているか検証し、回復可能なら修復する。 |
| Command Gate | 危険な操作、権限外パス、破壊的コマンドを拒否する。 |
| Actual Tool | file / shell / grep / patch / test / git diff などを実行する。 |

Coder / Worker / DCI ごとに個別の修復ロジックを持たせず、Tool Harness へ集約する。

## 5. 対象ツール

対象となる主な tool は以下である。

```text
readFile
writeFile
listDir
grep
rg
shellCommand
applyPatch
runTest
gitDiff
gitStatus
dciSearch
```

MVP では以下を優先対象とする。

```text
readFile
writeFile
shellCommand
grep / rg
applyPatch
```

## 6. 入力処理フロー

標準フローは以下である。

```text
1. tool call を受け取る
2. 対象 tool の schema を取得する
3. 入力を as-is で validate する
4. validate 成功:
     入力を一切変更せず、そのまま Command Gate へ渡す
5. validate 失敗:
     validator issue list を取得する
6. issue path ごとに修復候補を適用する
7. 再 validate する
8. 再 validate 成功:
     修復済みとしてログを残し、Command Gate へ渡す
9. 再 validate 失敗:
     model-readable retry message を返す
```

以下のような処理は禁止する。

```text
1. 入力全体を先に正規化する
2. null 除去、JSON parse、文字列加工を一律適用する
3. validate する
4. 実行する
```

`writeFile.content` のように、JSON 文字列や Markdown を本文として保存したい field が存在するため、事前の全体正規化はサイレントな破損につながる。

## 7. validate-then-repair 原則

例えば以下の入力がある。

```json
{
  "filePath": "/tmp/data.json",
  "content": "{\"items\":[\"a\",\"b\"]}"
}
```

この入力に対して、事前処理で JSON 文字列を勝手に配列化してはいけない。`content` は schema 上 string として有効なら触らない。修復対象は validator issue が出た path のみに限定する。

## 8. 修復対象の分類

修復対象は大きく 2 種類に分ける。

| 分類 | 内容 |
| --- | --- |
| Shape Invariant | 型、コンテナ、null、省略、文字列配列などの形状問題。 |
| Relational Invariant | 複数フィールド間の関係問題。 |

## 9. Shape Invariant Repair

### 9.1 optional null omission

optional field に `null` が送られた場合、schema 上 optional であり、`null` 自体が有効値ではないときだけ該当 key を削除する。

```json
{
  "absolutePath": "/tmp/a.md",
  "offset": null,
  "limit": null
}
```

期待する修復後:

```json
{
  "absolutePath": "/tmp/a.md"
}
```

### 9.2 json array string parse

配列が JSON 文字列として送られた場合、schema が array を期待し、受信値が parse 可能な JSON array string であるときだけ配列化する。

```json
{
  "paths": "[\"a.md\",\"b.md\"]"
}
```

期待する修復後:

```json
{
  "paths": ["a.md", "b.md"]
}
```

この修復は bare string wrap より先に実行する。

### 9.3 bare string wrap

配列が期待される箇所に生文字列が送られ、かつ JSON array string ではない場合、`array<string>` に限って 1 要素配列へ包む。

```json
{
  "paths": "a.md"
}
```

期待する修復後:

```json
{
  "paths": ["a.md"]
}
```

### 9.4 empty placeholder object unwrap

単一引数のところに不要な `args` / `input` / `params` / `arguments` が送られた場合、内部 object が schema に一致する可能性が高いときだけ root へ展開する。

```json
{
  "args": {
    "absolutePath": "/tmp/a.md"
  }
}
```

期待する修復後:

```json
{
  "absolutePath": "/tmp/a.md"
}
```

### 9.5 markdown autolink path unwrap

path field として登録された field に限り、Markdown link 化された path を安全に unwrap する。

```json
{
  "filePath": "/Users/x/proj/[notes.md](http://notes.md)"
}
```

期待する修復後:

```json
{
  "filePath": "/Users/x/proj/notes.md"
}
```

この修復は path field に限定する。`content`、`body`、`prompt` には適用しない。

## 10. Repair 順序

Shape repair は順序を持つ。

```text
1. optional null omission
2. empty placeholder object unwrap
3. markdown autolink path unwrap
4. json array string parse
5. bare string wrap
```

特に以下を守る。

- `json array string parse` は `bare string wrap` より先に行う。
- `path unwrap` は `content` field に適用しない。
- `optional null omission` は schema optional に限定する。

## 11. Relational Invariant Handling

Relational Invariant とは、各フィールド単体では有効だが、組み合わせとして不完全または矛盾している状態である。

### 11.1 readFile offset / limit

`offset` と `limit` は、どちらか片方だけ来た場合、ツール側で意味を補えるなら補う。

```text
limit のみ:
  offset = 0 を補う

offset のみ:
  limit = 2000 を補う
```

実行結果には注記を添える。

```text
Note: offset was not provided; defaulted to 0.
To read another range, retry with both offset and limit.
```

これはエラーではない。`ERROR:` prefix は付けない。

### 11.2 patch target relation

patch 内容はあるが target file が不明な場合、自動修復しない。誤ったファイルに patch を当てる危険があるためである。

返す retry message の例:

```text
Tool input was incomplete for applyPatch.

Problem:
- patch content was provided
- target file could not be determined safely

Retry with:
{
  "targetPath": "path/to/file",
  "patch": "..."
}
```

### 11.3 destructive operation confirmation

削除、無確認上書き、権限変更、`git reset --hard` などの破壊的操作は修復対象ではない。Command Gate で止める。

```text
This tool request was blocked by the safety gate.

Reason:
- destructive operation requires explicit approval
- automatic repair is not allowed for this class of action
```

## 12. Command Gate / Safety Gate

Tool Harness で修復された入力は、必ず Command Gate を通る。修復成功は、安全実行の許可を意味しない。

| 分類 | 方針 |
| --- | --- |
| `read_only` | 許可。 |
| `write_safe` | 条件付き許可。 |
| `write_sensitive` | 明示確認または proposal 化。 |
| `network` | 原則拒否または別権限。 |
| `destructive` | 拒否。 |
| `unknown` | 拒否または確認。 |

shell command の分類例:

| 方針 | コマンド例 |
| --- | --- |
| 許可 | `rg`, `grep`, `find`, `sed -n`, `head`, `tail`, `wc`, `git status`, `git diff` |
| 条件付き | 読取用途の `python3`, `go test`, `pytest`, `npm test` |
| 拒否 | `rm`, `mv`, `chmod`, `chown`, `curl`, `wget`, `git push`, `git reset --hard`, `npm install`, `pip install` |

## 13. Model-readable Retry Message

修復できなかった場合、validator の生エラーをそのまま返さない。モデルが次に何を直せばよいか分かる形式で返す。

```text
Tool input was invalid for readFile.

Problem:
- field: paths
- expected: array of strings
- received: string

Retry using:
{
  "paths": ["example/path.md"]
}
```

複数 issue の場合も、field、expected、received、retry example を明示する。

## 14. ログと EventId

すべての tool mediation には EventId を付与する。

イベント種別:

```text
tool_input_valid:{toolName}
tool_input_repaired:{toolName}
tool_input_invalid:{toolName}
tool_input_blocked:{toolName}
tool_input_relation_defaulted:{toolName}
tool_execution_started:{toolName}
tool_execution_completed:{toolName}
tool_execution_failed:{toolName}
```

ログ例:

```json
{
  "event_id": "evt_tool_20260518_000001",
  "tool_name": "readFile",
  "model_name": "deepseek-v4-pro",
  "actor": "Coder",
  "raw_input_hash": "sha256...",
  "validation_status": "repaired",
  "repairs_applied": [
    {
      "type": "bare_string_wrap",
      "path": ["paths"],
      "before_type": "string",
      "after_type": "array"
    }
  ],
  "relation_defaults_applied": [
    {
      "field": "offset",
      "value": 0,
      "reason": "limit was provided without offset"
    }
  ],
  "command_gate_status": "allowed",
  "created_at": "2026-05-18T12:00:00Z"
}
```

raw input 全文は原則保存しない。保存する場合は secret redaction 済み、file content を含まない、sensitivity 付与済みであることを条件にする。基本は hash 保存を優先する。

## 15. Telemetry

Tool Harness は、モデル別、ツール別の修復率を記録する。

```text
tool_call_count
tool_input_valid_rate
tool_input_repair_rate
tool_input_invalid_rate
tool_input_blocked_rate
repair_type_count
repair_success_rate
retry_success_rate
model_tool_regression_rate
```

用途:

- モデルごとのツール適性評価。
- tool schema の改善。
- prompt 改善。
- Worker / Coder のルーティング判断。
- 回帰検知。
- ローカル LLM 採用判断。

## 16. 永続化設計

MVP では SQLite に保存する。

### 16.1 tool_mediation_event

```sql
CREATE TABLE IF NOT EXISTS tool_mediation_event (
  event_id TEXT PRIMARY KEY,
  parent_event_id TEXT,
  actor TEXT NOT NULL,
  model_name TEXT,
  tool_name TEXT NOT NULL,
  raw_input_hash TEXT,
  validation_status TEXT NOT NULL,
  command_gate_status TEXT,
  retry_message TEXT,
  created_at TEXT NOT NULL
);
```

### 16.2 tool_repair_log

```sql
CREATE TABLE IF NOT EXISTS tool_repair_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  event_id TEXT NOT NULL,
  repair_type TEXT NOT NULL,
  issue_path TEXT NOT NULL,
  before_type TEXT,
  after_type TEXT,
  note TEXT,
  created_at TEXT NOT NULL
);
```

### 16.3 tool_relation_default_log

```sql
CREATE TABLE IF NOT EXISTS tool_relation_default_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  event_id TEXT NOT NULL,
  default_type TEXT NOT NULL,
  field_name TEXT NOT NULL,
  default_value TEXT NOT NULL,
  reason TEXT,
  surfaced_to_model INTEGER DEFAULT 1,
  created_at TEXT NOT NULL
);
```

### 16.4 tool_invalid_log

```sql
CREATE TABLE IF NOT EXISTS tool_invalid_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  event_id TEXT NOT NULL,
  tool_name TEXT NOT NULL,
  issue_path TEXT,
  expected TEXT,
  received TEXT,
  retry_message TEXT,
  created_at TEXT NOT NULL
);
```

### 16.5 tool_block_log

```sql
CREATE TABLE IF NOT EXISTS tool_block_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  event_id TEXT NOT NULL,
  tool_name TEXT NOT NULL,
  block_reason TEXT NOT NULL,
  command_text TEXT,
  risk_class TEXT,
  created_at TEXT NOT NULL
);
```

## 17. ツール定義側の要件

各 tool は以下を持つ。

```yaml
tool:
  name: readFile
  schema: ...
  repair_policy: ...
  relational_invariants: ...
  safety_class: read_only
  model_retry_template: ...
```

readFile 定義例:

```yaml
tool:
  name: readFile
  safety_class: read_only

  fields:
    absolutePath:
      type: path
      required: true

    offset:
      type: integer
      required: false

    limit:
      type: integer
      required: false

  shape_repairs:
    - optional_null_omission
    - markdown_autolink_path_unwrap

  relational_invariants:
    offset_limit_pair:
      if_limit_without_offset:
        default:
          offset: 0
        surface_note: true

      if_offset_without_limit:
        default:
          limit: 2000
        surface_note: true
```

writeFile 定義例:

```yaml
tool:
  name: writeFile
  safety_class: write_sensitive

  fields:
    filePath:
      type: path
      required: true

    content:
      type: string
      required: true

  shape_repairs:
    - markdown_autolink_path_unwrap

  forbidden_repairs:
    - json_parse_content
    - content_auto_format

  command_gate:
    require_diff_if_overwrite: true
    deny_secret_paths: true
```

## 18. writeFile の特別ルール

writeFile はサイレント破損の危険が高い。

`content` field には以下を適用しない。

```text
json array string parse
markdown autolink unwrap
trim
format
escape normalize
newline normalize
```

path field に限り、以下を許可する。

```text
optional null omission
markdown autolink path unwrap
全角半角の安全な正規化
```

既存ファイルを上書きする場合は、Command Gate で以下を確認する。

- 対象ファイルが allowlist 内か。
- denylist に該当しないか。
- diff または patch proposal があるか。
- Coder が直接破壊的変更をしていないか。

## 19. shellCommand の特別ルール

shellCommand で修復してよいのは、原則として引数形状のみである。

```text
commands: "rg foo docs/"
  ↓
commands: ["rg foo docs/"]
```

コマンド意味の変更は禁止する。

```text
禁止:
  rm を安全な別コマンドに置換する
  mv を cp に置換する
  git reset を git checkout に置換する
  curl を別 URL に変える
```

危険なコマンドは Command Gate で拒否する。

## 20. DCI との接続

DCI は `rg` / `grep` / `readFile` を多用する。そのため、DCI から発行される tool call も本層を必ず通す。

```text
DCI Query Planner
  ↓
Tool Call: rg
  ↓
Tool Harness
  ↓
Command Gate
  ↓
rg execution
  ↓
Search Trace
```

責務分担:

| 領域 | 責務 |
| --- | --- |
| DCI | 何を探すか、どの順番で読むかを決める。 |
| Tool Harness | readFile / rg / shell の入力を検証し、必要なら安全に修復する。 |
| Command Gate | 読取専用であることを確認する。 |

DCI 側でツール入力修復を個別実装してはいけない。修復ロジックは Tool Harness へ集約する。

## 21. Worker / Coder との接続

Worker は実行主体である。Worker は Tool Harness を通過した tool call のみ実行する。

```text
Worker receives tool call
  ↓
Tool Harness
  ↓
Command Gate
  ↓
Execution
```

Coder は計画、patch proposal 生成が主責務である。Coder が直接 tool を使う場合も、Tool Harness を通す。

Coder がしてよいこと:

```text
readFile
grep
rg
git diff
test 実行
patch proposal 生成
```

Coder がしてはいけないこと:

```text
破壊的変更の直接実行
無確認の上書き
git push
dependency install
環境変更
```

## 22. エラー表示方針

ユーザーには、低レベルな schema error をそのまま見せない。

```text
ツール入力に軽微な不整合があったため、自動修復して実行しました。
```

または、

```text
ツール入力を安全に修復できなかったため、実行しませんでした。
```

モデルには、次回の tool call を修正できる情報を返す。ログには `repair_type`、`issue_path`、`before_type`、`after_type`、`tool_name`、`model_name`、`actor`、`event_id` を保存する。

MVPでは、Tool Harness mediation event を JSONL に保存し、Viewer API から recent event を確認できるようにする。

```text
GET /viewer/tool-harness/recent?limit=50
```

この API は diagnostics 用であり、tool call の raw input 本文は返さない。`raw_input_hash`、`validation_status`、`repairs_applied`、`relation_defaults_applied` を中心に表示する。

## 23. セキュリティ

基本原則:

- 修復は安全性を下げてはいけない。
- 修復で権限を拡大してはいけない。
- 修復でパス範囲を広げてはいけない。
- 修復で書き込み先を変えてはいけない。
- 修復でコマンドの意味を変えてはいけない。

全ての path field は以下を確認する。

- allowlist 内か。
- denylist に該当しないか。
- path traversal を含まないか。
- secret path ではないか。
- absolute path の場合、許可 root 配下か。

以下は拒否する。

```text
../
..\
%2e%2e
~/
環境変数展開を含む危険パス
```

## 24. 設定ファイル案

### 24.1 tool_harness.yaml

```yaml
tool_harness:
  enabled: true
  mode: validate_then_repair
  record_events: true
  log_path: "./workspace/logs/tool_mediation.jsonl"

  default_limits:
    max_repair_attempts: 1
    max_retry_messages: 1

  repair_order:
    - optional_null_omission
    - empty_placeholder_object_unwrap
    - markdown_autolink_path_unwrap
    - json_array_string_parse
    - bare_string_wrap

  global_repairs:
    optional_null_omission:
      enabled: true

    json_array_string_parse:
      enabled: true
      only_on_schema_array: true

    bare_string_wrap:
      enabled: true
      only_on_schema_array: true

    markdown_autolink_path_unwrap:
      enabled: true
      only_on_path_fields: true

    empty_placeholder_object_unwrap:
      enabled: true
      wrapper_keys:
        - args
        - input
        - params
        - arguments

  logging:
    log_raw_input_hash: true
    log_raw_input_body: false
    log_repairs: true
    log_invalid: true
    log_blocked: true

  retry_message:
    model_readable: true
    include_expected_shape: true
    include_example: true
    include_raw_validator_issues: false
```

### 24.2 tool_safety.yaml

```yaml
tool_safety:
  shell:
    allow:
      - rg
      - grep
      - find
      - sed
      - head
      - tail
      - wc
      - git status
      - git diff

    deny:
      - rm
      - mv
      - chmod
      - chown
      - curl
      - wget
      - git push
      - git reset
      - npm install
      - pip install

  paths:
    allow_roots:
      - docs/
      - internal/
      - cmd/
      - test/
      - prompts/
      - memory/
      - records/
      - staging/

    deny_patterns:
      - ".env"
      - "*.pem"
      - "*.key"
      - "id_rsa"
      - "credentials.json"
      - "token.json"
      - "cookies.sqlite"

    deny_dirs:
      - node_modules/
      - .venv/
      - venv/
      - .git/
      - secrets/
      - private/
```

## 25. 実装ファイル案

RenCrow の現行 Go 構成に合わせ、実装候補は以下とする。

```text
internal/domain/toolharness/
  tool.go
  schema.go
  repair.go
  safety.go
  event.go

internal/application/toolharness/
  mediator.go
  validator.go
  repair_engine.go
  retry_message.go
  relational_invariants.go

internal/application/toolharness/repairs/
  optional_null.go
  json_array_string.go
  bare_string_wrap.go
  placeholder_unwrap.go
  markdown_path_unwrap.go

internal/infrastructure/tools/
  runner_*.go
  command_gate.go
  path_guard.go

internal/infrastructure/persistence/toolharness/
  sqlite_events.go
  sqlite_repairs.go
  sqlite_blocks.go

cmd/picoclaw/
  runtime_tool_harness.go
```

既存 `internal/application/toolloop`、`internal/infrastructure/tools`、`internal/infrastructure/security` と責務が重なる場合は、既存境界を優先し、Tool Harness は tool call 入力契約の検証、修復、retry message に責務を限定する。

## 26. 疑似コード

```go
func MediateToolCall(ctx context.Context, req ToolCallRequest) ToolMediationResult {
	eventID := NewToolEventID()
	schema := schemaRegistry.Get(req.ToolName)

	result := schema.Validate(req.RawInput)
	if result.OK {
		logger.LogValid(eventID, req)
		return commandGate.Check(ctx, req.WithEventID(eventID))
	}

	repaired := req.RawInput.Clone()
	repairs := make([]RepairNote, 0)

	for _, issue := range result.Issues {
		repair := repairEngine.FindRepair(issue, schema, repaired)
		if repair == nil {
			continue
		}
		next, note := repair.Apply(repaired, issue)
		repaired = next
		repairs = append(repairs, note)
	}

	second := schema.Validate(repaired)
	if !second.OK {
		retry := retryMessageBuilder.Build(req.ToolName, second.Issues)
		logger.LogInvalid(eventID, req, second.Issues, retry)
		return ToolMediationResult{Status: "invalid", EventID: eventID, RetryMessage: retry}
	}

	relation := relationalHandler.Apply(req.ToolName, repaired)
	gate := commandGate.Check(ctx, req.WithInput(relation.Input).WithEventID(eventID))
	logger.LogRepaired(eventID, req, repairs, relation.Defaults, gate.Status)
	return gate
}
```

## 27. テスト方針

必須テスト:

- optional null が削除される。
- JSON array string が配列化される。
- bare string が配列化される。
- JSON array string が bare string wrap されない。
- Markdown path link が path field だけで unwrap される。
- `content` field の Markdown は変更されない。
- `limit` のみ指定で `offset=0` が補われる。
- `offset` のみ指定で `limit=2000` が補われる。
- patch target 不明は自動修復されない。
- 危険 shell command は拒否される。
- 有効入力は一切変更されない。

推奨確認コマンド:

```bash
GOCACHE=/tmp/picoclaw-gocache go test ./internal/domain/toolharness ./internal/application/toolharness ./internal/infrastructure/tools
GOCACHE=/tmp/picoclaw-gocache go test ./internal/application/toolloop ./internal/application/autonomous ./internal/application/subagent
```

## 28. 成功指標

```text
tool_input_invalid_rate の低下
tool_retry_success_rate の向上
Coder task completion rate の向上
Worker execution failure の減少
repair 後の安全事故ゼロ
writeFile content 破損ゼロ
Command Gate bypass ゼロ
```

特に重要な指標:

```text
有効入力を変更しなかった率 = 100%
writeFile content 自動改変件数 = 0
destructive command 修復実行件数 = 0
```

## 29. MVP 実装順

### Phase 1: validate-then-repair 基盤

- schema registry
- validator
- repair engine
- EventId logging
- model-readable retry message

2026-05-18 時点のMVP実装では、`internal/domain/toolharness` に code-based `ToolSpec` registry を置く。これは `required`, `optional`, `path`, `array<string>` field を tool ごとに宣言する最小 registry である。また、既存 `internal/domain/tool.ToolManifest` の `input_schema` から `ToolSpec` を導出する入口も追加し、manifest / JSON Schema driven registry の最小経路を確保する。

### Phase 2: Shape repair 4 種

- optional null omission
- json array string parse
- bare string wrap
- empty placeholder object unwrap

### Phase 3: path 修復

- markdown autolink path unwrap
- path field 限定適用
- content field 保護

### Phase 4: relational invariant

- readFile offset / limit default
- 補足 note を結果に表示
- relation default log

### Phase 5: safety 統合

- Command Gate
- path guard
- shell allow / deny
- destructive operation block

### Phase 6: telemetry

- model x tool 修復率
- regression detection
- dashboard 用 metrics

## 30. DCI 仕様との関係

DCI は「原文を調べ直す能力」である。Tool Harness Contract Mediation Layer は「ツール呼び出しを失敗から復帰させる能力」である。

両者は別機能だが、DCI は Tool Harness に依存する。

```text
DCI:
  探索戦略

Tool Harness:
  ツール入力の契約調停

Command Gate:
  安全実行判定

Actual Tool:
  実行
```

したがって、DCI 仕様より後、または同時に本仕様を実装するのが望ましい。

## 31. 設計上の結論

Tool Harness Contract Mediation Layer は、RenCrow の Worker / Coder / DCI を実用レベルにするための共通実行基盤である。

この層がない場合、ローカル LLM やオープンソース LLM の軽微な tool call 揺れが、そのまま失敗として扱われる。その結果、モデルの能力不足ではなくハーネスの硬さによって、Worker / Coder の性能が低く見える。

RenCrow では、以下を原則とする。

```text
壊れていない入力は触らない。
壊れている箇所だけ、schema issue path に基づいて修復する。
危険な操作は修復せず止める。
修復したら必ずログに残す。
モデルには次に直せる形で返す。
```

この設計により、RenCrow はローカル LLM をより安定して Worker / Coder として使えるようになる。
