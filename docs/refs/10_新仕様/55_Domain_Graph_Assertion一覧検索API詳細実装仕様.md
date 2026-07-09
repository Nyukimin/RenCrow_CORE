# Domain Graph Assertion 一覧検索 API 詳細実装仕様

## 1. 目的

`domain_graph_assertion` に保存された外部世界の関係事実を、Viewer / API / `pkg/rencrowclient` から確認・検索できる current view として実装する。

現行実装では、validated L1 staging を `target=domain_graph` で `domain_graph_assertion` へ promote する入口まではある。一方で、保存済み assertion を一覧・検索する store API、Viewer API、runtime route、client validation、Viewer 表示は未実装である。

この仕様では、まず assertion を「見える」「検索できる」「malformed current view を成功扱いしない」状態まで実装する。Movie 固有 DB への変換、topic candidate 生成、Qdrant summary sync は次段に分ける。

## 2. 参考仕様

- `docs/01_正本仕様/実装仕様.md`
- `docs/10_新仕様/09_Memory_SourceRegistry仕様.md`
- `docs/10_新仕様/49_Movie_Graph_Mio_Topic仕様.md`
- `docs/10_新仕様/50_Hobby_Graph_Mio_Topic仕様.md`
- `docs/10_新仕様/52_Domain_Graph_DB経路実装仕様.md`
- `docs/10_新仕様/53_Domain_Graph_Assertion一覧検索API実装仕様.md`
- `docs/10_新仕様/54_Domain_Graph_Assertion一覧検索API実装仕様作成プロンプト.md`
- `docs/10_新仕様/10_検証仕様.md`

## 3. 現行実装調査

### 3.1 Domain Graph persistence

現行実装:

- `internal/infrastructure/persistence/conversation/l1_sqlite_schema.go`
  - `domain_graph_assertion` table は作成済み。
  - index は `domain, created_at DESC`、`domain, entity_type, entity_id`、`source_id, raw_hash`。
- `internal/infrastructure/persistence/conversation/l1_sqlite_types.go`
  - `L1DomainGraphAssertion` 型は存在する。
- `internal/infrastructure/persistence/conversation/l1_sqlite_domain_graph.go`
  - `PromoteValidatedStagingItemToDomainGraph(...)` は存在する。
  - pending / rejected staging は promote できない。
  - external fetch / search result 以外は domain graph promote できない。
  - evidence には staging ID、source、raw hash、license note、validation status が入る。

不足:

- `DomainGraphAssertionQuery` がない。
- `DomainGraphAssertions(ctx, q)` がない。
- `domain_graph_assertion` を scan する helper がない。
- query validation / pagination / filter SQL が未実装。

### 3.2 Source Registry promote

現行実装:

- `internal/adapter/viewer/source_registry_handler.go`
  - `/viewer/source-registry?action=promote` は `target=domain_graph` を受け付ける。
- `pkg/rencrowclient/client.go`
  - `SourceRegistryPromoteRequest` は `domain_graph` 用 field を持つ。
  - promote response の `StagingID` / `Domain` / `EntityType` / `CreatedAt` validation がある。

不足:

- promote 後の assertion を一覧で確認する API がない。
- Viewer から `target=domain_graph` を指定する UI は未実装である。Phase 1 では一覧 API を優先し、promote UI 拡張は別タスクにできる。

### 3.3 Viewer handler / runtime route

現行パターン:

- `internal/adapter/viewer/memory_layers_handler.go`
  - store nil 時に 503 を返す。
  - handler 内で query parse / limit validation / snapshot validation を行う。
- `cmd/picoclaw/runtime_viewer_handlers.go`
  - L1 store が nil の場合も unavailable handler を deps に登録する pattern がある。
- `cmd/picoclaw/routes.go`
  - deps に handler がある場合だけ route を登録する。
- `cmd/picoclaw/runtime_dependencies.go`
  - Viewer handler は `Dependencies` の `http.HandlerFunc` field として保持する。

不足:

- `viewerDomainGraphAssertions` 依存 field がない。
- `HandleDomainGraphAssertions(...)` handler がない。
- `/viewer/domain-graph/assertions` route がない。
- runtime readiness に domain graph を出す field がない。Phase 1 では必須にしないが、Ops で blocked を見たい場合は Phase 2 で追加する。

### 3.4 rencrowclient validation

現行パターン:

- `pkg/rencrowclient.Client` は Viewer API の typed method を持つ。
- Source Registry / Memory Layers / Runtime Config は response validation で malformed current view を拒否する。
- timestamp 欠落、ID 欠落、status 不正、duplicate ID を local client error として扱うパターンがある。

