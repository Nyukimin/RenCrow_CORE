package orchestrator

import (
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
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
