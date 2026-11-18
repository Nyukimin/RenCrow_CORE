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
	delegate := &gameTaskDecisionAdmissionCapture{}
	heartbeat := newHeartbeatGameDecisionProvider(delegate, func() conversationruntime.ActorAdmission {
		return scheduler
	}, true)
	provider := newGameTaskDecisionAdmissionProvider(heartbeat, tasks, client, admission)

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
	if err := tasks.ValidateRunExecution(ctx, task.TaskID, run.RunID, task.Assignee); err != nil {
		t.Fatalf("decision callback unexpectedly closed controller Run: %v", err)
	}

	wrong := request
	wrong.SessionID = "herzog_zwei_other_session"
	if _, err := provider.DecideGameTurn(ctx, wrong); err == nil {
		t.Fatal("wrong session unexpectedly reached Agent provider")
	}
	if delegate.calls != 1 {
		t.Fatalf("provider calls after wrong-session callback=%d, want one", delegate.calls)
	}

	completedReceipt := domaintask.GameLaunchReceipt{
		LaunchKey: string(task.TaskID), SessionID: sessionID, Status: domaintask.GameExecutionCompleted, Request: spec,
		Result:         &domaintask.GameLaunchResult{OK: true, GameID: spec.GameID, SessionID: sessionID, Status: string(domaintask.GameExecutionCompleted)},
		GameResultJSON: `{"winner":"aka","scores":{"aka":1}}`, CreatedAt: now, UpdatedAt: now.Add(2 * time.Second),
	}
	if err := scheduler.MarkGameGroupClosing(string(task.TaskID), sessionID); err != nil {
		t.Fatalf("mark terminal game group closing: %v", err)
	}
	finalized, err := tasks.FinalizeGameTaskRun(ctx, task.TaskID, run.RunID, task.Assignee, completedReceipt, "verified title completion")
	if err != nil || finalized.Status != domaintask.StatusSucceeded {
		t.Fatalf("finalize GAME Task = (status %q, error %v), want succeeded", finalized.Status, err)
	}
	if err := scheduler.ReleaseGameGroup(string(task.TaskID)); err != nil {
		t.Fatalf("release completed GAME actor group: %v", err)
	}
	if _, err := provider.DecideGameTurn(ctx, request); err == nil {
		t.Fatal("callback for the exact terminal Task session unexpectedly reached the legacy provider")
	}
	if delegate.calls != 1 {
		t.Fatalf("provider calls after terminal-session callback=%d, want one", delegate.calls)
	}

	delegate.allowLegacy = true
	legacy := request
	legacy.SessionID = "herzog_zwei_legacy_session"
	if _, err := provider.DecideGameTurn(ctx, legacy); err != nil {
		t.Fatalf("different legacy session for the same actor/game was rejected: %v", err)
	}
	if delegate.calls != 2 {
		t.Fatalf("provider calls after different legacy session=%d, want two", delegate.calls)
	}
}

type gameTaskDecisionAdmissionCapture struct {
	calls       int
	auth        gameTaskTurnAuthorization
	allowLegacy bool
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
	return viewer.GameBrainDecision{AgentID: "aka", Persona: "aka", Intent: "hold", Confidence: 1}, nil
}
