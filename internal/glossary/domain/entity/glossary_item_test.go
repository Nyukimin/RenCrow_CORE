package entity

import (
	"testing"
)

func TestNewGlossaryItem(t *testing.T) {
	term := "TestTerm"
	explanation := "Test explanation"
	source := "test_source"
	category := "test_category"
	
	item := NewGlossaryItem(term, explanation, source, category)
	
	if item.Term != term {
		t.Errorf("Expected term %s, got %s", term, item.Term)
	}
	
	if item.Explanation != explanation {
		t.Errorf("Expected explanation %s, got %s", explanation, item.Explanation)
	}
	
	if item.Source != source {
		t.Errorf("Expected source %s, got %s", source, item.Source)
	}
	
	if item.Category != category {
		t.Errorf("Expected category %s, got %s", category, item.Category)
	}
	
	if item.ID == "" {
		t.Error("Expected non-empty ID")
	}
	
	if item.CreatedAt.IsZero() {
		t.Error("Expected non-zero CreatedAt")
	}
	
	if item.UpdatedAt.IsZero() {
		t.Error("Expected non-zero UpdatedAt")
	}
	
	// Check that ID starts with "gloss_"
	if len(item.ID) <= 6 || item.ID[:6] != "gloss_" {
		t.Errorf("Expected ID to start with 'gloss_', got %s", item.ID)
	}
}
