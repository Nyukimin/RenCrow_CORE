package main

import (
	"context"
	"testing"

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
