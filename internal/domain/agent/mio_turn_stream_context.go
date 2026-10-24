package agent

import (
	"context"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

// MioTurnStreamGuardUse is the immutable authority identity plus the narrow
// generation-guarded callback used to revalidate pending deltas at RAM fanout.
type MioTurnStreamGuardUse struct {
	OwnerID           string
	SourceVersion     string
	BindingGeneration conversation.BindingGeneration
	Use               func(func() error) error
	// Retain creates a revocable publication qualification while Use is active.
	// The returned ticket must be released after writer claim or drop.
	Retain func() (conversation.PreparedChatTurnPublicationGuard, error)
}

// MioTurnStreamEventHandler receives normalized logical outcome events after
// the classifier has validated their branch and shape. For answer_delta it
// may return an admission ticket, which is completed only after the
// prepared-turn guard is released and is told whether the guarded admission
// succeeded. It is supplied only by a trusted CORE caller that explicitly
// opts into Mio's logical stream.
type MioTurnStreamEventHandler func(event llm.LogicalOutcomeEvent, guard MioTurnStreamGuardUse) (finish func(publish bool), err error)

// MioTurnGenerateRequestAdmissionFactory binds each exact Mio generation to
// its prepared turn and canonical Session owner. It creates a one-shot
// capability; the provider consumes it only after building its outbound HTTP
// request and before dispatch.
type MioTurnGenerateRequestAdmissionFactory func(context.Context, conversation.PreparedChatTurnGuard) (*llm.GenerateRequestAdmission, error)

type mioTurnStreamEventHandlerContextKey struct{}
type mioTurnGenerateRequestAdmissionFactoryContextKey struct{}
type mioTurnGenerateRequestAdmissionContextKey struct{}

// ContextWithMioTurnStreamEventHandler enables the v1 logical outcome stream
// for combined Mio CHAT and fixed-CHAT continuation generations.
func ContextWithMioTurnStreamEventHandler(ctx context.Context, handler MioTurnStreamEventHandler) context.Context {
	if ctx == nil || handler == nil {
		return ctx
	}
	return context.WithValue(ctx, mioTurnStreamEventHandlerContextKey{}, handler)
}

// MioTurnStreamEventHandlerFromContext returns the trusted stream callback, if
// the current turn opted in to logical outcome streaming.
func MioTurnStreamEventHandlerFromContext(ctx context.Context) MioTurnStreamEventHandler {
	if ctx == nil {
		return nil
	}
	handler, _ := ctx.Value(mioTurnStreamEventHandlerContextKey{}).(MioTurnStreamEventHandler)
	return handler
}

// ContextWithMioTurnGenerateRequestAdmissionFactory binds the private Mio
// generation gate for this request context.
func ContextWithMioTurnGenerateRequestAdmissionFactory(ctx context.Context, factory MioTurnGenerateRequestAdmissionFactory) context.Context {
	if ctx == nil || factory == nil {
		return ctx
	}
	return context.WithValue(ctx, mioTurnGenerateRequestAdmissionFactoryContextKey{}, factory)
}

// MioTurnGenerateRequestAdmissionFactoryFromContext returns the trusted
// one-shot request gate factory, when the orchestrator installed one.
func MioTurnGenerateRequestAdmissionFactoryFromContext(ctx context.Context) MioTurnGenerateRequestAdmissionFactory {
	if ctx == nil {
		return nil
	}
	factory, _ := ctx.Value(mioTurnGenerateRequestAdmissionFactoryContextKey{}).(MioTurnGenerateRequestAdmissionFactory)
	return factory
}

// ContextWithMioTurnGenerateRequestAdmission carries a one-shot capability to
// a route classifier that constructs its own exact GenerateRequest.
func ContextWithMioTurnGenerateRequestAdmission(ctx context.Context, admission *llm.GenerateRequestAdmission) context.Context {
	if ctx == nil || admission == nil {
		return ctx
	}
	return context.WithValue(ctx, mioTurnGenerateRequestAdmissionContextKey{}, admission)
}

// MioTurnGenerateRequestAdmissionFromContext returns a capability for the
// next exact Mio GenerateRequest, if one was prepared by the CORE caller.
func MioTurnGenerateRequestAdmissionFromContext(ctx context.Context) *llm.GenerateRequestAdmission {
	if ctx == nil {
		return nil
	}
	admission, _ := ctx.Value(mioTurnGenerateRequestAdmissionContextKey{}).(*llm.GenerateRequestAdmission)
	return admission
}
