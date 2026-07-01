package controller

import (
	"context"
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/glossary/application/service"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/glossary/infrastructure/persistence"
)

func TestGlossaryControllerDelegatesToService(t *testing.T) {
	repo, err := persistence.NewSQLiteGlossaryRepository(t.TempDir() + "/glossary.db")
	if err != nil {
		t.Fatalf("NewSQLiteGlossaryRepository failed: %v", err)
	}
	defer repo.Close()

	ctrl := NewGlossaryController(service.NewGlossaryService(repo))
	ctx := context.Background()
	item, err := ctrl.AddItem(ctx, "RenCrow", "assistant", "manual", "project")
	if err != nil {
		t.Fatalf("AddItem failed: %v", err)
	}

	found, err := ctrl.Search(ctx, "RenCrow")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if found.ID != item.ID {
		t.Fatalf("unexpected search result: %#v", found)
	}

	recent, err := ctrl.GetRecent(ctx, 5)
	if err != nil || len(recent) != 1 {
		t.Fatalf("GetRecent = %#v, %v", recent, err)
	}
	category, err := ctrl.GetByCategory(ctx, "project", 5)
	if err != nil || len(category) != 1 {
		t.Fatalf("GetByCategory = %#v, %v", category, err)
	}
}
