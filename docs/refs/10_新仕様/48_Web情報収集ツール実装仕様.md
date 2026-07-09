# Web 情報収集ツール実装仕様

## 1. 目的

RenCrow が外部 SaaS / 有料検索 API に依存せず、公開 Web ページを取得し、本文抽出、証跡 metadata、Source Registry / L1 staging への `pending` 保存までを常用できる形で実装する。

本仕様の Phase 1 は direct URL fetch のみを対象とする。検索候補発見、JS fallback、Viewer 新規画面、scheduler、自動 promote は後段とする。

## 2. 参考仕様

- `AGENTS.md`
- `CLAUDE.md`
- `docs/01_正本仕様/実装仕様.md`
- `docs/10_新仕様/46_Web情報収集ツール仕様.md`
- `docs/10_新仕様/47_Web情報収集ツール実装仕様作成プロンプト.md`
- `docs/10_新仕様/09_Memory_SourceRegistry仕様.md`
- `RenCrow_Tools/tools/webwright_fetch/README.md`
- `docs/10_新仕様/27_Browser_Trace_to_API_Discovery仕様.md`
- `docs/10_新仕様/20_Tool_Harness_Contract_Mediation仕様.md`
- `docs/10_新仕様/10_検証仕様.md`

## 3. 現行実装調査

### 3.1 Source Registry / L1 staging

現行の L1 staging 型は `internal/infrastructure/persistence/conversation/l1_sqlite_types.go` の `L1StagingItem` である。主な field は次。

- `ID`
- `Kind`
- `Namespace`
- `EventID`
- `SourceID`
- `SourceURL`
- `FetchedAt`
- `PublishedAt`
- `RawText`
- `RawHash`
- `SummaryDraft`
- `Keywords`
- `LicenseNote`
- `ValidationStatus`
- `Meta`
- `CreatedAt`
- `UpdatedAt`

保存 API は `SaveStagingItem(ctx, L1StagingItem)` である。保存時に次が行われる。

- `ValidationStatus` 未指定時は `pending`
- `Kind` は `external_fetch` / `memory_candidate` / `search_result` のみ許可
- `Namespace`、`EventID`、`SourceID`、`RawText` が必須
- `RawHash` は `rawTextHash(item.RawText)` で再計算される
- `ID` 未指定時は `namespace:event_id:raw_hash_prefix` で生成
- `staging.item_saved` event log を追加
- archive へ同期

Source Registry 由来の staging API は `StageSourceRegistryFetch(ctx, sourceID, payload)` である。ただしこれは既存 `l1_source_registry` entry を要求し、disabled source は拒否する。direct URL fetch の Phase 1 で毎回 Source Registry entry を増やすと registry が候補 URL で膨らむため、Phase 1 の `web-gather url` は `SaveStagingItem` を直接使う。ただし `Kind=external_fetch`、`ValidationStatus=pending`、`Meta` 契約は Source Registry staging と揃える。

validate / promote 境界は既存 API を使う。

- `ValidateStagingItem(ctx, id, policy)`
- `PromoteValidatedStagingItemToNews(ctx, id, category)`
- `PromoteValidatedStagingItemToKnowledge(ctx, id, domain)`
- `PromoteValidatedStagingItemToMemory(ctx, id, targetNamespace, promotedBy)`

`Promote*` は `ValidationStatus=validated` でない item を拒否する。Phase 1 の Web Gather は validate / promote を自動実行しない。

Viewer の確認 API は `GET /viewer/source-registry?action=staging&status=pending&limit=N` である。validate は `POST /viewer/source-registry?action=validate`、promote は `POST /viewer/source-registry?action=promote` を使う。

### 3.2 Webwright Fetch

`RenCrow_Tools/tools/webwright_fetch` は RenCrow 本体 runtime から切り離された補助ツールである。

- 本体 runtime へ Webwright / Playwright を必須 dependency として組み込まない
- Webwright の結果は `external_fetch` の `pending` staging JSONL に変換する
- `webwright_to_staging.py` は Go の `L1StagingItem` struct field 名に合わせた JSONL を出力する
- raw text に secret / cookie / Authorization らしき文字列がある場合は変換を失敗させる
- metadata には `webwright=true`、`tool=webwright_fetch`、`review_required=true`、`auto_promote=false` を入れる