不足:

- `DomainGraphAssertionsRequest`
- `DomainGraphAssertion`
- `DomainGraphAssertionsResponse`
- `Client.DomainGraphAssertions(ctx, req)`
- query string builder
- response validation

### 3.5 Viewer Memory UI

現行パターン:

- `internal/adapter/viewer/assets/js/tabs/memory.js`
  - Source Registry action failure は body 付き `HTTP <status>: ...` を表示する。
  - stale table を使わず unavailable state を出す方向に補強されている。
- `internal/adapter/viewer/viewer_memory_panel.test.mjs`
  - fake DOM による Node contract test がある。

不足:

- Domain Graph assertion 用の state / fetch / render がない。
- summary card / table / evidence details がない。
- fetch failure 時に stale assertion table を消す契約がない。

## 4. 実装範囲

### 4.1 Phase 1 で実装すること

- `L1SQLiteStore.DomainGraphAssertions(ctx, q)` を追加する。
- `GET /viewer/domain-graph/assertions` を追加する。
- L1 store nil 時に 503 `domain graph unavailable` を返す。
- `pkg/rencrowclient.DomainGraphAssertions(ctx, req)` を追加する。
- Viewer Memory tab に最小表示を追加する。
- persistence / handler / client / Viewer Node contract / runtime route test を追加する。

### 4.2 Phase 1 で実装しないこと

- assertion から Movie 固有 `movies` / `people` / `movie_people` への変換。
- `movie_topic_candidates` 生成。
- Hobby Graph 共通 DB の実装。
- Qdrant summary sync。
- graph visualization。
- source registry staging promote UI の `target=domain_graph` 対応。必要なら Phase 1.5 とする。

### 4.3 Phase 2 以降

- Movie adapter: `domain_graph_assertion` から Movie Graph 詳細 DB への反映。
- Topic candidate generator。
- Qdrant summary sync 状態 field。
- Ops readiness に `domain_graph_available` / `domain_graph_status_available` を追加。
- Domain Graph 専用 tab。

## 5. アーキテクチャ

### 5.1 persistence

追加/変更ファイル:

- `internal/infrastructure/persistence/conversation/l1_sqlite_types.go`
- `internal/infrastructure/persistence/conversation/l1_sqlite_domain_graph.go`
- `internal/infrastructure/persistence/conversation/l1_sqlite_store_test.go`

追加型:

```go
type DomainGraphAssertionQuery struct {
	Domain           string
	EntityType       string
	EntityID         string
	RelationType     string
	SourceID         string
	ValidationStatus string
	Limit            int
	Offset           int
}
```

追加関数:

```go
func (s *L1SQLiteStore) DomainGraphAssertions(ctx context.Context, q DomainGraphAssertionQuery) (int, []L1DomainGraphAssertion, error)
```

内部 helper:

```go
func normalizeDomainGraphAssertionQuery(q DomainGraphAssertionQuery) (DomainGraphAssertionQuery, error)
func scanDomainGraphAssertions(rows *sql.Rows) ([]L1DomainGraphAssertion, error)
func validateDomainGraphValidationStatus(status string) error
```

### 5.2 adapter / Viewer API

追加ファイル:

- `internal/adapter/viewer/domain_graph_handler.go`
- `internal/adapter/viewer/domain_graph_handler_test.go`

追加 interface:

```go
type DomainGraphAssertionStore interface {
	DomainGraphAssertions(ctx context.Context, q conversationpersistence.DomainGraphAssertionQuery) (int, []conversationpersistence.L1DomainGraphAssertion, error)
}
```

追加 handler:

```go
func HandleDomainGraphAssertions(store DomainGraphAssertionStore) http.HandlerFunc
```

### 5.3 cmd runtime

変更ファイル:

- `cmd/picoclaw/runtime_dependencies.go`
- `cmd/picoclaw/runtime_viewer_handlers.go`
- `cmd/picoclaw/routes.go`
- `cmd/picoclaw/runtime_viewer_handlers_test.go`

追加 field:

```go
viewerDomainGraphAssertions http.HandlerFunc
```

runtime wiring:

- `l1Store == nil` の場合も `viewer.HandleDomainGraphAssertions(nil)` を登録する。
- `l1Store != nil` の場合は `viewer.HandleDomainGraphAssertions(l1Store)` を登録する。
- route は `/viewer/domain-graph/assertions`。

### 5.4 client

変更ファイル:

- `pkg/rencrowclient/client.go`
- `pkg/rencrowclient/client_test.go`

追加型:

