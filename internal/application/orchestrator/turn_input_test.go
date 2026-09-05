package orchestrator

import (
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func newOrchestratorTestTurnInput(t *testing.T, message, channel, externalConversationID string) conversation.TurnInput {
	t.Helper()
	address, err := conversation.NewChannelAddress(channel, externalConversationID)
	if err != nil {
		t.Fatalf("create test channel address: %v", err)
	}
	input, err := conversation.NewTurnInput(modulecore.NewTaskID(), message, address)
	if err != nil {
		t.Fatalf("create test turn input: %v", err)
	}
	return input
}

func newOrchestratorTestCodeExecutionRequest(t *testing.T, jobID task.JobID, message string, route routing.Route, sessionID, channel, externalConversationID string) CodeExecutionRequest {
	t.Helper()
	input := newOrchestratorTestTurnInput(t, message, channel, externalConversationID).
		WithSessionID(sessionID).
		WithRoute(route)
	return CodeExecutionRequest{Input: input, JobID: jobID.String()}
}
