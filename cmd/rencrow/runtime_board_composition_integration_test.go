package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainsession "github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	sessionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/session"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const boardCompositionIntegrationEmission = "Could you confirm the synthetic maintenance window?"

type boardCompositionIntegrationConversation struct {
	calls   int
	scope   domainconversation.ConversationScope
	request llm.GenerateRequest
}

func (p *boardCompositionIntegrationConversation) GenerateConversation(_ context.Context, scope domainconversation.ConversationScope, request llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.calls++
	p.scope = scope
	p.request = request
	return llm.GenerateResponse{
		Content:    `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"external_peer","text":"` + boardCompositionIntegrationEmission + `"}}`,
		TokensUsed: 19,
		RequestID:  modulecore.NewRequestID(),
		ResponseID: modulecore.NewResponseID(),
	}, nil
}

func TestBoardCommunicationCompositionRecoversOrphanOwnerSessionAndRunsRealMio(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "board-composition.sqlite")
	sessionDirectory := t.TempDir()
	store, err := eventstore.NewSQLiteStore(databasePath)
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	defer func() {
		if store != nil {
			if err := store.Close(); err != nil {
				t.Errorf("Close(): %v", err)
			}
		}
	}()

	pin := boardConnectionTestPin()
	if got, err := store.PinBoardConnection(ctx, pin); err != nil || got != pin {
		t.Fatalf("PinBoardConnection(valid integration pin) = %#v, %v; want exact durable pin", got, err)
	}
	now := time.Date(2026, 11, 5, 4, 5, 6, 0, time.UTC)
	request := boardOwnerHTTPRequest{
		ChannelID: "channel-alpha", PeerID: "peer-alpha", Title: "Synthetic maintenance window",
		Instruction: "Ask the peer to confirm the synthetic maintenance window.", IdempotencyKey: "composition-integration-key-1",
	}
	reservationInput := eventstore.BoardOwnerRequestReservationInput{
		Source: conversationruntime.BoardOwnerRequestSource{
			OwnerID: pin.OwnerID, BoardInstanceID: pin.JournalInstanceID, CorePrincipalID: pin.PrincipalID,
			ChannelID: request.ChannelID, PeerPrincipalID: request.PeerID, Title: request.Title, IdempotencyKey: request.IdempotencyKey,
		},
		ActorID: "mio", Instruction: request.Instruction, Now: now,
	}
	orphan, err := store.ReserveBoardOwnerRequest(ctx, reservationInput)
	if err != nil {
		t.Fatalf("ReserveBoardOwnerRequest(orphan before owner acceptance): %v", err)
	}
	pendingBefore, err := store.ReadPendingBoardOwnerRequests(ctx, boardPublicationScope(pin), 0, 16)
	if err != nil || len(pendingBefore.Items) != 0 {
		t.Fatalf("pending requests before orphan recovery = %d, err %v; want a durable reservation with no accepted turn", len(pendingBefore.Items), err)
	}

	sessions := sessionpersistence.NewJSONSessionRepository(sessionDirectory)
	address, err := boardOwnerRequestSessionAddress(orphan.Source)
	if err != nil {
		t.Fatalf("boardOwnerRequestSessionAddress(orphan): %v", err)
	}
	orphanSession, err := sessions.LoadOrCreateCanonicalForOwnerSynchronously(
		ctx, orphan.AdmittedAt.Format("2006-01-02"), address, pin.OwnerID, orphan.AdmittedAt,
	)
	if err != nil || orphanSession.Status != domainsession.CanonicalSessionOwnerCreated || orphanSession.Session == nil || orphanSession.BoundOwnerID != pin.OwnerID {
		t.Fatalf("pre-acceptance canonical JSON Session = status %q session %v owner %q err %v; want a durable owner-bound orphan", orphanSession.Status, orphanSession.Session, orphanSession.BoundOwnerID, err)
	}
	orphanSessionID := modulecore.SessionID(orphanSession.Session.ID())
	if _, found, err := store.ReadSessionState(ctx, orphanSessionID, pin.OwnerID); err != nil || found {
		t.Fatalf("EventStore state for pre-created orphan Session found=%t err=%v; want no accepted CORE state yet", found, err)
	}

	wire := newBoardInboxWireHarness(t, pin)
	provider := &boardCompositionIntegrationConversation{}
	mio := agent.NewMioAgent(nil, nil, nil, nil, nil, nil).WithSystemPrompt("synthetic authored Mio persona")
	makeComposition := func(activeStore *eventstore.SQLiteStore, activeSessions *sessionpersistence.JSONSessionRepository) *boardCommunicationComposition {
		t.Helper()
		composition, err := newBoardCommunicationComposition(boardCommunicationCompositionDeps{
			Store: activeStore, Sessions: activeSessions, Mio: mio, Conversation: provider,
			Client: wire.client, Pin: pin, InboxScope: boardInboxTestScope(pin),
			Service: newBoardCommunicationService(boardCommunicationServiceDeps{enabled: true}), Now: func() time.Time { return now },
			OwnerID: pin.OwnerID, ActorID: "mio", AllowedChannelIDs: []string{"channel-alpha"}, AllowedPeerIDs: []string{"peer-alpha"},
		})
		if err != nil {
			t.Fatalf("newBoardCommunicationComposition(real EventStore and JSON Session repository): %v", err)
		}
		return composition
	}
	makeHandler := func(composition *boardCommunicationComposition) http.Handler {
		t.Helper()
		handler, err := newBoardOwnerHTTPHandler(pin.OwnerID, []byte(boardOwnerHTTPTestToken), composition.ownerHTTPCallbacks())
		if err != nil {
			t.Fatalf("newBoardOwnerHTTPHandler(real composition): %v", err)
		}
		return handler
	}
	ownerRequest := func(handler http.Handler, body []byte, token string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8097"+boardOwnerRequestsPath, bytes.NewReader(body))
		request.RemoteAddr = "127.0.0.1:49152"
		request.Header.Set("Content-Type", "application/json")
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	ownerStatus := func(handler http.Handler, turnID modulecore.TurnID) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8097"+boardOwnerRequestsPath+"/"+string(turnID), nil)
		request.RemoteAddr = "127.0.0.1:49152"
		request.Header.Set("Authorization", "Bearer "+boardOwnerHTTPTestToken)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	requestBody, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal owner request: %v", err)
	}

	composition := makeComposition(store, sessions)
	handler := makeHandler(composition)
	unauthorized := ownerRequest(handler, requestBody, "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized owner acceptance status = %d body=%s; want 401", unauthorized.Code, unauthorized.Body.String())
	}

	first := ownerRequest(handler, requestBody, boardOwnerHTTPTestToken)
	if first.Code != http.StatusAccepted {
		t.Fatalf("authenticated owner acceptance status = %d body=%s; want 202", first.Code, first.Body.String())
	}
	var firstReceipt boardOwnerHTTPAcceptedResponse
	if err := json.Unmarshal(first.Body.Bytes(), &firstReceipt); err != nil {
		t.Fatalf("decode first durable owner acceptance: %v", err)
	}
	if firstReceipt.OwnerID != pin.OwnerID || firstReceipt.SessionID.Validate() != nil || firstReceipt.TurnID.Validate() != nil ||
		firstReceipt.TraceID.Validate() != nil || firstReceipt.InputMessageID.Validate() != nil || firstReceipt.Stage != boardOwnerHTTPStageAccepted ||
		firstReceipt.SessionID != orphanSessionID || !firstReceipt.AcceptedAt.Equal(orphan.AdmittedAt) {
		t.Fatalf("accepted receipt = %+v; want the orphan Session ID and reservation's original owner, IDs, stage, and admission time", firstReceipt)
	}

	sessionPath := filepath.Join(sessionDirectory, string(firstReceipt.SessionID)+".json")
	firstSessionBytes, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatalf("read durable canonical JSON Session %s: %v", firstReceipt.SessionID, err)
	}
	var persistedSession struct {
		ID             string `json:"id"`
		OwnerID        string `json:"owner_id"`
		LogicalDate    string `json:"logical_date"`
		ChannelAddress struct {
			ChannelType            string `json:"channel_type"`
			ExternalConversationID string `json:"external_conversation_id"`
		} `json:"channel_address"`
	}
	if err := json.Unmarshal(firstSessionBytes, &persistedSession); err != nil {
		t.Fatalf("decode durable canonical JSON Session: %v", err)
	}
	if persistedSession.ID != string(firstReceipt.SessionID) || persistedSession.OwnerID != pin.OwnerID ||
		persistedSession.LogicalDate != orphan.AdmittedAt.Format("2006-01-02") ||
		persistedSession.ChannelAddress.ChannelType != address.ChannelType() ||
		persistedSession.ChannelAddress.ExternalConversationID != address.ExternalConversationID() {
		t.Fatalf("persisted canonical JSON Session = %+v; want the owner-bound Session for the recovered reservation", persistedSession)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close(before durable replay): %v", err)
	}
	store = nil
	store, err = eventstore.NewSQLiteStore(databasePath)
	if err != nil {
		t.Fatalf("NewSQLiteStore(reopen for durable owner replay): %v", err)
	}
	// Reopen the canonical JSON repository too, so the retry proves the accepted
	// Session can be resolved from disk after both runtime adapters restart.
	sessions = sessionpersistence.NewJSONSessionRepository(sessionDirectory)
	composition = makeComposition(store, sessions)
	handler = makeHandler(composition)
	second := ownerRequest(handler, requestBody, boardOwnerHTTPTestToken)
	if second.Code != http.StatusAccepted || second.Body.String() != first.Body.String() {
		t.Fatalf("same-payload durable replay status/body = %d/%s; want original 202 receipt %s", second.Code, second.Body.String(), first.Body.String())
	}
	secondSessionBytes, err := os.ReadFile(sessionPath)
	if err != nil || !bytes.Equal(secondSessionBytes, firstSessionBytes) {
		t.Fatalf("same-payload replay changed canonical Session JSON: read error=%v equal=%t", err, bytes.Equal(secondSessionBytes, firstSessionBytes))
	}
	resolved, err := sessions.LoadCanonicalForOwner(ctx, string(firstReceipt.SessionID), pin.OwnerID, address)
	if err != nil || resolved.Status != domainsession.CanonicalSessionOwnerMatched || resolved.Session == nil ||
		resolved.Session.ID() != string(firstReceipt.SessionID) || resolved.BoundOwnerID != pin.OwnerID {
		t.Fatalf("reopened JSON canonical Session resolution = status %q session %v owner %q err %v; want same owner-bound Session", resolved.Status, resolved.Session, resolved.BoundOwnerID, err)
	}

	changedRequest := request
	changedRequest.Instruction += " Use a different date."
	changedBody, err := json.Marshal(changedRequest)
	if err != nil {
		t.Fatalf("marshal changed owner request: %v", err)
	}
	changed := ownerRequest(handler, changedBody, boardOwnerHTTPTestToken)
	var changedResponse boardOwnerHTTPErrorResponse
	if err := json.Unmarshal(changed.Body.Bytes(), &changedResponse); err != nil || changed.Code != http.StatusConflict || changedResponse.Error != "idempotency_conflict" {
		t.Fatalf("changed-payload response status/body = %d/%s; want 409 idempotency_conflict (decode error %v)", changed.Code, changed.Body.String(), err)
	}

	pending, err := store.ReadPendingBoardOwnerRequests(ctx, boardPublicationScope(pin), 0, 16)
	if err != nil || len(pending.Items) != 1 {
		t.Fatalf("pending durable owner requests after replay/conflict = %d, err %v; want one original accepted turn", len(pending.Items), err)
	}
	pendingRequest := pending.Items[0]
	if pendingRequest.Accepted.TurnID != firstReceipt.TurnID || pendingRequest.Accepted.SessionID != firstReceipt.SessionID ||
		pendingRequest.Instruction != request.Instruction || !pendingRequest.Reservation.AdmittedAt.Equal(orphan.AdmittedAt) {
		t.Fatalf("recovered pending request = %+v; want the original orphan reservation and exact accepted receipt", pendingRequest)
	}
	state, found, err := store.ReadSessionState(ctx, pendingRequest.Accepted.SessionID, pin.OwnerID)
	if err != nil || !found || state.SessionID != firstReceipt.SessionID || state.OwnerID != pin.OwnerID ||
		state.RuntimeVersion != conversationruntime.ConversationRuntimeV2 || state.GenerationEpoch <= 0 {
		t.Fatalf("accepted owner Session state = %+v found=%t err=%v; want exact persisted owner/v2 state", state, found, err)
	}
	acceptedView := conversationruntime.ReadViewRequest{
		Principal: conversationruntime.Principal{OwnerID: pin.OwnerID, ActorID: pendingRequest.Accepted.ActorID},
		Session: conversationruntime.SessionState{
			SessionID: state.SessionID, OwnerID: state.OwnerID, RuntimeVersion: state.RuntimeVersion,
			CutoverSeq: state.CutoverSeq, AdmissionSeq: state.AdmissionSeq,
			ProjectionWatermark: state.ProjectionWatermark, GenerationEpoch: state.GenerationEpoch,
		},
		Accepted: toConversationAcceptedTurn(pendingRequest.Accepted),
	}

	// processCatchUp also drives new-thread publication through the BOARD wire
	// protocol. Exercise the composition-bound application call directly here;
	// durable BOARD delivery has its own wire integration coverage.
	acceptedResult := composition.ownerMio.RunAcceptedBoardOwnerRequest(ctx, conversationruntime.AcceptedTurnInput{ReadViewRequest: acceptedView})
	if acceptedResult.Status != conversationruntime.BoardOwnerRequestStatusCommitted || acceptedResult.Cause != nil ||
		acceptedResult.Outcome.Event.Payload["text"] != boardCompositionIntegrationEmission || provider.calls != 1 ||
		provider.scope.Purpose != domainconversation.PurposeBoardOwnerRequest || provider.request.ResponseFormat != llm.ResponseFormatJSONObject || provider.request.OnToken != nil {
		t.Fatalf("composition-bound Mio result/status/text/provider calls/scope/format/stream = %q/%v/%d/%q/%q/%t cause=%v; want one nonstreaming structured generation", acceptedResult.Status, acceptedResult.Outcome.Event.Payload["text"], provider.calls, provider.scope.Purpose, provider.request.ResponseFormat, provider.request.OnToken != nil, acceptedResult.Cause)
	}
	if !providerPromptContains(provider.request.Messages, request.Instruction) || !providerPromptContains(provider.request.Messages, "synthetic authored Mio persona") {
		t.Fatalf("deterministic GenerateConversation did not receive Mio persona and accepted instruction: %#v", provider.request.Messages)
	}

	replayedResult := composition.ownerMio.RunAcceptedBoardOwnerRequest(ctx, conversationruntime.AcceptedTurnInput{ReadViewRequest: acceptedView})
	if replayedResult.Status != conversationruntime.BoardOwnerRequestStatusCommitted || !replayedResult.Replayed || provider.calls != 1 ||
		replayedResult.Outcome.Event.EventID != acceptedResult.Outcome.Event.EventID || replayedResult.Outcome.Event.Payload["text"] != boardCompositionIntegrationEmission {
		t.Fatalf("Mio outcome replay status/replayed/calls/event/text = %q/%t/%d/%s/%v; want original committed outcome without another generation", replayedResult.Status, replayedResult.Replayed, provider.calls, replayedResult.Outcome.Event.EventID, replayedResult.Outcome.Event.Payload["text"])
	}

	statusResponse := ownerStatus(handler, firstReceipt.TurnID)
	var status boardOwnerHTTPStatusResponse
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &status); err != nil || statusResponse.Code != http.StatusOK ||
		status.OwnerID != pin.OwnerID || status.SessionID != firstReceipt.SessionID || status.TurnID != firstReceipt.TurnID ||
		status.TraceID != firstReceipt.TraceID || status.InputMessageID != firstReceipt.InputMessageID ||
		status.Stage != boardOwnerHTTPStagePublicationPending || status.UpdatedAt.Before(status.AcceptedAt) {
		t.Fatalf("owner status readback status/body = %d/%s; want exact durable publication_pending outcome (decode error %v)", statusResponse.Code, statusResponse.Body.String(), err)
	}
}

func providerPromptContains(messages []llm.Message, expected string) bool {
	for _, message := range messages {
		if message.Content == expected {
			return true
		}
	}
	return false
}
