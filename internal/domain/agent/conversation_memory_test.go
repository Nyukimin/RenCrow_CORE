package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
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

func TestSharedConversationContinuityIsTypedRecallL0(t *testing.T) {
	pack := &conversation.RecallPack{ShortContext: []conversation.Message{{Speaker: conversation.SpeakerUser, Msg: "previous"}}}
	messages := appendSharedConversationContinuityPrompt(nil, pack)
	if len(messages) != 1 || messages[0].Type != llm.PromptContextRecall || messages[0].Metadata["recall_section"] != "l0" {
		t.Fatalf("continuity prompt must be typed RecallPack L0: %#v", messages)
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

func TestEnforceExactSharedRecallAnswer(t *testing.T) {
	pack := &conversation.RecallPack{ShortContext: []conversation.Message{
		{Speaker: conversation.SpeakerUser, Msg: "この会話固有の合言葉は RC_CTX_20260803_1328_L1S4 です"},
		{Speaker: conversation.SpeakerMidori, Msg: "覚えました"},
	}}

	got := enforceExactSharedRecallAnswer("合言葉を英数字だけでそのまま教えて", "履歴が見つかりません", pack)
	if got != "RC_CTX_20260803_1328_L1S4" {
		t.Fatalf("exact recall answer=%q", got)
	}
	if got := enforceExactSharedRecallAnswer("前の話を要約して", "通常応答", pack); got != "通常応答" {
		t.Fatalf("non-exact request must keep model response, got %q", got)
	}

	ambiguous := &conversation.RecallPack{ShortContext: []conversation.Message{{
		Speaker: conversation.SpeakerUser,
		Msg:     "候補は RC_CTX_20260803_1328_L1S4 と RC_CTX_20260803_1328_OTHER9",
	}}}
	if got := enforceExactSharedRecallAnswer("合言葉をそのまま教えて", "確認が必要です", ambiguous); got != "確認が必要です" {
		t.Fatalf("ambiguous literals must keep model response, got %q", got)
	}
}
