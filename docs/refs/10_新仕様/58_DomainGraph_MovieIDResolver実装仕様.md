# Domain Graph Movie ID Resolver 実装仕様

## 1. 目的

`57_DomainGraph_MovieAdapter_Work同期実装仕様.md` で実装した `POST /viewer/movie-catalog/domain-graph-sync` に、Movie ID resolver を追加する。

Phase 1 では `domain_graph_assertion.entity_id = movie:57573` をそのまま `movies.movie_id` へ保存した。しかし既存の映画.com catalog は `57573` のような数値 ID を正本 ID として持つ。そのままでは Viewer の Movie Database に同一作品が `57573` と `movie:57573` の 2 行として現れる。

この仕様では、既存 catalog に数値 ID が存在する場合だけ `movie:{digits}` を数値 ID へ解決し、重複行を避ける。

## 2. 参考仕様

- `docs/01_正本仕様/実装仕様.md`
- `docs/10_新仕様/49_Movie_Graph_Mio_Topic仕様.md`
- `docs/10_新仕様/51_Movie_Watch_Event実装仕様.md`
- `docs/10_新仕様/57_DomainGraph_MovieAdapter_Work同期実装仕様.md`

## 3. 現行実装

実装済み:

- `POST /viewer/movie-catalog/domain-graph-sync`
- `domain=movie` / `entity_type=work` / `validation_status=validated` の assertion 取得
- `movies` table 作成
- assertion から `movies` への upsert

不足:

- `movie:57573` と `57573` の同一作品判定。
- 解決結果の追跡。
- 解決済み alias の台帳。

## 4. 実装範囲

### 4.1 実装すること

- `movie:{digits}` 形式の assertion entity ID を解決する。
- `source_url` または `evidence.source_url` が `https://eiga.com/movie/{digits}/` の場合も候補 ID として使う。
- 既存 `movies` table に数値 ID の行がある場合だけ、その数値 ID を canonical ID とする。
- 解決した alias を `movie_id_aliases` table に保存する。
- sync response に `resolved_movie_ids` を返す。
- handler test を追加する。

### 4.2 実装しないこと

- 既存 DB 全体の migration / merge。
- `movie_people` / `movie_watch_events` の既存行 rewrite。
- 未取得 numeric ID の強制 canonical 化。
- 人物 ID resolver。
- Source Registry / Domain Graph assertion 自体の ID 書き換え。

## 5. DB schema

Movie catalog DB に次を追加してよい。

```sql
CREATE TABLE IF NOT EXISTS movie_id_aliases (
  alias_id TEXT PRIMARY KEY,
  canonical_movie_id TEXT NOT NULL,
  source TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

意味:

| column | meaning |
| --- | --- |
| `alias_id` | `movie:57573` など、Domain Graph / 外部経路側の ID |
| `canonical_movie_id` | `movies.movie_id` として使う ID |
| `source` | `domain_graph_sync` |
| `created_at` | 初回保存時刻 |
| `updated_at` | 更新時刻 |

## 6. 解決ルール

### 6.1 canonical 候補

候補順:

1. `entity_id` が `movie:{digits}` なら `{digits}`
2. `source_url` が `https://eiga.com/movie/{digits}/` なら `{digits}`
3. `evidence.source_url` が `https://eiga.com/movie/{digits}/` なら `{digits}`

### 6.2 canonical 採用条件

候補 ID を canonical として採用する条件:

- `movies` table に `movie_id = {digits}` の既存行がある。

存在しない場合は、従来どおり raw `entity_id` を使う。

理由:

- 未取得の numeric ID まで canonical 化すると、映画.com catalog 由来の外部事実なのか Domain Graph assertion 由来なのかが曖昧になる。
- 既存 catalog 行がある場合だけ重複回避として安全に解決する。

### 6.3 alias 保存

canonical 採用時、raw ID と canonical ID が異なる場合だけ `movie_id_aliases` へ保存する。

```text
alias_id = raw entity_id
canonical_movie_id = resolved movie_id
source = domain_graph_sync
```

同じ alias は upsert する。

## 7. API response 追加

`POST /viewer/movie-catalog/domain-graph-sync` の success response に次を追加する。

```json
{
  "resolved_movie_ids": {
    "movie:57573": "57573"
  }
}
```

既存 field:

- `movie_ids` は実際に upsert した `movies.movie_id` を返す。
- 未解決の場合は raw `entity_id` が入る。

## 8. 実装設計

変更ファイル:

- `internal/adapter/viewer/movie_catalog_domain_graph_sync.go`
- `internal/adapter/viewer/movie_catalog_domain_graph_sync_test.go`
- `docs/10_新仕様/58_DomainGraph_MovieIDResolver実装仕様.md`

追加 helper:

```go
func ensureMovieCatalogIDAliasTables(ctx context.Context, db *sql.DB) error
func resolveMovieCatalogWorkMovieID(ctx context.Context, db *sql.DB, item conversationpersistence.L1DomainGraphAssertion, rawMovieID string) (string, string, error)
func movieCatalogCanonicalIDCandidate(item conversationpersistence.L1DomainGraphAssertion) string
func movieCatalogEigaMovieIDFromURL(rawURL string) string
func movieCatalogMovieIDExists(ctx context.Context, db *sql.DB, movieID string) (bool, error)
func upsertMovieCatalogIDAlias(ctx context.Context, db *sql.DB, aliasID string, canonicalMovieID string) error
```

戻り値:

- `resolveMovieCatalogWorkMovieID` の第1戻り値は upsert 先 movie_id。
- 第2戻り値は canonical へ解決した場合の canonical ID。未解決なら空。

## 9. テスト仕様

追加 test:

- `TestHandleMovieDomainGraphSyncResolvesMoviePrefixedIDToExistingCatalogID`

確認:

- 既存 `movies(movie_id='57573')` がある。
- assertion `entity_id='movie:57573'` を sync する。
- response `movie_ids` は `57573`。
- response `resolved_movie_ids["movie:57573"] == "57573"`。
- `movies` に `movie:57573` 行は作られない。
- `movie_id_aliases(alias_id='movie:57573', canonical_movie_id='57573')` が保存される。

既存 test:

- `TestHandleMovieDomainGraphSyncUpsertsMovieWorks` は、既存 canonical 行がない場合に raw ID のまま保存されることを維持する。

## 10. 検証コマンド

```bash
GOCACHE=/tmp/picoclaw-go-cache go test ./internal/adapter/viewer
git diff --check
```

## 11. 完了条件

- `movie:57573` と `57573` の重複 row を、既存 catalog row がある場合に避けられる。
- 解決結果が response と alias table で追跡できる。
- Domain Graph assertion 自体は書き換えない。
- 未取得 ID は勝手に canonical 化しない。
