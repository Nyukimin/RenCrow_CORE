package persistencequeue

import "context"

// SynchronousFallback is a request-scoped transition out of asynchronous
// persistence. Implementations must establish the durability barrier before
// returning a context without the Admission capability.
type SynchronousFallback func(context.Context) (context.Context, error)

type synchronousFallbackContextKey struct{}

// WithSynchronousFallback attaches an explicit fallback capability to ctx.
// Nil contexts and nil callbacks are left unchanged, so they cannot grant a
// fallback capability accidentally.
func WithSynchronousFallback(ctx context.Context, fallback SynchronousFallback) context.Context {
	if ctx == nil || fallback == nil {
		return ctx
	}
	return context.WithValue(ctx, synchronousFallbackContextKey{}, fallback)
}

// SynchronousFallbackFromContext returns the explicitly attached transition
// capability. Context values of other types do not grant this capability.
func SynchronousFallbackFromContext(ctx context.Context) (SynchronousFallback, bool) {
	if ctx == nil {
		return nil, false
	}
	fallback, ok := ctx.Value(synchronousFallbackContextKey{}).(SynchronousFallback)
	return fallback, ok && fallback != nil
}
