package mio_adapter

import (
	"context"
	"fmt"
	"strings"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/glossary/application/service"
)

type MioGlossaryAdapter struct {
	glossaryService *service.GlossaryService
}

func NewMioGlossaryAdapter(service *service.GlossaryService) *MioGlossaryAdapter {
	return &MioGlossaryAdapter{glossaryService: service}
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
	items, err := a.glossaryService.GetRecentGlossary(ctx, limit)
	if err != nil {
		return nil, err
	}
	
	var topics []string
	for _, item := range items {
		topics = append(topics, fmt.Sprintf("%s: %s", item.Term, item.Explanation))
	}

	return topics, nil
}

func (a *MioGlossaryAdapter) GetRecentContext(ctx context.Context, limit int) (string, error) {
	items, err := a.glossaryService.GetRecentGlossary(ctx, limit)
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return "", nil
	}

	var b strings.Builder
	b.WriteString("最近語彙メモ:\n")
	for _, item := range items {
		b.WriteString("- ")
		b.WriteString(item.Term)
		b.WriteString(": ")
		b.WriteString(item.Explanation)
		if item.Source != "" {
			b.WriteString(" (")
			b.WriteString(item.Source)
			b.WriteString(")")
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String()), nil
}
