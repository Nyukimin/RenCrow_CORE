# Hobby Graph Common DB Bootstrap 実装仕様

## 1. 目的

`50_Hobby_Graph_Mio_Topic仕様.md` の共通 DB schema を、RenCrow Viewer API から初期化・確認できるようにする。

Movie Graph は映画.com catalog 専用 table を持つが、映画以外の趣味領域では `hobby_items` / `hobby_relations` / `hobby_interactions` などの共通 table が必要である。現状は仕様だけがあり、DB を作る入口がない。

この仕様では、Hobby Graph の最初の実装単位として、共通 table の bootstrap API と stats current view を追加する。

## 2. 参考仕様

- `docs/10_新仕様/50_Hobby_Graph_Mio_Topic仕様.md`
- `docs/10_新仕様/55_Domain_Graph_Assertion一覧検索API詳細実装仕様.md`

## 3. 実装範囲

### 3.1 実装すること

- Hobby Graph SQLite DB path resolver を追加する。
- `POST /viewer/hobby-graph/bootstrap` を追加する。
- `GET /viewer/hobby-graph?action=stats` を追加する。
- 仕様 50 の共通 table を `CREATE TABLE IF NOT EXISTS` で作成する。
- stats response で各 table count を返す。
- route と handler test を追加する。

### 3.2 実装しないこと

- item / relation / interaction の登録 API。
- Domain Graph assertion から Hobby Graph への同期。
- Hobby topic candidate 生成。
- Viewer UI 追加。
- Qdrant sync。

## 4. DB path

解決順:

1. env `PICOCLAW_HOBBY_GRAPH_DB`
2. `HobbyGraphOptions.DBPath`
3. `tmp/hobby_graph/hobby_graph.sqlite`

`GET /viewer/hobby-graph?action=stats` は DB がない場合、既存 Movie catalog と同じ soft unavailable とする。

`POST /viewer/hobby-graph/bootstrap` は writable path を使い、親 directory を作成する。

## 5. DB schema

作成対象:

- `hobby_items`
- `hobby_relations`
- `hobby_interactions`
- `hobby_title_observations`
- `hobby_preference_signals`
- `hobby_topic_candidates`
- `hobby_collection_runs`
- `hobby_collection_targets`

schema は `50_Hobby_Graph_Mio_Topic仕様.md` に従う。

## 6. API

### 6.1 Bootstrap

```text
POST /viewer/hobby-graph/bootstrap
```

success:

```json
{
  "available": true,
  "db_path": "tmp/hobby_graph/hobby_graph.sqlite",
  "action": "bootstrap",
  "stats": {
    "hobby_items": 0
  }
}
```

GET は 405。

### 6.2 Stats

```text
GET /viewer/hobby-graph?action=stats
```

DB が存在する場合:

```json
{
  "available": true,
  "db_path": "...",
  "action": "stats",
  "stats": {
    "hobby_items": 0,
    "hobby_relations": 0
  }
}
```

DB が存在しない場合:

```json
{
  "available": false,
  "db_path": "...",
  "action": "stats",
  "error": "hobby graph database not found"
}
```

## 7. 実装設計

追加ファイル:

- `internal/adapter/viewer/hobby_graph_handler.go`
- `internal/adapter/viewer/hobby_graph_handler_test.go`

変更ファイル:

- `cmd/picoclaw/routes.go`

追加関数:

```go
type HobbyGraphOptions struct { DBPath string }
func HandleHobbyGraph(opts HobbyGraphOptions) http.HandlerFunc
func HandleHobbyGraphBootstrap(opts HobbyGraphOptions) http.HandlerFunc
func ensureHobbyGraphTables(ctx context.Context, db *sql.DB) error
func hobbyGraphStats(db *sql.DB) (map[string]int, error)
```

## 8. テスト仕様

- `TestHandleHobbyGraphBootstrapCreatesCommonTables`
- `TestHandleHobbyGraphMissingDBIsSoftUnavailable`
- `TestHandleHobbyGraphRejectsUnsupportedAction`
- `TestHandleHobbyGraphBootstrapRejectsInvalidMethod`

## 9. 検証コマンド

```bash
GOCACHE=/tmp/picoclaw-go-cache go test ./internal/adapter/viewer ./cmd/picoclaw
git diff --check
```

## 10. 完了条件

- Hobby Graph 共通 DB を API から bootstrap できる。
- stats current view で table count が見える。
- 未作成 DB は soft unavailable として見える。
- Movie Graph / Domain Graph 既存 route を壊さない。
