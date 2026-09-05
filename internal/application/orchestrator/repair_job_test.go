package orchestrator

import (
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestRepairTargetRouteUsesExplicitCoderSlot(t *testing.T) {
	for input, want := range map[string]routing.Route{
		"CODE1": routing.RouteCODE1,
		"CODE2": routing.RouteCODE2,
		"code3": routing.RouteCODE3,
		"CODE4": routing.RouteCODE4,
		"CHAT":  routing.RouteCODE2,
		"":      routing.RouteCODE2,
	} {
		if got := repairTargetRoute(input); got != want {
			t.Fatalf("repairTargetRoute(%q)=%s want=%s", input, got, want)
		}
	}
}

func TestRepairTurnInputCarriesCanonicalIdentitySeparatelyFromCompanionJobID(t *testing.T) {
	sessionID := string(modulecore.NewSessionID())
	jobID := modulecore.NewTaskID()
	got, err := repairTurnInput(ProcessRepairRequest{
		JobID:     jobID.String(),
		SessionID: sessionID,
	}, routing.RouteCODE2)
	if err != nil {
		t.Fatalf("repairTurnInput() error = %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("repair turn input validation failed: %v", err)
	}
	if got.RootTaskID().Validate() != nil || got.TurnID().Validate() != nil || got.TraceID().Validate() != nil || got.UserMessageID().Validate() != nil || got.AgentMessageID().Validate() != nil {
		t.Fatalf("repair turn input has invalid canonical identity: %#v", got)
	}
	if string(got.RootTaskID()) == jobID.String() {
		t.Fatalf("companion JobID was mixed into root task ID: job=%q root=%q", jobID, got.RootTaskID())
	}
	if got.MessageText() == "" || !strings.Contains(got.MessageText(), "Repair Job") {
		t.Fatalf("repair message = %q", got.MessageText())
	}
	if got.SessionID() != sessionID {
		t.Fatalf("repair turn input session=%q, want %q", got.SessionID(), sessionID)
	}
	address := got.ChannelAddress()
	if address.ChannelType() != "viewer" || address.ExternalConversationID() != "repair" {
		t.Fatalf("repair turn input address = %#v", address)
	}
	if got.Route() != routing.RouteCODE2 || got.HasForcedRoute() {
		t.Fatalf("repair turn input route=%q forced=%t", got.Route(), got.HasForcedRoute())
	}
}
