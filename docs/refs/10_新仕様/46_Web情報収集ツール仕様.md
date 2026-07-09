# Web 情報収集ツール仕様

## 目的

RenCrow が外部 SaaS / 有料検索 API に依存せず、日常的に Web 情報を集められる常用ツールを持つ。

このツールは、単発のブラウザ操作や ad hoc scraping ではなく、検索、取得、本文抽出、証跡保存、Source Registry staging までを一貫して扱う。

仮称を `rencrow-web-gather` とする。

## 基本思想

Web 情報収集は 1 つの巨大ツールにしない。次の責務に分ける。

```text
Discovery
  URL 候補を探す
Fetch
  URL を取得する
Extract
  本文、タイトル、日時、著者、出典を抽出する
Stage / Cache
  証跡つきで Source Registry / L1 staging へ保存する
Validate / Promote
  既存 Source Registry validator / promote に渡す
```

RenCrow の正本は「検索サービスの結果」ではなく、取得 URL、取得時刻、HTTP status、raw hash、抽出方式、本文、検証状態である。

## 対象

対象:

- 公開 Web ページ
- RSS / Atom / sitemap
- 公開 JSON endpoint
- ニュース、技術記事、仕様書、公式ドキュメント
- Source Registry へ pending staging として残したい外部情報
- IdleChat / Worker / Research / KB 更新のための補助情報

対象外:

- ログイン必須ページの自動常用取得
- 有料検索 API / SaaS scraping API への依存
- robots / terms / rate limit を回避する取得
- CAPTCHA、アクセス制御、paywall の突破
- cookie / Authorization header / API key を staging raw_text に保存する処理
- 個人情報やシークレットを含む取得結果の無審査 promote

## 全体構成

```text
User / Worker / Idle task
  ↓
rencrow-web-gather
  ├─ discovery provider
  │   ├─ direct URL
  │   ├─ RSS / Atom / sitemap
  │   ├─ SearXNG
  │   └─ YaCy
  ├─ fetch provider
  │   ├─ Go HTTP fetch
  │   ├─ Colly crawler
  │   └─ Webwright / Playwright fallback
  ├─ extractor
  │   ├─ go-readability
  │   ├─ metadata parser
  │   └─ trafilatura optional sidecar
  └─ staging writer
      ↓
Source Registry staging
  ↓
validator
  ↓
promote to news / knowledge / memory
```

## Provider 方針

### Discovery provider

Discovery provider は URL 候補を返すだけで、本文取得や memory promote を行わない。

| provider | 役割 | 備考 |
| --- | --- | --- |
| `direct_url` | ユーザー指定 URL を候補化する | 最小実装 |
| `rss_atom` | RSS / Atom / sitemap から URL を列挙する | Source Registry 既存 fetcher と整合させる |
| `web_gather` | Source Registry 登録 URL を Web Gather fetch / extract で pending staging する | validate / promote は自動実行しない |
| `searxng` | self-hosted metasearch から候補取得する | 外部検索 API は使わないが、上流検索エンジンには問い合わせる |
| `yacy` | YaCy peer / local index から候補取得する | 検索エンジン依存を減らす候補。初期 index 育成が必要 |
| `local_cache` | L1 search cache / promoted knowledge から再利用する | 再検索抑制 |

SearXNG と YaCy は差し替え可能にする。RenCrow の本体ロジックが特定検索エンジンの JSON schema に直接依存してはいけない。

### Fetch provider

Fetch provider は URL の取得と取得証跡の保存責務を持つ。

| provider | 用途 |
| --- | --- |
| `http` | 通常の HTML / JSON / text 取得 |
| `colly` | sitemap / link 巡回 / depth 制御つき取得 |
| `webwright` | JS 必須ページ、操作が必要なページ、スクリーンショット証跡が必要なページ |

通常は `http` を第一経路とする。`webwright` は fallback または明示指定であり、RenCrow 本体 runtime の必須 dependency にしない。

### Extractor

Extractor は取得結果から本文と metadata を抽出する。

