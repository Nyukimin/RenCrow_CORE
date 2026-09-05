package main

import (
	"fmt"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
)

// turnInputFromAgentMessage restores the canonical conversation input carried
// by an Agent transport message. Standalone consumers must never synthesize
// identities or derive one canonical identity from another.
func turnInputFromAgentMessage(msg domaintransport.Message) (conversation.TurnInput, error) {
	input, err := msg.ReconstructTurnInput()
	if err != nil {
		return conversation.TurnInput{}, fmt.Errorf("invalid agent turn input projection: %w", err)
	}
	return input, nil
}
