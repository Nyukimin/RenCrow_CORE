# Domain Graph Movie Adapter Work 同期実装仕様

## 1. 目的

`domain_graph_assertion` に保存された Movie domain の work assertion を、Movie catalog DB の `movies` table へ同期する最小 adapter を実装する。

これまでに、Source Registry staging から `target=domain_graph` へ promote し、`GET /viewer/domain-graph/assertions` で assertion current view を確認できる経路は実装済みである。一方、Movie Database tab が参照する `movies` / `people` / `movie_people` には assertion が反映されないため、ユーザーには「Domain Graph に入ったが Movie DB には出ない」状態になる。

この仕様では、まず Movie domain の `entity_type=work` assertion を `movies` へ upsert する read/write adapter を追加し、Viewer API から明示実行できるようにする。

## 2. 参考仕様

- `docs/01_正本仕様/実装仕様.md`
- `docs/10_新仕様/49_Movie_Graph_Mio_Topic仕様.md`
- `docs/10_新仕様/52_Domain_Graph_DB経路実装仕様.md`
- `docs/10_新仕様/55_Domain_Graph_Assertion一覧検索API詳細実装仕様.md`
- `docs/10_新仕様/56_SourceRegistry_DomainGraph_Promote_UI実装仕様.md`

## 3. 現行実装調査

### 3.1 Domain Graph side

実装済み:

- `internal/infrastructure/persistence/conversation/l1_sqlite_domain_graph.go`
  - `DomainGraphAssertions(ctx, q)` が存在する。
  - `domain=movie` / `entity_type=work` / `validation_status=validated` で絞り込める。
- `internal/adapter/viewer/domain_graph_handler.go`
  - `GET /viewer/domain-graph/assertions` が存在する。

不足:

- assertion を Movie catalog DB へ反映する adapter がない。
- 同期結果を追跡する endpoint がない。

### 3.2 Movie catalog side

実装済み:

- `internal/adapter/viewer/movie_catalog_handler.go`
  - `HandleMovieCatalog`
  - `HandleMovieCatalogFetch`
  - `HandleMovieCatalogPreference`
  - `MovieCatalogOptions{DBPath string}`
  - `movies` / `people` / `movie_people` を read する。
- `RenCrow_Tools/tools/eiga_catalog/eiga_catalog.py`
  - `movies(movie_id,title,url,synopsis)` を作成する。
  - `INSERT OR REPLACE INTO movies(movie_id,title,url,synopsis)` を使う。

不足:

- Go 側で `movies` table を作る helper がない。
- Domain Graph assertion から `movies` へ upsert する helper がない。
- Viewer API から同期を明示実行する endpoint がない。

## 4. 実装範囲

### 4.1 実装すること

- Movie catalog DB に `movies` table がない場合は作成する。
- `domain_graph_assertion` の validated movie/work assertion を `movies` へ upsert する。
- Viewer API `POST /viewer/movie-catalog/domain-graph-sync` を追加する。
- sync response に件数、skip 理由、upsert した movie IDs を返す。
- handler / runtime route / tests を追加する。

### 4.2 実装しないこと

- `people` table への人物同期。
- `movie_people` edge 同期。
- `movie_topic_candidates` 生成。
- Qdrant summary sync。
- 自動定期 sync。
- Domain Graph 専用 tab。

## 5. データ変換仕様

### 5.1 対象 assertion

対象:

```text
domain = movie
entity_type = work
validation_status = validated
```

対象外:

- `domain != movie`
- `entity_type != work`
- `validation_status != validated`
- `entity_id` が空
- `summary` と `source_url` が両方空

### 5.2 movie_id

`movies.movie_id` は assertion の `entity_id` を使う。

例:

| assertion.entity_id | movies.movie_id |
| --- | --- |
| `movie:57573` | `movie:57573` |
| `57573` | `57573` |

Phase 1 では `movie:` prefix を剥がさない。理由は、Domain Graph 側の ID 設計を壊さないためである。既存映画.com catalog の numeric ID へ正規化する処理は、次段の Movie ID resolver で扱う。

### 5.3 title

title の決定順:

1. `evidence.title`
2. `evidence.movie_title`
3. `summary`
4. `entity_id`

空にはしない。

### 5.4 url

url の決定順:

1. `source_url`
2. `evidence.source_url`
3. 空文字

### 5.5 synopsis

synopsis は `summary` を使う。summary が空の場合は空文字。

### 5.6 duplicate

同じ `movie_id` が複数 assertion から来る場合:

- `updated_at` が新しい assertion を優先する。
- 同一時刻なら後で走査した assertion で上書きしてよい。

SQLite は `INSERT OR REPLACE` ではなく、既存の watch / relation 周辺への影響を避けるため `INSERT ... ON CONFLICT(movie_id) DO UPDATE` を使う。

## 6. API 仕様

### 6.1 endpoint

```text
POST /viewer/movie-catalog/domain-graph-sync
```

GET は 405。

### 6.2 request

Phase 1 は body なしでよい。

任意 query:

| query | default | meaning |
| --- | --- | --- |
| `limit` | `200` | assertion 取得上限。最大 `500` |

### 6.3 success response

