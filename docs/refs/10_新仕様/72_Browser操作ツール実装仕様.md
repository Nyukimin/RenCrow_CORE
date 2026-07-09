# Browser 操作ツール実装仕様

## 1. 目的

`docs/10_新仕様/71_Browser操作ツール仕様.md` を、実装に着手できる粒度へ落とす。

本仕様では、RenCrow に browser operation capability を追加する。ただし初期実装では RenCrow 本体に Playwright を直接組み込まず、`RenCrow_Tools/tools/browser_actor` の Node.js sidecar CLI を第一経路とする。Go 側は JSON 入出力で sidecar を呼び、ToolRunner V2 から `browser.run` として公開する。

## 2. 実装スコープ

### 2.1 Phase 1 で作るもの

- `RenCrow_Tools/tools/browser_actor/run_browser_actor.mjs`
- `RenCrow_Tools/tools/browser_actor/README.md`
- `RenCrow_Tools/tools/browser_actor/fixtures/basic_form.html`
- `RenCrow_Tools/tools/browser_actor/test_browser_actor.mjs`
- JSON input / JSON output
- headless Chromium 実行
- `open`, `wait_for_selector`, `click`, `fill`, `press`, `screenshot`, `snapshot`, `extract_text`, `close`
- artifact 保存
- network / console summary 保存
- secret masking
- origin allowlist
- external effect の事前 block
- `doctor`

### 2.2 Phase 2 で作るもの

- `BrowserActorConfig`
- config defaults / validation
- `cmd/picoclaw/cli_browser_actor.go`
- `picoclaw browser-actor run`
- `picoclaw browser-actor doctor`
- `internal/infrastructure/browseractor`
- `internal/infrastructure/tools/runner_browser_actor.go`
- `ToolRunnerConfig.BrowserActorRunner`
- `browser.run` ToolRunner V2 metadata
- PolicyRunner / ToolHarness を通る構成
- Go unit / CLI test

### 2.3 Phase 3 で作るもの

- `BrowserProfile` metadata
- `profile login`
- Playwright `storageState` save / load
- `browser.profile.status`
- Cookie 値非表示 test
- profile origin allowlist

### 2.4 Phase 4 で作るもの

- BrowserTrace discovery 連携
- Workstream artifact 登録
- Viewer Ops summary
- latest screenshot thumbnail
- browser actor runtime readiness

### 2.5 Phase 1 で作らないもの

- MCP bridge
- Firefox / WebKit
- persistent browser server
- file upload
- human approval UI
- form submit / post / purchase / delete の実行
- login profile
- sessionStorage 保存
- raw response body の永続保存

## 3. 基本アーキテクチャ

```text
ToolLoop / Worker
  ↓
ToolRunner V2
  ↓
browser.run
  ↓
internal/infrastructure/browseractor.Runner
  ↓
picoclaw browser-actor run
  ↓
RenCrow_Tools/tools/browser_actor/run_browser_actor.mjs
  ↓
Playwright Chromium headless
```

Phase 1 は `RenCrow_Tools/tools/browser_actor` 単体で完結させる。

Phase 2 で Go adapter を追加する。Go adapter は sidecar の JSON contract だけに依存し、Playwright API を直接 import しない。

## 4. ディレクトリ構成

```text
RenCrow_Tools/tools/browser_actor/
  README.md
  run_browser_actor.mjs
  test_browser_actor.mjs
  fixtures/
    basic_form.html
    navigation.html
    external_effect.html

modules/browseractor/
  contract.go
  policy.go
  masking.go
  validation.go
  errors.go

internal/infrastructure/browseractor/
  runner.go
  command.go
  artifact.go
  runner_test.go

internal/infrastructure/tools/
  runner_browser_actor.go
  runner_browser_actor_test.go

cmd/picoclaw/
  cli_browser_actor.go
  cli_browser_actor_test.go
```

Phase 1 では `RenCrow_Tools/tools/browser_actor` のみ作る。Phase 2 以降で Go package を追加する。

## 5. Sidecar CLI contract

### 5.1 command