```go
type DomainGraphAssertionsRequest struct {
	Domain           string
	EntityType       string
	EntityID         string
	RelationType     string
	SourceID         string
	ValidationStatus string
	Limit            int
	Offset           int
}

type DomainGraphAssertion struct {
	ID               string         `json:"id"`
	StagingID        string         `json:"staging_id"`
	Domain           string         `json:"domain"`
	EntityType       string         `json:"entity_type"`
	EntityID         string         `json:"entity_id,omitempty"`
	RelationType     string         `json:"relation_type,omitempty"`
	SourceID         string         `json:"source_id"`
	SourceURL        string         `json:"source_url,omitempty"`
	RawHash          string         `json:"raw_hash"`
	Summary          string         `json:"summary"`
	Confidence       float64        `json:"confidence"`
	ValidationStatus string         `json:"validation_status"`
	Evidence         map[string]any `json:"evidence"`
	CreatedAt        string         `json:"created_at"`
	UpdatedAt        string         `json:"updated_at"`
}

type DomainGraphAssertionsResponse struct {
	Items  []DomainGraphAssertion `json:"items"`
	Limit  int                    `json:"limit"`
	Offset int                    `json:"offset"`
	Total  int                    `json:"total"`
}
```

追加 method:

```go
func (c *Client) DomainGraphAssertions(ctx context.Context, req DomainGraphAssertionsRequest) (DomainGraphAssertionsResponse, error)
```

### 5.5 Viewer UI

変更ファイル:

- `internal/adapter/viewer/assets/js/tabs/memory.js`
- `internal/adapter/viewer/viewer.html`
- `internal/adapter/viewer/viewer_memory_panel.test.mjs`

Phase 1 では Memory tab 内に `Domain Graph Assertions` section を追加する。専用 tab は Phase 2。

## 6. データ契約

### 6.1 query DTO

Viewer API query:

| query | default | validation |
| --- | --- | --- |
| `domain` | empty | token normalize |
| `entity_type` | empty | token normalize |
| `entity_id` | empty | trim only |
| `relation_type` | empty | token normalize |
| `source_id` | empty | trim only |
| `validation_status` | `validated` | `pending` / `validated` / `rejected` |
| `limit` | `50` | `1..200`、超過は 200 に丸める |
| `offset` | `0` | `>= 0`、負数は 400 |

### 6.2 response DTO

```json
{
  "items": [],
  "limit": 50,
  "offset": 0,
  "total": 0
}
```

`total` は filter 適用後、limit / offset 適用前の件数。

### 6.3 assertion item

API response は lower snake case とする。

必須:

- `id`
- `staging_id`
- `domain`
- `entity_type`
- `source_id`
- `raw_hash`
- `confidence`
- `validation_status`
- `evidence`
- `created_at`
- `updated_at`

任意:

- `entity_id`
- `relation_type`
- `source_url`
- `summary`

### 6.4 evidence JSON

`evidence` は map として返す。初期実装では redact 処理は行わないが、Viewer では `details` に閉じ、初期表示に展開しない。

禁止:

- raw web text 全文を evidence として表示前提にしない。
- secret / token / Authorization / cookie を evidence に表示する必要が出た場合は停止条件に該当する。

### 6.5 error response

handler error body:

| status | body |
| --- | --- |
| 400 | `invalid domain graph assertion query` |
| 405 | `method not allowed` |
| 503 | `domain graph unavailable` |
| 500 | `failed to load domain graph assertions` |

## 7. Query policy

### 7.1 filters

SQL は optional filter を `WHERE 1=1` へ積む形式でよい。

対象 column:

- `domain`
- `entity_type`
- `entity_id`
- `relation_type`
- `source_id`
- `validation_status`

### 7.2 normalization

`domain` / `entity_type` / `relation_type` は `normalizeDomainGraphToken()` と同じ方針にする。

```text
trim
lower
"-" -> "_"
" " -> "_"
```

`entity_id` / `source_id` は ID 文字列のため trim のみ。

### 7.3 pagination

- `limit <= 0`: 50
- `limit > 200`: 200
- `offset < 0`: error

handler では invalid offset を 400 とする。

### 7.4 default validation_status

空の場合は `validated`。理由は、現行 assertion は validated staging からのみ生成されるため。

`pending` / `rejected` は将来の review current view のために許可するが、現行 Phase 1 では基本的に空結果になる。

### 7.5 sort order

既定:

```sql
ORDER BY created_at DESC, assertion_id DESC
```

## 8. Viewer API 仕様

### 8.1 endpoint

```text
GET /viewer/domain-graph/assertions
```

POST / PUT / DELETE は 405。

