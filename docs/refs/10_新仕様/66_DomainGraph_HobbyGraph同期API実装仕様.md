# Domain Graph Hobby Graph 同期 API 実装仕様

## 1. 目的

`domain_graph_assertion` に保存された Movie 以外の趣味ドメイン assertion を、Hobby Graph 共通 DB の `hobby_items` / `hobby_relations` へ同期する最小 adapter を実装する。

Movie domain では `POST /viewer/movie-catalog/domain-graph-sync` により Domain Graph assertion を Movie catalog DB へ反映できる。一方、漫画・小説・音楽・ゲームなどの assertion は、Hobby Graph DB に反映する入口がまだない。

この仕様では、まず `entity_type=work` と `entity_type=work_relation` の validated assertion を Hobby Graph に同期する read/write adapter を追加し、Viewer API から明示実行できるようにする。

## 2. 参考仕様

- `docs/01_正本仕様/実装仕様.md`
- `docs/10_新仕様/50_Hobby_Graph_Mio_Topic仕様.md`
- `docs/10_新仕様/52_Domain_Graph_DB経路実装仕様.md`
- `docs/10_新仕様/55_Domain_Graph_Assertion一覧検索API詳細実装仕様.md`
- `docs/10_新仕様/57_DomainGraph_MovieAdapter_Work同期実装仕様.md`
- `docs/10_新仕様/64_HobbyGraph_Relation登録API実装仕様.md`

## 3. 実装範囲

### 3.1 実装すること

- `POST /viewer/hobby-graph/domain-graph-sync` を追加する。
- `domain_graph_assertion` から validated assertion を取得する。
- `entity_type=work` を `hobby_items` へ upsert する。
- `entity_type=work_relation` を `hobby_items` と `hobby_relations` へ upsert する。
- Hobby Graph DB がなければ writable path に作成し、共通 table を作成する。
- response に item / relation の checked、upserted、skipped、skip reason、item IDs を返す。

### 3.2 実装しないこと

- Movie domain の同期。Movie は既存 Movie adapter を使う。
- `hobby_interactions` の自動生成。
- `hobby_topic_candidates` の自動生成。
- Qdrant sync。
- 自動定期 sync。
- Domain Graph assertion の書き換え。
- fuzzy title resolver。

## 4. 対象 assertion

### 4.1 item assertion

対象:

```text
domain = query domain または全 domain
entity_type = work
validation_status = validated
domain != movie
```

対象外:

- `domain=movie`
- `entity_id` が空
- `summary` / evidence title / source URL がすべて空

### 4.2 relation assertion

対象:

```text
domain = query domain または全 domain
entity_type = work_relation
validation_status = validated
domain != movie
```

対象外:

- `domain=movie`
- source entity ID が空
- target entity ID が空
- relation_type が空

## 5. データ変換仕様

### 5.1 category

`category` は assertion `domain` を `normalizeHobbyGraphToken` した値を使う。

### 5.2 item_id

Domain Graph 由来 item の stable ID:

```text
hobby_item:{sha1(category,item_type,domain_graph_entity_id)[:16]}
```

interaction API の title-derived item ID とは別扱いにする。未解決 title resolver はこの実装範囲に含めない。

### 5.3 item_type

item assertion:

- `entity_type=work` は `item_type=work`。

relation assertion:

- source item type: `evidence.source_item_type` → `work`
- target item type: `evidence.target_item_type` → relation_type 由来 mapping → `related`

relation_type 由来 mapping:

| relation_type | target item_type |
| --- | --- |
| `created_by` | `creator` |
| `performed_by` | `artist` |
| `directed_by` | `person` |
| `published_by` | `publisher` |
| `developed_by` | `studio` |
| `part_of_series` | `series` |
| その他 | `related` |

### 5.4 title

item title の決定順:

1. `evidence.title`
2. `evidence.work_title`
3. `evidence.source_title`
4. `summary`
5. `entity_id`

relation source title:

1. `evidence.source_title`
2. `evidence.work_title`
3. `evidence.title`
4. `summary`
5. source entity ID

relation target title:

1. `evidence.target_title`
2. `evidence.target_label`
3. `evidence.creator_name`
4. `evidence.author_name`
5. `evidence.person_name`
6. `evidence.name`
7. target entity ID

### 5.5 relation source / target entity ID

source entity ID:

