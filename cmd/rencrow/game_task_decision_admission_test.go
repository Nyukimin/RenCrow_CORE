package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentheartbeat"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestGameTaskDecisionBindsEarlyCallbackBeforeProviderAndRejectsWrongSession(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	tasks := taskmanager.New(store, taskmanager.DefaultParallelLimits())
	spec, err := domaintask.NormalizeGameSpec(domaintask.GameSpec{
		GameID: "herzog_zwei", Personas: []string{"aka", "ao", "kin", "gin"}, Turns: 40, Mode: "realtime",
	})
	if err != nil {
		t.Fatalf("normalize Task spec: %v", err)
	}
	task, err := tasks.Create(ctx, domaintask.Task{
		Title: "Four-player Herzog Zwei", OwnerID: "operator", Assignee: "mio", Route: domaintask.RouteGAME,
	}, domaintask.SharedRoleContext{GameSpec: &spec})
	if err != nil {
		t.Fatalf("create GAME Task: %v", err)
	}
	now := time.Date(2026, time.November, 18, 10, 0, 0, 0, time.UTC)
	run, err := tasks.StartGameTaskRun(ctx, task.TaskID, modulecore.NewQueueItemID(), task.Assignee, "", "")
	if err != nil {
		t.Fatalf("start GAME controller Run: %v", err)
	}
	launch, created, err := tasks.BeginGameLaunch(ctx, task.TaskID)
	if err != nil || !created || launch.Progress.Status != domaintask.GameExecutionLaunching {
		t.Fatalf("BeginGameLaunch() = (%+v, %t, %v), want newly persisted intent", launch.Progress, created, err)
	}

	const sessionID = "herzog_zwei_20261118T100000000Z_1"
	observer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/games/launches/"+string(task.TaskID) {
			t.Errorf("unexpected receipt request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(gameTaskReceiptResponse{
			LaunchKey: string(task.TaskID), SessionID: sessionID, Status: string(domaintask.GameExecutionRunning),
			Request: spec, CreatedAt: now, UpdatedAt: now.Add(time.Second),
		})
	}))
	defer observer.Close()
	client, err := newGameTaskLaunchClient(observer.URL)
	if err != nil {
		t.Fatalf("create receipt client: %v", err)
	}
	scheduler := agentheartbeat.New([]string{"aka", "ao", "kin", "gin"}, agentheartbeat.Sources{}, nil)
	admission := newGameTaskRuntimeAdmission(&gameTaskAdmissionSessions{}, tasks)
	admission.setScheduler(func() gameTaskGroupScheduler { return scheduler })
	if reserved, err := admission.TryReserveTaskGroup(ctx, string(task.TaskID), spec); err != nil || !reserved {
		t.Fatalf("reserve actual player group before launch: reserved=%t err=%v", reserved, err)
	}
	pausedController := false
	delegate := &gameTaskDecisionAdmissionCapture{duringDecision: func(decisionCtx context.Context) error {
		if pausedController {
			return nil
		}
		pausedController = true
		return tasks.PauseGameTaskRun(decisionCtx, task.TaskID, run.RunID, task.Assignee)
	}}
	turnOwner := newGameTurnExecutionProvider(delegate, tasks)
	heartbeat := newHeartbeatGameDecisionProvider(turnOwner, func() conversationruntime.ActorAdmission {
		return scheduler
	}, true)
	provider := newGameTaskDecisionAdmissionProvider(heartbeat, tasks, client, admission, "operator")

	request := viewer.GameAgentDecisionRequest{Persona: "aka", GameID: spec.GameID, SessionID: sessionID}
	if _, err := provider.DecideGameTurn(ctx, request); err != nil {
		t.Fatalf("early Agent callback while launch POST response is pending: %v", err)
	}
	bound, err := tasks.GetGameTaskState(ctx, task.TaskID)
	if err != nil || bound.Progress.Status != domaintask.GameExecutionRunning || bound.Progress.SessionID != sessionID {
		t.Fatalf("callback did not bind durable receipt before provider: progress=%+v err=%v", bound.Progress, err)
	}
	if delegate.calls != 1 || delegate.auth != (gameTaskTurnAuthorization{LaunchKey: string(task.TaskID), GameID: spec.GameID, SessionID: sessionID}) {
		t.Fatalf("provider calls=%d authorization=%+v, want exact Task/session authorization once", delegate.calls, delegate.auth)
	}
	firstTurnTaskID := delegate.identity.TaskID
	firstTurn, err := tasks.Get(ctx, firstTurnTaskID)
	if err != nil || firstTurnTaskID == task.TaskID || firstTurn.ParentTaskID != task.TaskID || firstTurn.Status != domaintask.StatusSucceeded {
		t.Fatalf("first typed turn Task=%+v err=%v, want completed child under Game Task", firstTurn, err)
	}
	controllerRun, err := tasks.GetRun(ctx, run.RunID)
	if err != nil || controllerRun.Status != domaintask.RunStatusWaiting {
		t.Fatalf("controller Run=%+v err=%v, want pause during the in-flight child turn", controllerRun, err)
	}
	if _, err := provider.DecideGameTurn(ctx, request); err != nil {
		t.Fatalf("decision while typed controller is durably waiting: %v", err)
	}
	if delegate.calls != 2 || delegate.identity.TaskID == task.TaskID || delegate.identity.TaskID == firstTurnTaskID {
		t.Fatalf("waiting-controller turn identity=%+v calls=%d, want a new child Task/Run", delegate.identity, delegate.calls)
	}
	child, err := tasks.Get(ctx, delegate.identity.TaskID)
	if err != nil || child.ParentTaskID != task.TaskID || child.Status != domaintask.StatusSucceeded {
		t.Fatalf("waiting-controller child=%+v err=%v, want completed child of typed Game Task", child, err)
	}
	controllerRun, err = tasks.GetRun(ctx, run.RunID)
	if err != nil || controllerRun.Status != domaintask.RunStatusWaiting {
		t.Fatalf("controller Run=%+v err=%v, want it to remain waiting", controllerRun, err)
	}
	currentActivity, err := tasks.SnapshotCurrentActivities(ctx, []string{"aka"})
	if err != nil || currentActivity["aka"].Task.TaskID != task.TaskID {
		t.Fatalf("typed GAME Task stopped being current during decision: current=%+v err=%v", currentActivity, err)
	}
	wrong := request
	wrong.SessionID = "herzog_zwei_other_session"
	if _, err := provider.DecideGameTurn(ctx, wrong); err == nil {
		t.Fatal("wrong session unexpectedly reached Agent provider")
	}
	if delegate.calls != 2 {
		t.Fatalf("provider calls after wrong-session callback=%d, want two", delegate.calls)
	}

	completedReceipt := domaintask.GameLaunchReceipt{
		LaunchKey: string(task.TaskID), SessionID: sessionID, Status: domaintask.GameExecutionCompleted, Request: spec,
		Result:         &domaintask.GameLaunchResult{OK: true, GameID: spec.GameID, SessionID: sessionID, Status: string(domaintask.GameExecutionCompleted)},
		GameResultJSON: `{"winner":"aka","scores":{"aka":1}}`, CreatedAt: now, UpdatedAt: now.Add(2 * time.Second),
	}
	resumedRun, err := tasks.StartGameTaskRun(ctx, task.TaskID, modulecore.NewQueueItemID(), task.Assignee, run.RunID, spec.Digest())
	if err != nil {
		t.Fatalf("resume typed GAME controller after waiting turn: %v", err)
	}
	if err := scheduler.MarkGameGroupClosing(string(task.TaskID), sessionID); err != nil {
		t.Fatalf("mark terminal game group closing: %v", err)
	}
	finalized, err := tasks.FinalizeGameTaskRun(ctx, task.TaskID, resumedRun.RunID, task.Assignee, completedReceipt, "verified title completion")
	if err != nil || finalized.Status != domaintask.StatusSucceeded {
		t.Fatalf("finalize GAME Task = (status %q, error %v), want succeeded", finalized.Status, err)
	}
	if err := scheduler.ReleaseGameGroup(string(task.TaskID)); err != nil {
		t.Fatalf("release completed GAME actor group: %v", err)
	}
	if _, err := provider.DecideGameTurn(ctx, request); err == nil {
		t.Fatal("callback for the exact terminal Task session unexpectedly reached the legacy provider")
	}
	if delegate.calls != 2 {
		t.Fatalf("provider calls after terminal-session callback=%d, want two", delegate.calls)
	}

	delegate.allowLegacy = true
	legacy := request
	legacy.SessionID = "herzog_zwei_legacy_session"
	admission.sessions.(*gameTaskAdmissionSessions).source = gameObserverSessionSummary{
		GameID: "herzog_zwei", SessionID: legacy.SessionID, Persona: "aka", Personas: []string{"aka"}, Status: "running",
	}
	legacy.GameID = "herzog_zwei"
	if _, err := provider.DecideGameTurn(ctx, legacy); err != nil {
		t.Fatalf("different legacy session for the same actor/game was rejected: %v", err)
	}
	if delegate.calls != 3 {
		t.Fatalf("provider calls after different legacy session=%d, want three", delegate.calls)
	}
}