### 8.2 success response

`Content-Type: application/json`

```json
{
  "items": [
    {
      "id": "dg:movie:evt:hash",
      "staging_id": "kb:movie:evt:hash",
      "domain": "movie",
      "entity_type": "work",
      "entity_id": "movie:1",
      "relation_type": "performed_by",
      "source_id": "web:eiga",
      "source_url": "https://example.com/movie/1",
      "raw_hash": "hash",
      "summary": "summary",
      "confidence": 0.8,
      "validation_status": "validated",
      "evidence": {"staging_id": "kb:movie:evt:hash"},
      "created_at": "2026-06-06T10:00:00Z",
      "updated_at": "2026-06-06T10:00:00Z"
    }
  ],
  "limit": 50,
  "offset": 0,
  "total": 1
}
```

### 8.3 error response

- invalid query: 400
- persistence error: 500
- unsupported method: 405

### 8.4 unavailable response

L1 store が nil の場合:

```text
503 domain graph unavailable
```

route は存在させる。404 は使わない。

## 9. Client 仕様

### 9.1 request

`Client.DomainGraphAssertions(ctx, req)` は query string を組み立てる。

空 field は query に含めない。`Limit` / `Offset` は 0 の場合は含めなくてよい。

### 9.2 response validation

validation:

- `limit < 0` は error
- `offset < 0` は error
- `total < 0` は error
- item ID duplicate は error
- required string 欠落は error
- `confidence < 0 || confidence > 1` は error
- `validation_status` 不正は error
- `created_at` / `updated_at` は RFC3339 必須

### 9.3 malformed current view rejection

追加 test で次を拒否する。

- duplicate assertion ID
- missing `staging_id`
- missing `domain`
- missing `entity_type`
- missing `source_id`
- missing `raw_hash`
- invalid `validation_status`
- confidence out of range
- invalid `created_at`
- invalid `updated_at`
- negative `total`

query filter と response item の domain 不整合は Phase 1 では拒否しない。横断表示や backend の集計都合を妨げないためである。strict mode が必要になった場合は Phase 2。

## 10. Viewer 表示仕様

### 10.1 summary

Memory tab に `Domain Graph Assertions` section を追加する。

表示:

- total
- current filter
- domain count
- source count
- latest updated time

### 10.2 table

列:

- domain
- entity type
- entity id
- relation
- confidence
- source id
- summary
- created at

source URL は長いため、table の主列では短縮表示し、details 側に full URL を出す。

### 10.3 details

各 row に `details` を置く。

details:

- full source URL
- raw hash
- staging ID
- validation status
- evidence JSON

raw text 全文は出さない。

### 10.4 failure state

fetch failure 時:

- stale rows を残さない。
- status line に `Domain Graph unavailable: HTTP <status>: <body>` を表示する。
- console-only error にしない。

### 10.5 responsive constraints

- URL / raw hash / staging ID は `overflow-wrap:anywhere` 相当で折り返す。
- table が narrow 幅で押し広がる場合は cards / compact rows に切り替える。
- evidence JSON は初期展開しない。

## 11. Logs / Evidence

新規一覧 API 自体は read-only のため event log 追加は必須ではない。

ただし、handler test / client test では次を E2E 証跡として扱う。

- 2xx response
- non-empty item
- item required fields
- timestamp validation
- confidence validation
- `validation_status=validated`

promote の証跡は既存 `domain_graph.promoted_from_staging` event を使う。

## 12. Tests

### 12.1 Persistence

追加 test:

```go
func TestL1SQLiteStore_DomainGraphAssertionsFiltersAndPagination(t *testing.T)
func TestL1SQLiteStore_DomainGraphAssertionsRejectsNegativeOffset(t *testing.T)
```

確認:

- promoted assertion を domain で検索できる。
- `entity_type` / `entity_id` / `relation_type` / `source_id` で絞り込める。
- default `validation_status` が `validated`。
- `limit > 200` が 200 に丸められる。
- negative offset は error。
- evidence JSON が roundtrip する。

実行:

```bash
GOCACHE=/tmp/picoclaw-go-cache go test ./internal/infrastructure/persistence/conversation
```

### 12.2 Handler

追加 test:

```go
func TestHandleDomainGraphAssertionsReturnsCurrentView(t *testing.T)
func TestHandleDomainGraphAssertionsUnavailable(t *testing.T)
func TestHandleDomainGraphAssertionsRejectsInvalidQuery(t *testing.T)
```

確認:

- `GET /viewer/domain-graph/assertions?domain=movie` が JSON を返す。
- store nil は 503 `domain graph unavailable`。
- invalid offset は 400。
- response は raw text を含まない。

