package durablestore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	domain "github.com/Nyukimin/RenCrow_CORE/internal/domain/durablestore"
)

func TestSQLiteStoreRoundTripAndRequiresExistingParent(t *testing.T) {
	if _, err := NewSQLiteStore(filepath.Join(t.TempDir(), "missing", "workflow.db")); err == nil {
		t.Fatal("store must not create an unconfigured parent directory")
	}
	dir := t.TempDir()
	store, err := NewSQLiteStore(filepath.Join(dir, "workflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	want := domain.WorkflowResult{Status: domain.StatusCompleted, Lifecycle: domain.LifecycleValidated, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), Requirement: domain.StorageRequirement{RequirementID: "sr-1", DedupeKey: "dedupe-1"}}
	if err := store.Save(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := store.FindByDedupeKey(context.Background(), "dedupe-1")
	if err != nil || got == nil || got.Requirement.RequirementID != "sr-1" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}
