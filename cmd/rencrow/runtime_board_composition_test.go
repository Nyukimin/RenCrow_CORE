package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
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

type boardCompositionStoreFake struct {
	boardCommunicationCompositionStore
	steps                     *[]string
	pin                       eventstore.BoardConnectionPin
	sessionState              eventstore.SessionState
	stateFound                bool
	reservation               eventstore.BoardOwnerRequestReservation
	reserveInput              eventstore.BoardOwnerRequestReservationInput
	acceptInput               eventstore.BoardOwnerRequestAcceptInput
	accepted                  eventstore.AcceptedTurn
	publicationLookup         eventstore.BoardPublicationTurnLookupInput
	publicationOutcome        eventstore.BoardPublicationOutcome
	publicationFound          bool
	publicationLookupErr      error
	publicationLookupCalls    int
	publicationDueCalls       int
	responderPublicationDue   int
	responderPublicationActor string
	publicationDueTime        time.Time
}

func (s *boardCompositionStoreFake) ReadBoardConnectionPin(context.Context) (eventstore.BoardConnectionPin, bool, error) {
	return s.pin, s.pin.OwnerID != "", nil
}

func (s *boardCompositionStoreFake) ReadSessionState(context.Context, modulecore.SessionID, string) (eventstore.SessionState, bool, error) {
	return s.sessionState, s.stateFound, nil
}

func (s *boardCompositionStoreFake) LoadBoardPublicationByTurn(_ context.Context, input eventstore.BoardPublicationTurnLookupInput) (eventstore.BoardPublicationOutcome, bool, error) {
	s.publicationLookupCalls++
	s.publicationLookup = input
	return s.publicationOutcome, s.publicationFound, s.publicationLookupErr
}

func (s *boardCompositionStoreFake) NextBoardPublicationDue(context.Context, eventstore.BoardPublicationScope, time.Time) (time.Time, error) {
	s.publicationDueCalls++
	return s.publicationDueTime, nil
}

func (s *boardCompositionStoreFake) NextBoardPublicationDueForResponder(_ context.Context, _ eventstore.BoardPublicationScope, actorID string, _ time.Time) (time.Time, error) {
	s.responderPublicationDue++
	s.responderPublicationActor = actorID
	return s.publicationDueTime, nil
}

func (s *boardCompositionStoreFake) ReserveBoardOwnerRequest(_ context.Context, input eventstore.BoardOwnerRequestReservationInput) (eventstore.BoardOwnerRequestReservation, error) {
	*s.steps = append(*s.steps, "reserve")
	s.reserveInput = input
	s.reservation.Source = input.Source
	s.reservation.ActorID = input.ActorID
	s.reservation.InstructionSHA256 = "synthetic-instruction-hash"
	s.reservation.AdmittedAt = input.Now.UTC()
	return s.reservation, nil
}

func (s *boardCompositionStoreFake) AcceptBoardOwnerRequest(_ context.Context, input eventstore.BoardOwnerRequestAcceptInput) (eventstore.AcceptedTurn, error) {
	*s.steps = append(*s.steps, "accept")
	s.acceptInput = input
	s.accepted.SessionID = input.SessionID
	s.accepted.GenerationEpoch = input.GenerationEpoch
	s.accepted.ActorID = input.ActorID
	s.accepted.TriggerKind = "board_owner_request"
	s.accepted.State = "accepted"
	return s.accepted, nil
}

type boardCompositionSessionOwnerFake struct {
	steps      *[]string
	resolution session.CanonicalSessionOwnerResolution
	logicalDay string
	address    domainconversation.ChannelAddress
	ownerID    string
	createdAt  time.Time
}

func (r *boardCompositionSessionOwnerFake) LoadOrCreateCanonicalForOwner(_ context.Context, logicalDate string, address domainconversation.ChannelAddress, ownerID string, createdAt time.Time) (session.CanonicalSessionOwnerResolution, error) {
	*r.steps = append(*r.steps, "session")
	r.logicalDay, r.address, r.ownerID, r.createdAt = logicalDate, address, ownerID, createdAt
	return r.resolution, nil
}

