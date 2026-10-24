package tts

import (
	"testing"
	"time"
)

func TestPendingPlaybackStoreCompleteByPublicPlaybackRefClosesWaitAndTopicGate(t *testing.T) {
	store := NewPendingPlaybackStore()
	wait := store.Register("tts-1", "resp-1")
	store.RegisterTopicGate("idle-1", "tts-1")

	action := store.CompleteByPublicPlaybackRef("resp-1")
	if !action.Matched || action.ClearPublicBy != "resp-1" || !action.ClosePendingWait || !action.CloseTopicGate {
		t.Fatalf("unexpected action: %+v", action)
	}
	select {
	case <-wait:
	case <-time.After(time.Second):
		t.Fatal("pending wait should close")
	}
	if got := store.Snapshot(); got.PendingSessionCount != 0 || got.TopicGateCount != 0 {
		t.Fatalf("pending state should be empty: %+v", got)
	}
}

func TestPendingPlaybackStoreClearByWait(t *testing.T) {
	store := NewPendingPlaybackStore()
	wait := store.Register("tts-1", "resp-1")
	action := store.ClearByWait(wait)
	if !action.Matched || action.ClearPublicSession != "tts-1" {
		t.Fatalf("unexpected action: %+v", action)
	}
	select {
	case <-wait:
	case <-time.After(time.Second):
		t.Fatal("pending wait should close")
	}
}

func TestPendingPlaybackStoreClearAll(t *testing.T) {
	store := NewPendingPlaybackStore()
	first := store.Register("tts-1", "resp-1")
	second := store.Register("tts-2", "resp-2")
	store.RegisterTopicGate("idle-1", "tts-1")
	sessions := store.ClearAll()
	if len(sessions) != 2 {
		t.Fatalf("sessions = %#v, want 2", sessions)
	}
	for name, ch := range map[string]<-chan struct{}{"first": first, "second": second} {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatalf("%s wait should close", name)
		}
	}
	if got := store.Snapshot(); got.PendingSessionCount != 0 || got.PendingPublicPlaybackCount != 0 || got.TopicGateCount != 0 {
		t.Fatalf("pending state should be empty: %+v", got)
	}
}

func TestPendingPlaybackStoreCancelPlaybackGenerationPreservesNewerWait(t *testing.T) {
	store := NewPendingPlaybackStore()
	oldWait := store.RegisterForPlaybackGeneration("tts-old", "ref-old", PlaybackGenerationIdentity{
		RuntimeEpoch: "runtime-a", PlaybackGeneration: 4,
	})
	newWait := store.RegisterForPlaybackGeneration("tts-new", "ref-new", PlaybackGenerationIdentity{
		RuntimeEpoch: "runtime-a", PlaybackGeneration: 5,
	})

	sessions := store.CancelPlaybackGeneration("runtime-a", 4)
	if len(sessions) != 1 || sessions[0] != "tts-old" {
		t.Fatalf("cancelled sessions = %#v, want only tts-old", sessions)
	}
	select {
	case <-oldWait:
	case <-time.After(time.Second):
		t.Fatal("old playback wait should close")
	}
	select {
	case <-newWait:
		t.Fatal("newer playback wait should remain open")
	default:
	}
	lateOld := store.RegisterForPlaybackGeneration("tts-old-late", "ref-old-late", PlaybackGenerationIdentity{
		RuntimeEpoch: "runtime-a", PlaybackGeneration: 4,
	})
	select {
	case <-lateOld:
	default:
		t.Fatal("late old playback wait should be rejected as already complete")
	}
	if got := store.Snapshot(); got.PendingSessionCount != 1 || got.PendingPublicPlaybackCount != 1 {
		t.Fatalf("pending state should contain only the newer playback: %+v", got)
	}
}