実行:

```bash
GOCACHE=/tmp/picoclaw-go-cache go test ./internal/adapter/viewer
```

### 12.3 Client

追加 test:

```go
func TestDomainGraphAssertionsCurrentView(t *testing.T)
func TestDomainGraphAssertionsRejectsMalformedCurrentView(t *testing.T)
func TestDomainGraphAssertionsBuildsQuery(t *testing.T)
```

確認:

- valid response を受け取れる。
- duplicate ID を拒否する。
- missing required field を拒否する。
- invalid timestamp / confidence / status を拒否する。
- query string が期待通り。

実行:

```bash
GOCACHE=/tmp/picoclaw-go-cache go test ./pkg/rencrowclient
```

### 12.4 Viewer Node contract

追加 test:

```js
test('viewer renders domain graph assertions without raw text')
test('viewer clears stale domain graph assertions on fetch failure')
```

確認:

- total / domain count / latest rows が出る。
- evidence は details に閉じる。
- source URL が折り返し可能な class を持つ。
- fetch failure で stale rows を残さない。

実行:

```bash
node --test internal/adapter/viewer/viewer_memory_panel.test.mjs
```

### 12.5 Runtime route

追加 test:

```go
func TestBuildViewerRuntimeHandlersRegistersDomainGraphUnavailableHandler(t *testing.T)
```

確認:

- L1 store nil でも `viewerDomainGraphAssertions` は nil ではない。
- handler は 503 `domain graph unavailable` を返す。

実行:

```bash
GOCACHE=/tmp/picoclaw-go-cache go test ./cmd/picoclaw
```

### 12.6 Live manual check

L1 store が有効な runtime でのみ実施する。

```bash
curl -sS 'http://127.0.0.1:18790/viewer/domain-graph/assertions?domain=movie&limit=10'
```

確認:

- 200 または L1 disabled 時の 503 を明確に区別する。
- 503 は blocked state であり成功扱いしない。

## 13. 実装手順

1. `l1_sqlite_types.go` に `DomainGraphAssertionQuery` を追加する。
2. `l1_sqlite_domain_graph.go` に `DomainGraphAssertions()` と scan helper を追加する。
3. persistence test を追加し、先に失敗を確認する。
4. `domain_graph_handler.go` を追加する。
5. handler test を追加する。
6. `runtime_dependencies.go` / `runtime_viewer_handlers.go` / `routes.go` を接続する。
7. runtime handler test を追加する。
8. `pkg/rencrowclient/client.go` に request / response / method / validation を追加する。
9. client test を追加する。
10. Memory tab に最小 UI を追加する。
11. Viewer Node contract test を追加する。
12. 対象 test を実行する。

## 14. 完了条件

- validated staging から promote した assertion が `GET /viewer/domain-graph/assertions` で見える。
- `domain` / `entity_type` / `entity_id` / `relation_type` / `source_id` で絞り込める。
- `limit` / `offset` が効く。
- L1 store 無効時は 503 `domain graph unavailable`。
- `pkg/rencrowclient` が malformed current view を拒否する。
- Viewer が fetch failure を stale table で隠さない。
- raw text / pending staging / Qdrant sync と assertion current view を混同しない。
- 以下が通る。

```bash
GOCACHE=/tmp/picoclaw-go-cache go test ./internal/infrastructure/persistence/conversation ./internal/adapter/viewer ./pkg/rencrowclient ./cmd/picoclaw
node --test internal/adapter/viewer/viewer_memory_panel.test.mjs
```

## 15. 停止条件

次に該当する場合は実装を停止し、仕様または設計を見直す。

- `domain_graph_assertion` schema と現行 code が矛盾している。
- L1 store の無効時 handler pattern が既存 runtime と合わない。
- Viewer に追加することで Memory / Source Registry の既存 UI が大きく崩れる。
- evidence JSON に secret / token / raw web text 全文を表示する必要が出る。
- Qdrant sync の状態を同時実装しないと誤表示を避けられない。
- `domain_graph_assertion` を read-only current view として返すだけでは、ユーザーが Qdrant / Movie DB 反映済みと誤認する表示になる。

## 16. 将来拡張

- Source Registry staging UI から `target=domain_graph` promote を操作できるようにする。
- Domain Graph 専用 tab を作る。
- Movie Graph adapter を追加し、assertion から `movie_topic_candidates` を生成する。
- Hobby Graph 共通 items / relations / interactions へ展開する。
- Qdrant summary sync と sync status 表示を追加する。
- assertion conflict review UI を追加する。
