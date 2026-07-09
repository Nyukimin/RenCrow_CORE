# Browser 操作ツール仕様

## 1. 目的

RenCrow が、ブラウザを人間ユーザーのように操作できる能力を持つ。

この能力は、単なる Web fetch や scraping ではない。ページを開き、画面状態を観測し、クリック、入力、遷移、待機、スクリーンショット取得、network trace 取得を行い、その結果を Workstream / BrowserTrace / Viewer で追跡可能にする。

初期名称を `browser_actor` とする。

## 2. 参考仕様

- `AGENTS.md`
- `CLAUDE.md`
- `TOOL_CONTRACT.md`
- `docs/01_正本仕様/実装仕様.md`
- `docs/10_新仕様/20_Tool_Harness_Contract_Mediation仕様.md`
- `docs/10_新仕様/21_AI_Native_Engineering_Workflow仕様.md`
- `docs/10_新仕様/27_Browser_Trace_to_API_Discovery仕様.md`
- `docs/10_新仕様/28_SuperAgent_Harness_Reference_DeerFlow仕様.md`
- `docs/10_新仕様/29_Sandbox_Promotion_Gate仕様.md`
- `docs/10_新仕様/46_Web情報収集ツール仕様.md`
- `docs/10_新仕様/48_Web情報収集ツール実装仕様.md`
- `RenCrow_Tools/tools/webwright_fetch/README.md`

## 3. Web Gather との責務分離

`web_gather` は、URL 取得、本文抽出、Source Registry / L1 staging への pending 保存を担当する。

`browser_actor` は、ユーザー操作に近い browser session execution を担当する。

| 項目 | web_gather | browser_actor |
| --- | --- | --- |
| 主目的 | Web 情報収集と staging | ユーザー操作の代行と検証 |
| 第一経路 | Go HTTP fetch | Playwright headless browser |
| 入力 | URL / search query | start URL / action sequence / goal |
| 出力 | extracted text / staging item | action log / screenshot / DOM snapshot / trace |
| Cookie 利用 | 原則しない | profile_id 経由で許可 |
| promote | 自動しない | 自動しない |
| 外部副作用 | 取得のみ | 投稿、送信、購入などは human approval 必須 |

`web_gather` の `fetch_provider=webwright` は、JS 必須ページやスクリーンショット証跡つき fetch の fallback である。`browser_actor` は、継続的な操作 session と profile 管理を扱うため、同じ実装へ統合しない。

## 4. 基本方針

### 4.1 Headless first

通常実行は headless browser とする。

Playwright / Chromium は、画面を表示しなくても DOM、layout、script、network、screenshot、trace を処理できる。したがってサーバー上で GUI が表示できない環境でも、headless 実行を標準とする。

headed 実行は、次の場合だけ使う。

- 初回ログインなど、人間が実画面で操作する必要がある場合
- CAPTCHA、OS dialog、権限 UI など自動処理が不適切な場合
- デバッグ目的で画面確認が必要な場合

headed 実行には X11 / Wayland / Xvfb などの表示環境が必要になる。RenCrow 本体 runtime の必須条件にはしない。

### 4.2 Profile first

Cookie や localStorage を直接 LLM に扱わせない。

通常の browser tool call は `profile_id` を受け取り、その profile に紐づく browser storage state を読み書きする。Cookie 値、Authorization header、session token などの秘密値は、tool response、chat message、Viewer 通常表示、artifact 本文へ出さない。

### 4.3 Evidence first

ブラウザ操作の完了判定は、LLM の自己申告ではなく証跡で行う。

各 run は最低限、次を保存する。

- action log
- final URL
- final title
- screenshot
- DOM snapshot または accessibility snapshot
- console log summary
- network summary
- error details

必要に応じて Playwright trace zip、HAR、request / response JSONL を保存する。

### 4.4 Approval first for external effects

外部世界へ影響する操作は、Cookie がある場合でも human approval を必須とする。

外部副作用の例:

