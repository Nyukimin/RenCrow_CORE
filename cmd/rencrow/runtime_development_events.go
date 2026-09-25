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

// atlasExecutionActorID is the single CORE agent that owns every Atlas Action:
// it is the actor handed to Service.WithExecutionOwners, which creates the
// Atlas Task/Run, and it is the actor the sink has to stamp on the companion
// events written under those Task/Run identities.  Wiring both from one symbol
// keeps a receipt's Action owner and its companion event's actor from drifting.
const atlasExecutionActorID = "shiro"

type developmentEventLogSink struct {
	store *viewer.CanonicalEventLog
	// canonicalActorID names the CORE agent that owns the Atlas Action behind
	// the bound execution identity (atlasExecutionActorID at runtime wiring).
	//
	// A ToolExecutionScope answers who was authorized to reach which data, which
	// is not the same question as who executed a Run.  The owner API binds the
	// authenticated human as a *user* scope, so copying that scope onto a
	// run-scoped event labelled CORE's own Atlas Run as user-executed, and the
	// canonical actor contract rejected every receipt companion with
	// "execution actor_kind must be \"agent\"" (Step18 production E2E).
	canonicalActorID string
}

// canonicalDevelopmentEventID adopts the EventID that an Atlas lifecycle
// transition event already claims.  A receipt stores that EventID as its
// TransitionEventID, so minting a fresh one here would put a real canonical
// event under an id no receipt references and leave the receipt dangling.
// Development projection events claim no EventID and keep minting one at this
// store boundary.
func canonicalDevelopmentEventID(event backlogapp.DevelopmentEvent) (modulecore.EventID, error) {
	if strings.TrimSpace(string(event.EventID)) == "" {
		return modulecore.NewEventID(), nil
	}
	if err := event.EventID.Validate(); err != nil {
		return "", fmt.Errorf("development event %q claims a non-canonical EventID %q: %w", event.Type, event.EventID, err)
	}
	return event.EventID, nil
}

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
	eventID, err := canonicalDevelopmentEventID(event)
	if err != nil {
		return err
	}
	outgoing := orchestrator.OrchestratorEvent{EventID: eventID, Type: strings.TrimSpace(event.Type), From: "rencrow_core", TraceID: modulecore.TraceID(event.TraceID), Timestamp: event.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")}
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
		if strings.TrimSpace(s.canonicalActorID) == "" {
			return fmt.Errorf("development event sink requires the canonical Atlas execution actor")
		}
		outgoing.TaskID, outgoing.RunID = identity.TaskID, identity.RunID
		actorKind, actorID := string(scope.ActorKind), scope.ActorID
		if scope.ActorKind == tool.ActorKindUser {
			// The user authorized the call; CORE executes the Atlas Run under the
			// wired Action owner, so the event carries that agent identity.
			actorKind, actorID = string(tool.ActorKindAgent), s.canonicalActorID
		}
		outgoing.ActorKind, outgoing.ActorID = actorKind, actorID
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
