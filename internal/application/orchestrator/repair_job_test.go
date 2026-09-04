package orchestrator

import (
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
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

func TestRepairTaskCarriesCanonicalSessionIDSeparatelyFromChatID(t *testing.T) {
	sessionID := string(modulecore.NewSessionID())
	got := repairTask(ProcessRepairRequest{
		JobID:     task.NewJobID().String(),
		SessionID: sessionID,
	}, routing.RouteCODE2)
	if got.SessionID() != sessionID || got.ChatID() != "repair" {
		t.Fatalf("repair task identity session=%q chat=%q", got.SessionID(), got.ChatID())
	}
}
