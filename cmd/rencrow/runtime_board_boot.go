package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/boardcommunication"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var errBoardCommunicationBootUnavailable = errors.New("BOARD communication boot is unavailable")

const (
	boardCommunicationWakeTimeout = 5 * time.Second
	boardCommunicationOwnerActor  = "mio"
)

type boardCommunicationBoot struct {
	mu           sync.RWMutex
	openMu       sync.Mutex
	store        *eventstore.SQLiteStore
	sessions     boardCommunicationCanonicalSessionOwner
	mio          *agent.MioAgent
	peerChat     *agent.BoardPeerChatRuntime
	conversation boardCommunicationConversationProvider
	service      *boardCommunicationService
	settings     boardConnectionSettings
	inboxScope   boardInboxScope
	ownerID      string
	client       *boardcommunication.Client
	pin          eventstore.BoardConnectionPin
	composition  *boardCommunicationComposition
}

func buildBoardCommunicationRuntime(
	cfg *config.Config,
	deps *Dependencies,
	canonicalStore runtimeCanonicalEventStore,
	sessionRepository any,
	mio *agent.MioAgent,
	conversation boardCommunicationConversationProvider,
	responderWiring ...boardCommunicationResponderWiring,
) {
	if cfg == nil || deps == nil || !cfg.BoardCommunication.Enabled {
		return
	}

	service := newBoardCommunicationService(boardCommunicationServiceDeps{enabled: true})
	deps.boardCommunication = service
	boot := &boardCommunicationBoot{service: service}
	ownerID := strings.TrimSpace(cfg.LocalAgentOps.UserID)
	if ownerID == "" || ownerID != cfg.LocalAgentOps.UserID || !agentOpsUserIDPattern.MatchString(ownerID) {
		ownerID = ""
	}
	if cfg.LocalAgentOps.Enabled && ownerID != "" {
		if ownerToken, err := readAgentOpsToken(cfg.LocalAgentOps.AuthTokenFile); err == nil {
			handler, handlerErr := newBoardOwnerHTTPHandler(ownerID, ownerToken, boot.ownerCallbacks())
			if handlerErr == nil {
				deps.boardOwnerHTTP = handler
			}
		}
	}

	store, storeOK := canonicalStore.(*eventstore.SQLiteStore)
	sessions, sessionsOK := sessionRepository.(boardCommunicationCanonicalSessionOwner)
	if storeOK {
		boot.store = store
	}
	if sessionsOK {
		boot.sessions = sessions
	}
	boot.mio = mio
	boot.conversation = conversation
	boot.ownerID = ownerID
	if len(cfg.BoardCommunication.Responders) > 0 {
		wiring := boardCommunicationResponderWiring{Readback: responseFirstReadbackWiring{Assignees: make(map[string]agent.ReadbackAssigneeConfig)}}
		if len(responderWiring) > 0 {
			wiring = responderWiring[0]
		}
		buildBoardCommunicationResponderRuntimes(cfg, deps, boot.store, boot.sessions, ownerID, wiring)
	}
	cutoverAt, err := time.Parse(time.RFC3339, cfg.BoardCommunication.CutoverAt)
	if err != nil {
		service.setStatus(boardCommunicationServiceStateUnavailable, false)
		return
	}
	boardConfig := cfg.BoardCommunication
	allowedChannels := append([]string(nil), boardConfig.ChannelIDs...)
	allowedPeers := append([]string(nil), boardConfig.PeerIDs...)
	transport := boardcommunication.Options{
		URL: boardConfig.URL, UnixSocket: boardConfig.UnixSocket,
		TokenSource: func(ctx context.Context) ([]byte, error) {
			if ctx == nil {
				return nil, errBoardCommunicationBootUnavailable
			}
			return config.ReadBoardCommunicationBearerToken(boardConfig.TokenFile)
		},
	}
	boot.settings = boardConnectionSettings{
		OwnerID: ownerID, ExpectedPrincipalID: boardConfig.PrincipalID,
		AllowedChannelIDs: allowedChannels, CutoverAt: cutoverAt.UTC(), Transport: transport,
	}
	boot.inboxScope = boardInboxScope{
		AllowedChannelIDs: allowedChannels, AllowedPeerIDs: allowedPeers,
		WakeTimeout: boardCommunicationWakeTimeout,
	}
	if !storeOK || !sessionsOK || mio == nil || conversation == nil ||
		!cfg.LocalAgentOps.Enabled || boot.ownerID == "" || deps.boardOwnerHTTP == nil {
		service.setStatus(boardCommunicationServiceStateUnavailable, false)
		return
	}
	if !validBoardConnectionSettings(boot.settings) || !validBoardInboxAllowlist(allowedPeers) {
		service.setStatus(boardCommunicationServiceStateUnavailable, false)
		return
	}

	service.deps.lifecycle = boardCommunicationLifecycleDeps{enabled: true, open: boot.openGeneration}
	service.deps.process = func(ctx context.Context) (boardCommunicationServiceStep, error) {
		composition := boot.currentComposition()
		if composition == nil {
			return boardCommunicationServiceStep{}, errBoardCommunicationBootUnavailable
		}
		return composition.processCatchUp(ctx)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	deps.boardCommunicationCancel = cancel
	deps.boardCommunicationDone = done
	go func() {
		defer close(done)
		_ = service.Run(ctx)
		boot.closeClient()
	}()
}

func buildBoardCommunicationResponderRuntimes(
	cfg *config.Config,
	deps *Dependencies,
	store *eventstore.SQLiteStore,
	sessions boardCommunicationCanonicalSessionOwner,
	ownerID string,
	wiring boardCommunicationResponderWiring,
) {
	if cfg == nil || deps == nil || !cfg.BoardCommunication.Enabled {
		return
	}
	if deps.boardCommunicationResponderServices == nil {
		deps.boardCommunicationResponderServices = make(map[string]*boardCommunicationService)
	}
	for _, configuredResponder := range cfg.BoardCommunication.Responders {
		responder := configuredResponder
		service := newBoardCommunicationService(boardCommunicationServiceDeps{enabled: true})
		deps.boardCommunicationResponderServices[responder.ActorID] = service
		peerChat, err := newBoardPeerChatRuntime(responder.ActorID, wiring)
		cutoverAt, cutoverErr := time.Parse(time.RFC3339, responder.CutoverAt)
		if err != nil {
			log.Printf("BOARD responder %s unavailable: provider/prompt configuration", responder.ActorID)
			service.setStatus(boardCommunicationServiceStateUnavailable, false)
			continue
		}
		if cutoverErr != nil {
			log.Printf("BOARD responder %s unavailable: cutover configuration", responder.ActorID)
			service.setStatus(boardCommunicationServiceStateUnavailable, false)
			continue
		}
		if store == nil || sessions == nil || ownerID == "" {
			log.Printf("BOARD responder %s unavailable: canonical runtime dependencies", responder.ActorID)
			service.setStatus(boardCommunicationServiceStateUnavailable, false)
			continue
		}
		allowedChannels := append([]string(nil), responder.ChannelIDs...)
		allowedPeers := append([]string(nil), responder.PeerIDs...)
		transport := boardcommunication.Options{
			URL: cfg.BoardCommunication.URL, UnixSocket: cfg.BoardCommunication.UnixSocket,
			TokenSource: func(ctx context.Context) ([]byte, error) {
				if ctx == nil {
					return nil, errBoardCommunicationBootUnavailable
				}
				return config.ReadBoardCommunicationBearerToken(responder.TokenFile)
			},
		}
		settings := boardConnectionSettings{
			OwnerID: ownerID, ExpectedPrincipalID: responder.ActorID,
			AllowedChannelIDs: allowedChannels, CutoverAt: cutoverAt.UTC(),
			UsePrincipalScopedPins: true, Transport: transport,
		}
		inboxScope := boardInboxScope{
			ResponderActorID: responder.ActorID, AllowedChannelIDs: allowedChannels,
			AllowedPeerIDs: allowedPeers, WakeTimeout: boardCommunicationWakeTimeout,
		}
		if !validBoardConnectionSettings(settings) || !validBoardInboxAllowlist(allowedPeers) {
			log.Printf("BOARD responder %s unavailable: connection or inbox scope configuration", responder.ActorID)
			service.setStatus(boardCommunicationServiceStateUnavailable, false)
			continue
		}
		boot := &boardCommunicationBoot{
			store: store, sessions: sessions, peerChat: peerChat, service: service,
			settings: settings, inboxScope: inboxScope, ownerID: ownerID,
		}
		service.deps.lifecycle = boardCommunicationLifecycleDeps{enabled: true, open: boot.openGeneration}
		service.deps.process = func(ctx context.Context) (boardCommunicationServiceStep, error) {
			composition := boot.currentComposition()
			if composition == nil {
				return boardCommunicationServiceStep{}, errBoardCommunicationBootUnavailable
			}
			return composition.processCatchUp(ctx)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		deps.boardCommunicationResponderCancels = append(deps.boardCommunicationResponderCancels, cancel)
		deps.boardCommunicationResponderDones = append(deps.boardCommunicationResponderDones, done)
		go func() {
			defer close(done)
			_ = service.Run(ctx)
			boot.closeClient()
		}()
	}
}

func (b *boardCommunicationBoot) openGeneration(ctx context.Context) (boardCommunicationLifecycleGeneration, error) {
	if b == nil || ctx == nil || b.store == nil || b.service == nil || b.sessions == nil ||
		(b.peerChat == nil && (b.mio == nil || b.conversation == nil)) {
		return boardCommunicationLifecycleGeneration{}, errBoardCommunicationBootUnavailable
	}
	b.openMu.Lock()
	defer b.openMu.Unlock()
	b.mu.RLock()
	client, pin, composition := b.client, b.pin, b.composition
	b.mu.RUnlock()
	if client == nil {
		openedClient, openedPin, err := openBoardCommunicationConnection(ctx, b.store, b.settings)
		if err != nil {
			b.service.setStatus(boardCommunicationServiceStateUnavailable, false)
			return boardCommunicationLifecycleGeneration{}, err
		}
		inboxScope := b.inboxScope
		inboxScope.Pin = openedPin
		createdComposition, err := newBoardCommunicationComposition(boardCommunicationCompositionDeps{
			Store: b.store, Sessions: b.sessions, Mio: b.mio, PeerChat: b.peerChat, Conversation: b.conversation,
			Client: openedClient, Pin: openedPin, InboxScope: inboxScope, Service: b.service,
			UsePrincipalScopedPins: b.settings.UsePrincipalScopedPins,
			Now:                    time.Now, OwnerID: b.ownerID, ActorID: func() string {
				if b.peerChat != nil {
					return ""
				}
				return boardCommunicationOwnerActor
			}(),
			AllowedChannelIDs: append([]string(nil), b.settings.AllowedChannelIDs...),
			AllowedPeerIDs:    append([]string(nil), b.inboxScope.AllowedPeerIDs...),
		})
		if err != nil {
			openedClient.Close()
			b.service.setStatus(boardCommunicationServiceStateUnavailable, false)
			return boardCommunicationLifecycleGeneration{}, err
		}
		b.mu.Lock()
		b.client, b.pin, b.composition = openedClient, openedPin, createdComposition
		b.mu.Unlock()
		client, pin, composition = openedClient, openedPin, createdComposition
	}
	if client == nil || composition == nil {
		return boardCommunicationLifecycleGeneration{}, errBoardCommunicationBootUnavailable
	}
	identity, err := client.ReadConnection(ctx)
	if err != nil {
		b.service.setStatus(boardCommunicationServiceStateUnavailable, false)
		return boardCommunicationLifecycleGeneration{}, err
	}
	if identity.PrincipalID != pin.PrincipalID || identity.JournalInstanceID != pin.JournalInstanceID {
		b.service.setStatus(boardCommunicationServiceStateUnavailable, false)
		return boardCommunicationLifecycleGeneration{}, boardcommunication.ErrConnectionIdentityMismatch
	}
	if err := verifyBoardConnectionContexts(ctx, client, b.settings.AllowedChannelIDs); err != nil {
		b.service.setStatus(boardCommunicationServiceStateUnavailable, false)
		return boardCommunicationLifecycleGeneration{}, err
	}
	return boardCommunicationLifecycleGeneration{
		openEvents: func(streamCtx context.Context, after int64) (boardCommunicationEventStream, error) {
			return client.OpenEvents(streamCtx, after)
		},
		catchUp: func(catchUpCtx context.Context) error {
			_, err := composition.processCatchUp(catchUpCtx)
			return err
		},
		close: func() {},
	}, nil
}

func (b *boardCommunicationBoot) currentComposition() *boardCommunicationComposition {
	if b == nil {
		return nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.composition
}

func (b *boardCommunicationBoot) closeClient() {
	if b == nil {
		return
	}
	b.mu.Lock()
	client := b.client
	b.client = nil
	b.mu.Unlock()
	if client != nil {
		client.Close()
	}
}

func (b *boardCommunicationBoot) ownerCallbacks() boardOwnerHTTPCallbacks {
	return boardOwnerHTTPCallbacks{
		Accept: func(ctx context.Context, ownerID string, request boardOwnerHTTPRequest) (boardOwnerHTTPAcceptedReceipt, error) {
			composition := b.currentComposition()
			if composition == nil {
				return boardOwnerHTTPAcceptedReceipt{}, ErrBoardCommunicationCompositionUnavailable
			}
			callbacks := composition.ownerHTTPCallbacks()
			if callbacks.Accept == nil {
				return boardOwnerHTTPAcceptedReceipt{}, ErrBoardCommunicationCompositionUnavailable
			}
			return callbacks.Accept(ctx, ownerID, request)
		},
		Status: func(ctx context.Context, ownerID string, turnID modulecore.TurnID) (boardOwnerHTTPStatusReceipt, bool, error) {
			return b.readOwnerRequestStatus(ctx, ownerID, turnID)
		},
	}
}

func (b *boardCommunicationBoot) readOwnerRequestStatus(ctx context.Context, ownerID string, turnID modulecore.TurnID) (boardOwnerHTTPStatusReceipt, bool, error) {
	if b == nil || ctx == nil || ownerID == "" || ownerID != b.ownerID || turnID.Validate() != nil {
		return boardOwnerHTTPStatusReceipt{}, false, errBoardOwnerHTTPNotFound
	}
	if b.store == nil {
		return boardOwnerHTTPStatusReceipt{}, false, ErrBoardCommunicationCompositionUnavailable
	}
	return readBoardOwnerHTTPStatus(
		ctx, b.store, ownerID, turnID, b.settings.ExpectedPrincipalID, b.settings.CutoverAt,
	)
}

func registerBoardCommunicationOwnerRoutes(mux *http.ServeMux, deps *Dependencies) {
	if mux == nil || deps == nil || deps.boardOwnerHTTP == nil {
		return
	}
	mux.Handle(boardOwnerRequestsPath, deps.boardOwnerHTTP)
	mux.Handle(boardOwnerRequestsPath+"/", deps.boardOwnerHTTP)
}
