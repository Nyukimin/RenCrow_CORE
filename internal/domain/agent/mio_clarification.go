package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

// GenerateClarificationDecision runs one non-streaming Mio generation over
// the retained conversation snapshot and the authenticated private assignee
// question. The typed result cannot select another assignee or alter ACL.
func (g *MioEmissionGenerator) GenerateClarificationDecision(
	ctx context.Context,
	scope conversation.ConversationScope,
	snapshot PromptSnapshot,
	control conversation.ClarificationControlContext,
	allowlist conversation.EmissionAllowlist,
) (conversation.ClarificationDecision, InvocationReceipt, error) {
	if err := scope.Validate(); err != nil || scope.Purpose != conversation.PurposeClarificationControl {
		return conversation.ClarificationDecision{}, InvocationReceipt{}, errors.New("clarification generation requires a clarification control scope")
	}
	if err := control.Validate(scope); err != nil {
		return conversation.ClarificationDecision{}, InvocationReceipt{}, err
	}
	if g == nil || g.provider == nil {
		return conversation.ClarificationDecision{}, InvocationReceipt{}, errors.New("Mio conversation provider is unavailable")
	}
	snapshot = snapshot.Clone()
	if err := validateEmissionSnapshot(snapshot, allowlist); err != nil {
		return conversation.ClarificationDecision{}, InvocationReceipt{}, err
	}
	handoffContext, err := clarificationHandoffContextMessage(control)
	if err != nil {
		return conversation.ClarificationDecision{}, InvocationReceipt{}, fmt.Errorf("marshal canonical clarification handoff: %w", err)
	}
	messages := append(snapshot.Messages, handoffContext, llm.Message{
		Role: "user", Type: llm.PromptContextVariable,
		Metadata: map[string]string{
			"runtime_context_kind": "agent_clarification_question",
			"visibility":           "private",
			"source_actor_id":      control.QuestionSender,
			"message_id":           string(control.QuestionMessageID),
		},
		Content: "Private clarification question from the assigned agent (this is not a new user_message event):\n" + control.QuestionText,
	})
	messages = insertEmissionContract(messages, []llm.Message{clarificationEmissionContract(control)})
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
		return conversation.ClarificationDecision{}, receipt, fmt.Errorf("generate Mio clarification decision: %w", generateErr)
	}
	decision, err := conversation.ParseClarificationDecision([]byte(response.Content), scope)
	if err != nil {
		receipt.Status = "invalid_output"
		return conversation.ClarificationDecision{}, receipt, fmt.Errorf("validate Mio clarification decision: %w", err)
	}
	return decision, receipt, nil
}

func clarificationHandoffContextMessage(control conversation.ClarificationControlContext) (llm.Message, error) {
	content, err := json.Marshal(struct {
		Goal                string                         `json:"goal"`
		Instruction         string                         `json:"instruction"`
		Constraints         []conversation.AgentConstraint `json:"constraints"`
		ExpectedOutput      string                         `json:"expected_output"`
		AllowedCapabilities []string                       `json:"allowed_capabilities"`
	}{
		Goal: control.Handoff.Goal, Instruction: control.Handoff.Instruction,
		Constraints: control.Handoff.Constraints, ExpectedOutput: control.Handoff.ExpectedOutput,
		AllowedCapabilities: control.Handoff.AllowedCapabilities,
	})
	if err != nil {
		return llm.Message{}, err
	}
	return llm.Message{
		Role: "user", Type: llm.PromptContextVariable,
		Metadata: map[string]string{
			"runtime_context_kind": "canonical_clarification_handoff",
			"visibility":           "private",
		},
		Content: "Canonical private handoff data from EventStore (reference context, not a new user_message event):\n" + string(content),
	}, nil
}

func clarificationEmissionContract(control conversation.ClarificationControlContext) llm.Message {
	return llm.Message{
		Role: "system", Type: llm.PromptContextVariable,
		Metadata: map[string]string{"runtime_context_kind": "mio_clarification_contract"},
		Content: fmt.Sprintf(`Mio clarification control output contract.
The retained conversation context contains the original user request, the canonical handoff, and a private question from the assigned agent. Decide whether the stored context already answers that question. Ask the user only when required information is absent. Otherwise continue the same delegation with a clearer instruction.
The current canonical assignee is %q. CORE binds any continuation to this same assignee, keeps the same delegation ID, increments its revision once, and copies the existing goal, constraints, expected output, and allowed capabilities. You cannot change the assignee, constraints, or capabilities. Do not create or claim a Task.
Return exactly one JSON object using schema_version "mio.clarification.v1". For a user question use {"schema_version":"mio.clarification.v1","kind":"ask_user","question":"..."}. For same-assignee continuation use {"schema_version":"mio.clarification.v1","kind":"continue_delegation","instruction":"..."}. Do not include user_message, delegation identity, revision, recipient, constraints, or capabilities.`, control.Handoff.ToActor),
	}
}