```bash
node /home/nyukimi/RenCrow/RenCrow_Tools/tools/browser_actor/run_browser_actor.mjs run --json < request.json
node /home/nyukimi/RenCrow/RenCrow_Tools/tools/browser_actor/run_browser_actor.mjs doctor --json
```

Phase 2 の Go CLI はこれを包む。

```bash
picoclaw browser-actor run --json < request.json
picoclaw browser-actor doctor --json
```

### 5.2 request

```json
{
  "schema_version": "1.0",
  "run_id": "browser_run_manual_1",
  "goal": "Open fixture and fill a draft form without submitting",
  "start_url": "file:///home/nyukimi/RenCrow/RenCrow_Tools/tools/browser_actor/fixtures/basic_form.html",
  "profile_id": "",
  "storage_state_path": "",
  "headless": true,
  "viewport": {"width": 1366, "height": 900},
  "allowed_origins": ["file://"],
  "artifact_dir": "workspace/browser_runs/browser_run_manual_1",
  "timeout_ms": 30000,
  "max_actions": 20,
  "save_trace": true,
  "save_screenshot": true,
  "mask_secrets": true,
  "actions": [
    {"type": "open"},
    {"type": "wait_for_selector", "selector": "#name"},
    {"type": "fill", "selector": "#name", "value": "RenCrow"},
    {"type": "screenshot", "name": "filled"},
    {"type": "snapshot"},
    {"type": "extract_text", "selector": "body"}
  ]
}
```

### 5.3 response

```json
{
  "schema_version": "1.0",
  "run_id": "browser_run_manual_1",
  "status": "completed",
  "risk_level": "draft_input",
  "started_at": "2026-06-08T00:00:00Z",
  "completed_at": "2026-06-08T00:00:03Z",
  "start_url": "file:///...",
  "final_url": "file:///...",
  "title": "Browser Actor Fixture",
  "artifact_dir": "workspace/browser_runs/browser_run_manual_1",
  "artifacts": {
    "run": "workspace/browser_runs/browser_run_manual_1/run.json",
    "actions": "workspace/browser_runs/browser_run_manual_1/actions.jsonl",
    "console": "workspace/browser_runs/browser_run_manual_1/console.jsonl",
    "network": "workspace/browser_runs/browser_run_manual_1/network.jsonl",
    "snapshot": "workspace/browser_runs/browser_run_manual_1/snapshot.json",
    "screenshot": "workspace/browser_runs/browser_run_manual_1/filled.png",
    "trace": "workspace/browser_runs/browser_run_manual_1/trace.zip"
  },
  "actions": [
    {"action_id": "act_1", "type": "open", "status": "completed"},
    {"action_id": "act_2", "type": "wait_for_selector", "status": "completed"}
  ],
  "warnings": [],
  "error": null
}
```

### 5.4 error response

stdout は JSON のみとする。stderr は実行ログだけに使う。

```json
{
  "schema_version": "1.0",
  "run_id": "browser_run_manual_1",
  "status": "failed",
  "error": {
    "code": "PERMISSION_DENIED",
    "message": "external effect action is blocked without human approval",
    "details": {
      "action_index": 3,
      "action_type": "click",
      "matched_rule": "submit_keyword"
    }
  },
  "warnings": []
}
```

error code は `TOOL_CONTRACT.md` に合わせる。

- `VALIDATION_FAILED`
- `PERMISSION_DENIED`
- `TIMEOUT`
- `NOT_FOUND`
- `INTERNAL_ERROR`

## 6. Action contract

### 6.1 共通 field

```json
{
  "type": "click",
  "selector": "#submit",
  "name": "optional-stable-name",
  "timeout_ms": 5000
}
```

### 6.2 action types

