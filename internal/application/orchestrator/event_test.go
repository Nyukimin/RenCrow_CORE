package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestOrchestratorEventThreadTupleJSONRoundTrip(t *testing.T) {
	threadID := modulecore.NewThreadID()
	original := OrchestratorEvent{
		Type:       "idlechat.message",
		From:       "mio",
		Content:    "canonical tuple",
		MessageID:  "idle-message",
		TraceID:    modulecore.NewTraceID(),
		SessionID:  "idle-session",
		ThreadID:   threadID,
		ThreadSeq:  modulecore.ThreadSeq(4),
		ThreadKind: modulecore.ThreadKindIdleChat,
		Timestamp:  "2026-09-03T00:00:00Z",
	}

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("json.Unmarshal() wire error = %v", err)
	}
	for _, key := range []string{"thread_id", "thread_seq", "thread_kind"} {
		if _, ok := wire[key]; !ok {
			t.Fatalf("JSON missing %q: %s", key, encoded)
		}
	}

	var roundtrip OrchestratorEvent
	if err := json.Unmarshal(encoded, &roundtrip); err != nil {
		t.Fatalf("json.Unmarshal() event error = %v", err)
	}
	if roundtrip.ThreadID != original.ThreadID ||
		roundtrip.ThreadSeq != original.ThreadSeq ||
		roundtrip.ThreadKind != original.ThreadKind {
		t.Fatalf("thread tuple roundtrip = (%q, %d, %q), want (%q, %d, %q)",
			roundtrip.ThreadID, roundtrip.ThreadSeq, roundtrip.ThreadKind,
			original.ThreadID, original.ThreadSeq, original.ThreadKind)
	}
}

func TestOrchestratorEventClarificationReplyCorrelationJSONRoundTrip(t *testing.T) {
	replyTurnID := modulecore.NewTurnID()
	original := OrchestratorEvent{
		Type:                         "agent.response",
		From:                         "mio",
		To:                           "user",
		Kind:                         "readback",
		Visibility:                   "public",
		TurnID:                       modulecore.NewTurnID(),
		ClientSubmissionID:           "original-submission",
		ResponseToTurnID:             replyTurnID,
		ResponseToClientSubmissionID: "clarification-reply-submission",
		Content:                      "accepted readback",
		Timestamp:                    "2026-10-22T00:00:00Z",
	}

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("json.Unmarshal() wire error = %v", err)
	}
	var responseToTurnID string
	if err := json.Unmarshal(wire["response_to_turn_id"], &responseToTurnID); err != nil {
		t.Fatalf("response_to_turn_id JSON = %s: %v", wire["response_to_turn_id"], err)
	}
	if responseToTurnID != string(replyTurnID) {
		t.Fatalf("response_to_turn_id = %q, want %q", responseToTurnID, replyTurnID)
	}
	var responseToSubmissionID string
	if err := json.Unmarshal(wire["response_to_client_submission_id"], &responseToSubmissionID); err != nil {
		t.Fatalf("response_to_client_submission_id JSON = %s: %v", wire["response_to_client_submission_id"], err)
	}
	if responseToSubmissionID != original.ResponseToClientSubmissionID {
		t.Fatalf("response_to_client_submission_id = %q, want %q", responseToSubmissionID, original.ResponseToClientSubmissionID)
	}

	var roundtrip OrchestratorEvent
	if err := json.Unmarshal(encoded, &roundtrip); err != nil {
		t.Fatalf("json.Unmarshal() event error = %v", err)
	}
	if roundtrip.ResponseToTurnID != original.ResponseToTurnID ||
		roundtrip.ResponseToClientSubmissionID != original.ResponseToClientSubmissionID {
		t.Fatalf("clarification reply correlation roundtrip = (%q, %q), want (%q, %q)",
			roundtrip.ResponseToTurnID, roundtrip.ResponseToClientSubmissionID,
			original.ResponseToTurnID, original.ResponseToClientSubmissionID)
	}
}

func TestEventPublicationFailureTrackerRetainsFirstFailureAndClears(t *testing.T) {
	tracker := newEventPublicationFailureTracker()
	traceID := modulecore.NewTraceID()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	tracker.Begin(traceID, cancel)

	firstErr := errors.New("first canonical append failure")
	secondErr := errors.New("later canonical append failure")
	tracker.Record(traceID, firstErr)
	tracker.Record(traceID, secondErr)

	if cause := context.Cause(ctx); !errors.Is(cause, firstErr) {
		t.Fatalf("context cause = %v, want first error %v", cause, firstErr)
	}
	got := tracker.End(traceID)
	if !errors.Is(got, firstErr) {
		t.Fatalf("End() error = %v, want first error %v", got, firstErr)
	}
	if errors.Is(got, secondErr) {
		t.Fatalf("End() replaced first error with later error: %v", got)
	}
	if errors.Is(tracker.End(traceID), firstErr) || errors.Is(tracker.End(traceID), secondErr) {
		t.Fatal("End() retained a failure after clearing the trace")
	}
}
