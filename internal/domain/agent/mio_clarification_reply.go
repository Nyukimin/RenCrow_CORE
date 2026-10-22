package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// GenerateClarificationReplyInstruction makes one Mio call for an explicit
// public answer to a stored clarification prompt. The output can only supply
// a revised private instruction; routing and the original handoff contract
// remain owned by CORE.
func (g *MioEmissionGenerator) GenerateClarificationReplyInstruction(
	ctx context.Context,
	scope conversation.ConversationScope,
	snapshot PromptSnapshot,
	input conversation.ClarificationReplyGenerationContext,
) (string, InvocationReceipt, error) {
	if err := input.Validate(scope); err != nil {
		return "", InvocationReceipt{}, err
	}
	if g == nil || g.provider == nil {
		return "", InvocationReceipt{}, errors.New("Mio conversation provider is unavailable")
	}
	snapshot = snapshot.Clone()
	allowlist := conversation.EmissionAllowlist{AllowedSourceMessageIDs: append([]core.MessageID(nil), snapshot.AllowedSourceMessageIDs...)}
	if err := validateEmissionSnapshot(snapshot, allowlist); err != nil {
		return "", InvocationReceipt{}, err
	}
	last := snapshot.Messages[len(snapshot.Messages)-1]
	if last.Role != "user" || strings.TrimSpace(last.Content) == "" || snapshot.AllowedSourceMessageIDs[len(snapshot.AllowedSourceMessageIDs)-1] != scope.InputID {
		return "", InvocationReceipt{}, errors.New("clarification reply prompt must end with the accepted public answer")
	}

	handoffContext, err := clarificationReplyHandoffContextMessage(input)
	if err != nil {
		return "", InvocationReceipt{}, fmt.Errorf("marshal canonical clarification reply handoff: %w", err)
	}
	question := llm.Message{
		Role: "user", Type: llm.PromptContextVariable,
		Metadata: map[string]string{
			"runtime_context_kind": "agent_clarification_question",
			"visibility":           "private",
			"source_actor_id":      input.QuestionSender,
			"message_id":           string(input.QuestionMessageID),
		},
		Content: "Original private clarification question from the assigned agent (not a public user message):\n" + input.QuestionText,
	}
	answer := llm.Message{
		Role: "user", Type: llm.PromptContextVariable,
		Metadata: map[string]string{
			"runtime_context_kind": "public_clarification_answer",
			"visibility":           "public",
			"source_actor_id":      scope.ActorID,
			"message_id":           string(scope.InputID),
		},
		Content: "Explicit public answer from the authenticated user (the final canonical user message below):\n" + last.Content,
	}
	messages := append(snapshot.Messages[:len(snapshot.Messages)-1], handoffContext, question, answer, last)
	messages = insertEmissionContract(messages, clarificationReplyInstructionContract(input.Handoff.ToActor))
	request := (&MioAgent{generation: g.generation}).generationRequest(messages, nil)
	request.ResponseFormat = llm.ResponseFormatJSONObject
	request.OnToken = nil
	response, generateErr := g.provider.GenerateConversation(ctx, scope, request)
	receipt := InvocationReceipt{
		RequestID: string(response.RequestID), ResponseID: string(response.ResponseID),
		ProviderLabel: strings.TrimSpace(g.provider.Name()), TokensUsed: response.TokensUsed, Status: "completed",
	}
	if generateErr != nil {
		receipt.Status = "generation_failed"
		return "", receipt, fmt.Errorf("generate Mio clarification reply instruction: %w", generateErr)
	}
	parsed, err := conversation.ParseClarificationReplyInstruction([]byte(response.Content), scope)
	if err != nil {
		receipt.Status = "invalid_output"
		return "", receipt, fmt.Errorf("validate Mio clarification reply instruction: %w", err)
	}
	return parsed.Instruction, receipt, nil
}

func clarificationReplyHandoffContextMessage(input conversation.ClarificationReplyGenerationContext) (llm.Message, error) {
	handoff := input.Handoff
	content, err := json.Marshal(struct {
		DelegationID        string                         `json:"delegation_id"`
		Revision            uint64                         `json:"revision"`
		Assignee            string                         `json:"assignee"`
		Goal                string                         `json:"goal"`
		CurrentInstruction  string                         `json:"current_instruction"`
		Constraints         []conversation.AgentConstraint `json:"constraints"`
		ExpectedOutput      string                         `json:"expected_output"`
		AllowedCapabilities []string                       `json:"allowed_capabilities"`
	}{
		DelegationID: string(handoff.DelegationID), Revision: handoff.Revision, Assignee: handoff.ToActor,
		Goal: handoff.Goal, CurrentInstruction: handoff.Instruction, Constraints: handoff.Constraints,
		ExpectedOutput: handoff.ExpectedOutput, AllowedCapabilities: handoff.AllowedCapabilities,
	})
	if err != nil {
		return llm.Message{}, err
	}
	return llm.Message{
		Role: "user", Type: llm.PromptContextVariable,
		Metadata: map[string]string{
			"runtime_context_kind": "canonical_clarification_reply_handoff",
			"visibility":           "private",
		},
		Content: "Canonical private handoff from EventStore (preserve its goal, constraints, expected output, and capability set):\n" + string(content),
	}, nil
}

func clarificationReplyInstructionContract(assignee string) []llm.Message {
	return []llm.Message{{
		Role: "system", Type: llm.PromptContextVariable,
		Metadata: map[string]string{"runtime_context_kind": "mio_clarification_reply_contract"},
		Content: fmt.Sprintf(`Mio clarification reply output contract:
The user has explicitly answered a public clarification prompt. The prompt contains the exact canonical public answer, a separately labeled copy of that answer, the original private question from the assigned agent, and the canonical stored handoff.
Produce one clearer private instruction for the existing assignee %q that incorporates the user's answer and the original task. Preserve the existing goal, constraints, expected output, and allowed capabilities. Do not choose another assignee, add/remove constraints or capabilities, ask the user another question, produce a user-facing response, or claim any work has been done.
Return exactly one JSON object with no extra fields or prose: {"schema_version":"mio.clarification_reply.v1","instruction":"..."}.`, assignee),
	}}
}
