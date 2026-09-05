package routing

import (
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func newRoutingTurnInput(t *testing.T, messageText, channelType, externalConversationID string) conversation.TurnInput {
	t.Helper()
	address, err := conversation.NewChannelAddress(channelType, externalConversationID)
	if err != nil {
		t.Fatalf("NewChannelAddress(%q, %q): %v", channelType, externalConversationID, err)
	}
	input, err := conversation.NewTurnInput(modulecore.NewTaskID(), messageText, address)
	if err != nil {
		t.Fatalf("NewTurnInput(%q): %v", messageText, err)
	}
	return input
}
