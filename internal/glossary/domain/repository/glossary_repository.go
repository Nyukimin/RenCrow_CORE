package repository

import (
	"context"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/glossary/domain/entity"
)

type GlossaryRepository interface {
	Save(ctx context.Context, item *entity.GlossaryItem) error
	FindByTerm(ctx context.Context, term string) (*entity.GlossaryItem, error)
	FindRecent(ctx context.Context, limit int) ([]*entity.GlossaryItem, error)
	FindByCategory(ctx context.Context, category string, limit int) ([]*entity.GlossaryItem, error)
	Delete(ctx context.Context, id string) error
}
