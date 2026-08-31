package main

import (
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestIdleChatViewerEventKeepsOwnerSessionTrace(t *testing.T) {
	traceID := modulecore.NewTraceID()
	event := idleChatViewerEvent(idlechat.TimelineEvent{
		Type:      "idlechat.message",
		From:      "mio",
		To:        "shiro",
		Content:   "確認です。",
		SessionID: "idle-trace-topic-00",
		MessageID: "msg-1",
		TurnIndex: 1,
		TraceID:   traceID,
	})
	if event.TraceID != string(traceID) {
		t.Fatalf("viewer trace_id = %q, want %q", event.TraceID, traceID)
	}
}

func TestIdleChatRelayFailureCancelsExactPrefetchOwner(t *testing.T) {
	clearAllIdleChatTTSPending()
	resetTTSPublicSessionStateForTest()
	resetActiveViewerControlForTest()
	activeViewerControl.Claim("audio", "relay-failure-viewer")
	setIdleChatViewerClientCount(func() int { return 1 })
	manager := newIdleChatTTSPrefetchManager(&idleChatPrefetchMockBridge{})
	t.Cleanup(func() {
		manager.CancelAll()
		setIdleChatViewerClientCount(nil)
		resetActiveViewerControlForTest()
		clearAllIdleChatTTSPending()
		resetTTSPublicSessionStateForTest()
	})

	traceID := modulecore.NewTraceID()
	prefetch := idlechat.TTSPrefetchEvent{
		SessionID: "idle-relay-failure",
		MessageID: "idle-relay-failure:msg:0001",
		From:      "mio",
		To:        "shiro",
		TurnIndex: 1,
		TraceID:   traceID,
		Token:     "発行失敗時に残してはいけない音声です。",
	}
	manager.Push(prefetch)
	if !manager.HasActive(prefetch.SessionID, prefetch.MessageID, traceID) {
		t.Fatal("prefetch stream was not registered before relay failure")
	}
	emit := newIdleChatRuntimeEventEmitter(func(orchestrator.OrchestratorEvent) error {
		return errors.New("relay failed")
	}, manager, nil)
	emit(idlechat.TimelineEvent{
		Type:      "idlechat.message",
		SessionID: prefetch.SessionID,
		MessageID: prefetch.MessageID,
		TraceID:   traceID,
	})

	deadline := time.Now().Add(time.Second)
	key := streamKey(prefetch.SessionID, prefetch.MessageID, string(traceID))
	for {
		manager.mu.Lock()
		_, active := manager.streams[key]
		manager.mu.Unlock()
		if !active {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatal("relay failure left the exact prefetch owner active")
		}
		// HasActive reports closed as soon as cancellation starts. Waiting for
		// the manager entry to disappear proves the stream goroutine reached
		// its removal defer before shared test globals are reset.
		runtime.Gosched()
	}
}
