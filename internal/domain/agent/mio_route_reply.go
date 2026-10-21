package agent

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
)

var errMioChatDraftUnavailable = errors.New("combined Mio CHAT reply is unavailable")

// MioChatDraft is an opaque, turn-local handoff from route selection to CHAT
// dispatch. It is not committed or published until ChatWithDraft is called.
type MioChatDraft struct {
	reply       string
	failure     error
	ready       bool
	turnID      string
	sessionID   string
	messageID   string
	messageHash [32]byte
	generation  llm.GenerateResponse
	recallPack  *conversation.RecallPack
	latestOther string
	speaker     conversation.Speaker
}

// Failed reports whether preparation encountered an error that makes this
// draft terminal. The coordinator must retain it so CHAT dispatch returns that
// error instead of promoting the fallback decision into another LLM flow.
func (draft *MioChatDraft) Failed() bool {
	return draft != nil && draft.failure != nil
}

// DecideActionWithChatDraft follows the same deterministic route precedence as
// DecideAction. When allowed and eligible, its classifier may also prepare a
// Mio reply for explicit adoption after the orchestrator activates the route.
func (m *MioAgent) DecideActionWithChatDraft(ctx context.Context, t conversation.TurnInput, allowCombined bool) (routing.Decision, *MioChatDraft, error) {
	return m.decideAction(ctx, t, allowCombined)
}

// ChatWithDraft adopts a previously generated reply after task/run activation.
// It never invokes the provider; invalid drafts fail closed instead of falling
// back to another LLM generation.
func (m *MioAgent) ChatWithDraft(ctx context.Context, t conversation.TurnInput, draft *MioChatDraft) (string, error) {
	if draft == nil {
		return "", fmt.Errorf("%w: draft is nil", errMioChatDraftUnavailable)
	}
	if draft.turnID != string(t.TurnID()) || draft.sessionID != t.SessionID() ||
		draft.messageID != string(t.UserMessageID()) || draft.messageHash != sha256.Sum256([]byte(t.MessageText())) {
		return "", fmt.Errorf("%w: draft does not belong to this conversation turn", errMioChatDraftUnavailable)
	}
	if draft.failure != nil {
		return "", fmt.Errorf("%w: %v", errMioChatDraftUnavailable, draft.failure)
	}
	if !draft.ready || strings.TrimSpace(draft.reply) == "" {
		return "", fmt.Errorf("%w: CHAT reply is empty", errMioChatDraftUnavailable)
	}
	userMessage := t.MessageText()
	response := conversation.StripLeadingSpeakerLabels(draft.reply, draft.speaker)
	if strings.TrimSpace(response) == "" {
		return "", fmt.Errorf("%w: CHAT reply is empty after speaker-label filtering", errMioChatDraftUnavailable)
	}
	if violatesAttributionInChat(response, draft.latestOther) {
		return "", fmt.Errorf("%w: CHAT reply failed attribution validation", errMioChatDraftUnavailable)
	}
	response = enforceExactSharedRecallAnswer(userMessage, response, draft.recallPack)
	response = conversation.StripLeadingSpeakerLabels(response, draft.speaker)
	m.rememberExpression(response)
	if draft.generation.Content != "" {
		reportGenerationMetrics(ctx, draft.generation)
	}
	if m.conversationEngine != nil {
		if err := commitConversationTurn(ctx, m.conversationEngine, t, t.SessionID(), userMessage, response, draft.speaker, draft.recallPack); err != nil {
			return response, err
		}
	}
	return response, nil
}

