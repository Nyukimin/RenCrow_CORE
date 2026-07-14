package l1sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgerelation"
)

func TestL1SQLiteStore_KnowledgeRelationTables(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
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
