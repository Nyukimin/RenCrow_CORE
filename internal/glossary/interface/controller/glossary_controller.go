package controller

import (
	"context"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/glossary/application/service"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/glossary/domain/entity"
)

type GlossaryController struct {
	service *service.GlossaryService
}

func NewGlossaryController(service *service.GlossaryService) *GlossaryController {
	return &GlossaryController{service: service}
}

func (c *GlossaryController) AddItem(ctx context.Context, term, explanation, source, category string) (*entity.GlossaryItem, error) {
	return c.service.AddGlossaryItem(ctx, term, explanation, source, category)
}

func (c *GlossaryController) GetRecent(ctx context.Context, limit int) ([]*entity.GlossaryItem, error) {
	return c.service.GetRecentGlossary(ctx, limit)
}

func (c *GlossaryController) Search(ctx context.Context, term string) (*entity.GlossaryItem, error) {
	return c.service.SearchByTerm(ctx, term)
}

func (c *GlossaryController) GetByCategory(ctx context.Context, category string, limit int) ([]*entity.GlossaryItem, error) {
	return c.service.GetByCategory(ctx, category, limit)
}
