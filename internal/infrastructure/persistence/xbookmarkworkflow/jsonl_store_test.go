package xbookmarkworkflow

import (
	"context"
	"path/filepath"
	"testing"

	domainworkflow "github.com/Nyukimin/RenCrow_CORE/internal/domain/xbookmarkworkflow"
)

func TestJSONLStoreKeepsLatestResultAndFilters(t *testing.T) {
	store := NewJSONLStore(filepath.Join(t.TempDir(), "x_bookmark_workflows.jsonl"))
	ctx := context.Background()
	first := domainworkflow.Result{ID: "one", SourceRecordID: "source-1", Workflow: domainworkflow.WorkflowImagePromptDraw, Status: domainworkflow.StatusBlocked, UpdatedAt: "2026-08-05T01:00:00Z"}
	latest := first
	latest.Status = domainworkflow.StatusCompleted
	latest.UpdatedAt = "2026-08-05T02:00:00Z"
	other := domainworkflow.Result{ID: "two", SourceRecordID: "source-2", Workflow: domainworkflow.WorkflowAITipRenCrowEvaluation, Status: domainworkflow.StatusSkipped, UpdatedAt: "2026-08-05T03:00:00Z"}
	for _, value := range []domainworkflow.Result{first, other, latest} {
		if err := store.Save(ctx, value); err != nil {
			t.Fatalf("Save failed: %v", err)
		}
	}
	got, ok, err := store.Get(ctx, "one")
	if err != nil || !ok || got.Status != domainworkflow.StatusCompleted {
		t.Fatalf("Get did not return latest value: got=%+v ok=%v err=%v", got, ok, err)
	}
	values, err := store.List(ctx, domainworkflow.ResultQuery{Workflow: domainworkflow.WorkflowImagePromptDraw, Limit: 10})
	if err != nil || len(values) != 1 || values[0].ID != "one" {
		t.Fatalf("filtered List failed: values=%+v err=%v", values, err)
	}
}
