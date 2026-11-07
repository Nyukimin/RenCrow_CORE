package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/boardcommunication"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var ErrBoardCommunicationCompositionUnavailable = errors.New("BOARD communication composition unavailable")

// boardCommunicationCompositionStore is one existing EventStore handle for
// the pin, owner admission and Mio outcome journal, peer acceptance, and B2
// publication delivery. It adds no persistence surface of its own.
type boardCommunicationCompositionStore interface {
	ReadBoardConnectionPin(context.Context) (eventstore.BoardConnectionPin, bool, error)
	PinBoardConnection(context.Context, eventstore.BoardConnectionPin) (eventstore.BoardConnectionPin, error)
	ReadBoardOwnerRequestStatus(context.Context, eventstore.BoardPublicationScope, modulecore.TurnID) (eventstore.BoardOwnerRequestReadback, bool, error)
	ReserveBoardOwnerRequest(context.Context, eventstore.BoardOwnerRequestReservationInput) (eventstore.BoardOwnerRequestReservation, error)
	AcceptBoardOwnerRequest(context.Context, eventstore.BoardOwnerRequestAcceptInput) (eventstore.AcceptedTurn, error)
	ReadSessionState(context.Context, modulecore.SessionID, string) (eventstore.SessionState, bool, error)
	ReadCompleteSessionTail(context.Context, modulecore.SessionID, modulecore.EventSeq) (eventstore.CanonicalTail, error)
	ReadBoardPeerThreadSession(context.Context, eventstore.BoardPeerThreadSessionKey) (eventstore.BoardPeerThreadSessionBinding, bool, error)
	BindBoardPeerThreadSession(context.Context, eventstore.BoardPeerThreadSessionBinding) (eventstore.BoardPeerThreadSessionBinding, error)
	FindAcceptedBoardPeerRequest(context.Context, eventstore.BoardPeerRequestSource) (eventstore.AcceptedTurn, bool, error)
	AcceptBoardPeerRequest(context.Context, eventstore.BoardPeerRequestAcceptInput) (eventstore.AcceptedTurn, error)
	ReadPendingBoardOwnerRequests(context.Context, eventstore.BoardPublicationScope, modulecore.EventSeq, int) (eventstore.BoardOwnerRequestPendingPage, error)
	LoadBoardPublicationByTurn(context.Context, eventstore.BoardPublicationTurnLookupInput) (eventstore.BoardPublicationOutcome, bool, error)
	NextBoardPublicationDue(context.Context, eventstore.BoardPublicationScope, time.Time) (time.Time, error)
	RecordBoardTerminalReply(context.Context, eventstore.BoardTerminalReplySource) (eventstore.BoardTerminalReplyReceipt, bool, error)
	conversationruntime.BoardOwnerRequestJournal
	conversationruntime.ExternalTurnJournal
	boardPublicationDeliveryJournal
}

var _ boardCommunicationCompositionStore = (*eventstore.SQLiteStore)(nil)

type boardCommunicationCanonicalSessionOwner interface {
	LoadOrCreateCanonicalForOwner(context.Context, string, domainconversation.ChannelAddress, string, time.Time) (session.CanonicalSessionOwnerResolution, error)
}

type boardCommunicationConversationProvider interface {
	GenerateConversation(context.Context, domainconversation.ConversationScope, llm.GenerateRequest) (llm.GenerateResponse, error)
}

// boardCommunicationPeerCallbacks are the typed EventStore/read-view and
// external-only runtime seams. The private claim is forwarded only with the
// exact accepted request and canonical source that produced it.
type boardCommunicationPeerCallbacks struct {
	FindAccepted func(context.Context, boardInboxSource) (conversationruntime.ReadViewRequest, bool, error)
	Accept       func(context.Context, boardInboxSource) (conversationruntime.ReadViewRequest, error)
	Process      func(context.Context, conversationruntime.ReadViewRequest, boardInboxSource, boardInboxClaim, *agent.MioAgent, boardCommunicationConversationProvider) (conversationruntime.TurnResult, error)
	HasOutcome   func(context.Context, conversationruntime.ReadViewRequest) (bool, error)
	Correlate    func(context.Context, boardInboxSource) (bool, error)
}

type boardCommunicationCompositionDeps struct {
	Store                  boardCommunicationCompositionStore
	Sessions               boardCommunicationCanonicalSessionOwner
	Mio                    *agent.MioAgent
	PeerChat               *agent.BoardPeerChatRuntime
	Conversation           boardCommunicationConversationProvider
	Peer                   boardCommunicationPeerCallbacks
	Client                 *boardcommunication.Client
	Pin                    eventstore.BoardConnectionPin
	InboxScope             boardInboxScope
	UsePrincipalScopedPins bool
	Service                *boardCommunicationService
	Now                    func() time.Time
	OwnerID                string
	ActorID                string
	AllowedChannelIDs      []string
	AllowedPeerIDs         []string
}

type boardCommunicationComposition struct {
	deps              boardCommunicationCompositionDeps
	ownerMio          *conversationruntime.BoardOwnerRequestRuntime
	peerMio           *conversationruntime.Runtime
	peerResponder     *agent.BoardPeerChatRuntime
	peerOwner         boardInboxOwner
	boardClient       *boardcommunication.Client
	ownerPendingAfter modulecore.EventSeq
}