type boardCompositionConversationFake struct{ calls int }

func (p *boardCompositionConversationFake) GenerateConversation(context.Context, domainconversation.ConversationScope, llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.calls++
	return llm.GenerateResponse{}, nil
}

func (*boardCompositionConversationFake) Name() string { return "board-composition-conversation-fake" }

func boardCompositionTestDependencies(t *testing.T, steps *[]string) boardCommunicationCompositionDeps {
	t.Helper()
	now := time.Date(2026, 11, 5, 4, 5, 6, 0, time.UTC)
	address, err := domainconversation.NewChannelAddress("board-owner", "board-owner:synthetic-request-scope")
	if err != nil {
		t.Fatalf("NewChannelAddress: %v", err)
	}
	canonical, err := session.NewCanonicalSessionWithOwner(modulecore.NewSessionID(), "owner-alpha", now.Format("2006-01-02"), address, now)
	if err != nil {
		t.Fatalf("NewCanonicalSessionWithOwner: %v", err)
	}
	traceID := modulecore.NewTraceID()
	messageID := modulecore.NewMessageID()
	turnID := modulecore.NewTurnID()
	event := modulecore.NewEventEnvelope(traceID, "", nil, "orchestrator", "conversation.board_owner_request", now, map[string]any{})
	event.SessionID, event.TurnID, event.MessageID = modulecore.SessionID(canonical.ID()), turnID, messageID
	store := &boardCompositionStoreFake{
		steps: steps,
		accepted: eventstore.AcceptedTurn{SessionID: modulecore.SessionID(canonical.ID()), TurnID: turnID,
			InputMessageID: messageID, InputEvent: event, State: "accepted", TriggerKind: "board_owner_request"},
	}
	store.reservation = eventstore.BoardOwnerRequestReservation{AdmittedAt: now}
	sessions := &boardCompositionSessionOwnerFake{
		steps:      steps,
		resolution: session.CanonicalSessionOwnerResolution{Session: canonical, BoundOwnerID: "owner-alpha", Status: session.CanonicalSessionOwnerCreated},
	}
	pin := boardInboxTestPin()
	store.pin = pin
	wire := newBoardInboxWireHarness(t, pin)
	service := newBoardCommunicationService(boardCommunicationServiceDeps{enabled: true})
	return boardCommunicationCompositionDeps{
		Store: store, Sessions: sessions, Mio: (&agent.MioAgent{}).WithSystemPrompt("synthetic authored Mio persona"),
		Conversation: &boardCompositionConversationFake{}, Client: wire.client, Pin: pin,
		InboxScope: boardInboxTestScope(pin), Service: service, Now: func() time.Time { return now },
		OwnerID: pin.OwnerID, ActorID: "mio", AllowedChannelIDs: []string{"channel-alpha"}, AllowedPeerIDs: []string{"peer-alpha"},
	}
}

