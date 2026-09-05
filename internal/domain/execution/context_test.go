package execution

import (
	"context"
	"testing"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestExecutionIdentityContextPreservesIndependentCanonicalIDs(t *testing.T) {
	taskID := modulecore.NewTaskID()
	runID := modulecore.NewRunID()
	traceID := modulecore.NewTraceID()
	ctx, err := WithIdentity(context.Background(), taskID, runID, traceID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := IdentityFromContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.TaskID != taskID || got.RunID != runID || got.TraceID != traceID {
		t.Fatalf("identity = %#v, want task %q run %q trace %q", got, taskID, runID, traceID)
	}
}

func TestExecutionIdentityContextFailsClosed(t *testing.T) {
	if _, err := IdentityFromContext(context.Background()); err == nil {
		t.Fatal("missing execution identity was accepted")
	}
	if _, err := WithIdentity(context.Background(), "legacy", modulecore.NewRunID(), modulecore.NewTraceID()); err == nil {
		t.Fatal("invalid task identity was accepted")
	}
	if _, err := WithIdentity(context.Background(), modulecore.NewTaskID(), "legacy", modulecore.NewTraceID()); err == nil {
		t.Fatal("invalid run identity was accepted")
	}
	if _, err := WithIdentity(context.Background(), modulecore.NewTaskID(), modulecore.NewRunID(), "legacy"); err == nil {
		t.Fatal("invalid trace identity was accepted")
	}
	if _, err := WithIdentity(context.Background(), modulecore.NewTaskID(), "", modulecore.NewTraceID()); err == nil {
		t.Fatal("missing run identity was accepted")
	}
}

func TestExecutionIdentityContextRebindIsIdempotentOnlyForExactIdentity(t *testing.T) {
	taskID := modulecore.NewTaskID()
	runID := modulecore.NewRunID()
	traceID := modulecore.NewTraceID()
	ctx, err := WithIdentity(context.Background(), taskID, runID, traceID)
	if err != nil {
		t.Fatal(err)
	}
	rebound, err := WithIdentity(ctx, taskID, runID, traceID)
	if err != nil {
		t.Fatalf("exact identity rebind failed: %v", err)
	}
	if rebound != ctx {
		t.Fatal("exact identity rebind should preserve the existing context")
	}

	for name, ids := range map[string]struct {
		taskID  modulecore.TaskID
		runID   modulecore.RunID
		traceID modulecore.TraceID
	}{
		"task mismatch":  {taskID: modulecore.NewTaskID(), runID: runID, traceID: traceID},
		"run mismatch":   {taskID: taskID, runID: modulecore.NewRunID(), traceID: traceID},
		"trace mismatch": {taskID: taskID, runID: runID, traceID: modulecore.NewTraceID()},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := WithIdentity(ctx, ids.taskID, ids.runID, ids.traceID); err == nil {
				t.Fatal("different identity replaced an existing execution identity")
			}
		})
	}
}