func newBoardCommunicationComposition(deps boardCommunicationCompositionDeps) (*boardCommunicationComposition, error) {
	if deps.Store == nil || deps.Sessions == nil || deps.Client == nil || deps.Service == nil || deps.Now == nil || deps.OwnerID == "" {
		return nil, ErrBoardCommunicationCompositionUnavailable
	}
	var ownerRuntime *conversationruntime.BoardOwnerRequestRuntime
	var peerComposer conversationruntime.PromptComposer
	var peerGenerator conversationruntime.EmissionGenerator
	responderActorID := ""
	if deps.PeerChat == nil {
		if deps.Mio == nil || deps.Conversation == nil || deps.ActorID == "" || deps.UsePrincipalScopedPins {
			return nil, ErrBoardCommunicationCompositionUnavailable
		}
		var err error
		ownerRuntime, err = conversationruntime.NewBoardOwnerRequestRuntime(conversationruntime.BoardOwnerRequestRuntimeDependencies{
			Composer: deps.Mio,
			Generator: agent.NewMioEmissionGenerator(responseFirstConversationProvider{
				provider: deps.Conversation,
			}),
			Journal:       deps.Store,
			PolicyVersion: responseFirstPolicyVer,
		})
		if err != nil {
			return nil, ErrBoardCommunicationCompositionUnavailable
		}
		peerComposer = deps.Mio
		peerGenerator = agent.NewMioEmissionGenerator(responseFirstConversationProvider{provider: deps.Conversation})
	} else {
		responderActorID = string(deps.PeerChat.Actor())
		if !domainconversation.IsExternalPeerResponderActor(responderActorID) ||
			responderActorID != deps.InboxScope.ResponderActorID || responderActorID != deps.Pin.PrincipalID ||
			!deps.UsePrincipalScopedPins || deps.PeerChat.Composer == nil || deps.PeerChat.Generator == nil {
			return nil, ErrBoardCommunicationCompositionUnavailable
		}
		peerComposer = deps.PeerChat.Composer
		peerGenerator = deps.PeerChat.Generator
	}
	peerRuntime, err := conversationruntime.NewExternalRuntime(conversationruntime.RuntimeDependencies{
		ReadViews:                conversationruntime.NewReadViewService(boardCommunicationCanonicalTailReader{store: deps.Store}),
		Composer:                 peerComposer,
		Generator:                peerGenerator,
		ExternalJournal:          deps.Store,
		ExternalResponderActorID: responderActorID,
		Config:                   conversationruntime.RuntimeConfig{PolicyVersion: responseFirstPolicyVer},
	})
	if err != nil {
		return nil, ErrBoardCommunicationCompositionUnavailable
	}
	composition := &boardCommunicationComposition{
		deps: deps, ownerMio: ownerRuntime, peerMio: peerRuntime, peerResponder: deps.PeerChat, boardClient: deps.Client,
	}
	callbacks := deps.Peer
	if callbacks.FindAccepted == nil {
		callbacks.FindAccepted = composition.findAcceptedPeerRequest
	}
	if callbacks.Accept == nil {
		callbacks.Accept = composition.acceptPeerRequest
	}
	if callbacks.Process == nil {
		callbacks.Process = composition.processClaimedPeerRequest
	}
	if callbacks.HasOutcome == nil {
		callbacks.HasOutcome = composition.hasDurablePeerOutcome
	}
	if callbacks.Correlate == nil {
		callbacks.Correlate = composition.correlateTerminalPeerReply
	}
	composition.peerOwner = boardCommunicationPeerOwner{
		callbacks: callbacks, mio: deps.Mio, conversation: deps.Conversation,
	}
	return composition, nil
}

// acceptOwnerRequest is the authenticated owner callback. The implementation
// must reserve, resolve the existing canonical Session, durably accept, and
// only then signal the optional local processor.
func (c *boardCommunicationComposition) acceptOwnerRequest(ctx context.Context, ownerID string, request boardOwnerHTTPRequest) (boardOwnerHTTPAcceptedReceipt, error) {
	if c == nil || c.ownerMio == nil || ctx == nil || c.deps.Store == nil || c.deps.Sessions == nil || c.deps.Now == nil ||
		ownerID == "" || ownerID != c.deps.OwnerID || !validBoardOwnerHTTPRequestForComposition(request, c.deps.AllowedChannelIDs, c.deps.AllowedPeerIDs) {
		return boardOwnerHTTPAcceptedReceipt{}, errBoardOwnerHTTPInvalid
	}
	if err := ctx.Err(); err != nil {
		return boardOwnerHTTPAcceptedReceipt{}, err
	}
	pin, found, err := c.deps.Store.ReadBoardConnectionPin(ctx)
	if err != nil {
		return boardOwnerHTTPAcceptedReceipt{}, err
	}
	if !found || !sameBoardCommunicationPin(pin, c.deps.Pin) || pin.OwnerID != ownerID {
		return boardOwnerHTTPAcceptedReceipt{}, errBoardOwnerHTTPNotFound
	}
	source := conversationruntime.BoardOwnerRequestSource{
		OwnerID: ownerID, BoardInstanceID: pin.JournalInstanceID, CorePrincipalID: pin.PrincipalID,
		ChannelID: request.ChannelID, PeerPrincipalID: request.PeerID,
		Title: request.Title, IdempotencyKey: request.IdempotencyKey,
	}
	reservation, err := c.deps.Store.ReserveBoardOwnerRequest(ctx, eventstore.BoardOwnerRequestReservationInput{
		Source: source, ActorID: c.deps.ActorID, Instruction: request.Instruction, Now: c.deps.Now().UTC(),
	})
	if err != nil {
		return boardOwnerHTTPAcceptedReceipt{}, boardOwnerRequestHTTPError(err)
	}
	address, err := boardOwnerRequestSessionAddress(reservation.Source)
	if err != nil {
		return boardOwnerHTTPAcceptedReceipt{}, errBoardOwnerHTTPInvalid
	}
	admittedAt := reservation.AdmittedAt.UTC()
	resolution, err := resolveBoardCanonicalSession(ctx, c.deps.Sessions, admittedAt.Format("2006-01-02"), address, ownerID, admittedAt)
	if err != nil {
		return boardOwnerHTTPAcceptedReceipt{}, err
	}
	if resolution.Session == nil || resolution.BoundOwnerID != ownerID ||
		(resolution.Status != session.CanonicalSessionOwnerCreated && resolution.Status != session.CanonicalSessionOwnerMatched) {
		return boardOwnerHTTPAcceptedReceipt{}, errBoardOwnerHTTPNotFound
	}
	state, stateFound, err := c.deps.Store.ReadSessionState(ctx, modulecore.SessionID(resolution.Session.ID()), ownerID)
	if err != nil {
		return boardOwnerHTTPAcceptedReceipt{}, err
	}
	generationEpoch := int64(1)
	createNewSession := !stateFound
	if stateFound {
		if state.SessionID != modulecore.SessionID(resolution.Session.ID()) || state.OwnerID != ownerID ||
			state.RuntimeVersion != conversationruntime.ConversationRuntimeV2 || state.GenerationEpoch <= 0 {
			return boardOwnerHTTPAcceptedReceipt{}, errBoardOwnerHTTPConflict
		}
		generationEpoch = state.GenerationEpoch
	}
	accepted, err := c.deps.Store.AcceptBoardOwnerRequest(ctx, eventstore.BoardOwnerRequestAcceptInput{
		Reservation: reservation, ActorID: c.deps.ActorID, Instruction: request.Instruction,
		SessionID: modulecore.SessionID(resolution.Session.ID()), RuntimeVersion: conversationruntime.ConversationRuntimeV2,
		GenerationEpoch: generationEpoch, CreateNewSession: createNewSession,
	})
	if err != nil {
		return boardOwnerHTTPAcceptedReceipt{}, err
	}
	if accepted.TriggerKind != "board_owner_request" || accepted.ActorID != c.deps.ActorID ||
		accepted.SessionID != modulecore.SessionID(resolution.Session.ID()) || (accepted.State != "accepted" && accepted.State != "completed") ||
		accepted.InputEvent.EventType != "conversation.board_owner_request" || accepted.InputEvent.SessionID != accepted.SessionID ||
		accepted.InputEvent.TurnID != accepted.TurnID || accepted.InputEvent.MessageID != accepted.InputMessageID ||
		accepted.TurnID.Validate() != nil || accepted.SessionID.Validate() != nil || accepted.InputMessageID.Validate() != nil ||
		accepted.InputEvent.TraceID.Validate() != nil || !accepted.InputEvent.OccurredAt.Equal(reservation.AdmittedAt) {
		return boardOwnerHTTPAcceptedReceipt{}, errBoardOwnerHTTPConflict
	}
	receipt := boardOwnerHTTPAcceptedReceipt{
		OwnerID: ownerID, SessionID: accepted.SessionID, TurnID: accepted.TurnID,
		TraceID: accepted.InputEvent.TraceID, InputMessageID: accepted.InputMessageID,
		AcceptedAt: accepted.InputEvent.OccurredAt.UTC(), Durable: true,
	}
	if !validBoardOwnerHTTPAcceptedReceipt(receipt, ownerID) {
		return boardOwnerHTTPAcceptedReceipt{}, errBoardOwnerHTTPConflict
	}
	c.deps.Service.NotifyDurableAcceptance()
	return receipt, nil
}

