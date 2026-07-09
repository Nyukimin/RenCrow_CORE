# Domain Graph Assertion 一覧検索 API 実装仕様

## 目的

`domain_graph_assertion` に promote された外部世界の関係事実を、Viewer / API / client から確認・検索できるようにする。

現状の `target=domain_graph` promote は、validated staging を Domain Graph assertion として保存する入口である。次の実装では、保存された assertion を見える状態にし、Movie / Hobby / Mio の後続処理へ渡せる current view を作る。

## 対象範囲

対象:

- Domain Graph assertion の一覧取得
- domain / entity_type / entity_id / relation_type / source_id / validation_status による絞り込み
- limit / offset によるページング
- Viewer Source Registry / Memory 系 UI からの参照
- `pkg/rencrowclient` からの typed client
- malformed current view を拒否する local client validation

対象外:

- assertion から Movie 固有 `movies` / `people` / `movie_people` へ変換する処理
- topic candidate 生成
- Qdrant summary sync
- graph DB 製品への移行
- pending staging の自動 promote

## API

### Viewer API

```text
GET /viewer/domain-graph/assertions
```

query:

| query | 必須 | 意味 |
| --- | --- | --- |
| `domain` | 任意 | `movie` / `manga` / `music` など |
| `entity_type` | 任意 | `work` / `person` / `organization` / `series` など |
| `entity_id` | 任意 | domain 内の正規 ID |
| `relation_type` | 任意 | `performed_by` / `created_by` / `catalog_fact` など |
| `source_id` | 任意 | Source Registry / Web Gather / API source ID |
| `validation_status` | 任意 | 原則 `validated`。将来 `rejected` assertion を扱う場合に備えて残す |
| `limit` | 任意 | 既定 50、最大 200 |
| `offset` | 任意 | 既定 0 |

response:

```json
{
  "items": [
    {
      "id": "dg:movie:evt:hash",
      "staging_id": "kb:movie:evt:hash",
      "domain": "movie",
      "entity_type": "work",
      "entity_id": "movie:1",
      "relation_type": "performed_by",
      "source_id": "web:eiga",
      "source_url": "https://example.com/movie/1",
      "raw_hash": "sha256...",
      "summary": "作品A -> 人物B: performed_by",
      "confidence": 0.8,
      "validation_status": "validated",
      "evidence": {
        "staging_id": "kb:movie:evt:hash",
        "event_id": "evt",
        "source_id": "web:eiga"
      },
      "created_at": "2026-06-06T10:00:00Z",
      "updated_at": "2026-06-06T10:00:00Z"
    }
  ],
  "limit": 50,
  "offset": 0,
  "total": 1
}
```

## Store

`L1SQLiteStore` に次を追加する。

```go
type DomainGraphAssertionQuery struct {
    Domain           string
    EntityType       string
    EntityID         string
    RelationType     string
    SourceID         string
    ValidationStatus string
    Limit            int
    Offset           int
}

func (s *L1SQLiteStore) DomainGraphAssertions(ctx context.Context, q DomainGraphAssertionQuery) (int, []L1DomainGraphAssertion, error)
```

### query validation

- `limit <= 0` は 50
- `limit > 200` は 200
- `offset < 0` は error
- `domain` / `entity_type` / `relation_type` は `trim -> lower -> hyphen/space を underscore` で正規化
- `validation_status` が空なら `validated`
- `validation_status` は `pending` / `validated` / `rejected` のみ許可。ただし現行 assertion は validated promote のみ生成する

## Viewer 表示

初期表示は「見えること」を優先し、重い graph UI は作らない。

表示する要約:

- total
- domain 別件数
- source_id 別件数
- 最新 assertion 10件
- confidence が低い assertion

表示ルール:

- raw text 全文は初期表示しない
- evidence JSON は `details` に閉じる
- source URL は長文でレイアウトを壊さない
- `validation_status` と `confidence` を必ず表示する
- assertion を Qdrant 同期済みと誤認させない。Qdrant sync 状態は別フィールドが実装されるまで表示しない

## Client

`pkg/rencrowclient` に次を追加する。

```go
type DomainGraphAssertionsRequest struct {
    Domain           string
    EntityType       string
    EntityID         string
    RelationType     string
    SourceID         string
    ValidationStatus string
    Limit            int
    Offset           int
}

type DomainGraphAssertionsResponse struct {
    Items  []DomainGraphAssertion `json:"items"`
    Limit  int                    `json:"limit"`
    Offset int                    `json:"offset"`
    Total  int                    `json:"total"`
}

func (c *Client) DomainGraphAssertions(ctx context.Context, req DomainGraphAssertionsRequest) (DomainGraphAssertionsResponse, error)
```

client validation:

- item ID 重複を拒否する
- `id` / `staging_id` / `domain` / `entity_type` / `source_id` / `raw_hash` / `validation_status` / `created_at` / `updated_at` 欠落を拒否する
- `confidence < 0` または `confidence > 1` を拒否する
- `validation_status` が `pending` / `validated` / `rejected` 以外なら拒否する
- `created_at` / `updated_at` は RFC3339 必須
- response の `limit` / `offset` / `total` が負数なら拒否する

## テスト

### persistence

- promoted assertion を domain で検索できる
- entity_type / entity_id / relation_type / source_id で絞り込める
- `validation_status` 既定が `validated` になる
- `limit` が最大 200 に丸められる
- negative offset は拒否する

### Viewer handler

- `GET /viewer/domain-graph/assertions?domain=movie` が JSON current view を返す
- L1 store 未設定時は 503 `domain graph unavailable`
- invalid limit / offset は 400
- response は raw text ではなく assertion summary / evidence を返す

### client

- valid current view を受け取れる
- duplicate assertion ID を拒否する
- timestamp 欠落を拒否する
- malformed confidence を拒否する
- target filter と response item の domain 不整合は、将来 strict mode を追加するまで warning ではなく許容する。理由は query が複数条件なしの場合に横断表示を許すため

## 実装順

1. `DomainGraphAssertionQuery` と `DomainGraphAssertions()` を `L1SQLiteStore` へ追加
2. `/viewer/domain-graph/assertions` handler を追加
3. runtime route に handler を接続し、L1 store 無効時は 503 handler を返す
4. `pkg/rencrowclient.DomainGraphAssertions()` を追加
5. Viewer には最小表示だけ追加する

## 完了条件

- validated staging から promote した assertion が API で見える
- Viewer / client は malformed current view を成功扱いしない
- pending staging / raw web text / Qdrant sync と assertion current view を混同しない
