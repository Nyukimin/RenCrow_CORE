# Browser Trace to API Discovery 仕様

## 1. 目的

本仕様は、RenCrow において、ブラウザ操作中に発生したネットワーク通信を解析し、Web 画面の裏側で使われている API endpoint、request / response schema、取得フローを発見する仕組みを定義する。

この仕組みを **Browser Trace to API Discovery** と呼ぶ。

目的は、毎回ブラウザ操作や DOM scraping に依存するのではなく、可能な場合に、より安定した API ベースの取得処理へ変換することである。

```text
Browser 操作
  ↓
Network Trace
  ↓
API endpoint / schema 推定
  ↓
OpenAPI / client / coverage report
  ↓
Fetcher / Ingestor / Source Registry
```

本仕様は、browser trace が取得した CDP request / response event を offline post-processing し、観測された URL をテンプレート化し、JSON schema をサンプルから推定し、OpenAPI draft と coverage report を出力する考え方を RenCrow 向けに整理したものである。

## 2. 位置づけ

本仕様は、RenCrow の外部取得、Web 観測、Source Registry、staging / validator に接続する。

```text
19_DCI_直接コーパス探索仕様
  ローカル原文、ログ、仕様を調べ直す能力

21_AI_Native_Engineering_Workflow仕様
  Worker / Coder が働く開発環境を整える仕様

23_Workstream_Operating_Loop仕様
  作業ごとの継続ループと Artifact を扱う仕様

27_Browser_Trace_to_API_Discovery仕様
  Browser 操作ログから API 仕様を発見し、安定取得へ変換する仕様
```

本仕様は、DOM scraping の代替または補助として使う。

```text
DOM Scraping:
  画面構造から情報を抜く

Browser Trace to API Discovery:
  画面操作中の通信から API 仕様を推定する
```

## 3. 基本思想

RenCrow では、Web 取得の安定性を以下の順に考える。

```text
1. 公式 API
2. RSS / Atom / 公開 feed
3. 公開 JSON endpoint
4. Browser Trace から推定した API
5. DOM scraping
6. Manual browser operation
```

Browser Trace to API Discovery は、DOM scraping より安定する可能性があるが、公式 API ではない場合が多い。

したがって、発見された API はそのまま正式運用に使わず、必ず以下を通す。

```text
Trace
  ↓
API Candidate
  ↓
staging
  ↓
validator
  ↓
Source Registry
  ↓
Fetcher implementation
```

## 4. 対象

本仕様の対象は以下である。

```text
- JavaScript-heavy な Web サイト
- 公式 API 仕様が見つからない Web アプリ
- DOM 構造が頻繁に変わるサイト
- ブラウザ操作では取得できるが、HTML から直接取りにくいデータ
- ログイン後に画面内で読み込まれる一覧、詳細、検索結果
- Playwright / Browser automation で取得している既存ワークフロー
```

対象外は以下である。

```text
- 利用規約で自動取得が禁止されているサイト
- 認証情報や個人情報を含む通信の無制限解析
- 決済、課金、医療、金融、個人情報の操作系 API
- CSRF token や session cookie を外部保存しないと動かない API
- 意図的に非公開、保護されている内部 API
- レート制限やアクセス制御を回避する目的の利用
```

## 5. 用語定義

### 5.1 Browser Trace

ブラウザ操作中の通信、DOM、console、screenshot などを記録した観測ログ。

本仕様では、特に CDP network request / response を重視する。

Browser Trace to API Discovery 自体は traffic capture を行わない。既に保存された trace を入力にした offline post-processing として扱う。

### 5.2 API Candidate

Trace から発見された API らしき endpoint。

まだ正式 Source ではない。

### 5.3 Coverage Report

どの画面操作、endpoint、method、schema が観測済みで、どの操作が未観測かを示すレポート。

### 5.4 Generated Client

観測された API に対して生成された、検証用の簡易 client。

RenCrow では、正式採用前の検証用 Artifact として扱う。

## 6. 全体フロー

```text
User / Worker
  ↓
Browser Automation
  ↓
browser-trace 相当の記録
  ↓
Network Trace 保存
  ↓
Browser Trace to API Discovery
  ├─ request / response pairing
  ├─ endpoint grouping
  ├─ URL templatization
  ├─ schema inference
  ├─ OpenAPI draft generation
  ├─ coverage report generation
  └─ generated client draft
        ↓
API Candidate
        ↓
staging
        ↓
validator
        ↓
Source Registry
        ↓
Fetcher / Ingestor
```

