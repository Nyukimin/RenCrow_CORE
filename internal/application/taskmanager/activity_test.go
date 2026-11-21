package taskmanager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
)

type activityTransactionHiddenStore struct{ Store }

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
