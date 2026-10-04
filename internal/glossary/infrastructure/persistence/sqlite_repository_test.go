package persistence

import (
	"context"
	"errors"
	"strings"
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

func TestGlossaryOwnerMutationReceiptIsAtomicBoundAndDurable(t *testing.T) {
	path := t.TempDir() + "/glossary-owner-receipt.db"
	ctx := context.Background()
	identity := GlossaryOperationIdentity{OpID: "glossary-candidate-commit", Operation: "save_candidate", PayloadHash: strings.Repeat("a", 64)}
	candidate := testGlossaryCandidate()
	repo, err := NewSQLiteGlossaryRepository(path)
	if err != nil {
		t.Fatalf("NewSQLiteGlossaryRepository: %v", err)
	}
	if err := repo.SaveCandidateForStorageHostOperation(ctx, identity, candidate); err != nil {
		t.Fatalf("SaveCandidateForStorageHostOperation: %v", err)
	}
	receipt, found, err := repo.LookupStorageHostOperationReceipt(ctx, identity)
	if err != nil || !found || receipt.Identity != identity || string(receipt.ResultJSON) != "null" {
		t.Fatalf("owner receipt=%+v found=%v err=%v", receipt, found, err)
	}
	if err := repo.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := NewSQLiteGlossaryRepository(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	if err := reopened.SaveCandidateForStorageHostOperation(ctx, identity, candidate); err != nil {
		t.Fatalf("exact replay after owner reopen: %v", err)
	}
	got, ok, err := reopened.FindCandidateByID(ctx, candidate.ID)
	if err != nil || !ok || got != candidate {
		t.Fatalf("candidate after exact replay=%+v found=%v err=%v", got, ok, err)
	}
	changed := identity
	changed.PayloadHash = strings.Repeat("b", 64)
	if err := reopened.SaveCandidateForStorageHostOperation(ctx, changed, candidate); !errors.Is(err, ErrGlossaryOperationConflict) {
		t.Fatalf("same op_id with changed payload err=%v, want ErrGlossaryOperationConflict", err)
	}
	receipt, found, err = reopened.LookupStorageHostOperationReceipt(ctx, changed)
	if err != nil || !found || receipt.Identity != identity {
		t.Fatalf("changed-payload lookup=%+v found=%v err=%v, want original durable binding", receipt, found, err)
	}
}
