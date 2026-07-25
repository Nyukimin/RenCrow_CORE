package main

import (
	"context"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

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
	leaseCtx, release, ok := tracker.TryAcquireIdleLease(context.Background())
	if !ok {
		t.Fatal("idle lease should be acquired")
	}
	defer release()
	if _, _, second := tracker.TryAcquireIdleLease(context.Background()); second {
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
