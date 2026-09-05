package orchestrator

import (
	"context"
	"fmt"
	"strings"

	appstore "github.com/Nyukimin/RenCrow_CORE/internal/application/durablestore"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainstore "github.com/Nyukimin/RenCrow_CORE/internal/domain/durablestore"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const durableStoreCapability = "durable_store_change"

type DurableStoreWorkflow interface {
	Handle(context.Context, appstore.Input) (domainstore.WorkflowResult, bool, error)
}

func (o *MessageOrchestrator) SetDurableStoreWorkflow(workflow DurableStoreWorkflow) {
	o.durableStoreWorkflow = workflow
}

func (o *DistributedOrchestrator) SetDurableStoreWorkflow(workflow DurableStoreWorkflow) {
	o.durableStoreWorkflow = workflow
}

func evaluateDurableStore(ctx context.Context, workflow DurableStoreWorkflow, req ProcessMessageRequest) (domainstore.WorkflowResult, bool, error) {
	if workflow == nil || strings.HasPrefix(strings.TrimSpace(req.UserMessage), "/") {
		return domainstore.WorkflowResult{}, false, nil
	}
	result, handled, err := workflow.Handle(ctx, durableStoreInput(req))
	if err != nil {
		return domainstore.WorkflowResult{}, handled, fmt.Errorf("durable store workflow failed: %w", err)
	}
	return result, handled, nil
}

func (o *MessageOrchestrator) completeDurableStore(ctx context.Context, req ProcessMessageRequest, sess *session.Session, input domainconversation.TurnInput, taskID modulecore.TaskID, result domainstore.WorkflowResult) (ProcessMessageResponse, error) {
	routed := input.WithRoute(routing.RouteCHAT)
	if err := o.sessions.SaveCompletedTurnInput(ctx, sess, routed); err != nil {
		return ProcessMessageResponse{}, err
	}
	response := formatDurableStoreResult(result)
	o.events.Emit("storage.requirement.received", "mio", "storage", result.Requirement.RequirementID, string(routing.RouteCHAT), taskID.String(), req.SessionID, req.Channel, req.ChatID)
	if result.Classification.OwnerModule != "" {
		o.events.Emit("storage.owner.resolved", "storage", result.Classification.OwnerModule, result.Classification.Reason, string(routing.RouteCHAT), taskID.String(), req.SessionID, req.Channel, req.ChatID)
	}
	emitDurableStoreProposalEvents(o.events.Emit, result, taskID.String(), req)
	o.events.Emit(durableStoreEvent(result), "mio", "worker", response, string(routing.RouteCHAT), taskID.String(), req.SessionID, req.Channel, req.ChatID)
	return durableStoreResponse(response, result, taskID), nil
}

func (o *DistributedOrchestrator) completeDurableStore(ctx context.Context, req ProcessMessageRequest, sess *session.Session, input domainconversation.TurnInput, taskID modulecore.TaskID, result domainstore.WorkflowResult) (ProcessMessageResponse, error) {
	routed := input.WithRoute(routing.RouteCHAT)
	if err := o.sessions.SaveCompletedTurnInput(ctx, sess, routed); err != nil {
		return ProcessMessageResponse{}, err
	}
	response := formatDurableStoreResult(result)
	o.emit("storage.requirement.received", "mio", "storage", result.Requirement.RequirementID, string(routing.RouteCHAT), taskID.String(), req.SessionID, req.Channel, req.ChatID)
	if result.Classification.OwnerModule != "" {
		o.emit("storage.owner.resolved", "storage", result.Classification.OwnerModule, result.Classification.Reason, string(routing.RouteCHAT), taskID.String(), req.SessionID, req.Channel, req.ChatID)
	}
	emitDurableStoreProposalEvents(o.emit, result, taskID.String(), req)
	o.emit(durableStoreEvent(result), "mio", "worker", response, string(routing.RouteCHAT), taskID.String(), req.SessionID, req.Channel, req.ChatID)
	return durableStoreResponse(response, result, taskID), nil
}

func emitDurableStoreProposalEvents(emit func(string, string, string, string, string, string, string, string, string), result domainstore.WorkflowResult, taskID string, req ProcessMessageRequest) {
	if result.Proposal == nil {
		return
	}
	emit("storage.proposal.created", result.Classification.OwnerModule, "storage", result.Proposal.ProposalID, string(routing.RouteCHAT), taskID, req.SessionID, req.Channel, req.ChatID)
	event := "storage.proposal.rejected"
	if result.Proposal.ValidationPassed {
		event = "storage.proposal.validated"
	}
	emit(event, "storage", "worker", result.Reason, string(routing.RouteCHAT), taskID, req.SessionID, req.Channel, req.ChatID)
}

func durableStoreInput(req ProcessMessageRequest) appstore.Input {
	requestedBy := strings.TrimSpace(req.ChatID)
	if requestedBy == "" {
		requestedBy = "authenticated_user"
	}
	return appstore.Input{RequestID: req.MessageID, TraceID: req.TraceID, RequestedBy: requestedBy, UserScope: req.Channel + ":" + req.ChatID, Message: req.UserMessage}
}

func durableStoreResponse(response string, result domainstore.WorkflowResult, taskID modulecore.TaskID) ProcessMessageResponse {
	copyResult := result
	return ProcessMessageResponse{Response: response, Route: routing.RouteCHAT, Confidence: 1, TaskID: taskID.String(), Capability: durableStoreCapability, StorageWorkflow: &copyResult}
}

func durableStoreEvent(result domainstore.WorkflowResult) string {
	switch result.Status {
	case domainstore.StatusCompleted:
		if result.Lifecycle == domainstore.LifecycleActive {
			return "storage.activated"
		}
		return "storage.validated"
	case domainstore.StatusRejected:
		return "storage.rejected"
	default:
		return "storage.blocked"
	}
}

func formatDurableStoreResult(result domainstore.WorkflowResult) string {
	return fmt.Sprintf("永続Store判定\nstatus: %s\nlifecycle: %s\nrequirement_id: %s\noutcome: %s\nclass: %s\nchange_class: %s\nowner: %s\nstore_id: %s\nreason_code: %s\nreason: %s",
		result.Status, result.Lifecycle, result.Requirement.RequirementID, result.Requirement.RequestedOutcome,
		result.Classification.Class, result.Classification.ChangeClass, result.Classification.OwnerModule, result.Classification.StoreID, result.ReasonCode, result.Reason)
}
