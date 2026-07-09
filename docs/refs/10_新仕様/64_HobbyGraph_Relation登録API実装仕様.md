# Hobby Graph Relation 登録 API 実装仕様

## 1. 目的

Hobby Graph の `hobby_relations` に、既存 item 同士の関係を手動登録できる API を追加する。

`63_HobbyGraph_Interaction登録API実装仕様.md` で item と interaction を登録できるようになった。次に、作品と作者、作品とシリーズ、作品と出版社などの関係を保存できる入口が必要である。

## 2. 実装範囲

### 2.1 実装すること

- `POST /viewer/hobby-graph/relation` を追加する。
- DB がなければ bootstrap と同じ writable path に作成する。
- `from_item_id` と `to_item_id` が `hobby_items` に存在することを確認する。
- `hobby_relations` へ relation を upsert する。
- response に relation を返す。

### 2.2 実装しないこと

- item の自動作成。
- fuzzy title resolver。
- Domain Graph assertion からの自動 relation 同期。
- topic candidate 生成。
- Viewer UI。

## 3. request

```json
{
  "from_item_id": "hobby_item:work",
  "to_item_id": "hobby_item:creator",
  "relation_type": "created_by",
  "source": "manual",
  "evidence_url": "https://example.com/source",
  "evidence": {
    "note": "公式プロフィールより"
  }
}
```

必須:

- `from_item_id`
- `to_item_id`
- `relation_type`

任意:

- `source` default `manual`
- `evidence_url`
- `evidence`

## 4. ID

```text
hobby_relation:{sha1(from_item_id,to_item_id,relation_type,source)[:16]}
```

同じ relation の再送信は upsert する。

## 5. validation

400:

- JSON が壊れている。
- required field が空。

404:

- `from_item_id` または `to_item_id` が存在しない。

405:

- POST 以外。

## 6. response

```json
{
  "available": true,
  "db_path": "...",
  "relation": {
    "relation_id": "hobby_relation:...",
    "from_item_id": "...",
    "to_item_id": "...",
    "relation_type": "created_by",
    "source": "manual",
    "evidence_url": "https://example.com/source"
  }
}
```

## 7. 変更ファイル

- `internal/adapter/viewer/hobby_graph_handler.go`
- `internal/adapter/viewer/hobby_graph_handler_test.go`
- `cmd/rencrow/routes.go`

## 8. テスト

- `TestHandleHobbyGraphRelationCreatesRelationBetweenExistingItems`
- `TestHandleHobbyGraphRelationRejectsMissingItem`
- `TestHandleHobbyGraphRelationRejectsInvalidRequest`
- `TestHandleHobbyGraphRelationRejectsInvalidMethod`

## 9. 検証コマンド

```bash
GOCACHE=/tmp/rencrow-go-cache go test ./internal/adapter/viewer ./cmd/rencrow
git diff --check
```

## 10. 完了条件

- Hobby Graph に item 間 relation を保存できる。
- 存在しない item への relation を保存しない。
- relation と interaction が別 table として維持される。
- 同じ request の再送信で重複しない。