## 7. 入力

### 7.1 必須入力

```text
- trace_run_id
- requests.jsonl
- responses.jsonl
- target_site
- user_goal
- captured_flow_description
```

### 7.2 任意入力

```text
- screenshots
- DOM snapshots
- console logs
- user annotations
- expected data fields
- known official docs
- login state description
```

## 8. 出力

### 8.1 標準出力

```text
api-spec/
  index.html
  openapi.yaml
  client.mjs
  coverage.md
  endpoint_inventory.json
  validation_notes.md
```

### 8.2 RenCrow 追加出力

```text
staging/api_candidates.jsonl
source_registry_candidates.jsonl
risk_assessment.md
fetcher_plan.md
```

## 9. API Candidate 形式

```json
{
  "candidate_id": "api_cand_20260518_000001",
  "trace_run_id": "trace_20260518_000001",
  "site_id": "example_site",
  "method": "GET",
  "observed_url": "https://example.com/api/items?page=1",
  "templated_url": "https://example.com/api/items?page={page}",
  "path_template": "/api/items",
  "query_params": [
    {
      "name": "page",
      "type": "integer",
      "observed_values": ["1", "2"]
    }
  ],
  "request_schema": null,
  "response_schema": {
    "type": "object",
    "properties": {
      "items": {
        "type": "array"
      }
    }
  },
  "auth_required": true,
  "contains_personal_data": "unknown",
  "risk_level": "medium",
  "status": "candidate"
}
```

## 10. Discovery 処理

### 10.1 request / response pairing

request_id、URL、timestamp、method などを使って、request と response を対応づける。

```text
request
  ↓
response
  ↓
paired_exchange
```

### 10.2 endpoint grouping

同じ API と見なせる URL をまとめる。

```text
/api/items?page=1
/api/items?page=2
/api/items?page=3
  ↓
/api/items?page={page}
```

### 10.3 URL templatization

観測値から path parameter や query parameter を推定する。

```text
/users/123
/users/456
  ↓
/users/{user_id}
```

ただし、ID 推定は誤る可能性があるため、confidence を付与する。

### 10.4 schema inference

response sample から JSON schema を推定する。

注意点:

```text
- sample が少ない schema は暫定扱い
- nullable / optional を過剰断定しない
- union type の可能性を残す
- empty array だけで型を確定しない
- error response も別 schema として扱う
```

### 10.5 OpenAPI draft generation

推定された endpoint / method / parameter / schema から OpenAPI draft を生成する。

これは正式仕様ではなく、**observed spec** として扱う。

```text
observed_openapi.yaml
```

### 10.6 Coverage report

どの操作が観測済みか、どこが不足しているかを出す。

```markdown
# API Coverage Report

## Observed flows
- search list
- item detail
- pagination

## Observed endpoints
- GET /api/items
- GET /api/items/{id}

## Missing flows
- create
- update
- delete
- error cases
- auth refresh

## Recommended next traces
- empty search result
- pagination last page
- invalid id
```

## 11. Source Registry 接続

Trace から発見した API は、即 promoted にしない。

必ず Source Registry の状態遷移を通す。

```text
observed
  ↓
candidate
  ↓
validated
  ↓
promoted
```

### 11.1 observed

Trace 上で観測されたが、意味や安定性は未確認。

### 11.2 candidate

endpoint として利用できる可能性がある。

### 11.3 validated

再実行、schema 確認、利用規約確認、PII 確認を通過。

### 11.4 promoted

Fetcher / Ingestor に正式採用可能。

## 12. Validator

### 12.1 必須検証

```text
- 利用規約に反しないか
- robots / API policy / rate limit の確認
- 認証情報を保存していないか
- 個人情報を含まないか
- CSRF / session token を固定化していないか
- 再実行可能か
- 公式 API や RSS が存在しないか
- error response を理解しているか
- rate limit を尊重できるか
```

### 12.2 promoted 禁止条件

以下に該当する場合、promoted にしてはいけない。

```text
- 認証 cookie を直接保存しないと動かない
- 個人アカウント固有の情報を無分類で扱う
- 規約上自動取得が禁止されている
- rate limit が不明
- response schema が安定しない
- destructive endpoint を含む
- purchase / payment / delete / update 操作を含む
```

## 13. Safety Gate

### 13.1 読み取り専用原則

MVP では、発見、利用する API は read-only に限定する。

許可:

```text
GET
HEAD
OPTIONS
```

条件付き:

```text
POST search
POST query
```

