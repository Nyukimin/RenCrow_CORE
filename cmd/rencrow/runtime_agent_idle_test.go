package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

type idleGameDecisionProvider struct {
	mu       sync.Mutex
	request  viewer.GameAgentDecisionRequest
	calls    int
	decision viewer.GameBrainDecision
}

func (p *idleGameDecisionProvider) DecideGameTurn(_ context.Context, request viewer.GameAgentDecisionRequest) (viewer.GameBrainDecision, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.request = request
	p.calls++
	return p.decision, nil
}

type idleGameAdmission struct {
	mu       sync.Mutex
	actor    string
	priority domaintask.Priority
	acquires int
	releases int
	err      error
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
	request := viewer.GameAgentDecisionRequest{Persona: "Shiro"}
	if _, err := provider.DecideGameTurn(context.Background(), request); err != nil {
		t.Fatalf("DecideGameTurn() error = %v", err)
	}
	if admission.acquires != 1 || admission.actor != "shiro" || admission.priority != domaintask.PriorityLow || admission.releases != 1 {
		t.Fatalf("admission = actor %q priority %q acquired %d released %d; want shiro/low/1/1", admission.actor, admission.priority, admission.acquires, admission.releases)
	}
	if next.calls != 1 || next.request.Persona != "Shiro" {
		t.Fatalf("delegate calls=%d request persona=%q; want original request once", next.calls, next.request.Persona)
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
