package taskmanager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type activityTransactionHiddenStore struct{ Store }

type activityRootCandidateHiddenStore struct{ Store }

func (s activityRootCandidateHiddenStore) TaskTransaction(ctx context.Context, taskID modulecore.TaskID, fn func(Store) error) error {
	return s.Store.TaskTransaction(ctx, taskID, func(tx Store) error {
		return fn(activityRootCandidateHiddenStore{Store: tx})
	})
}

func (s activityRootCandidateHiddenStore) GetTaskActivity(ctx context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	writer, ok := s.Store.(domaintask.ActivityTaskWriter)
	if !ok {
		return domaintask.Task{}, domaintask.ErrActivityTaskWriterUnsupported
	}
	return writer.GetTaskActivity(ctx, taskID)
}

func (s activityRootCandidateHiddenStore) SaveTaskActivity(ctx context.Context, value domaintask.Task) error {
	writer, ok := s.Store.(domaintask.ActivityTaskWriter)
	if !ok {
		return domaintask.ErrActivityTaskWriterUnsupported
	}
	return writer.SaveTaskActivity(ctx, value)
}

func (s activityRootCandidateHiddenStore) SaveNewPassiveActivityTask(ctx context.Context, value domaintask.Task) error {
	writer, ok := s.Store.(domaintask.ActivityTaskWriter)
	if !ok {
		return domaintask.ErrActivityTaskWriterUnsupported
	}
	return writer.SaveNewPassiveActivityTask(ctx, value)
}

type activityStartBarrierStore struct {
	Store
	transaction domaintask.ActivityTaskTransaction
	mu          sync.Mutex
	arrivals    int
	release     chan struct{}
}

func (s *activityStartBarrierStore) ActivityTransaction(ctx context.Context, taskIDs []modulecore.TaskID, fn func(domaintask.Store) error) error {
	s.mu.Lock()
	s.arrivals++
	if s.arrivals == 2 {
		close(s.release)
	}
	release := s.release
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-release:
	}
	return s.transaction.ActivityTransaction(ctx, taskIDs, fn)
}

func (s *activityStartBarrierStore) FindRunningRootActivityCandidate(ctx context.Context, actorID string, excludeTaskID modulecore.TaskID) (domaintask.ActivityRootTaskCandidate, bool, error) {
	reader, ok := s.Store.(domaintask.ActivityRootTaskCandidateReader)
	if !ok {
		return domaintask.ActivityRootTaskCandidate{}, false, domaintask.ErrCurrentActivityReaderUnsupported
	}
	return reader.FindRunningRootActivityCandidate(ctx, actorID, excludeTaskID)
}