- 投稿
- 送信
- コメント
- 購入
- 予約
- 申込
- 削除
- 設定変更
- file upload
- payment / subscription / cancellation
- 他ユーザーへ通知される操作

この境界は prompt だけに閉じず、PolicyRunner / ToolHarness / BrowserActor 側の判定で止める。

## 5. 対象

対象:

- 公開 Web ページの操作確認
- Viewer / local app の E2E 操作
- ログイン済み profile を使った read-only 画面確認
- フォーム入力の draft 作成
- スクリーンショット証跡の取得
- network trace から API candidate を抽出するための操作
- Workstream の調査、検証、UI 確認

対象外:

- CAPTCHA 回避
- paywall / access control 回避
- robots / terms / rate limit を回避する常用取得
- 人間承認なしの投稿、購入、送信、削除、予約
- Cookie / token / password の LLM 露出
- シークレットを artifact raw text へ保存する処理
- 不特定多数サイトへの大量自動巡回

## 6. 全体構成

```text
User / Chat / Worker / Workstream
  ↓
ToolLoop / ToolRunner V2
  ↓
browser.* tools
  ↓
browser_actor Go adapter
  ↓
RenCrow_Tools/tools/browser_actor CLI
  ↓
Playwright Chromium headless
  ↓
Artifacts / BrowserTrace / Workstream / Viewer
```

初期実装は、RenCrow 本体に Playwright を直接組み込まない。`RenCrow_Tools/tools/browser_actor` を sidecar CLI とし、Go adapter は JSON 入出力で呼び出す。

MCP は Phase 3 以降の差し替え経路とする。汎用 MCP client が安定するまでは、Playwright CLI / Node sidecar を第一経路にする。

## 7. データモデル

### 7.1 BrowserProfile

```json
{
  "profile_id": "github_main",
  "browser": "chromium",
  "scope": "site",
  "origin_allowlist": ["https://github.com"],
  "storage_state_path": "workspace/browser_profiles/github_main/chromium/storage_state.json",
  "created_at": "2026-06-07T00:00:00Z",
  "updated_at": "2026-06-07T00:00:00Z",
  "status": "active",
  "secret_material": "masked"
}
```

必須 field:

- `profile_id`
- `browser`
- `storage_state_path`
- `status`
- `created_at`
- `updated_at`

`storage_state_path` は workspace 内の専用 directory に限定する。

推奨 path:

```text
workspace/browser_profiles/<profile_id>/<browser>/storage_state.json
workspace/browser_profiles/<profile_id>/profile.json
```

`profile_id` はユーザー、サイト、用途を区別する。

例:

- `default_readonly`
- `github_main`
- `google_readonly`
- `shopping_no_purchase`
- `viewer_local`

### 7.2 BrowserRun

```json
{
  "run_id": "browser_run_20260607_000001",
  "workstream_id": "ws_1",
  "profile_id": "github_main",
  "start_url": "https://github.com/",
  "final_url": "https://github.com/notifications",
  "goal": "Open notifications and capture unread count",
  "status": "completed",
  "headless": true,
  "started_at": "2026-06-07T00:00:00Z",
  "completed_at": "2026-06-07T00:00:10Z",
  "artifact_dir": "workspace/browser_runs/browser_run_20260607_000001",
  "risk_level": "read_only"
}
```

### 7.3 BrowserAction

```json
{
  "action_id": "act_1",
  "run_id": "browser_run_20260607_000001",
  "type": "click",
  "selector": "text=Notifications",
  "status": "completed",
  "started_at": "2026-06-07T00:00:03Z",
  "completed_at": "2026-06-07T00:00:04Z",
  "screenshot_path": "workspace/browser_runs/browser_run_20260607_000001/act_1.png"
}
```

action type:

- `open`
- `snapshot`
- `click`
- `fill`
- `press`
- `select`
- `wait_for_selector`
- `wait_for_url`
- `screenshot`
- `extract_text`
- `trace_start`
- `trace_stop`
- `close`

