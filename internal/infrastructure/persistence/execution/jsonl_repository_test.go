package execution

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	domain "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
)

func TestJSONLRepository_CreateUpdateCount(t *testing.T) {
	repo, err := NewJSONLRepository(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatalf("NewJSONLRepository failed: %v", err)
	}

	rec := domain.Record{
		JobID:     "j1",
		ActionID:  "a1",
		Tool:      "shell",
		Decision:  domain.DecisionAllow,
		Status:    domain.StatusRunning,
		StartedAt: time.Now().UTC(),
	}
	if err := repo.Create(context.Background(), rec); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	updated, err := repo.UpdateStatus(context.Background(), "j1", "a1", domain.StatusSucceeded, "")
	if err != nil {
		t.Fatalf("UpdateStatus failed: %v", err)
	}
	if updated.Status != domain.StatusSucceeded {
		t.Fatalf("unexpected status: %s", updated.Status)
	}
	if updated.FinishedAt == nil {
		t.Fatal("expected finished_at to be set")
	}

	counts, err := repo.CountByStatus(context.Background())
	if err != nil {
		t.Fatalf("CountByStatus failed: %v", err)
	}
	if counts[domain.StatusSucceeded] != 1 {
		t.Fatalf("expected succeeded=1, got %d", counts[domain.StatusSucceeded])
	}
}