func (c *boardCommunicationComposition) ownerHTTPCallbacks() boardOwnerHTTPCallbacks {
	return boardOwnerHTTPCallbacks{Accept: c.acceptOwnerRequest, Status: c.ownerRequestStatus}
}

func (c *boardCommunicationComposition) ownerRequestStatus(ctx context.Context, ownerID string, turnID modulecore.TurnID) (boardOwnerHTTPStatusReceipt, bool, error) {
	if c == nil || c.ownerMio == nil || ctx == nil || ownerID == "" || ownerID != c.deps.OwnerID || turnID.Validate() != nil {
		return boardOwnerHTTPStatusReceipt{}, false, errBoardOwnerHTTPNotFound
	}
	return readBoardOwnerHTTPStatus(ctx, c.deps.Store, ownerID, turnID, c.deps.Pin.PrincipalID, c.deps.Pin.CutoverAt)
}

type boardCommunicationPeerOwner struct {
	callbacks    boardCommunicationPeerCallbacks
	mio          *agent.MioAgent
	conversation boardCommunicationConversationProvider
}

func (o boardCommunicationPeerOwner) FindAcceptedBoardRequest(ctx context.Context, source boardInboxSource) (conversationruntime.ReadViewRequest, bool, error) {
	if o.callbacks.FindAccepted == nil {
		return conversationruntime.ReadViewRequest{}, false, ErrBoardCommunicationCompositionUnavailable
	}
	return o.callbacks.FindAccepted(ctx, source)
}

func (o boardCommunicationPeerOwner) AcceptBoardRequest(ctx context.Context, source boardInboxSource) (conversationruntime.ReadViewRequest, error) {
	if o.callbacks.Accept == nil {
		return conversationruntime.ReadViewRequest{}, ErrBoardCommunicationCompositionUnavailable
	}
	return o.callbacks.Accept(ctx, source)
}

func (o boardCommunicationPeerOwner) ProcessClaimedBoardRequest(ctx context.Context, accepted conversationruntime.ReadViewRequest, source boardInboxSource, claim boardInboxClaim) (conversationruntime.TurnResult, error) {
	if o.callbacks.Process == nil {
		return conversationruntime.TurnResult{}, ErrBoardCommunicationCompositionUnavailable
	}
	return o.callbacks.Process(ctx, accepted, source, claim, o.mio, o.conversation)
}

func (o boardCommunicationPeerOwner) HasDurableBoardOutcome(ctx context.Context, accepted conversationruntime.ReadViewRequest) (bool, error) {
	if o.callbacks.HasOutcome == nil {
		return false, ErrBoardCommunicationCompositionUnavailable
	}
	return o.callbacks.HasOutcome(ctx, accepted)
}

func (o boardCommunicationPeerOwner) CorrelateTerminalBoardReply(ctx context.Context, source boardInboxSource) (bool, error) {
	if o.callbacks.Correlate == nil {
		return false, ErrBoardCommunicationCompositionUnavailable
	}
	return o.callbacks.Correlate(ctx, source)
}

func (c *boardCommunicationComposition) findAcceptedPeerRequest(ctx context.Context, source boardInboxSource) (conversationruntime.ReadViewRequest, bool, error) {
	if c == nil || ctx == nil || c.deps.Store == nil {
		return conversationruntime.ReadViewRequest{}, false, ErrBoardCommunicationCompositionUnavailable
	}
	if err := c.validateResponderBinding(source.Binding); err != nil {
		return conversationruntime.ReadViewRequest{}, false, err
	}
	peerSource, err := boardPeerRequestSource(c.deps.InboxScope.Pin, source)
	if err != nil {
		return conversationruntime.ReadViewRequest{}, false, err
	}
	accepted, found, err := c.deps.Store.FindAcceptedBoardPeerRequest(ctx, peerSource)
	if err != nil || !found {
		return conversationruntime.ReadViewRequest{}, found, err
	}
	request, err := c.readAcceptedPeerRequest(ctx, accepted, source)
	if err != nil {
		return conversationruntime.ReadViewRequest{}, false, err
	}
	return request, true, nil
}

