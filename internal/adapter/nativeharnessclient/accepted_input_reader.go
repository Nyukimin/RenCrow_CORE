package nativeharnessclient

import (
	"context"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

// AcceptedInputReader is the narrow, private-to-runtime capability for reading
// the canonical Conversation-owned OPS input. Implementations must bind the
// read to the original authenticated user request and must not derive a user
// context from Shiro or from serialized request metadata.
type AcceptedInputReader interface {
	ReadAcceptedInput(context.Context) (conversation.AcceptedOPSInput, error)
}

type acceptedInputReaderContextKey struct{}

// WithAcceptedInputReader carries one request-scoped owner reader through the
// trusted Shiro execution context. The capability is not a request field and
// is never persisted with Task, Action, Harness context or model metadata.
func WithAcceptedInputReader(ctx context.Context, reader AcceptedInputReader) context.Context {
	if ctx == nil || reader == nil {
		return ctx
	}
	return context.WithValue(ctx, acceptedInputReaderContextKey{}, reader)
}

// AcceptedInputReaderFromContext returns the typed owner capability, if the
// authenticated ingress attached one to this execution.
func AcceptedInputReaderFromContext(ctx context.Context) (AcceptedInputReader, bool) {
	if ctx == nil {
		return nil, false
	}
	reader, ok := ctx.Value(acceptedInputReaderContextKey{}).(AcceptedInputReader)
	return reader, ok && reader != nil
}
