package main

import (
	"context"
	"fmt"
	"strings"

	appstore "github.com/Nyukimin/RenCrow_CORE/internal/application/durablestore"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

type runtimeDurableStoreWorkflowWritePayload struct {
	Message string `json:"message"`
}

func registerRuntimeDataWriteDurableStoreWorkflow(r *runtimeDataWriteRegistry, workflow orchestrator.DurableStoreWorkflow) error {
	if r == nil || workflow == nil {
		return fmt.Errorf("durable store workflow data write unavailable")
	}
	return r.RegisterWithContract("durable_store_workflow", "handle_storage_intent", dataRecallAccessUser, runtimeDataWriteContract{
		RequiredPayloadFields: []string{"message"},
	}, func(ctx context.Context, request tools.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
		scope, err := runtimeDataWriteOwnerScope(ctx)
		if err != nil {
			return runtimeDataWriteOwnerResult{}, err
		}
		if strings.TrimSpace(scope.AuthenticatedUserID) == "" || !scope.Allows(domaintool.DataScopeUser) {
			return runtimeDataWriteOwnerResult{}, fmt.Errorf("authenticated user scope is required")
		}
		payload, err := decodeRuntimeDurableStoreWorkflowWritePayload(request.Payload)
		if err != nil {
			return runtimeDataWriteOwnerResult{}, err
		}
		result, handled, err := workflow.Handle(ctx, appstore.Input{
			RequestID:   strings.TrimSpace(scope.RequestID),
			TraceID:     runtimeDataWriteDerivedID(runtimeTraceIDPrefix, scope.RequestID),
			RequestedBy: strings.TrimSpace(scope.ActorID),
			UserScope:   strings.TrimSpace(scope.AuthenticatedUserID),
			Message:     payload.Message,
		})
		if err != nil {
			return runtimeDataWriteOwnerResult{}, err
		}
		if !handled {
			return runtimeDataWriteOwnerResult{}, fmt.Errorf("message is not a durable storage intent")
		}
		return runtimeDataWriteOwnerResult{
			SchemaVersion:    "durable-store-workflow/v1",
			MigrationState:   "embedded_current",
			ValidationState:  "owner_validated",
			AuditRef:         result.Requirement.RequirementID,
			IdempotencyKey:   scope.RequestID,
			IdempotentReplay: result.RequestReplay,
			PolicyRevision:   runtimeDataWritePolicyRevision,
		}, nil
	})
}

func decodeRuntimeDurableStoreWorkflowWritePayload(payload map[string]any) (runtimeDurableStoreWorkflowWritePayload, error) {
	if err := validateRuntimeDataWritePayloadKeys(payload, map[string]struct{}{"message": {}}); err != nil {
		return runtimeDurableStoreWorkflowWritePayload{}, err
	}
	var decoded runtimeDurableStoreWorkflowWritePayload
	if err := decodeRuntimeDataWritePayload(payload, &decoded); err != nil {
		return runtimeDurableStoreWorkflowWritePayload{}, err
	}
	decoded.Message = strings.TrimSpace(decoded.Message)
	if decoded.Message == "" {
		return runtimeDurableStoreWorkflowWritePayload{}, fmt.Errorf("message is required")
	}
	return decoded, nil
}
