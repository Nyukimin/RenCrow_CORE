package idlechat

import (
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestEmitTopicToTimelineDoesNotWaitForTTSCompletion(t *testing.T) {
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")
	ttsDone := make(chan struct{})
	eventSeen := make(chan struct{}, 1)
	o.SetEventEmitter(func(ev TimelineEvent) TTSLifecycle {
		if ev.Type != "idlechat.topic" {
			t.Fatalf("unexpected event type: %s", ev.Type)
		}
		if !strings.HasPrefix(ev.MessageID, "msg_") || ev.TurnIndex != 0 {
			t.Fatalf("unexpected topic identity: %+v", ev)
		}
		eventSeen <- struct{}{}
		return TTSLifecycle{Ready: ttsDone, Done: ttsDone}
	})
	activateIdleChatTestSession(o, canonicalIdleChatTestSessionID("idle-wait"))
	o.mu.Lock()
	generation := o.activeGeneration
	o.mu.Unlock()

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		o.emitTopicToTimeline(canonicalIdleChatTestSessionID("idle-wait"), "記憶と風景の関係", StrategyExternalStimulus, generation)
	}()

	select {
	case <-eventSeen:
	case <-time.After(time.Second):
		t.Fatal("topic event was not emitted")
	}
	select {
	case <-returned:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("emitTopicToTimeline waited for TTS completion")
	}
	close(ttsDone)
}

func TestWaitForTTSReadyTimesOut(t *testing.T) {
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")
	generation := activateIdleChatTestSession(o, canonicalIdleChatTestSessionID("idle-timeout"))
	o.mu.Lock()
	traceID := o.activeTraceID
	o.mu.Unlock()
	old := idleChatTTSWaitTimeout
	idleChatTTSWaitTimeout = 10 * time.Millisecond
	defer func() { idleChatTTSWaitTimeout = old }()
	var timeoutEvent TTSTimeoutEvent
	o.SetTTSTimeoutReporter(func(ev TTSTimeoutEvent) {
		timeoutEvent = ev
	})

	blocked := make(chan struct{})
	start := time.Now()
	o.waitForTTSReadyForEvent(TimelineEvent{
		SessionID:  canonicalIdleChatTestSessionID("idle-timeout"),
		MessageID:  "idle-timeout:msg:0001",
		TurnIndex:  1,
		TraceID:    traceID,
		Generation: generation,
	}, TTSLifecycle{Ready: blocked})

	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("waitForTTSReady did not time out promptly: %s", elapsed)
	}
	if timeoutEvent.Kind != "timeout" || timeoutEvent.SessionID != canonicalIdleChatTestSessionID("idle-timeout") || timeoutEvent.MessageID != "idle-timeout:msg:0001" || timeoutEvent.TurnIndex != 1 {
		t.Fatalf("unexpected timeout event: %#v", timeoutEvent)
	}
}

func TestIdleChatTTSWaitTimeoutDefaultIsSixtySeconds(t *testing.T) {
	if idleChatTTSWaitTimeout != 60*time.Second {
		t.Fatalf("unexpected idleChatTTSWaitTimeout: %s", idleChatTTSWaitTimeout)
	}
}

func TestIdleChatTTSSessionDrainTimeoutDefaultIsOneHundredTwentySeconds(t *testing.T) {
	if idleChatTTSSessionDrainTimeout != 120*time.Second {
		t.Fatalf("unexpected idleChatTTSSessionDrainTimeout: %s", idleChatTTSSessionDrainTimeout)
	}
}

func TestWaitForTTSSessionDrainWaitsForOutstandingPlaybackBeforeNextSession(t *testing.T) {
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")
	old := idleChatTTSSessionDrainTimeout
	idleChatTTSSessionDrainTimeout = 200 * time.Millisecond
	defer func() { idleChatTTSSessionDrainTimeout = old }()

	done := make(chan struct{})
	go func() {
		time.Sleep(30 * time.Millisecond)
		close(done)
	}()
	start := time.Now()
	o.waitForTTSSessionDrain("idle-drain", 1, []TTSLifecycle{{Done: done}})

	if elapsed := time.Since(start); elapsed < 25*time.Millisecond {
		t.Fatalf("session drain returned before outstanding playback completed: %s", elapsed)
	}
}