1. `evidence.source_item_id`
2. `evidence.from_item_id`
3. `evidence.work_id`
4. `entity_id`

target entity ID:

1. `evidence.target_item_id`
2. `evidence.to_item_id`
3. `evidence.object_id`
4. `evidence.creator_id`
5. `evidence.author_id`
6. `evidence.person_id`

### 5.6 relation_id

```text
hobby_relation:{sha1(from_item_id,to_item_id,relation_type,source)[:16]}
```

`source` は `domain_graph` とする。

### 5.7 evidence_json

`hobby_relations.evidence_json` には次を含める。

- `assertion_id`
- `domain`
- `entity_id`
- `relation_type`
- `source_url`
- `summary`
- `raw_evidence`

## 6. API 仕様

### 6.1 endpoint

```text
POST /viewer/hobby-graph/domain-graph-sync
```

GET は 405。

### 6.2 query

| query | default | max / note |
| --- | --- | --- |
| `domain` | empty | empty は全 domain。`movie` は同期時に skip |
| `limit` | `200` | max `500` |

### 6.3 success response

```json
{
  "available": true,
  "db_path": "tmp/hobby_graph/hobby_graph.sqlite",
  "domain": "",
  "entity_type": "work",
  "checked": 2,
  "upserted": 1,
  "skipped": 1,
  "item_ids": ["hobby_item:..."],
  "skip_reasons": {
    "movie_domain": 1
  },
  "relation_checked": 1,
  "relation_upserted": 1,
  "relation_skipped": 0,
  "relation_skip_reasons": {}
}
```

### 6.4 unavailable response

L1 store が nil、または writable DB path を作れない場合は 503。

body:

```text
hobby domain graph sync unavailable
```

## 7. 実装設計

追加ファイル:

- `internal/adapter/viewer/hobby_domain_graph_sync.go`
- `internal/adapter/viewer/hobby_domain_graph_sync_test.go`

変更ファイル:

- `cmd/picoclaw/runtime_dependencies.go`
- `cmd/picoclaw/runtime_viewer_handlers.go`
- `cmd/picoclaw/routes.go`

追加関数:

```go
type HobbyDomainGraphAssertionStore interface {
  DomainGraphAssertions(ctx context.Context, q conversationpersistence.DomainGraphAssertionQuery) (int, []conversationpersistence.L1DomainGraphAssertion, error)
}

func HandleHobbyDomainGraphSync(opts HobbyGraphOptions, store HobbyDomainGraphAssertionStore) http.HandlerFunc
func syncHobbyDomainGraphItemAssertions(ctx context.Context, db *sql.DB, items []conversationpersistence.L1DomainGraphAssertion) (hobbyDomainGraphItemSyncResult, error)
func syncHobbyDomainGraphRelationAssertions(ctx context.Context, db *sql.DB, items []conversationpersistence.L1DomainGraphAssertion) (hobbyDomainGraphRelationSyncResult, error)
```

runtime は `buildViewerRuntimeHandlers` で L1 store の有無に応じて handler を登録する。

## 8. テスト仕様

追加 test:

- `TestHandleHobbyDomainGraphSyncUpsertsWorkItems`
- `TestHandleHobbyDomainGraphSyncUpsertsWorkRelations`
- `TestHandleHobbyDomainGraphSyncUnavailable`
- `TestHandleHobbyDomainGraphSyncRejectsInvalidMethod`
- `TestHandleHobbyDomainGraphSyncRejectsInvalidLimit`

確認:

- work query と work_relation query の両方が呼ばれる。
- query は `ValidationStatus=validated` である。
- `domain=movie` assertion は skip される。
- work assertion から `hobby_items` に保存される。
- relation assertion から source / target item と `hobby_relations` が保存される。
- relation evidence に assertion ID と raw evidence が残る。

## 9. 検証コマンド

```bash
GOCACHE=/tmp/picoclaw-go-cache go test ./internal/adapter/viewer ./cmd/picoclaw
git diff --check
```

## 10. 完了条件

- Domain Graph 由来の Hobby work が `hobby_items` に入る。
- Domain Graph 由来の Hobby work relation が `hobby_relations` に入る。
- Movie domain は Hobby Graph sync で処理しない。
- Domain Graph assertion、Hobby item、Hobby relation の境界が混ざらない。
- skip 理由が response で追跡できる。
