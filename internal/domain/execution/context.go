package execution

import (
	"context"
	"fmt"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type identityContextKey struct{}

// Identity is the owner-selected correlation bound to one policy-mediated
// execution. TaskID is required; TraceID is independent and optional only for
// non-conversation owner routes.
type Identity struct {
	TaskID  modulecore.TaskID
	TraceID modulecore.TraceID
}

// WithIdentity binds an already selected Task identity to the execution
// context. It never generates or derives one identity from another.
func WithIdentity(ctx context.Context, taskID modulecore.TaskID, traceID modulecore.TraceID) (context.Context, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	identity := Identity{TaskID: taskID, TraceID: traceID}
	if err := identity.Validate(); err != nil {
		return nil, err
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
	if i.TraceID != "" {
		if err := i.TraceID.Validate(); err != nil {
			return fmt.Errorf("trace_id: %w", err)
		}
	}
	return nil
}
