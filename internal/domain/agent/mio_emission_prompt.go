package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

// ComposeConversationPrompt adds Mio's authored and runtime-owned context to
// the accepted canonical history. It deliberately does not construct a legacy
// TurnInput, Task, or Run: the accepted conversation scope already identifies
// the session and the snapshot already contains the current user input.
func (m *MioAgent) ComposeConversationPrompt(ctx context.Context, scope conversation.ConversationScope, snapshot PromptSnapshot) (PromptSnapshot, error) {
	if ctx == nil {
		return PromptSnapshot{}, errors.New("Mio conversation prompt context is required")
	}
	if m == nil {
		return PromptSnapshot{}, errors.New("Mio agent is unavailable")
	}
	if err := scope.Validate(); err != nil {
		return PromptSnapshot{}, fmt.Errorf("validate Mio conversation scope: %w", err)
	}
	switch scope.Purpose {
	case conversation.PurposeMioTurn, conversation.PurposeReportReply, conversation.PurposeClarificationControl, conversation.PurposeClarificationReply:
	default:
		return PromptSnapshot{}, fmt.Errorf("unsupported Mio prompt purpose %q", scope.Purpose)
	}
	if err := validateMioConversationPromptSnapshot(snapshot); err != nil {
		return PromptSnapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return PromptSnapshot{}, err
	}

	composed := snapshot.Clone()
	currentUser := composed.Messages[len(composed.Messages)-1]
	dynamic := make([]llm.Message, 0, len(composed.Messages)+8)

	if m.conversationEngine != nil {
		recallPack, err := m.conversationEngine.BeginTurn(ctx, string(scope.SessionID), currentUser.Content)
		if err != nil {
			return PromptSnapshot{}, fmt.Errorf("load Mio conversation recall: %w", err)
		}
		if recallPack != nil {
			filtered := recallPack.FilterForRole("chat").WithoutPersonaSystemPrompt()
			// The snapshot is the canonical source for this conversation. BeginTurn
			// may return a stale short-history projection, including a rolling
			// summary that overlaps it, so only noncanonical memory hints are added.
			filtered.ShortContext = nil
			filtered.RollingSummary = ""
			dynamic = append(dynamic, filtered.ToPromptMessages()...)
		}
	}

	if m.recentContext != nil {
		if glossaryContext, err := m.recentContext(ctx, 6); err != nil {
			log.Printf("[Mio] recent context failed: %v", err)
		} else if strings.TrimSpace(glossaryContext) != "" {
			dynamic = append(dynamic, llm.Message{
				Role:    "system",
				Content: strings.TrimSpace(glossaryContext) + "\n最近の語彙は、断定せず軽い補足として扱ってください。",
				Type:    llm.PromptContextRecall,
			})
		}
	}
	if brief, ok := dailyNewsBriefFromContext(ctx); ok {
		dynamic = append(dynamic, llm.Message{
			Role:    "system",
			Content: dailyNewsBriefSystemPrompt(brief),
			Type:    llm.PromptContextRecall,
		})
	}

	// Snapshot history replaces BeginTurn's short context. Preserve the
	// canonical roles and text, while classifying prior turns as dynamic recall
	// so they follow the static character/stable prefix.
	for _, message := range composed.Messages[:len(composed.Messages)-1] {
		message.Type = llm.PromptContextRecall
		dynamic = append(dynamic, message)
	}
	if prompt := viewerRecipientSystemPrompt("mio", currentUser.Content); prompt != "" {
		dynamic = append(dynamic, llm.Message{
			Role:     "system",
			Content:  prompt,
			Type:     llm.PromptContextVariable,
			Metadata: map[string]string{"runtime_context_kind": "recipient_contract"},
		})
	}
	if err := ctx.Err(); err != nil {
		return PromptSnapshot{}, err
	}

	composed.Messages = assemblePromptContext(
		m.systemPromptForViewerRecipient("mio"),
		currentRuntimeContext(ctx, m.runtimeContextProvider, "mio", m.stableRuntimeContexts["mio"]),
		dynamic,
		currentUser,
	)
	return composed, nil
}

func validateMioConversationPromptSnapshot(snapshot PromptSnapshot) error {
	if len(snapshot.Messages) == 0 {
		return errors.New("Mio prompt snapshot must contain canonical conversation messages")
	}
	if len(snapshot.AllowedSourceMessageIDs) != len(snapshot.Messages) {
		return errors.New("Mio prompt snapshot source IDs do not match canonical messages")
	}
	seen := make(map[string]struct{}, len(snapshot.AllowedSourceMessageIDs))
	for _, id := range snapshot.AllowedSourceMessageIDs {
		if id.Validate() != nil {
			return errors.New("Mio prompt snapshot contains an invalid source message ID")
		}
		if _, exists := seen[string(id)]; exists {
			return errors.New("Mio prompt snapshot contains a duplicate source message ID")
		}
		seen[string(id)] = struct{}{}
	}
	for index, message := range snapshot.Messages {
		if strings.TrimSpace(message.Content) == "" {
			return errors.New("Mio prompt snapshot contains an empty canonical message")
		}
		if message.Role != "user" && message.Role != "assistant" {
			return errors.New("Mio prompt snapshot contains an unsupported canonical role")
		}
		if index == len(snapshot.Messages)-1 && message.Role != "user" {
			return errors.New("Mio prompt snapshot must end with the accepted user input")
		}
	}
	return nil
}
