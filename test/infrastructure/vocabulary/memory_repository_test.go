package vocabulary_test

import (
	"testing"
	"time"

	vocabDomain "github.com/Nyukimin/picoclaw_multiLLM/domain/vocabulary"
	vocabInfra "github.com/Nyukimin/picoclaw_multiLLM/infrastructure/vocabulary"
)

func TestMemoryRepository(t *testing.T) {
	repo := vocabInfra.NewMemoryRepository()
	
	entry1 := &vocabDomain.Entry{
		Term:        "AI Summit",
		Description: "Annual artificial intelligence conference",
		Source:      "Test",
		Timestamp:   time.Now(),
	}
	
	entry2 := &vocabDomain.Entry{
		Term:        "Quantum Computing",
		Description: "Next-generation computing technology",
		Source:      "Test",
		Timestamp:   time.Now().Add(-10 * 24 * time.Hour), // 10 days ago
	}
	
	// Test Store
	if err := repo.Store(entry1); err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	
	if err := repo.Store(entry2); err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	
	// Test FindByTerm
	results, err := repo.FindByTerm("AI")
	if err != nil {
		t.Fatalf("FindByTerm() error = %v", err)
	}
	
	if len(results) != 1 {
		t.Errorf("FindByTerm() got %d results, want 1", len(results))
	}
	
	// Test FindRecent
	recent, err := repo.FindRecent(7)
	if err != nil {
		t.Fatalf("FindRecent() error = %v", err)
	}
	
	if len(recent) != 1 {
		t.Errorf("FindRecent() got %d results, want 1", len(recent))
	}
	
	// Test GetAll
	all, err := repo.GetAll()
	if err != nil {
		t.Fatalf("GetAll() error = %v", err)
	}
	
	if len(all) != 2 {
		t.Errorf("GetAll() got %d results, want 2", len(all))
	}
}
