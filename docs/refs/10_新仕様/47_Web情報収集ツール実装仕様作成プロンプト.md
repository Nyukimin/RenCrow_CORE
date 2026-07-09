# Web 情報収集ツール 実装仕様作成プロンプト

あなたは RenCrow / picoclaw_multiLLM の仕様整理担当兼実装仕様作成担当です。

目的は、`docs/10_新仕様/46_Web情報収集ツール仕様.md` を入口として、RenCrow が外部 SaaS / 有料検索 API に依存せず常用できる Web 情報収集ツールを実装できる粒度まで落とし込むことです。

今回は実装そのものは行いません。仕様調査、現行 code の確認、責務分解、段階実装計画、検証条件、停止条件を整理し、実装担当者がそのまま着手できる実装仕様を作成してください。

作成先は次とします。

- `docs/10_新仕様/48_Web情報収集ツール実装仕様.md`

## 最初に読むもの

必ず以下を読むこと。

1. `AGENTS.md`
2. `CLAUDE.md`
3. `docs/01_正本仕様/実装仕様.md`
4. `docs/10_新仕様/46_Web情報収集ツール仕様.md`
5. `docs/10_新仕様/09_Memory_SourceRegistry仕様.md`
6. `RenCrow_Tools/tools/webwright_fetch/README.md`
7. `docs/10_新仕様/27_Browser_Trace_to_API_Discovery仕様.md`
8. `docs/10_新仕様/20_Tool_Harness_Contract_Mediation仕様.md`
9. `docs/10_新仕様/10_検証仕様.md`

必要に応じて以下も確認すること。

- `docs/10_新仕様/13_実装項目インベントリ.md`
- `docs/10_新仕様/19_DCI_直接コーパス探索仕様.md`
- `docs/10_新仕様/21_AI_Native_Engineering_Workflow仕様.md`
- `docs/10_新仕様/32_E2E_runtime確認チェックリスト.md`
- `rules/common/rules_security.md`
- `rules/common/rules_state_management.md`
- `rules/common/rules_logging.md`
- `rules/common/rules_testing.md`

## production code 確認対象

少なくとも以下を `rg` とファイル読み取りで確認すること。

```text
internal/application/sourcefetcher
internal/adapter/viewer/source_registry_handler.go
internal/infrastructure/persistence/conversation/l1_sqlite_*
internal/domain/security
internal/infrastructure/persistence/toolregistry
internal/domain/tool
internal/infrastructure/tools
cmd/picoclaw/cli_*.go
cmd/picoclaw/runtime_viewer_handlers.go
RenCrow_Tools/tools/webwright_fetch
```

確認観点:

- Source Registry staging の Go 型、必須 field、保存 API
- staging validate / promote の呼び出し境界
- search cache / event log / raw hash の既存実装
- security warning の保存方法
- CLI 追加の既存パターン
- Tool Registry / tool harness への登録パターン
- Viewer source registry panel の表示契約
- Webwright Fetch の staging JSONL 変換形式

## 実装仕様で必ず守る原則

- 外部 API key を前提にしない。
- Google / Bing / Brave / Tavily / SerpAPI 等の有料・外部検索 API を必須にしない。
- SearXNG / YaCy は provider として差し替え可能にする。
- Phase 1 は direct URL fetch を最小実装にする。
- 通常 fetch は Go HTTP を第一経路にする。
- HTML 本文抽出は `go-readability` を第一候補にする。
- Colly は link crawl / sitemap / depth 制御が必要になった段階で追加する。
- Webwright / Playwright は JS fallback とし、RenCrow 本体 runtime の必須 dependency にしない。
- fetch 結果を直接 confirmed memory / knowledge にしない。
- 出力は Source Registry / L1 staging の `pending` とする。
- validate / promote は既存 Source Registry の境界を使う。
- prompt injection warning を必ず metadata に残す。
- cookie / Authorization / Set-Cookie / API key / secret を raw_text や meta に保存しない。
- robots / terms / 403 / 429 / CAPTCHA / bot challenge は回避しない。失敗分類として記録する。
- Viewer 表示 state と永続化 state を混同しない。
- fallback を成功扱いしない。

## 調査タスク

次を調査し、実装仕様に反映してください。

### 1. 既存 Source Registry 境界

- staging item の保存 API
- raw hash の生成場所
- validation status の状態遷移
- security warnings の保存形式
- promote 先の news / knowledge / memory の違い
- Viewer で staging を確認する API

### 2. CLI 追加方法

- `picoclaw` CLI の subcommand 追加パターン
- config 読み込み、runtime dependency 初期化の範囲
- CLI で L1 store を使う場合の初期化方法
- CLI 実行結果の stdout / stderr 契約

### 3. HTTP fetch 実装境界

- request timeout
- max body bytes
- redirect policy
- User-Agent
- content-type 判定
- charset / gzip / brotli 対応の要否
- domain rate limit
- failure cache

