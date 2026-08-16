package l1sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

// RED: the owner lifecycle plan must have a dedicated SQLite table in the
// real L1 schema; a type-only implementation is not sufficient.
func TestL1SQLiteLifecyclePlanSchemaExists(t *testing.T) {
	store := newLifecycleREDStore(t)
	defer store.Close()
	var name string
	err := store.db.QueryRowContext(context.Background(), `
SELECT name FROM sqlite_master
WHERE type = 'table' AND name = 'l1_user_memory_lifecycle_plan'
`).Scan(&name)
	if err != nil {
		t.Fatalf("lifecycle plan schema lookup failed: %v", err)
	}
	if name != "l1_user_memory_lifecycle_plan" {
		t.Fatalf("lifecycle plan table=%q, want dedicated table", name)
	}
}

func newLifecycleREDStore(t *testing.T) *L1SQLiteStore {
	t.Helper()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "lifecycle-red.db"))
	if err != nil {
		t.Fatalf("new l1 store: %v", err)
	}
	if store == nil || store.db == nil {
		t.Fatal("store database is nil")
	}
	return store
}
