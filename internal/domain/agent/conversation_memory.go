package agent

import (
	"context"
	"regexp"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

var exactSharedRecallLiteralPattern = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_-]{7,}`)

const sharedAgentConversationContinuityPrompt = `Conversation continuity contract:
RecallPack contains the authoritative shared conversation history for Mio, Shiro, Kuro, and Midori.
Treat messages from every named Agent as one continuous conversation with the user.
Do not claim that history is missing when it is present. When asked to recall exact text or a token, copy it exactly from the shared history.`

func appendSharedConversationContinuityPrompt(messages []llm.Message, pack *conversation.RecallPack) []llm.Message {
	if pack == nil || !pack.HasContext() {
		return messages
	}
	return append(messages, llm.Message{Role: "system", Content: sharedAgentConversationContinuityPrompt})
}

// enforceExactSharedRecallAnswer keeps an explicit exact-token recall request
// deterministic when the shared L1 transcript contains one unambiguous literal.
// The model still receives the full RecallPack; this only guards against a model
// ignoring an exact-copy instruction after successful retrieval.
func enforceExactSharedRecallAnswer(userMessage, response string, pack *conversation.RecallPack) string {
	if literal, ok := exactSharedRecallAnswer(userMessage, pack); ok {
		return literal
	}
	return response
}

func exactSharedRecallAnswer(userMessage string, pack *conversation.RecallPack) (string, bool) {
	if pack == nil {
		return "", false
	}
	query := strings.ToLower(strings.TrimSpace(userMessage))
	requestsLiteral := strings.Contains(query, "合言葉") || strings.Contains(query, "英数字") || strings.Contains(query, "token")
	requestsExact := strings.Contains(query, "だけ") || strings.Contains(query, "そのまま") || strings.Contains(query, "一字も変えず") || strings.Contains(query, "exact") || strings.Contains(query, "verbatim")
	if !requestsLiteral || !requestsExact {
		return "", false
	}

	candidates := make(map[string]struct{})
	for _, message := range pack.ShortContext {
		for _, literal := range exactSharedRecallLiteralPattern.FindAllString(message.Msg, -1) {
			if containsASCIILetterAndDigit(literal) {
				candidates[literal] = struct{}{}
			}
		}
	}
	if len(candidates) != 1 {
		return "", false
	}
	for literal := range candidates {
		return literal, true
	}
	return "", false
}

func containsASCIILetterAndDigit(value string) bool {
	hasLetter := false
	hasDigit := false
	for _, char := range value {
		switch {
		case char >= 'A' && char <= 'Z', char >= 'a' && char <= 'z':
			hasLetter = true
		case char >= '0' && char <= '9':
			hasDigit = true
		}
	}
	return hasLetter && hasDigit
}

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

func conversationSpeakerForViewerRecipient(recipient string, fallback conversation.Speaker) conversation.Speaker {
	switch strings.ToLower(strings.TrimSpace(recipient)) {
	case "mio":
		return conversation.SpeakerMio
	case "shiro":
		return conversation.SpeakerShiro
	case "kuro":
		return conversation.SpeakerKuro
	case "midori":
		return conversation.SpeakerMidori
	default:
		return fallback
	}
}
