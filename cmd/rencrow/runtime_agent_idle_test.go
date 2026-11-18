package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentheartbeat"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

type idleGameDecisionProvider struct {
	mu       sync.Mutex
	request  viewer.GameAgentDecisionRequest
	calls    int
	decision viewer.GameBrainDecision
	during   func(context.Context, viewer.GameAgentDecisionRequest)
	err      error
}

func (p *idleGameDecisionProvider) DecideGameTurn(ctx context.Context, request viewer.GameAgentDecisionRequest) (viewer.GameBrainDecision, error) {
	p.mu.Lock()
	p.request = request
	p.calls++
	during := p.during
	decision := p.decision
	err := p.err
	p.mu.Unlock()
	if during != nil {
		during(ctx, request)
	}
	return decision, err
}

type idleGameAdmission struct {
	mu           sync.Mutex
	actor        string
	priority     domaintask.Priority
	acquires     int
	taskAcquires int
	gameID       string
	launchKey    string
	sessionID    string
	releases     int
	err          error
}

func (a *idleGameAdmission) AcquireGameTaskTurn(_ context.Context, actor string, priority domaintask.Priority, gameID, launchKey, sessionID string) (func(), error) {
	a.mu.Lock()
	a.actor = actor
	a.priority = priority
	a.gameID = gameID
	a.launchKey = launchKey
	a.sessionID = sessionID
	a.taskAcquires++
	err := a.err
	a.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return func() {
		a.mu.Lock()
		a.releases++
		a.mu.Unlock()
	}, nil
}

func (a *idleGameAdmission) Acquire(_ context.Context, actor string, priority domaintask.Priority) (func(), error) {
	a.mu.Lock()
	a.actor = actor
	a.priority = priority
	a.acquires++
	err := a.err
	a.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return func() {
		a.mu.Lock()
		a.releases++
		a.mu.Unlock()
	}, nil
}

func TestGameAutoplayIdleSourceUsesOneOwnerAndHonorsServiceCooldown(t *testing.T) {
	now := time.Date(2026, time.November, 12, 12, 0, 0, 0, time.UTC)
	calls := 0
	source := newGameAutoplayIdleSource(func(context.Context) time.Duration {
		calls++
		return 30 * time.Minute
	}, func() time.Time { return now })

	if _, found, err := source(context.Background(), "shiro"); err != nil || found {
		t.Fatalf("non-owner source result = found %t, err %v; want no work", found, err)
	}
	work, found, err := source(context.Background(), gameAutoplayIdleOwnerActor)
	if err != nil || !found {
		t.Fatalf("owner source result = found %t, err %v; want work", found, err)
	}
	if work.Priority != domaintask.PriorityLow {
		t.Fatalf("idle work priority = %q, want low", work.Priority)
	}
	if calls != 0 {
		t.Fatalf("discovery ran autoplay %d times, want 0", calls)
	}
	if err := work.Run(context.Background(), make(chan struct{})); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("autoplay calls = %d, want 1", calls)
	}
	if _, found, err := source(context.Background(), gameAutoplayIdleOwnerActor); err != nil || found {
		t.Fatalf("source before cooldown = found %t, err %v; want no work", found, err)
	}

	now = now.Add(30 * time.Minute)
	if _, found, err := source(context.Background(), gameAutoplayIdleOwnerActor); err != nil || !found {
		t.Fatalf("source at cooldown = found %t, err %v; want work", found, err)
	}
}

func TestGameAutoplayIdleSourceCancelsRunWhenSchedulerStopsIdleWork(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	source := newGameAutoplayIdleSource(func(ctx context.Context) time.Duration {
		close(started)
		<-ctx.Done()
		close(canceled)
		return time.Minute
	}, time.Now)
	work, found, err := source(context.Background(), gameAutoplayIdleOwnerActor)
	if err != nil || !found {
		t.Fatalf("source result = found %t, err %v; want work", found, err)
	}
	stop := make(chan struct{})
	runDone := make(chan error, 1)
	go func() { runDone <- work.Run(context.Background(), stop) }()
	<-started
	close(stop)
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("RunOnce context was not canceled after idle stop")
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("idle Run did not return after cancellation")
	}
}

