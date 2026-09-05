package orchestrator

import (
	"context"
	"testing"
	"time"

	appstore "github.com/Nyukimin/RenCrow_CORE/internal/application/durablestore"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainstore "github.com/Nyukimin/RenCrow_CORE/internal/domain/durablestore"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
)

type stubDurableStoreWorkflow struct {
	result  domainstore.WorkflowResult
	handled bool
}

func (s stubDurableStoreWorkflow) Handle(context.Context, appstore.Input) (domainstore.WorkflowResult, bool, error) {
	return s.result, s.handled, nil
}

func TestDurableStoreWorkflowPreemptsGeneralRouting(t *testing.T) {
	decisions := 0
	mio := &mockMioAgent{decideFunc: func(context.Context, conversation.TurnInput) (routing.Decision, error) {
		decisions++
		return routing.NewDecision(routing.RouteCODE, 1, "should not run"), nil
	}}
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetDurableStoreWorkflow(stubDurableStoreWorkflow{handled: true, result: domainstore.WorkflowResult{
		Status: domainstore.StatusBlocked, Lifecycle: domainstore.LifecycleProposed, Reason: "implementer unavailable",
		Requirement:    domainstore.StorageRequirement{RequirementID: "sr-1", RequestedOutcome: domainstore.OutcomeImplement},
		Classification: domainstore.Classification{Class: domainstore.ClassNewStore, OwnerModule: "RenCrow_GAMES"},
		CreatedAt:      time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}})
	resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{SessionID: "s1", Channel: "viewer", ChatID: "ren", UserMessage: "ゲームDBを実装して"})
	if err != nil {
		t.Fatal(err)
	}
	if decisions != 0 {
		t.Fatalf("general routing called %d times", decisions)
	}
	if resp.Capability != "durable_store_change" || resp.StorageWorkflow == nil || resp.StorageWorkflow.Status != domainstore.StatusBlocked {
		t.Fatalf("unexpected response: %+v", resp)
	}
}
