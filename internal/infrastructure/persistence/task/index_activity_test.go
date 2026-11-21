package task

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func newActivityIndexTestStore(t *testing.T, root string) *JSONLStore {
	t.Helper()
	store, err := NewJSONLStoreWithOptions(root, OpenOptions{Index: true, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close activity index store: %v", err)
		}
	})
	return store
}

func newActivityIndexTestRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp(os.TempDir(), "activity-index-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove activity index store: %v", err)
		}
	})
	return root
}

func newActivityIndexTestTask(actorID string, now time.Time) domaintask.Task {
	started := now
	return domaintask.Task{
		TaskID: modulecore.NewTaskID(), Title: "Evaluating next activity", Assignee: actorID,
		Route: domaintask.RouteGeneral, Status: domaintask.StatusRunning, Priority: domaintask.PriorityNormal,
		InterruptPolicy: domaintask.InterruptSilent, ReadOnly: true, CreatedAt: now, UpdatedAt: now, StartedAt: &started,
		Activity: &domaintask.ActivityMetadata{
			Kind: domaintask.ActivityKindEvaluating, Label: "Evaluating next activity", Phase: "evaluating",
			Passive: true, Revision: 1,
			Participants: []domaintask.ActivityParticipant{{ActorID: actorID, IsCurrent: true, Revision: 1}},
		},
	}
}

func saveNewActivityIndexTask(ctx context.Context, tx domaintask.Store, value domaintask.Task) error {
	writer, ok := tx.(domaintask.ActivityTaskWriter)
	if !ok {
		return domaintask.ErrActivityTaskWriterUnsupported
	}
	return writer.SaveNewPassiveActivityTask(ctx, value)
}