func TestHeartbeatGameDecisionProviderReservesTheRequestedTurnActor(t *testing.T) {
	next := &idleGameDecisionProvider{decision: viewer.GameBrainDecision{
		AgentID: "shiro", Persona: "shiro", Intent: "search", Confidence: 0.9,
	}}
	admission := &idleGameAdmission{}
	provider := newHeartbeatGameDecisionProvider(next, func() conversationruntime.ActorAdmission { return admission }, true)
	request := viewer.GameAgentDecisionRequest{Persona: "Shiro", GameID: "nethack"}
	if _, err := provider.DecideGameTurn(context.Background(), request); err != nil {
		t.Fatalf("DecideGameTurn() error = %v", err)
	}
	if admission.acquires != 1 || admission.actor != "shiro" || admission.priority != domaintask.PriorityLow || admission.releases != 1 {
		t.Fatalf("admission = actor %q priority %q acquired %d released %d; want shiro/low/1/1", admission.actor, admission.priority, admission.acquires, admission.releases)
	}
	if next.calls != 1 || next.request.Persona != "Shiro" || next.request.GameID != "nethack" {
		t.Fatalf("delegate calls=%d request=(persona %q game %q); want original request once", next.calls, next.request.Persona, next.request.GameID)
	}
}

func TestHeartbeatGameDecisionProviderUsesBoundTaskGroupIdentity(t *testing.T) {
	next := &idleGameDecisionProvider{decision: viewer.GameBrainDecision{AgentID: "aka", Persona: "aka", Intent: "search", Confidence: 0.9}}
	admission := &idleGameAdmission{}
	provider := newHeartbeatGameDecisionProvider(next, func() conversationruntime.ActorAdmission { return admission }, true)
	ctx := withGameTaskTurnAuthorization(context.Background(), gameTaskTurnAuthorization{
		LaunchKey: "task-key", GameID: "herzog_zwei", SessionID: "hz_session_1",
	})
	request := viewer.GameAgentDecisionRequest{Persona: "aka", GameID: "herzog_zwei", SessionID: "hz_session_1"}
	if _, err := provider.DecideGameTurn(ctx, request); err != nil {
		t.Fatalf("DecideGameTurn() error=%v", err)
	}
	if admission.taskAcquires != 1 || admission.acquires != 0 || admission.actor != "aka" || admission.gameID != "herzog_zwei" ||
		admission.launchKey != "task-key" || admission.sessionID != "hz_session_1" || admission.priority != domaintask.PriorityLow || admission.releases != 1 {
		t.Fatalf("group admission=%+v; want exact Task key/session and one released per-turn lease", admission)
	}
}

