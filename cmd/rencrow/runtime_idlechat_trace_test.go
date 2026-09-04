package main

import (
	"encoding/json"
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
	threadID := modulecore.NewThreadID()
	threadSeq := modulecore.ThreadSeq(7)
	threadKind := modulecore.ThreadKindIdleChat
	event := idleChatViewerEvent(idlechat.TimelineEvent{
		Type:       "idlechat.message",
		From:       "mio",
		To:         "shiro",
		Content:    "確認です。",
		SessionID:  "idle-trace-topic-00",
		MessageID:  "msg-1",
		TurnIndex:  1,
		TraceID:    traceID,
		ThreadID:   threadID,
		ThreadSeq:  threadSeq,
		ThreadKind: threadKind,
	})
	if event.TraceID != string(traceID) {
		t.Fatalf("viewer trace_id = %q, want %q", event.TraceID, traceID)
	}
	if event.SessionID != "idle-trace-topic-00" || event.MessageID != "msg-1" {
		t.Fatalf("viewer owner identity = (session=%q, message=%q), want (session=%q, message=%q)",
			event.SessionID, event.MessageID, "idle-trace-topic-00", "msg-1")
	}
	if event.ThreadID != threadID || event.ThreadSeq != threadSeq || event.ThreadKind != threadKind {
		t.Fatalf("viewer thread tuple = (%q, %d, %q), want (%q, %d, %q)",
			event.ThreadID, event.ThreadSeq, event.ThreadKind,
			threadID, threadSeq, threadKind)
	}

	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var roundtrip orchestrator.OrchestratorEvent
	if err := json.Unmarshal(encoded, &roundtrip); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if roundtrip.TraceID != string(traceID) ||
		roundtrip.SessionID != event.SessionID ||
		roundtrip.MessageID != event.MessageID ||
		roundtrip.ThreadID != threadID ||
		roundtrip.ThreadSeq != threadSeq ||
		roundtrip.ThreadKind != threadKind {
		t.Fatalf("viewer JSON identity = (trace=%q, session=%q, message=%q, thread=%q, %d, %q), want (trace=%q, session=%q, message=%q, thread=%q, %d, %q)",
			roundtrip.TraceID, roundtrip.SessionID, roundtrip.MessageID,
			roundtrip.ThreadID, roundtrip.ThreadSeq, roundtrip.ThreadKind,
			string(traceID), event.SessionID, event.MessageID, threadID, threadSeq, threadKind)
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