func TestBoardCommunicationCompositionOwnerAdmissionPrecedesDurableWake(t *testing.T) {
	steps := make([]string, 0, 4)
	deps := boardCompositionTestDependencies(t, &steps)
	deps.Service = newBoardCommunicationService(boardCommunicationServiceDeps{enabled: true})
	deps.Service.deps.process = func(context.Context) (boardCommunicationServiceStep, error) {
		steps = append(steps, "generation")
		return boardCommunicationServiceStep{}, nil
	}
	composition, err := newBoardCommunicationComposition(deps)
	if err != nil {
		t.Fatalf("newBoardCommunicationComposition: %v", err)
	}
	if composition.ownerMio == nil {
		t.Fatal("composition did not bind the dedicated Mio owner-request runtime")
	}
	request := boardOwnerHTTPRequest{ChannelID: "channel-alpha", PeerID: "peer-alpha", Title: "Synthetic title", Instruction: "Ask for confirmation", IdempotencyKey: "request-1"}
	receipt, err := composition.ownerHTTPCallbacks().Accept(context.Background(), deps.OwnerID, request)
	if err != nil {
		t.Fatalf("acceptOwnerRequest: %v", err)
	}
	want := []string{"reserve", "session", "accept"}
	if !reflect.DeepEqual(steps, want) {
		t.Fatalf("owner admission steps = %v, want %v; generation must wait for durable acceptance", steps, want)
	}
	select {
	case <-deps.Service.localWake:
	default:
		t.Fatal("durable owner acceptance did not coalesce a local wake")
	}
	if deps.Conversation.(*boardCompositionConversationFake).calls != 0 {
		t.Fatal("Mio provider was called before owner acceptance was durable")
	}
	store := deps.Store.(*boardCompositionStoreFake)
	sessions := deps.Sessions.(*boardCompositionSessionOwnerFake)
	if store.acceptInput.SessionID != modulecore.SessionID(sessions.resolution.Session.ID()) ||
		store.acceptInput.GenerationEpoch <= 0 || store.acceptInput.RuntimeVersion != conversationruntime.ConversationRuntimeV2 ||
		sessions.logicalDay != store.reservation.AdmittedAt.UTC().Format("2006-01-02") ||
		sessions.ownerID != deps.OwnerID || !sessions.createdAt.Equal(store.reservation.AdmittedAt.UTC()) {
		t.Fatalf("owner acceptance did not use the reserved instant and canonical Session: accept=%+v resolution=%+v reservation=%+v", store.acceptInput, sessions, store.reservation)
	}
	if !receipt.Durable || receipt.OwnerID != deps.OwnerID || receipt.SessionID != store.acceptInput.SessionID ||
		receipt.TurnID != store.accepted.TurnID || receipt.TraceID != store.accepted.InputEvent.TraceID ||
		receipt.InputMessageID != store.accepted.InputMessageID || !receipt.AcceptedAt.Equal(store.reservation.AdmittedAt) {
		t.Fatalf("owner HTTP acceptance receipt = %+v, not exact durable Store acceptance %+v", receipt, store.accepted)
	}
}

