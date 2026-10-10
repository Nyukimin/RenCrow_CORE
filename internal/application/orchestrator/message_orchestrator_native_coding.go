package orchestrator

import (
	"context"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
)

// NativeCodingAdmission is the admission of the execution profile
// shiro_native_coding_v1 (docs 03 and 05, RenCrow_Harness sections). It exists
// only when authenticated configuration enabled the profile. Callers pass the
// typed turn for admission checks, but its message, model output and client
// requests never choose or enable the profile.
//
// AdmitNativeCoding returns nil when the work can be handed to RenCrow_Harness
// (the Harness is available and the build CORE pinned, the workspace and the
// binding are supplied) and a typed refusal otherwise
// (agent.ErrNativeCodingRejected or agent.ErrNativeCodingBlocked). A refusal
// ends the turn: the old routes are never a fallback.
type NativeCodingAdmission interface {
	AdmitNativeCoding(ctx context.Context, input domainconversation.TurnInput) error
}

// AdmitNativeCoding is the one place in CORE that gives a turn the backend
// selection shiro_native_coding_v1 (an architecture test fixes the callers of
// TurnInput.WithBackendSelection). It selects a new OPS turn when the profile
// is enabled, from the configured admission only. The MessageOrchestrator and
// authenticated Agent OPS ingress share this function. Other routes (CHAT,
// CODE, WILD, ANALYZE, ...) and turns without an admission are returned
// unchanged, so they keep today's behavior.
func AdmitNativeCoding(ctx context.Context, admission NativeCodingAdmission, input domainconversation.TurnInput, route routing.Route) (domainconversation.TurnInput, error) {
	if admission == nil || route != routing.RouteOPS {
		return input, nil
	}
	if input.BackendSelection() != domainconversation.BackendSelectionNone {
		return input, nil
	}
	if err := admission.AdmitNativeCoding(ctx, input); err != nil {
		return input, err
	}
	return input.WithBackendSelection(domainconversation.BackendShiroNativeCodingV1)
}

// SetNativeCodingAdmission installs the admission of shiro_native_coding_v1.
// Without it no turn is ever selected and every route runs as before.
func (o *MessageOrchestrator) SetNativeCodingAdmission(admission NativeCodingAdmission) {
	if o.routeDispatcher != nil {
		o.routeDispatcher.SetNativeCodingAdmission(admission)
	}
}

// SetNativeCodingAdmission installs the admission on the route dispatcher.
func (d *messageRouteDispatcher) SetNativeCodingAdmission(admission NativeCodingAdmission) {
	d.nativeCoding = admission
}
