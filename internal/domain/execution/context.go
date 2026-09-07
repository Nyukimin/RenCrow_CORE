package execution

import (
	"context"
	"fmt"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type identityContextKey struct{}

// Identity is the owner-selected correlation bound to one policy-mediated
// execution. TaskID and RunID are required and independently issued by the
// Task owner; TraceID is independent and optional only for non-conversation
// owner routes.
type Identity struct {
	TaskID  modulecore.TaskID
	RunID   modulecore.RunID
	TraceID modulecore.TraceID
}

// WithIdentity binds an already selected Task identity to the execution
// context. It never generates or derives one identity from another.
func WithIdentity(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, traceID modulecore.TraceID) (context.Context, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	identity := Identity{TaskID: taskID, RunID: runID, TraceID: traceID}
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	if bound, ok := ctx.Value(identityContextKey{}).(Identity); ok {
		if err := bound.Validate(); err != nil {
			return nil, fmt.Errorf("existing execution identity: %w", err)
		}
		if bound != identity {
			return nil, fmt.Errorf("execution identity is already bound: task_id=%s run_id=%s trace_id=%s", bound.TaskID, bound.RunID, bound.TraceID)
		}
		return ctx, nil
	}
	return context.WithValue(ctx, identityContextKey{}, identity), nil
}

// IdentityFromContext returns the owner-selected execution identity.
func IdentityFromContext(ctx context.Context) (Identity, error) {
	if ctx == nil {
		return Identity{}, fmt.Errorf("execution task identity is required")
	}
	identity, ok := ctx.Value(identityContextKey{}).(Identity)
	if !ok {
		return Identity{}, fmt.Errorf("execution task identity is required")
	}
	if err := identity.Validate(); err != nil {
		return Identity{}, err
	}
	return identity, nil
}

func (i Identity) Validate() error {
	if err := i.TaskID.Validate(); err != nil {
		return fmt.Errorf("task_id: %w", err)
	}
	if err := i.RunID.Validate(); err != nil {
		return fmt.Errorf("run_id: %w", err)
	}
	if i.TraceID != "" {
		if err := i.TraceID.Validate(); err != nil {
			return fmt.Errorf("trace_id: %w", err)
		}
	}
	return nil
}

type boundActionAttemptContextKey struct{}

// BoundActionAttempt binds one already-minted ActionID and AttemptID to the
// execution context so downstream policy mediation reuses them instead of
// issuing new identities.
type BoundActionAttempt struct {
	ActionID  modulecore.ActionID
	AttemptID modulecore.AttemptID
}

// WithBoundActionAttempt binds an owner-minted Action and Attempt pair.
func WithBoundActionAttempt(ctx context.Context, actionID modulecore.ActionID, attemptID modulecore.AttemptID) (context.Context, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	if err := actionID.Validate(); err != nil {
		return nil, fmt.Errorf("action_id: %w", err)
	}
	if err := attemptID.Validate(); err != nil {
		return nil, fmt.Errorf("attempt_id: %w", err)
	}
	bound := BoundActionAttempt{ActionID: actionID, AttemptID: attemptID}
	if existing, ok := ctx.Value(boundActionAttemptContextKey{}).(BoundActionAttempt); ok {
		if existing != bound {
			return nil, fmt.Errorf("bound action attempt is already set: action_id=%s attempt_id=%s", existing.ActionID, existing.AttemptID)
		}
		return ctx, nil
	}
	return context.WithValue(ctx, boundActionAttemptContextKey{}, bound), nil
}

// BoundActionAttemptFromContext returns a bound ActionID and AttemptID when present.
func BoundActionAttemptFromContext(ctx context.Context) (modulecore.ActionID, modulecore.AttemptID, bool) {
	if ctx == nil {
		return "", "", false
	}
	bound, ok := ctx.Value(boundActionAttemptContextKey{}).(BoundActionAttempt)
	if !ok {
		return "", "", false
	}
	if err := bound.ActionID.Validate(); err != nil {
		return "", "", false
	}
	if err := bound.AttemptID.Validate(); err != nil {
		return "", "", false
	}
	return bound.ActionID, bound.AttemptID, true
}