func TestCurrentActivityIndexEmptyReadAndSidecarRebuild(t *testing.T) {
	root := newActivityIndexTestRoot(t)
	store := newActivityIndexTestStore(t, root)
	ctx := context.Background()
	if current, err := store.CurrentActivityTasks(ctx, []string{"shiro"}); err != nil || len(current) != 0 {
		t.Fatalf("empty current activity read=%#v err=%v", current, err)
	}
	task := newActivityIndexTestTask("shiro", time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC))
	if err := store.ActivityTransaction(ctx, []modulecore.TaskID{task.TaskID}, func(tx domaintask.Store) error {
		return saveNewActivityIndexTask(ctx, tx, task)
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = newActivityIndexTestStore(t, root)
	current, err := store.CurrentActivityTasks(ctx, []string{"shiro"})
	if err != nil || current["shiro"].TaskID != task.TaskID {
		t.Fatalf("rebuilt current activity=%#v err=%v", current, err)
	}
}

func TestActiveExternalGameIndexRebuildIncludesOffCurrentTask(t *testing.T) {
	root := newActivityIndexTestRoot(t)
	store := newActivityIndexTestStore(t, root)
	ctx := context.Background()
	now := time.Date(2026, 11, 21, 13, 0, 0, 0, time.UTC)
	started := now
	game := domaintask.Task{
		TaskID: modulecore.NewTaskID(), Title: "Playing nethack", Assignee: "aka", Route: domaintask.RouteGeneral,
		Status: domaintask.StatusRunning, Priority: domaintask.PriorityNormal, InterruptPolicy: domaintask.InterruptSilent,
		ReadOnly: true, CreatedAt: now, UpdatedAt: now, StartedAt: &started,
		Activity: &domaintask.ActivityMetadata{Kind: domaintask.ActivityKindGame, Label: "Playing nethack", Phase: "playing", Passive: true, Revision: 1,
			Game:         &domaintask.ActivityGameReference{GameID: "nethack", SessionID: "session_1"},
			Participants: []domaintask.ActivityParticipant{{ActorID: "aka", Role: "player", Revision: 1}, {ActorID: "shiro", Role: "player", Revision: 1}}},
	}
	if err := store.ActivityTransaction(ctx, []modulecore.TaskID{game.TaskID}, func(tx domaintask.Store) error {
		return saveNewActivityIndexTask(ctx, tx, game)
	}); err != nil {
		t.Fatal(err)
	}
	active, err := store.ListActiveExternalGameActivities(ctx)
	if err != nil || len(active) != 1 || active[0].TaskID != game.TaskID {
		t.Fatalf("off-current active Games=%#v err=%v", active, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = newActivityIndexTestStore(t, root)
	active, err = store.ListActiveExternalGameActivities(ctx)
	if err != nil || len(active) != 1 || active[0].TaskID != game.TaskID {
		t.Fatalf("rebuilt off-current active Games=%#v err=%v", active, err)
	}
}

func TestCurrentActivityIndexValidatesFinalAtomicHandoffAndRollback(t *testing.T) {
	root := newActivityIndexTestRoot(t)
	store := newActivityIndexTestStore(t, root)
	ctx := context.Background()
	now := time.Date(2026, 11, 21, 11, 0, 0, 0, time.UTC)
	previous := newActivityIndexTestTask("shiro", now)
	if err := store.ActivityTransaction(ctx, []modulecore.TaskID{previous.TaskID}, func(tx domaintask.Store) error {
		return saveNewActivityIndexTask(ctx, tx, previous)
	}); err != nil {
		t.Fatal(err)
	}
	next := newActivityIndexTestTask("shiro", now.Add(time.Minute))
	if err := store.ActivityTransaction(ctx, []modulecore.TaskID{next.TaskID, previous.TaskID}, func(tx domaintask.Store) error {
		if err := saveNewActivityIndexTask(ctx, tx, next); err != nil {
			return err
		}
		writer := tx.(domaintask.ActivityTaskWriter)
		old, err := writer.GetTaskActivity(ctx, previous.TaskID)
		if err != nil {
			return err
		}
		old.Activity.Participants[0].IsCurrent = false
		old.Activity.Participants[0].Revision = 2
		old.Activity.Revision = 2
		old.UpdatedAt = now.Add(time.Minute)
		return writer.SaveTaskActivity(ctx, old)
	}); err != nil {
		t.Fatalf("atomic handoff with next Task appended first: %v", err)
	}
	current, err := store.CurrentActivityTasks(ctx, []string{"shiro"})
	if err != nil || current["shiro"].TaskID != next.TaskID {
		t.Fatalf("current after handoff=%#v err=%v", current, err)
	}
	current["shiro"].Activity.Participants[0].IsCurrent = false
	current["shiro"].Activity.Participants[0].Role = "mutated"
	rollback := errors.New("injected activity write failure")
	err = store.ActivityTransaction(ctx, []modulecore.TaskID{next.TaskID}, func(tx domaintask.Store) error {
		writer := tx.(domaintask.ActivityTaskWriter)
		latest, err := writer.GetTaskActivity(ctx, next.TaskID)
		if err != nil {
			return err
		}
		latest.Activity.Phase = "mutated"
		latest.Activity.Revision++
		latest.UpdatedAt = now.Add(2 * time.Minute)
		if err := writer.SaveTaskActivity(ctx, latest); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("injected rollback err=%v", err)
	}
	unchanged, err := store.CurrentActivityTasks(ctx, []string{"shiro"})
	if err != nil || unchanged["shiro"].TaskID != next.TaskID || unchanged["shiro"].Activity.Phase != "evaluating" || unchanged["shiro"].Activity.Participants[0].Role != "" {
		t.Fatalf("rollback/current snapshot mutation leaked: %#v err=%v", unchanged, err)
	}
}

func TestTransactionTaskCloneCopiesActivityGameReference(t *testing.T) {
	original := newActivityIndexTestTask("shiro", time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC))
	original.Activity.Kind = domaintask.ActivityKindGame
	original.Activity.Label = "Playing nethack"
	original.Activity.Game = &domaintask.ActivityGameReference{GameID: "nethack", SessionID: "session_1"}
	cloned := cloneTransactionTask(original)
	cloned.Activity.Game.SessionID = "mutated"
	cloned.Activity.Participants[0].Role = "mutated"
	if original.Activity.Game.SessionID != "session_1" || original.Activity.Participants[0].Role != "" {
		t.Fatalf("Activity clone shares nested references with source: %#v", original.Activity)
	}
}
