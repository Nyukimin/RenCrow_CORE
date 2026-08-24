package l1sqlite

import (
	"context"
	"strings"
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

func TestUserMemoryViewerSearchUsesFTS5Trigram(t *testing.T) {
	store, err := NewL1SQLiteStore(l1TestTempDir(t) + "/l1.db")
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	var schema string
	if err := store.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'l1_user_memory_viewer_fts'`).Scan(&schema); err != nil {
		t.Fatalf("read FTS schema: %v", err)
	}
	if !strings.Contains(schema, "fts5") || !strings.Contains(schema, "trigram") {
		t.Fatalf("unexpected FTS schema: %s", schema)
	}
}

func TestSQLiteRuntimeSupportsUserMemoryFTS5(t *testing.T) {
	store, err := NewL1SQLiteStore(l1TestTempDir(t) + "/l1.db")
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	if _, err := store.db.Exec(`CREATE VIRTUAL TABLE temp.user_memory_fts_probe USING fts5(statement, evidence, tokenize='trigram')`); err != nil {
		t.Fatalf("FTS5 is required for bounded User Memory search: %v", err)
	}
}
