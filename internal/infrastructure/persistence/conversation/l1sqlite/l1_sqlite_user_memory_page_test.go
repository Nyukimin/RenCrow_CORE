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

	items, hasMore, err := store.ListUserMemoriesPage(ctx, "ren", MemoryStateCandidate, false, "GPU", 1, 0)
	if err != nil {
		t.Fatalf("ListUserMemoriesPage search: %v", err)
	}
	if hasMore || len(items) != 1 || items[0].Statement != "middle GPU candidate" {
		t.Fatalf("unexpected search page has_more=%v items=%+v", hasMore, items)
	}

	items, hasMore, err = store.ListUserMemoriesPage(ctx, "ren", MemoryStateCandidate, false, "", 1, 1)
	if err != nil {
		t.Fatalf("ListUserMemoriesPage offset: %v", err)
	}
	if !hasMore || len(items) != 1 {
		t.Fatalf("unexpected offset page has_more=%v items=%+v", hasMore, items)
	}

	if _, err := store.ForgetUserMemory(ctx, items[0].ID, "projection sync test"); err != nil {
		t.Fatalf("ForgetUserMemory: %v", err)
	}
	active, _, err := store.ListUserMemoriesPage(ctx, "ren", MemoryStateCandidate, false, items[0].Statement, 10, 0)
	if err != nil {
		t.Fatalf("ListUserMemoriesPage after forget: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("inactive memory remained in search projection: %+v", active)
	}
}

func TestUserMemoryViewerSearchIndexCoversSearchText(t *testing.T) {
	store, err := NewL1SQLiteStore(l1TestTempDir(t) + "/l1.db")
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	rows, err := store.db.Query(`PRAGMA index_info('idx_l1_user_memory_viewer_search_cover')`)
	if err != nil {
		t.Fatalf("index_info: %v", err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var seq, cid int
		var name string
		if err := rows.Scan(&seq, &cid, &name); err != nil {
			t.Fatalf("scan index_info: %v", err)
		}
		columns[name] = true
	}
	for _, want := range []string{"namespace", "memory_state", "active", "created_at", "id", "statement", "evidence_text"} {
		if !columns[want] {
			t.Fatalf("search covering index missing %s: %+v", want, columns)
		}
	}
}
