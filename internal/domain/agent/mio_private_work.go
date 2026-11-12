package agent

import (
	"context"
	"fmt"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

// ExecutePrivateWork generates a text-only response for a Task-owned Mio or
// Shiro assignment. It deliberately avoids public conversation, recall,
// persona editing, and Tool paths.
func (m *MioAgent) ExecutePrivateWork(ctx context.Context, input conversation.TurnInput, allowedCapabilities []string) (string, error) {
	actorID := input.ViewerRecipient()
	if actorID != "mio" && actorID != "shiro" {
		return "", fmt.Errorf("%w: unsupported Mio private-work recipient %q", ErrPrivateWorkCapabilityDenied, actorID)
	}

	capabilities, err := validatePrivateWorkEntry(ctx, input, actorID, allowedCapabilities)
	if err != nil {
		return "", err
	}
	if len(capabilities) != 0 {
		return "", fmt.Errorf("%w: Mio private work is text-only", ErrPrivateWorkCapabilityDenied)
	}
	if m == nil || m.llmProvider == nil {
		return "", fmt.Errorf("Mio private-work LLM provider is unavailable")
	}

	ctx = llm.WithExecutionObservationDefaults(ctx, llm.ExecutionObservation{
		TaskID: input.RootTaskID(), TraceID: string(input.TraceID()),
		Initiator: actorID, Caller: "agent." + actorID, Purpose: "execute_private_work",
	})
	messages := assemblePromptContext(
		m.systemPromptForViewerRecipient(actorID),
		"",
		nil,
		llm.Message{Role: "user", Content: input.MessageText()},
	)
	request := m.generationRequest(messages, nil)
	request.ResponseFormat = llm.ResponseFormatText

	response, err := m.llmProvider.Generate(ctx, request)
	if err != nil {
		return "", err
	}
	return response.Content, nil
}
