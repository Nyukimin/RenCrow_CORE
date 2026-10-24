package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainsession "github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type canonicalPublicationBindingReader interface {
	CurrentPublicationBinding(context.Context, string) (domainsession.CanonicalSessionPublicationBinding, error)
	UsePublicationBinding(context.Context, domainsession.CanonicalSessionPublicationBinding, func() error) error
}

type canonicalReservedV2ErrorPublicationBindingReader interface {
	CurrentReservedV2ErrorPublicationBinding(context.Context, string, string, conversation.ChannelAddress) (domainsession.CanonicalSessionPublicationBinding, error)
	UseReservedV2ErrorPublicationBinding(context.Context, domainsession.CanonicalSessionPublicationBinding, func() error) error
}

// runtimeSessionVisibility resolves publication identity from the warmed
// canonical Session projection. It intentionally keeps no SessionID keyed
// owner cache: same-ID recreation must immediately invalidate old events.
type runtimeSessionVisibility struct {
	repository orchestrator.SessionRepository
	reader     canonicalPublicationBindingReader
}

func newRuntimeSessionVisibility(repository orchestrator.SessionRepository) *runtimeSessionVisibility {
	reader, _ := repository.(canonicalPublicationBindingReader)
	return &runtimeSessionVisibility{repository: repository, reader: reader}
}

