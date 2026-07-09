# Hobby Graph Interaction 登録 API 実装仕様

## 1. 目的

Hobby Graph に、映画以外を含む趣味 item と interaction を手動登録できる API を追加する。

`62_HobbyGraph_CommonDB_Bootstrap実装仕様.md` で共通 table を作れるようになったが、まだデータを入れる入口がない。仕様 50 では「見た」「読んだ」「聴いた」「遊んだ」「気になる」は作品属性ではなく、れん個人の interaction event として保存することになっている。

この仕様では、最初の登録 API として `POST /viewer/hobby-graph/interaction` を追加する。

## 2. 実装範囲

### 2.1 実装すること

- `POST /viewer/hobby-graph/interaction` を追加する。
- DB がなければ bootstrap と同じ writable path に作成する。
- `hobby_items` へ item を upsert する。
- `hobby_interactions` へ interaction event を upsert する。
- `hobby_title_observations` へ `resolved` observation を upsert する。
- response に `item` と `interaction` を返す。

### 2.2 実装しないこと

- relation 登録。
- title resolver / fuzzy match。
- preference signal 自動生成。
- topic candidate 生成。
- Viewer UI。

## 3. request

```json
{
  "category": "manga",
  "item_type": "work",
  "title": "ダンジョン飯",
  "interaction_type": "read",
  "occurred_at": "2026-06-06",
  "source": "manual",
  "source_batch_id": "manual_20260606",
  "rating": 5,
  "note": "アニメ版も気になる"
}
```

必須:

- `category`
- `item_type`
- `title`
- `interaction_type`

任意:

- `occurred_at`
- `source` default `manual`
- `source_batch_id`
- `rating`
- `note`

## 4. ID

### 4.1 item_id

```text
hobby_item:{sha1(category,item_type,normalized_title)[:16]}
```

### 4.2 interaction_id

```text
hobby_interaction:{sha1(item_id,interaction_type,occurred_at,source,source_batch_id,note)[:16]}
```

同じ入力の再送信は同じ interaction を upsert する。

### 4.3 observation_id

```text
hobby_titleobs:{sha1(category,normalized_title,source,source_batch_id)[:16]}
```

## 5. response

```json
{
  "available": true,
  "db_path": "tmp/hobby_graph/hobby_graph.sqlite",
  "item": {
    "item_id": "hobby_item:...",
    "category": "manga",
    "item_type": "work",
    "title": "ダンジョン飯",
    "normalized_title": "ダンジョン飯"
  },
  "interaction": {
    "interaction_id": "hobby_interaction:...",
    "item_id": "hobby_item:...",
    "category": "manga",
    "interaction_type": "read",
    "original_title": "ダンジョン飯"
  }
}
```

## 6. validation

400:

- body JSON が壊れている。
- required field が空。
- `rating` が 0 未満または 5 超。

405:

- POST 以外。

## 7. 変更ファイル

- `internal/adapter/viewer/hobby_graph_handler.go`
- `internal/adapter/viewer/hobby_graph_handler_test.go`
- `cmd/picoclaw/routes.go`

## 8. テスト

- `TestHandleHobbyGraphInteractionCreatesItemInteractionAndObservation`
- `TestHandleHobbyGraphInteractionRejectsInvalidRequest`
- `TestHandleHobbyGraphInteractionRejectsInvalidMethod`

## 9. 検証コマンド

```bash
GOCACHE=/tmp/picoclaw-go-cache go test ./internal/adapter/viewer ./cmd/picoclaw
git diff --check
```

## 10. 完了条件

- Hobby Graph に手動 interaction を保存できる。
- item と interaction が混ざらず、別 table に保存される。
- title observation が resolved として残る。
- 同じ request の再送信で重複しない。
