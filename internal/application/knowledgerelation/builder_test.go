package knowledgerelation

import (
	"context"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgerelation"
)

type fakeRelationStore struct {
	relations []knowledgerelation.Relation
}

func (f *fakeRelationStore) UpsertKnowledgeRelation(_ context.Context, relation knowledgerelation.Relation) error {
	f.relations = append(f.relations, relation)
	return nil
}

func TestBuilderBuildAndStore(t *testing.T) {
	store := &fakeRelationStore{}
	cfg := knowledgerelation.DefaultScoringConfig()
	cfg.MinimumScore = 3
	builder := NewBuilder(store, cfg)
	relations, err := builder.BuildAndStore(context.Background(), []knowledgerelation.ItemMetadata{
		{ItemID: "a", Projects: []string{"RenCrow"}},
		{ItemID: "b", Projects: []string{"RenCrow"}},
	})
	if err != nil {
		t.Fatalf("BuildAndStore failed: %v", err)
	}
	if len(relations) != 2 || len(store.relations) != 2 {
		t.Fatalf("expected bidirectional relations, got relations=%#v stored=%#v", relations, store.relations)
	}
}
