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

func (o *MessageOrchestrator) handleDurableStore(ctx context.Context, req ProcessMessageRequest, sess *session.Session, input domainconversation.TurnInput, jobID modulecore.TaskID) (ProcessMessageResponse, bool, error) {
	if o.durableStoreWorkflow == nil || strings.HasPrefix(strings.TrimSpace(req.UserMessage), "/") {
		return ProcessMessageResponse{}, false, nil
	}
	result, handled, err := o.durableStoreWorkflow.Handle(ctx, durableStoreInput(req))
	if err != nil {
		return ProcessMessageResponse{}, handled, fmt.Errorf("durable store workflow failed: %w", err)
	}
	if !handled {
		return ProcessMessageResponse{}, false, nil
	}
	routed := input.WithRoute(routing.RouteCHAT)
	if err := o.sessions.SaveCompletedTurnInput(ctx, sess, routed); err != nil {
		return ProcessMessageResponse{}, true, err
	}
	response := formatDurableStoreResult(result)
	o.events.Emit("storage.requirement.received", "mio", "storage", result.Requirement.RequirementID, string(routing.RouteCHAT), jobID.String(), req.SessionID, req.Channel, req.ChatID)
	if result.Classification.OwnerModule != "" {
		o.events.Emit("storage.owner.resolved", "storage", result.Classification.OwnerModule, result.Classification.Reason, string(routing.RouteCHAT), jobID.String(), req.SessionID, req.Channel, req.ChatID)
	}
	emitDurableStoreProposalEvents(o.events.Emit, result, jobID.String(), req)
	o.events.Emit(durableStoreEvent(result), "mio", "worker", response, string(routing.RouteCHAT), jobID.String(), req.SessionID, req.Channel, req.ChatID)
	return durableStoreResponse(response, result, jobID), true, nil
}

func (o *DistributedOrchestrator) handleDurableStore(ctx context.Context, req ProcessMessageRequest, sess *session.Session, input domainconversation.TurnInput, jobID modulecore.TaskID) (ProcessMessageResponse, bool, error) {
	if o.durableStoreWorkflow == nil || strings.HasPrefix(strings.TrimSpace(req.UserMessage), "/") {
		return ProcessMessageResponse{}, false, nil
	}
	result, handled, err := o.durableStoreWorkflow.Handle(ctx, durableStoreInput(req))
	if err != nil {
		return ProcessMessageResponse{}, handled, fmt.Errorf("durable store workflow failed: %w", err)
	}
	if !handled {
		return ProcessMessageResponse{}, false, nil
	}
	routed := input.WithRoute(routing.RouteCHAT)
	if err := o.sessions.SaveCompletedTurnInput(ctx, sess, routed); err != nil {
		return ProcessMessageResponse{}, true, err
	}
	response := formatDurableStoreResult(result)
	o.emit("storage.requirement.received", "mio", "storage", result.Requirement.RequirementID, string(routing.RouteCHAT), jobID.String(), req.SessionID, req.Channel, req.ChatID)
	if result.Classification.OwnerModule != "" {
		o.emit("storage.owner.resolved", "storage", result.Classification.OwnerModule, result.Classification.Reason, string(routing.RouteCHAT), jobID.String(), req.SessionID, req.Channel, req.ChatID)
	}
	emitDurableStoreProposalEvents(o.emit, result, jobID.String(), req)
	o.emit(durableStoreEvent(result), "mio", "worker", response, string(routing.RouteCHAT), jobID.String(), req.SessionID, req.Channel, req.ChatID)
	return durableStoreResponse(response, result, jobID), true, nil
}

func emitDurableStoreProposalEvents(emit func(string, string, string, string, string, string, string, string, string), result domainstore.WorkflowResult, jobID string, req ProcessMessageRequest) {
	if result.Proposal == nil {
		return
	}
	emit("storage.proposal.created", result.Classification.OwnerModule, "storage", result.Proposal.ProposalID, string(routing.RouteCHAT), jobID, req.SessionID, req.Channel, req.ChatID)
	event := "storage.proposal.rejected"
	if result.Proposal.ValidationPassed {
		event = "storage.proposal.validated"
	}
	emit(event, "storage", "worker", result.Reason, string(routing.RouteCHAT), jobID, req.SessionID, req.Channel, req.ChatID)
}

func durableStoreInput(req ProcessMessageRequest) appstore.Input {
	requestedBy := strings.TrimSpace(req.ChatID)
	if requestedBy == "" {
		requestedBy = "authenticated_user"
	}
	return appstore.Input{RequestID: req.MessageID, TraceID: req.TraceID, RequestedBy: requestedBy, UserScope: req.Channel + ":" + req.ChatID, Message: req.UserMessage}
}

func durableStoreResponse(response string, result domainstore.WorkflowResult, jobID modulecore.TaskID) ProcessMessageResponse {
	copyResult := result
	return ProcessMessageResponse{Response: response, Route: routing.RouteCHAT, Confidence: 1, JobID: jobID.String(), Capability: durableStoreCapability, StorageWorkflow: &copyResult}
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