Phase 1 の Go HTTP fetch は Webwright と競合しない。Webwright は Phase 4 の明示 fallback として接続する。

### 3.3 CLI / Tool Registry

CLI は `cmd/picoclaw/main.go` の command switch に追加する。既存パターンは `source-registry`、`knowledge`、`evidence` である。

Source Registry CLI は次の構成である。

- `cmdSourceRegistry()` が config を読み、L1 SQLite store を初期化する
- `runSourceRegistryCommand(args, store, stdout, stderr)` が実処理を行う
- CLI test は `run...Command` に fake store / temp store を渡す
- JSON 出力は `writeJSONCLI` を使う
- L1 SQLite path は `config.LoadConfig(getConfigPath()).Conversation.L1SQLitePath` から読む

Tool Registry / tool harness は `internal/domain/tool` と `internal/infrastructure/tools` にある。既存 tool metadata は `ToolMetadata` で `ToolID`、`Version`、`Category`、`Parameters` を持つ。実行結果は `ToolResponse` で成功時 `Result`、失敗時 `ToolError` を返す。

Phase 1 では CLI を先に実装する。Worker tool `web_gather.fetch` は同じ application usecase を呼ぶ薄い adapter とし、CLI 実装と重複させない。

### 3.4 Security warning

外部入力の prompt injection warning は `internal/domain/security.DetectPromptInjectionWarnings(text)` を使う。

現行 Source Registry sweeper は `sourceRegistryMetaWithWarnings` で次を `Meta` に保存している。

- `security_warnings`
- `security_warning_source`

Phase 1 の Web Gather も抽出本文に同じ検出器を適用する。`security_warning_source` は `web_gather` とする。warning は拒否そのものではないが、auto promote 禁止 metadata として扱う。

## 4. 実装範囲

### 4.1 Phase 1 で実装すること

- `picoclaw web-gather url <url>`
- Go HTTP fetch
- URL validation
- request timeout
- max body bytes
- redirect 上限
- User-Agent 明示
- content-type 判定
- `text/html` の本文抽出
- `text/plain` / markdown の整形
- JSON の短い pretty / text 化
- raw hash の証跡保存
- Source Registry / L1 staging への `pending` 保存
- `security_warnings` metadata
- 失敗分類
- unit / fixture / CLI / Source Registry integration test

### 4.2 Phase 1 で実装しないこと

- SearXNG provider
- YaCy provider
- RSS / Atom / sitemap discovery の新規実装
- Colly crawler
- Webwright 自動 fallback
- Viewer 新規タブ
- scheduler / sweep
- automatic validate
- automatic promote
- Google / Bing / Brave / Tavily / SerpAPI 連携
- CAPTCHA / bot challenge / terms / robots 回避
- ログイン必須ページの常用取得

### 4.3 Phase 2 以降

- Phase 2: `web_gather.search`、SearXNG provider、local cache provider、search result cache
- Phase 3: Source Registry source kind / scheduled source 連携、review flow の E2E。`web_gather` source は pending staging で止め、自動 validate / promote は行わない。
- Phase 4: `fetch_provider=webwright` の明示 fallback、trace / screenshot artifact 連携
- Phase 5: YaCy / local index、domain / topic 別 index 育成

## 5. アーキテクチャ

### 5.1 package 構成

第一案は次とする。

```text
modules/webgather
  contract.go
  policy.go
  normalize.go
  errors.go
  staging.go

internal/application/webgather
  fetch_usecase.go
  search_usecase.go          # Phase 2
  staging_writer.go

internal/infrastructure/webgather
  http_fetcher.go
  html_extractor.go
  searxng_discovery.go       # Phase 2
  yacy_discovery.go          # Phase 5
  cache.go                   # Phase 2

cmd/picoclaw
  cli_web_gather.go
```

`modules/webgather` は domain に近い contract と policy のみを持つ。HTTP、HTML parser、SQLite store へ直接依存しない。

### 5.2 domain / modules contract

`modules/webgather` に次を定義する。

- `FetchRequest`
- `FetchResult`
- `ExtractedDocument`
- `FetchPolicy`
- `FetchProvider`
- `Extractor`
- `DiscoveryProvider`
- `StagingRecord`
- `ErrorCode`

