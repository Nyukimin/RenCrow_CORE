package conversation

import (
	"context"
	"fmt"
	"strings"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// SessionAudience states whether a resolved canonical Session is public or
// bound to one authenticated User.
type SessionAudience string

const (
	SessionAudiencePublic  SessionAudience = "public"
	SessionAudiencePrivate SessionAudience = "private"
)

// BindingGeneration is the owner-managed invalidation generation for a
// canonical Session binding. It is distinct from session head revisions.
type BindingGeneration uint64

const BindingGenerationUnavailable BindingGeneration = 0

// SessionBinding is the immutable identity and audience resolved for one
// message-processing turn. BindingGeneration is owner-managed revocation state;
// zero means that the owner has not supplied a usable generation yet.
type SessionBinding struct {
	sessionID              modulecore.SessionID
	channelAddress         ChannelAddress
	audience               SessionAudience
	ownerID                string
	bindingGeneration      BindingGeneration
	publicationIncarnation string
}

// NewSessionBinding constructs a validated binding from a canonical resolver
// result. Callers must verify the resolver's proof and Session identity first.
func NewSessionBinding(sessionID modulecore.SessionID, address ChannelAddress, audience SessionAudience, ownerID string, bindingGeneration BindingGeneration, publicationIncarnation ...string) (SessionBinding, error) {
	if len(publicationIncarnation) > 1 {
		return SessionBinding{}, fmt.Errorf("at most one publication incarnation may be supplied")
	}
	binding := SessionBinding{
		sessionID:         sessionID,
		channelAddress:    address,
		audience:          audience,
		ownerID:           ownerID,
		bindingGeneration: bindingGeneration,
	}
	if len(publicationIncarnation) == 1 {
		binding.publicationIncarnation = publicationIncarnation[0]
	}
	if err := binding.Validate(); err != nil {
		return SessionBinding{}, err
	}
	return binding, nil
}

// NewSessionBindingWithPublicationIncarnation binds one turn to the exact
// durable Session publication lifecycle. Canonical Session resolvers use this
// constructor after owner, address, and Session identity have been verified.
func NewSessionBindingWithPublicationIncarnation(sessionID modulecore.SessionID, address ChannelAddress, audience SessionAudience, ownerID string, bindingGeneration BindingGeneration, publicationIncarnation string) (SessionBinding, error) {
	if strings.TrimSpace(publicationIncarnation) == "" || strings.TrimSpace(publicationIncarnation) != publicationIncarnation {
		return SessionBinding{}, fmt.Errorf("SessionBinding publication incarnation is required and normalized")
	}
	return NewSessionBinding(sessionID, address, audience, ownerID, bindingGeneration, publicationIncarnation)
}

// Validate checks the immutable binding fields. Generation zero is valid here
// but is insufficient for any prepared-publication guard.
func (b SessionBinding) Validate() error {
	if err := b.sessionID.Validate(); err != nil {
		return fmt.Errorf("invalid bound session_id: %w", err)
	}
	if err := b.channelAddress.Validate(); err != nil {
		return fmt.Errorf("invalid bound ChannelAddress: %w", err)
	}
	switch b.audience {
	case SessionAudiencePublic:
		if b.ownerID != "" {
			return fmt.Errorf("public SessionBinding must not include an owner")
		}
	case SessionAudiencePrivate:
		if strings.TrimSpace(b.ownerID) == "" || strings.TrimSpace(b.ownerID) != b.ownerID {
			return fmt.Errorf("private SessionBinding requires a normalized owner")
		}
	default:
		return fmt.Errorf("invalid SessionBinding audience %q", b.audience)
	}
	if b.publicationIncarnation != "" {
		if strings.TrimSpace(b.publicationIncarnation) != b.publicationIncarnation {
			return fmt.Errorf("SessionBinding publication incarnation must be normalized")
		}
	}
	return nil
}

// ValidateForPublication requires the durable Session lifecycle identity used
// by event emission and owner-controlled publication claims.
func (b SessionBinding) ValidateForPublication() error {
	if err := b.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(b.publicationIncarnation) == "" {
		return fmt.Errorf("SessionBinding publication incarnation is unavailable")
	}
	return nil
}

func (b SessionBinding) SessionID() modulecore.SessionID { return b.sessionID }

func (b SessionBinding) ChannelAddress() ChannelAddress { return b.channelAddress }

func (b SessionBinding) Audience() SessionAudience { return b.audience }

func (b SessionBinding) OwnerID() string { return b.ownerID }

func (b SessionBinding) BindingGeneration() BindingGeneration { return b.bindingGeneration }

func (b SessionBinding) PublicationIncarnation() string { return b.publicationIncarnation }

type sessionBindingContextKey struct{}

// WithSessionBinding replaces any earlier turn binding in ctx. Invalid
// bindings also replace earlier values and are rejected when read.
func WithSessionBinding(ctx context.Context, binding SessionBinding) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sessionBindingContextKey{}, binding)
}

// SessionBindingFromContext returns only a present, valid turn binding.
func SessionBindingFromContext(ctx context.Context) (SessionBinding, bool) {
	if ctx == nil {
		return SessionBinding{}, false
	}
	binding, ok := ctx.Value(sessionBindingContextKey{}).(SessionBinding)
	if !ok || binding.Validate() != nil {
		return SessionBinding{}, false
	}
	return binding, true
}
