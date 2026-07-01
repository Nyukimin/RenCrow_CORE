package vocabulary

// Repository defines the interface for glossary storage
type Repository interface {
	// Store adds or updates an entry
	Store(entry *Entry) error
	// FindByTerm returns entries matching the term (case-insensitive partial match)
	FindByTerm(term string) ([]*Entry, error)
	// FindRecent returns entries from the last N days
	FindRecent(days int) ([]*Entry, error)
	// GetAll returns all entries
	GetAll() ([]*Entry, error)
	// Clear removes all entries
	Clear() error
}