func TestBoardCommunicationCompositionAgentOwnerAdmissionUsesMeetingRouteAndLivePin(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*boardCommunicationCompositionDeps, *boardInboxWireHarness, *boardOwnerHTTPRequest)
		wantError bool
	}{
		{name: "meeting route authorizes peer outside legacy peer allowlist"},
		{name: "unlisted route peer", wantError: true, mutate: func(_ *boardCommunicationCompositionDeps, _ *boardInboxWireHarness, request *boardOwnerHTTPRequest) {
			request.PeerID = "muse-comm"
		}},
		{name: "unlisted route channel", wantError: true, mutate: func(_ *boardCommunicationCompositionDeps, _ *boardInboxWireHarness, request *boardOwnerHTTPRequest) {
			request.ChannelID = "channel-other"
		}},
		{name: "wrong requested actor", wantError: true, mutate: func(_ *boardCommunicationCompositionDeps, _ *boardInboxWireHarness, request *boardOwnerHTTPRequest) {
			request.ActorID = "shiro"
		}},
		{name: "rotated live principal", wantError: true, mutate: func(_ *boardCommunicationCompositionDeps, wire *boardInboxWireHarness, _ *boardOwnerHTTPRequest) {
			wire.identity.PrincipalID = "shiro"
		}},
		{name: "rotated live journal", wantError: true, mutate: func(_ *boardCommunicationCompositionDeps, wire *boardInboxWireHarness, _ *boardOwnerHTTPRequest) {
			wire.identity.JournalInstanceID = "journal_abcdef0123456789abcdef0123456789"
		}},
		{name: "persisted pin changed", wantError: true, mutate: func(deps *boardCommunicationCompositionDeps, _ *boardInboxWireHarness, _ *boardOwnerHTTPRequest) {
			deps.Store.(*boardCompositionStoreFake).pin.JournalInstanceID = "journal_abcdef0123456789abcdef0123456789"
		}},
		{name: "channel no longer in live contexts", wantError: true, mutate: func(_ *boardCommunicationCompositionDeps, wire *boardInboxWireHarness, _ *boardOwnerHTTPRequest) {
			wire.contexts = []boardcommunication.BoardContext{{ID: "channel-other", Label: "Other channel"}}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			steps := make([]string, 0, 4)
			deps, wire := boardCompositionMeetingTestDependencies(t, &steps)
			composition, err := newBoardCommunicationComposition(deps)
			if err != nil {
				t.Fatalf("newBoardCommunicationComposition: %v", err)
			}
			request := boardOwnerHTTPRequest{
				ActorID: "mio", ChannelID: domainconversation.BoardCommunicationMeetingChannelID,
				PeerID: "dots-comm", Title: "Meeting request", Instruction: "Ask for confirmation.", IdempotencyKey: "meeting-request-1",
			}
			if test.mutate != nil {
				test.mutate(&deps, wire, &request)
				composition.deps = deps
			}
			receipt, acceptErr := composition.ownerHTTPCallbacks().AgentAccept(context.Background(), deps.OwnerID, request)
			if test.wantError {
				if acceptErr == nil || len(steps) != 0 {
					t.Fatalf("AgentAccept() = receipt %+v, err %v, steps %v; want rejection before durable reservation", receipt, acceptErr, steps)
				}
				return
			}
			if acceptErr != nil || !receipt.Durable {
				t.Fatalf("AgentAccept() = receipt %+v, err %v; want durable acceptance", receipt, acceptErr)
			}
			store := deps.Store.(*boardCompositionStoreFake)
			if store.reserveInput.Source.TargetActorID != "mio" || store.reserveInput.Source.PeerPrincipalID != "dots-comm" ||
				store.reserveInput.ActorID != "mio" || store.acceptInput.ActorID != "mio" {
				t.Fatalf("new agent admission lost target/source binding: reserve=%+v accept=%+v", store.reserveInput, store.acceptInput)
			}
			if !reflect.DeepEqual(wire.trace.snapshot(), []string{"identity", "contexts"}) ||
				!reflect.DeepEqual(steps, []string{"reserve", "session", "accept"}) {
				t.Fatalf("agent acceptance order wire=%v durable=%v; want live identity and channel checks before durable admission", wire.trace.snapshot(), steps)
			}
		})
	}
}

func boardCompositionMeetingTestDependencies(t *testing.T, steps *[]string) (boardCommunicationCompositionDeps, *boardInboxWireHarness) {
	t.Helper()
	deps := boardCompositionTestDependencies(t, steps)
	pin := deps.Pin
	pin.PrincipalID = domainconversation.BoardCommunicationMeetingMioPrincipalID
	deps.Pin = pin
	deps.Store.(*boardCompositionStoreFake).pin = pin
	deps.InboxScope = boardInboxTestScope(pin)
	deps.InboxScope.AllowedChannelIDs = []string{domainconversation.BoardCommunicationMeetingChannelID}
	deps.InboxScope.AllowedPeerIDs = []string{"peer-alpha"} // legacy peer scope deliberately excludes the routed peer
	deps.InboxScope.MeetingRoutes = []domainconversation.BoardCommunicationMeetingRoute{{
		ChannelID:         domainconversation.BoardCommunicationMeetingChannelID,
		SenderPrincipalID: pin.PrincipalID, RecipientPrincipalID: "dots-comm",
	}}
	wire := newBoardInboxWireHarness(t, pin)
	wire.contexts = []boardcommunication.BoardContext{{ID: domainconversation.BoardCommunicationMeetingChannelID, Label: "Meeting"}}
	deps.Client = wire.client
	provider := deps.Conversation.(*boardCompositionConversationFake)
	ownerChat, err := agent.NewBoardPeerChatOwnerRequestRuntime(agent.BoardPeerChatActorMio, "authored Mio persona", provider)
	if err != nil {
		t.Fatalf("NewBoardPeerChatOwnerRequestRuntime(Mio): %v", err)
	}
	deps.OwnerRequestChat = ownerChat
	return deps, wire
}