### 4. Extractor 境界

- `go-readability` を dependency として追加する場合の影響
- title / excerpt / byline / site name / image / favicon の扱い
- plain text / JSON / markdown の扱い
- extraction failed でも staging するか
- raw HTML 全文を保存しない方針

### 5. Discovery provider

Phase 1 では direct URL のみでよい。

Phase 2 以降のために、SearXNG / YaCy / local cache の interface をどう切るか整理してください。

### 6. Tool contract

Worker から使える tool として以下を定義してください。

- `web_gather.fetch`
- `web_gather.search`
- `web_gather.search_and_fetch` は後段でよい

Tool contract は `input schema`、`output schema`、失敗時 response、ログ項目を含めること。

### 7. Viewer / Ops 表示

初期実装で Viewer まで入れるか、Phase 2 以降にするかを判断してください。

最低限、Source Registry staging から確認できるなら Phase 1 では Viewer 新規タブを作らない選択を許可します。

## 実装仕様の構成

作成する `48_Web情報収集ツール実装仕様.md` は、必ず以下の構成にしてください。

```markdown
# Web 情報収集ツール実装仕様

## 1. 目的

## 2. 参考仕様

## 3. 現行実装調査
### 3.1 Source Registry / L1 staging
### 3.2 Webwright Fetch
### 3.3 CLI / Tool Registry
### 3.4 Security warning

## 4. 実装範囲
### 4.1 Phase 1 で実装すること
### 4.2 Phase 1 で実装しないこと
### 4.3 Phase 2 以降

## 5. アーキテクチャ
### 5.1 package 構成
### 5.2 domain / modules contract
### 5.3 application usecase
### 5.4 infrastructure provider
### 5.5 cmd runtime / CLI

## 6. データ契約
### 6.1 input DTO
### 6.2 output DTO
### 6.3 staging mapping
### 6.4 meta schema
### 6.5 error code

## 7. Fetch policy
### 7.1 timeout
### 7.2 rate limit
### 7.3 redirect
### 7.4 body size
### 7.5 content type
### 7.6 robots / blocked response

## 8. Extract policy
### 8.1 HTML
### 8.2 plain text
### 8.3 JSON
### 8.4 extraction failure

## 9. Security policy

## 10. CLI 仕様

## 11. Tool contract

## 12. Logs / Evidence

## 13. Tests
### 13.1 Unit
### 13.2 Application
### 13.3 Infrastructure fixture
### 13.4 CLI
### 13.5 Source Registry integration
### 13.6 Live manual check

## 14. 実装手順

## 15. 完了条件

## 16. 停止条件

## 17. 将来拡張
```

## package 構成の期待案

実装仕様では、以下の構成を第一案として検討してください。

```text
modules/webgather
  contract.go
  policy.go
  normalize.go
  errors.go
  staging.go

internal/application/webgather
  fetch_usecase.go
  search_usecase.go
  staging_writer.go

internal/infrastructure/webgather
  http_fetcher.go
  readability_extractor.go
  searxng_discovery.go        # Phase 2
  yacy_discovery.go           # Phase 5
  cache.go

cmd/picoclaw
  cli_web_gather.go
```

既存 architecture により、より適切な配置がある場合は、理由を明記して変更してよい。

## Phase 1 の最小実装仕様

Phase 1 は必ず小さくする。

実装対象:

- `picoclaw web-gather url <url>`
- Go HTTP fetch
- max body bytes
- request timeout
- content-type 判定
- HTML は `go-readability` 抽出
- text/plain はそのまま整形
- JSON は短い pretty / text 化
- Source Registry / L1 staging へ `pending` 保存
- security warning metadata
- raw hash
- failure classification
- unit / fixture / CLI test

実装しない:

- SearXNG
- YaCy
- Webwright 自動 fallback
- Viewer 新規画面
- scheduler / sweep
- automatic promote
- external PR / external API

## Error code

実装仕様では、少なくとも以下の error code を定義してください。

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

## Staging meta 必須項目

実装仕様では、staging meta に最低限以下を入れるよう定義してください。

```json
{
  "tool": "rencrow-web-gather",
  "tool_version": "v0.1",
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
  "security_warning_source": "web_gather",
  "security_warnings": [],
  "license_note": "review source terms before promotion"
}
```

## 実装仕様作成時の禁止事項

- 実コードを変更しない。
- dependency を追加しない。
- Git commit / push をしない。
- 「既存の Source Registry に多分入る」と推測で書かない。必ず code を確認する。
- テストだけを根拠に runtime 実装済み扱いしない。
- Webwright を本体必須 dependency にしない。
- 取得結果の自動 promote を Phase 1 に入れない。

## 最終出力

作業完了時は、次を報告してください。

- 作成したファイルパス
- 読んだ主な仕様
- 確認した production code
- Phase 1 の実装範囲
- Phase 1 でやらないこと
- 未解決の判断事項