func TestLegacyGameDecisionCreatesChildRunUnderExactObserverGameAndKeepsItCurrent(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStoreWithOptions(t.TempDir(), taskpersistence.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	tasks := taskmanager.New(store, taskmanager.DefaultParallelLimits())
	const sessionID = "nethack_20261121_120000_1"
	reader := &gameTaskAdmissionSessions{source: gameObserverSessionSummary{
		GameID: "nethack", SessionID: sessionID, Persona: "mio", Personas: []string{"mio"}, Status: "running",
	}}
	admission := newGameTaskRuntimeAdmission(reader, tasks)
	delegate := &gameTurnIdentityCapture{tasks: tasks}
	turnOwner := newGameTurnExecutionProvider(delegate, tasks)
	heartbeat := newHeartbeatGameDecisionProvider(turnOwner, nil, false)
	provider := newGameTaskDecisionAdmissionProvider(heartbeat, tasks, &gameTaskLaunchClient{baseURL: "http://observer.invalid"}, admission, "configured-user")
	request := viewer.GameAgentDecisionRequest{GameID: "nethack", SessionID: sessionID, Persona: "mio", Turn: 1}
	if _, err := provider.DecideGameTurn(ctx, request); err != nil {
		t.Fatalf("legacy decision: %v", err)
	}
	if delegate.calls != 1 || delegate.identity.TaskID == "" || delegate.identity.RunID == "" {
		t.Fatalf("delegate calls=%d identity=%+v, want one child Task/Run identity", delegate.calls, delegate.identity)
	}
	child, err := tasks.Get(ctx, delegate.identity.TaskID)
	if err != nil || child.ParentTaskID != delegate.parentID || child.Status != domaintask.StatusSucceeded {
		t.Fatalf("child Task=%+v parent=%s err=%v, want completed child under Game parent", child, delegate.parentID, err)
	}
	if delegate.parentID == "" || child.ParentTaskID != delegate.parentID {
		t.Fatalf("child parent=%s, observed Game Task=%s", child.ParentTaskID, delegate.parentID)
	}
	current, err := tasks.SnapshotCurrentActivities(ctx, []string{"mio"})
	if err != nil || current["mio"].Task.TaskID != delegate.parentID || current["mio"].Task.Activity.Game.SessionID != sessionID {
		t.Fatalf("turn did not preserve exact Game current activity: current=%+v err=%v", current, err)
	}
}

func TestLegacyGameDecisionRejectsUnknownSessionAndDoesNotTakeOverForeground(t *testing.T) {
	for _, tc := range []struct {
		name          string
		busy          bool
		foreignRoster bool
		sessionID     string
	}{
		{name: "unknown observer session", sessionID: "nethack_missing"},
		{name: "foreground chat remains current", busy: true, sessionID: "nethack_busy"},
		{name: "persona absent from observer roster", foreignRoster: true, sessionID: "nethack_foreign_persona"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := taskpersistence.NewJSONLStoreWithOptions(t.TempDir(), taskpersistence.OpenOptions{Index: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			tasks := taskmanager.New(store, taskmanager.DefaultParallelLimits())
			if tc.busy {
				if _, _, err := tasks.CreateAndStartRun(ctx, domaintask.Task{Title: "foreground chat", Route: domaintask.RouteCHAT, Assignee: "mio"}, domaintask.SharedRoleContext{}); err != nil {
					t.Fatal(err)
				}
			}
			observerSessionID := tc.sessionID
			observerPersona := "mio"
			observerPersonas := []string{"mio"}
			if !tc.busy {
				observerSessionID = "nethack_different_observer_session"
			}
			if tc.foreignRoster {
				observerSessionID = tc.sessionID
				observerPersona = "shiro"
				observerPersonas = []string{"shiro"}
			}
			reader := &gameTaskAdmissionSessions{source: gameObserverSessionSummary{
				GameID: "nethack", SessionID: observerSessionID, Persona: observerPersona, Personas: observerPersonas, Status: "running",
			}}
			admission := newGameTaskRuntimeAdmission(reader, tasks)
			delegate := &gameTurnIdentityCapture{tasks: tasks}
			provider := newGameTaskDecisionAdmissionProvider(
				newHeartbeatGameDecisionProvider(newGameTurnExecutionProvider(delegate, tasks), nil, false),
				tasks, &gameTaskLaunchClient{baseURL: "http://observer.invalid"}, admission, "configured-user",
			)
			request := viewer.GameAgentDecisionRequest{GameID: "nethack", SessionID: tc.sessionID, Persona: "mio", Turn: 1}
			if _, err := provider.DecideGameTurn(ctx, request); err == nil {
				t.Fatal("decision unexpectedly reached the provider")
			}
			if delegate.calls != 0 {
				t.Fatalf("provider calls=%d, want 0", delegate.calls)
			}
			if tc.busy {
				current, err := tasks.SnapshotCurrentActivities(ctx, []string{"mio"})
				if err != nil || current["mio"].Task.Activity.Kind != domaintask.ActivityKindChat {
					t.Fatalf("foreground activity was replaced: current=%+v err=%v", current, err)
				}
			}
		})
	}
}

func TestLegacyGameTurnClosesRunWhenDecisionContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store, err := taskpersistence.NewJSONLStoreWithOptions(t.TempDir(), taskpersistence.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	tasks := taskmanager.New(store, taskmanager.DefaultParallelLimits())
	const sessionID = "nethack_20261121_120100_1"
	reader := &gameTaskAdmissionSessions{source: gameObserverSessionSummary{
		GameID: "nethack", SessionID: sessionID, Persona: "mio", Personas: []string{"mio"}, Status: "running",
	}}
	admission := newGameTaskRuntimeAdmission(reader, tasks)
	delegate := &gameTurnIdentityCapture{tasks: tasks, cancel: cancel}
	provider := newGameTaskDecisionAdmissionProvider(
		newHeartbeatGameDecisionProvider(newGameTurnExecutionProvider(delegate, tasks), nil, false),
		tasks, &gameTaskLaunchClient{baseURL: "http://observer.invalid"}, admission, "configured-user",
	)
	_, err = provider.DecideGameTurn(ctx, viewer.GameAgentDecisionRequest{GameID: "nethack", SessionID: sessionID, Persona: "mio", Turn: 1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled decision error=%v, want context.Canceled", err)
	}
	child, getErr := tasks.Get(context.Background(), delegate.identity.TaskID)
	if getErr != nil || child.Status != domaintask.StatusCancelled {
		t.Fatalf("cancelled turn Task=%+v err=%v, want terminal cancellation", child, getErr)
	}
	current, snapshotErr := tasks.SnapshotCurrentActivities(context.Background(), []string{"mio"})
	if snapshotErr != nil || current["mio"].Task.TaskID != delegate.parentID {
		t.Fatalf("cancelled turn changed Game current: current=%+v err=%v", current, snapshotErr)
	}
}

type gameTurnIdentityCapture struct {
	tasks    *taskmanager.Manager
	calls    int
	identity domainexecution.Identity
	parentID modulecore.TaskID
	cancel   context.CancelFunc
}

func (capture *gameTurnIdentityCapture) DecideGameTurn(ctx context.Context, _ viewer.GameAgentDecisionRequest) (viewer.GameBrainDecision, error) {
	capture.calls++
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		return viewer.GameBrainDecision{}, err
	}
	capture.identity = identity
	task, err := capture.tasks.Get(ctx, identity.TaskID)
	if err != nil {
		return viewer.GameBrainDecision{}, err
	}
	capture.parentID = task.ParentTaskID
	if capture.cancel != nil {
		capture.cancel()
		return viewer.GameBrainDecision{}, context.Canceled
	}
	return viewer.GameBrainDecision{AgentID: "mio", Persona: "mio", Intent: "wait", Confidence: 1}, nil
}

type gameTaskDecisionAdmissionCapture struct {
	calls          int
	auth           gameTaskTurnAuthorization
	identity       domainexecution.Identity
	allowLegacy    bool
	duringDecision func(context.Context) error
}

func (capture *gameTaskDecisionAdmissionCapture) DecideGameTurn(ctx context.Context, _ viewer.GameAgentDecisionRequest) (viewer.GameBrainDecision, error) {
	capture.calls++
	auth, ok := gameTaskTurnAuthorizationFromContext(ctx)
	if !ok {
		if capture.allowLegacy {
			return viewer.GameBrainDecision{AgentID: "aka", Persona: "aka", Intent: "hold", Confidence: 1}, nil
		}
		return viewer.GameBrainDecision{}, errors.New("Task turn authorization missing")
	}
	capture.auth = auth
	if identity, err := domainexecution.IdentityFromContext(ctx); err == nil {
		capture.identity = identity
	}
	if capture.duringDecision != nil {
		if err := capture.duringDecision(ctx); err != nil {
			return viewer.GameBrainDecision{}, err
		}
	}
	return viewer.GameBrainDecision{AgentID: "aka", Persona: "aka", Intent: "hold", Confidence: 1}, nil
}
