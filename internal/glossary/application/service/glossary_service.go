package service

import (
	"context"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/glossary/domain/entity"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/glossary/domain/repository"
)

type GlossaryService struct {
	repo repository.GlossaryRepository
}

func NewGlossaryService(repo repository.GlossaryRepository) *GlossaryService {
	return &GlossaryService{repo: repo}
}

func (s *GlossaryService) AddGlossaryItem(ctx context.Context, term, explanation, source, category string) (*entity.GlossaryItem, error) {
	item := entity.NewGlossaryItem(term, explanation, source, category)
	if err := s.repo.Save(ctx, item); err != nil {
		return nil, err
	}
	return item, nil
}

func (s *GlossaryService) GetRecentGlossary(ctx context.Context, limit int) ([]*entity.GlossaryItem, error) {
	return s.repo.FindRecent(ctx, limit)
}

func (s *GlossaryService) SearchByTerm(ctx context.Context, term string) (*entity.GlossaryItem, error) {
	return s.repo.FindByTerm(ctx, term)
}

func (s *GlossaryService) GetByCategory(ctx context.Context, category string, limit int) ([]*entity.GlossaryItem, error) {
	return s.repo.FindByCategory(ctx, category, limit)
}