func TestConcurrentGenericRootStartsRetryCurrentSnapshotConflict(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStoreWithOptions(t.TempDir(), taskpersistence.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	wrapped := &activityStartBarrierStore{Store: store, transaction: store, release: make(chan struct{})}
	manager := New(wrapped, ParallelLimits{Global: 3, PerModule: 1, CodingTasks: 2, LongResearchTasks: 1, DestructiveTasks: 2})
	type result struct {
		task domaintask.Task
		run  domaintask.Run
		err  error
	}
	ids := []modulecore.TaskID{modulecore.NewTaskID(), modulecore.NewTaskID()}
	results := make(chan result, len(ids))
	for _, taskID := range ids {
		go func(taskID modulecore.TaskID) {
			task, run, err := manager.CreateAndStartRun(ctx, domaintask.Task{
				TaskID: taskID, Title: "concurrent OPS request", Route: domaintask.RouteOperations, Assignee: "shiro", OwnerID: "shiro",
			}, domaintask.SharedRoleContext{})
			results <- result{task: task, run: run, err: err}
		}(taskID)
	}
	started := make(map[modulecore.TaskID]domaintask.Run, len(ids))
	for range ids {
		outcome := <-results
		if outcome.err != nil {
			t.Fatalf("concurrent root start err=%v", outcome.err)
		}
		if outcome.task.TaskID == "" || outcome.run.TaskID != outcome.task.TaskID {
			t.Fatalf("Task/Run identities=%s/%s", outcome.task.TaskID, outcome.run.TaskID)
		}
		started[outcome.task.TaskID] = outcome.run
	}
	if len(started) != len(ids) {
		t.Fatalf("started Task IDs=%v, want both stable request IDs %v", started, ids)
	}
	for _, taskID := range ids {
		if _, found := started[taskID]; !found {
			t.Fatalf("request TaskID %s was not preserved across retry: started=%v", taskID, started)
		}
	}
	tasks, err := manager.List(ctx, domaintask.Filter{Limit: 10})
	if err != nil || len(tasks) != 2 {
		t.Fatalf("Tasks=%+v err=%v, want exactly two concurrent executions", tasks, err)
	}
	runs, err := manager.ListRuns(ctx, domaintask.RunFilter{})
	if err != nil || len(runs) != 2 {
		t.Fatalf("Runs=%+v err=%v, want exactly one Run per request", runs, err)
	}
	for _, task := range tasks {
		if _, found := started[task.TaskID]; !found || task.Status != domaintask.StatusRunning || task.Activity == nil || task.Activity.Passive {
			t.Fatalf("unexpected concurrent Task: %+v", task)
		}
	}
	for _, run := range runs {
		if _, found := started[run.TaskID]; !found || run.Status != domaintask.RunStatusRunning {
			t.Fatalf("unexpected concurrent Run: %+v", run)
		}
	}
	current, err := manager.SnapshotCurrentActivities(ctx, []string{"shiro"})
	if err != nil || current["shiro"].Task.TaskID == "" {
		t.Fatalf("current activity=%+v err=%v, want exactly one canonical current Task", current, err)
	}
	if _, found := started[current["shiro"].Task.TaskID]; !found {
		t.Fatalf("current activity points outside the two execution Tasks: %+v", current["shiro"])
	}
}

func TestRootCompletionRestoresAnotherActiveRootBeforeEvaluating(t *testing.T) {
	for _, firstToComplete := range []string{"older root", "current root"} {
		t.Run(firstToComplete, func(t *testing.T) {
			ctx := context.Background()
			store, err := taskpersistence.NewJSONLStoreWithOptions(t.TempDir(), taskpersistence.OpenOptions{Index: true})
			if err != nil {
				t.Fatal(err)
			}
			registerTaskStoreCleanup(t, store)
			manager := New(store, DefaultParallelLimits())
			older, olderRun, err := manager.CreateAndStartRun(ctx, domaintask.Task{Title: "first work", Route: domaintask.RouteCHAT, Assignee: "shiro"}, domaintask.SharedRoleContext{})
			if err != nil {
				t.Fatal(err)
			}
			currentRoot, currentRun, err := manager.CreateAndStartRun(ctx, domaintask.Task{Title: "second work", Route: domaintask.RouteOperations, Assignee: "shiro"}, domaintask.SharedRoleContext{})
			if err != nil {
				t.Fatal(err)
			}
			current, err := manager.SnapshotCurrentActivities(ctx, []string{"shiro"})
			if err != nil || current["shiro"].Task.TaskID != currentRoot.TaskID {
				t.Fatalf("initial current=%+v err=%v, want most recently started root", current, err)
			}
			if firstToComplete == "older root" {
				if _, err := manager.CompleteRun(ctx, older.TaskID, olderRun.RunID, "shiro", domaintask.StatusSucceeded, "older complete", ""); err != nil {
					t.Fatal(err)
				}
				current, err = manager.SnapshotCurrentActivities(ctx, []string{"shiro"})
				if err != nil || current["shiro"].Task.TaskID != currentRoot.TaskID {
					t.Fatalf("after noncurrent root completed current=%+v err=%v, want current root unchanged", current, err)
				}
				if _, err := manager.CompleteRun(ctx, currentRoot.TaskID, currentRun.RunID, "shiro", domaintask.StatusSucceeded, "current complete", ""); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := manager.CompleteRun(ctx, currentRoot.TaskID, currentRun.RunID, "shiro", domaintask.StatusFailed, "current failed", ""); err != nil {
					t.Fatal(err)
				}
				current, err = manager.SnapshotCurrentActivities(ctx, []string{"shiro"})
				if err != nil || current["shiro"].Task.TaskID != older.TaskID || current["shiro"].Task.Activity == nil || current["shiro"].Task.Activity.Kind != domaintask.ActivityKindChat || current["shiro"].Participant.Revision < 3 {
					t.Fatalf("after current root completed current=%+v err=%v, want older active root with monotonic assignment revision", current, err)
				}
				if _, err := manager.CompleteRun(ctx, older.TaskID, olderRun.RunID, "shiro", domaintask.StatusCancelled, "older cancelled", ""); err != nil {
					t.Fatal(err)
				}
			}
			current, err = manager.SnapshotCurrentActivities(ctx, []string{"shiro"})
			if err != nil || current["shiro"].Task.Activity == nil || current["shiro"].Task.Activity.Kind != domaintask.ActivityKindEvaluating || !current["shiro"].Task.Activity.Passive {
				t.Fatalf("final current=%+v err=%v, want passive Evaluating after last root terminal", current, err)
			}
			runs, err := manager.ListRuns(ctx, domaintask.RunFilter{})
			if err != nil || len(runs) != 2 {
				t.Fatalf("Runs=%+v err=%v, want exactly the two execution Runs", runs, err)
			}
			for _, run := range runs {
				if !domaintask.IsRunTerminal(run.Status) {
					t.Fatalf("execution Run left active: %+v", run)
				}
			}
			if evaluationRuns, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: current["shiro"].Task.TaskID}); err != nil || len(evaluationRuns) != 0 {
				t.Fatalf("Evaluating Task Runs=%+v err=%v, want none", evaluationRuns, err)
			}
		})
	}
}