func TestBoardCommunicationCompositionPeerHandoffUsesClaimAndWithholdsACKUntilB2(t *testing.T) {
	steps := make([]string, 0, 8)
	deps := boardCompositionTestDependencies(t, &steps)
	item := boardInboxTestItem("item-alpha", "message-alpha", deps.Pin.CutoverAt.Add(time.Minute), "Please confirm Friday.")
	wire := newBoardInboxWireHarness(t, deps.Pin)
	wire.thread = boardInboxTestThread(item, "request", "")
	wire.claimResults = []boardcommunication.ClaimResult{boardInboxClaimResult(item, "claimed")}
	wire.allowInboxItems(item.ItemID)
	deps.Client = wire.client
	var gotSource boardInboxSource
	var gotClaim boardInboxClaim
	var gotAccepted conversationruntime.ReadViewRequest
	deps.Peer = boardCommunicationPeerCallbacks{
		FindAccepted: func(context.Context, boardInboxSource) (conversationruntime.ReadViewRequest, bool, error) {
			steps = append(steps, "find")
			return conversationruntime.ReadViewRequest{}, false, nil
		},
		Accept: func(_ context.Context, source boardInboxSource) (conversationruntime.ReadViewRequest, error) {
			steps = append(steps, "durable_accept")
			return boardInboxTestAcceptedRequest(source, deps.OwnerID), nil
		},
		Process: func(_ context.Context, accepted conversationruntime.ReadViewRequest, source boardInboxSource, claim boardInboxClaim, mio *agent.MioAgent, provider boardCommunicationConversationProvider) (conversationruntime.TurnResult, error) {
			steps = append(steps, "mio_process")
			gotAccepted, gotSource, gotClaim = accepted, source, claim
			if mio != deps.Mio || provider != deps.Conversation {
				t.Fatalf("peer generator bindings = Mio %p/provider %p, want configured Mio %p/provider %p", mio, provider, deps.Mio, deps.Conversation)
			}
			return conversationruntime.TurnResult{Status: conversationruntime.TurnStatusDelivered, Committed: true}, nil
		},
		HasOutcome: func(context.Context, conversationruntime.ReadViewRequest) (bool, error) {
			steps = append(steps, "b2_outcome_pending")
			return false, nil
		},
		Correlate: func(context.Context, boardInboxSource) (bool, error) { return false, nil },
	}
	composition, err := newBoardCommunicationComposition(deps)
	if err != nil {
		t.Fatalf("newBoardCommunicationComposition: %v", err)
	}
	if err := handleBoardInboxItem(context.Background(), wire.client, composition.peerOwner, deps.InboxScope, item); err != nil {
		t.Fatalf("handleBoardInboxItem: %v", err)
	}
	if gotSource.Item.ItemID != item.ItemID || gotSource.Message.ID != item.MessageID ||
		gotAccepted.Accepted.TriggerKind != "external_peer_message" || gotClaim.ClaimID != boardInboxClaimID(deps.Pin, item) ||
		gotClaim.ClaimReceipt != "synthetic-claim-receipt" || wire.ackCalls != 0 {
		t.Fatalf("peer handoff/outcome gate mismatch: source=%+v accepted=%+v claim=%+v ACKs=%d", gotSource, gotAccepted.Accepted, gotClaim, wire.ackCalls)
	}
	if !reflect.DeepEqual(steps, []string{"find", "durable_accept", "mio_process", "b2_outcome_pending"}) {
		t.Fatalf("peer processing order = %v; ACK must wait for durable publication completion", steps)
	}
}