`FetchProvider` は bytes と HTTP 証跡を返すだけにする。`Extractor` は bytes / content-type / final URL から本文と metadata を返す。`StagingWriter` は application 層で L1 staging に mapping する。

### 5.3 application usecase

`internal/application/webgather.FetchURL(ctx, req)` は次の順に処理する。

1. 入力 URL / namespace / source_id / policy を検証する
2. `FetchProvider` で取得する
3. fetch 失敗は staging 成功扱いにしない
4. `Extractor` で本文を抽出する
5. `DetectPromptInjectionWarnings` を抽出本文に適用する
6. `StagingWriter` で `L1StagingItem` を `pending` 保存する
7. `FetchResult` を返す

`FetchURL` は validate / promote を呼ばない。

### 5.4 infrastructure provider

`internal/infrastructure/webgather.HTTPFetcher` は `net/http` を使う。

HTML 抽出は Phase 1 では dependency 追加判断を分ける。

- 推奨: `go-readability` を直接 dependency として追加する
- dependency 追加を避ける場合: 既に indirect に存在する `goquery` で title / meta / visible text 抽出の最小 extractor を実装し、`extractor=html_basic` と明記する

仕様としては `go_readability` を第一候補にする。実装開始時に dependency 追加を明示確認し、追加しない場合は extractor 名と精度リスクを仕様差分として残す。

### 5.5 cmd runtime / CLI

`cmd/picoclaw/main.go` に `web-gather` command を追加する。`cmdWebGather()` は `source-registry` CLI と同じく config を読み、`Conversation.L1SQLitePath` から L1 store を初期化する。

CLI 実処理は test 可能にする。

```go
func runWebGatherCommand(args []string, deps webGatherCLIDeps, out io.Writer, errOut io.Writer) int
```

`deps` は fetch usecase、または fetcher / extractor / staging store を持つ小さい interface とする。

## 6. データ契約

### 6.1 input DTO

CLI:

```bash
picoclaw web-gather url <url> \
  --namespace kb:web \
  --source-id web:example:article \
  --json
```

`FetchRequest`:

```json
{
  "url": "https://example.com/article",
  "namespace": "kb:web",
  "source_id": "web:example:article",
  "fetch_provider": "http",
  "extractor": "go_readability",
  "store_staging": true,
  "refresh": false,
  "policy": {
    "request_timeout_ms": 15000,
    "max_body_bytes": 5242880,
    "max_redirects": 5
  }
}
```

`namespace` の既定は `kb:web`。`source_id` 未指定時は canonical URL から `web:<host>:<path_hash>` を生成する。

### 6.2 output DTO

成功:

```json
{
  "url": "https://example.com/article",
  "final_url": "https://example.com/article",
  "status": "ok",
  "http_status": 200,
  "content_type": "text/html",
  "title": "Example",
  "text_preview": "本文の先頭...",
  "raw_hash": "sha256:...",
  "raw_bytes": 12345,
  "extracted_chars": 2345,
  "staging_id": "kb:web:web:example:article:...",
  "validation_status": "pending",
  "security_warnings": [],
  "diagnostics": {
    "fetch_provider": "http",
    "extractor": "go_readability",
    "elapsed_ms": 123,
    "cache_hit": false
  }
}
```

失敗:

```json
{
  "url": "https://example.com/article",
  "status": "failed",
  "error_code": "fetch_timeout",
  "error_message": "request timed out",
  "diagnostics": {
    "fetch_provider": "http",
    "elapsed_ms": 15000
  }
}
```

失敗時は `staging_id` を返さない。fallback を成功扱いしない。

### 6.3 staging mapping

`L1StagingItem` mapping:

| L1 field | Web Gather mapping |
| --- | --- |
| `Kind` | `external_fetch` |
| `Namespace` | request namespace。既定 `kb:web` |
| `EventID` | `web_gather:<source_id>:<fetched_at_or_hash>` |
| `SourceID` | request source_id または canonical URL 由来 |
| `SourceURL` | final URL。redirect がなければ input URL |
| `FetchedAt` | fetch 完了 UTC |
| `PublishedAt` | extractor metadata から取れた場合のみ |
| `RawText` | 抽出本文。raw HTML 全文は保存しない |
| `RawHash` | store 側で再計算。meta には `sha256:<hex>` を保存 |
| `SummaryDraft` | title + excerpt または本文先頭の短縮 |
| `Keywords` | request keyword または host / content type |
| `LicenseNote` | `review source terms before promotion` 既定 |
| `ValidationStatus` | `pending` |
| `Meta` | 6.4 の schema |

