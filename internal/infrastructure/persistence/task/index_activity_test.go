package task

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
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

func TestActiveExternalGameReaderSupportsUnindexedBoundedSnapshot(t *testing.T) {
	root := newActivityIndexTestRoot(t)
	open := func() *JSONLStore {
		store, err := NewJSONLStoreWithOptions(root, OpenOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return store
	}
	store := open()
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close unindexed activity store: %v", err)
		}
	})
	ctx := context.Background()
	if activities, err := store.ListActiveExternalGameActivities(ctx); err != nil || len(activities) != 0 {
		t.Fatalf("empty unindexed active Game read=%#v err=%v", activities, err)
	}
	now := time.Date(2026, 11, 21, 14, 0, 0, 0, time.UTC)
	makeGame := func(status domaintask.Status, sessionID string) domaintask.Task {
		started := now
		task := domaintask.Task{
			TaskID: modulecore.NewTaskID(), Title: "Playing nethack", Assignee: "aka", Route: domaintask.RouteGeneral,
			Status: status, Priority: domaintask.PriorityNormal, InterruptPolicy: domaintask.InterruptSilent,
			ReadOnly: true, CreatedAt: now, UpdatedAt: now, StartedAt: &started,
			Activity: &domaintask.ActivityMetadata{
				Kind: domaintask.ActivityKindGame, Label: "Playing nethack", Phase: "playing", Passive: true, Revision: 1,
				Game:         &domaintask.ActivityGameReference{GameID: "nethack", SessionID: sessionID},
				Participants: []domaintask.ActivityParticipant{{ActorID: "aka", Role: "player", Revision: 1}},
			},
		}
		if domaintask.IsTerminal(status) {
			finished := now
			task.FinishedAt = &finished
		}
		return task
	}
	eligible := make([]domaintask.Task, 0, domaintask.MaxActiveExternalGameActivities)
	seed := make([]domaintask.Task, 0, domaintask.MaxActiveExternalGameActivities+2)
	for index := 0; index < domaintask.MaxActiveExternalGameActivities; index++ {
		game := makeGame(domaintask.StatusRunning, fmt.Sprintf("live-session-%d", index))
		eligible = append(eligible, game)
		seed = append(seed, game)
	}
	terminal := makeGame(domaintask.StatusRunning, "terminal-session")
	withRun := makeGame(domaintask.StatusRunning, "run-backed-session")
	seed = append(seed, terminal, withRun)
	seedIDs := make([]modulecore.TaskID, 0, len(seed))
	for _, task := range seed {
		seedIDs = append(seedIDs, task.TaskID)
	}
	if err := store.ActivityTransaction(ctx, seedIDs, func(tx domaintask.Store) error {
		for _, task := range seed {
			if err := saveNewActivityIndexTask(ctx, tx, task); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.TaskTransaction(ctx, terminal.TaskID, func(tx domaintask.Store) error {
		writer, ok := tx.(domaintask.ActivityTaskWriter)
		if !ok {
			return domaintask.ErrActivityTaskWriterUnsupported
		}
		latest, err := writer.GetTaskActivity(ctx, terminal.TaskID)
		if err != nil {
			return err
		}
		finished := now.Add(time.Minute)
		latest.Status = domaintask.StatusSucceeded
		latest.FinishedAt = &finished
		latest.UpdatedAt = finished
		return tx.SaveTask(ctx, latest)
	}); err != nil {
		t.Fatal(err)
	}
	completed := now
	generation, err := store.WriterGeneration()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRun(ctx, domaintask.Run{
		WriterGeneration: generation, RunID: modulecore.NewRunID(), TaskID: withRun.TaskID, StartReason: domaintask.RunStartReasonFirst,
		Assignee: "aka", Status: domaintask.RunStatusSucceeded, StartedAt: now, CompletedAt: &completed,
	}); err != nil {
		t.Fatal(err)
	}
	activities, err := store.ListActiveExternalGameActivities(ctx)
	if err != nil || len(activities) != domaintask.MaxActiveExternalGameActivities {
		t.Fatalf("64 unindexed active Games=%d err=%v, want exactly the bounded limit", len(activities), err)
	}
	want := make(map[modulecore.TaskID]struct{}, len(eligible))
	for _, task := range eligible {
		want[task.TaskID] = struct{}{}
	}
	var previous modulecore.TaskID
	for _, task := range activities {
		if _, found := want[task.TaskID]; !found || (previous != "" && task.TaskID <= previous) {
			t.Fatalf("unindexed result order/content is invalid at Task %s", task.TaskID)
		}
		previous = task.TaskID
	}
	indexed := newActivityIndexTestStore(t, newActivityIndexTestRoot(t))
	eligibleIDs := make([]modulecore.TaskID, 0, len(eligible))
	for _, task := range eligible {
		eligibleIDs = append(eligibleIDs, task.TaskID)
	}
	if err := indexed.ActivityTransaction(ctx, eligibleIDs, func(tx domaintask.Store) error {
		for _, task := range eligible {
			if err := saveNewActivityIndexTask(ctx, tx, task); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	indexedActivities, err := indexed.ListActiveExternalGameActivities(ctx)
	if err != nil || len(indexedActivities) != len(activities) {
		t.Fatalf("indexed active Game comparison=%d err=%v", len(indexedActivities), err)
	}
	for index := range activities {
		if activities[index].TaskID != indexedActivities[index].TaskID {
			t.Fatalf("indexed/non-indexed order differs at %d: %s != %s", index, indexedActivities[index].TaskID, activities[index].TaskID)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = open()
	activities, err = store.ListActiveExternalGameActivities(ctx)
	if err != nil || len(activities) != domaintask.MaxActiveExternalGameActivities {
		t.Fatalf("reopened unindexed active Games=%d err=%v", len(activities), err)
	}
	extra := makeGame(domaintask.StatusRunning, "live-session-65")
	if err := store.ActivityTransaction(ctx, []modulecore.TaskID{extra.TaskID}, func(tx domaintask.Store) error {
		return saveNewActivityIndexTask(ctx, tx, extra)
	}); err != nil {
		t.Fatal(err)
	}
	if activities, err := store.ListActiveExternalGameActivities(ctx); !errors.Is(err, domaintask.ErrActiveExternalGameActivitiesTooMany) || activities != nil {
		t.Fatalf("65 unindexed active Games=%#v err=%v, want bounded fail-closed error", activities, err)
	}
}

func TestActivityRootCandidateReaderIncludesPrivateOwnerScopeWithoutReturningTaskContent(t *testing.T) {
	for _, indexed := range []bool{false, true} {
		name := "without index"
		if indexed {
			name = "with index"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store, err := NewJSONLStoreWithOptions(t.TempDir(), OpenOptions{Index: indexed})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := store.Close(); err != nil {
					t.Errorf("close activity root candidate store: %v", err)
				}
			})
			now := time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC)
			started := now
			root := domaintask.Task{
				TaskID: modulecore.NewTaskID(), Title: "private title must not leave the owner store", Assignee: "aka",
				Route: domaintask.RouteCHAT, Status: domaintask.StatusRunning, Priority: domaintask.PriorityNormal,
				InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now, StartedAt: &started,
				WorkstreamID: modulecore.NewWorkstreamID(), GoalID: modulecore.NewGoalID(),
				PrivateAccess: &domaintask.TaskPrivateAccess{Scope: domaintask.TaskAccessScopeUser, AuthenticatedUserID: "user-a"},
			}
			allowWrite := func(context.Context, domaintask.TaskWriteAuthorizationInput) error { return nil }
			if err := store.SaveTaskAuthorized(ctx, root, allowWrite); err != nil {
				t.Fatalf("save private root Task: %v", err)
			}
			activity := &domaintask.ActivityMetadata{
				Kind: domaintask.ActivityKindChat, Label: "会話", Phase: "active", Revision: 1,
				Participants: []domaintask.ActivityParticipant{{ActorID: "aka", Role: "assistant", Revision: 1}},
			}
			if err := store.ActivityTransaction(ctx, []modulecore.TaskID{root.TaskID}, func(tx domaintask.Store) error {
				writer := tx.(domaintask.ActivityTaskWriter)
				latest, err := writer.GetTaskActivity(ctx, root.TaskID)
				if err != nil {
					return err
				}
				latest.Activity = activity
				return writer.SaveTaskActivity(ctx, latest)
			}); err != nil {
				t.Fatalf("attach activity metadata: %v", err)
			}
			generation, err := store.WriterGeneration()
			if err != nil {
				t.Fatal(err)
			}
			if err := store.SaveRunAuthorized(ctx, domaintask.Run{
				WriterGeneration: generation, RunID: modulecore.NewRunID(), TaskID: root.TaskID, StartReason: domaintask.RunStartReasonFirst,
				Assignee: "aka", Status: domaintask.RunStatusRunning, StartedAt: now,
			}, allowWrite); err != nil {
				t.Fatalf("save private active Run: %v", err)
			}
			candidate, found, err := store.FindRunningRootActivityCandidate(ctx, "aka", "")
			if err != nil || !found {
				t.Fatalf("private root candidate=%+v found=%v err=%v", candidate, found, err)
			}
			if candidate.TaskID != root.TaskID || candidate.Kind != activity.Kind || candidate.Label != activity.Label || candidate.Phase != activity.Phase || candidate.ParticipantRole != "assistant" {
				t.Fatalf("private root candidate projection=%+v", candidate)
			}
			if strings.Contains(fmt.Sprintf("%+v", candidate), root.Title) {
				t.Fatal("private Task title escaped the narrow candidate projection")
			}
			if _, err := store.GetTask(ctx, root.TaskID); !errors.Is(err, domaintask.ErrNotFound) {
				t.Fatalf("public GetTask exposed private root: err=%v", err)
			}
			publicTasks, err := store.ListTasks(ctx, domaintask.Filter{Assignee: "aka", Status: domaintask.StatusRunning, Limit: 10})
			if err != nil {
				t.Fatalf("public ListTasks: %v", err)
			}
			for _, publicTask := range publicTasks {
				if publicTask.TaskID == root.TaskID {
					t.Fatal("public ListTasks exposed private root")
				}
			}
			if _, found, err := store.FindRunningRootActivityCandidate(ctx, "aka", root.TaskID); err != nil || found {
				t.Fatalf("excluded root candidate found=%v err=%v", found, err)
			}
		})
	}
}

func TestActivityRootCandidateReaderFailsClosedBeyondRawCandidateBound(t *testing.T) {
	for _, indexed := range []bool{false, true} {
		name := "without index"
		if indexed {
			name = "with index"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store, err := NewJSONLStoreWithOptions(t.TempDir(), OpenOptions{Index: indexed})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			now := time.Date(2026, 11, 21, 16, 0, 0, 0, time.UTC)
			started := now
			if err := store.Transaction(ctx, func(tx domaintask.Store) error {
				for range domaintask.MaxActivityRootCandidateScan + 1 {
					task := domaintask.Task{
						TaskID: modulecore.NewTaskID(), Title: "candidate without activity metadata", Assignee: "aka",
						Route: domaintask.RouteCHAT, Status: domaintask.StatusRunning, Priority: domaintask.PriorityNormal,
						InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now, StartedAt: &started,
					}
					if err := tx.SaveTask(ctx, task); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatalf("seed over-bound raw root candidates: %v", err)
			}
			if candidate, found, err := store.FindRunningRootActivityCandidate(ctx, "aka", ""); !errors.Is(err, domaintask.ErrActivityRootCandidateSearchLimit) || found || candidate != (domaintask.ActivityRootTaskCandidate{}) {
				t.Fatalf("over-bound root candidate=%+v found=%v err=%v", candidate, found, err)
			}
		})
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
