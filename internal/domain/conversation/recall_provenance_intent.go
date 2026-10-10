package conversation

import "context"

type recallProvenanceIntentKey struct{}

// WithNativeRecallProvenanceIntent marks the native Worker BeginTurn path as
// requiring verified source references for quote-eligible Knowledge summaries.
// It carries no identity or authorization grant.
func WithNativeRecallProvenanceIntent(ctx context.Context) context.Context {
	if ctx == nil {
		return nil
	}
	return context.WithValue(ctx, recallProvenanceIntentKey{}, true)
}

// HasNativeRecallProvenanceIntent reports whether this is the native Worker
// recall path that must verify eligible summary bytes against owner Raw data.
func HasNativeRecallProvenanceIntent(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	requested, _ := ctx.Value(recallProvenanceIntentKey{}).(bool)
	return requested
}