func (c *boardCommunicationComposition) acceptPeerRequest(ctx context.Context, source boardInboxSource) (conversationruntime.ReadViewRequest, error) {
	if c == nil || ctx == nil || c.deps.Store == nil || c.deps.Sessions == nil {
		return conversationruntime.ReadViewRequest{}, ErrBoardCommunicationCompositionUnavailable
	}
	if err := c.validateResponderBinding(source.Binding); err != nil {
		return conversationruntime.ReadViewRequest{}, err
	}
	peerSource, err := boardPeerRequestSource(c.deps.InboxScope.Pin, source)
	if err != nil {
		return conversationruntime.ReadViewRequest{}, err
	}
	key := eventstore.BoardPeerThreadSessionKey{
		Scope: peerSource.Scope, ChannelID: source.Binding.ChannelID,
		ThreadID: source.Binding.ThreadID, ThreadCreatedAt: source.ThreadCreatedAt.UTC(),
		ResponderActorID: source.Binding.ResponderActorID,
	}
	address, err := boardPeerThreadSessionAddress(key)
	if err != nil {
		return conversationruntime.ReadViewRequest{}, err
	}
	createdAt := source.ThreadCreatedAt.UTC()
	resolution, err := resolveBoardCanonicalSession(ctx, c.deps.Sessions,
		createdAt.Format("2006-01-02"), address, c.deps.OwnerID, createdAt)
	if err != nil {
		return conversationruntime.ReadViewRequest{}, err
	}
	if resolution.Session == nil || resolution.BoundOwnerID != c.deps.OwnerID ||
		(resolution.Status != session.CanonicalSessionOwnerCreated && resolution.Status != session.CanonicalSessionOwnerMatched) {
		return conversationruntime.ReadViewRequest{}, ErrBoardInboxCanonicalMismatch
	}
	resolvedSessionID := modulecore.SessionID(resolution.Session.ID())
	state, stateFound, err := c.deps.Store.ReadSessionState(ctx, resolvedSessionID, c.deps.OwnerID)
	if err != nil {
		return conversationruntime.ReadViewRequest{}, err
	}
	generationEpoch := int64(1)
	createNewSession := !stateFound
	if stateFound {
		if state.SessionID != resolvedSessionID || state.OwnerID != c.deps.OwnerID ||
			state.RuntimeVersion != conversationruntime.ConversationRuntimeV2 || state.GenerationEpoch <= 0 {
			return conversationruntime.ReadViewRequest{}, ErrBoardInboxCanonicalMismatch
		}
		generationEpoch = state.GenerationEpoch
	}
	threadBinding, found, err := c.deps.Store.ReadBoardPeerThreadSession(ctx, key)
	if err != nil {
		return conversationruntime.ReadViewRequest{}, err
	}
	if found && (threadBinding.Key != key || threadBinding.SessionID != resolvedSessionID ||
		(!stateFound && !threadBinding.CreateNewSession)) {
		return conversationruntime.ReadViewRequest{}, ErrBoardInboxCanonicalMismatch
	}
	if !found {
		threadBinding, err = c.deps.Store.BindBoardPeerThreadSession(ctx, eventstore.BoardPeerThreadSessionBinding{
			Key: key, SessionID: resolvedSessionID, CreateNewSession: createNewSession,
		})
		if err != nil {
			return conversationruntime.ReadViewRequest{}, err
		}
		if threadBinding.Key != key || threadBinding.SessionID != resolvedSessionID ||
			threadBinding.CreateNewSession != createNewSession {
			return conversationruntime.ReadViewRequest{}, ErrBoardInboxCanonicalMismatch
		}
	}
	accepted, err := c.deps.Store.AcceptBoardPeerRequest(ctx, eventstore.BoardPeerRequestAcceptInput{
		Source: peerSource, SessionID: threadBinding.SessionID, RuntimeVersion: conversationruntime.ConversationRuntimeV2,
		GenerationEpoch: generationEpoch,
	})
	if err != nil {
		return conversationruntime.ReadViewRequest{}, err
	}
	return c.readAcceptedPeerRequest(ctx, accepted, source)
}

func (c *boardCommunicationComposition) readAcceptedPeerRequest(ctx context.Context, accepted eventstore.AcceptedTurn, source boardInboxSource) (conversationruntime.ReadViewRequest, error) {
	if c == nil || ctx == nil || c.deps.Store == nil {
		return conversationruntime.ReadViewRequest{}, ErrBoardCommunicationCompositionUnavailable
	}
	if err := c.validateResponderBinding(source.Binding); err != nil {
		return conversationruntime.ReadViewRequest{}, err
	}
	if err := validateAcceptedBoardRequest(conversationruntime.ReadViewRequest{
		Principal: conversationruntime.Principal{OwnerID: c.deps.OwnerID, ActorID: accepted.ActorID},
		Session:   conversationruntime.SessionState{OwnerID: c.deps.OwnerID},
		Accepted:  toConversationAcceptedTurn(accepted),
	}, c.deps.InboxScope.Pin, source.Binding); err != nil {
		return conversationruntime.ReadViewRequest{}, err
	}
	state, found, err := c.deps.Store.ReadSessionState(ctx, accepted.SessionID, c.deps.OwnerID)
	if err != nil {
		return conversationruntime.ReadViewRequest{}, err
	}
	if !found || state.SessionID != accepted.SessionID || state.OwnerID != c.deps.OwnerID ||
		state.RuntimeVersion != conversationruntime.ConversationRuntimeV2 ||
		state.GenerationEpoch != accepted.GenerationEpoch || accepted.State == "" {
		return conversationruntime.ReadViewRequest{}, ErrBoardInboxCanonicalMismatch
	}
	request := conversationruntime.ReadViewRequest{
		Principal: conversationruntime.Principal{OwnerID: c.deps.OwnerID, ActorID: accepted.ActorID},
		Session: conversationruntime.SessionState{
			SessionID: state.SessionID, OwnerID: state.OwnerID, RuntimeVersion: state.RuntimeVersion,
			CutoverSeq: state.CutoverSeq, AdmissionSeq: state.AdmissionSeq,
			ProjectionWatermark: state.ProjectionWatermark, GenerationEpoch: state.GenerationEpoch,
		},
		Accepted: toConversationAcceptedTurn(accepted),
	}
	if err := validateAcceptedBoardRequest(request, c.deps.InboxScope.Pin, source.Binding); err != nil {
		return conversationruntime.ReadViewRequest{}, err
	}
	return request, nil
}

