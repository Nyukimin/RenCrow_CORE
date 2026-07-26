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

// TestNewGlossaryItemGeneratesUniqueIDs は連続生成したIDが衝突しないことを確認する
//
// ID は PRIMARY KEY であり、保存は INSERT OR REPLACE のため、衝突すると
// エラーにならず先行レコードを無言で上書きする。クロック粒度が粗い環境
// （Windowsでは100ns〜1ms程度）で時刻のみのIDは確実に衝突する。
func TestNewGlossaryItemGeneratesUniqueIDs(t *testing.T) {
	const count = 1000
	seen := make(map[string]int, count)
	for i := 0; i < count; i++ {
		item := NewGlossaryItem("term", "explanation", "source", "category")
		if prev, dup := seen[item.ID]; dup {
			t.Fatalf("duplicate ID %q generated at iteration %d and %d", item.ID, prev, i)
		}
		seen[item.ID] = i
	}
}

func TestGenerateIDIsUniqueUnderConcurrency(t *testing.T) {
	const goroutines = 8
	const perGoroutine = 200

	results := make(chan string, goroutines*perGoroutine)
	done := make(chan struct{})
	for g := 0; g < goroutines; g++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := 0; i < perGoroutine; i++ {
				results <- generateID()
			}
		}()
	}
	for g := 0; g < goroutines; g++ {
		<-done
	}
	close(results)

	seen := make(map[string]struct{}, goroutines*perGoroutine)
	for id := range results {
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate ID %q generated concurrently", id)
		}
		seen[id] = struct{}{}
	}
}
