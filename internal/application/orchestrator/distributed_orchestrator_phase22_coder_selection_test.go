package orchestrator

import (
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/routing"
	domaintransport "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/transport"
)

func TestPhase22DistributedCoderSelectionCodeUsesOnlyCoder1(t *testing.T) {
	selector := newDistributedCoderSelection(nil, map[string]domaintransport.Transport{
		"coder2": &distMockTransport{},
	}, NewNodeSelector(), nil)

	if got := selector.RouteToCoder(routing.RouteCODE); got != "" {
		t.Fatalf("expected CODE to stay empty when coder1 is unconnected, got %s", got)
	}
}

func TestPhase22DistributedCoderSelectionExplicitRouteDoesNotFallback(t *testing.T) {
	selector := newDistributedCoderSelection(nil, map[string]domaintransport.Transport{
		"coder2": &distMockTransport{},
	}, NewNodeSelector(), nil)

	if got := selector.RouteToCoder(routing.RouteCODE1); got != "" {
		t.Fatalf("expected CODE1 to stay empty when coder1 is unconnected, got %s", got)
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