func (c *boardCommunicationComposition) processClaimedPeerRequest(
	ctx context.Context,
	accepted conversationruntime.ReadViewRequest,
	source boardInboxSource,
	claim boardInboxClaim,
	mio *agent.MioAgent,
	provider boardCommunicationConversationProvider,
) (conversationruntime.TurnResult, error) {
	if c == nil || ctx == nil || c.peerMio == nil ||
		!claim.LeaseUntil.After(c.deps.Now().UTC()) {
		return conversationruntime.TurnResult{}, ErrBoardCommunicationCompositionUnavailable
	}
	if err := c.validateResponderBinding(source.Binding); err != nil {
		return conversationruntime.TurnResult{}, err
	}
	if c.peerResponder == nil && (mio != c.deps.Mio || provider != c.deps.Conversation) {
		return conversationruntime.TurnResult{}, ErrBoardCommunicationCompositionUnavailable
	}
	if err := validateAcceptedBoardRequest(accepted, c.deps.InboxScope.Pin, source.Binding); err != nil {
		return conversationruntime.TurnResult{}, err
	}
	lookup, err := conversationruntime.ExternalTurnLookupForRequest(accepted)
	if err != nil {
		return conversationruntime.TurnResult{}, err
	}
	committed, found, err := c.deps.Store.LoadExternalCommittedOutcome(ctx, lookup)
	if err != nil {
		return conversationruntime.TurnResult{}, err
	}
	result := conversationruntime.TurnResult{}
	if !found {
		generationDeadline := claim.LeaseUntil.Add(-boardPeerPublicationReserve)
		if !generationDeadline.After(c.deps.Now().UTC()) {
			return result, nil
		}
		generationCtx, cancel := context.WithDeadline(ctx, generationDeadline)
		result = c.peerMio.RunAcceptedTurn(generationCtx, conversationruntime.AcceptedTurnInput{ReadViewRequest: accepted})
		cancel()
		if result.Status != conversationruntime.TurnStatusPublicationPending {
			if errors.Is(result.Cause, context.DeadlineExceeded) && ctx.Err() == nil {
				return result, nil
			}
			if result.Cause != nil {
				return result, result.Cause
			}
			return result, fmt.Errorf("external peer processing ended in state %q", result.Status)
		}
		committed, found, err = c.deps.Store.LoadExternalCommittedOutcome(ctx, lookup)
		if err != nil || !found {
			if err == nil {
				err = conversationruntime.ErrExternalTurnJournalRequired
			}
			return result, err
		}
	} else {
		result = conversationruntime.TurnResult{Status: conversationruntime.TurnStatusPublicationPending, Committed: true}
	}
	if err := conversationruntime.ValidateExternalCommittedBundle(committed, lookup, accepted.Accepted); err != nil {
		return result, err
	}
	if !claim.LeaseUntil.After(c.deps.Now().UTC().Add(boardPeerPublicationReserve)) {
		return result, nil
	}
	progressed, err := runBoardPublicationDelivery(ctx, c.boardClient, c.deps.Store, boardPublicationDeliveryInput{
		Scope: boardPublicationScope(c.deps.InboxScope.Pin), Now: c.deps.Now().UTC(), LeaseDuration: boardPublicationLeaseDuration,
		ClaimHint: &boardPublicationClaimHint{
			Source: source, Claim: claim,
			CanonicalResponseMessageID: committed.PublicationIntent.CanonicalResponseMessageID,
		},
	})
	if err != nil {
		var retryScheduled *boardPublicationRetryScheduledError
		if errors.As(err, &retryScheduled) && ctx.Err() == nil && !errors.Is(err, boardcommunication.ErrConnectionIdentityMismatch) {
			return result, nil
		}
		return result, err
	}
	if progressed && c.deps.Service != nil {
		c.deps.Service.NotifyPublicationSettled()
	}
	return result, nil
}

func (c *boardCommunicationComposition) hasDurablePeerOutcome(ctx context.Context, accepted conversationruntime.ReadViewRequest) (bool, error) {
	if c == nil || ctx == nil || c.deps.Store == nil {
		return false, ErrBoardCommunicationCompositionUnavailable
	}
	if accepted.Principal.OwnerID != c.deps.OwnerID || accepted.Session.OwnerID != c.deps.OwnerID ||
		accepted.Accepted.TriggerKind != "external_peer_message" || accepted.Accepted.SessionID != accepted.Session.SessionID {
		return false, ErrBoardInboxCanonicalMismatch
	}
	lookup, err := conversationruntime.ExternalTurnLookupForRequest(accepted)
	if err != nil {
		return false, ErrBoardInboxCanonicalMismatch
	}
	binding := lookup.SourceBinding
	if c.validateResponderBinding(binding) != nil || binding.BoardInstanceID != c.deps.InboxScope.Pin.JournalInstanceID ||
		binding.RecipientPrincipalID != c.deps.InboxScope.Pin.PrincipalID {
		return false, ErrBoardInboxCanonicalMismatch
	}
	outcome, found, err := c.deps.Store.LoadBoardPublicationByTurn(ctx, eventstore.BoardPublicationTurnLookupInput{
		Scope: boardPublicationScope(c.deps.InboxScope.Pin), SessionID: accepted.Accepted.SessionID,
		TurnID: accepted.Accepted.TurnID, ResponderActorID: binding.ResponderActorID,
	})
	if err != nil || !found {
		return false, err
	}
	if outcome.Scope != boardPublicationScope(c.deps.InboxScope.Pin) || outcome.WorkID == "" ||
		outcome.CanonicalMessage.MessageID == "" || outcome.SendReceipt.MessageID != outcome.CanonicalMessage.MessageID ||
		outcome.CanonicalMessage.SenderPrincipalID != c.deps.InboxScope.Pin.PrincipalID ||
		outcome.CanonicalMessage.ChannelID != binding.ChannelID || outcome.CanonicalMessage.ThreadID != binding.ThreadID ||
		outcome.CanonicalMessage.ReplyTo != binding.MessageID || outcome.ExpectedPeerPrincipalID != binding.SenderPrincipalID ||
		!containsBoardInboxID(outcome.CanonicalMessage.Recipients, binding.SenderPrincipalID) {
		return false, ErrBoardInboxCanonicalMismatch
	}
	return true, nil
}

func (c *boardCommunicationComposition) nextBoardPublicationDue(ctx context.Context, scope eventstore.BoardPublicationScope, now time.Time) (time.Time, error) {
	if c == nil || c.deps.Store == nil {
		return time.Time{}, ErrBoardCommunicationCompositionUnavailable
	}
	if c.peerResponder == nil {
		return c.deps.Store.NextBoardPublicationDue(ctx, scope, now)
	}
	actorID := string(c.peerResponder.Actor())
	if !domainconversation.IsExternalPeerResponderActor(actorID) {
		return time.Time{}, ErrBoardCommunicationCompositionUnavailable
	}
	store, ok := c.deps.Store.(interface {
		NextBoardPublicationDueForResponder(context.Context, eventstore.BoardPublicationScope, string, time.Time) (time.Time, error)
	})
	if !ok {
		return time.Time{}, ErrBoardCommunicationCompositionUnavailable
	}
	return store.NextBoardPublicationDueForResponder(ctx, scope, actorID, now)
}

func (c *boardCommunicationComposition) correlateTerminalPeerReply(ctx context.Context, source boardInboxSource) (bool, error) {
	if c == nil || ctx == nil || c.deps.Store == nil {
		return false, ErrBoardCommunicationCompositionUnavailable
	}
	if err := c.validateResponderBinding(source.Binding); err != nil {
		return false, err
	}
	terminalSource, err := boardTerminalReplySource(c.deps.InboxScope.Pin, source)
	if err != nil {
		return false, err
	}
	receipt, recorded, err := c.deps.Store.RecordBoardTerminalReply(ctx, terminalSource)
	if errors.Is(err, eventstore.ErrBoardCommunicationReadbackConflict) {
		return false, nil
	}
	if err != nil || !recorded {
		return false, err
	}
	if receipt.Scope != terminalSource.Scope || receipt.BoardMessageID != terminalSource.Binding.MessageID ||
		receipt.ParentBoardMessageID != terminalSource.Binding.ReplyTo {
		return false, ErrBoardInboxCanonicalMismatch
	}
	return true, nil
}