Phase 1 では file upload を実装しない。Phase 2 以降でも file upload は human approval 必須とする。

## 8. Profile / Cookie 仕様

### 8.1 保存対象

Playwright の `storageState` を保存対象とする。

含まれるもの:

- cookies
- localStorage
- origin ごとの storage state

sessionStorage は Playwright の標準 `storageState` だけでは完全保存対象ではないため、必要な場合は明示的に追加 export する。ただし Phase 1 では対象外とする。

### 8.2 初回ログイン

初回ログインは原則 human-in-the-loop とする。

手順:

1. `browser.profile.login` を `headed=true` で開始する
2. 人間がログインする
3. RenCrow は Cookie 値を表示せず `storage_state.json` だけ保存する
4. 以後の headless run は `profile_id` で保存済み state を使う

### 8.3 Cookie import

Cookie import は管理用操作であり、通常の Worker tool call からは使わない。

許可条件:

- 明示コマンドであること
- import 元が workspace 内であること
- JSON schema validation を通ること
- secret 値を stdout / log / Viewer に出さないこと
- import 後の profile は `status=needs_validation` になること

### 8.4 Cookie export

Cookie export は原則禁止とする。

バックアップ目的で必要な場合も、export 先は workspace 内の protected profile directory に限定し、chat / Viewer 通常表示へ値を出さない。

### 8.5 Profile 使用時の制約

`profile_id` を使う run は、profile の `origin_allowlist` を超えて移動してはいけない。redirect や link click で allowlist 外へ出た場合は run を停止し、`blocked_origin` として記録する。

## 9. CLI 仕様

### 9.1 `browser-actor run`

```bash
rencrow browser-actor run --json < request.json
```

input:

```json
{
  "goal": "Open the local Viewer and capture the Ops tab",
  "start_url": "http://127.0.0.1:18790/viewer?tab=ops",
  "profile_id": "viewer_local",
  "headless": true,
  "actions": [
    {"type": "open"},
    {"type": "wait_for_selector", "selector": "body"},
    {"type": "screenshot", "name": "ops"},
    {"type": "snapshot"}
  ],
  "artifact_dir": "workspace/browser_runs/manual_ops",
  "timeout_ms": 30000
}
```

output:

```json
{
  "run_id": "browser_run_20260607_000001",
  "status": "completed",
  "final_url": "http://127.0.0.1:18790/viewer?tab=ops",
  "title": "RenCrow Viewer",
  "artifact_dir": "workspace/browser_runs/manual_ops",
  "artifacts": {
    "screenshot": "workspace/browser_runs/manual_ops/ops.png",
    "snapshot": "workspace/browser_runs/manual_ops/snapshot.json",
    "action_log": "workspace/browser_runs/manual_ops/actions.jsonl",
    "network": "workspace/browser_runs/manual_ops/network.jsonl",
    "console": "workspace/browser_runs/manual_ops/console.jsonl"
  },
  "warnings": []
}
```

### 9.2 `browser-actor profile login`

```bash
rencrow browser-actor profile login \
  --profile-id github_main \
  --start-url https://github.com/login \
  --headed
```

この command は interactive human-in-the-loop を前提とする。実行後、Cookie 値を表示せず profile metadata だけ返す。

### 9.3 `browser-actor doctor`

```bash
rencrow browser-actor doctor --json
```

確認項目:

- Node.js / npm / npx
- Playwright package
- Chromium browser availability
- browser profile root writable
- browser run artifact root writable
- headless launch check
- screenshot write check

## 10. ToolRunner V2 contract

### 10.1 `browser.run`

複数 action を 1 run として実行する。

input schema:

```json
{
  "type": "object",
  "properties": {
    "goal": {"type": "string"},
    "start_url": {"type": "string"},
    "profile_id": {"type": "string"},
    "headless": {"type": "boolean"},
    "actions": {
      "type": "array",
      "items": {"type": "object"}
    },
    "timeout_ms": {"type": "integer"},
    "artifact_dir": {"type": "string"}
  },
  "required": ["start_url", "actions"]
}
```

