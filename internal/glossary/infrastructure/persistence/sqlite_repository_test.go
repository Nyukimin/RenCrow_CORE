package persistence

import (
	"context"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/glossary/domain/entity"
)

func testGlossaryCandidate() entity.GlossaryCandidate {
	return entity.GlossaryCandidate{
		ID:          "glossary-candidate/sha256:test",
		Term:        "CandidateTerm",
		Explanation: "candidate explanation",
		SourceURL:   "https://example.com/source",
		Category:    "new_word",
		ProposedBy:  "shiro",
		State:       entity.GlossaryCandidateState,
		CreatedAt:   time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC),
	}
}

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

func TestSQLiteGlossaryRepositoryCandidateExactInsertAndFind(t *testing.T) {
	path := t.TempDir() + "/glossary.db"
	repo, err := NewSQLiteGlossaryRepository(path)
	if err != nil {
		t.Fatalf("NewSQLiteGlossaryRepository failed: %v", err)
	}
	defer repo.Close()

	ctx := context.Background()
	candidate := testGlossaryCandidate()
	if err := repo.SaveCandidate(ctx, candidate); err != nil {
		t.Fatalf("SaveCandidate failed: %v", err)
	}
	found, ok, err := repo.FindCandidateByID(ctx, candidate.ID)
	if err != nil || !ok {
		t.Fatalf("FindCandidateByID = %#v found=%v err=%v", found, ok, err)
	}
	if found != candidate {
		t.Fatalf("candidate = %#v, want %#v", found, candidate)
	}
	if _, ok, err := repo.FindCandidateByID(ctx, "missing-candidate"); err != nil || ok {
		t.Fatalf("missing candidate result found=%v err=%v", ok, err)
	}
	if err := repo.SaveCandidate(ctx, candidate); err == nil {
		t.Fatal("duplicate candidate insert must fail")
	}

	canonical := entity.NewGlossaryItem(candidate.Term, "canonical", "manual", candidate.Category)
	if err := repo.Save(ctx, canonical); err != nil {
		t.Fatalf("Save canonical item failed: %v", err)
	}
	item, err := repo.FindByTerm(ctx, candidate.Term)
	if err != nil || item.Explanation != "canonical" {
		t.Fatalf("canonical lookup = %#v err=%v", item, err)
	}
}

func TestSQLiteGlossaryRepositoryCandidateRejectsMalformedStoredRow(t *testing.T) {
	repo, err := NewSQLiteGlossaryRepository(t.TempDir() + "/glossary.db")
	if err != nil {
		t.Fatalf("NewSQLiteGlossaryRepository failed: %v", err)
	}
	defer repo.Close()

	_, err = repo.db.Exec(`
		INSERT INTO glossary_candidates
			(id, term, explanation, source_url, category, proposed_by, state, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, "malformed", "term", "explanation", "http://unsafe.example", "new_word", "shiro", entity.GlossaryCandidateState, time.Now().UTC())
	if err != nil {
		t.Fatalf("seed malformed candidate failed: %v", err)
	}
	if _, found, err := repo.FindCandidateByID(context.Background(), "malformed"); err == nil || found {
		t.Fatalf("malformed candidate result found=%v err=%v", found, err)
	}

	if _, err := repo.db.Exec(`UPDATE glossary_candidates SET state = ? WHERE id = ?`, "promoted", "malformed"); err != nil {
		t.Fatalf("corrupt candidate state failed: %v", err)
	}
	if _, found, err := repo.FindCandidateByID(context.Background(), "malformed"); err == nil || found {
		t.Fatalf("invalid state candidate result found=%v err=%v", found, err)
	}

}
