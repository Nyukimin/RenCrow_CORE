package l1sqlite

import (
	"context"
	"testing"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

func TestListUserMemoriesPageMakesOlderCandidatesSearchable(t *testing.T) {
	store, err := NewL1SQLiteStore(l1TestTempDir(t) + "/l1.db")
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	for _, statement := range []string{"newest candidate", "middle GPU candidate", "oldest candidate"} {
		if _, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
			UserID: "ren", Type: domainmemory.UserMemoryTypeProfile, Statement: statement,
			State: MemoryStateCandidate, EvidenceEventIDs: []string{"chatgpt_export:test"},
			Confidence: 0.9, Sensitivity: "normal", Scope: "global", Source: "chatgpt_import",
		}); err != nil {
			t.Fatalf("CreateUserMemory(%q): %v", statement, err)
		}
	}

	items, total, err := store.ListUserMemoriesPage(ctx, "ren", MemoryStateCandidate, false, "GPU", 1, 0)
	if err != nil {
		t.Fatalf("ListUserMemoriesPage search: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].Statement != "middle GPU candidate" {
		t.Fatalf("unexpected search page total=%d items=%+v", total, items)
	}

	items, total, err = store.ListUserMemoriesPage(ctx, "ren", MemoryStateCandidate, false, "", 1, 1)
	if err != nil {
		t.Fatalf("ListUserMemoriesPage offset: %v", err)
	}
	if total != 3 || len(items) != 1 {
		t.Fatalf("unexpected offset page total=%d items=%+v", total, items)
	}
}