### 6.4 meta schema

最低限次を保存する。

```json
{
  "tool": "rencrow-web-gather",
  "tool_version": "v0.1",
  "discovery_provider": "direct_url",
  "fetch_provider": "http",
  "extractor": "go_readability",
  "source_url": "https://example.com",
  "canonical_url": "https://example.com",
  "http_status": 200,
  "content_type": "text/html",
  "fetched_at": "RFC3339",
  "elapsed_ms": 123,
  "raw_hash": "sha256:...",
  "raw_bytes": 12345,
  "extracted_chars": 1234,
  "title": "Example",
  "byline": "",
  "site_name": "",
  "published_at": "",
  "security_warning_source": "web_gather",
  "security_warnings": [],
  "review_required": true,
  "auto_promote": false,
  "license_note": "review source terms before promotion"
}
```

保存禁止:

- request header の `Cookie`
- `Authorization`
- `Set-Cookie`
- API key
- secret token
- raw HTML 全文

### 6.5 error code

Phase 1 で定義する error code:

```text
invalid_url
unsupported_scheme
blocked_by_policy
robots_disallowed
rate_limited
fetch_timeout
fetch_failed
http_status_error
body_too_large
unsupported_content_type
extract_failed
empty_content
security_warning
staging_failed
cache_error
```

`security_warning` は原則として fetch 全体の失敗ではなく metadata warning である。ただし secret / credential が本文に含まれる疑いが強い場合は `blocked_by_policy` として staging しない。

## 7. Fetch policy

### 7.1 timeout

既定値:

- `request_timeout`: 15s
- CLI option: `--timeout-sec`
- tool input: `policy.request_timeout_ms`

timeout は `context.WithTimeout` と `http.Client.Timeout` のどちらか一方に寄せ、二重 timeout の原因を logs に残せるようにする。

### 7.2 rate limit

Phase 1 の CLI 単発 fetch では process 内の domain rate limit のみでよい。

既定値:

- `global_concurrency`: 2
- `per_domain_concurrency`: 1
- `per_domain_min_interval`: 3s

CLI 単発では `per_domain_min_interval` は同一 process 内でのみ効く。scheduler 実装時に永続 failure cache / rate state を追加する。

### 7.3 redirect

既定 `max_redirects=5`。redirect chain は meta に URL 件数と final URL のみを保存し、cookie / header は保存しない。

scheme downgrade や `file://`、`ftp://`、localhost / private address は既定拒否とする。ただしローカル検証用に `--allow-localhost` を明示した場合だけ `localhost` / `127.0.0.1` を許可する。

### 7.4 body size

既定 `max_body_bytes=5MB`。`io.LimitReader` 相当で読み、超過検出時は `body_too_large` として失敗させる。

抽出後の `RawText` は上限を別に持つ。既定 `max_extracted_chars=200000` とし、超過時は切り詰めた事実を meta に残す。

### 7.5 content type

Phase 1 で扱う content-type:

- `text/html`
- `application/xhtml+xml`
- `text/plain`
- `text/markdown`
- `application/json`
- `application/ld+json`

それ以外は `unsupported_content_type` とする。PDF / image / audio / video は Phase 1 対象外。

### 7.6 robots / blocked response

Phase 1 では完全な robots parser は必須にしない。ただし次は成功扱いしない。

- 403
- 429
- CAPTCHA / bot challenge らしき HTML
- terms / robots で自動取得禁止が明示された site policy が設定されている URL

HTTP 4xx / 5xx は `http_status_error`。429 は `rate_limited`。禁止 policy は `blocked_by_policy` または `robots_disallowed`。

## 8. Extract policy

### 8.1 HTML

`text/html` は `go_readability` を第一候補にする。

抽出する metadata:

- title
- excerpt
- byline
- site name
- canonical URL
- published time
- main image URL

raw HTML 全文は保存しない。本文抽出に失敗した場合、title / meta description だけで staging してはいけない。本文が空なら `empty_content` とする。

### 8.2 plain text

