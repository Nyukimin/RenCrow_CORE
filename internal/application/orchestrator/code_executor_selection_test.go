package orchestrator

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestDefaultCodeExecutorInitializesCoderStatus(t *testing.T) {
	executor := NewDefaultCodeExecutor(&mockCoderAgent{}, nil, nil, nil, nil, nil, noopEventEmitter)
	if executor.coderStatus == nil {
		t.Fatal("NewDefaultCodeExecutor must initialize CoderStatus when nil is passed")
	}
}

func TestCodeExecutorGenericRouteRoundRobinsReadyCodersWithoutQualityRanking(t *testing.T) {
	coders := []*mockCoderAgent{
		{response: "coder1"},
		{response: "coder2"},
		{response: "coder3"},
		{response: "coder4"},
	}
	executor := NewDefaultCodeExecutor(
		coders[0], coders[1], coders[2], coders[3], nil, NewCoderStatus(), noopEventEmitter,
	).WithCapabilities([]capability.CoderCapability{
		{Name: "coder1", Quality: 5, Available: true},
		{Name: "coder2", Quality: 0, Available: true},
		{Name: "coder3", Quality: 3, Available: true},
		{Name: "coder4", Quality: 1, Available: true},
	})

	for i, want := range []string{"coder1", "coder2", "coder3", "coder4", "coder1"} {
		req := newOrchestratorTestCodeExecutionRequest(t, modulecore.NewTaskID(), "work", routing.RouteCODE, "session", "test", "chat")
		got, err := executor.ExecuteCode(context.Background(), req)
		if err != nil {
			t.Fatalf("ExecuteCode #%d: %v", i, err)
		}
		if got.Response != want {
			t.Fatalf("ExecuteCode #%d selected %q, want %q", i, got.Response, want)
		}
	}
}

func TestCodeExecutorGenericRouteUsesReadyCoderEvenWhenQualityIsUnknown(t *testing.T) {
	coder1 := &mockCoderAgent{response: "unavailable coder1"}
	coder2 := &mockCoderAgent{response: "ready coder2"}
	executor := NewDefaultCodeExecutor(coder1, coder2, nil, nil, nil, nil, noopEventEmitter).WithCapabilities([]capability.CoderCapability{
		{Name: "coder1", Quality: 5, Available: false},
		{Name: "coder2", Quality: 0, Available: true},
	})
	req := newOrchestratorTestCodeExecutionRequest(t, modulecore.NewTaskID(), "work", routing.RouteCODE, "session", "test", "chat")

	got, err := executor.ExecuteCode(context.Background(), req)
	if err != nil {
		t.Fatalf("ExecuteCode: %v", err)
	}
	if got.Response != "ready coder2" {
		t.Fatalf("selected %q, want ready coder2", got.Response)
	}
}

func TestCodeExecutorExplicitRouteRejectsBusyRequestedCoderWithoutFallback(t *testing.T) {
	status := NewCoderStatus()
	if !status.Acquire("coder2") {
		t.Fatal("fixture failed to occupy coder2")
	}
	executor := NewDefaultCodeExecutor(
		&mockCoderAgent{response: "coder1"},
		&mockCoderAgent{response: "coder2"},
		nil, nil, nil, status, noopEventEmitter,
	)
	req := newOrchestratorTestCodeExecutionRequest(t, modulecore.NewTaskID(), "work", routing.RouteCODE2, "session", "test", "chat")

	_, err := executor.ExecuteCode(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "coder2 is busy") {
		t.Fatalf("ExecuteCode error = %v, want busy error for coder2", err)
	}
	if status.IsBusy("coder1") {
		t.Fatal("explicit CODE2 must not acquire or fall back to coder1")
	}
}

func TestCodeExecutorExplicitRoutesUseTheirNamedCoderWithoutCapabilityFallback(t *testing.T) {
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
		t.Run(tc.name, func(t *testing.T) {
			coders := []*mockCoderAgent{
				{response: "coder1"},
				{response: "coder2"},
				{response: "coder3"},
				{response: "coder4"},
			}
			executor := NewDefaultCodeExecutor(coders[0], coders[1], coders[2], coders[3], nil, nil, noopEventEmitter)
			req := newOrchestratorTestCodeExecutionRequest(t, modulecore.NewTaskID(), "work", tc.route, "session", "test", "chat")
			got, err := executor.ExecuteCode(context.Background(), req)
			if err != nil {
				t.Fatalf("ExecuteCode: %v", err)
			}
			if got.Response != tc.name {
				t.Fatalf("response=%q, want %s", got.Response, tc.name)
			}
		})
	}
}

func TestCodeExecutorExplicitRouteRejectsCapabilityUnavailableSlot(t *testing.T) {
	status := NewCoderStatus()
	executor := NewDefaultCodeExecutor(
		&mockCoderAgent{response: "coder1"},
		&mockCoderAgent{response: "coder2"}, nil, nil, nil, status, noopEventEmitter,
	).WithCapabilities([]capability.CoderCapability{
		{Name: "coder1", Available: true},
		{Name: "coder2", Available: false},
	})
	req := newOrchestratorTestCodeExecutionRequest(t, modulecore.NewTaskID(), "work", routing.RouteCODE2, "session", "test", "chat")

	_, err := executor.ExecuteCode(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "coder2 is unavailable") {
		t.Fatalf("ExecuteCode error = %v, want coder2 unavailable", err)
	}
	if status.IsBusy("coder1") || status.IsBusy("coder2") {
		t.Fatal("unavailable explicit route must not occupy or substitute a coder")
	}
}

func TestCodeExecutorParallelGenericTasksOccupyDifferentCodersAndReleaseOnError(t *testing.T) {
	gate := make(chan struct{})
	entered := make(chan string, 2)
	status := NewCoderStatus()
	coder1 := &blockingSelectionCoder{name: "coder1", entered: entered, release: gate}
	coder2 := &blockingSelectionCoder{name: "coder2", entered: entered, release: gate}
	executor := NewDefaultCodeExecutor(coder1, coder2, nil, nil, nil, status, noopEventEmitter).WithCapabilities([]capability.CoderCapability{
		{Name: "coder1", Available: true},
		{Name: "coder2", Available: true},
	})

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	requests := []CodeExecutionRequest{
		newOrchestratorTestCodeExecutionRequest(t, modulecore.NewTaskID(), "work", routing.RouteCODE, "session", "test", "chat"),
		newOrchestratorTestCodeExecutionRequest(t, modulecore.NewTaskID(), "work", routing.RouteCODE, "session", "test", "chat"),
	}
	for _, req := range requests {
		wg.Add(1)
		go func(req CodeExecutionRequest) {
			defer wg.Done()
			_, err := executor.ExecuteCode(context.Background(), req)
			errCh <- err
		}(req)
	}

	selected := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case name := <-entered:
			if selected[name] {
				t.Fatalf("two active tasks selected %s", name)
			}
			selected[name] = true
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for both generic tasks to enter their selected coders")
		}
	}
	if len(selected) != 2 {
		t.Fatalf("active coder selections=%v, want two distinct coders", selected)
	}
	close(gate)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("ExecuteCode: %v", err)
		}
	}
	for _, name := range []string{"coder1", "coder2"} {
		if status.IsBusy(name) {
			t.Fatalf("%s remained busy after task completion", name)
		}
	}
}

type blockingSelectionCoder struct {
	name    string
	entered chan<- string
	release <-chan struct{}
}

func (c *blockingSelectionCoder) Generate(ctx context.Context, _ domainconversation.TurnInput, _ string) (string, error) {
	c.entered <- c.name
	select {
	case <-c.release:
		return c.name, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
