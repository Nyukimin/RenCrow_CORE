package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/boardcommunication"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
)

var ErrBoardCommunicationConnectionUnavailable = errors.New("BOARD communication connection unavailable")

type boardConnectionPrincipalPinStore interface {
	ReadBoardConnectionPinForPrincipal(context.Context, string) (eventstore.BoardConnectionPin, bool, error)
	PinBoardConnectionForPrincipal(context.Context, eventstore.BoardConnectionPin) (eventstore.BoardConnectionPin, error)
}

type boardConnectionSettings struct {
	OwnerID                string
	ExpectedPrincipalID    string
	AllowedChannelIDs      []string
	CutoverAt              time.Time
	UsePrincipalScopedPins bool
	Transport              boardcommunication.Options
}

func openBoardCommunicationConnection(ctx context.Context, store *eventstore.SQLiteStore, settings boardConnectionSettings) (*boardcommunication.Client, eventstore.BoardConnectionPin, error) {
	if ctx == nil || store == nil || !validBoardConnectionSettings(settings) {
		return nil, eventstore.BoardConnectionPin{}, ErrBoardCommunicationConnectionUnavailable
	}
	pin, found, err := readBoardConnectionPinForPrincipal(ctx, store, settings.ExpectedPrincipalID, settings.UsePrincipalScopedPins)
	if err != nil {
		return nil, eventstore.BoardConnectionPin{}, fmt.Errorf("read durable BOARD connection pin: %w", err)
	}
	if found {
		if pin.OwnerID != settings.OwnerID || pin.PrincipalID != settings.ExpectedPrincipalID || !pin.CutoverAt.Equal(settings.CutoverAt) {
			return nil, eventstore.BoardConnectionPin{}, ErrBoardCommunicationConnectionUnavailable
		}
		client, err := newPinnedBoardClient(settings.Transport, pin)
		if err != nil {
			return nil, eventstore.BoardConnectionPin{}, err
		}
		identity, err := client.ReadConnection(ctx)
		if err != nil {
			client.Close()
			return nil, eventstore.BoardConnectionPin{}, fmt.Errorf("read bound BOARD connection identity: %w", err)
		}
		if identity.PrincipalID != pin.PrincipalID || identity.JournalInstanceID != pin.JournalInstanceID {
			client.Close()
			return nil, eventstore.BoardConnectionPin{}, ErrBoardCommunicationConnectionUnavailable
		}
		if err := verifyBoardConnectionContexts(ctx, client, settings.AllowedChannelIDs); err != nil {
			client.Close()
			return nil, eventstore.BoardConnectionPin{}, err
		}
		return client, pin, nil
	}

	if settings.Transport.ExpectedConnection != (boardcommunication.ConnectionIdentity{}) {
		return nil, eventstore.BoardConnectionPin{}, ErrBoardCommunicationConnectionUnavailable
	}
	identity, err := boardcommunication.DiscoverConnection(ctx, settings.Transport)
	if err != nil {
		return nil, eventstore.BoardConnectionPin{}, fmt.Errorf("discover BOARD connection identity: %w", err)
	}
	if identity.PrincipalID != settings.ExpectedPrincipalID {
		return nil, eventstore.BoardConnectionPin{}, ErrBoardCommunicationConnectionUnavailable
	}
	pin = eventstore.BoardConnectionPin{
		OwnerID:           settings.OwnerID,
		JournalInstanceID: identity.JournalInstanceID,
		PrincipalID:       identity.PrincipalID,
		CutoverAt:         settings.CutoverAt,
	}
	client, err := newPinnedBoardClient(settings.Transport, pin)
	if err != nil {
		return nil, eventstore.BoardConnectionPin{}, err
	}
	if err := verifyBoardConnectionContexts(ctx, client, settings.AllowedChannelIDs); err != nil {
		client.Close()
		return nil, eventstore.BoardConnectionPin{}, err
	}
	pinned, err := pinBoardConnectionForPrincipal(ctx, store, pin, settings.UsePrincipalScopedPins)
	if err != nil {
		client.Close()
		return nil, eventstore.BoardConnectionPin{}, fmt.Errorf("persist BOARD connection pin: %w", err)
	}
	if pinned.OwnerID != pin.OwnerID || pinned.JournalInstanceID != pin.JournalInstanceID ||
		pinned.PrincipalID != pin.PrincipalID || !pinned.CutoverAt.Equal(pin.CutoverAt) {
		client.Close()
		return nil, eventstore.BoardConnectionPin{}, ErrBoardCommunicationConnectionUnavailable
	}
	return client, pinned, nil
}

