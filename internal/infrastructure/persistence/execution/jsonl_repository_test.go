package execution

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	domain "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestJSONLRepository_CreateUpdateCount(t *testing.T) {
	repo, err := NewJSONLRepository(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatalf("NewJSONLRepository failed: %v", err)
	}

	taskID := modulecore.NewTaskID()
	rec := domain.Record{
		TaskID:    taskID,
		ActionID:  "a1",
		Tool:      "shell",
		Decision:  domain.DecisionAllow,
		Status:    domain.StatusRunning,
		StartedAt: time.Now().UTC(),
	}
	if err := repo.Create(context.Background(), rec); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	updated, err := repo.UpdateStatus(context.Background(), taskID, "a1", domain.StatusSucceeded, "")
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

func TestJSONLRepositoryRejectsInvalidTaskIdentity(t *testing.T) {
	repo, err := NewJSONLRepository(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatalf("NewJSONLRepository failed: %v", err)
	}

	err = repo.Create(context.Background(), domain.Record{TaskID: "legacy", ActionID: "a1", Status: domain.StatusRunning, StartedAt: time.Now().UTC()})
	if err == nil {
		t.Fatal("expected invalid task identity rejection")
	}
}