func (m *MioAgent) classifyChatWithDraft(ctx context.Context, t conversation.TurnInput, evidence []routing.DecisionEvidence) (routing.Decision, *MioChatDraft, error) {
	userMessage := t.MessageText()
	draft := &MioChatDraft{
		turnID:      string(t.TurnID()),
		sessionID:   t.SessionID(),
		messageID:   string(t.UserMessageID()),
		messageHash: sha256.Sum256([]byte(userMessage)),
		recallPack:  nil,
		speaker:     conversationSpeakerForViewerRecipient(t.ViewerRecipient(), conversation.SpeakerMio),
	}
	recallPack, messages := m.beginChatRecall(ctx, t, userMessage)
	draft.recallPack = recallPack
	if exactReply, ok := exactSharedRecallAnswer(userMessage, recallPack); ok {
		// Exact-token recall stays deterministic. The normal route classifier
		// runs once, and the already prepared answer is adopted after activation.
		turntiming.Mark(ctx, turntiming.PointClassificationStarted)
		classified, err := m.classifier.Classify(ctx, t)
		turntiming.Mark(ctx, turntiming.PointClassificationEnded)
		if err == nil && classified.Route != "" && classified.Confidence >= 0.7 {
			classified.Evidence = append(evidence, classifierEvidence(classified)...)
			draft.reply = exactReply
			draft.ready = true
			return classified, draft, nil
		}
		draft.reply = exactReply
		draft.ready = true
		return routeDecisionFallback(evidence, classified, err), draft, nil
	}

	turntiming.Mark(ctx, turntiming.PointPromptPreparationStarted)
	messages, latestOther := m.assembleChatPrompt(ctx, t, userMessage, messages, recallPack, false)
	draft.latestOther = latestOther
	turntiming.Mark(ctx, turntiming.PointPromptPreparationEnded)
	req := m.generationRequest(messages, nil)
	generationCtx := turntiming.WithBodyGenerationContext(ctx)
	turntiming.Mark(ctx, turntiming.PointClassificationStarted)
	turntiming.Mark(ctx, turntiming.PointGenerationProviderStart)
	classification, err := m.classifier.(RouteReplyClassifier).ClassifyWithChatReply(generationCtx, req)
	turntiming.Mark(ctx, turntiming.PointGenerationProviderEnd)
	turntiming.Mark(ctx, turntiming.PointClassificationEnded)
	if err != nil {
		draft.failure = err
		return routeDecisionFallback(evidence, routing.Decision{}, err), draft, nil
	}
	if classification.Decision.Route == "" || classification.Decision.Confidence < 0.7 {
		draft.failure = fmt.Errorf("combined classifier returned low-confidence route %q", classification.Decision.Route)
		return routeDecisionFallback(evidence, classification.Decision, nil), draft, nil
	}
	classification.Decision.Evidence = append(evidence, classifierEvidence(classification.Decision)...)
	draft.reply = classification.Reply
	draft.ready = classification.ReplyValid
	draft.generation = classification.Generation
	if classification.Decision.Route == routing.RouteCHAT && !draft.ready {
		draft.failure = errors.New("combined classifier returned an empty CHAT reply")
	}
	return classification.Decision, draft, nil
}

func routeDecisionFallback(evidence []routing.DecisionEvidence, classified routing.Decision, classifyErr error) routing.Decision {
	reason := "classifier returned low confidence"
	if classifyErr != nil {
		reason = fmt.Sprintf("classifier failed: %v", classifyErr)
	} else if classified.Route == "" {
		reason = "classifier returned empty route"
	}
	evidence = append(evidence, routing.DecisionEvidence{
		Source:     routing.EvidenceSourceClassifier,
		Matched:    false,
		Route:      classified.Route,
		Confidence: classified.Confidence,
		Reason:     reason,
	}, routing.DecisionEvidence{
		Source:     routing.EvidenceSourceSafeFallback,
		Matched:    true,
		Route:      routing.RouteCHAT,
		Confidence: 0.7,
		Reason:     "default to CHAT",
	})
	return routing.NewDecisionWithEvidence(routing.RouteCHAT, 0.7, "No rule match, default to CHAT", evidence...)
}

func (m *MioAgent) canUseCombinedChatDraft(ctx context.Context, t conversation.TurnInput) bool {
	if m.conversationEngine == nil || m.llmProvider == nil || SpeechPlanCallbackFromContext(ctx) != nil {
		return false
	}
	if _, ok := m.conversationEngine.(typedConversationTurnEngine); !ok {
		return false
	}
	if len(t.Attachments()) != 0 || t.ForcedRoute() != "" {
		return false
	}
	recipient := strings.ToLower(strings.TrimSpace(t.ViewerRecipient()))
	if recipient != "" && recipient != "mio" {
		return false
	}
	message := strings.TrimSpace(t.MessageText())
	if strings.HasPrefix(message, "/") {
		return false
	}
	if _, ok := parseMioMovieCatalogLookup(message); ok {
		return false
	}
	if _, ok := parseMioPersonRelatedCatalogLookup(message); ok {
		return false
	}
	if _, ok := parseMioDataToolLookup(message); ok {
		return false
	}
	if needsWebSearch(message) || (m.personaEditor != nil && detectPersonaEditIntent(message)) {
		return false
	}
	return true
}
