package orchestrator

import (
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestDistributedCoderSelectionExplicitRoutesUseTheirNamedSlotIncludingCODE4(t *testing.T) {
	status := NewCoderStatus()
	selector := newDistributedCoderSelection(nil, connectedCoderTransports(), NewNodeSelector(), nil, status)
	routes := []struct {
		route routing.Route
		name  string
	}{
		{routing.RouteCODE1, "coder1"},
		{routing.RouteCODE2, "coder2"},
		{routing.RouteCODE3, "coder3"},
		{routing.RouteCODE4, "coder4"},
	}
	for _, tc := range routes {
		name, release, err := selector.AcquireForRoute(tc.route, "")
		if err != nil || name != tc.name {
			t.Fatalf("AcquireForRoute(%s) = %q, %v, want %s", tc.route, name, err, tc.name)
		}
		if !status.IsBusy(tc.name) {
			t.Fatalf("%s was not occupied after explicit selection", tc.name)
		}
		release()
	}
}

func TestDistributedCoderSelectionExplicitRouteRejectsUnavailableRequestedCoder(t *testing.T) {
	selector := newDistributedCoderSelection(nil, connectedCoderTransports(), NewNodeSelector(), nil)
	selector.SetCoderCapabilities([]capability.CoderCapability{
		{Name: "coder1", Available: true},
		{Name: "coder2", Available: true},
		{Name: "coder3", Available: true},
		{Name: "coder4", Available: false},
	})

	name, release, err := selector.AcquireForRoute(routing.RouteCODE4, "")
	if name != "" || release != nil || err == nil || !strings.Contains(err.Error(), "coder4 is unavailable") {
		t.Fatalf("AcquireForRoute(CODE4) = %q, release=%t, %v, want coder4 unavailable", name, release != nil, err)
	}
	if selector.coderStatus.IsBusy("coder1") || selector.coderStatus.IsBusy("coder4") {
		t.Fatal("unavailable explicit route must not acquire or substitute another coder")
	}
}

func TestDistributedCoderSelectionGenericRoundRobinsReadyConnectedCoders(t *testing.T) {
	selector := newDistributedCoderSelection(nil, connectedCoderTransports(), NewNodeSelector(), nil)
	selector.SetCoderCapabilities([]capability.CoderCapability{
		{Name: "coder1", Quality: 1, Available: true},
		{Name: "coder2", Quality: 5, Available: true},
		{Name: "coder3", Quality: 3, Available: true},
		{Name: "coder4", Quality: 4, Available: true},
	})

	var releases []func()
	for _, want := range []string{"coder1", "coder2", "coder3", "coder4"} {
		got, release, err := selector.AcquireForRoute(routing.RouteCODE, "")
		if err != nil || got != want {
			t.Fatalf("AcquireForRoute(CODE) = %q, %v, want %s", got, err, want)
		}
		releases = append(releases, release)
	}
	if _, release, err := selector.AcquireForRoute(routing.RouteCODE, ""); err == nil || release != nil {
		t.Fatalf("fifth simultaneous CODE acquisition = release %t, error %v; want all-busy error", release != nil, err)
	}
	for _, release := range releases {
		release()
	}
}

func TestDistributedOrchestratorAndLocalExecutorCanShareCoderStatus(t *testing.T) {
	sharedStatus := NewCoderStatus()
	router := newTestMessageRouter(t, "coder2")
	defer router.Stop()
	distributed := NewDistributedOrchestrator(&distMockSessionRepo{}, &distMockMioAgent{}, router, session.NewCentralMemory(), nil, sharedStatus)
	if distributed.coderStatus != sharedStatus || distributed.coderSelector.coderStatus != sharedStatus {
		t.Fatal("DistributedOrchestrator selection must use the injected shared CoderStatus")
	}

	name, release, err := distributed.coderSelector.AcquireForRoute(routing.RouteCODE2, "")
	if err != nil || name != "coder2" {
		t.Fatalf("distributed explicit acquisition = %q, %v", name, err)
	}
	local := NewDefaultCodeExecutor(nil, &mockCoderAgent{response: "local coder2"}, nil, nil, nil, sharedStatus, noopEventEmitter)
	req := newOrchestratorTestCodeExecutionRequest(t, modulecore.NewTaskID(), "work", routing.RouteCODE2, "session", "test", "chat")
	if _, err := local.ExecuteCode(t.Context(), req); err == nil || !strings.Contains(err.Error(), "coder2 is busy") {
		release()
		t.Fatalf("local route error = %v, want shared coder2 busy", err)
	}
	release()
	got, err := local.ExecuteCode(t.Context(), req)
	if err != nil || got.Response != "local coder2" {
		t.Fatalf("local route after shared release = %#v, %v", got, err)
	}
}

func TestDistributedExecutionTreatsCODE4AsCodeRoute(t *testing.T) {
	if !isCodeRoute(routing.RouteCODE4) {
		t.Fatal("CODE4 must be recognized as a distributed code route")
	}
}

func connectedCoderTransports() map[string]domaintransport.Transport {
	return map[string]domaintransport.Transport{
		"coder1": &distMockTransport{},
		"coder2": &distMockTransport{},
		"coder3": &distMockTransport{},
		"coder4": &distMockTransport{},
	}
}

func newTestMessageRouter(t *testing.T, agents ...string) *transport.MessageRouter {
	t.Helper()
	router := transport.NewMessageRouter()
	for _, name := range agents {
		router.RegisterAgent(name, transport.NewLocalTransport())
	}
	return router
}
