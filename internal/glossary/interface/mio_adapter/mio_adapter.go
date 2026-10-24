package mio_adapter

import (
	"context"
	"fmt"

	"github.com/Nyukimin/RenCrow_CORE/internal/glossary/application/service"
)

type MioGlossaryAdapter struct {
	glossaryService *service.GlossaryService
}

func NewMioGlossaryAdapter(service *service.GlossaryService) *MioGlossaryAdapter {
	return &MioGlossaryAdapter{glossaryService: service}
}

func (a *MioGlossaryAdapter) RefreshRecentPromptSnapshot(ctx context.Context) error {
	return a.glossaryService.RefreshRecentPromptSnapshot(ctx)
}

func (a *MioGlossaryAdapter) GetContextForTerm(ctx context.Context, term string) (string, error) {
	item, err := a.glossaryService.SearchByTerm(ctx, term)
	if err != nil {
		return "", err
	}

	if item == nil {
		return "", nil
	}

	return fmt.Sprintf("Recent context: %s (Source: %s)", item.Explanation, item.Source), nil
}

func (a *MioGlossaryAdapter) GetRecentTopics(ctx context.Context, limit int) ([]string, error) {
	_, topics := a.glossaryService.RecentPromptSnapshot(limit)
	return topics, nil
}

func (a *MioGlossaryAdapter) GetRecentContext(ctx context.Context, limit int) (string, error) {
	contextText, _ := a.glossaryService.RecentPromptSnapshot(limit)
	return contextText, nil
}
