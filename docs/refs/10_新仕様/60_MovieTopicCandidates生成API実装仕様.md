# Movie Topic Candidates 生成 API 実装仕様

## 1. 目的

Movie catalog DB の `movies` / `movie_people` / `movie_watch_events` から、Mio が会話で使える `movie_topic_candidates` を生成する。

`57` から `59` で Domain Graph assertion を `movies` と `movie_people` へ同期できるようになった。次に必要なのは、外部カタログ事実とれんの鑑賞履歴を混同せず、「何を話題にできるか」を候補として残すことである。

この仕様では、LLM 生成ではなく deterministic な最小 generator を Viewer API として追加する。

## 2. 参考仕様

- `docs/10_新仕様/49_Movie_Graph_Mio_Topic仕様.md`
- `docs/10_新仕様/51_Movie_Watch_Event実装仕様.md`
- `docs/10_新仕様/59_DomainGraph_MoviePeopleEdge同期実装仕様.md`

## 3. 実装範囲

### 3.1 実装すること

- `POST /viewer/movie-catalog/topic-candidates/generate` を追加する。
- `movie_topic_candidates` table がなければ作成する。
- `movie_watch_events` と `movie_people` から、見た作品に関係する人物の別作品候補を生成する。
- 生成根拠を `evidence_json` に保存する。
- 既存候補は candidate ID で upsert する。
- response に生成件数、skip 件数、DB path を返す。

### 3.2 実装しないこと

- LLM による候補文章生成。
- `used` / `dismissed` 更新 API。
- Viewer UI での候補一覧表示。
- Qdrant sync。
- 自動定期生成。

## 4. DB schema

```sql
CREATE TABLE IF NOT EXISTS movie_topic_candidates (
  candidate_id TEXT PRIMARY KEY,
  topic_type TEXT NOT NULL,
  target_movie_id TEXT,
  target_person_id TEXT,
  title TEXT NOT NULL,
  reason TEXT NOT NULL,
  evidence_json TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'candidate',
  generated_by TEXT NOT NULL,
  generated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  used_at TEXT
);
```

## 5. 生成ルール

### 5.1 watched_followup

対象:

- `movie_watch_events.movie_id` がある。
- その映画に `movie_people` edge がある。
- 同じ `person_id` に紐づく別 `movie_people.movie_id` がある。
- 別作品は `movie_watch_events` に存在しない。

候補:

| field | value |
| --- | --- |
| `topic_type` | `watched_followup` |
| `target_movie_id` | 別作品 movie_id |
| `target_person_id` | person_id |
| `title` | `person_name` と別作品 title を含む短文 |
| `reason` | 見た作品と同じ人物が関係していること |
| `evidence_json` | watched movie / candidate movie / person / role / source を含める |
| `status` | `candidate` |
| `generated_by` | `movie_topic_candidate_generator` |

### 5.2 candidate_id

安定 ID:

```text
movie_topic:{topic_type}:{watched_movie_id}:{target_movie_id}:{person_id}:{role}
```

実装では SHA1 で短縮してよい。

## 6. API

### 6.1 endpoint

```text
POST /viewer/movie-catalog/topic-candidates/generate
```

GET は 405。

### 6.2 query

| query | default | max |
| --- | --- | --- |
| `limit` | `20` | `100` |

### 6.3 success response

```json
{
  "available": true,
  "db_path": "tmp/eiga_catalog/eiga_catalog.sqlite",
  "generated": 3,
  "skipped": 1,
  "candidate_ids": ["movie_topic:..."]
}
```

### 6.4 unavailable response

DB が見つからない場合は 200 JSON の `available=false` とする。既存 Movie catalog read API と同じ soft unavailable 境界に合わせる。

```json
{
  "available": false,
  "db_path": "...",
  "error": "movie catalog database not found"
}
```

## 7. 実装設計

追加ファイル:

- `internal/adapter/viewer/movie_topic_candidates_handler.go`
- `internal/adapter/viewer/movie_topic_candidates_handler_test.go`

変更ファイル:

- `cmd/rencrow/routes.go`

追加関数:

```go
func HandleMovieTopicCandidatesGenerate(opts MovieCatalogOptions) http.HandlerFunc
func generateMovieTopicCandidates(ctx context.Context, db *sql.DB, limit int) (movieTopicCandidatesGenerateResponse, error)
func ensureMovieTopicCandidateTables(ctx context.Context, db *sql.DB) error
```

## 8. テスト仕様

追加 test:

- `TestHandleMovieTopicCandidatesGenerateCreatesWatchedFollowup`
- `TestHandleMovieTopicCandidatesGenerateMissingDBIsSoftUnavailable`
- `TestHandleMovieTopicCandidatesGenerateRejectsInvalidMethod`

確認:

- 見た映画 A と人物 P の edge がある。
- 同じ人物 P の未視聴映画 B の edge がある。
- API 実行で B を target にした `movie_topic_candidates` が生成される。
- evidence_json に watched / candidate / person / role が入る。
- 2回実行しても candidate ID で upsert され重複しない。

## 9. 検証コマンド

```bash
GOCACHE=/tmp/rencrow-go-cache go test ./internal/adapter/viewer
git diff --check
```

## 10. 完了条件

- Mio 話題候補の最小生成入口がある。
- 外部 catalog 事実、ユーザー鑑賞履歴、話題候補が別 table のまま保たれる。
- 根拠なしのおすすめを作らない。