禁止:

```text
POST create
PUT
PATCH
DELETE
payment
purchase
refund
account update
message send
```

### 13.2 認証情報

以下を保存してはいけない。

```text
cookie
session token
csrf token
authorization header
refresh token
password
secret
```

Trace 保存時は secret redaction を行う。

### 13.3 個人情報

response に個人情報が含まれる可能性がある場合、以下を付与する。

```text
sensitivity: personal
review_required: true
```

## 14. RenCrow での利用ケース

### 14.1 News / Media サイト

画面上で記事一覧や検索結果を表示している API を観測し、安定したニュース取得 fetcher へ変換する。

### 14.2 X / SNS 調査

ログイン済みブラウザでの観測は慎重に扱う。

原則として、公式 API、エクスポート、手動入力を優先する。

Trace 利用時は personal data と rate limit に注意する。

### 14.3 商品、競合調査

競合 LP や公開ページの裏側で呼ばれる公開 JSON を確認し、market research item として staging する。

### 14.4 RenCrow 開発

自作 Viewer や Web UI の network trace を確認し、内部 API の仕様書を自動生成する。

これは特に安全で有用である。

## 15. Artifact Review Surface

生成された `api-spec/index.html`、`openapi.yaml`、`coverage.md` は RenCrow Viewer でレビュー可能にする。

```text
Artifact:
  type: api_spec
  files:
    - api-spec/index.html
    - openapi.yaml
    - coverage.md
```

人間は以下を確認する。

```text
- endpoint が妥当か
- 不要な個人情報が含まれていないか
- schema 推定が過剰でないか
- promoted してよいか
- fetcher 化してよいか
```

## 16. Fetcher 生成

validated 以上になった API Candidate は、Fetcher 候補を生成できる。

```text
OpenAPI draft
  ↓
client draft
  ↓
fetcher_plan.md
  ↓
Fetcher proposal
  ↓
Human approval
  ↓
implementation
```

### 16.1 Fetcher 要件

```text
- rate limit 設定
- retry 方針
- timeout
- user-agent
- error handling
- schema validation
- Source Registry 連携
- staging 出力
- promoted DB への直接 write 禁止
```

## 17. Tool / Skill 構成

```text
skills/core/browser-trace-to-api-discovery/
  SKILL.md
  trace_requirements.md
  openapi_review_checklist.md
  safety_checklist.md
  fetcher_plan_template.md
  evals/
    public-json-api.md
    pagination.md
    auth-risk.md
    pii-risk.md
```

## 18. SKILL.md 案

```markdown
# Browser Trace to API Discovery

## Purpose

ブラウザ操作中の network trace から、API endpoint、schema、coverage を推定し、安定した Fetcher 化の候補を作る。

## When to Use

- 公式 API 仕様がない
- browser-trace がある
- DOM scraping が不安定
- サイトが裏側で JSON を取得している
- Fetcher 化できるか調べたい

## When Not to Use

- 利用規約で自動取得が禁止されている
- 決済、削除、送信など write 操作が関係する
- personal data を含む可能性が高い
- 公式 API / RSS が存在する
- ただ画面を読むだけで十分

## Procedure

1. trace_run_id を確認する
2. requests / responses jsonl を読む
3. request / response を pairing する
4. endpoint を grouping する
5. URL を template 化する
6. schema を推定する
7. OpenAPI draft を生成する
8. coverage report を作る
9. risk assessment を作る
10. staging/api_candidates.jsonl に出す

## Safety

- traffic capture 自体は行わない
- read-only endpoint を優先する
- credentials を保存しない
- promoted 前に validator を通す
```

## 19. DB 設計

### 19.1 browser_trace_run

```sql
CREATE TABLE IF NOT EXISTS browser_trace_run (
  trace_run_id TEXT PRIMARY KEY,
  workstream_id TEXT,
  site_id TEXT,
  goal TEXT,
  trace_path TEXT NOT NULL,
  captured_at TEXT NOT NULL,
  created_at TEXT NOT NULL
);
```

### 19.2 api_candidate

```sql
CREATE TABLE IF NOT EXISTS api_candidate (
  candidate_id TEXT PRIMARY KEY,
  trace_run_id TEXT NOT NULL,
  site_id TEXT,
  method TEXT NOT NULL,
  observed_url TEXT,
  templated_url TEXT,
  path_template TEXT,
  auth_required INTEGER DEFAULT 0,
  contains_personal_data TEXT DEFAULT 'unknown',
  risk_level TEXT,
  status TEXT NOT NULL,
  confidence REAL,
  created_at TEXT NOT NULL
);
```

