# To-Be: Knowledge Relation・Recall

- status: active
- lifecycle: canonical child
- owner: RenCrow_CORE
- parent_spec: `../10_RenCrow_ToBe_統合仕様.md`
- source_spec: `../10_RenCrow_ToBe_統合仕様.md`の2026-07-15分割前章
- last_reviewed: 2026-07-15
- scope: Knowledge Relation、Recall、evidence、promotion

## 8. Knowledge Relation / Recall

### 8.1 基本方針

Knowledge DB はカテゴリ別に分ける。

Relation はカテゴリをまたぐ索引として持つ。

```text
Knowledge DB
├─ kb:x_bookmark
├─ kb:note
├─ kb:qiita
├─ kb:github
├─ kb:paper
├─ kb:culture
└─ kb:news

Relation Layer
├─ entity
├─ topic
├─ project
├─ creator
├─ technology
├─ source
└─ related_item
```

Relation は世界を完全にモデル化するためではなく、思い出すための細い道を残すために使う。

### 8.2 Schema

```sql
CREATE TABLE knowledge_items (
    item_id TEXT PRIMARY KEY,
    source_type TEXT NOT NULL,
    domain TEXT NOT NULL,
    title TEXT NOT NULL,
    summary TEXT NOT NULL,
    source_url TEXT,
    author TEXT,
    published_at TIMESTAMP,
    embedding_id TEXT,
    metadata_json TEXT NOT NULL DEFAULT '{}',
    validation_state TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL
);

CREATE TABLE entities (
    entity_id TEXT PRIMARY KEY,
    canonical_name TEXT NOT NULL,
    entity_type TEXT NOT NULL,
    aliases_json TEXT NOT NULL DEFAULT '[]',
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL
);

CREATE TABLE item_entities (
    item_id TEXT NOT NULL,
    entity_id TEXT NOT NULL,
    relation_kind TEXT NOT NULL,
    score DOUBLE NOT NULL,
    evidence TEXT,
    created_at TIMESTAMP NOT NULL,
    PRIMARY KEY (item_id, entity_id, relation_kind)
);

CREATE TABLE item_relations (
    src_item_id TEXT NOT NULL,
    dst_item_id TEXT NOT NULL,
    relation_type TEXT NOT NULL,
    score DOUBLE NOT NULL,
    evidence TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    PRIMARY KEY (src_item_id, dst_item_id, relation_type)
);

CREATE INDEX idx_knowledge_items_domain ON knowledge_items(domain);
CREATE INDEX idx_item_entities_entity ON item_entities(entity_id);
CREATE INDEX idx_item_relations_src_score ON item_relations(src_item_id, score DESC);
CREATE INDEX idx_item_relations_dst_score ON item_relations(dst_item_id, score DESC);
```

### 8.3 Relation types

```text
same_topic
same_entity
same_project
same_author
supports
contradicts
implements
references
updates
derived_from
applies_to_project
used_together_in_conversation
```

### 8.4 Relation scoring

初期 scoring:

```text
same_entity        +3
same_project       +3
same_topic         +2
same_author        +1
vector_similarity  +0..2
conversation_pair  +1
```

保存条件:

```text
score >= 4
```

探索制限:

```text
max_hops = 2
max_related_items_per_seed = 10
max_total_relation_items = 30
```

### 8.5 Recall order

```text
1. 完全一致
2. タグ一致
3. Entity / Topic / Project 一致
4. Relation 上位
5. 足りない時だけ Vector 検索
6. Recall budget で再ランキング
```

### 8.6 RecallPack extension

`RecallPack` には次を追加する。

```go
type RelationSnippet struct {
    ItemID       string
    Title        string
    Summary      string
    SourceType   string
    RelationType string
    Score        float64
    Evidence     string
    Hop          int
}
```

追加先:

```text
internal/domain/conversation/recall_pack.go
```

ただし初期導入では既存 field を壊さず、optional field として追加する。

### 8.7 責務配置 package

```text
internal/domain/knowledgerelation
internal/application/knowledgerelation
internal/infrastructure/persistence/conversation/l1_sqlite_knowledge_relation.go
internal/application/knowledge/relation_builder.go
```

既存 `L1SQLite` / `Source Registry` / `VectorDB` と競合させない。Relation layer は L1 SQLite の追加テーブルとして始める。
