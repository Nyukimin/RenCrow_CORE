package vocabulary

import (
	"sort"
	"strings"
	"sync"
	"time"

	vocabDomain "github.com/Nyukimin/picoclaw_multiLLM/domain/vocabulary"
)

// MemoryRepository implements in-memory storage for glossary entries
type MemoryRepository struct {
	mu      sync.RWMutex
	entries map[string]*vocabDomain.Entry // key: term
}

// NewMemoryRepository creates a new in-memory repository
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		entries: make(map[string]*vocabDomain.Entry),
	}
}

// Store adds or updates an entry
func (r *MemoryRepository) Store(entry *vocabDomain.Entry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	
	r.entries[strings.ToLower(entry.Term)] = entry
	return nil
}

// FindByTerm returns entries matching the term
func (r *MemoryRepository) FindByTerm(term string) ([]*vocabDomain.Entry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	
	var results []*vocabDomain.Entry
	searchTerm := strings.ToLower(term)
	
	for key, entry := range r.entries {
		if strings.Contains(key, searchTerm) {
			results = append(results, entry)
		}
	}
	
	return results, nil
}

// FindRecent returns entries from the last N days
func (r *MemoryRepository) FindRecent(days int) ([]*vocabDomain.Entry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	
	var results []*vocabDomain.Entry
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	
	for _, entry := range r.entries {
		if entry.Timestamp.After(cutoff) {
			results = append(results, entry)
		}
	}
	
	// Sort by timestamp, newest first
	sort.Slice(results, func(i, j int) bool {
		return results[i].Timestamp.After(results[j].Timestamp)
	})
	
	return results, nil
}

// GetAll returns all entries
func (r *MemoryRepository) GetAll() ([]*vocabDomain.Entry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	
	results := make([]*vocabDomain.Entry, 0, len(r.entries))
	for _, entry := range r.entries {
		results = append(results, entry)
	}
	
	return results, nil
}

// Clear removes all entries
func (r *MemoryRepository) Clear() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	
	r.entries = make(map[string]*vocabDomain.Entry)
	return nil
}