### 19.3 api_candidate_schema

```sql
CREATE TABLE IF NOT EXISTS api_candidate_schema (
  schema_id TEXT PRIMARY KEY,
  candidate_id TEXT NOT NULL,
  schema_type TEXT NOT NULL,
  schema_json TEXT NOT NULL,
  sample_count INTEGER DEFAULT 0,
  confidence REAL,
  created_at TEXT NOT NULL
);
```

### 19.4 api_coverage_report

```sql
CREATE TABLE IF NOT EXISTS api_coverage_report (
  report_id TEXT PRIMARY KEY,
  trace_run_id TEXT NOT NULL,
  observed_flows TEXT,
  observed_endpoints TEXT,
  missing_flows TEXT,
  recommended_next_traces TEXT,
  created_at TEXT NOT NULL
);
```

## 20. 設定ファイル案

```yaml
browser_trace_to_api:
  enabled: true
  default_mode: "offline_postprocess"

  input:
    require_browser_trace: true
    accepted_paths:
      - ".o11y/"
      - "traces/"

  output:
    generate_openapi: true
    generate_client_draft: true
    generate_coverage_report: true
    generate_risk_assessment: true

  safety:
    read_only_only: true
    redact_credentials: true
    require_terms_review: true
    require_human_approval_for_promote: true
    deny_methods:
      - "PUT"
      - "PATCH"
      - "DELETE"
    deny_sensitive_flows:
      - "payment"
      - "purchase"
      - "refund"
      - "account_update"
      - "message_send"

  source_registry:
    default_status: "candidate"
    require_validator_before_promoted: true
```

## 20.1 実装状況

2026-05 時点で、Browser Trace to API Discovery は offline post-process の最小基盤まで部分実装済みである。

実装済み。

```text
Domain:
  internal/domain/browsertrace
  trace run
  api candidate
  api candidate schema
  api coverage report
  api artifact

Application:
  internal/application/browsertrace
  requests.jsonl / responses.jsonl reader
  request / response pairing
  URL templatization
  query parameter extraction
  response JSON schema inference
  write method filtering
  credential value non-persistence
  API candidate validation result generation
  observed OpenAPI draft artifact generation
  coverage report artifact generation
  endpoint inventory artifact generation
  risk assessment artifact generation
  fetcher plan artifact generation
  client draft artifact generation
  optional live policy check
  robots.txt read-only check
  HEAD response rate-limit header check

Persistence:
  internal/infrastructure/persistence/browsertrace
  JSONL store
  SQLite store
  api_artifact.jsonl
  api_candidate_validation.jsonl
  L1 staging candidate store

Config:
  browser_trace_to_api.enabled
  browser_trace_to_api.storage
  browser_trace_to_api.log_path
  browser_trace_to_api.sqlite_path
  browser_trace_to_api.read_only_only
  browser_trace_to_api.require_terms_review
  browser_trace_to_api.require_human_approval_for_promote
  browser_trace_to_api.generate_openapi
  browser_trace_to_api.generate_coverage_report
  browser_trace_to_api.accepted_paths
  browser_trace_to_api.deny_methods
  browser_trace_to_api.deny_sensitive_flows

Viewer / API:
  GET  /viewer/browser-trace-api
  POST /viewer/browser-trace-api/discover
  `browser_trace_to_api.accepted_paths` 外の trace / requests / responses path は拒否
  `live_policy_check=true` 指定時だけ robots / rate-limit の read-only live check
  api_validations response
  api_artifacts response
  Workstream Artifact pending_review registration
  Ops summary card

Source Registry / L1:
  discovery result の api candidate を `kb:browser_trace_api` namespace の
  `L1StagingKindSearchResult` pending candidate として保存する。
  meta には `source_kind=browser_trace_api`, `review_required=true`,
  `promote_requires_validator=true`, `risk_level`, `auth_required`,
  `contains_personal_data` を残す。
```

残作業。

