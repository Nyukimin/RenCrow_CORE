package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

type traceAwareConversationEngine struct {
	*mockConversationEngine
	traceErr error
	endErr   error
	trace    struct {
		sessionID  string
		responseID string
		role       string
	}
	endAs struct {
		sessionID string
		speaker   conversation.Speaker
	}
}

func (e *traceAwareConversationEngine) RecordRecallTrace(ctx context.Context, sessionID string, responseID string, role string, pack conversation.RecallPack) error {
	e.trace.sessionID = sessionID
	e.trace.responseID = responseID
	e.trace.role = role
	return e.traceErr
}

func (e *traceAwareConversationEngine) EndTurnAs(ctx context.Context, sessionID string, userMessage string, response string, speaker conversation.Speaker) error {
	e.endAs.sessionID = sessionID
	e.endAs.speaker = speaker
	return e.endErr
}

func TestConversationMemoryOptionalInterfaces(t *testing.T) {
	engine := &traceAwareConversationEngine{mockConversationEngine: &mockConversationEngine{}}
	pack := conversation.RecallPack{}

	if err := recordRecallTrace(context.Background(), engine, "session-1", "response-1", "wild", pack); err != nil {
		t.Fatalf("recordRecallTrace failed: %v", err)
	}
	if engine.trace.sessionID != "session-1" || engine.trace.responseID != "response-1" || engine.trace.role != "wild" {
		t.Fatalf("unexpected trace call: %#v", engine.trace)
	}

	if err := endConversationTurnAs(context.Background(), engine, "session-1", "hello", "hi", conversation.SpeakerShiro); err != nil {
		t.Fatalf("endConversationTurnAs failed: %v", err)
	}
	if engine.endAs.sessionID != "session-1" || engine.endAs.speaker != conversation.SpeakerShiro {
		t.Fatalf("unexpected EndTurnAs call: %#v", engine.endAs)
	}
}

func TestConversationMemoryOptionalInterfaceErrors(t *testing.T) {
	traceErr := errors.New("trace failed")
	endErr := errors.New("end failed")
	engine := &traceAwareConversationEngine{
		mockConversationEngine: &mockConversationEngine{},
		traceErr:               traceErr,
		endErr:                 endErr,
	}

	if err := recordRecallTrace(context.Background(), engine, "session-1", "response-1", "wild", conversation.RecallPack{}); !errors.Is(err, traceErr) {
		t.Fatalf("expected trace error, got %v", err)
	}
	if err := endConversationTurnAs(context.Background(), engine, "session-1", "hello", "hi", conversation.SpeakerShiro); !errors.Is(err, endErr) {
		t.Fatalf("expected end error, got %v", err)
	}
}
