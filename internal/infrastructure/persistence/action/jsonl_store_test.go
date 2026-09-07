package action_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	actionstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestJSONLStoreActionAttemptRoundTrip(t *testing.T) {
	t.Parallel()
	store, err := actionstore.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	action := domainaction.Action{
		ActionID:  modulecore.NewActionID(),
		TaskID:    modulecore.NewTaskID(),
		RunID:     modulecore.NewRunID(),
		Kind:      domainaction.KindExternalSend,
		Status:    domainaction.StatusOpen,
		CreatedAt: now,
		UpdatedAt: now,
	}
	attempt := domainaction.Attempt{
		AttemptID:   modulecore.NewAttemptID(),
		ActionID:    action.ActionID,
		StartReason: domainaction.AttemptStartReasonFirst,
		Status:      domainaction.AttemptStatusRunning,
		StartedAt:   now,
	}
	action.CurrentAttemptID = attempt.AttemptID
	if err := store.SaveAction(ctx, action); err != nil {
		t.Fatalf("save action: %v", err)
	}
	if err := store.SaveAttempt(ctx, attempt); err != nil {
		t.Fatalf("save attempt: %v", err)
	}
	got, err := store.GetAction(ctx, action.ActionID)
	if err != nil {
		t.Fatalf("get action: %v", err)
	}
	if got.ActionID != action.ActionID || got.Kind != domainaction.KindExternalSend {
		t.Fatalf("unexpected action: %+v", got)
	}
	gotAttempt, err := store.GetAttempt(ctx, attempt.AttemptID)
	if err != nil {
		t.Fatalf("get attempt: %v", err)
	}
	if gotAttempt.AttemptID != attempt.AttemptID {
		t.Fatalf("unexpected attempt: %+v", gotAttempt)
	}
}
