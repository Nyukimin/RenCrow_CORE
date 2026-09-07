package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	backlogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/backlog"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type developmentEventLogSink struct{ store *viewer.CanonicalEventLog }

func (s developmentEventLogSink) AppendDevelopmentEvent(ctx context.Context, event backlogapp.DevelopmentEvent) error {
	if s.store == nil {
		return fmt.Errorf("development canonical event store is required")
	}
	if ctx == nil {
		return fmt.Errorf("development event context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	outgoing := orchestrator.OrchestratorEvent{EventID: modulecore.NewEventID(), Type: strings.TrimSpace(event.Type), From: "rencrow_core", TraceID: modulecore.TraceID(event.TraceID), Timestamp: event.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")}
	if identity, err := execution.IdentityFromContext(ctx); err == nil {
		if outgoing.TraceID != "" && identity.TraceID != "" && outgoing.TraceID != identity.TraceID {
			return fmt.Errorf("development event trace conflicts with execution identity")
		}
		scope, found := tool.ToolExecutionScopeFromContext(ctx)
		if !found {
			return fmt.Errorf("development execution actor scope is required")
		}
		if err := scope.Validate(); err != nil {
			return fmt.Errorf("development execution actor scope: %w", err)
		}
		outgoing.TaskID, outgoing.RunID = identity.TaskID, identity.RunID
		outgoing.ActorKind, outgoing.ActorID = string(scope.ActorKind), scope.ActorID
		if identity.TraceID != "" {
			outgoing.TraceID = identity.TraceID
		}
	}
	// ArtifactID here is the existing development record/diagnostic reference,
	// not a MessageID. Only a real message owner may issue MessageID.
	content, err := json.Marshal(map[string]any{"unit_id": event.UnitID, "artifact_id": event.ArtifactID, "request_id": event.RequestID, "fields": event.Fields})
	if err != nil {
		return err
	}
	outgoing.Content = string(content)
	return s.store.Append(outgoing)
}
