package idlechat

import (
	"context"
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

func TestRecoverIfStalledAllowsBoundedDialogueGenerationToFinish(t *testing.T) {
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")
	o.mu.Lock()
	o.chatActive = true
	o.sessionMode = "forecast"
	o.activeSessionID = "forecast-1"
	o.activeGeneration = 7
	// This is the state observed while CodexExe is still preparing the
	// complete Forecast dialogue. It is older than the generic heartbeat
	// threshold, but younger than the bounded generation allowance.
	o.watchdogStage = "dialogue_generation"
	o.watchdogUpdatedAt = now.Add(-3 * time.Minute)
	o.mu.Unlock()

	if _, ok := o.RecoverIfStalled(now, 2*time.Minute, "heartbeat_idlechat_sequence_stall"); ok {
		t.Fatal("bounded dialogue generation must not be recovered at the generic threshold")
	}
	if !o.WatchdogSnapshot(now).ChatActive {
		t.Fatal("bounded dialogue generation should remain active")
	}

	if _, ok := o.RecoverIfStalled(now.Add(11*time.Minute), 2*time.Minute, "heartbeat_idlechat_sequence_stall"); !ok {
		t.Fatal("dialogue generation that exceeds its bound must remain recoverable")
	}
}

type blockingForecastDialogueGenerator struct {
	started chan struct{}
}

func (g *blockingForecastDialogueGenerator) Generate(ctx context.Context, _ string) (string, error) {
	close(g.started)
	<-ctx.Done()
	return "", ctx.Err()
}

func TestForecastMarksBoundedDialogueGenerationStage(t *testing.T) {
	domain := forecastDomains[0]
	generator := &blockingForecastDialogueGenerator{started: make(chan struct{})}
	config := DefaultDialogueInterestingnessConfig()
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")
	o.SetDialogueEpisodeService(NewPersistentDialogueEpisodeService("", generator, map[string]string{
		"mio":   "Mio canonical",
		"shiro": "Shiro canonical",
	}, config))
	stock := newForecastTopicStock("")
	if !stock.push(domain.Name, PreparedTopic{Domain: domain, Topic: "検証用の未来展望", Created: time.Now().UTC()}) {
		t.Fatal("failed to prepare forecast topic")
	}
	o.emitMu.Lock()
	o.mu.Lock()
	o.topicStockBuf = stock
	o.chatActive = true
	o.sessionMode = "forecast"
	generation := o.beginIdleRunLocked()
	o.bindIdleSessionLocked(canonicalIdleChatTestSessionID("forecast-watchdog-test"))
	o.mu.Unlock()
	o.emitMu.Unlock()

	done := make(chan struct{})
	go func() {
		o.runForecastSessionDomains(canonicalIdleChatTestSessionID("forecast-watchdog-test"), generation, time.Now().In(jst), []ForecastDomain{domain})
		close(done)
	}()
	select {
	case <-generator.started:
	case <-time.After(time.Second):
		t.Fatal("forecast dialogue generation did not start")
	}

	snapshot, stageDeadlineAt := o.watchdogSnapshot(time.Now())
	if snapshot.Stage != watchdogDialogueGenerationStage {
		t.Fatalf("watchdog stage = %q, want %q", snapshot.Stage, watchdogDialogueGenerationStage)
	}
	if stageDeadlineAt.IsZero() || !stageDeadlineAt.After(time.Now().Add(9*time.Minute)) {
		t.Fatalf("dialogue generation deadline = %v, want at least nine minutes ahead", stageDeadlineAt)
	}

	o.Interrupt("watchdog-test")
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("interrupt did not stop forecast dialogue generation")
	}
}

var _ IdleChatCodexGenerator = (*blockingForecastDialogueGenerator)(nil)
