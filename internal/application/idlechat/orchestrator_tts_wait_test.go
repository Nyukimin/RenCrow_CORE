package idlechat

import (
	"testing"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/session"
)

func TestEmitTopicToTimelineDoesNotWaitForTTSCompletion(t *testing.T) {
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")
	ttsDone := make(chan struct{})
	eventSeen := make(chan struct{}, 1)
	o.SetEventEmitter(func(ev TimelineEvent) <-chan struct{} {
		if ev.Type != "idlechat.topic" {
			t.Fatalf("unexpected event type: %s", ev.Type)
		}
		if ev.MessageID != "idle-wait:topic" || ev.TurnIndex != 0 {
			t.Fatalf("unexpected topic identity: %+v", ev)
		}
		eventSeen <- struct{}{}
		return ttsDone
	})

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		o.emitTopicToTimeline("idle-wait", "記憶と風景の関係", StrategyExternalStimulus)
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

func TestWaitForTTSDoneTimesOut(t *testing.T) {
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")
	old := idleChatTTSWaitTimeout
	idleChatTTSWaitTimeout = 10 * time.Millisecond
	defer func() { idleChatTTSWaitTimeout = old }()
	var timeoutEvent TTSTimeoutEvent
	o.SetTTSTimeoutReporter(func(ev TTSTimeoutEvent) {
		timeoutEvent = ev
	})

	blocked := make(chan struct{})
	start := time.Now()
	o.waitForTTSDoneForEvent(TimelineEvent{
		SessionID: "idle-timeout",
		MessageID: "idle-timeout:msg:0001",
		TurnIndex: 1,
	}, blocked)

	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("waitForTTSDone did not time out promptly: %s", elapsed)
	}
	if timeoutEvent.Kind != "timeout" || timeoutEvent.SessionID != "idle-timeout" || timeoutEvent.MessageID != "idle-timeout:msg:0001" || timeoutEvent.TurnIndex != 1 {
		t.Fatalf("unexpected timeout event: %#v", timeoutEvent)
	}
}

func TestIdleChatTTSWaitTimeoutDefaultIsSixtySeconds(t *testing.T) {
	if idleChatTTSWaitTimeout != 60*time.Second {
		t.Fatalf("unexpected idleChatTTSWaitTimeout: %s", idleChatTTSWaitTimeout)
	}
}

func TestIdleChatTTSSessionDrainTimeoutDefaultIsSixtySeconds(t *testing.T) {
	if idleChatTTSSessionDrainTimeout != 60*time.Second {
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
	o.waitForTTSSessionDrain("idle-drain", []<-chan struct{}{done})

	if elapsed := time.Since(start); elapsed < 25*time.Millisecond {
		t.Fatalf("session drain returned before outstanding playback completed: %s", elapsed)
	}
}

func TestWaitForTTSSessionDrainTimesOutInsteadOfStoppingSystem(t *testing.T) {
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")
	old := idleChatTTSSessionDrainTimeout
	idleChatTTSSessionDrainTimeout = 10 * time.Millisecond
	defer func() { idleChatTTSSessionDrainTimeout = old }()
	var timeoutEvent TTSTimeoutEvent
	o.SetTTSTimeoutReporter(func(ev TTSTimeoutEvent) {
		timeoutEvent = ev
	})

	blocked := make(chan struct{})
	start := time.Now()
	o.waitForTTSSessionDrain("idle-drain-timeout", []<-chan struct{}{blocked})

	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("session drain did not time out promptly: %s", elapsed)
	}
	if timeoutEvent.Kind != "session_audio_timeout" || timeoutEvent.SessionID != "idle-drain-timeout" || timeoutEvent.RemainingIndex != 1 || timeoutEvent.RemainingCount != 1 {
		t.Fatalf("unexpected drain timeout event: %#v", timeoutEvent)
	}
}
