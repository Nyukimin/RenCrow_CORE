package persistence

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSQLiteGlossaryRepositoryConnectionPolicy(t *testing.T) {
	repo, err := NewSQLiteGlossaryRepository(filepath.Join(t.TempDir(), "glossary.db"))
	if err != nil {
		t.Fatalf("NewSQLiteGlossaryRepository failed: %v", err)
	}
	defer repo.Close()

	if got := repo.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", got)
	}
	var busyTimeout int
	if err := repo.db.QueryRowContext(context.Background(), "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("query busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("busy_timeout = %d, want 5000", busyTimeout)
	}
}