func TestHeartbeatGameDecisionProviderProjectsRealNethackTurnToLivingHeartbeat(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	scheduler := agentheartbeat.New([]string{"mio"}, agentheartbeat.Sources{}, nil)
	started := make(chan error, 1)
	go func() { started <- scheduler.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-started:
			if err != nil {
				t.Errorf("Start() = %v", err)
			}
		case <-time.After(time.Second):
			t.Error("scheduler did not stop")
		}
		if err := scheduler.Wait(context.Background()); err != nil {
			t.Errorf("Wait() = %v", err)
		}
	})
	deadline := time.Now().Add(time.Second)
	ready := false
	for time.Now().Before(deadline) {
		if _, ready = scheduler.LivingSnapshot(context.Background(), nil); ready {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !ready {
		t.Fatal("scheduler did not become ready")
	}

	var activeHeartbeat string
	next := &idleGameDecisionProvider{
		decision: viewer.GameBrainDecision{AgentID: "mio", Persona: "mio", Intent: "search", Confidence: 0.9},
		during: func(context.Context, viewer.GameAgentDecisionRequest) {
			recorder := httptest.NewRecorder()
			heartbeatRequest := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18790/viewer/living/heartbeat", nil)
			heartbeatRequest.RemoteAddr = "127.0.0.1:49152"
			viewer.HandleLivingHeartbeat(scheduler, nil, nil).ServeHTTP(recorder, heartbeatRequest)
			if recorder.Code != http.StatusOK {
				t.Errorf("active heartbeat status=%d body=%s, want 200", recorder.Code, recorder.Body.String())
			}
			activeHeartbeat = recorder.Body.String()
		},
	}
	provider := newHeartbeatGameDecisionProvider(next, func() conversationruntime.ActorAdmission { return scheduler }, true)
	request := viewer.GameAgentDecisionRequest{
		GameID: "nethack", SessionID: "session-private", Turn: 1, Persona: "mio",
		Observation: map[string]any{"private_note": "must-not-appear"}, AvailableActions: []string{"search"}, Request: "choose_next_action",
	}
	if _, err := provider.DecideGameTurn(context.Background(), request); err != nil {
		t.Fatalf("DecideGameTurn() error = %v", err)
	}

	if !strings.Contains(activeHeartbeat, `"activity_kind":"game"`) || !strings.Contains(activeHeartbeat, `"game_id":"nethack"`) {
		t.Fatalf("active heartbeat = %s, want game/nethack while provider runs", activeHeartbeat)
	}
	for _, privateValue := range []string{"session-private", "private_note", "must-not-appear", "choose_next_action"} {
		if strings.Contains(activeHeartbeat, privateValue) {
			t.Fatalf("active heartbeat leaked %q: %s", privateValue, activeHeartbeat)
		}
	}
	recorder := httptest.NewRecorder()
	heartbeatRequest := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18790/viewer/living/heartbeat", nil)
	heartbeatRequest.RemoteAddr = "127.0.0.1:49152"
	viewer.HandleLivingHeartbeat(scheduler, nil, nil).ServeHTTP(recorder, heartbeatRequest)
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), `"state":"running"`) {
		t.Fatalf("released game turn heartbeat status=%d body=%s, want no active game lease", recorder.Code, recorder.Body.String())
	}
	for _, privateValue := range []string{"nethack", "game_id", "session-private", "private_note", "must-not-appear", "choose_next_action"} {
		if strings.Contains(recorder.Body.String(), privateValue) {
			t.Fatalf("released heartbeat leaked %q: %s", privateValue, recorder.Body.String())
		}
	}

	failure := errors.New("game decision failed")
	next.mu.Lock()
	next.err = failure
	next.mu.Unlock()
	if _, err := provider.DecideGameTurn(context.Background(), request); !errors.Is(err, failure) {
		t.Fatalf("failed DecideGameTurn() error = %v, want %v", err, failure)
	}
	recorder = httptest.NewRecorder()
	heartbeatRequest = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18790/viewer/living/heartbeat", nil)
	heartbeatRequest.RemoteAddr = "127.0.0.1:49152"
	viewer.HandleLivingHeartbeat(scheduler, nil, nil).ServeHTTP(recorder, heartbeatRequest)
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "game_id") {
		t.Fatalf("heartbeat after failed turn status=%d body=%s, want no active game descriptor", recorder.Code, recorder.Body.String())
	}
}

func TestHeartbeatGameDecisionProviderRejectsTurnWhenAdmissionIsUnavailable(t *testing.T) {
	next := &idleGameDecisionProvider{}
	provider := newHeartbeatGameDecisionProvider(next, func() conversationruntime.ActorAdmission { return nil }, true)
	_, err := provider.DecideGameTurn(context.Background(), viewer.GameAgentDecisionRequest{Persona: "mio"})
	if err == nil {
		t.Fatal("DecideGameTurn() error = nil, want admission unavailable error")
	}
	if next.calls != 0 {
		t.Fatalf("delegate calls = %d, want 0 when admission unavailable", next.calls)
	}
}

func TestHeartbeatGameDecisionProviderReturnsAdmissionErrorsWithoutCallingProvider(t *testing.T) {
	next := &idleGameDecisionProvider{}
	wantErr := errors.New("actor lease closed")
	admission := &idleGameAdmission{err: wantErr}
	provider := newHeartbeatGameDecisionProvider(next, func() conversationruntime.ActorAdmission { return admission }, true)
	_, err := provider.DecideGameTurn(context.Background(), viewer.GameAgentDecisionRequest{Persona: "kuro"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("DecideGameTurn() error = %v, want wrapped admission error %v", err, wantErr)
	}
	if admission.acquires != 1 || next.calls != 0 {
		t.Fatalf("admission acquires=%d provider calls=%d; want 1/0", admission.acquires, next.calls)
	}
}