func (c *boardCommunicationComposition) processCatchUp(ctx context.Context) (boardCommunicationServiceStep, error) {
	if c == nil || ctx == nil || c.boardClient == nil || c.deps.Store == nil || c.deps.Now == nil {
		return boardCommunicationServiceStep{}, ErrBoardCommunicationCompositionUnavailable
	}
	if err := ctx.Err(); err != nil {
		return boardCommunicationServiceStep{}, err
	}
	pin, found, err := readBoardConnectionPinForPrincipal(ctx, c.deps.Store, c.deps.Pin.PrincipalID, c.deps.UsePrincipalScopedPins)
	if err != nil || !found || !sameBoardCommunicationPin(pin, c.deps.Pin) {
		return boardCommunicationServiceStep{}, errors.Join(ErrBoardCommunicationCompositionUnavailable, err)
	}
	_, inboxDue, err := catchUpBoardInboxWithDue(ctx, c.boardClient, c.peerOwner, c.deps.InboxScope, make(map[string]boardcommunication.InboxItem))
	if err != nil {
		return boardCommunicationServiceStep{}, err
	}
	scope := boardPublicationScope(pin)
	var ownerRetryDue time.Time
	ownerScanHasMore := false
	for pageIndex := 0; c.ownerMio != nil && pageIndex < boardOwnerPendingMaxPagesPerSweep; pageIndex++ {
		pending, err := c.deps.Store.ReadPendingBoardOwnerRequests(ctx, scope, c.ownerPendingAfter, boardOwnerPendingPageSize)
		if err != nil {
			return boardCommunicationServiceStep{}, err
		}
		if len(pending.Items) == 0 {
			c.ownerPendingAfter = 0
			ownerScanHasMore = false
			break
		}
		for _, item := range pending.Items {
			if err := ctx.Err(); err != nil {
				return boardCommunicationServiceStep{}, err
			}
			if item.Accepted.TriggerKind != "board_owner_request" ||
				item.Reservation.Source.OwnerID != c.deps.OwnerID || item.Accepted.SessionID == "" ||
				item.Lookup.TurnID != item.Accepted.TurnID {
				return boardCommunicationServiceStep{}, errBoardOwnerHTTPConflict
			}
			state, stateFound, err := c.deps.Store.ReadSessionState(ctx, item.Accepted.SessionID, c.deps.OwnerID)
			if err != nil || !stateFound || state.SessionID != item.Accepted.SessionID || state.OwnerID != c.deps.OwnerID ||
				state.RuntimeVersion != conversationruntime.ConversationRuntimeV2 || state.GenerationEpoch != item.Accepted.GenerationEpoch {
				if err == nil {
					err = ErrBoardInboxCanonicalMismatch
				}
				return boardCommunicationServiceStep{}, err
			}
			request := conversationruntime.ReadViewRequest{
				Principal: conversationruntime.Principal{OwnerID: c.deps.OwnerID, ActorID: item.Accepted.ActorID},
				Session: conversationruntime.SessionState{
					SessionID: state.SessionID, OwnerID: state.OwnerID, RuntimeVersion: state.RuntimeVersion,
					CutoverSeq: state.CutoverSeq, AdmissionSeq: state.AdmissionSeq,
					ProjectionWatermark: state.ProjectionWatermark, GenerationEpoch: state.GenerationEpoch,
				},
				Accepted: toConversationAcceptedTurn(item.Accepted),
			}
			result := c.ownerMio.RunAcceptedBoardOwnerRequest(ctx, conversationruntime.AcceptedTurnInput{ReadViewRequest: request})
			if result.Status != conversationruntime.BoardOwnerRequestStatusCommitted {
				if result.Status != conversationruntime.BoardOwnerRequestStatusFailedBeforeCommit &&
					result.Status != conversationruntime.BoardOwnerRequestStatusCommitUncertain {
					return boardCommunicationServiceStep{}, fmt.Errorf("owner Mio processing ended in state %q", result.Status)
				}
				retryAt := c.deps.Now().UTC().Add(5 * time.Second)
				ownerRetryDue = earliestBoardCommunicationDue(ownerRetryDue, retryAt)
				continue
			}
		}
		if len(pending.Items) < boardOwnerPendingPageSize {
			c.ownerPendingAfter = 0
			ownerScanHasMore = false
			break
		}
		if pending.NextAdmissionSeq <= c.ownerPendingAfter {
			return boardCommunicationServiceStep{}, errBoardOwnerHTTPConflict
		}
		c.ownerPendingAfter = pending.NextAdmissionSeq
		ownerScanHasMore = true
	}
	if ownerScanHasMore {
		c.deps.Service.NotifyDurableAcceptance()
	}
	publicationProgressed, publicationErr := runBoardPublicationDelivery(ctx, c.boardClient, c.deps.Store, boardPublicationDeliveryInput{
		Scope: scope, Now: c.deps.Now().UTC(), LeaseDuration: boardPublicationLeaseDuration,
	})
	if publicationErr != nil {
		var retryScheduled *boardPublicationRetryScheduledError
		if !errors.As(publicationErr, &retryScheduled) || ctx.Err() != nil ||
			errors.Is(publicationErr, boardcommunication.ErrConnectionIdentityMismatch) {
			return boardCommunicationServiceStep{}, publicationErr
		}
	}
	if publicationProgressed {
		c.deps.Service.NotifyPublicationSettled()
	}
	publicationDue, err := c.nextBoardPublicationDue(ctx, scope, c.deps.Now().UTC())
	if err != nil {
		return boardCommunicationServiceStep{}, err
	}
	if !ownerRetryDue.IsZero() {
		return boardCommunicationServiceStep{NextDueAt: earliestBoardCommunicationDue(inboxDue, publicationDue, ownerRetryDue)}, nil
	}
	return boardCommunicationServiceStep{NextDueAt: earliestBoardCommunicationDue(inboxDue, publicationDue)}, nil
}

func (c *boardCommunicationComposition) validateResponderBinding(binding domainconversation.ExternalPeerBinding) error {
	if c == nil {
		return ErrBoardCommunicationCompositionUnavailable
	}
	actorID := "mio"
	if c.peerResponder != nil {
		actorID = string(c.peerResponder.Actor())
	}
	if binding.EffectiveResponderActorID() != actorID {
		return ErrBoardInboxCanonicalMismatch
	}
	if actorID == "mio" {
		if binding.ResponderActorID != "" {
			return ErrBoardInboxCanonicalMismatch
		}
		return nil
	}
	if binding.ResponderActorID != actorID || binding.RecipientPrincipalID != actorID {
		return ErrBoardInboxCanonicalMismatch
	}
	return nil
}

