package main

import (
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestToConversationAcceptedTurnPreservesTriggerKind(t *testing.T) {
	stored := eventstore.AcceptedTurn{
		SessionID: modulecore.NewSessionID(), TurnID: modulecore.NewTurnID(), ActorID: "shiro",
		TriggerKind: "report_reply", InputMessageID: modulecore.NewMessageID(),
		AdmissionSeq: 15, GenerationEpoch: 3, State: "accepted",
	}

	got := toConversationAcceptedTurn(stored)
	if got.TriggerKind != stored.TriggerKind || got.ActorID != stored.ActorID || got.InputMessageID != stored.InputMessageID {
		t.Fatalf("accepted turn conversion = %+v, want persisted trigger and trusted report actor", got)
	}
}