```json
{
  "available": true,
  "db_path": "/path/to/eiga_catalog.sqlite",
  "domain": "movie",
  "entity_type": "work",
  "checked": 3,
  "upserted": 2,
  "skipped": 1,
  "movie_ids": ["movie:1", "movie:2"],
  "skip_reasons": {
    "missing_entity_id": 1
  }
}
```

### 6.4 unavailable response

Movie catalog writable path を作れない、または L1 store が nil の場合は 503。

body:

```text
movie domain graph sync unavailable
```

### 6.5 error response

| status | body |
| --- | --- |
| 400 | `invalid movie domain graph sync request` |
| 405 | `method not allowed` |
| 500 | `failed to sync movie domain graph assertions` |

## 7. 実装設計

### 7.1 追加/変更ファイル

- `internal/adapter/viewer/movie_catalog_domain_graph_sync.go`
- `internal/adapter/viewer/movie_catalog_domain_graph_sync_test.go`
- `cmd/picoclaw/runtime_dependencies.go`
- `cmd/picoclaw/runtime_viewer_handlers.go`
- `cmd/picoclaw/routes.go`
- `cmd/picoclaw/runtime_viewer_handlers_test.go`

### 7.2 store interface

```go
type MovieDomainGraphAssertionStore interface {
  DomainGraphAssertions(ctx context.Context, q conversationpersistence.DomainGraphAssertionQuery) (int, []conversationpersistence.L1DomainGraphAssertion, error)
}
```

### 7.3 sync result

```go
type movieDomainGraphSyncResult struct {
  Available   bool           `json:"available"`
  DBPath      string         `json:"db_path"`
  Domain      string         `json:"domain"`
  EntityType  string         `json:"entity_type"`
  Checked     int            `json:"checked"`
  Upserted    int            `json:"upserted"`
  Skipped     int            `json:"skipped"`
  MovieIDs    []string       `json:"movie_ids"`
  SkipReasons map[string]int `json:"skip_reasons"`
}
```

### 7.4 helper

```go
func HandleMovieDomainGraphSync(opts MovieCatalogOptions, store MovieDomainGraphAssertionStore) http.HandlerFunc
func syncMovieDomainGraphAssertions(ctx context.Context, db *sql.DB, items []conversationpersistence.L1DomainGraphAssertion) (movieDomainGraphSyncResult, error)
func ensureMovieCatalogWorkTables(ctx context.Context, db *sql.DB) error
func movieCatalogWorkFromAssertion(item conversationpersistence.L1DomainGraphAssertion) (movieCatalogWorkUpsert, string)
```

skip reason:

- `missing_entity_id`
- `empty_work_payload`

## 8. runtime wiring

`buildViewerRuntimeHandlers(...)` で次を登録する。

- `l1Store == nil`: unavailable handler
- `l1Store != nil`: `viewer.HandleMovieDomainGraphSync(viewer.MovieCatalogOptions{}, l1Store)`

`routes.go`:

```go
mux.HandleFunc("/viewer/movie-catalog/domain-graph-sync", dependencies.viewerMovieDomainGraphSync)
```

## 9. テスト仕様

### 9.1 adapter test

追加 test:

```go
func TestHandleMovieDomainGraphSyncUpsertsMovieWorks(t *testing.T)
func TestHandleMovieDomainGraphSyncUnavailable(t *testing.T)
func TestHandleMovieDomainGraphSyncRejectsInvalidMethod(t *testing.T)
```

確認:

- validated movie/work assertion が `movies` に upsert される。
- response が `checked` / `upserted` / `movie_ids` を返す。
- `raw_text` は使わない。
- `entity_id` 空は skip される。
- store nil は 503。
- GET は 405。

### 9.2 runtime route test

既存 `runtime_viewer_handlers_test.go` に route registration または unavailable handler test を追加する。

### 9.3 実行コマンド

```bash
GOCACHE=/tmp/picoclaw-go-cache go test ./internal/adapter/viewer ./cmd/picoclaw
```

## 10. 完了条件

- `POST /viewer/movie-catalog/domain-graph-sync` が存在する。
- Movie domain work assertion が `movies` へ upsert される。
- `entity_id` が空の assertion は skip される。
- L1 store nil 時は 503。
- GET は 405。
- runtime route が登録される。
- 既存 Movie catalog read / fetch / preference API が壊れていない。
- 以下が通る。

```bash
GOCACHE=/tmp/picoclaw-go-cache go test ./internal/adapter/viewer ./cmd/picoclaw
git diff --check
```

## 11. 停止条件

- `movies` schema が現行 `RenCrow_Tools/tools/eiga_catalog/eiga_catalog.py` と矛盾する。
- assertion の `evidence` に secret / token / raw web text 全文を取り込む必要が出る。
- numeric movie ID 正規化を同時に行わないと既存 Movie DB と破壊的に衝突する。
- `movie_people` edge 同期まで同時にやらないと UI が誤表示になる。

## 12. 次段候補

- Movie ID resolver: `movie:57573` と `57573` の正規化。
- person assertion を `people` へ同期する。
- relation assertion を `movie_people` へ同期する。
- `movie_topic_candidates` を生成する。
