package feed

import (
	"context"
	"fmt"
	"strings"

	"github.com/mmcdole/gofeed"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/glossary/domain/entity"
)

type RSSParser struct {
	feedURLs []string
}

func NewRSSParser(feedURLs []string) *RSSParser {
	return &RSSParser{feedURLs: feedURLs}
}

func (p *RSSParser) FetchAndParse(ctx context.Context) ([]*entity.GlossaryItem, error) {
	var allItems []*entity.GlossaryItem
	fp := gofeed.NewParser()

	for _, url := range p.feedURLs {
		feed, err := fp.ParseURLWithContext(url, ctx)
		if err != nil {
			continue // Skip failed feeds
		}

		for _, item := range feed.Items {
			if glossaryItem := p.extractFromItem(item, url); glossaryItem != nil {
				allItems = append(allItems, glossaryItem)
			}
		}
	}

	return allItems, nil
}

func (p *RSSParser) extractFromItem(item *gofeed.Item, source string) *entity.GlossaryItem {
	// Simple extraction logic - can be enhanced with NLP later
	title := item.Title
	description := item.Description
	
	// Extract potential terms (simplified - just use first few words)
	terms := p.extractPotentialTerms(title)
	if len(terms) == 0 {
		terms = p.extractPotentialTerms(description)
	}
	
	if len(terms) == 0 {
		return nil
	}
	
	// Use first term found
	term := terms[0]
	category := p.determineCategory(term, title)
	explanation := p.generateExplanation(term, title, description)
	
	return entity.NewGlossaryItem(term, explanation, source, category)
}

func (p *RSSParser) extractPotentialTerms(text string) []string {
	// Simple extraction - look for capitalized phrases
	words := strings.Fields(text)
	var terms []string
	
	for i := 0; i < len(words); i++ {
		word := strings.Trim(words[i], ".,!?;:\"'()[]{}")
		if len(word) > 2 && strings.ToUpper(word[:1]) == word[:1] {
			terms = append(terms, word)
		}
	}
	
	return terms
}

func (p *RSSParser) determineCategory(term, context string) string {
	// Simple category detection
	lowerContext := strings.ToLower(context)
	
	if strings.Contains(lowerContext, "company") || strings.Contains(lowerContext, "inc.") || 
	   strings.Contains(lowerContext, "corp") || strings.Contains(lowerContext, "ltd") {
		return "organization"
	}
	
	if strings.Contains(lowerContext, "city") || strings.Contains(lowerContext, "country") ||
	   strings.Contains(lowerContext, "region") || strings.Contains(lowerContext, "place") {
		return "location"
	}
	
	return "new_word"
}

func (p *RSSParser) generateExplanation(term, title, description string) string {
	// Create a simple explanation based on context
	context := title
	if len(description) > 0 {
		context = description
	}
	
	return fmt.Sprintf("Mentioned in news: %s", truncateString(context, 100))
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