`text/plain` / markdown は UTF-8 text として正規化し、過剰な空行を畳む。文字コードが UTF-8 でない場合は `golang.org/x/net/html/charset` などで decode する。

### 8.3 JSON

JSON は構造をそのまま RawText に保存しない。短い pretty text または重要 key の text 化を行う。

Phase 1 では次の方針に限定する。

- 1MB 以下の JSON のみ対象
- top-level object / array を pretty 化
- `html`、`raw_html`、`token`、`secret`、`cookie`、`authorization` らしき key は保存対象から除外
- secret pattern が見つかったら `blocked_by_policy`

### 8.4 extraction failure

fetch 成功、extract 失敗の場合でも、RawText が空なら staging しない。`extract_failed` または `empty_content` として失敗 response と logs に残す。

HTTP status、content-type、raw bytes、elapsed は diagnostics と logs に残す。ただし raw body は保存しない。

## 9. Security policy

- 外部本文を system / developer / tool instruction と混ぜない
- `DetectPromptInjectionWarnings` を抽出本文へ適用する
- warning は `Meta.security_warnings` に保存する
- `security_warning_source=web_gather` とする
- warning 付き item も `pending` に留める
- warning 付き item を自動 promote しない
- secret / cookie / auth header / API key は raw_text / meta に保存しない
- private network への SSRF を既定拒否する
- robots / terms / CAPTCHA / rate limit を回避しない
- fallback 成功を禁止する

## 10. CLI 仕様

Phase 1 command:

```bash
picoclaw web-gather url <url> [options]
```

options:

```text
--namespace <namespace>        既定 kb:web
--source-id <source_id>        未指定時は URL 由来
--extractor <name>             go_readability|html_basic|plain_text|json_text
--timeout-sec <seconds>        既定 15
--max-body-bytes <bytes>       既定 5242880
--max-redirects <n>            既定 5
--license-note <text>          既定 review source terms before promotion
--json                         JSON 出力
--allow-localhost              fixture / local 検証用
--dry-run                      fetch まで行い staging 保存しない
```

Phase 2 search command:

```bash
picoclaw web-gather search <query> --provider searxng [options]
picoclaw web-gather search-and-fetch <query> --provider searxng [options]
picoclaw web-gather run-source <source_id> [--json]
picoclaw web-gather webwright-fetch --task <task> [--start-url <url>] [--task-id <id>] [--dry-run]
picoclaw web-gather import-webwright-jsonl <path> [--json]
picoclaw web-gather doctor [--json]
```

SearXNG は self-hosted endpoint を必須とする。CLI では `--searxng-url` を指定できる。未指定時は `config.yaml` の `web_gather.searxng_base_url` を使い、CLI option は config より優先する。

`run-source` は Source Registry に登録済みの `kind=web_gather` source だけを実行する。fetch / extract の結果は `pending` staging に保存し、validate / promote は自動実行しない。RSS / Atom / PyPI など他 kind の source は `web-gather run-source` では拒否し、`source-registry sweep` または Viewer の Source Registry 操作に分ける。

`import-webwright-jsonl` は `RenCrow_Tools/tools/webwright_fetch/webwright_to_staging.py` が出力した JSONL を L1 staging に取り込む。`Kind=external_fetch`、`ValidationStatus=pending`、`Meta.webwright=true` または `Meta.tool=webwright_fetch`、`Meta.review_required=true`、`Meta.auto_promote=false` を必須とする。raw text に credential-like 文字列がある item は保存しない。

`webwright-fetch` は `webwright_fetch.runner_path` の Python wrapper を明示実行するだけで、RenCrow 本体 runtime に Webwright / Playwright を必須 dependency として組み込まない。実行時は `webwright_fetch.enabled=true` を必須とする。`--dry-run` は設定確認用として enabled=false でも許可する。

`webwright_fetch.uvx_from` は外部取得を伴うため opt-in とし、既定では空にする。Webwright が別途インストール済みの Python を使う場合は `webwright_fetch.python` を指定する。`webwright-fetch` の実行前には `webwright_fetch.responses_endpoint` の TCP 到達性を preflight し、到達不能なら Webwright を起動せず失敗理由を CLI に出す。