| type | 必須 field | Phase 1 | 備考 |
| --- | --- | --- | --- |
| `open` | none | yes | `start_url` を開く |
| `wait_for_selector` | `selector` | yes | visible wait |
| `wait_for_url` | `url_pattern` | no | Phase 2 |
| `click` | `selector` | yes | submit keyword は block |
| `fill` | `selector`, `value` | yes | password field value は artifact 保存しない |
| `press` | `key` | yes | form 内 Enter は block |
| `select` | `selector`, `value` | no | Phase 2 |
| `screenshot` | `name` | yes | name は path-safe |
| `snapshot` | none | yes | DOM / accessibility snapshot |
| `extract_text` | `selector` | yes | text のみ。HTML raw は保存しない |
| `trace_start` | none | no | Phase 2 |
| `trace_stop` | none | no | Phase 2 |
| `close` | none | yes | browser close |

Phase 1 では `save_trace=true` の場合、run 全体で Playwright trace を開始 / 終了する。action としての `trace_start` / `trace_stop` は未実装でよい。

## 7. Risk classification

### 7.1 classification order

1. unsupported action は `blocked`
2. action 数が `max_actions` 超過なら `blocked`
3. start URL / current URL が allowlist 外なら `blocked`
4. selector / text / key から submit 相当を検出したら `external_effect`
5. `fill` がある場合は `draft_input`
6. `click` がある場合は `navigation`
7. それ以外は `read_only`

### 7.2 submit keyword

大文字小文字を無視して次を検出する。

```text
submit
send
post
publish
buy
purchase
checkout
delete
remove
reserve
confirm
apply
upload
支払
購入
投稿
送信
削除
予約
確定
申し込
申込
```

Phase 1 では `external_effect` は実行前に `PERMISSION_DENIED` で止める。human approval による実行継続は Phase 2 以降。

### 7.3 POST detection

Phase 1 では、操作中に `POST` / `PUT` / `PATCH` / `DELETE` request を検知した場合、run を即時失敗にする。ただし fixture や local Viewer の read-only API で必要になる場合があるため、将来は `allowed_methods` を request に追加できるよう余地を残す。

Phase 1 の標準は `GET` / static file 操作を想定する。

## 8. Origin allowlist

### 8.1 normalize

URL origin は次の形へ正規化する。

```text
scheme://host[:port]
```

`file://` は Phase 1 fixture 用として明示許可したときだけ許可する。

### 8.2 allowlist check

- `start_url` の origin が `allowed_origins` に含まれること
- navigation 後の `page.url()` origin が `allowed_origins` に含まれること
- redirect で allowlist 外へ出た場合は停止すること

`allowed_origins` が空の場合は、`start_url` origin のみを自動許可してよい。ただし `profile_id` を使う場合は profile 側 allowlist を優先する。

## 9. Secret masking

### 9.1 mask targets

artifact 保存前に次を mask する。

- request header `Cookie`
- request header `Authorization`
- response header `Set-Cookie`
- field name に `password`, `token`, `secret`, `apikey`, `api_key`, `session`, `csrf` を含む値
- HTML input type `password` に入力した value

mask 文字列は固定で `[MASKED]` とする。

### 9.2 保存禁止

次は保存しない。

- raw request body
- raw response body
- Cookie value
- Authorization value
- password field value

`extract_text` は user-visible text のみ保存する。HTML raw は保存しない。

## 10. Artifact contract

### 10.1 directory

Phase 1:

```text
workspace/browser_runs/<run_id>/
  run.json
  actions.jsonl
  console.jsonl
  network.jsonl
  snapshot.json
  extracted_text.json
  final.png
  trace.zip
```

`artifact_dir` が指定されていない場合は `workspace/browser_runs/<run_id>` を使う。

### 10.2 run.json

`BrowserRun` response の完全版を保存する。ただし `error.details` に secret を入れない。

### 10.3 actions.jsonl

1 action 1 line。

```json
{"action_id":"act_1","type":"open","status":"completed","started_at":"...","completed_at":"..."}
```

### 10.4 network.jsonl

summary のみ。

```json
{
  "ts": "2026-06-08T00:00:01Z",
  "request_id": "req_1",
  "method": "GET",
  "url": "https://example.com/path",
  "origin": "https://example.com",
  "path": "/path",
  "status": 200,
  "resource_type": "document",
  "duration_ms": 123,
  "request_headers": {"cookie": "[MASKED]"},
  "response_headers": {"set-cookie": "[MASKED]"}
}
```