```text
Artifact:
  Workstream Artifact Review Surface への pending_review 登録はある。
  client draft artifact はある。未検証 candidate では実行時に error になる review-only draft とする。

Source Registry:
  L1 staging candidate はある。次は Source Registry entry / validator / promote UI の状態遷移へ接続する。

Validator:
  terms / official API / auth / PII / risk の未確認は `needs_review` として保存する。
  `browser_trace_to_api.deny_sensitive_flows` に一致する endpoint は
  `sensitive_flow_review_required` として保存する。
  `live_policy_check=true` 指定時は robots.txt と HEAD response の rate-limit header を確認し、
  未確認の場合は `robots_review_required` / `rate_limit_review_required` として保存する。
  通常の offline post-process では外部アクセスしない。

Fetcher:
  validation result を反映した fetcher_plan artifact はある。
  validation result を反映した client draft artifact はある。
  blocked candidate は validator issue 解消まで実装提案不可として表示する。
  次は Human approval 後の proposal / 実装接続を行う。

Storage:
  JSONL store と互換の SQLite tables はある。
  `browser_trace_to_api.storage: sqlite` の場合は runtime で SQLite store を使う。
```

## 21. EventId

```text
browser_trace_registered
browser_trace_api_discovery_started
browser_trace_api_discovery_completed
api_candidate_found
api_schema_inferred
openapi_draft_generated
coverage_report_generated
api_candidate_validation_started
api_candidate_validated
api_candidate_rejected
fetcher_plan_created
fetcher_proposal_created
```

## 22. MVP 実装順

### 22.1 Phase 1: Trace 入力対応

- trace_run 登録
- `requests.jsonl` / `responses.jsonl` 読取
- request / response pairing

### 22.2 Phase 2: Endpoint 発見

- endpoint grouping
- URL templatization
- query parameter 抽出
- method 分類

### 22.3 Phase 3: Schema 推定

- response sample 収集
- JSON schema 推定
- confidence 付与
- error response 分離

### 22.4 Phase 4: Artifact 生成

- `observed_openapi.yaml`
- `coverage.md`
- `endpoint_inventory.json`
- `risk_assessment.md`
- `fetcher_plan.md`
- `client.mjs`

### 22.5 Phase 5: Source Registry 連携

- api_candidate DB
- staging 出力
- validator
- promoted 判定

### 22.6 Phase 6: Fetcher proposal

- client draft は review-only artifact として生成済み。未検証 candidate は実行時に error とする。
- `fetcher_plan.md`
- `/viewer/browser-trace-api/fetcher-proposals` で validated candidate + Human approval から review-only `fetcher_proposal` artifact を生成する。
- Fetcher proposal は正式 Fetcher 実装や promoted DB write を行わない。実装する場合は別 Goal / Promotion Gate で扱う。

## 23. 成功指標

```text
trace_to_api_run_count
api_candidate_count
validated_api_candidate_count
promoted_api_candidate_count
dom_scraping_replaced_count
fetcher_success_rate
schema_validation_error_rate
coverage_gap_count
safety_rejection_count
```

重要指標:

```text
- DOM scraping から API fetcher へ置換できた数
- promoted 後の取得成功率
- schema mismatch 発生率
- 個人情報、認証情報の漏えいゼロ
- write 系 endpoint 誤実行ゼロ
```

## 24. 禁止事項

```text
- browser trace から認証情報を保存する
- write 系 endpoint を自動実行する
- promoted 前に fetcher へ組み込む
- 利用規約確認なしに定期取得する
- personal data を無分類で staging する
- CSRF token や cookie をコードに埋め込む
- API 発見をレート制限回避に使う
- 公式 API があるのに非公開 API を優先する
```

## 25. 設計上の結論

Browser Trace to API Discovery は、RenCrow の外部取得能力を高めるための発見 Skill である。

これはブラウザ操作を自動化する Skill ではない。

これは、既に取得された Browser Trace を解析し、API 候補を抽出し、OpenAPI draft と coverage report を作る offline post-processing である。

RenCrow では、この仕組みを以下の目的で使う。

```text
- DOM scraping 依存を減らす
- Web 取得を API fetcher 化する
- Source Registry を強化する
- News / Market Research / Knowledge 取得を安定化する
- 自作 Web UI の内部 API 仕様を自動生成する
```

ただし、非公開 API やログイン済み通信を扱うため、Source Registry、staging、validator、Human approval を必須とする。

## 26. まとめ

本仕様は、RenCrow における Browser Trace から API 仕様を発見する仕組みを定義する。

カテゴリは以下である。

```text
Browser Trace
API Discovery
OpenAPI Draft
Fetcher Generation
Source Registry
Staging
Validator
Safety Gate
```

最終原則は以下である。

```text
ブラウザ操作で見えた通信は、すぐに正式 API として扱わない。
まず観測し、候補化し、検証し、Source Registry へ昇格する。

安定取得のために API 化する。
安全確認なしに自動取得しない。
```