func TestBoardCommunicationCompositionSeparatesResponderSessionsAndFencesReplay(t *testing.T) {
	steps := make([]string, 0, 4)
	deps := boardCompositionTestDependencies(t, &steps)
	deps.Pin.PrincipalID = "shiro"
	deps.InboxScope.Pin = deps.Pin
	deps.InboxScope.ResponderActorID = "shiro"
	deps.UsePrincipalScopedPins = true
	provider := &boardCompositionConversationFake{}
	peerChat, err := agent.NewBoardPeerChatRuntime(agent.BoardPeerChatActorShiro, "authored Shiro persona", provider)
	if err != nil {
		t.Fatalf("NewBoardPeerChatRuntime(): %v", err)
	}
	deps.PeerChat = peerChat
	composition, err := newBoardCommunicationComposition(deps)
	if err != nil {
		t.Fatalf("newBoardCommunicationComposition(responder): %v", err)
	}
	if composition.ownerMio != nil || composition.peerMio == nil || composition.peerResponder != peerChat {
		t.Fatalf("responder composition routed through owner Mio or lost peer runtime: owner=%p peer=%p responder=%p", composition.ownerMio, composition.peerMio, composition.peerResponder)
	}

	legacyKey := eventstore.BoardPeerThreadSessionKey{
		Scope:     eventstore.BoardPublicationScope{OwnerID: "owner-alpha", JournalInstanceID: "journal_0123456789abcdef0123456789abcdef", PrincipalID: "principal-alpha"},
		ChannelID: "channel-alpha", ThreadID: "thread-alpha", ThreadCreatedAt: time.Date(2026, 11, 5, 3, 0, 0, 0, time.UTC),
	}
	legacyAddress, err := boardPeerThreadSessionAddress(legacyKey)
	if err != nil {
		t.Fatalf("boardPeerThreadSessionAddress(legacy): %v", err)
	}
	legacyPayload, err := json.Marshal(struct {
		BoardInstanceID string `json:"board_instance_id"`
		CorePrincipalID string `json:"core_principal_id"`
		ChannelID       string `json:"channel_id"`
		ThreadID        string `json:"thread_id"`
	}{legacyKey.Scope.JournalInstanceID, legacyKey.Scope.PrincipalID, legacyKey.ChannelID, legacyKey.ThreadID})
	if err != nil {
		t.Fatalf("marshal legacy peer session key: %v", err)
	}
	wantLegacyAddress, err := domainconversation.NewChannelAddress("board", "thread:"+base64.RawURLEncoding.EncodeToString(legacyPayload))
	if err != nil || legacyAddress != wantLegacyAddress {
		t.Fatalf("legacy peer address changed: got=%+v want=%+v err=%v", legacyAddress, wantLegacyAddress, err)
	}
	responderKey := legacyKey
	responderKey.Scope.PrincipalID = "shiro"
	responderKey.ResponderActorID = "shiro"
	responderAddress, err := boardPeerThreadSessionAddress(responderKey)
	if err != nil || responderAddress == legacyAddress {
		t.Fatalf("explicit Shiro session address = %+v, legacy=%+v, err=%v; want isolated key", responderAddress, legacyAddress, err)
	}

	wrongSource := boardInboxSource{Binding: domainconversation.ExternalPeerBinding{
		BoardInstanceID: deps.Pin.JournalInstanceID, RecipientPrincipalID: "midori", ChannelID: "channel-alpha",
		ThreadID: "thread-alpha", MessageID: "message-alpha", SenderPrincipalID: "peer-alpha",
		ResponderActorID: "midori", WireKind: "request", Classification: domainconversation.ExternalPeerClassificationRequest,
	}}
	claim := boardInboxClaim{LeaseUntil: time.Now().UTC().Add(time.Minute)}
	_, err = composition.processClaimedPeerRequest(context.Background(), conversationruntime.ReadViewRequest{}, wrongSource, claim, nil, nil)
	if !errors.Is(err, ErrBoardInboxCanonicalMismatch) || provider.calls != 0 {
		t.Fatalf("cross-actor replay error=%v provider calls=%d; want fence before any replay lookup or generation", err, provider.calls)
	}
}