func TestWaitForTTSSessionDrainTimesOutInsteadOfStoppingSystem(t *testing.T) {
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")
	generation := activateIdleChatTestSession(o, canonicalIdleChatTestSessionID("idle-drain-timeout"))
	old := idleChatTTSSessionDrainTimeout
	idleChatTTSSessionDrainTimeout = 10 * time.Millisecond
	defer func() { idleChatTTSSessionDrainTimeout = old }()
	var timeoutEvent TTSTimeoutEvent
	o.SetTTSTimeoutReporter(func(ev TTSTimeoutEvent) {
		timeoutEvent = ev
	})

	blocked := make(chan struct{})
	start := time.Now()
	o.waitForTTSSessionDrain(canonicalIdleChatTestSessionID("idle-drain-timeout"), generation, []TTSLifecycle{{Done: blocked}})

	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("session drain did not time out promptly: %s", elapsed)
	}
	if timeoutEvent.Kind != "session_audio_timeout" || timeoutEvent.SessionID != canonicalIdleChatTestSessionID("idle-drain-timeout") || timeoutEvent.RemainingIndex != 1 || timeoutEvent.RemainingCount != 1 {
		t.Fatalf("unexpected drain timeout event: %#v", timeoutEvent)
	}
}

func TestIdleChatTTSTimeoutCarriesValidatedTraceAndGeneration(t *testing.T) {
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")
	generation := activateIdleChatTestSession(o, canonicalIdleChatTestSessionID("idle-timeout-trace"))
	o.mu.Lock()
	traceID := o.activeTraceID
	o.mu.Unlock()
	old := idleChatTTSWaitTimeout
	idleChatTTSWaitTimeout = 10 * time.Millisecond
	defer func() { idleChatTTSWaitTimeout = old }()
	var timeoutEvent TTSTimeoutEvent
	o.SetTTSTimeoutReporter(func(ev TTSTimeoutEvent) {
		timeoutEvent = ev
	})

	o.waitForTTSReadyForEvent(TimelineEvent{
		SessionID:  canonicalIdleChatTestSessionID("idle-timeout-trace"),
		MessageID:  "idle-timeout-trace:msg:0001",
		TurnIndex:  1,
		TraceID:    traceID,
		Generation: generation,
	}, TTSLifecycle{Ready: make(chan struct{})})

	if timeoutEvent.TraceID != traceID || timeoutEvent.Generation != generation {
		t.Fatalf("timeout identity = trace:%q generation:%d, want trace:%q generation:%d", timeoutEvent.TraceID, timeoutEvent.Generation, traceID, generation)
	}
}

func TestIdleChatTTSTimeoutReporterRejectsStaleOrMalformedOwnership(t *testing.T) {
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")
	generation := activateIdleChatTestSession(o, canonicalIdleChatTestSessionID("idle-timeout-owner"))
	o.mu.Lock()
	traceID := o.activeTraceID
	o.mu.Unlock()
	called := 0
	o.SetTTSTimeoutReporter(func(TTSTimeoutEvent) { called++ })

	for _, ev := range []TTSTimeoutEvent{
		{Kind: "timeout", SessionID: canonicalIdleChatTestSessionID("idle-timeout-owner"), MessageID: "message", TraceID: modulecore.TraceID("not-a-trace"), Generation: generation},
		{Kind: "timeout", SessionID: canonicalIdleChatTestSessionID("idle-timeout-owner"), MessageID: "message", TraceID: modulecore.NewTraceID(), Generation: generation},
		{Kind: "timeout", SessionID: canonicalIdleChatTestSessionID("idle-timeout-owner"), MessageID: "message", TraceID: traceID, Generation: generation + 1},
	} {
		o.reportTTSTimeoutEvent(ev)
	}
	if called != 0 {
		t.Fatalf("stale or malformed timeout reached reporter %d times", called)
	}
}

func TestIdleChatTTSSessionDrainTimeoutCarriesOwnerTrace(t *testing.T) {
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")
	generation := activateIdleChatTestSession(o, canonicalIdleChatTestSessionID("idle-drain-trace"))
	o.mu.Lock()
	traceID := o.activeTraceID
	o.mu.Unlock()
	old := idleChatTTSSessionDrainTimeout
	idleChatTTSSessionDrainTimeout = 10 * time.Millisecond
	defer func() { idleChatTTSSessionDrainTimeout = old }()
	var timeoutEvent TTSTimeoutEvent
	o.SetTTSTimeoutReporter(func(ev TTSTimeoutEvent) {
		timeoutEvent = ev
	})

	o.waitForTTSSessionDrain(canonicalIdleChatTestSessionID("idle-drain-trace"), generation, []TTSLifecycle{{Done: make(chan struct{})}})

	if timeoutEvent.Kind != "session_audio_timeout" || timeoutEvent.TraceID != traceID || timeoutEvent.Generation != generation {
		t.Fatalf("session timeout identity = %+v, want trace:%q generation:%d", timeoutEvent, traceID, generation)
	}
}