`doctor` は L1 staging store、SearXNG 設定有無、Webwright enabled 状態、runner path、Python、`uvx_from` opt-in 状態、Responses endpoint 到達性を確認する。Webwright disabled は skipped とし、enabled かつ endpoint 到達不能など常用実行を阻害する状態は fail とする。

stdout:

- 成功時は staging id、URL、raw hash、warning 件数を表示
- `--json` 時は 6.2 の output DTO を JSON で出す

stderr:

- 入力エラー、fetch エラー、extract エラー、staging エラー
- 失敗時は error code を含める

exit code:

- `0`: fetch / extract / pending staging 成功
- `1`: 入力、fetch、extract、staging の失敗
- `2`: CLI usage error

## 11. Tool contract

### `web_gather.fetch`

input schema:

```json
{
  "type": "object",
  "properties": {
    "url": {"type": "string"},
    "fetch_provider": {"type": "string", "enum": ["http", "webwright"]},
    "extractor": {"type": "string", "enum": ["go_readability", "html_basic", "plain_text", "json_text"]},
    "namespace": {"type": "string"},
    "source_id": {"type": "string"},
    "store_staging": {"type": "boolean"},
    "refresh": {"type": "boolean"},
    "policy": {
      "type": "object",
      "properties": {
        "request_timeout_ms": {"type": "integer"},
        "max_body_bytes": {"type": "integer"},
        "max_redirects": {"type": "integer"}
      }
    }
  },
  "required": ["url"]
}
```

output schema は 6.2 と同じ。`store_staging=false` の場合は `staging_id` を空にし、`validation_status` も空にする。

失敗時 response:

```json
{
  "error": {
    "code": "fetch_timeout",
    "message": "request timed out",
    "details": {
      "url": "https://example.com",
      "elapsed_ms": 15000
    }
  }
}
```

Tool harness の `ToolError` に mapping する場合、timeout は `TIMEOUT`、rate limit は `RATE_LIMITED`、policy / validation は `VALIDATION_FAILED`、内部エラーは `INTERNAL_ERROR` とする。Web Gather 固有 error code は details に残す。

### `web_gather.search`

Phase 2 で実装する。Worker runtime は `config.yaml` の `web_gather.searxng_base_url` が空でない場合だけ SearXNG provider を登録する。未設定時の `provider=searxng` は設定エラーとして扱い、外部検索 API や fallback provider へ黙って切り替えない。

input:

```json
{
  "query": "string",
  "provider": "searxng|yacy|local_cache",
  "limit": 5,
  "language": "ja",
  "freshness": "any|day|week|month",
  "namespace": "kb:research"
}
```

output:

```json
{
  "query": "string",
  "provider": "string",
  "results": [
    {
      "url": "string",
      "title": "string",
      "snippet": "string",
      "rank": 1,
      "source_engine": "string"
    }
  ],
  "diagnostics": {
    "elapsed_ms": 0,
    "cache_hit": false,
    "error": ""
  }
}
```

### `web_gather.search_and_fetch`

Phase 2 以降。検索候補の品質と fetch / extract 失敗を混同しないため、初期実装では登録しない。

## 12. Logs / Evidence

最低限、構造化ログまたは L1 event に次を残す。

- `web_gather.fetch_started`
- `web_gather.fetch_completed`
- `web_gather.extract_completed`
- `web_gather.staging_saved`
- `web_gather.fetch_failed`

payload:

- `url`
- `final_url`
- `host`
- `fetch_provider`
- `extractor`
- `http_status`
- `content_type`
- `elapsed_ms`
- `raw_bytes`
- `extracted_chars`
- `raw_hash`
- `staging_id`
- `error_code`
- `security_warning_count`

本文、cookie、Authorization、Set-Cookie、secret は log に出さない。

## 13. Tests

### 13.1 Unit

対象:

- URL normalize
- source_id generation
- error code mapping
- content-type classification
- secret / header redaction
- prompt injection warning metadata
- staging mapping

想定:

```bash
GOCACHE=/tmp/picoclaw-gocache go test ./modules/webgather
```

### 13.2 Application

fake fetcher / fake extractor / fake staging store を使い、次を確認する。

- success path が `pending` staging を保存する
- fetch failure は staging しない
- extract failure は staging しない
- warning 付き本文が `Meta.security_warnings` を持つ
- validate / promote を呼ばない

```bash
GOCACHE=/tmp/picoclaw-gocache go test ./internal/application/webgather
```

