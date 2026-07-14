# Knowledge Relation 接続実装仕様

- status: active
- document_type: implementation_spec
- path: `docs/04_構築指標/04_KnowledgeRelation接続実装仕様.md`
- source_spec: `docs/02_正本仕様/10_RenCrow_ToBe_統合仕様.md`
- last_reviewed: 2026-07-14

## 1. 目的

本仕様は、`knowledge_relation` MVP を Knowledge import、batch、Recall runtime へ接続するための実装仕様である。

目的は、カテゴリ別 Knowledge DB を壊さず、Entity / Topic / Project を接着剤にして 1-2 hop の横断想起を行うことである。

## 2. 現在の実装状態

MVP 実装済み:

```text
internal/domain/knowledgerelation
internal/application/knowledgerelation
internal/infrastructure/persistence/conversation/l1sqlite/l1_sqlite_knowledge_relation.go
internal/domain/conversation/recall_pack.go
internal/domain/conversation/recall_trace.go
```

現在できること:

- relation 用 domain model がある。
- L1 SQLite に `l1_knowledge_entity` / `l1_knowledge_item_entity` / `l1_knowledge_item_relation` がある。
- 同じ Entity / Topic / Project から relation を構築できる。
- RecallPack に `RelationSnippet` を入れられる。
- RecallTrace に `knowledge_relation` が残る。

未接続:

- Knowledge import 時の entity / topic 保存
- relation builder の import hook
- 夜間 batch / dry-run
- `RealConversationEngine.BeginTurn` からの 1-2 hop relation expansion
- Viewer / Ops の relation overview

## 3. 実装範囲

### 3.1 含める

- L1KnowledgeItem から relation metadata を作る mapper
- import 後 relation build hook
- relation dry-run batch service
- 1 hop relation recall
- 2 hop 上限つき expansion
- RecallTrace への evidence / hop 記録
- read-only Viewer API

### 3.2 含めない

- Neo4j / Memgraph 導入
- VectorDB 必須化
- 3 hop 以上の探索
- 既存 Knowledge item の full backfill 自動実行
- 外部検索結果の自動 confirmed knowledge 化
- LLM による entity 抽出の本番接続

LLM entity extraction は将来の adapter とし、初回は既存 metadata / keyword / domain field からの deterministic extraction に限定する。

## 4. Relation metadata

### 4.1 入力元

対象:

```text
l1_knowledge_item
wiki_page_index
knowledge_memory personal / creative / news item
```

初回接続では `l1_knowledge_item` を優先する。

### 4.2 Metadata rule

`L1KnowledgeItem` から次を抽出する。

```text
entities:
  - title 内の CamelCase / 英数字技術語
  - keywords_text 内の固有名詞候補
  - source_id / domain に由来する project / source

topics:
  - domain
  - keywords_text の snake_case / kebab-case

projects:
  - RenCrow / RenCrow_CORE / RenCrow_LLM など既知 prefix
```

初回は deterministic rule のみとし、曖昧な entity は保存しない。

## 5. Domain / Application 追加

### 5.1 MetadataExtractor

`internal/application/knowledgerelation` に追加する。

```go
type MetadataExtractor struct {
    aliases AliasResolver
}

func (e *MetadataExtractor) ExtractFromL1KnowledgeItem(item l1sqlite.L1KnowledgeItem) knowledgerelation.ItemMetadata
```

要件:

- 空文字を返さない。
- entity name は canonicalize する。
- 同一 item 内で重複を除去する。
- source_type は domain / source_id から決める。

### 5.2 RelationBuildService

```go
type RelationBuildService struct {
    store Store
    builder *Builder
}

func (s *RelationBuildService) BuildForItem(ctx context.Context, item l1sqlite.L1KnowledgeItem) (BuildReport, error)
func (s *RelationBuildService) BuildBatch(ctx context.Context, query BatchQuery) (BuildReport, error)
```

`BatchQuery`:

```go
type BatchQuery struct {
    Domain string
    Limit int
    DryRun bool
    Since time.Time
}
```

`BuildReport`:

```go
type BuildReport struct {
    CheckedItems int
    EntityUpserts int
    ItemEntityUpserts int
    RelationUpserts int
    Skipped int
    SkipReasons map[string]int
    DryRun bool
}
```

## 6. Persistence interface

既存 `l1sqlite` の relation methods を `RealConversationManager` へ公開する。

追加 interface:

```go
type KnowledgeRelationStore interface {
    SaveKnowledgeEntity(ctx context.Context, item l1sqlite.L1KnowledgeEntity) error
    SaveKnowledgeItemEntity(ctx context.Context, item l1sqlite.L1KnowledgeItemEntity) error
    SaveKnowledgeItemRelation(ctx context.Context, item l1sqlite.L1KnowledgeItemRelation) error
    RelatedKnowledgeItems(ctx context.Context, itemID string, maxHop int, limit int) ([]l1sqlite.L1KnowledgeRelationHit, error)
}
```

