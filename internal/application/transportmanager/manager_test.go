package transportmanager_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/transportmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	transportpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestManagerOwnsMultipleTransportRequestsWithinOneAttempt(t *testing.T) {
	ctx, actions, action, attempt := transportTestAction(t)
	storeRoot := filepath.Join(t.TempDir(), "transport")
	store, err := transportpersistence.NewJSONLStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	manager := transportmanager.New(store, actions)

	first, err := manager.BeginRequest(ctx, "llm.chat")
	if err != nil {
		t.Fatal(err)
	}
	firstResponse, err := manager.CompleteResponse(ctx, first.RequestID, "provider-response-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.BeginRequest(ctx, "llm.chat")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.FailWithoutResponse(ctx, second.RequestID, "dial failed"); err != nil {
		t.Fatal(err)
	}

	if first.RequestID == second.RequestID || first.Sequence != 1 || second.Sequence != 2 {
		t.Fatalf("requests do not preserve ordered unique identity: first=%#v second=%#v", first, second)
	}
	if first.AttemptID != attempt.AttemptID || second.AttemptID != attempt.AttemptID || first.ActionID != action.ActionID || second.ActionID != action.ActionID {
		t.Fatalf("request lineage escaped one Action Attempt: first=%#v second=%#v", first, second)
	}
	if firstResponse.RequestID != first.RequestID || firstResponse.ResponseID == "" || firstResponse.ExternalRef != "provider-response-1" {
		t.Fatalf("response lineage=%#v", firstResponse)
	}
	requests, err := manager.ListRequests(context.Background(), domaintransport.RequestFilter{AttemptID: attempt.AttemptID})
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[0].Status != domaintransport.RequestStatusResponded || requests[1].Status != domaintransport.RequestStatusFailed {
		t.Fatalf("requests=%#v", requests)
	}
	responses, err := manager.ListResponses(context.Background(), domaintransport.ResponseFilter{AttemptID: attempt.AttemptID})
	if err != nil {
		t.Fatal(err)
	}
	if len(responses) != 1 || responses[0].RequestID != first.RequestID {
		t.Fatalf("responses=%#v", responses)
	}
	reopened, err := transportpersistence.NewJSONLStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	restarted := transportmanager.New(reopened, actions)
	restartedRequests, err := restarted.ListRequests(context.Background(), domaintransport.RequestFilter{AttemptID: attempt.AttemptID})
	if err != nil || len(restartedRequests) != 2 || restartedRequests[0].RequestID != first.RequestID || restartedRequests[1].RequestID != second.RequestID {
		t.Fatalf("restart receipts=%#v err=%v", restartedRequests, err)
	}
}

func TestManagerRejectsUnboundAndForeignAttemptBeforePersistence(t *testing.T) {
	ctx, actions, _, _ := transportTestAction(t)
	store, err := transportpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "transport"))
	if err != nil {
		t.Fatal(err)
	}
	manager := transportmanager.New(store, actions)

	if _, err := manager.BeginRequest(context.Background(), "llm.chat"); err == nil {
		t.Fatal("unbound transport request was accepted")
	}
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := domainexecution.WithIdentity(context.Background(), identity.TaskID, identity.RunID, identity.TraceID)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err = domainexecution.WithChildBoundActionAttempt(foreign, modulecore.NewActionID(), modulecore.NewAttemptID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.BeginRequest(foreign, "llm.chat"); err == nil {
		t.Fatal("foreign Action Attempt was accepted")
	}
	requests, err := manager.ListRequests(context.Background(), domaintransport.RequestFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 0 {
		t.Fatalf("rejected requests persisted=%#v", requests)
	}
}

func TestManagerRestartRecoveryFailsSentRequestWithoutResponse(t *testing.T) {
	ctx, actions, action, attempt := transportTestAction(t)
	storeRoot := filepath.Join(t.TempDir(), "transport")
	store, err := transportpersistence.NewJSONLStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	manager := transportmanager.New(store, actions)
	request, err := manager.BeginRequest(ctx, "llm.generate")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := actions.CompleteAttempt(ctx, action.ActionID, attempt.AttemptID, domainaction.AttemptStatusCancelled, domainaction.StatusCancelled, "process restart"); err != nil {
		t.Fatal(err)
	}
	reopened, err := transportpersistence.NewJSONLStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	restarted := transportmanager.New(reopened, actions)
	recovered, err := restarted.RecoverAfterRestart(context.Background())
	if err != nil || recovered != 1 {
		t.Fatalf("RecoverAfterRestart()=%d err=%v", recovered, err)
	}
	requests, _ := restarted.ListRequests(context.Background(), domaintransport.RequestFilter{})
	responses, _ := restarted.ListResponses(context.Background(), domaintransport.ResponseFilter{})
	if len(requests) != 1 || requests[0].RequestID != request.RequestID || requests[0].Status != domaintransport.RequestStatusFailed || len(responses) != 0 {
		t.Fatalf("restart receipts requests=%#v responses=%#v", requests, responses)
	}
}

func transportTestAction(t *testing.T) (context.Context, *actionmanager.Manager, domainaction.Action, domainaction.Attempt) {
	t.Helper()
	actionStore, err := actionpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatal(err)
	}
	actions := actionmanager.New(actionStore)
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	ctx, err := domainexecution.WithIdentity(context.Background(), taskID, runID, modulecore.NewTraceID())
	if err != nil {
		t.Fatal(err)
	}
	action, attempt, err := actions.CreateAction(ctx, actionmanager.CreateInput{
		TaskID: taskID,
		RunID:  runID,
		Kind:   domainaction.KindLLM,
		Name:   "llm.chat",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = domainexecution.WithBoundActionAttempt(ctx, action.ActionID, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, actions, action, attempt
}
