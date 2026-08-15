package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

type countingLLMProvider struct {
	calls atomic.Int32
}

func (p *countingLLMProvider) Generate(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.calls.Add(1)
	return llm.GenerateResponse{}, nil
}

func (p *countingLLMProvider) Name() string {
	return "counting"
}

func TestLLMBusyTrackerSeparatesIdleChatFromExternalBusy(t *testing.T) {
	tracker := newLLMBusyTracker()
	endChat := tracker.Begin(context.Background(), "chat")
	endIdle := tracker.Begin(llm.WithBusySource(context.Background(), "idlechat"), "chat")

	snapshot := tracker.Snapshot()
	if !snapshot.Active || snapshot.ActiveCount != 2 {
		t.Fatalf("active snapshot = %+v, want active_count=2", snapshot)
	}
	if !snapshot.External || snapshot.ExternalCount != 1 || snapshot.ExternalSources["chat"] != 1 {
		t.Fatalf("external snapshot = %+v, want one chat external source", snapshot)
	}

	endChat()
	snapshot = tracker.Snapshot()
	if !snapshot.Active || snapshot.ActiveCount != 1 {
		t.Fatalf("active after chat done = %+v, want idlechat active", snapshot)
	}
	if snapshot.External || snapshot.ExternalCount != 0 {
		t.Fatalf("external after chat done = %+v, want no external busy", snapshot)
	}

	endIdle()
	if snapshot = tracker.Snapshot(); snapshot.Active || snapshot.ActiveCount != 0 {
		t.Fatalf("snapshot after all done = %+v, want inactive", snapshot)
	}
}

func TestLLMBusyTrackerIdleLeaseIsAtomicAndCancelledByForegroundWork(t *testing.T) {
	tracker := newLLMBusyTracker()
	leaseCtx, release, ok := tracker.TryAcquireIdleLease(context.Background(), "profile_promotion")
	if !ok {
		t.Fatal("idle lease should be acquired")
	}
	defer release()
	if _, _, second := tracker.TryAcquireIdleLease(context.Background(), "other_background"); second {
		t.Fatal("second idle lease should be rejected")
	}

	endBackground := tracker.Begin(leaseCtx, "profile_promotion")
	select {
	case <-leaseCtx.Done():
		t.Fatal("leased background request cancelled itself")
	default:
	}
	endForeground := tracker.Begin(context.Background(), "chat")
	select {
	case <-leaseCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("foreground request did not cancel idle lease")
	}
	endForeground()
	endBackground()
}

func TestLLMBusyTrackerIdleLeaseIsVisibleAsExternalBusyUntilReleased(t *testing.T) {
	tracker := newLLMBusyTracker()
	_, release, ok := tracker.TryAcquireIdleLease(context.Background(), "profile_promotion")
	if !ok {
		t.Fatal("idle lease should be acquired")
	}

	snapshot := tracker.Snapshot()
	if !snapshot.Active || snapshot.ActiveCount != 1 {
		t.Fatalf("active snapshot = %+v, want idle lease as one active request", snapshot)
	}
	if !snapshot.External || snapshot.ExternalCount != 1 {
		t.Fatalf("external snapshot = %+v, want idle lease to pause IdleChat", snapshot)
	}
	if snapshot.ExternalSources["profile_promotion"] != 1 {
		t.Fatalf("external sources = %+v, want named idle lease source", snapshot.ExternalSources)
	}

	release()
	if snapshot = tracker.Snapshot(); snapshot.Active || snapshot.External {
		t.Fatalf("snapshot after release = %+v, want inactive", snapshot)
	}
}

func TestTrackedProviderAtomicallyRejectsIdleChatWhenIdleLeaseIsHeld(t *testing.T) {
	tracker := newLLMBusyTracker()
	leaseCtx, release, ok := tracker.TryAcquireIdleLease(context.Background(), "profile_promotion")
	if !ok {
		t.Fatal("idle lease should be acquired")
	}
	defer release()

	inner := &countingLLMProvider{}
	provider := trackLLMProvider("chat", inner, tracker)
	_, err := provider.Generate(
		llm.WithBusySource(context.Background(), "idlechat"),
		llm.GenerateRequest{},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("idlechat generate error = %v, want context canceled", err)
	}
	if inner.calls.Load() != 0 {
		t.Fatalf("inner provider calls = %d, want zero", inner.calls.Load())
	}
	select {
	case <-leaseCtx.Done():
		t.Fatal("rejected IdleChat must not preempt the ProfilePromotion lease")
	default:
	}
}