### 10.5 console.jsonl

```json
{"ts":"2026-06-08T00:00:01Z","type":"log","text":"ready"}
```

Console text に secret らしき値がある場合も mask する。

## 11. Phase 1 sidecar implementation

### 11.1 Node.js module style

`run_browser_actor.mjs` は ESM とする。

使用 dependency:

- `playwright`
- Node.js standard library

新しい npm package は追加しない。

### 11.2 main flow

```text
parse argv
  ↓
if doctor: run doctor
  ↓
read stdin JSON
  ↓
validate request
  ↓
classify risk
  ↓
reject external_effect / blocked
  ↓
create artifact dir
  ↓
launch chromium
  ↓
new context with storageState if provided
  ↓
attach console/network collectors
  ↓
start trace if requested
  ↓
execute actions sequentially
  ↓
write artifacts
  ↓
close browser
  ↓
print response JSON
```

### 11.3 validation rules

- `start_url` required
- `actions` required and non-empty
- `actions.length <= max_actions`
- `artifact_dir` must stay under workspace or `tmp`
- `run_id` must match `^[a-zA-Z0-9_.-]+$`
- screenshot `name` must match `^[a-zA-Z0-9_.-]+$`
- unsupported action is validation error
- selector length max 1000
- value length max 10000

### 11.4 path policy

`artifact_dir` is allowed only under:

- `workspace/browser_runs`
- `tmp/browser_runs`
- `output/playwright`

Phase 1 fixture tests may use temp dir.

## 12. Phase 2 Go implementation

### 12.1 Config

`internal/adapter/config/config_types.go`

```go
type BrowserActorConfig struct {
    Enabled         bool     `yaml:"enabled"`
    RunnerPath      string   `yaml:"runner_path"`
    NodeBinary      string   `yaml:"node_binary"`
    Browser         string   `yaml:"browser"`
    HeadlessDefault bool     `yaml:"headless_default"`
    ProfileRoot     string   `yaml:"profile_root"`
    ArtifactRoot    string   `yaml:"artifact_root"`
    TimeoutMS       int      `yaml:"timeout_ms"`
    MaxActions      int      `yaml:"max_actions"`
    NetworkScope    string   `yaml:"network_scope"`
    AllowedOrigins  []string `yaml:"allowed_origins"`
    SaveTrace       bool     `yaml:"save_trace"`
    SaveScreenshot  bool     `yaml:"save_screenshot"`
    MaskSecrets     bool     `yaml:"mask_secrets"`
}
```

defaults:

```yaml
browser_actor:
  enabled: false
  runner_path: "/home/nyukimi/RenCrow/RenCrow_Tools/tools/browser_actor/run_browser_actor.mjs"
  node_binary: "node"
  browser: "chromium"
  headless_default: true
  profile_root: "workspace/browser_profiles"
  artifact_root: "workspace/browser_runs"
  timeout_ms: 30000
  max_actions: 30
  network_scope: "allowlist"
  allowed_origins:
    - "http://127.0.0.1:18790"
    - "http://localhost:18790"
  save_trace: true
  save_screenshot: true
  mask_secrets: true
```

validation:

- enabled のとき `runner_path`, `node_binary`, `artifact_root` 必須
- `timeout_ms > 0`
- `max_actions` は 1 から 100
- `network_scope` は `allowlist` または `blocked`
- `profile_root` / `artifact_root` は path traversal 禁止

### 12.2 modules/browseractor

Go 側の DTO / validation / policy を置く。

主な型:

```go
type RunRequest struct {
    SchemaVersion string
    RunID string
    Goal string
    StartURL string
    ProfileID string
    StorageStatePath string
    Headless bool
    Viewport Viewport
    AllowedOrigins []string
    ArtifactDir string
    TimeoutMS int
    MaxActions int
    SaveTrace bool
    SaveScreenshot bool
    MaskSecrets bool
    Actions []Action
}

type Action struct {
    Type string
    Selector string
    Value string
    Key string
    Name string
    TimeoutMS int
}

type RunResponse struct {
    SchemaVersion string
    RunID string
    Status string
    RiskLevel string
    StartURL string
    FinalURL string
    Title string
    ArtifactDir string
    Artifacts map[string]string
    Actions []ActionResult
    Warnings []string
    Error *Error
}
```

