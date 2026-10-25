package orchestrator

import (
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
)

func TestPhase22DistributedCoderSelectionGenericUsesReadyConnectedPool(t *testing.T) {
	selector := newDistributedCoderSelection(nil, map[string]domaintransport.Transport{
		"coder2": &distMockTransport{},
	}, NewNodeSelector(), nil)
	selector.SetCoderCapabilities([]capability.CoderCapability{{Name: "coder2", Available: true}})

	got, release, err := selector.AcquireForRoute(routing.RouteCODE, "")
	if err != nil || got != "coder2" {
		t.Fatalf("AcquireForRoute(CODE) = %q, %v, want connected ready coder2", got, err)
	}
	if !selector.coderStatus.IsBusy("coder2") {
		t.Fatal("selected coder2 should remain occupied until task release")
	}
	release()
	if selector.coderStatus.IsBusy("coder2") {
		t.Fatal("coder2 should be idle after release")
	}
}

func TestPhase22DistributedCoderSelectionExplicitRouteDoesNotFallback(t *testing.T) {
	selector := newDistributedCoderSelection(nil, map[string]domaintransport.Transport{
		"coder2": &distMockTransport{},
	}, NewNodeSelector(), nil)

	if got, release, err := selector.AcquireForRoute(routing.RouteCODE1, ""); got != "" || release != nil || err == nil {
		t.Fatalf("AcquireForRoute(CODE1) = %q, release=%t, %v, want unavailable without fallback", got, release != nil, err)
	}
}

func TestPhase22DistributedCoderSelectionSSHTransportMeansConnected(t *testing.T) {
	selector := newDistributedCoderSelection(nil, map[string]domaintransport.Transport{
		"coder3": &distMockTransport{},
	}, NewNodeSelector(), nil)

	if !selector.IsCoderConnected("coder3") {
		t.Fatal("expected SSH transport to count as connected")
	}
	if selector.IsCoderConnected("coder1") {
		t.Fatal("expected missing coder to be disconnected")
	}
}