func TestBoardCommunicationCompositionScopesDurablePeerOutcomeToTrustedResponder(t *testing.T) {
	steps := make([]string, 0, 2)
	deps := boardCompositionTestDependencies(t, &steps)
	deps.Pin.PrincipalID = "shiro"
	deps.InboxScope.Pin = deps.Pin
	deps.InboxScope.ResponderActorID = "shiro"
	deps.UsePrincipalScopedPins = true
	provider := &boardCompositionConversationFake{}
	peerChat, err := agent.NewBoardPeerChatRuntime(agent.BoardPeerChatActorShiro, "authored Shiro persona", provider)
	if err != nil {
		t.Fatalf("NewBoardPeerChatRuntime(): %v", err)
	}
	deps.PeerChat = peerChat
	composition, err := newBoardCommunicationComposition(deps)
	if err != nil {
		t.Fatalf("newBoardCommunicationComposition(responder): %v", err)
	}
	store := deps.Store.(*boardCompositionStoreFake)
	accepted := boardCompositionAcceptedPeerRequest(t, deps, "shiro")
	store.publicationOutcome = boardCompositionPublicationOutcome(deps.Pin, "message-alpha", "response-alpha")
	store.publicationFound = true

	found, err := composition.hasDurablePeerOutcome(context.Background(), accepted)
	if err != nil || !found {
		t.Fatalf("hasDurablePeerOutcome(shiro) = %t, %v; want durable outcome", found, err)
	}
	if store.publicationLookupCalls != 1 || store.publicationLookup.ResponderActorID != "shiro" ||
		store.publicationLookup.Scope != boardPublicationScope(deps.Pin) ||
		store.publicationLookup.SessionID != accepted.Accepted.SessionID || store.publicationLookup.TurnID != accepted.Accepted.TurnID {
		t.Fatalf("publication lookup = %+v calls=%d; want the validated Shiro actor and exact turn", store.publicationLookup, store.publicationLookupCalls)
	}

	store.publicationLookupCalls = 0
	wrongActor := boardCompositionAcceptedPeerRequest(t, deps, "midori")
	found, err = composition.hasDurablePeerOutcome(context.Background(), wrongActor)
	if !errors.Is(err, ErrBoardInboxCanonicalMismatch) || found || store.publicationLookupCalls != 0 {
		t.Fatalf("cross-responder outcome lookup = %t, %v calls=%d; want pre-lookup actor fence", found, err, store.publicationLookupCalls)
	}
}

