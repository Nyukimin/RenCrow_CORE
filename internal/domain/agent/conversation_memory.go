package agent

import (
	"context"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

type speakerAwareConversationEngine interface {
	EndTurnAs(ctx context.Context, sessionID string, userMessage string, response string, speaker conversation.Speaker) error
}

type recallTraceConversationEngine interface {
	RecordRecallTrace(ctx context.Context, sessionID string, responseID string, role string, pack conversation.RecallPack) error
}

func recordRecallTrace(ctx context.Context, engine conversation.ConversationEngine, sessionID string, responseID string, role string, pack conversation.RecallPack) error {
	if recorder, ok := engine.(recallTraceConversationEngine); ok {
		return recorder.RecordRecallTrace(ctx, sessionID, responseID, role, pack)
	}
	return nil
}

func endConversationTurnAs(ctx context.Context, engine conversation.ConversationEngine, sessionID, userMessage, response string, speaker conversation.Speaker) error {
	if aware, ok := engine.(speakerAwareConversationEngine); ok {
		return aware.EndTurnAs(ctx, sessionID, userMessage, response, speaker)
	}
	return engine.EndTurn(ctx, sessionID, userMessage, response)
}
