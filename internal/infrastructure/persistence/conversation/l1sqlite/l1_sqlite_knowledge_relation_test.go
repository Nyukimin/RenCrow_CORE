package l1sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgerelation"
)

func TestL1SQLiteStore_KnowledgeRelationTables(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.UpsertKnowledgeEntity(ctx, L1KnowledgeEntity{
		EntityID:      "entity:mlx",
		CanonicalName: "MLX",
		EntityType:    "technology",
		Aliases:       []string{"Apple MLX"},
	}); err != nil {
		t.Fatalf("UpsertKnowledgeEntity failed: %v", err)
	}
	if err := store.LinkKnowledgeItemEntity(ctx, L1KnowledgeItemEntity{
		ItemID:       "qiita-mlx",
		EntityID:     "entity:mlx",
		RelationKind: "mentions",
		Score:        0.9,
		Evidence:     "title mentions MLX",
	}); err != nil {
		t.Fatalf("LinkKnowledgeItemEntity failed: %v", err)
	}
	if err := store.UpsertKnowledgeRelation(ctx, knowledgerelation.Relation{
		SrcItemID:    "qiita-mlx",
		DstItemID:    "github-mlx",
		RelationType: knowledgerelation.RelationSameEntity,
		Score:        3,
		Evidence:     "same entity: MLX",
	}); err != nil {
		t.Fatalf("UpsertKnowledgeRelation failed: %v", err)
	}

	relations, err := store.RelatedKnowledgeRelations(ctx, "qiita-mlx", 10)
	if err != nil {
		t.Fatalf("RelatedKnowledgeRelations failed: %v", err)
	}
	if len(relations) != 1 {
		t.Fatalf("len(relations) = %d, want 1", len(relations))
	}
	if relations[0].DstItemID != "github-mlx" || relations[0].RelationType != knowledgerelation.RelationSameEntity {
		t.Fatalf("unexpected relation: %#v", relations[0])
	}
}

func TestL1SQLiteStoreRelatedKnowledgeItemsHonorsHopLimitAndDeduplicatesCycles(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c", "d"} {
		insertRelationKnowledgeItem(t, store, id)
	}
	for _, relation := range []knowledgerelation.Relation{
		{SrcItemID: "a", DstItemID: "b", RelationType: knowledgerelation.RelationSameEntity, Score: 5, Evidence: "a-b"},
		{SrcItemID: "b", DstItemID: "c", RelationType: knowledgerelation.RelationSameTopic, Score: 4, Evidence: "b-c"},
		{SrcItemID: "c", DstItemID: "a", RelationType: knowledgerelation.RelationSameProject, Score: 6, Evidence: "cycle"},
		{SrcItemID: "c", DstItemID: "d", RelationType: knowledgerelation.RelationSameProject, Score: 3, Evidence: "third-hop"},
	} {
		if err := store.SaveKnowledgeItemRelation(ctx, relation); err != nil {
			t.Fatalf("SaveKnowledgeItemRelation failed: %v", err)
		}
	}
	oneHop, err := store.RelatedKnowledgeItems(ctx, "a", 1, 20)
	if err != nil {
		t.Fatalf("RelatedKnowledgeItems(1) failed: %v", err)
	}
	if len(oneHop) != 1 || oneHop[0].Item.ID != "b" || oneHop[0].Hop != 1 {
		t.Fatalf("oneHop=%#v", oneHop)
	}
	twoHop, err := store.RelatedKnowledgeItems(ctx, "a", 2, 20)
	if err != nil {
		t.Fatalf("RelatedKnowledgeItems(2) failed: %v", err)
	}
	if len(twoHop) != 2 || twoHop[0].Item.ID == "a" || twoHop[1].Item.ID == "a" {
		t.Fatalf("twoHop=%#v", twoHop)
	}
	if twoHop[1].Item.ID != "c" || twoHop[1].Hop != 2 {
		t.Fatalf("expected c at hop 2: %#v", twoHop)
	}
	if _, err := store.RelatedKnowledgeItems(ctx, "a", 3, 20); err == nil {
		t.Fatal("expected max_hop validation error")
	}
}

func TestL1SQLiteStoreKnowledgeRelationSummaryAndBatchList(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	insertRelationKnowledgeItem(t, store, "a")
	insertRelationKnowledgeItem(t, store, "b")
	if err := store.SaveKnowledgeEntity(ctx, L1KnowledgeEntity{EntityID: "entity:mlx", CanonicalName: "mlx", EntityType: "entity"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveKnowledgeItemEntity(ctx, L1KnowledgeItemEntity{ItemID: "a", EntityID: "entity:mlx", RelationKind: "mentions", Score: 1, Evidence: "title"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveKnowledgeItemRelation(ctx, knowledgerelation.Relation{SrcItemID: "a", DstItemID: "b", RelationType: knowledgerelation.RelationSameEntity, Score: 5, Evidence: "same entity"}); err != nil {
		t.Fatal(err)
	}
	summary, err := store.KnowledgeRelationSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary.EntityCount != 1 || summary.ItemEntityCount != 1 || summary.RelationCount != 1 {
		t.Fatalf("summary=%#v", summary)
	}
	items, err := store.ListKnowledgeItemsForRelations(ctx, "all", 10, time.Time{})
	if err != nil || len(items) != 2 {
		t.Fatalf("items=%#v err=%v", items, err)
	}
}

func insertRelationKnowledgeItem(t *testing.T, store *L1SQLiteStore, id string) {
	t.Helper()
	now := time.Now().UTC()
	_, err := store.db.Exec(`INSERT INTO l1_knowledge_item
(id, staging_id, domain, title, source_id, source_url, raw_text, raw_hash, summary_draft, keywords_json, license_note, meta_json, created_at, updated_at)
VALUES (?, ?, 'general', ?, 'test', '', ?, ?, ?, '["mlx"]', '', '{}', ?, ?)`, id, "staging-"+id, "Title "+id, "body "+id, "hash-"+id, "summary "+id, now, now)
	if err != nil {
		t.Fatalf("insert knowledge item %s: %v", id, err)
	}
}