side effect:

- process
- network
- local_write

### 10.2 `browser.snapshot`

現在または指定 URL の DOM / accessibility snapshot を取得する。Phase 1 では `browser.run` の action として実装し、独立 tool は Phase 2 とする。

### 10.3 `browser.profile.status`

profile metadata だけを返す。Cookie 値は返さない。

### 10.4 `browser.profile.login`

headed human login を開始し、storage state を保存する。Phase 1 では CLI のみとし、Worker tool としては公開しない。

## 11. Policy / Guardrail

### 11.1 Risk classification

action sequence は実行前に risk を分類する。

| risk | 内容 | 実行条件 |
| --- | --- | --- |
| `read_only` | open / snapshot / screenshot / text extract | 通常許可 |
| `draft_input` | fill / type で送信前の入力だけ行う | 許可。ただし submit しない |
| `navigation` | link click / route change | allowlist 内なら許可 |
| `external_effect` | submit / post / purchase / delete / upload | human approval 必須 |
| `blocked` | CAPTCHA 回避、paywall 回避、secret leak 等 | 拒否 |

### 11.2 Submit detection

次の action は原則 external effect として扱う。

- `click` selector / text が `submit`, `send`, `post`, `buy`, `purchase`, `delete`, `reserve`, `confirm`, `apply`, `upload` を含む
- `press Enter` が form 内で実行される
- `form.submit()` 相当の JS 実行
- method `POST` / `PUT` / `PATCH` / `DELETE` を伴う操作

誤検出を避けるため、Phase 1 では submit 相当 action を blocked とし、human approval path は Phase 2 で実装する。

### 11.3 Network policy

初期設定は allowlist を推奨する。

開発用途:

```yaml
browser_actor:
  network_scope: allowlist
  allowed_origins:
    - http://127.0.0.1:18790
    - http://localhost:18790
```

一般 Web 調査用途では、run ごとに `start_url` origin を allowlist に追加する。ただし redirect で別 origin へ出る場合は停止する。

### 11.4 Secret masking

artifact へ保存する前に次を mask する。

- Cookie header
- Authorization header
- Set-Cookie header
- API key らしき文字列
- password field value
- token / session / csrf らしき key-value

mask 前の raw network body は保存しない。

## 12. Artifact 仕様

推奨 path:

```text
workspace/browser_runs/<run_id>/
  run.json
  actions.jsonl
  console.jsonl
  network.jsonl
  snapshot.json
  final.png
  trace.zip
```

`network.jsonl` は summary に限定する。

保存する field:

- timestamp
- method
- URL origin / path
- status
- resource type
- duration
- request id
- masked request headers summary
- masked response headers summary

保存しない field:

- Cookie value
- Authorization value
- raw credential body
- full HTML body unless explicit extract action がある場合

## 13. BrowserTrace 連携

BrowserTrace API discovery は既存の `internal/domain/browsertrace` と `/viewer/browser-trace-api` を再利用する。

`browser_actor` は、必要に応じて次を出力する。

```text
workspace/browser_runs/<run_id>/requests.jsonl
workspace/browser_runs/<run_id>/responses.jsonl
```

その後、既存の `/viewer/browser-trace-api/discover` または application discoverer に渡し、API candidate、schema、coverage report、fetcher proposal を作る。

この連携は API 発見の補助であり、見つかった API を自動利用または promote してはいけない。terms review、PII review、human approval を通す。

## 14. Viewer 仕様

Viewer Ops には初期表示で 3 から 5 個程度の要約だけを出す。

表示例:

- Browser Actor readiness
- recent browser runs
- blocked / approval-required count
- latest screenshot thumbnail
- profile status summary

詳細は `details` または dedicated panel に分離する。

Viewer に Cookie 値、Authorization 値、password 値を表示してはいけない。

## 15. Config 仕様

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