| extractor | 用途 |
| --- | --- |
| `go_readability` | HTML 記事本文抽出の第一候補 |
| `metadata` | title / description / canonical / published_time / author 抽出 |
| `plain_text` | text/plain / markdown / JSON の簡易整形 |
| `trafilatura` | 精度重視の optional sidecar。Go 本体へ直接混ぜない |

抽出失敗時も raw hash、HTTP status、content type、失敗理由を staging meta に残す。

## CLI

最初の常用インターフェースは CLI とする。

```bash
rencrow web-gather url https://example.com/article \
  --namespace kb:web \
  --source-id web:example:article

rencrow web-gather search "local LLM queue timeout" \
  --provider searxng \
  --limit 5 \
  --namespace kb:research

rencrow web-gather run-source source_id
```

CLI は取得結果を直接 memory / knowledge へ promote しない。出力は pending staging とし、既存の validator / promote を通す。

## API / Tool contract

Worker から呼べる tool contract は次を想定する。

### `web_gather.search`

入力:

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

出力:

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

### `web_gather.fetch`

入力:

```json
{
  "url": "string",
  "fetch_provider": "http|colly|webwright",
  "extractor": "go_readability|plain_text|trafilatura",
  "namespace": "kb:web",
  "source_id": "web:example",
  "store_staging": true
}
```

出力:

```json
{
  "url": "string",
  "status": "ok|failed",
  "http_status": 200,
  "title": "string",
  "text_preview": "string",
  "raw_hash": "sha256:...",
  "staging_id": "string",
  "validation_status": "pending",
  "diagnostics": {
    "fetch_provider": "http",
    "extractor": "go_readability",
    "elapsed_ms": 0,
    "error": ""
  }
}
```

### `web_gather.search_and_fetch`

検索候補を取得し、上位 N 件を fetch / extract / staging する。

この API は便利だが、初期実装では `search` と `fetch` を分ける。検索候補の質と本文抽出失敗を混同しないためである。

## Staging 出力

Source Registry / L1 staging へ保存する場合、`Kind` は `external_fetch` とする。

必須 field:

- `Kind`
- `Namespace`
- `SourceID`
- `SourceURL`
- `RawText`
- `SummaryDraft`
- `RawHash`
- `ValidationStatus=pending`
- `CreatedAt`
- `UpdatedAt`

必須 meta:

```json
{
  "tool": "rencrow-web-gather",
  "discovery_provider": "searxng",
  "fetch_provider": "http",
  "extractor": "go_readability",
  "http_status": 200,
  "content_type": "text/html",
  "canonical_url": "https://example.com/article",
  "fetched_at": "RFC3339",
  "elapsed_ms": 1234,
  "raw_hash": "sha256:...",
  "security_warning_source": "web_gather",
  "security_warnings": [],
  "license_note": "review source terms before promotion"
}
```

## Cache

Web 情報収集は L1 search cache と fetch cache を使う。

cache key:

```text
search: provider + normalized_query + language + freshness
fetch: canonical_url + accept + extractor
```

cache は成功結果だけでなく、短時間の失敗も記録してよい。ただし失敗 cache を permanent failure として扱わない。

推奨 TTL:

| 種別 | TTL |
| --- | ---: |
| search result | 6h |
| article fetch | 24h |
| official docs fetch | 7d |
| failure cache | 10m |

ユーザーが明示 refresh した場合は cache を無視して再取得する。

## Rate limit / robots / terms

初期実装では、完全な robots 解釈を必須にしないが、少なくとも次を守る。

- domain 別 concurrency を制限する。
- domain 別 minimum interval を持つ。
- User-Agent を明示する。
- robots / terms が取得禁止を示す場合は失敗として staging し、成功扱いしない。
- 403 / 429 / CAPTCHA / bot challenge は回避せず、`blocked_by_site` として記録する。

推奨初期値:

```text
global_concurrency: 2
per_domain_concurrency: 1
per_domain_min_interval: 3s
request_timeout: 15s
max_body_bytes: 5MB
max_redirects: 5
```

