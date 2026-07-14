package knowledgerelation

import (
	"context"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgerelation"
)

type RelationStore interface {
	UpsertKnowledgeRelation(ctx context.Context, relation knowledgerelation.Relation) error
}

type Builder struct {
	store RelationStore
	cfg   knowledgerelation.ScoringConfig
}

func NewBuilder(store RelationStore, cfg knowledgerelation.ScoringConfig) *Builder {
	return &Builder{store: store, cfg: cfg}
}

func (b *Builder) BuildAndStore(ctx context.Context, items []knowledgerelation.ItemMetadata) ([]knowledgerelation.Relation, error) {
	if b == nil {
		return knowledgerelation.BuildRelations(items, knowledgerelation.DefaultScoringConfig()), nil
	}
	relations := knowledgerelation.BuildRelations(items, b.cfg)
	if b.store == nil {
		return relations, nil
	}
	for _, relation := range relations {
		if err := b.store.UpsertKnowledgeRelation(ctx, relation); err != nil {
			return relations, err
		}
	}
	return relations, nil
}
