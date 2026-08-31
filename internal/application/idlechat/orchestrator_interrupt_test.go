package idlechat

import (
	"context"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type blockingInterruptProvider struct {
	started chan struct{}
	done    chan error
	stream  chan bool
}

func activateIdleChatTestSession(o *IdleChatOrchestrator, sessionID string) uint64 {
	o.emitMu.Lock()
	defer o.emitMu.Unlock()
	o.mu.Lock()
	defer o.mu.Unlock()
	o.chatActive = true
	generation := o.beginIdleRunLocked()
	o.bindIdleSessionLocked(sessionID)
	return generation
}

func (p *blockingInterruptProvider) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.stream <- req.OnToken != nil
	close(p.started)
	<-ctx.Done()
	err := ctx.Err()
	p.done <- err
	return llm.GenerateResponse{}, err
}

func (p *blockingInterruptProvider) Name() string { return "blocking-interrupt" }

func TestIdleChatInterruptResetsStateAndCancelsRunContext(t *testing.T) {
	provider := &blockingInterruptProvider{
		started: make(chan struct{}),
		done:    make(chan error, 1),
		stream:  make(chan bool, 1),
	}
	o := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")

	o.mu.Lock()
	o.manualMode = true
	o.chatActive = true
	o.sessionMode = "idle"
	o.currentTopic = "topic"
	o.sessionContext = "context"
	o.beginIdleRunLocked()
	o.activeSessionID = "idle-test-topic-00"
	o.mu.Unlock()

	go func() {
		_, _ = o.generateIdleLLM(provider, llm.GenerateRequest{Messages: []llm.Message{{Role: "user", Content: "hello"}}})
	}()

	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("provider did not start")
	}
	if streaming := <-provider.stream; !streaming {
		t.Fatal("IdleChat request must stream so interrupt reaches the physical backend")
	}

	o.Interrupt("user_input")

	select {
	case err := <-provider.done:
		if err == nil {
			t.Fatal("expected canceled context error")
		}
	case <-time.After(time.Second):
		t.Fatal("interrupt did not cancel running LLM request")
	}

	if o.IsManualMode() {
		t.Fatal("manualMode should be false after interrupt")
	}
	if o.IsChatActive() {
		t.Fatal("chatActive should be false after interrupt")
	}
	if got := o.CurrentMode(); got != "" {
		t.Fatalf("CurrentMode() = %q, want empty", got)
	}
	if got := o.CurrentTopic(); got != "" {
		t.Fatalf("CurrentTopic() = %q, want empty", got)
	}
}

