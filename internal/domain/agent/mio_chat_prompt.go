package agent

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
)

func (m *MioAgent) beginChatRecall(ctx context.Context, t conversation.TurnInput, userMessage string) (*conversation.RecallPack, []llm.Message) {
	if m.conversationEngine == nil {
		return nil, nil
	}
	turntiming.Mark(ctx, turntiming.PointBeginTurnStarted)
	recallPack, err := m.conversationEngine.BeginTurn(ctx, t.SessionID(), userMessage)
	turntiming.Mark(ctx, turntiming.PointBeginTurnEnded)
	if err != nil {
		log.Printf("[Mio] BeginTurn failed: %v", err)
	}
	if recallPack == nil {
		return nil, nil
	}
	turntiming.Mark(ctx, turntiming.PointRecallProjectionStarted)
	filtered := recallPack.FilterForRole("chat").WithoutPersonaSystemPrompt()
	recallPack = &filtered
	var messages []llm.Message
	messages = appendSharedConversationContinuityPrompt(messages, recallPack)
	messages = append(messages, recallPack.ToPromptMessages()...)
	turntiming.Mark(ctx, turntiming.PointRecallProjectionEnded)
	return recallPack, messages
}

// assembleChatPrompt applies Mio's shared runtime context and canonical prompt
// order to both regular CHAT and the combined route/reply candidate.
func (m *MioAgent) assembleChatPrompt(
	ctx context.Context,
	t conversation.TurnInput,
	userMessage string,
	messages []llm.Message,
	recallPack *conversation.RecallPack,
	speechPlanMode bool,
) ([]llm.Message, string) {
	latestOther := ""
	attributionPrompt := ""
	if recallPack != nil {
		selfCtx, otherCtx := buildAttributionContextsFromShort(recallPack.ShortContext, conversationSpeakerForViewerRecipient(t.ViewerRecipient(), conversation.SpeakerMio), 5)
		latestOther = latestOtherMessageFromShort(recallPack.ShortContext, conversationSpeakerForViewerRecipient(t.ViewerRecipient(), conversation.SpeakerMio))
		attributionPrompt = fmt.Sprintf(
			"発言帰属ガード:\n- 現在のAgentは%s。\n- 自分の過去発言(要約): %s\n- 他者の発言(要約): %s\n要件: 他者の発言を自分の新規アイデアとして扱わない。既出アイデアに触れる場合は発言者を明示する。",
			conversationSpeakerForViewerRecipient(t.ViewerRecipient(), conversation.SpeakerMio),
			strings.Join(selfCtx, " / "),
			strings.Join(otherCtx, " / "),
		)
	}

	if m.recentContext != nil {
		if glossaryContext, err := m.recentContext(ctx, 6); err != nil {
			log.Printf("[Mio] recent context failed: %v", err)
		} else if strings.TrimSpace(glossaryContext) != "" {
			messages = append(messages, llm.Message{
				Role:    "system",
				Content: glossaryContext + "\n最近の語彙は、断定せず軽い補足として扱ってください。",
				Type:    llm.PromptContextRecall,
			})
		}
	}
	if brief, ok := dailyNewsBriefFromContext(ctx); ok {
		messages = append(messages, llm.Message{
			Role:    "system",
			Content: dailyNewsBriefSystemPrompt(brief),
			Type:    llm.PromptContextRecall,
		})
	}
	if prompt := viewerRecipientSystemPrompt(t.ViewerRecipient(), userMessage); prompt != "" {
		messages = append(messages, llm.Message{Role: "system", Content: prompt, Type: llm.PromptContextVariable, Metadata: map[string]string{"runtime_context_kind": "recipient_contract"}})
	}
	if prompt := m.runtimeMioPromptContext(t); prompt != "" {
		messages = append(messages, llm.Message{Role: "system", Content: prompt, Type: llm.PromptContextVariable, Metadata: map[string]string{"runtime_context_kind": "runtime_state"}})
	}
	if attributionPrompt != "" {
		messages = append(messages, llm.Message{Role: "system", Content: attributionPrompt, Type: llm.PromptContextVariable, Metadata: map[string]string{"runtime_context_kind": "attribution_state"}})
	}
	if speechPlanMode {
		messages = append(messages, llm.Message{Role: "system", Content: speechPlanInstruction(), Type: llm.PromptContextVariable, Metadata: map[string]string{"runtime_context_kind": "speech_plan_contract"}})
	}

	currentUserMessage := userMessageWithAttachments(userMessage, t.Attachments())
	turntiming.Mark(ctx, turntiming.PointPromptAssemblyStarted)
	prepared := assemblePromptContext(
		m.systemPromptForViewerRecipient(t.ViewerRecipient()),
		m.stablePromptContext(ctx, t),
		messages,
		currentUserMessage,
	)
	return prepared, latestOther
}
