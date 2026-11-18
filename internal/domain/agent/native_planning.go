package agent

import "context"

// NativePlanningPort is a closed actor-bound dispatch surface. Implementations
// must bind identity from trusted runtime context, never the request envelope.
type NativePlanningPort interface {
	Dispatch(context.Context, string, string) (string, bool, error)
}

func dispatchNativePlanning(ctx context.Context, port NativePlanningPort, actorID, message string) (string, bool, error) {
	if port == nil {
		return "", false, nil
	}
	return port.Dispatch(ctx, actorID, message)
}