### 12.3 internal/infrastructure/browseractor

`Runner` は sidecar command を呼ぶ。

```go
type Runner struct {
    cfg Config
    commandRunner CommandRunner
}

func (r *Runner) Run(ctx context.Context, req browseractor.RunRequest) (browseractor.RunResponse, error)
func (r *Runner) Doctor(ctx context.Context) (browseractor.DoctorResponse, error)
```

`CommandRunner` は test injection 可能にする。

```go
type CommandRunner func(ctx context.Context, command string, args []string, stdin []byte) (stdout []byte, stderr []byte, exitCode int, err error)
```

### 12.4 cmd/picoclaw CLI

`cmd/picoclaw/main.go` に command を追加する。

```text
picoclaw browser-actor run --json < request.json
picoclaw browser-actor doctor --json
```

CLI 実処理は test 可能にする。

```go
func runBrowserActorCommand(args []string, deps browserActorCLIDeps, in io.Reader, out io.Writer, errOut io.Writer) int
```

### 12.5 ToolRunner V2

`ToolRunnerConfig` に追加する。

```go
BrowserActorRunner BrowserActorRunner
```

interface:

```go
type BrowserActorRunner interface {
    Run(ctx context.Context, req browseractor.RunRequest) (browseractor.RunResponse, error)
}
```

tool:

```text
browser.run
```

metadata:

- `ToolID`: `browser.run`
- `Version`: `0.1.0`
- `Category`: `query`
- side effect は ToolMetadata だけでは表現しきれないため、PolicyEngine 側で network/process/local_write として扱う

Tool response:

- success: `tool.NewSuccess(resp)`
- validation error: `tool.ErrValidationFailed`
- blocked external effect: `tool.ErrPermissionDenied`
- sidecar timeout: `tool.ErrTimeout`
- sidecar non-zero unknown: `tool.ErrInternalError`

## 13. PolicyRunner integration

`internal/infrastructure/security/policy_engine.go` の network tool 判定へ `browser.run` を追加する。

`browser.run` は network + process + local_write を伴う。Phase 2 では最低限、network allowlist policy を通す。より厳密には `execution.Action.Arguments` から `start_url` host を抽出できるよう `SandboxGuard.ExtractNetworkHost` の対象を増やす。

blocked の例:

- `network_scope=blocked`
- allowlist 外 host
- action risk `external_effect`
- artifact path outside workspace

## 14. BrowserTrace integration

Phase 4 で実装する。

`browser_actor` sidecar は request / response summary から、既存 `browsertrace.Discoverer` が読める JSONL を生成する。

```text
workspace/browser_runs/<run_id>/requests.jsonl
workspace/browser_runs/<run_id>/responses.jsonl
```

Go 側は次を呼ぶ。

```go
browsertraceapp.Discoverer.Discover(browsertraceapp.DiscoverRequest{
    TraceRunID: runID,
    WorkstreamID: workstreamID,
    SiteID: siteID,
    Goal: goal,
    TracePath: artifactDir,
    RequestsPath: filepath.Join(artifactDir, "requests.jsonl"),
    ResponsesPath: filepath.Join(artifactDir, "responses.jsonl"),
})
```

保存先は既存 browsertrace store を使う。Source Registry / L1 staging へは candidate として保存し、promote しない。

## 15. Viewer integration

Phase 4 で実装する。

Viewer Ops に次を追加する。

- readiness card: enabled / runner / node / playwright / chromium / artifact root
- recent runs: run_id, status, risk, final_url, created_at
- profile summary: profile_id, status, origin count, updated_at
- blocked summary: blocked count / approval required count
- latest screenshot thumbnail

初期表示は要約に限定し、actions.jsonl / network.jsonl / console.jsonl は `details` に分離する。

