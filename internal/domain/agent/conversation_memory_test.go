package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

func TestSanitizeRecallPackForGenerationRejectsDegenerateAgentExamplesOnly(t *testing.T) {
	pack := &conversation.RecallPack{ShortContext: []conversation.Message{
		{Speaker: conversation.SpeakerUser, Msg: "いって、いって、とユーザーが引用した"},
		{Speaker: conversation.SpeakerMio, Msg: "り1/20、いって、いって、疎通確認了解！"},
		{Speaker: conversation.SpeakerShiro, Msg: "通常の調査結果です"},
	}}

	got := pack.WithoutUnsafeAgentExamples()
	if len(got.ShortContext) != 2 {
		t.Fatalf("short context count=%d, want user quote and healthy Agent message", len(got.ShortContext))
	}
	if got.ShortContext[0].Speaker != conversation.SpeakerUser || got.ShortContext[1].Speaker != conversation.SpeakerShiro {
		t.Fatalf("unexpected retained context: %#v", got.ShortContext)
	}
	if len(got.RejectedTraceItems) != 1 || got.RejectedTraceItems[0].Decision != "excluded" || !strings.Contains(got.RejectedTraceItems[0].Reason, "degenerate") {
		t.Fatalf("missing bounded rejection trace: %#v", got.RejectedTraceItems)
	}
	if len(pack.ShortContext) != 3 || len(pack.RejectedTraceItems) != 0 {
		t.Fatalf("source RecallPack was mutated: %#v", pack)
	}
}

func TestUnsafePromptExampleDetectsRepeatedMotifAndRuneRun(t *testing.T) {
	for _, value := range []string{
		"り1/20、いって、いって、疎通確認了解！",
		"応答です。0000000000000000",
	} {
		if !conversation.IsUnsafeGeneratedTextForPrompt(value) {
			t.Fatalf("unsafe prompt example was accepted: %q", value)
		}
	}
	for _, value := range []string{
		"確認しました。次の境界を調べます。",
		"一つずつ、確実に確認します。",
	} {
		if conversation.IsUnsafeGeneratedTextForPrompt(value) {
			t.Fatalf("healthy prompt example was rejected: %q", value)
		}
	}
}

func TestSharedConversationContinuityIsTypedRecallL0(t *testing.T) {
	pack := &conversation.RecallPack{ShortContext: []conversation.Message{{Speaker: conversation.SpeakerUser, Msg: "previous"}}}
	messages := appendSharedConversationContinuityPrompt(nil, pack)
	if len(messages) != 1 || messages[0].Type != llm.PromptContextRecall || messages[0].Metadata["recall_section"] != "l0" {
		t.Fatalf("continuity prompt must be typed RecallPack L0: %#v", messages)
	}
}

func TestCommitConversationTurnUsesExactFilteredPack(t *testing.T) {
	var got conversation.ConversationTurnRequest
	engine := &mockConversationEngine{
		commitTurnFunc: func(_ context.Context, request conversation.ConversationTurnRequest) (conversation.ConversationTurnResult, error) {
			got = request
			return conversation.ConversationTurnResult{TurnID: request.TurnID, Status: conversation.ConversationTurnCompleted}, nil
		},
	}
	pack := &conversation.RecallPack{ShortContext: []conversation.Message{{Speaker: conversation.SpeakerUser, Msg: "kept"}}}
	if err := commitConversationTurn(context.Background(), engine, "job-1", "chat-1", "hello", "hi", conversation.SpeakerShiro, pack); err != nil {
		t.Fatalf("commitConversationTurn failed: %v", err)
	}
	if got.TurnID != "job-1" || got.SessionID != "chat-1" || got.AgentSpeaker != conversation.SpeakerShiro || len(got.RecallTraceItems) != 1 || got.RecallTraceItems[0].Summary != "kept" {
		t.Fatalf("unexpected typed request=%#v", got)
	}
}

func TestCommitConversationTurnPartialOutcomeDoesNotFailTurn(t *testing.T) {
	engine := &mockConversationEngine{
		commitTurnFunc: func(_ context.Context, request conversation.ConversationTurnRequest) (conversation.ConversationTurnResult, error) {
			return conversation.ConversationTurnResult{
				TurnID:         request.TurnID,
				Status:         conversation.ConversationTurnPartial,
				PendingTargets: []string{string(conversation.ConversationTurnTargetRedisProjection)},
				ErrorCode:      conversation.ConversationTurnErrorUnavailable,
			}, conversation.ErrConversationTurnUnavailable
		},
	}
	if err := commitConversationTurn(context.Background(), engine, "job-1", "chat-1", "hello", "hi", conversation.SpeakerMio, nil); err != nil {
		t.Fatalf("partial outcome must not fail the committed turn: %v", err)
	}
}

func TestCommitConversationTurnFailedReturnsTypedError(t *testing.T) {
	engine := &mockConversationEngine{
		commitTurnFunc: func(_ context.Context, request conversation.ConversationTurnRequest) (conversation.ConversationTurnResult, error) {
			return conversation.ConversationTurnResult{
				TurnID:    request.TurnID,
				Status:    conversation.ConversationTurnFailed,
				ErrorCode: conversation.ConversationTurnErrorUnavailable,
			}, conversation.ErrConversationTurnUnavailable
		},
	}
	if err := commitConversationTurn(context.Background(), engine, "job-1", "chat-1", "hello", "hi", conversation.SpeakerMio, nil); !errors.Is(err, conversation.ErrConversationTurnUnavailable) {
		t.Fatalf("error=%v, want typed unavailable", err)
	}
}

func TestCommitConversationTurnUnexpectedStatusFailsClosed(t *testing.T) {
	engine := &mockConversationEngine{
		commitTurnFunc: func(_ context.Context, request conversation.ConversationTurnRequest) (conversation.ConversationTurnResult, error) {
			return conversation.ConversationTurnResult{TurnID: request.TurnID, Status: conversation.ConversationTurnFailed}, nil
		},
	}
	if err := commitConversationTurn(context.Background(), engine, "job-1", "chat-1", "hello", "hi", conversation.SpeakerMio, nil); !errors.Is(err, conversation.ErrConversationTurnInternal) {
		t.Fatalf("error=%v, want typed internal fail-closed", err)
	}
}

func TestCommitConversationTurnRejectsLegacyOnlyEngine(t *testing.T) {
	type legacyOnlyEngine struct {
		conversation.ConversationEngine
	}
	legacy := &legacyOnlyEngine{}
	if err := commitConversationTurn(context.Background(), legacy, "job-1", "chat-1", "hello", "hi", conversation.SpeakerMio, nil); !errors.Is(err, conversation.ErrConversationTurnUnavailable) {
		t.Fatalf("error=%v, want typed route unavailable", err)
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