`enabled=false` でも `doctor` と dry-run は実行できる。実 browser run は `enabled=true` を必須とする。

## 16. 実装配置

### 16.1 Tool Build Mode

最初に独立 sidecar として作る。

```text
RenCrow_Tools/tools/browser_actor/
  README.md
  run_browser_actor.mjs
  fixtures/
  test_browser_actor.mjs
```

### 16.2 Safe Build Mode

RenCrow 本体への接続は小差分で行う。

```text
internal/domain/browseractor/
internal/infrastructure/browseractor/
internal/infrastructure/tools/runner_browser_actor.go
cmd/rencrow/cli_browser_actor.go
internal/adapter/config/
internal/adapter/viewer/
```

既存 `webgather` とは usecase / config / tool metadata を分ける。

## 17. 実装フェーズ

### Phase 1: standalone CLI

- `RenCrow_Tools/tools/browser_actor/run_browser_actor.mjs`
- JSON input / JSON output
- headless Chromium 起動
- open / wait / click / fill / press / screenshot / snapshot
- artifact 保存
- fixture HTML E2E
- `doctor`

### Phase 2: Go CLI / ToolRunner

- `rencrow browser-actor run`
- `browser.run` ToolRunner V2
- config defaults / validation
- PolicyRunner 連携
- ToolHarness metadata
- unit test

### Phase 3: Profile / Cookie

- `browser-actor profile login`
- `BrowserProfile` metadata
- storage state save / load
- origin allowlist
- Cookie 値非表示 test
- profile status Viewer 表示

### Phase 4: BrowserTrace / Viewer

- request / response summary export
- BrowserTrace discover 連携
- Workstream artifact 登録
- Viewer Ops summary
- screenshot thumbnail

### Phase 5: MCP bridge

- 汎用 MCP subprocess client の整理
- Playwright / Chrome DevTools MCP を provider として差し替え可能にする
- tool metadata は `browser.*` に固定し、MCP 固有名を Agent に直接露出しない

## 18. テスト方針

### 18.1 Unit

- input schema validation
- path traversal rejection
- origin allowlist
- risk classification
- secret masking
- profile metadata validation

### 18.2 CLI

- fixture HTML を headless で操作できる
- screenshot が保存される
- snapshot が保存される
- timeout で JSON error を返す
- disabled config では run が拒否される
- doctor が Playwright 状態を返す

### 18.3 ToolRunner

- `browser.run` metadata が登録される
- PolicyRunner が network / process / local_write として扱う
- unknown action が validation error になる
- external effect は human approval なしで blocked になる

### 18.4 Viewer / E2E

- `/viewer` local page を headless で開き screenshot を取得できる
- narrow / desktop viewport の artifact を保存できる
- BrowserTrace discovery へ request / response JSONL を渡せる
- Cookie 値が Viewer / logs / artifacts に出ない

## 19. 完了条件

Phase 1 完了条件:

- `RenCrow_Tools/tools/browser_actor` が単体で動く
- headless Chromium で fixture 操作が通る
- artifact が deterministic path に保存される
- JSON input / output が TOOL_CONTRACT に従う
- secret masking test がある

Phase 2 完了条件:

- `rencrow browser-actor run` が動く
- `browser.run` ToolRunner V2 から呼べる
- external effect が approval なしで止まる
- config / doctor / tests がある

Phase 3 完了条件:

- `profile_id` で storage state を保存 / 復元できる
- Cookie 値が tool response / Viewer / log に出ない
- allowlist 外 origin で停止する

## 20. 未決事項

- Firefox / WebKit 対応を Phase 1 に含めるか
- persistent browser server を持つか、run ごとに browser を起動するか
- sessionStorage の保存をどこまで扱うか
- human approval UI を Viewer のどの panel に置くか
- BrowserTrace の raw response body 保存範囲
- mobile emulation / device profile の標準セット

未決事項は実装前にすべて決める必要はない。Phase 1 は headless Chromium + storageState なし + local fixture で開始する。
