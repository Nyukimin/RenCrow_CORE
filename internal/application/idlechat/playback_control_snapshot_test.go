package idlechat

import (
	"context"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestPlaybackControlSnapshotChangedHandlerRunsOutsideOwnerLock(t *testing.T) {
	orchestrator := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "ren"}, 5, 10, 0.8, nil, "")
	defer orchestrator.Stop()

	changed := make(chan PlaybackControlSnapshot, 1)
	orchestrator.SetPlaybackControlSnapshotChangedHandler(func() {
		changed <- orchestrator.PlaybackControlSnapshot()
	})
	orchestrator.SetRuntimeEpoch("runtime-2")

	select {
	case snapshot := <-changed:
		if snapshot.RuntimeEpoch != "runtime-2" {
			t.Fatalf("runtime epoch = %q, want runtime-2", snapshot.RuntimeEpoch)
		}
	case <-time.After(time.Second):
		t.Fatal("snapshot change callback did not return after reading owner state")
	}
}

func TestStopPlaybackPublishesControlStateBeforeReturningReceipt(t *testing.T) {
	orchestrator := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "ren"}, 5, 10, 0.8, nil, "")
	defer orchestrator.Stop()
	orchestrator.SetRuntimeEpoch("runtime-stop")

	changed := make(chan PlaybackControlSnapshot, 2)
	orchestrator.SetPlaybackControlSnapshotChangedHandler(func() {
		changed <- orchestrator.PlaybackControlSnapshot()
	})
	orchestrator.mu.Lock()
	orchestrator.chatActive = true
	orchestrator.manualMode = true
	orchestrator.activeGeneration = 7
	orchestrator.mu.Unlock()

	receipt, err := orchestrator.StopPlaybackForForeground(context.Background(), PlaybackStopRequest{
		RuntimeEpoch:  "runtime-stop",
		StateRevision: 11,
		Reason:        "foreground_presence",
	})
	if err != nil {
		t.Fatalf("StopPlaybackForForeground() error = %v", err)
	}
	if !receipt.PlaybackStopped {
		t.Fatal("stop receipt did not record the active playback stop")
	}

	select {
	case snapshot := <-changed:
		if snapshot.PlaybackActive || snapshot.PlaybackGeneration != receipt.Generation {
			t.Fatalf("stop snapshot = %+v, receipt = %+v", snapshot, receipt)
		}
	case <-time.After(time.Second):
		t.Fatal("stop did not publish its RAM state before returning the receipt")
	}
}

func TestPlaybackControlSnapshotTracksManagedGenerationWork(t *testing.T) {
	orchestrator := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "ren"}, 5, 10, 0.8, nil, "")
	defer orchestrator.Stop()
	orchestrator.SetRuntimeEpoch("runtime-work")

	changed := make(chan PlaybackControlSnapshot, 2)
	orchestrator.SetPlaybackControlSnapshotChangedHandler(func() {
		changed <- orchestrator.PlaybackControlSnapshot()
	})
	lease, ok := orchestrator.acquireGenerationWork()
	if !ok {
		t.Fatal("generation work was not admitted")
	}
	select {
	case snapshot := <-changed:
		if snapshot.GenerationQuiesced || snapshot.GenerationState != "running" {
			t.Fatalf("active work snapshot = %+v", snapshot)
		}
	case <-time.After(time.Second):
		t.Fatal("generation work admission did not notify the snapshot owner")
	}

	lease.release()
	select {
	case snapshot := <-changed:
		if !snapshot.GenerationQuiesced || snapshot.GenerationState != "quiesced" {
			t.Fatalf("quiesced work snapshot = %+v", snapshot)
		}
	case <-time.After(time.Second):
		t.Fatal("generation work completion did not notify the snapshot owner")
	}
}

func TestPlaybackEventGenerationFenceRejectsOutputAfterStop(t *testing.T) {
	orchestrator := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "ren"}, 5, 10, 0.8, nil, "")
	defer orchestrator.Stop()
	orchestrator.SetRuntimeEpoch("runtime-event")
	sessionID := string(modulecore.NewSessionID())
	generation := activateIdleChatTestSession(orchestrator, sessionID)
	orchestrator.mu.Lock()
	traceID := orchestrator.activeTraceID
	orchestrator.mu.Unlock()

	if _, ok := orchestrator.PlaybackEventContext(traceID, modulecore.SessionID(sessionID), "runtime-event", generation); !ok {
		t.Fatal("current playback generation was rejected")
	}
	receipt, err := orchestrator.StopPlaybackForForeground(context.Background(), PlaybackStopRequest{
		RuntimeEpoch: "runtime-event", StateRevision: 1, Reason: "foreground_presence",
	})
	if err != nil {
		t.Fatalf("StopPlaybackForForeground() error = %v", err)
	}
	if receipt.Generation <= generation {
		t.Fatalf("stop generation = %d, active generation = %d", receipt.Generation, generation)
	}

	accepted := false
	current, err := orchestrator.PublishIfCurrentPlaybackEvent(traceID, modulecore.SessionID(sessionID), "runtime-event", generation, func() error {
		accepted = true
		return nil
	})
	if err != nil {
		t.Fatalf("PublishIfCurrentPlaybackEvent() error = %v", err)
	}
	if current || accepted || orchestrator.IsCurrentPlaybackEvent(traceID, modulecore.SessionID(sessionID), "runtime-event", generation) {
		t.Fatal("stopped playback generation remained eligible for presentation")
	}
}