func TestCurrentGameTurnRunUsesChildIdentityAndPreservesGameActivity(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStoreWithOptions(t.TempDir(), taskpersistence.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	game, err := manager.EnsureExternalGameActivity(ctx, "configured-user", "nethack", "session-turn-1", "playing", []domaintask.ActivityParticipant{{ActorID: "mio", Role: "player"}})
	if err != nil {
		t.Fatalf("adopt external game: %v", err)
	}
	snapshot, err := manager.SnapshotCurrentActivities(ctx, []string{"mio"})
	if err != nil {
		t.Fatal(err)
	}
	gameRevision := snapshot["mio"].Participant.Revision
	child, run, err := manager.CreateAndStartCurrentGameTurnRun(ctx, game.TaskID, "mio", gameRevision, "nethack", "session-turn-1", domaintask.Task{
		Title: "Game turn", OwnerID: "mio", Assignee: "mio", ParentTaskID: game.TaskID, Route: domaintask.RouteGeneral, ReadOnly: true,
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("create current game child Run: %v", err)
	}
	if child.ParentTaskID != game.TaskID || child.Status != domaintask.StatusRunning || run.TaskID != child.TaskID {
		t.Fatalf("child Task/Run = (%+v, %+v), want active child of Game Task %s", child, run, game.TaskID)
	}
	current, err := manager.SnapshotCurrentActivities(ctx, []string{"mio"})
	if err != nil || current["mio"].Task.TaskID != game.TaskID || current["mio"].Task.Activity.Kind != domaintask.ActivityKindGame {
		t.Fatalf("game activity changed during turn execution: current=%+v err=%v", current, err)
	}
	if _, err := manager.CompleteRun(ctx, child.TaskID, run.RunID, "mio", domaintask.StatusSucceeded, "turn completed", ""); err != nil {
		t.Fatalf("complete game turn Run: %v", err)
	}
	current, err = manager.SnapshotCurrentActivities(ctx, []string{"mio"})
	if err != nil || current["mio"].Task.TaskID != game.TaskID {
		t.Fatalf("game activity changed after turn completion: current=%+v err=%v", current, err)
	}

	chat, _, err := manager.CreateAndStartRun(ctx, domaintask.Task{Title: "foreground chat", Route: domaintask.RouteCHAT, Assignee: "mio"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	chatSnapshot, err := manager.SnapshotCurrentActivities(ctx, []string{"mio"})
	if err != nil || chatSnapshot["mio"].Task.TaskID != chat.TaskID {
		t.Fatalf("foreground chat did not become current: current=%+v err=%v", chatSnapshot, err)
	}
	if _, err := manager.BindCurrentActivity(ctx, "mio", chat.TaskID, chatSnapshot["mio"].Participant.Revision, game.TaskID, domaintask.ActivityMetadata{
		Kind: domaintask.ActivityKindGame, Label: game.Activity.Label, Phase: game.Activity.Phase, Passive: true,
		Game: game.Activity.Game, Participants: []domaintask.ActivityParticipant{{ActorID: "mio", Role: "player"}},
	}); err != nil {
		t.Fatalf("restore same Game Task after chat: %v", err)
	}
	if _, _, err := manager.CreateAndStartCurrentGameTurnRun(ctx, game.TaskID, "mio", gameRevision, "nethack", "session-turn-1", domaintask.Task{
		Title: "stale Game turn", OwnerID: "mio", Assignee: "mio", ParentTaskID: game.TaskID, Route: domaintask.RouteGeneral,
	}, domaintask.SharedRoleContext{}); !errors.Is(err, domaintask.ErrCurrentActivityConflict) {
		t.Fatalf("stale participant revision error=%v, want current-activity conflict", err)
	}
	current, err = manager.SnapshotCurrentActivities(ctx, []string{"mio"})
	if err != nil || current["mio"].Task.TaskID != game.TaskID {
		t.Fatalf("stale turn changed current Game: current=%+v err=%v", current, err)
	}
}

func TestCreateAndStartRunBindsCanonicalRootTaskAndPreservesNestedChild(t *testing.T) {
	storeRoot, err := os.MkdirTemp(os.TempDir(), "task-activity-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(storeRoot) })
	store, err := taskpersistence.NewJSONLStoreWithOptions(storeRoot, taskpersistence.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	ctx := context.Background()

	root, rootRun, err := manager.CreateAndStartRun(ctx, domaintask.Task{
		Title: "chat with Shiro", Route: domaintask.RouteCHAT, Assignee: "shiro",
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	current, err := manager.SnapshotCurrentActivities(ctx, []string{"shiro"})
	if err != nil {
		t.Fatal(err)
	}
	if got := current["shiro"].Task.TaskID; got != root.TaskID {
		t.Fatalf("current TaskID=%s, want root %s", got, root.TaskID)
	}
	if current["shiro"].Task.Activity.Kind != domaintask.ActivityKindChat {
		t.Fatalf("activity kind=%s, want chat", current["shiro"].Task.Activity.Kind)
	}

	child, _, err := manager.CreateAndStartRun(ctx, domaintask.Task{
		Title: "provider child", Route: domaintask.RouteGeneral, Assignee: "shiro", ParentTaskID: root.TaskID,
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	current, err = manager.SnapshotCurrentActivities(ctx, []string{"shiro"})
	if err != nil {
		t.Fatal(err)
	}
	if got := current["shiro"].Task.TaskID; got != root.TaskID || got == child.TaskID {
		t.Fatalf("nested Task hijacked current activity: got %s, want root %s", got, root.TaskID)
	}
	childForAnotherActor, _, err := manager.CreateAndStartRun(ctx, domaintask.Task{
		Title: "work for Aka", Route: domaintask.RouteGeneral, Assignee: "aka", ParentTaskID: root.TaskID,
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	current, err = manager.SnapshotCurrentActivities(ctx, []string{"aka", "shiro"})
	if err != nil || current["aka"].Task.TaskID != childForAnotherActor.TaskID || current["shiro"].Task.TaskID != root.TaskID {
		t.Fatalf("cross-actor child assignment=%#v err=%v", current, err)
	}
	resumedChild, err := manager.Create(ctx, domaintask.Task{
		Title: "resumed child", Route: domaintask.RouteGeneral, Assignee: "gin", ParentTaskID: root.TaskID,
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CompleteRun(ctx, root.TaskID, rootRun.RunID, "shiro", domaintask.StatusSucceeded, "parent complete", ""); err != nil {
		t.Fatal(err)
	}
	resumedChild, err = manager.Start(ctx, resumedChild.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	current, err = manager.SnapshotCurrentActivities(ctx, []string{"gin"})
	if err != nil || current["gin"].Task.TaskID != resumedChild.TaskID {
		t.Fatalf("resumed child current=%#v err=%v", current, err)
	}
}

func TestStartExistingRootTaskBindsItAsCurrent(t *testing.T) {
	storeRoot, err := os.MkdirTemp(os.TempDir(), "task-activity-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(storeRoot) })
	store, err := taskpersistence.NewJSONLStoreWithOptions(storeRoot, taskpersistence.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	ctx := context.Background()
	queued, err := manager.Create(ctx, domaintask.Task{Title: "queued chat", Route: domaintask.RouteCHAT, Assignee: "shiro"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	started, err := manager.Start(ctx, queued.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	current, err := manager.SnapshotCurrentActivities(ctx, []string{"shiro"})
	if err != nil || current["shiro"].Task.TaskID != started.TaskID || current["shiro"].Task.Activity.Kind != domaintask.ActivityKindChat {
		t.Fatalf("started current=%#v err=%v", current, err)
	}
}

func TestStoreWithoutActivityTransactionCannotIgnoreExistingCurrentTask(t *testing.T) {
	storeRoot, err := os.MkdirTemp(os.TempDir(), "activity-hidden-port-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(storeRoot) })
	store, err := taskpersistence.NewJSONLStoreWithOptions(storeRoot, taskpersistence.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	baseManager := New(store, DefaultParallelLimits())
	ctx := context.Background()
	if _, err := baseManager.CreateCurrentActivity(ctx, domaintask.Task{Title: "rest", Route: domaintask.RouteGeneral, Assignee: "shiro"}, domaintask.ActivityMetadata{
		Kind: domaintask.ActivityKindRest, Label: "Resting", Phase: "resting", Passive: true,
		Participants: []domaintask.ActivityParticipant{{ActorID: "shiro"}},
	}); err != nil {
		t.Fatal(err)
	}
	wrapped := New(activityTransactionHiddenStore{Store: store}, DefaultParallelLimits())
	if _, _, err := wrapped.CreateAndStartRun(ctx, domaintask.Task{Title: "new work", Route: domaintask.RouteGeneral, Assignee: "shiro"}, domaintask.SharedRoleContext{}); !errors.Is(err, domaintask.ErrActivityTaskWriterUnsupported) {
		t.Fatalf("create with existing current Task and missing owner transaction err=%v", err)
	}
	current, err := baseManager.SnapshotCurrentActivities(ctx, []string{"shiro"})
	if err != nil || current["shiro"].Task.Activity.Kind != domaintask.ActivityKindRest {
		t.Fatalf("current Task changed through hidden owner port: %#v err=%v", current, err)
	}
}

func TestTerminalCurrentActivityFailsClosedWhenRootCandidateReaderIsHidden(t *testing.T) {
	store, err := taskpersistence.NewJSONLStoreWithOptions(t.TempDir(), taskpersistence.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	ctx := context.Background()
	base := New(store, DefaultParallelLimits())
	started, run, err := base.CreateAndStartRun(ctx, domaintask.Task{Title: "active chat", Route: domaintask.RouteCHAT, Assignee: "shiro"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	wrapped := New(activityRootCandidateHiddenStore{Store: store}, DefaultParallelLimits())
	if _, err := wrapped.CompleteRun(ctx, started.TaskID, run.RunID, "shiro", domaintask.StatusSucceeded, "finished", ""); !errors.Is(err, domaintask.ErrCurrentActivityReaderUnsupported) {
		t.Fatalf("terminal completion with hidden current-owner reader err=%v, want fail-closed unsupported-reader error", err)
	}
	unchangedTask, err := base.Get(ctx, started.TaskID)
	if err != nil || unchangedTask.Status != domaintask.StatusRunning {
		t.Fatalf("Task after rejected terminal handoff=%#v err=%v, want unchanged running Task", unchangedTask, err)
	}
	current, err := base.SnapshotCurrentActivities(ctx, []string{"shiro"})
	if err != nil || current["shiro"].Task.TaskID != started.TaskID {
		t.Fatalf("current after rejected terminal handoff=%#v err=%v, want original current Task", current, err)
	}
}

func TestRootTaskActivityLabelDoesNotCopyUnboundedOrControlledTitle(t *testing.T) {
	storeRoot, err := os.MkdirTemp(os.TempDir(), "task-activity-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(storeRoot) })
	store, err := taskpersistence.NewJSONLStoreWithOptions(storeRoot, taskpersistence.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	ctx := context.Background()
	longJapaneseTitle := strings.Repeat("長", 128)
	chat, _, err := manager.CreateAndStartRun(ctx, domaintask.Task{Title: longJapaneseTitle, Route: domaintask.RouteCHAT, Assignee: "shiro"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("long Japanese CHAT Task failed to start: %v", err)
	}
	if chat.Title != longJapaneseTitle || chat.Activity == nil || chat.Activity.Label != "Chatting" {
		t.Fatalf("CHAT Task canonical title/activity label=%q/%#v", chat.Title, chat.Activity)
	}
	controlTitle := "work\x01 item"
	work, _, err := manager.CreateAndStartRun(ctx, domaintask.Task{Title: controlTitle, Route: domaintask.RouteGeneral, Assignee: "mio"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("control-containing canonical WORK title failed to start: %v", err)
	}
	if work.Title != controlTitle || work.Activity == nil || work.Activity.Label != "Working" {
		t.Fatalf("WORK Task canonical title/activity label=%q/%#v", work.Title, work.Activity)
	}
}

func TestPassiveActivityCompletionTransitionsAtomically(t *testing.T) {
	storeRoot, err := os.MkdirTemp(os.TempDir(), "task-activity-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(storeRoot) })
	store, err := taskpersistence.NewJSONLStoreWithOptions(storeRoot, taskpersistence.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	ctx := context.Background()

	rest, err := manager.CreateCurrentActivity(ctx, domaintask.Task{Title: "rest", Route: domaintask.RouteGeneral, Assignee: "shiro"}, domaintask.ActivityMetadata{
		Kind: domaintask.ActivityKindRest, Label: "Resting", Phase: "resting", Passive: true,
		Participants: []domaintask.ActivityParticipant{{ActorID: "shiro"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := manager.SnapshotCurrentActivities(ctx, []string{"shiro"})
	if err != nil {
		t.Fatal(err)
	}
	if current["shiro"].Task.Status != domaintask.StatusRunning || current["shiro"].Task.Activity == nil || !current["shiro"].Task.Activity.Passive {
		t.Fatalf("rest Task is not running passive current activity: %#v", current["shiro"].Task)
	}
	replacement, err := manager.CompleteAndTransitionCurrentActivity(ctx, "shiro", rest.TaskID,
		current["shiro"].Participant.Revision, domaintask.StatusSucceeded, "rest window elapsed",
		domaintask.Task{Title: "evaluating next activity", Route: domaintask.RouteGeneral, Assignee: "shiro"},
		domaintask.ActivityMetadata{Kind: domaintask.ActivityKindEvaluating, Label: "Choosing next activity", Phase: "evaluating", Passive: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	closed, err := manager.Get(ctx, rest.TaskID)
	if err != nil || closed.Status != domaintask.StatusSucceeded {
		t.Fatalf("closed rest Task=%#v err=%v", closed, err)
	}
	current, err = manager.SnapshotCurrentActivities(ctx, []string{"shiro"})
	if err != nil || current["shiro"].Task.TaskID != replacement.TaskID {
		t.Fatalf("replacement current=%#v err=%v", current, err)
	}
}

func TestExternalExecutionFenceRejectsActivityCompletionWithoutChangingBinding(t *testing.T) {
	storeRoot, err := os.MkdirTemp(os.TempDir(), "task-activity-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(storeRoot) })
	store, err := taskpersistence.NewJSONLStoreWithOptions(storeRoot, taskpersistence.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	ctx := context.Background()
	rest, err := manager.CreateCurrentActivity(ctx, domaintask.Task{Title: "rest", Route: domaintask.RouteGeneral, Assignee: "shiro"}, domaintask.ActivityMetadata{
		Kind: domaintask.ActivityKindRest, Label: "Resting", Phase: "resting", Passive: true,
		Participants: []domaintask.ActivityParticipant{{ActorID: "shiro"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := manager.SnapshotCurrentActivities(ctx, []string{"shiro"})
	if err != nil {
		t.Fatal(err)
	}
	const fenceID = "activity-test:execution-fence"
	if err := store.AcquireTaskExecutionFence(ctx, rest.TaskID, fenceID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.ReleaseTaskExecutionFence(context.Background(), rest.TaskID, fenceID) })
	_, err = manager.CompleteAndTransitionCurrentActivity(ctx, "shiro", rest.TaskID, current["shiro"].Participant.Revision,
		domaintask.StatusCancelled, "interrupted", domaintask.Task{Title: "evaluating", Route: domaintask.RouteGeneral, Assignee: "shiro"},
		domaintask.ActivityMetadata{Kind: domaintask.ActivityKindEvaluating, Label: "Choosing next activity", Phase: "evaluating", Passive: true},
	)
	if !errors.Is(err, taskpersistence.ErrTaskExecutionFenceActive) {
		t.Fatalf("completion with active execution fence err=%v, want fence conflict", err)
	}
	unchanged, err := manager.SnapshotCurrentActivities(ctx, []string{"shiro"})
	if err != nil || unchanged["shiro"].Task.TaskID != rest.TaskID || unchanged["shiro"].Participant.Revision != current["shiro"].Participant.Revision {
		t.Fatalf("current binding changed after rejected completion: %#v err=%v", unchanged, err)
	}
}

func TestQueuedRunClosureAtomicallyInstallsEvaluationTask(t *testing.T) {
	manager, _, task, run := startQueueBackedRunForClosure(t)
	if err := manager.CloseStartedRun(context.Background(), task.TaskID, run.RunID, task.Assignee,
		RunOutcome{Status: domaintask.StatusSucceeded, Summary: "work complete"}, nil); err != nil {
		t.Fatal(err)
	}
	closed, err := manager.Get(context.Background(), task.TaskID)
	if err != nil || closed.Status != domaintask.StatusSucceeded {
		t.Fatalf("closed Task=%#v err=%v", closed, err)
	}
	closedRun, err := manager.GetRun(context.Background(), run.RunID)
	if err != nil || closedRun.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("closed Run=%#v err=%v", closedRun, err)
	}
	current, err := manager.SnapshotCurrentActivities(context.Background(), []string{task.Assignee})
	if err != nil {
		t.Fatal(err)
	}
	evaluation := current[task.Assignee].Task
	if evaluation.TaskID == task.TaskID || evaluation.Status != domaintask.StatusRunning || evaluation.Activity == nil ||
		evaluation.Activity.Kind != domaintask.ActivityKindEvaluating || !evaluation.Activity.Passive || !evaluation.ReadOnly || evaluation.InterruptPolicy != domaintask.InterruptSilent {
		t.Fatalf("current evaluation Task=%#v, want a distinct running passive evaluating Task", evaluation)
	}
}

func TestEnsureExternalGameActivityAdoptsObservedSessionWithoutReplacingBusyActor(t *testing.T) {
	storeRoot, err := os.MkdirTemp(os.TempDir(), "task-activity-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(storeRoot) })
	store, err := taskpersistence.NewJSONLStoreWithOptions(storeRoot, taskpersistence.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	ctx := context.Background()

	busy, _, err := manager.CreateAndStartRun(ctx, domaintask.Task{Title: "existing work", Route: domaintask.RouteGeneral, Assignee: "aka"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	game, err := manager.EnsureExternalGameActivity(ctx, "configured-user-1", "nethack", "session_1", "playing", []domaintask.ActivityParticipant{
		{ActorID: "shiro", Role: "player"}, {ActorID: "aka", Role: "player"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if game.TaskID == busy.TaskID || game.Status != domaintask.StatusRunning || game.Route != domaintask.RouteGeneral || game.Activity == nil ||
		game.Activity.Kind != domaintask.ActivityKindGame || game.Activity.Game == nil || game.Activity.Game.SessionID != "session_1" {
		t.Fatalf("adopted game Task=%#v", game)
	}
	current, err := manager.SnapshotCurrentActivities(ctx, []string{"aka", "shiro"})
	if err != nil {
		t.Fatal(err)
	}
	if current["aka"].Task.TaskID != busy.TaskID || current["shiro"].Task.TaskID != game.TaskID {
		t.Fatalf("current assignments aka=%s shiro=%s", current["aka"].Task.TaskID, current["shiro"].Task.TaskID)
	}
	replayed, err := manager.EnsureExternalGameActivity(ctx, "configured-user-1", "nethack", "session_1", "playing", []domaintask.ActivityParticipant{
		{ActorID: "aka", Role: "player"}, {ActorID: "shiro", Role: "player"},
	})
	if err != nil || replayed.TaskID != game.TaskID {
		t.Fatalf("idempotent replay Task=%s err=%v, want %s", replayed.TaskID, err, game.TaskID)
	}
}

func TestPassiveGameActivitySurvivesActorHandoffsUntilObservedTerminal(t *testing.T) {
	storeRoot, err := os.MkdirTemp(os.TempDir(), "task-activity-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(storeRoot) })
	store, err := taskpersistence.NewJSONLStoreWithOptions(storeRoot, taskpersistence.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	ctx := context.Background()

	game, err := manager.EnsureExternalGameActivity(ctx, "configured-user", "nethack", "shared_session", "playing", []domaintask.ActivityParticipant{
		{ActorID: "aka", Role: "player"}, {ActorID: "shiro", Role: "player"},
	})
	if err != nil {
		t.Fatal(err)
	}
	chatShiro, _, err := manager.CreateAndStartRun(ctx, domaintask.Task{Title: "chat", Route: domaintask.RouteCHAT, Assignee: "shiro"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	stillRunning, err := manager.Get(ctx, game.TaskID)
	if err != nil || stillRunning.Status != domaintask.StatusRunning || stillRunning.Activity.Game.SessionID != "shared_session" {
		t.Fatalf("game after first actor handoff=%#v err=%v", stillRunning, err)
	}
	chatAka, _, err := manager.CreateAndStartRun(ctx, domaintask.Task{Title: "chat", Route: domaintask.RouteCHAT, Assignee: "aka"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	stillRunning, err = manager.Get(ctx, game.TaskID)
	if err != nil || stillRunning.Status != domaintask.StatusRunning || stillRunning.Activity.Game.SessionID != "shared_session" {
		t.Fatalf("game after all participant handoffs=%#v err=%v", stillRunning, err)
	}
	current, err := manager.SnapshotCurrentActivities(ctx, []string{"aka", "shiro"})
	if err != nil || current["aka"].Task.TaskID != chatAka.TaskID || current["shiro"].Task.TaskID != chatShiro.TaskID {
		t.Fatalf("current after handoffs=%#v err=%v", current, err)
	}
	liveGames, err := manager.ListActiveExternalGameActivities(ctx)
	if err != nil || len(liveGames) != 1 || liveGames[0].TaskID != game.TaskID {
		t.Fatalf("off-current external Games=%#v err=%v", liveGames, err)
	}
	shiroRevision := current["shiro"].Participant.Revision
	resumed, err := manager.BindCurrentActivity(ctx, "shiro", chatShiro.TaskID, shiroRevision, game.TaskID, domaintask.ActivityMetadata{
		Kind: domaintask.ActivityKindGame, Label: "Playing nethack", Phase: "playing", Passive: true,
		Game: &domaintask.ActivityGameReference{GameID: "nethack", SessionID: "shared_session"},
	})
	if err != nil || resumed.TaskID != game.TaskID || resumed.Status != domaintask.StatusRunning {
		t.Fatalf("resume game Task=%#v err=%v", resumed, err)
	}
	current, err = manager.SnapshotCurrentActivities(ctx, []string{"shiro"})
	if err != nil || current["shiro"].Task.TaskID != game.TaskID || current["shiro"].Task.Activity.Game.SessionID != "shared_session" {
		t.Fatalf("resumed game current=%#v err=%v", current, err)
	}
	if _, err := manager.CompleteObservedGameActivity(ctx, game.TaskID, "nethack", "shared_session", map[string]uint64{"shiro": current["shiro"].Participant.Revision}, domaintask.StatusSucceeded, "session ended"); err != nil {
		t.Fatalf("complete observed game: %v", err)
	}
	closed, err := manager.Get(ctx, game.TaskID)
	if err != nil || closed.Status != domaintask.StatusSucceeded {
		t.Fatalf("closed game Task=%#v err=%v", closed, err)
	}
	liveGames, err = manager.ListActiveExternalGameActivities(ctx)
	if err != nil || len(liveGames) != 0 {
		t.Fatalf("terminal Game remained in live external index: %#v err=%v", liveGames, err)
	}
	current, err = manager.SnapshotCurrentActivities(ctx, []string{"shiro"})
	if err != nil || current["shiro"].Task.TaskID == game.TaskID || current["shiro"].Task.Activity.Kind != domaintask.ActivityKindEvaluating {
		t.Fatalf("current after observed terminal=%#v err=%v", current, err)
	}
}

func TestObservedGameTerminalAfterAllParticipantsLeftPreservesForeground(t *testing.T) {
	storeRoot, err := os.MkdirTemp(os.TempDir(), "task-activity-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(storeRoot) })
	store, err := taskpersistence.NewJSONLStoreWithOptions(storeRoot, taskpersistence.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	ctx := context.Background()

	game, err := manager.EnsureExternalGameActivity(ctx, "configured-user", "nethack", "ended_session", "playing", []domaintask.ActivityParticipant{
		{ActorID: "aka", Role: "player"}, {ActorID: "shiro", Role: "player"},
	})
	if err != nil {
		t.Fatal(err)
	}
	akaChat, _, err := manager.CreateAndStartRun(ctx, domaintask.Task{Title: "Aka chat", Route: domaintask.RouteCHAT, Assignee: "aka"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	shiroChat, _, err := manager.CreateAndStartRun(ctx, domaintask.Task{Title: "Shiro chat", Route: domaintask.RouteCHAT, Assignee: "shiro"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CompleteObservedGameActivity(ctx, game.TaskID, "nethack", "ended_session", map[string]uint64{}, domaintask.StatusSucceeded, "observed session end"); err != nil {
		t.Fatalf("close Game Task after every participant left: %v", err)
	}
	closed, err := manager.Get(ctx, game.TaskID)
	if err != nil || closed.Status != domaintask.StatusSucceeded {
		t.Fatalf("closed Game Task=%#v err=%v", closed, err)
	}
	current, err := manager.SnapshotCurrentActivities(ctx, []string{"aka", "shiro"})
	if err != nil || current["aka"].Task.TaskID != akaChat.TaskID || current["shiro"].Task.TaskID != shiroChat.TaskID {
		t.Fatalf("foreground assignments after game terminal=%#v err=%v", current, err)
	}
}

func TestRunlessPassiveActivitiesDoNotConsumeExecutionCapacity(t *testing.T) {
	storeRoot, err := os.MkdirTemp(os.TempDir(), "task-activity-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(storeRoot) })
	store, err := taskpersistence.NewJSONLStoreWithOptions(storeRoot, taskpersistence.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	limits := DefaultParallelLimits()
	limits.Global = 1
	manager := New(store, limits)
	ctx := context.Background()
	var firstPassive domaintask.Task
	for index := 0; index < 8; index++ {
		actorID := fmt.Sprintf("actor%d", index)
		created, err := manager.CreateCurrentActivity(ctx, domaintask.Task{Title: "rest", Route: domaintask.RouteGeneral, Assignee: actorID}, domaintask.ActivityMetadata{
			Kind: domaintask.ActivityKindRest, Label: "Resting", Phase: "resting", Passive: true,
			Participants: []domaintask.ActivityParticipant{{ActorID: actorID}},
		})
		if err != nil {
			t.Fatalf("create passive activity %s: %v", actorID, err)
		}
		if index == 0 {
			firstPassive = created
		}
	}
	if _, err := manager.Start(ctx, firstPassive.TaskID); !errors.Is(err, domaintask.ErrCurrentActivityInvalid) {
		t.Fatalf("starting passive Task err=%v, want ErrCurrentActivityInvalid", err)
	}
	if _, _, err := manager.CreateAndStartRun(ctx, domaintask.Task{Title: "active work", Route: domaintask.RouteGeneral, Assignee: "worker"}, domaintask.SharedRoleContext{}); err != nil {
		t.Fatalf("first active Run with eight passive Tasks: %v", err)
	}
	if _, _, err := manager.CreateAndStartRun(ctx, domaintask.Task{Title: "second active work", Route: domaintask.RouteGeneral, Assignee: "worker2"}, domaintask.SharedRoleContext{}); !errors.Is(err, ErrParallelLimit) {
		t.Fatalf("second active Run error=%v, want ErrParallelLimit", err)
	}
}