### 13.3 Infrastructure fixture

`httptest.Server` で fixture を用意する。

- static HTML article
- malformed HTML
- text/plain
- markdown
- JSON endpoint
- 404
- 429
- oversized body
- redirect loop
- prompt injection text
- secret-like text

```bash
GOCACHE=/tmp/picoclaw-gocache go test ./internal/infrastructure/webgather
```

### 13.4 CLI

`runWebGatherCommand` に fake deps を渡して確認する。

- usage error
- `--json`
- `--namespace`
- `--source-id`
- failure exit code
- stderr に error code が出る

```bash
GOCACHE=/tmp/picoclaw-gocache go test ./cmd/picoclaw -run WebGather
```

### 13.5 Source Registry integration

temp L1 SQLite store を使い、実際に `SaveStagingItem` された item を `RecentStagingItems(pending)` で確認する。

確認項目:

- `Kind=external_fetch`
- `ValidationStatus=pending`
- `RawHash` が store 側再計算と一致
- `Meta.raw_hash` が `sha256:` prefix つき
- pending item は promote できない
- validate 後だけ promote できる

```bash
GOCACHE=/tmp/picoclaw-gocache go test ./internal/infrastructure/persistence/conversation ./internal/adapter/viewer
```

### 13.6 Live manual check

live runtime が必要な確認。

```bash
picoclaw web-gather url https://example.com --namespace kb:web --json
curl -fsS 'http://127.0.0.1:18790/viewer/source-registry?action=staging&status=pending&limit=5'
```

確認対象:

- CLI が成功する
- pending staging が Viewer Source Registry API から見える
- validated / promoted へ勝手に進まない
- warning metadata が Viewer API の `meta` に出る

live service を見られない場合は完了報告で明記する。

## 14. 実装手順

1. `modules/webgather` に contract、policy、error code、normalize、staging mapping の failing tests を追加する。
2. `internal/application/webgather` に fake provider ベースの usecase test を追加する。
3. `FetchURL` usecase を実装する。
4. `internal/infrastructure/webgather.HTTPFetcher` を fixture test つきで実装する。
5. HTML extractor を実装する。`go-readability` dependency を追加する場合はこの段階で最小追加する。
6. `StagingWriter` を `L1SQLiteStore.SaveStagingItem` に接続する。
7. `cmd/picoclaw/cli_web_gather.go` を追加する。
8. `main.go` と `cmdHelp()` に `web-gather` を追加する。
9. Worker tool `web_gather.fetch` は CLI が通った後に同じ usecase へ接続する。
10. `git diff --check` と対象 test を実行する。
11. 必要なら live manual check を行う。

## 15. 完了条件

- `picoclaw web-gather url <url>` が direct URL を fetch / extract / pending staging できる
- fetch / extract / staging の失敗が別々の error code で返る
- pending staging が validate 前に promote されない
- raw hash と source URL が staging と meta に残る
- prompt injection warning が `Meta.security_warnings` に残る
- secret / cookie / Authorization / Set-Cookie が raw_text / meta / log に保存されない
- 外部 API key なしで動く
- Webwright / Playwright が本体 runtime 必須 dependency にならない
- Phase 1 対象 test が通る
- live 確認を実施した場合、Viewer Source Registry API から pending item が確認できる

## 16. 停止条件

次に該当した場合は実装を止めて報告する。

- `SaveStagingItem` の契約変更が必要になる
- Source Registry / L1 schema migration が必要になる
- dependency 追加の影響が大きい
- `go-readability` の採用により既存 indirect dependency と衝突する
- robots / terms / CAPTCHA 回避が必要になる
- private network / localhost 取得を既定許可したくなる
- secret-like raw text を保存しないとテストを通せない
- validate / promote を Phase 1 で自動化したくなる
- Viewer 新規画面なしでは運用できないことが判明する

## 17. 将来拡張

- SearXNG discovery provider
- YaCy discovery provider
- RSS / Atom / sitemap discovery の統合
- Colly による depth / sitemap / link crawl
- Webwright 明示 fallback
- Browser Trace to API Discovery との artifact 接続
- fetch cache / failure cache の永続化
- Viewer Ops panel
- scheduled source sweep
- domain 別 policy / allowlist / denylist
- site terms / robots policy registry