func readBoardConnectionPinForPrincipal(ctx context.Context, store interface {
	ReadBoardConnectionPin(context.Context) (eventstore.BoardConnectionPin, bool, error)
}, principalID string, usePrincipalScopedPins bool) (eventstore.BoardConnectionPin, bool, error) {
	if store == nil {
		return eventstore.BoardConnectionPin{}, false, ErrBoardCommunicationConnectionUnavailable
	}
	if !usePrincipalScopedPins {
		return store.ReadBoardConnectionPin(ctx)
	}
	if scoped, ok := store.(boardConnectionPrincipalPinStore); ok {
		return scoped.ReadBoardConnectionPinForPrincipal(ctx, principalID)
	}
	return eventstore.BoardConnectionPin{}, false, ErrBoardCommunicationConnectionUnavailable
}

func pinBoardConnectionForPrincipal(ctx context.Context, store interface {
	PinBoardConnection(context.Context, eventstore.BoardConnectionPin) (eventstore.BoardConnectionPin, error)
}, pin eventstore.BoardConnectionPin, usePrincipalScopedPins bool) (eventstore.BoardConnectionPin, error) {
	if store == nil {
		return eventstore.BoardConnectionPin{}, ErrBoardCommunicationConnectionUnavailable
	}
	if !usePrincipalScopedPins {
		return store.PinBoardConnection(ctx, pin)
	}
	if scoped, ok := store.(boardConnectionPrincipalPinStore); ok {
		return scoped.PinBoardConnectionForPrincipal(ctx, pin)
	}
	return eventstore.BoardConnectionPin{}, ErrBoardCommunicationConnectionUnavailable
}

func validBoardConnectionSettings(settings boardConnectionSettings) bool {
	if settings.OwnerID == "" || strings.TrimSpace(settings.OwnerID) != settings.OwnerID ||
		settings.ExpectedPrincipalID == "" || settings.CutoverAt.IsZero() || settings.CutoverAt.Location() != time.UTC ||
		len(settings.AllowedChannelIDs) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(settings.AllowedChannelIDs))
	for _, channelID := range settings.AllowedChannelIDs {
		if strings.TrimSpace(channelID) == "" || strings.TrimSpace(channelID) != channelID {
			return false
		}
		if _, exists := seen[channelID]; exists {
			return false
		}
		seen[channelID] = struct{}{}
	}
	return true
}

func newPinnedBoardClient(options boardcommunication.Options, pin eventstore.BoardConnectionPin) (*boardcommunication.Client, error) {
	options.ExpectedConnection = boardcommunication.ConnectionIdentity{
		PrincipalID:       pin.PrincipalID,
		JournalInstanceID: pin.JournalInstanceID,
	}
	client, err := boardcommunication.NewClient(options)
	if err != nil {
		return nil, fmt.Errorf("create bound BOARD client: %w", err)
	}
	return client, nil
}

func verifyBoardConnectionContexts(ctx context.Context, client *boardcommunication.Client, allowedChannelIDs []string) error {
	contexts, err := client.ListContexts(ctx)
	if err != nil {
		return fmt.Errorf("read bound BOARD contexts: %w", err)
	}
	available := make(map[string]struct{}, len(contexts))
	for _, context := range contexts {
		available[context.ID] = struct{}{}
	}
	for _, channelID := range allowedChannelIDs {
		if _, found := available[channelID]; !found {
			return ErrBoardCommunicationConnectionUnavailable
		}
	}
	return nil
}
