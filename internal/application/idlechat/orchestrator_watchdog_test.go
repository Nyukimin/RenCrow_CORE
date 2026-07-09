package idlechat

import (
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
)

func TestWatchdogSnapshotReportsCurrentSequenceStage(t *testing.T) {
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "ren"}, 5, 10, 0.8, nil, "")
	o.mu.Lock()
	o.chatActive = true
	o.manualMode = true
	o.sessionMode = "idle"
	o.activeSessionID = "idle-1-topic-00"
	o.activeGeneration = 7
	o.watchdogStage = "response_generation"
	o.watchdogDetail = "Ren->Mio turn=2"
	o.watchdogFrom = "Ren"
	o.watchdogTo = "Mio"
	o.watchdogTurnIndex = 2
	o.watchdogUpdatedAt = now.Add(-45 * time.Second)
	o.mu.Unlock()

	snapshot := o.WatchdogSnapshot(now)
	if !snapshot.ChatActive || !snapshot.ManualMode {
		t.Fatalf("snapshot active/manual = %t/%t, want true/true", snapshot.ChatActive, snapshot.ManualMode)
	}
	if snapshot.Stage != "response_generation" || snapshot.Detail != "Ren->Mio turn=2" {
		t.Fatalf("snapshot stage/detail = %q/%q", snapshot.Stage, snapshot.Detail)
	}
	if snapshot.From != "Ren" || snapshot.To != "Mio" || snapshot.TurnIndex != 2 {
		t.Fatalf("snapshot route = %s->%s turn=%d", snapshot.From, snapshot.To, snapshot.TurnIndex)
	}
	if snapshot.AgeSeconds != 45 {
		t.Fatalf("snapshot age = %d, want 45", snapshot.AgeSeconds)
	}
}

func TestRecoverIfStalledInterruptsActiveIdleChat(t *testing.T) {
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "ren"}, 5, 10, 0.8, nil, "")
	o.mu.Lock()
	o.chatActive = true
	o.manualMode = true
	o.sessionMode = "idle"
	o.activeSessionID = "idle-1-topic-00"
	o.activeGeneration = 7
	o.watchdogStage = "tts_wait"
	o.watchdogDetail = "Ren->Mio turn=2"
	o.watchdogUpdatedAt = now.Add(-3 * time.Minute)
	o.mu.Unlock()

	recovery, ok := o.RecoverIfStalled(now, 2*time.Minute, "heartbeat_idlechat_sequence_stall")
	if !ok {
		t.Fatal("expected stale active session to recover")
	}
	if !recovery.Recovered || recovery.Before.Stage != "tts_wait" {
		t.Fatalf("unexpected recovery: %+v", recovery)
	}
	after := o.WatchdogSnapshot(now)
	if after.ChatActive || after.ManualMode || after.SessionID != "" {
		t.Fatalf("after recovery active/manual/session = %t/%t/%q", after.ChatActive, after.ManualMode, after.SessionID)
	}
}

func TestRecoverIfStalledKeepsFreshIdleChatActive(t *testing.T) {
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "ren"}, 5, 10, 0.8, nil, "")
	o.mu.Lock()
	o.chatActive = true
	o.manualMode = true
	o.activeSessionID = "idle-1-topic-00"
	o.watchdogStage = "response_generation"
	o.watchdogUpdatedAt = now.Add(-30 * time.Second)
	o.mu.Unlock()

	if _, ok := o.RecoverIfStalled(now, 2*time.Minute, "heartbeat_idlechat_sequence_stall"); ok {
		t.Fatal("fresh active session should not recover")
	}
	if !o.WatchdogSnapshot(now).ChatActive {
		t.Fatal("fresh active session should remain active")
	}
}