## Security

外部入力は prompt や memory と混ぜない。

- `security.DetectPromptInjectionWarnings` を抽出本文に適用する。
- warning は staging meta に保存する。
- warning 付きの staging は自動 promote しない。
- API key、cookie、Authorization header、Set-Cookie は raw_text / meta に保存しない。
- HTML raw 全文の保存は初期実装では行わない。保存するのは抽出本文、hash、metadata、短い diagnostic とする。

## Viewer

Viewer では以下を確認できるようにする。

- 直近 search query
- search provider
- result count
- fetch success / failed count
- staging count
- blocked_by_site / timeout / extraction_failed の件数
- staging item へのリンク

Viewer は収集結果を正式 memory として表示してはいけない。pending / validated / promoted を区別する。

## 実装境界

| 層 | 責務 |
| --- | --- |
| `modules/webgather` | provider contract、URL正規化、cache key、rate policy、抽出結果 DTO |
| `internal/application/webgather` | search / fetch / search_and_fetch use case |
| `internal/infrastructure/webgather` | HTTP / Colly / SearXNG / YaCy / readability 実装 |
| `RenCrow_Tools/tools/webwright_fetch` | JS fallback。既存ツールを再利用 |
| `internal/application/sourcefetcher` | staging / validate / promote の既存境界 |
| `cmd/rencrow` | CLI / runtime wiring |
| `internal/adapter/viewer` | 診断表示 |

Go 本体の第一実装は `http` + `go_readability` + staging writer とする。SearXNG / YaCy / Webwright は provider として後から追加できる形にする。

## 段階実装

### Phase 1: URL fetch 常用化

- `rencrow web-gather url <url>`
- Go HTTP fetch
- go-readability 抽出
- Source Registry pending staging JSON 生成
- security warning metadata
- unit test / local fixture test

### Phase 2: Search discovery

- `web_gather.search`
- SearXNG provider
- local cache provider
- search result cache
- search result から明示 fetch

### Phase 3: Source Registry integration

- Source Registry source kind `web_gather`
- scheduled sweep では `web_gather` source を pending staging で止める
- Viewer staging review 連携
- validator / promote flow の E2E は既存 review 操作に委ねる

### Phase 4: JS fallback

- `fetch_provider=webwright`
- 既存 `RenCrow_Tools/tools/webwright_fetch` との共通 staging meta
- screenshot / trace artifact の保存
- Browser Trace to API Discovery への接続

### Phase 5: YaCy / local index

- YaCy discovery provider
- domain / topic 別 local index 育成
- SearXNG なしでも候補を返せる構成

## 検証

local test:

```bash
GOCACHE=/tmp/rencrow-gocache go test ./modules/webgather ./internal/application/webgather ./internal/infrastructure/webgather
GOCACHE=/tmp/rencrow-gocache go test ./internal/application/sourcefetcher ./internal/infrastructure/persistence/conversation
```

fixture:

- static HTML article
- malformed HTML
- text/plain
- JSON endpoint
- 404
- 429
- oversized body
- prompt injection text

live 確認:

```bash
rencrow web-gather url https://example.com --namespace kb:web
rencrow web-gather search "RenCrow local LLM queue timeout" --provider searxng --limit 3
curl -fsS 'http://127.0.0.1:18790/viewer/source-registry?action=staging&status=pending'
```

完了条件:

- fetch / extract / staging が別々に失敗分類される。
- pending staging が validate 前に promote されない。
- raw hash と source URL が残る。
- security warning が meta に残る。
- external service API key なしで動く。

## 非目標

- Google / Bing / Brave / Tavily / SerpAPI 等の外部 API key を前提にしない。
- CAPTCHA 回避、アクセス制御回避、proxy rotation を実装しない。
- 取得本文を無審査で confirmed memory にしない。
- Webwright を RenCrow 本体の必須 dependency にしない。
- 検索順位を真実として扱わない。
