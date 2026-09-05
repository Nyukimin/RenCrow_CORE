package execution

import (
	"context"
	"testing"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestExecutionIdentityContextPreservesIndependentCanonicalIDs(t *testing.T) {
	taskID := modulecore.NewTaskID()
	traceID := modulecore.NewTraceID()
	ctx, err := WithIdentity(context.Background(), taskID, traceID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := IdentityFromContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.TaskID != taskID || got.TraceID != traceID {
		t.Fatalf("identity = %#v, want task %q trace %q", got, taskID, traceID)
	}
}

func TestExecutionIdentityContextFailsClosed(t *testing.T) {
	if _, err := IdentityFromContext(context.Background()); err == nil {
		t.Fatal("missing execution identity was accepted")
	}
	if _, err := WithIdentity(context.Background(), "legacy", modulecore.NewTraceID()); err == nil {
		t.Fatal("invalid task identity was accepted")
	}
	if _, err := WithIdentity(context.Background(), modulecore.NewTaskID(), "legacy"); err == nil {
		t.Fatal("invalid trace identity was accepted")
	}
}