const (
	boardPublicationLeaseDuration     = time.Minute
	boardPeerPublicationReserve       = 15 * time.Second
	boardOwnerPendingPageSize         = 16
	boardOwnerPendingMaxPagesPerSweep = 4
)

type boardCommunicationSynchronousCanonicalSessionOwner interface {
	LoadOrCreateCanonicalForOwnerSynchronously(context.Context, string, domainconversation.ChannelAddress, string, time.Time) (session.CanonicalSessionOwnerResolution, error)
}

type boardCommunicationCanonicalTailReader struct {
	store interface {
		ReadCompleteSessionTail(context.Context, modulecore.SessionID, modulecore.EventSeq) (eventstore.CanonicalTail, error)
	}
}

func (r boardCommunicationCanonicalTailReader) ReadCompleteSessionTail(ctx context.Context, sessionID modulecore.SessionID, after modulecore.EventSeq) (conversationruntime.CanonicalTail, error) {
	if r.store == nil {
		return conversationruntime.CanonicalTail{}, ErrBoardCommunicationCompositionUnavailable
	}
	tail, err := r.store.ReadCompleteSessionTail(ctx, sessionID, after)
	if err != nil {
		return conversationruntime.CanonicalTail{}, err
	}
	result := conversationruntime.CanonicalTail{
		ThroughSeq: tail.ThroughSeq, Complete: tail.Complete,
		Messages: make([]conversationruntime.CanonicalMessage, 0, len(tail.Messages)),
	}
	for _, message := range tail.Messages {
		result.Messages = append(result.Messages, conversationruntime.CanonicalMessage{
			Event: message.Event, Index: toConversationMessageIndex(message.Index),
		})
	}
	return result, nil
}

func resolveBoardCanonicalSession(
	ctx context.Context,
	sessions boardCommunicationCanonicalSessionOwner,
	logicalDate string,
	address domainconversation.ChannelAddress,
	ownerID string,
	createdAt time.Time,
) (session.CanonicalSessionOwnerResolution, error) {
	if synchronous, ok := sessions.(boardCommunicationSynchronousCanonicalSessionOwner); ok {
		return synchronous.LoadOrCreateCanonicalForOwnerSynchronously(ctx, logicalDate, address, ownerID, createdAt)
	}
	return sessions.LoadOrCreateCanonicalForOwner(ctx, logicalDate, address, ownerID, createdAt)
}

func validBoardOwnerHTTPRequestForComposition(request boardOwnerHTTPRequest, channels, peers []string) bool {
	return validBoardOwnerHTTPValue(request.ChannelID, 512) && validBoardOwnerHTTPValue(request.PeerID, 512) &&
		validBoardOwnerHTTPValue(request.Title, 2048) && validBoardOwnerHTTPValue(request.IdempotencyKey, 512) &&
		validBoardOwnerHTTPInstruction(request.Instruction) && containsBoardInboxID(channels, request.ChannelID) &&
		containsBoardInboxID(peers, request.PeerID)
}

func boardOwnerRequestHTTPError(err error) error {
	switch {
	case errors.Is(err, eventstore.ErrBoardOwnerRequestConflict):
		return errBoardOwnerHTTPConflict
	case errors.Is(err, eventstore.ErrBoardOwnerRequestPinMismatch), errors.Is(err, eventstore.ErrBoardOwnerRequestScopeMismatch):
		return errBoardOwnerHTTPNotFound
	default:
		return err
	}
}

func boardOwnerRequestSessionAddress(source conversationruntime.BoardOwnerRequestSource) (domainconversation.ChannelAddress, error) {
	encoded, err := json.Marshal(struct {
		BoardInstanceID string `json:"board_instance_id"`
		CorePrincipalID string `json:"core_principal_id"`
		OwnerID         string `json:"owner_id"`
		IdempotencyKey  string `json:"idempotency_key"`
	}{source.BoardInstanceID, source.CorePrincipalID, source.OwnerID, source.IdempotencyKey})
	if err != nil {
		return domainconversation.ChannelAddress{}, err
	}
	return domainconversation.NewChannelAddress("board", "owner-request:"+base64.RawURLEncoding.EncodeToString(encoded))
}

func boardPeerThreadSessionAddress(key eventstore.BoardPeerThreadSessionKey) (domainconversation.ChannelAddress, error) {
	encoded, err := json.Marshal(struct {
		BoardInstanceID  string `json:"board_instance_id"`
		CorePrincipalID  string `json:"core_principal_id"`
		ChannelID        string `json:"channel_id"`
		ThreadID         string `json:"thread_id"`
		ResponderActorID string `json:"responder_actor_id,omitempty"`
	}{key.Scope.JournalInstanceID, key.Scope.PrincipalID, key.ChannelID, key.ThreadID, key.ResponderActorID})
	if err != nil {
		return domainconversation.ChannelAddress{}, err
	}
	return domainconversation.NewChannelAddress("board", "thread:"+base64.RawURLEncoding.EncodeToString(encoded))
}

func boardPublicationScope(pin eventstore.BoardConnectionPin) eventstore.BoardPublicationScope {
	return eventstore.BoardPublicationScope{
		OwnerID: pin.OwnerID, JournalInstanceID: pin.JournalInstanceID, PrincipalID: pin.PrincipalID,
	}
}

func sameBoardCommunicationPin(left, right eventstore.BoardConnectionPin) bool {
	return left.OwnerID == right.OwnerID && left.JournalInstanceID == right.JournalInstanceID &&
		left.PrincipalID == right.PrincipalID && !left.CutoverAt.IsZero() && left.CutoverAt.Equal(right.CutoverAt)
}

func boardOwnerHTTPStatusReceiptFromReadback(readback eventstore.BoardOwnerRequestReadback) (boardOwnerHTTPStatusReceipt, error) {
	receipt := boardOwnerHTTPStatusReceipt{
		OwnerID: readback.OwnerID, SessionID: readback.SessionID, TurnID: readback.TurnID,
		TraceID: readback.TraceID, InputMessageID: readback.InputMessageID,
		Stage: readback.Stage, AcceptedAt: readback.AcceptedAt.UTC(), UpdatedAt: readback.UpdatedAt.UTC(), Durable: true,
	}
	if !validBoardOwnerHTTPStatusReceipt(receipt) {
		return boardOwnerHTTPStatusReceipt{}, errBoardOwnerHTTPNotFound
	}
	return receipt, nil
}