Viewer に secret value を表示しない test を追加する。

## 16. Test plan

### 16.1 Phase 1 Node tests

command:

```bash
node /home/nyukimi/RenCrow/RenCrow_Tools/tools/browser_actor/test_browser_actor.mjs
```

test cases:

- doctor succeeds when Playwright Chromium can launch
- fixture page opens headless
- fill action changes visible value
- screenshot file is written
- snapshot file is written
- extract_text returns visible text
- click on submit-like selector is blocked before execution
- password value is not saved in actions / console / network / response
- allowlist outside origin is blocked
- timeout returns JSON error

### 16.2 Phase 2 Go tests

commands:

```bash
GOCACHE=/tmp/picoclaw-gocache go test ./modules/browseractor
GOCACHE=/tmp/picoclaw-gocache go test ./internal/infrastructure/browseractor
GOCACHE=/tmp/picoclaw-gocache go test ./internal/infrastructure/tools
GOCACHE=/tmp/picoclaw-gocache go test ./cmd/picoclaw
```

test cases:

- config defaults
- config validation rejects bad paths
- CLI run passes JSON to sidecar
- CLI doctor returns JSON
- ToolRunner registers `browser.run`
- ToolRunner maps sidecar validation error to `VALIDATION_FAILED`
- PolicyRunner denies network blocked mode
- PolicyRunner denies allowlist mismatch

### 16.3 Phase 4 E2E

local Viewer:

```bash
picoclaw browser-actor run --json < tmp/browser_actor_viewer_ops.json
```

requirements:

- `/viewer?tab=ops` opens headless
- screenshot exists
- final URL / title recorded
- no Cookie / Authorization values in artifacts
- BrowserTrace discovery can consume generated request / response JSONL

## 17. Implementation order

1. Create `RenCrow_Tools/tools/browser_actor` standalone sidecar.
2. Add Node fixture tests.
3. Add config type/default/validation.
4. Add Go infrastructure runner.
5. Add `picoclaw browser-actor` CLI.
6. Add ToolRunner V2 `browser.run`.
7. Add PolicyEngine support.
8. Add profile storage support.
9. Add BrowserTrace export / discover integration.
10. Add Viewer Ops summary.

Do not start with Viewer or MCP. The first acceptance point is a headless sidecar that can operate a local fixture and produce masked artifacts.

## 18. Acceptance checklist

Phase 1:

- [ ] sidecar accepts JSON stdin and writes JSON stdout
- [ ] stdout contains no logs
- [ ] stderr contains logs only
- [ ] headless Chromium fixture test passes
- [ ] screenshot / snapshot / action log are written
- [ ] external effect is blocked
- [ ] secret masking test passes

Phase 2:

- [ ] config defaults and validation pass
- [ ] `picoclaw browser-actor doctor --json` works
- [ ] `picoclaw browser-actor run --json` calls sidecar
- [ ] `browser.run` appears in ToolRunner metadata
- [ ] `browser.run` returns structured `ToolResponse`
- [ ] PolicyRunner can deny disallowed network

Phase 3:

- [ ] `profile_id` loads storageState without exposing Cookie values
- [ ] profile metadata is separate from storage file
- [ ] profile allowlist blocks unexpected origin

Phase 4:

- [ ] BrowserTrace JSONL is generated
- [ ] BrowserTrace discover stores candidate artifacts
- [ ] Viewer Ops shows readiness and recent runs
- [ ] Viewer does not show secret values

## 19. Open questions

- Phase 2 で `browser.snapshot` を独立 tool として公開するか、`browser.run` に閉じるか。
- `POST` をすべて止めると local app E2E が弱くなるため、Phase 2 で `allowed_methods` を導入するか。
- profile storage root を `workspace/browser_profiles` のままにするか、runtime state として `~/.picoclaw/browser_profiles` に置くか。
- Viewer の human approval UI を Browser Actor 固有にするか、既存 Sandbox / Promotion Gate に寄せるか。

Phase 1 の実装では、これらの未決事項を避ける。`browser.run` の最小 read-only / draft_input 実行を完成させてから決める。