func TestBoardCommunicationCompositionUsesResponderScopedPublicationDue(t *testing.T) {
	now := time.Date(2026, 11, 5, 4, 5, 6, 0, time.UTC)
	steps := make([]string, 0, 2)
	responderDeps := boardCompositionTestDependencies(t, &steps)
	responderDeps.Pin.PrincipalID = "kuro"
	responderDeps.InboxScope.Pin = responderDeps.Pin
	responderDeps.InboxScope.ResponderActorID = "kuro"
	responderDeps.UsePrincipalScopedPins = true
	provider := &boardCompositionConversationFake{}
	peerChat, err := agent.NewBoardPeerChatRuntime(agent.BoardPeerChatActorKuro, "authored Kuro persona", provider)
	if err != nil {
		t.Fatalf("NewBoardPeerChatRuntime(): %v", err)
	}
	responderDeps.PeerChat = peerChat
	responder, err := newBoardCommunicationComposition(responderDeps)
	if err != nil {
		t.Fatalf("newBoardCommunicationComposition(responder): %v", err)
	}
	responderStore := responderDeps.Store.(*boardCompositionStoreFake)
	if _, err := responder.nextBoardPublicationDue(context.Background(), boardPublicationScope(responderDeps.Pin), now); err != nil {
		t.Fatalf("nextBoardPublicationDue(responder): %v", err)
	}
	if responderStore.responderPublicationDue != 1 || responderStore.responderPublicationActor != "kuro" || responderStore.publicationDueCalls != 0 {
		t.Fatalf("responder due calls scoped=%d actor=%q legacy=%d; want Kuro-only query", responderStore.responderPublicationDue, responderStore.responderPublicationActor, responderStore.publicationDueCalls)
	}

	legacyDeps := boardCompositionTestDependencies(t, &steps)
	legacy, err := newBoardCommunicationComposition(legacyDeps)
	if err != nil {
		t.Fatalf("newBoardCommunicationComposition(legacy): %v", err)
	}
	legacyStore := legacyDeps.Store.(*boardCompositionStoreFake)
	if _, err := legacy.nextBoardPublicationDue(context.Background(), boardPublicationScope(legacyDeps.Pin), now); err != nil {
		t.Fatalf("nextBoardPublicationDue(legacy): %v", err)
	}
	if legacyStore.publicationDueCalls != 1 || legacyStore.responderPublicationDue != 0 {
		t.Fatalf("legacy due calls=%d responder=%d; want unchanged legacy query", legacyStore.publicationDueCalls, legacyStore.responderPublicationDue)
	}
}

func boardCompositionAcceptedPeerRequest(t *testing.T, deps boardCommunicationCompositionDeps, recipient string) conversationruntime.ReadViewRequest {
	t.Helper()
	binding := domainconversation.ExternalPeerBinding{
		BoardInstanceID: deps.Pin.JournalInstanceID, RecipientPrincipalID: recipient,
		ChannelID: "channel-alpha", ThreadID: "thread-alpha", MessageID: "message-alpha",
		SenderPrincipalID: "peer-alpha", WireKind: "request", Classification: domainconversation.ExternalPeerClassificationRequest,
	}
	if domainconversation.IsExternalPeerResponderActor(recipient) {
		binding.ResponderActorID = recipient
	}
	actorID, err := binding.SourceActorID()
	if err != nil {
		t.Fatalf("ExternalPeerBinding.SourceActorID(): %v", err)
	}
	sessionID, turnID := modulecore.NewSessionID(), modulecore.NewTurnID()
	input := modulecore.NewEventEnvelope(modulecore.NewTraceID(), "", nil, "orchestrator", "conversation.external_peer_message", time.Now().UTC(), map[string]any{
		"external_peer_binding": binding.ToPayload(),
	})
	input.SessionID, input.TurnID, input.MessageID = sessionID, turnID, modulecore.NewMessageID()
	return conversationruntime.ReadViewRequest{
		Principal: conversationruntime.Principal{OwnerID: deps.OwnerID, ActorID: "mio"},
		Session:   conversationruntime.SessionState{SessionID: sessionID, OwnerID: deps.OwnerID, GenerationEpoch: 1},
		Accepted: conversationruntime.AcceptedTurn{
			ActorID: actorID, SessionID: sessionID, TurnID: turnID, GenerationEpoch: 1,
			TriggerKind: "external_peer_message", InputEvent: input,
		},
	}
}

func boardCompositionPublicationOutcome(pin eventstore.BoardConnectionPin, requestID, responseID string) eventstore.BoardPublicationOutcome {
	return eventstore.BoardPublicationOutcome{
		Scope: boardPublicationScope(pin), WorkID: "work-alpha", ExpectedPeerPrincipalID: "peer-alpha",
		SendReceipt: eventstore.BoardPublicationSendReceipt{MessageID: responseID},
		CanonicalMessage: eventstore.BoardPublicationCanonicalMessage{
			MessageID: responseID, ThreadID: "thread-alpha", ChannelID: "channel-alpha",
			SenderPrincipalID: pin.PrincipalID, Recipients: []string{"peer-alpha"}, ReplyTo: requestID,
		},
	}
}
