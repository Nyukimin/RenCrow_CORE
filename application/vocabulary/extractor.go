package vocabulary

import (
	"regexp"
	"strings"
)

// ExtractedTerm represents a term extracted from text
type ExtractedTerm struct {
	Term        string
	Description string
}

// TermExtractor extracts terms and creates simple descriptions
type TermExtractor struct {
	patterns []*regexp.Regexp
}

// NewTermExtractor creates a new term extractor
func NewTermExtractor() *TermExtractor {
	return &TermExtractor{
		patterns: []*regexp.Regexp{
			regexp.MustCompile(`\b[A-Z][a-z]+(?:\s+[A-Z][a-z]+)*\b`), // Capitalized phrases
			regexp.MustCompile(`\b[A-Z]{2,}\b`), // Acronyms
		},
	}
}

// ExtractTerms extracts potential terms from text
func (e *TermExtractor) ExtractTerms(text string) []ExtractedTerm {
	var terms []ExtractedTerm
	seen := make(map[string]bool)
	
	for _, pattern := range e.patterns {
		matches := pattern.FindAllString(text, -1)
		for _, match := range matches {
			// Skip common words and short matches
			if len(match) < 3 || e.isCommonWord(match) {
				continue
			}
			
			if !seen[match] {
				terms = append(terms, ExtractedTerm{
					Term:        match,
					Description: e.createDescription(match, text),
				})
				seen[match] = true
			}
		}
	}
	
	return terms
}

func (e *TermExtractor) isCommonWord(word string) bool {
	common := []string{"The", "And", "For", "With", "This", "That", "From", "Have", "Will"}
	for _, c := range common {
		if strings.EqualFold(word, c) {
			return true
		}
	}
	return false
}

func (e *TermExtractor) createDescription(term, context string) string {
	// Create a simple description based on context
	// In a real implementation, this would use NLP or pattern matching
	return "Mentioned in recent news: " + context[:min(50, len(context))]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
