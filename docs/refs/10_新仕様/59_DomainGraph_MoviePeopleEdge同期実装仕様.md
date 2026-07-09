# Domain Graph Movie People Edge 同期実装仕様

## 1. 目的

Domain Graph の Movie relation assertion を、Movie catalog DB の `movie_people` table へ同期する。

`57_DomainGraph_MovieAdapter_Work同期実装仕様.md` で作品 `movies` への同期を実装したが、人物との関係 edge はまだ同期されない。そのため、Viewer の Movie Database では作品が見えても「出演者・監督・スタッフとの関係」が出ず、Mio の話題候補生成に必要な関係事実も不足する。

この仕様では、Domain Graph のうち `entity_type=work_person` を `movie_people` へ同期する最小 adapter を追加する。

## 2. 参考仕様

- `docs/01_正本仕様/実装仕様.md`
- `docs/10_新仕様/49_Movie_Graph_Mio_Topic仕様.md`
- `docs/10_新仕様/57_DomainGraph_MovieAdapter_Work同期実装仕様.md`
- `docs/10_新仕様/58_DomainGraph_MovieIDResolver実装仕様.md`

## 3. 実装範囲

### 3.1 実装すること

- `POST /viewer/movie-catalog/domain-graph-sync` で work 同期に続けて relation edge も同期する。
- `domain_graph_assertion` から `domain=movie` / `entity_type=work_person` / `validation_status=validated` を取得する。
- `movie_people` table がなければ作成する。
- assertion evidence から person ID / person name / movie title / URL を抽出する。
- `movie_people` へ `INSERT ... ON CONFLICT(movie_id, person_id, role, source) DO UPDATE` する。
- response に relation 件数と skip 理由を追加する。

### 3.2 実装しないこと

- `people` table 本体への人物 upsert。
- 人物 ID resolver。
- `movie_topic_candidates` 生成。
- Qdrant sync。
- Domain Graph assertion の書き換え。

## 4. 対象 assertion

対象:

```text
domain = movie
entity_type = work_person
validation_status = validated
```

対象外:

- `domain != movie`
- `entity_type != work_person`
- `validation_status != validated`
- movie ID が空
- person ID が空

## 5. データ変換

### 5.1 movie_id

決定順:

1. `evidence.movie_id`
2. `entity_id`

`movie:57573` 形式、または映画.com URL から得られる numeric ID は、`58_DomainGraph_MovieIDResolver実装仕様.md` と同じ resolver を通す。

### 5.2 person_id

決定順:

1. `evidence.person_id`
2. `evidence.target_person_id`
3. `evidence.object_id`

`person:30003` 形式の場合は `30003` へ正規化してよい。人物 ID は映画.com numeric ID を優先するが、未解決の場合は raw ID を残す。

### 5.3 role

決定順:

1. `evidence.role`
2. `relation_type`
3. `関係`

`relation_type` は Domain Graph 側で lower snake になりやすいため、`actor` / `cast` は `出演`、`director` は `監督`、`staff` は `スタッフ` へ表示用正規化する。

### 5.4 source

`source = domain_graph` とする。

### 5.5 optional fields

| movie_people column | 決定順 |
| --- | --- |
| `movie_title` | `evidence.movie_title` → `evidence.title` → `summary` |
| `person_name` | `evidence.person_name` → `evidence.name` → `evidence.target_label` → `person_id` |
| `movie_url` | `evidence.movie_url` → `source_url` → `evidence.source_url` |
| `person_url` | `evidence.person_url` |

## 6. response 追加

`POST /viewer/movie-catalog/domain-graph-sync` の response に次を追加する。

```json
{
  "relation_checked": 2,
  "relation_upserted": 1,
  "relation_skipped": 1,
  "relation_skip_reasons": {
    "missing_person_id": 1
  }
}
```

`checked` / `upserted` / `skipped` は従来どおり work assertion 用として維持する。

## 7. 実装設計

変更ファイル:

- `internal/adapter/viewer/movie_catalog_domain_graph_sync.go`
- `internal/adapter/viewer/movie_catalog_domain_graph_sync_test.go`
- `docs/10_新仕様/59_DomainGraph_MoviePeopleEdge同期実装仕様.md`

追加 helper:

```go
func ensureMovieCatalogPeopleEdgeTables(ctx context.Context, db *sql.DB) error
func syncMovieDomainGraphRelationAssertions(ctx context.Context, db *sql.DB, items []conversationpersistence.L1DomainGraphAssertion) (movieDomainGraphRelationSyncResult, error)
func movieCatalogPeopleEdgeFromAssertion(item conversationpersistence.L1DomainGraphAssertion) (movieCatalogPeopleEdgeUpsert, string)
func normalizeMovieCatalogPersonID(raw string) string
func normalizeMovieCatalogRole(raw string) string
```

runtime は既存 `POST /viewer/movie-catalog/domain-graph-sync` の中で second query を行う。

## 8. テスト仕様

追加 test:

- `TestHandleMovieDomainGraphSyncUpsertsMoviePeopleEdges`

確認:

- work query と relation query の両方が呼ばれる。
- relation query は `Domain=movie`, `EntityType=work_person`, `ValidationStatus=validated`。
- evidence から `person_id`, `person_name`, `role` を読み、`movie_people` に保存される。
- `movie_id=movie:57573` は既存 catalog row がある場合 `57573` へ解決される。
- person ID `person:30003` は `30003` へ正規化される。
- person ID 欠落 assertion は skip される。

## 9. 検証コマンド

```bash
GOCACHE=/tmp/rencrow-go-cache go test ./internal/adapter/viewer
git diff --check
```

## 10. 完了条件

- Domain Graph 由来の Movie work-person relation が `movie_people` に入る。
- 作品同期、ID resolver、既存 Movie catalog read API を壊さない。
- 人物 catalog 本体の取得済み/未取得は混同しない。
- skip 理由が response で追跡できる。
