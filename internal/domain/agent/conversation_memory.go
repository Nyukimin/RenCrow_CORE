package agent

import (
	"context"
	"log"
	"regexp"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
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
	return append(messages, llm.Message{
		Role:     "system",
		Content:  sharedAgentConversationContinuityPrompt,
		Type:     llm.PromptContextRecall,
		Metadata: map[string]string{"recall_section": "l0"},
	})
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

type typedConversationTurnEngine interface {
	CommitConversationTurn(context.Context, conversation.ConversationTurnRequest) (conversation.ConversationTurnResult, error)
}

type preparedConversationTurnEngine interface {
	PrepareConversationTurn(context.Context, string, string) (*conversation.ConversationTurnPreparation, error)
	ReleaseConversationTurnPreparation(context.Context, *conversation.ConversationTurnPreparation) error
}

type preparedChatTurnEngine interface {
	PrepareChatTurn(context.Context, string, string, string) (*conversation.PreparedChatTurn, error)
}

type preparedChatTurnCapability interface {
	SupportsPreparedChatTurn() bool
}

func supportsPreparedChatTurn(engine conversation.ConversationEngine) bool {
	if _, ok := engine.(preparedChatTurnEngine); !ok {
		return false
	}
	if capability, ok := engine.(preparedChatTurnCapability); ok {
		return capability.SupportsPreparedChatTurn()
	}
	return true
}

// prepareReadyChatTurn opts production engines into the atomic RecallPack,
// head preparation, and guard path. Engines that explicitly report this
// capability unsupported, or legacy test engines that do not implement it,
// retain the existing combined-draft behavior.
func prepareReadyChatTurn(ctx context.Context, engine conversation.ConversationEngine, t conversation.TurnInput) (*conversation.PreparedChatTurn, error) {
	preparedEngine, ok := engine.(preparedChatTurnEngine)
	if !ok {
		return nil, conversation.ErrConversationTurnUnavailable
	}
	prepared, err := preparedEngine.PrepareChatTurn(ctx, string(t.TurnID()), t.SessionID(), t.MessageText())
	return prepared, err
}

func prepareConversationTurn(ctx context.Context, engine conversation.ConversationEngine, t conversation.TurnInput) (*conversation.ConversationTurnPreparation, error) {
	if !supportsConversationTurnPreparation(engine) {
		return nil, conversation.ErrConversationTurnUnavailable
	}
	preparer := engine.(preparedConversationTurnEngine)
	return preparer.PrepareConversationTurn(ctx, string(t.TurnID()), t.SessionID())
}

type conversationTurnPreparationCapability interface {
	SupportsConversationTurnPreparation() bool
}

func supportsConversationTurnPreparation(engine conversation.ConversationEngine) bool {
	if _, ok := engine.(preparedConversationTurnEngine); !ok {
		return false
	}
	if capability, ok := engine.(conversationTurnPreparationCapability); ok {
		return capability.SupportsConversationTurnPreparation()
	}
	return true
}

func releaseConversationTurnPreparation(ctx context.Context, engine conversation.ConversationEngine, preparation *conversation.ConversationTurnPreparation) {
	if preparation == nil {
		return
	}
	releaser, ok := engine.(preparedConversationTurnEngine)
	if !ok {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	} else {
		ctx = context.WithoutCancel(ctx)
	}
	if err := releaser.ReleaseConversationTurnPreparation(ctx, preparation); err != nil {
		log.Printf("[ConversationTurn] prepared head release failed: %v", err)
	}
}

func releasePreparedChatTurnGuard(guard conversation.PreparedChatTurnGuard) {
	if guard == nil {
		return
	}
	if err := guard.Release(); err != nil {
		log.Printf("[ConversationTurn] prepared chat turn release failed: %v", err)
	}
}

// commitConversationTurn is the only production Agent completion route. The
// narrow capability check prevents an Agent from silently falling back to the
// legacy EndTurn/EndTurnAs API, which cannot carry the exact filtered RecallPack
// or the durable outbox transaction.
//
// Accepted means the canonical manager admitted the immutable turn to the
// shared persistence queue. Partial means the turn is committed in L1 with its
// receipt while one or more followers stay in the durable outbox.
func commitConversationTurn(ctx context.Context, engine conversation.ConversationEngine, t conversation.TurnInput, sessionID, userMessage, response string, speaker conversation.Speaker, pack *conversation.RecallPack, preparation ...*conversation.ConversationTurnPreparation) error {
	committer, ok := engine.(typedConversationTurnEngine)
	if !ok {
		return conversation.ErrConversationTurnUnavailable
	}
	var prepared *conversation.ConversationTurnPreparation
	if len(preparation) > 0 {
		prepared = preparation[0]
	}
	turntiming.Mark(ctx, turntiming.PointConversationCommitStarted)
	result, err := committer.CommitConversationTurn(ctx, conversation.ConversationTurnRequest{
		TurnID:           t.TurnID(),
		TraceID:          t.TraceID(),
		RootTaskID:       t.RootTaskID(),
		UserMessageID:    t.UserMessageID(),
		AgentMessageID:   t.AgentMessageID(),
		SessionID:        sessionID,
		UserMessage:      userMessage,
		AgentMessage:     response,
		AgentSpeaker:     speaker,
		RecallTraceItems: pack.ToTraceItems(),
		Preparation:      prepared,
	})
	turntiming.Mark(ctx, turntiming.PointConversationCommitEnded)
	if err != nil {
		return err
	}
	if result.Status == conversation.ConversationTurnAccepted {
		return nil
	}
	if result.Status == conversation.ConversationTurnPartial {
		log.Printf("[ConversationTurn] partial outcome turn_id=%s pending_targets=%v error_code=%s",
			result.TurnID, result.PendingTargets, result.ErrorCode)
		return nil
	}
	if result.Status != conversation.ConversationTurnCompleted {
		return conversation.ErrConversationTurnInternal
	}
	return nil
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
