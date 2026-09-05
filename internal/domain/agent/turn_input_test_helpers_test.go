package agent

import (
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func newAgentTurnInput(t *testing.T, messageText, channelType, externalConversationID string) conversation.TurnInput {
	t.Helper()
	return newAgentTurnInputWithRoot(t, modulecore.NewTaskID(), messageText, channelType, externalConversationID)
}

func newAgentTurnInputWithRoot(t *testing.T, rootTaskID modulecore.TaskID, messageText, channelType, externalConversationID string) conversation.TurnInput {
	t.Helper()
	address, err := conversation.NewChannelAddress(channelType, externalConversationID)
	if err != nil {
		t.Fatalf("NewChannelAddress(%q, %q): %v", channelType, externalConversationID, err)
	}
	input, err := conversation.NewTurnInput(rootTaskID, messageText, address)
	if err != nil {
		t.Fatalf("NewTurnInput(%q): %v", messageText, err)
	}
	return input
}
