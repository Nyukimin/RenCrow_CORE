package taskmanager

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestEnsureGameTaskCreatedUsesDurableGlobalCreateReceipt(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := taskpersistence.NewJSONLStore(filepath.Join(root, "tasks"))
	if err != nil {
		t.Fatalf("open JSONL Task writer: %v", err)
	}
	manager := New(store, DefaultParallelLimits())
	requestID := modulecore.NewRequestID()
	draft, shared := gameTaskCreateInput(t, "first title", "initial plan")
	first, err := manager.EnsureGameTaskCreated(ctx, requestID, "ren", draft, shared)
	if err != nil {
		t.Fatalf("EnsureGameTaskCreated() first: %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("close first Task writer: %v", err)
	}

	store, err = taskpersistence.NewJSONLStore(filepath.Join(root, "tasks"))
	if err != nil {
		t.Fatalf("reopen JSONL Task writer: %v", err)
	}
	manager = New(store, DefaultParallelLimits())
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close reopened Task writer: %v", err)
		}
	})

	replayed, err := manager.EnsureGameTaskCreated(ctx, requestID, "ren", draft, shared)
	if err != nil {
		t.Fatalf("EnsureGameTaskCreated() replay after reopen: %v", err)
	}
	if replayed.TaskID != first.TaskID {
		t.Fatalf("replayed task_id=%s, first=%s", replayed.TaskID, first.TaskID)
	}
	if replayed.Route != domaintask.RouteGAME || replayed.OwnerID != "ren" || replayed.Title != "first title" {
		t.Fatalf("replayed Task identity=%+v", replayed)
	}
	if replayed.Activity == nil || replayed.Activity.Kind != domaintask.ActivityKindGame || replayed.Activity.Game == nil ||
		replayed.Activity.Game.GameID != shared.GameSpec.GameID || replayed.Activity.Phase != "preparing" || len(replayed.Activity.Participants) != len(shared.GameSpec.Personas) {
		t.Fatalf("typed GAME Task activity metadata=%#v", replayed.Activity)
	}

	changedDraft, changedShared := gameTaskCreateInput(t, "changed title", "initial plan")
	if _, err := manager.EnsureGameTaskCreated(ctx, requestID, "ren", changedDraft, changedShared); !errors.Is(err, taskpersistence.ErrTaskOperationConflict) {
		t.Fatalf("changed title retry error=%v, want operation conflict", err)
	}
	changedDraft, changedShared = gameTaskCreateInput(t, "first title", "changed plan")
	if _, err := manager.EnsureGameTaskCreated(ctx, requestID, "ren", changedDraft, changedShared); !errors.Is(err, taskpersistence.ErrTaskOperationConflict) {
		t.Fatalf("changed plan retry error=%v, want operation conflict", err)
	}

	found, exists, err := manager.LookupGameTaskCreation(ctx, requestID, "ren", draft, shared)
	if err != nil || !exists || found.TaskID != first.TaskID {
		t.Fatalf("LookupGameTaskCreation()=(%+v,%t,%v), want same Task", found, exists, err)
	}
	missingID := modulecore.NewRequestID()
	if missing, exists, err := manager.LookupGameTaskCreation(ctx, missingID, "ren", draft, shared); err != nil || exists || missing.TaskID != "" {
		t.Fatalf("missing lookup=(%+v,%t,%v), want absent without creation", missing, exists, err)
	}
	items, err := manager.List(ctx, domaintask.Filter{Route: domaintask.RouteGAME, Limit: 10})
	if err != nil || len(items) != 1 || items[0].TaskID != first.TaskID {
		t.Fatalf("GAME Tasks after idempotent retries=%+v err=%v, want exactly one", items, err)
	}
	contextValue, err := manager.Context(ctx, first.TaskID)
	if err != nil || contextValue.GameSpec == nil || contextValue.GameProgress == nil || contextValue.GameProgress.LaunchKey != string(first.TaskID) {
		t.Fatalf("stored GAME context=%+v err=%v", contextValue, err)
	}
}

func TestEnsureGameTaskCreatedRejectsMissingIdentityAndNonGameInput(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	manager := New(store, DefaultParallelLimits())
	defer manager.Close()
	requestID := modulecore.NewRequestID()
	draft, shared := gameTaskCreateInput(t, "valid title", "plan")
	if _, err := manager.EnsureGameTaskCreated(ctx, "", "ren", draft, shared); err == nil {
		t.Fatal("missing request_id was accepted")
	}
	if _, err := manager.EnsureGameTaskCreated(ctx, requestID, "", draft, shared); err == nil {
		t.Fatal("missing authenticated owner was accepted")
	}
	draft.Route = domaintask.RouteCHAT
	if _, err := manager.EnsureGameTaskCreated(ctx, requestID, "ren", draft, shared); err == nil {
		t.Fatal("non-GAME route was accepted")
	}
	items, err := manager.List(ctx, domaintask.Filter{Limit: 10})
	if err != nil || len(items) != 0 {
		t.Fatalf("invalid create wrote Tasks=%+v err=%v", items, err)
	}
}

func gameTaskCreateInput(t *testing.T, title, plan string) (domaintask.Task, domaintask.SharedRoleContext) {
	t.Helper()
	spec, err := domaintask.NormalizeGameSpec(domaintask.GameSpec{GameID: "herzog_zwei", Personas: []string{"aka", "ao", "kin", "gin"}, Turns: 120, Mode: "realtime"})
	if err != nil {
		t.Fatal(err)
	}
	draft := domaintask.Task{Title: title, Route: domaintask.RouteGAME, OwnerID: "ren", Assignee: "mio", Priority: domaintask.PriorityNormal}
	shared := domaintask.SharedRoleContext{GameSpec: &spec, UserIntent: title, CurrentPlan: plan}
	return draft, shared
}