func TestIdleChatInterruptDiscardsStaleTimelineEvent(t *testing.T) {
	o := NewIdleChatOrchestrator(&capturingIdleProvider{response: "ok"}, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")
	emitted := 0
	o.SetEventEmitter(func(ev TimelineEvent) TTSLifecycle {
		emitted++
		done := make(chan struct{})
		close(done)
		return TTSLifecycle{Ready: done, Done: done}
	})

	o.mu.Lock()
	o.chatActive = true
	o.beginIdleRunLocked()
	o.activeSessionID = "idle-stale-topic-00"
	o.mu.Unlock()

	o.Interrupt("user_input")
	done := o.emitTimelineEvent(TimelineEvent{
		Type:      "idlechat.message",
		From:      "mio",
		To:        "shiro",
		Content:   "late response",
		SessionID: "idle-stale-topic-00",
	})

	if done.Ready != nil || done.Done != nil {
		t.Fatal("stale idlechat event should not return a TTS lifecycle")
	}
	if emitted != 0 {
		t.Fatalf("stale idlechat event emitted %d times, want 0", emitted)
	}
}

func TestIdleChatSessionBindsTimelineAndPrefetchToOneCanonicalTrace(t *testing.T) {
	o := NewIdleChatOrchestrator(&capturingIdleProvider{response: "ok"}, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")
	var timeline []TimelineEvent
	var prefetch []TTSPrefetchEvent
	o.SetEventEmitter(func(ev TimelineEvent) TTSLifecycle {
		timeline = append(timeline, ev)
		return TTSLifecycle{}
	})
	o.SetTTSPrefetchEmitter(func(ev TTSPrefetchEvent) {
		prefetch = append(prefetch, ev)
	})

	generation := activateIdleChatTestSession(o, "idle-trace-topic-00")

	o.emitTimelineEvent(TimelineEvent{Type: "idlechat.topic", SessionID: "idle-trace-topic-00", Generation: generation})
	o.emitTimelineEvent(TimelineEvent{Type: "idlechat.message", SessionID: "idle-trace-topic-00", MessageID: "msg-1", TurnIndex: 1, Generation: generation})
	o.emitStoryTTSPrefetch("idle-trace-topic-00", generation, StoryEpisodeTurn{Speaker: "mio", MessageID: "msg-1", TurnIndex: 1, SpeechText: "確認です。"})

	if len(timeline) != 2 || len(prefetch) != 1 {
		t.Fatalf("timeline=%d prefetch=%d, want 2 and 1", len(timeline), len(prefetch))
	}
	want := timeline[0].TraceID
	if want.Validate() != nil {
		t.Fatalf("timeline trace_id = %q, want canonical TraceID", want)
	}
	if want == modulecore.TraceID("idle-trace-topic-00") {
		t.Fatal("TraceID must not reuse SessionID")
	}
	if timeline[1].TraceID != want || prefetch[0].TraceID != want {
		t.Fatalf("session traces = topic:%q message:%q prefetch:%q, want one trace", timeline[0].TraceID, timeline[1].TraceID, prefetch[0].TraceID)
	}
	o.cancelIdleRunIfGeneration(generation)
	o.emitTimelineEvent(TimelineEvent{Type: "idlechat.message", SessionID: "idle-trace-topic-00", MessageID: "late-msg", TurnIndex: 2, Generation: generation})
	if len(timeline) != 2 {
		t.Fatalf("late timeline event was emitted after trace owner ended: timeline=%d", len(timeline))
	}

	o.Interrupt("test_complete")
	secondGeneration := activateIdleChatTestSession(o, "idle-trace-topic-01")
	o.emitTimelineEvent(TimelineEvent{Type: "idlechat.topic", SessionID: "idle-trace-topic-01", Generation: secondGeneration})
	if len(timeline) != 3 {
		t.Fatalf("timeline=%d, want 3 after second session", len(timeline))
	}
	if timeline[2].TraceID.Validate() != nil || timeline[2].TraceID == want {
		t.Fatalf("second session trace_id = %q, want a new canonical trace distinct from %q", timeline[2].TraceID, want)
	}
}

func TestIdleChatPreparedReplayAfterInterruptClearsStaleSessionMarker(t *testing.T) {
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")
	var timeline []TimelineEvent
	o.SetEventEmitter(func(ev TimelineEvent) TTSLifecycle {
		timeline = append(timeline, ev)
		return TTSLifecycle{}
	})
	firstGeneration := activateIdleChatTestSession(o, "idle-replay")
	o.emitTimelineEvent(TimelineEvent{
		Type:       "idlechat.message",
		From:       "mio",
		To:         "shiro",
		Content:    "最初の発話です。",
		SessionID:  "idle-replay",
		Generation: firstGeneration,
	})
	if len(timeline) != 1 {
		t.Fatalf("first replay event count = %d, want 1", len(timeline))
	}
	firstTrace := timeline[0].TraceID
	o.Interrupt("user_input")

	secondGeneration := activateIdleChatTestSession(o, "idle-replay")
	if o.isInterruptedSession("idle-replay") {
		t.Fatal("new bind for the same session must clear the stale interrupted marker")
	}
	if secondGeneration == firstGeneration {
		t.Fatalf("replayed session generation = %d, want a new generation after interrupt", secondGeneration)
	}
	second := o.emitTimelineEvent(TimelineEvent{
		Type:       "idlechat.message",
		From:       "mio",
		To:         "shiro",
		Content:    "再開した発話です。",
		SessionID:  "idle-replay",
		Generation: secondGeneration,
	})
	if second.Ready != nil || second.Done != nil {
		t.Fatal("test emitter should not expose a TTS lifecycle")
	}
	if len(timeline) != 2 {
		t.Fatalf("replayed event count = %d, want 2", len(timeline))
	}
	if timeline[1].TraceID.Validate() != nil || timeline[1].TraceID == firstTrace {
		t.Fatalf("replayed TraceID = %q, want a new valid trace distinct from %q", timeline[1].TraceID, firstTrace)
	}
	o.emitTimelineEvent(TimelineEvent{
		Type:       "idlechat.message",
		Content:    "遅れて届いた最初の発話です。",
		SessionID:  "idle-replay",
		TraceID:    firstTrace,
		Generation: firstGeneration,
	})
	if len(timeline) != 2 {
		t.Fatalf("stale replay event was relabeled/emitted: event count=%d", len(timeline))
	}
}

func TestIdleChatRejectsMalformedExplicitTraceBeforeEmission(t *testing.T) {
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")
	emitted := 0
	o.SetEventEmitter(func(TimelineEvent) TTSLifecycle {
		emitted++
		return TTSLifecycle{}
	})
	generation := activateIdleChatTestSession(o, "idle-invalid-trace")
	o.emitTimelineEvent(TimelineEvent{
		Type:       "idlechat.message",
		Content:    "不正なTraceは拒否されるべきです。",
		SessionID:  "idle-invalid-trace",
		TraceID:    modulecore.TraceID("not-a-trace"),
		Generation: generation,
	})
	if emitted != 0 {
		t.Fatalf("malformed explicit trace was emitted %d times", emitted)
	}
}

func TestIdleChatPopulatesOnlyEmptyTraceForValidatedGeneration(t *testing.T) {
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")
	var timeline []TimelineEvent
	o.SetEventEmitter(func(ev TimelineEvent) TTSLifecycle {
		timeline = append(timeline, ev)
		return TTSLifecycle{}
	})
	generation := activateIdleChatTestSession(o, "idle-explicit-trace")
	o.emitTimelineEvent(TimelineEvent{
		Type:       "idlechat.message",
		SessionID:  "idle-explicit-trace",
		Generation: generation,
	})
	if len(timeline) != 1 || timeline[0].TraceID.Validate() != nil {
		t.Fatalf("empty explicit trace was not populated for owner: %+v", timeline)
	}
	wrongTrace := modulecore.NewTraceID()
	o.emitTimelineEvent(TimelineEvent{
		Type:       "idlechat.message",
		SessionID:  "idle-explicit-trace",
		TraceID:    wrongTrace,
		Generation: generation,
	})
	if len(timeline) != 1 {
		t.Fatalf("mismatched explicit trace was emitted: count=%d", len(timeline))
	}
	o.emitTimelineEvent(TimelineEvent{
		Type:       "idlechat.message",
		SessionID:  "idle-explicit-trace",
		TraceID:    timeline[0].TraceID,
		Generation: generation + 1,
	})
	if len(timeline) != 1 {
		t.Fatalf("stale generation with matching trace was emitted: count=%d", len(timeline))
	}
}

func TestIdleChatInterruptNotifiesRuntimeAfterOwnerTransition(t *testing.T) {
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")
	generation := activateIdleChatTestSession(o, "idle-runtime-cancel")
	notified := make(chan bool, 1)
	o.SetInterruptHandler(func() {
		notified <- !o.ownsIdleSession("idle-runtime-cancel", generation)
	})
	o.Interrupt("user_input")
	select {
	case ownerEnded := <-notified:
		if !ownerEnded {
			t.Fatal("runtime cancellation hook ran before the old owner ended")
		}
	case <-time.After(time.Second):
		t.Fatal("runtime cancellation hook was not called")
	}
}

func TestIdleChatGenerationErrorDoesNotRecordAfterInterrupt(t *testing.T) {
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(nil, memory, []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")
	generation := activateIdleChatTestSession(o, "idle-generation-error")
	o.Interrupt("user_input")
	o.recordGenerationErrorToTimeline("shiro", "mio", "idle-generation-error", "cancelled", 1, generation)

	for _, entry := range memory.GetUnifiedView(0) {
		if entry.Message.SessionID == "idle-generation-error" {
			t.Fatalf("generation error was recorded after owner interruption: %+v", entry.Message)
		}
	}
}

func TestIdleChatStopManualModeDisablesAutomaticRestart(t *testing.T) {
	o := NewIdleChatOrchestrator(&capturingIdleProvider{response: "ok"}, session.NewCentralMemory(), []string{"mio", "shiro"}, 1, 10, 0.7, nil, "")
	o.mu.Lock()
	o.manualMode = true
	o.chatActive = true
	o.lastActivity = time.Now().Add(-time.Hour)
	o.mu.Unlock()

	o.StopManualMode()
	if !o.IsDisabled() {
		t.Fatal("StopManualMode should disable automatic IdleChat restart")
	}

	o.checkAndStartChat()
	if o.IsChatActive() {
		t.Fatal("IdleChat auto monitor restarted after StopManualMode")
	}
	if got := o.CurrentMode(); got != "" {
		t.Fatalf("CurrentMode() after disabled auto check = %q, want empty", got)
	}
	if snapshot := o.WatchdogSnapshot(time.Now()); !snapshot.Disabled {
		t.Fatalf("watchdog disabled = false, want true: %+v", snapshot)
	}
}

func TestIdleChatExplicitStartClearsDisabledStopLatch(t *testing.T) {
	o := NewIdleChatOrchestrator(&capturingIdleProvider{response: "ok"}, session.NewCentralMemory(), []string{"mio", "shiro"}, 1, 10, 0.7, nil, "")
	o.StopManualMode()
	if !o.IsDisabled() {
		t.Fatal("expected disabled stop latch before explicit start")
	}

	if err := o.StartManualMode(); err != nil {
		t.Fatalf("StartManualMode failed: %v", err)
	}
	if o.IsDisabled() {
		t.Fatal("explicit StartManualMode should clear disabled stop latch")
	}
	if !o.IsManualMode() {
		t.Fatal("manual mode should be active after explicit start")
	}
}

func TestIdleChatExternalLLMBusyPreventsAutomaticStart(t *testing.T) {
	o := NewIdleChatOrchestrator(&capturingIdleProvider{response: "ok"}, session.NewCentralMemory(), []string{"mio", "shiro"}, 1, 10, 0.7, nil, "")
	o.SetExternalLLMBusyFunc(func() bool { return true })
	o.mu.Lock()
	o.lastActivity = time.Now().Add(-time.Hour)
	o.mu.Unlock()

	o.checkAndStartChat()
	if o.IsChatActive() {
		t.Fatal("IdleChat auto monitor started while external LLM is busy")
	}
	if snapshot := o.WatchdogSnapshot(time.Now()); !snapshot.ExternalLLMBusy {
		t.Fatalf("watchdog external_llm_busy = false, want true: %+v", snapshot)
	}
}