type boardOwnerHTTPStatusStore interface {
	ReadBoardConnectionPin(context.Context) (eventstore.BoardConnectionPin, bool, error)
	ReadBoardOwnerRequestStatus(context.Context, eventstore.BoardPublicationScope, modulecore.TurnID) (eventstore.BoardOwnerRequestReadback, bool, error)
}

func readBoardOwnerHTTPStatus(
	ctx context.Context,
	store boardOwnerHTTPStatusStore,
	ownerID string,
	turnID modulecore.TurnID,
	expectedPrincipalID string,
	expectedCutoverAt time.Time,
) (boardOwnerHTTPStatusReceipt, bool, error) {
	if ctx == nil || store == nil || ownerID == "" || turnID.Validate() != nil || expectedPrincipalID == "" || expectedCutoverAt.IsZero() {
		return boardOwnerHTTPStatusReceipt{}, false, errBoardOwnerHTTPNotFound
	}
	pin, found, err := store.ReadBoardConnectionPin(ctx)
	if err != nil || !found {
		return boardOwnerHTTPStatusReceipt{}, found, err
	}
	if pin.OwnerID != ownerID || pin.PrincipalID != expectedPrincipalID || !pin.CutoverAt.Equal(expectedCutoverAt) {
		return boardOwnerHTTPStatusReceipt{}, false, errBoardOwnerHTTPNotFound
	}
	scope := boardPublicationScope(pin)
	readback, found, err := store.ReadBoardOwnerRequestStatus(ctx, scope, turnID)
	if err != nil || !found {
		return boardOwnerHTTPStatusReceipt{}, found, err
	}
	if readback.Scope != scope || readback.OwnerID != ownerID || readback.TurnID != turnID {
		return boardOwnerHTTPStatusReceipt{}, false, errBoardOwnerHTTPNotFound
	}
	receipt, err := boardOwnerHTTPStatusReceiptFromReadback(readback)
	if err != nil {
		return boardOwnerHTTPStatusReceipt{}, false, err
	}
	return receipt, true, nil
}

func boardPeerRequestSource(pin eventstore.BoardConnectionPin, source boardInboxSource) (eventstore.BoardPeerRequestSource, error) {
	binding := source.Binding
	binding.Classification = domainconversation.ExternalPeerClassificationRequest
	messageAt, err := time.Parse(time.RFC3339Nano, source.Message.At)
	if err != nil || messageAt.IsZero() || source.ThreadCreatedAt.IsZero() || source.Item.CreatedAt.IsZero() ||
		source.Message.ID != source.Item.MessageID || source.Message.By != source.Item.SenderID ||
		source.Message.Body != source.Item.Body || source.Message.ChannelID != source.Item.ChannelID ||
		source.Message.ThreadID != source.Item.ThreadID || source.Message.ReplyTo != "" ||
		!isBoardInboxRequest(source.Message) || pin.OwnerID == "" ||
		!source.Item.CreatedAt.After(pin.CutoverAt) || source.Item.SenderID == pin.PrincipalID {
		return eventstore.BoardPeerRequestSource{}, ErrBoardInboxCanonicalMismatch
	}
	if err := binding.ValidateNewRequest(); err != nil || binding.BoardInstanceID != pin.JournalInstanceID ||
		binding.RecipientPrincipalID != pin.PrincipalID || binding.ChannelID != source.Item.ChannelID ||
		binding.ThreadID != source.Item.ThreadID || binding.MessageID != source.Item.MessageID ||
		binding.SenderPrincipalID != source.Item.SenderID ||
		!containsBoardInboxID(source.Item.Recipients, pin.PrincipalID) {
		return eventstore.BoardPeerRequestSource{}, ErrBoardInboxCanonicalMismatch
	}
	return eventstore.BoardPeerRequestSource{
		Scope: boardPublicationScope(pin), Binding: binding, InboxItemID: source.Item.ItemID,
		InboxCreatedAt: source.Item.CreatedAt.UTC(), MessageAt: messageAt.UTC(), ThreadCreatedAt: source.ThreadCreatedAt.UTC(),
		Body: source.Message.Body, Recipients: append([]string(nil), source.Item.Recipients...),
		Mentions: append([]string(nil), source.Item.Mentions...), CausationID: source.Message.CausationID,
		HopCount: source.Message.HopCount,
	}, nil
}

func boardTerminalReplySource(pin eventstore.BoardConnectionPin, source boardInboxSource) (eventstore.BoardTerminalReplySource, error) {
	binding := source.Binding
	binding.Classification = domainconversation.ExternalPeerClassificationCorrelatedReplyTerminal
	messageAt, err := time.Parse(time.RFC3339Nano, source.Message.At)
	if err != nil || messageAt.IsZero() || source.ThreadCreatedAt.IsZero() || source.Item.CreatedAt.IsZero() ||
		!isBoardInboxReplyCandidate(source.Message) || source.Message.ID != source.Item.MessageID ||
		source.Message.By != source.Item.SenderID || source.Message.ChannelID != source.Item.ChannelID ||
		source.Message.ThreadID != source.Item.ThreadID || !containsBoardInboxID(source.Item.Recipients, pin.PrincipalID) {
		return eventstore.BoardTerminalReplySource{}, ErrBoardInboxCanonicalMismatch
	}
	if err := binding.Validate(); err != nil || binding.BoardInstanceID != pin.JournalInstanceID ||
		binding.RecipientPrincipalID != pin.PrincipalID || binding.ChannelID != source.Item.ChannelID ||
		binding.ThreadID != source.Item.ThreadID || binding.MessageID != source.Item.MessageID ||
		binding.SenderPrincipalID != source.Item.SenderID {
		return eventstore.BoardTerminalReplySource{}, ErrBoardInboxCanonicalMismatch
	}
	return eventstore.BoardTerminalReplySource{
		Scope: boardPublicationScope(pin), Binding: binding, InboxItemID: source.Item.ItemID,
		InboxCreatedAt: source.Item.CreatedAt.UTC(), MessageAt: messageAt.UTC(), ThreadCreatedAt: source.ThreadCreatedAt.UTC(),
		Body: source.Message.Body, Recipients: append([]string(nil), source.Item.Recipients...),
		Mentions: append([]string(nil), source.Item.Mentions...), CausationID: source.Message.CausationID,
		HopCount: source.Message.HopCount,
	}, nil
}

func earliestBoardCommunicationDue(values ...time.Time) time.Time {
	var earliest time.Time
	for _, value := range values {
		if value.IsZero() {
			continue
		}
		if earliest.IsZero() || value.Before(earliest) {
			earliest = value
		}
	}
	return earliest
}

var _ boardInboxOwner = boardCommunicationPeerOwner{}
