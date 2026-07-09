# Hobby Graph Topic Candidates 生成 API 実装仕様

## 1. 目的

Hobby Graph DB の `hobby_interactions` / `hobby_relations` / `hobby_items` から、Mio が会話で使える `hobby_topic_candidates` を生成する。

`62` から `64` で Hobby Graph の共通 DB、interaction 登録、relation 登録の入口ができた。次に必要なのは、れん個人の履歴と item 間 relation を混同せず、「何を話題にできるか」を候補として残すことである。

この仕様では、LLM 生成ではなく deterministic な最小 generator を Viewer API として追加する。

## 2. 参考仕様

- `docs/10_新仕様/50_Hobby_Graph_Mio_Topic仕様.md`
- `docs/10_新仕様/62_HobbyGraph_CommonDB_Bootstrap実装仕様.md`
- `docs/10_新仕様/63_HobbyGraph_Interaction登録API実装仕様.md`
- `docs/10_新仕様/64_HobbyGraph_Relation登録API実装仕様.md`
- `docs/10_新仕様/60_MovieTopicCandidates生成API実装仕様.md`

## 3. 実装範囲

### 3.1 実装すること

- `POST /viewer/hobby-graph/topic-candidates/generate` を追加する。
- Hobby Graph DB が見つからない場合は soft unavailable を返す。
- `hobby_topic_candidates` table がなければ作成する。
- interaction 済み item から outgoing relation を辿り、話題候補を生成する。
- 生成根拠を `evidence_json` に保存する。
- 既存候補は candidate ID で upsert する。
- response に生成件数、skip 件数、DB path、candidate IDs を返す。

### 3.2 実装しないこと

- LLM による候補文章生成。
- 外部検索。
- `used` / `dismissed` 更新 API。
- Viewer UI での候補一覧表示。
- Qdrant sync。
- 自動定期生成。
- Domain Graph assertion から Hobby Graph への同期。

## 4. 生成ルール

### 4.1 followup_relation

対象:

- `hobby_interactions.item_id` がある。
- `hobby_interactions.interaction_type` が `watched` / `read` / `listened` / `played` / `cleared` / `attended` / `owned` / `liked` / `interested` のいずれか。
- interaction 済み item から `hobby_relations.from_item_id` で related item へ辿れる。
- related item が `hobby_items` に存在する。

候補:

| field | value |
| --- | --- |
| `category` | interaction category |
| `topic_type` | `followup_relation` |
| `target_item_id` | related item の `item_id` |
| `title` | interaction item と related item の関係を含む短文 |
| `reason` | れんの interaction と relation を根拠にする短文 |
| `evidence_json` | interaction / source item / related item / relation を含める |
| `status` | `candidate` |
| `generated_by` | `hobby_topic_candidate_generator` |

### 4.2 candidate_id

安定 ID:

```text
hobby_topic:{sha1(topic_type,interaction_id,relation_id,target_item_id)[:16]}
```

同じ候補の再生成は upsert する。

## 5. API

### 5.1 endpoint

```text
POST /viewer/hobby-graph/topic-candidates/generate
```

GET は 405。

### 5.2 query

| query | default | max |
| --- | --- | --- |
| `limit` | `20` | `100` |

`limit` が 0 以下または数値でない場合は 400。

### 5.3 success response

```json
{
  "available": true,
  "db_path": "tmp/hobby_graph/hobby_graph.sqlite",
  "generated": 1,
  "skipped": 0,
  "candidate_ids": ["hobby_topic:..."]
}
```

### 5.4 unavailable response

DB が見つからない場合は 200 JSON の `available=false` とする。`GET /viewer/hobby-graph?action=stats` と同じ soft unavailable 境界に合わせる。

```json
{
  "available": false,
  "db_path": "...",
  "error": "hobby graph database not found"
}
```

## 6. 実装設計

追加ファイル:

- `internal/adapter/viewer/hobby_topic_candidates_handler.go`
- `internal/adapter/viewer/hobby_topic_candidates_handler_test.go`

変更ファイル:

- `cmd/picoclaw/routes.go`

追加関数:

```go
func HandleHobbyTopicCandidatesGenerate(opts HobbyGraphOptions) http.HandlerFunc
func generateHobbyTopicCandidates(ctx context.Context, db *sql.DB, limit int) (hobbyTopicCandidatesGenerateResponse, error)
func hobbyTopicCandidateLimit(r *http.Request) (int, error)
```

## 7. テスト仕様

追加 test:

- `TestHandleHobbyTopicCandidatesGenerateCreatesFollowupRelation`
- `TestHandleHobbyTopicCandidatesGenerateMissingDBIsSoftUnavailable`
- `TestHandleHobbyTopicCandidatesGenerateRejectsInvalidRequest`
- `TestHandleHobbyTopicCandidatesGenerateRejectsInvalidMethod`

確認:

- interaction 済み作品 A と related item B の relation がある。
- API 実行で B を target にした `hobby_topic_candidates` が生成される。
- evidence_json に interaction / source item / related item / relation が入る。
- 2回実行しても candidate ID で upsert され重複しない。

## 8. 検証コマンド

```bash
GOCACHE=/tmp/picoclaw-go-cache go test ./internal/adapter/viewer ./cmd/picoclaw
git diff --check
```

## 9. 完了条件

- Hobby Graph から Mio 話題候補の最小生成入口がある。
- れんの interaction、item relation、話題候補が別 table のまま保たれる。
- 根拠なしのおすすめを作らない。
- DB 未作成時は soft unavailable で返る。
