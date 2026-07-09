package persistence

import (
	"context"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/glossary/domain/entity"
)

func TestSQLiteGlossaryRepositoryCRUD(t *testing.T) {
	repo, err := NewSQLiteGlossaryRepository(t.TempDir() + "/glossary.db")
	if err != nil {
		t.Fatalf("NewSQLiteGlossaryRepository failed: %v", err)
	}
	defer repo.Close()

	ctx := context.Background()
	item := entity.NewGlossaryItem("Mio", "chat agent", "manual", "agent")
	if err := repo.Save(ctx, item); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	found, err := repo.FindByTerm(ctx, "Mio")
	if err != nil {
		t.Fatalf("FindByTerm failed: %v", err)
	}
	if found.ID != item.ID || found.Explanation != "chat agent" {
		t.Fatalf("unexpected found item: %#v", found)
	}

	recent, err := repo.FindRecent(ctx, 10)
	if err != nil {
		t.Fatalf("FindRecent failed: %v", err)
	}
	if len(recent) != 1 || recent[0].Term != "Mio" {
		t.Fatalf("unexpected recent items: %#v", recent)
	}

	byCategory, err := repo.FindByCategory(ctx, "agent", 10)
	if err != nil {
		t.Fatalf("FindByCategory failed: %v", err)
	}
	if len(byCategory) != 1 || byCategory[0].Term != "Mio" {
		t.Fatalf("unexpected category items: %#v", byCategory)
	}

	if err := repo.Delete(ctx, item.ID); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if _, err := repo.FindByTerm(ctx, "Mio"); err == nil {
		t.Fatal("expected FindByTerm error after delete")
	}
}

func TestSQLiteGlossaryRepositoryCloseNilSafe(t *testing.T) {
	var nilRepo *SQLiteGlossaryRepository
	if err := nilRepo.Close(); err != nil {
		t.Fatalf("nil Close failed: %v", err)
	}

	repo := &SQLiteGlossaryRepository{}
	if err := repo.Close(); err != nil {
		t.Fatalf("empty Close failed: %v", err)
	}
}