`RelatedKnowledgeItems` の制約:

- `maxHop <= 2`
- score desc
- cycle を返さない
- 同一 item を重複して返さない
- source item 自身を返さない

## 7. Import hook

接続箇所:

```text
internal/infrastructure/persistence/conversation/real_manager_kb.go
SaveL1KnowledgeItem
```

初回実装:

1. `SaveL1KnowledgeItem` 成功後に relation build を呼ぶ。
2. relation build が失敗しても Knowledge item 保存は失敗にしない。
3. 失敗は warning log と BuildReport に残す。
4. config flag が false の場合は実行しない。

config:

```yaml
knowledge_relation:
  enabled: false
  build_on_import: false
  max_hops: 2
  minimum_score: 4
```

初期 default は disabled。

## 8. Batch / CLI

CLI は dry-run を先に実装する。

```bash
rencrow knowledge relations build --domain all --limit 100 --dry-run
rencrow knowledge relations build --domain qiita --limit 100
```

禁止:

- full backfill を default にしない。
- `--dry-run=false` を省略時 default にしない。
- relation 数が閾値を超える場合に silent continue しない。

閾値:

```text
max_relation_upserts_per_run = 5000
```

超えた場合は fail ではなく `blocked_needs_review` report を返す。

## 9. Runtime recall expansion

接続箇所:

```text
internal/infrastructure/persistence/conversation/engine_impl.go
RealConversationEngine.BeginTurn
```

順序:

1. 既存 recall / L1 FTS / Wiki / Vector KB を実行する。
2. 採用された L1 knowledge item の `id` を seed にする。
3. `RelatedKnowledgeItems(maxHop=2, limit=3)` を呼ぶ。
4. `RelationSnippet` として RecallPack に追加する。
5. role filter / budget は既存 RecallPack policy に任せる。

制約:

- relation recall は externalRecall が明確な発話でのみ使う。
- 既存 `shouldUseExternalRecallForUserMessage` を無視しない。
- relation lookup 失敗は graceful degradation。
- VectorDB unavailable でも relation recall は成立する。

## 10. Viewer / Client API

Phase 100 に接続する read-only API:

```text
GET /viewer/knowledge-relations?item_id=...&max_hop=2&limit=20
GET /viewer/knowledge-relations/summary?limit=20
```

response:

```json
{
  "summary": {
    "entity_count": 0,
    "item_entity_count": 0,
    "relation_count": 0,
    "max_hop": 2
  },
  "items": [],
  "relations": []
}
```

## 11. 実装手順

### Task 1: extractor

対象:

```text
internal/application/knowledgerelation
```

確認:

```bash
go test ./internal/application/knowledgerelation
```

### Task 2: store interface / relation query

対象:

```text
internal/infrastructure/persistence/conversation/l1sqlite
internal/infrastructure/persistence/conversation
```

確認:

```bash
go test ./internal/infrastructure/persistence/conversation/l1sqlite ./internal/infrastructure/persistence/conversation
```

### Task 3: build service

対象:

```text
internal/application/knowledgerelation
```

確認:

```bash
go test ./internal/application/knowledgerelation
```

### Task 4: import hook opt-in

対象:

```text
internal/infrastructure/persistence/conversation/real_manager_kb.go
cmd/rencrow/runtime_conversation.go
internal/adapter/config
```

確認:

```bash
go test ./internal/infrastructure/persistence/conversation ./cmd/rencrow
```

### Task 5: runtime recall expansion

対象:

```text
internal/infrastructure/persistence/conversation/engine_impl.go
internal/domain/conversation
```

確認:

```bash
go test ./internal/infrastructure/persistence/conversation ./internal/domain/conversation
```

### Task 6: Viewer API / client

対象:

```text
internal/adapter/viewer
pkg/rencrowclient
cmd/rencrow/routes.go
```

確認:

```bash
go test ./internal/adapter/viewer ./pkg/rencrowclient ./cmd/rencrow
```

## 12. 完了条件

- import 時 relation build を opt-in で実行できる。
- dry-run batch が relation upsert 予定数を返す。
- 1 hop relation が RecallPack に入る。
- 2 hop 上限を超えない。
- VectorDB unavailable でも relation recall が動く。
- RecallTrace に `kind=knowledge_relation` と hop / evidence が残る。
- Viewer API で relation summary を取得できる。
- `go test ./...` が通る。

## 13. 停止条件

次は別仕様へ切り出す。

- LLM entity extraction の本番接続
- full backfill
- relation visualization graph
- Neo4j / Memgraph 移行
- source confidence を使った contradiction 判定
