# Hobby Graph Overview 表示 API / Ops 実装仕様

## 1. 目的

Hobby Graph の現在状態を Viewer / Ops から短時間で把握できるようにする。

`62` から `66` で Hobby Graph の DB 初期化、interaction、relation、topic candidate 生成、Domain Graph 同期の入口はできた。一方、Viewer から見えるのは stats だけで、最新 item / relation / interaction / topic candidate を一覧できない。この状態では「何が入っているか」「候補が生成されたか」を探すために DB や全画面スクロールへ戻る必要がある。

この仕様では、Hobby Graph の compact current view を API で返し、Ops secondary card に表示する。

## 2. 参考仕様

- `docs/10_新仕様/50_Hobby_Graph_Mio_Topic仕様.md`
- `docs/10_新仕様/62_HobbyGraph_CommonDB_Bootstrap実装仕様.md`
- `docs/10_新仕様/63_HobbyGraph_Interaction登録API実装仕様.md`
- `docs/10_新仕様/64_HobbyGraph_Relation登録API実装仕様.md`
- `docs/10_新仕様/65_HobbyGraph_TopicCandidates生成API実装仕様.md`
- `docs/10_新仕様/66_DomainGraph_HobbyGraph同期API実装仕様.md`
- `rules/rules_viewer_ui.md`

## 3. 実装範囲

### 3.1 実装すること

- `GET /viewer/hobby-graph?action=overview` を追加する。
- response に stats と最新 item / relation / interaction / topic candidate を返す。
- DB がない場合は既存 stats と同じ soft unavailable を返す。
- Ops data refresh で overview を取得する。
- Ops secondary card に Hobby Graph の件数、最新候補、最新 relation を表示する。
- API / JS の契約テストを追加する。

### 3.2 実装しないこと

- Hobby Graph 専用タブ。
- item / relation の編集 UI。
- topic candidate の used / dismissed 更新 UI。
- Qdrant sync 状態表示。
- 外部検索や自動収集の起動 UI。

## 4. API 仕様

### 4.1 endpoint

```text
GET /viewer/hobby-graph?action=overview&limit=5
```

`limit`:

- default `5`
- max `20`
- 0 以下または数値でない場合は 400

### 4.2 success response

```json
{
  "available": true,
  "db_path": "tmp/hobby_graph/hobby_graph.sqlite",
  "action": "overview",
  "stats": {
    "hobby_items": 2,
    "hobby_relations": 1,
    "hobby_interactions": 1,
    "hobby_topic_candidates": 1
  },
  "items": [
    {
      "item_id": "hobby_item:...",
      "category": "manga",
      "item_type": "work",
      "title": "ダンジョン飯",
      "normalized_title": "ダンジョン飯",
      "updated_at": "2026-06-06 00:00:00"
    }
  ],
  "relations": [
    {
      "relation_id": "hobby_relation:...",
      "from_item_id": "...",
      "from_title": "ダンジョン飯",
      "to_item_id": "...",
      "to_title": "九井諒子",
      "relation_type": "created_by",
      "source": "domain_graph",
      "created_at": "2026-06-06 00:00:00"
    }
  ],
  "interactions": [
    {
      "interaction_id": "hobby_interaction:...",
      "item_id": "...",
      "title": "ダンジョン飯",
      "category": "manga",
      "interaction_type": "read",
      "source": "manual",
      "created_at": "2026-06-06 00:00:00"
    }
  ],
  "topic_candidates": [
    {
      "candidate_id": "hobby_topic:...",
      "category": "manga",
      "topic_type": "followup_relation",
      "target_item_id": "...",
      "target_title": "九井諒子",
      "title": "「ダンジョン飯」からcreated_by「九井諒子」を話題にする",
      "reason": "readした「ダンジョン飯」とcreated_byで関係している",
      "status": "candidate",
      "generated_by": "hobby_topic_candidate_generator",
      "generated_at": "2026-06-06 00:00:00"
    }
  ]
}
```

### 4.3 unavailable response

DB が見つからない場合:

```json
{
  "available": false,
  "db_path": "...",
  "action": "overview",
  "error": "hobby graph database not found"
}
```

## 5. Ops 表示仕様

Ops secondary card:

| field | 表示 |
| --- | --- |
| title | `Hobby Graph` |
| big | `items / relations / topics` |
| sub | DB path、最新 candidate、最新 relation、unavailable 理由 |

表示方針:

- card 内に長文を詰め込まない。
- 最新 candidate は title を短縮表示する。
- 最新 relation は `from_title -> relation_type -> to_title` を短縮表示する。
- fetch 失敗と DB unavailable は区別する。

## 6. 実装設計

変更ファイル:

- `internal/adapter/viewer/hobby_graph_handler.go`
- `internal/adapter/viewer/hobby_graph_handler_test.go`
- `internal/adapter/viewer/assets/js/viewer.js`
- `internal/adapter/viewer/assets/js/tabs/ops.js`
- `internal/adapter/viewer/viewer_static_contract_test.go`
- `docs/10_新仕様/67_HobbyGraph_Overview表示API_Ops実装仕様.md`

追加関数:

```go
func hobbyGraphOverview(db *sql.DB, limit int) (hobbyGraphOverviewResponse, error)
func hobbyGraphOverviewLimit(r *http.Request) (int, error)
```

JS:

```js
function refreshHobbyGraphOverviewData()
function hobbyGraphOpsCard()
```

## 7. テスト仕様

追加 test:

- `TestHandleHobbyGraphOverviewReturnsRecentRows`
- `TestHandleHobbyGraphOverviewMissingDBIsSoftUnavailable`
- `TestHandleHobbyGraphOverviewRejectsInvalidLimit`
- static contract で `refreshHobbyGraphOverviewData` と `hobbyGraphOpsCard` を確認する。

## 8. 検証コマンド

```bash
GOCACHE=/tmp/rencrow-go-cache go test ./internal/adapter/viewer ./cmd/rencrow
node --test internal/adapter/viewer/viewer_memory_panel.test.mjs
git diff --check
```

## 9. 完了条件

- Hobby Graph の最新 item / relation / interaction / topic candidate が API で見える。
- Ops card から Hobby Graph の有無、件数、最新候補、最新 relation が分かる。
- DB unavailable と fetch failure が混同されない。
- 既存 stats / bootstrap / interaction / relation / topic candidate / domain sync API を壊さない。