// OwnerID resolves only the current canonical owner projection and never
// retains a SessionID keyed result across deletion or recreation.
func (v *runtimeSessionVisibility) OwnerID(ctx context.Context, sessionID string) (string, error) {
	if ctx == nil {
		return "", errors.New("canonical session visibility requires a context")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(sessionID) != sessionID || modulecore.SessionID(sessionID).Validate() != nil {
		return "", errors.New("canonical session visibility requires a valid session ID")
	}
	if v == nil || v.reader == nil {
		return "", errors.New("canonical session publication reader is unavailable")
	}
	binding, err := v.reader.CurrentPublicationBinding(ctx, sessionID)
	if err != nil {
		return "", fmt.Errorf("read current canonical Session owner: %w", err)
	}
	if err := binding.Validate(); err != nil {
		return "", fmt.Errorf("current canonical Session owner projection is invalid: %w", err)
	}
	return binding.OwnerID(), nil
}

// CanAccessSession preserves the public/session-owner predicate used by other
// runtime projections, with each call resolved from the current canonical
// projection instead of a stale cache.
func (v *runtimeSessionVisibility) CanAccessSession(ctx context.Context, sessionID, authenticatedOwnerID string) (bool, error) {
	ownerID, err := v.OwnerID(ctx, sessionID)
	if err != nil {
		return false, err
	}
	if authenticatedOwnerID == "" {
		return ownerID == "", nil
	}
	if strings.TrimSpace(authenticatedOwnerID) != authenticatedOwnerID {
		return false, nil
	}
	return ownerID != "" && ownerID == authenticatedOwnerID, nil
}

// ResolveEvent returns the exact current canonical binding only when the event
// carries that Session lifecycle and the caller's owner scope matches its
// audience. Legacy events without an incarnation fail closed.
func (v *runtimeSessionVisibility) ResolveEvent(ctx context.Context, event orchestrator.OrchestratorEvent, authenticatedOwnerID string) (domainsession.CanonicalSessionPublicationBinding, bool, error) {
	if ctx == nil {
		return domainsession.CanonicalSessionPublicationBinding{}, false, errors.New("canonical session visibility requires a context")
	}
	if err := ctx.Err(); err != nil {
		return domainsession.CanonicalSessionPublicationBinding{}, false, err
	}
	if v == nil || v.reader == nil {
		return domainsession.CanonicalSessionPublicationBinding{}, false, errors.New("canonical session publication reader is unavailable")
	}
	if event.SessionID.Validate() != nil || strings.TrimSpace(event.PublicationIncarnation) == "" || domainsession.ValidatePublicationIncarnation(event.PublicationIncarnation) != nil {
		return domainsession.CanonicalSessionPublicationBinding{}, false, nil
	}
	if strings.TrimSpace(authenticatedOwnerID) != authenticatedOwnerID {
		return domainsession.CanonicalSessionPublicationBinding{}, false, nil
	}
	binding, err := v.reader.CurrentPublicationBinding(ctx, string(event.SessionID))
	if err != nil {
		return domainsession.CanonicalSessionPublicationBinding{}, false, fmt.Errorf("read current canonical Session publication binding: %w", err)
	}
	if err := binding.Validate(); err != nil {
		return domainsession.CanonicalSessionPublicationBinding{}, false, fmt.Errorf("current canonical Session publication binding is invalid: %w", err)
	}
	address := binding.ChannelAddress()
	if binding.SessionID() != event.SessionID || binding.PublicationIncarnation() != event.PublicationIncarnation ||
		event.Channel != address.ChannelType() || event.ChannelAddress != address.ExternalConversationID() {
		return domainsession.CanonicalSessionPublicationBinding{}, false, nil
	}
	if authenticatedOwnerID == "" {
		return binding, binding.Audience() == conversation.SessionAudiencePublic, nil
	}
	return binding, binding.Audience() == conversation.SessionAudiencePrivate && binding.OwnerID() == authenticatedOwnerID, nil
}

// ResolvePrivateTerminalErrorEvent adds the reservation-specific authority
// needed for a failed response-first turn before its Session has activated.
// Normal events continue through ResolveEvent and ordinary publication APIs.
func (v *runtimeSessionVisibility) ResolvePrivateTerminalErrorEvent(ctx context.Context, event orchestrator.OrchestratorEvent, authenticatedOwnerID string) (domainsession.CanonicalSessionPublicationBinding, bool, bool, error) {
	if ctx == nil {
		return domainsession.CanonicalSessionPublicationBinding{}, false, false, errors.New("canonical session visibility requires a context")
	}
	if v == nil || v.reader == nil {
		return domainsession.CanonicalSessionPublicationBinding{}, false, false, errors.New("canonical session publication reader is unavailable")
	}
	if !isAuthenticatedPrivateMioTerminalError(event, authenticatedOwnerID) {
		return domainsession.CanonicalSessionPublicationBinding{}, false, false, nil
	}
	address, err := conversation.NewChannelAddress("viewer", "cmd:"+authenticatedOwnerID)
	if err != nil {
		return domainsession.CanonicalSessionPublicationBinding{}, false, false, err
	}

	// Existing canonical owner-bound Sessions keep the ordinary visibility
	// contract. Reserved Sessions fall through to the error-only authority.
	binding, allowed, resolveErr := v.ResolveEvent(ctx, event, authenticatedOwnerID)
	if resolveErr == nil && allowed {
		return binding, true, false, nil
	}
	if err := ctx.Err(); err != nil {
		return domainsession.CanonicalSessionPublicationBinding{}, false, false, err
	}
	reader, ok := v.repository.(canonicalReservedV2ErrorPublicationBindingReader)
	if !ok {
		return domainsession.CanonicalSessionPublicationBinding{}, false, false, nil
	}
	binding, err = reader.CurrentReservedV2ErrorPublicationBinding(ctx, string(event.SessionID), authenticatedOwnerID, address)
	if err != nil {
		if ctx.Err() != nil {
			return domainsession.CanonicalSessionPublicationBinding{}, false, false, ctx.Err()
		}
		return domainsession.CanonicalSessionPublicationBinding{}, false, false, nil
	}
	if !bindingMatchesTerminalError(binding, event, authenticatedOwnerID) {
		return domainsession.CanonicalSessionPublicationBinding{}, false, false, nil
	}
	return binding, true, true, nil
}

// IsCurrent makes the final visibility decision under the repository's
// short, read-locked binding claim after route policy has been checked. This
// closes the race where a Session is deleted or rekeyed during that check.
func (v *runtimeSessionVisibility) IsCurrent(ctx context.Context, expected domainsession.CanonicalSessionPublicationBinding) (bool, error) {
	if ctx == nil {
		return false, errors.New("canonical session visibility requires a context")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if v == nil || v.reader == nil {
		return false, errors.New("canonical session publication reader is unavailable")
	}
	err := v.reader.UsePublicationBinding(ctx, expected, func() error { return nil })
	if err == nil {
		return true, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, ctxErr
	}
	// A failed claim means the session was deleted, rekeyed, or its canonical
	// projection became unavailable while policy was being evaluated.
	return false, nil
}

// IsCurrentPrivateTerminalError revalidates a reservation-bound error through
// its narrowly scoped repository claim. Existing owner-bound Sessions use the
// normal current-binding check.
func (v *runtimeSessionVisibility) IsCurrentPrivateTerminalError(ctx context.Context, expected domainsession.CanonicalSessionPublicationBinding) (bool, error) {
	current, err := v.IsCurrent(ctx, expected)
	if err == nil && current {
		return true, nil
	}
	if ctx == nil {
		return false, errors.New("canonical session visibility requires a context")
	}
	if v == nil {
		return false, errors.New("canonical session visibility is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	reader, ok := v.repository.(canonicalReservedV2ErrorPublicationBindingReader)
	if !ok {
		return false, nil
	}
	claimed := false
	err = reader.UseReservedV2ErrorPublicationBinding(ctx, expected, func() error {
		claimed = true
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil
	}
	return claimed, nil
}

func bindingMatchesTerminalError(binding domainsession.CanonicalSessionPublicationBinding, event orchestrator.OrchestratorEvent, ownerID string) bool {
	return binding.Validate() == nil && binding.SessionID() == event.SessionID && binding.OwnerID() == ownerID &&
		binding.Audience() == conversation.SessionAudiencePrivate &&
		binding.ChannelAddress().ChannelType() == event.Channel &&
		binding.ChannelAddress().ExternalConversationID() == event.ChannelAddress &&
		binding.PublicationIncarnation() == event.PublicationIncarnation
}
