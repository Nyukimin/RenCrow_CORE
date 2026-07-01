package vocabulary

// Service defines the business logic for glossary operations
type Service interface {
	// UpdateFromSources fetches from configured sources and updates the store
	UpdateFromSources() (int, error)
	// GetContext returns recent entries as context for LLM injection
	GetContext(maxEntries int) (string, error)
	// SearchTerms finds entries matching search terms
	SearchTerms(terms []string) ([]*Entry, error)
}
