package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/boardcommunication"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const boardInboxTestBearer = "synthetic-board-inbox-token"

var boardInboxTestIdentity = boardcommunication.ConnectionIdentity{
	PrincipalID:       "principal-alpha",
	JournalInstanceID: "journal_0123456789abcdef0123456789abcdef",
}

func boardInboxTestPin() eventstore.BoardConnectionPin {
	return eventstore.BoardConnectionPin{
		OwnerID:           "owner-alpha",
		JournalInstanceID: boardInboxTestIdentity.JournalInstanceID,
		PrincipalID:       boardInboxTestIdentity.PrincipalID,
		CutoverAt:         time.Date(2026, 11, 5, 3, 4, 5, 0, time.UTC),
	}
}

func boardInboxTestScope(pin eventstore.BoardConnectionPin) boardInboxScope {
	return boardInboxScope{
		Pin: pin, AllowedChannelIDs: []string{"channel-alpha"}, AllowedPeerIDs: []string{"peer-alpha"}, WakeTimeout: 3 * time.Second,
	}
}

func TestValidBoardInboxScopeRequiresResponderActorToMatchPinnedPrincipal(t *testing.T) {
	legacy := boardInboxTestScope(boardInboxTestPin())
	if !validBoardInboxScope(legacy) {
		t.Fatal("legacy Mio inbox scope became invalid without a responder actor")
	}
	responder := boardInboxTestScope(boardInboxTestPin())
	responder.Pin.PrincipalID = "shiro"
	responder.ResponderActorID = "shiro"
	if !validBoardInboxScope(responder) {
		t.Fatal("valid Shiro inbox scope rejected")
	}
	responder.ResponderActorID = "midori"
	if validBoardInboxScope(responder) {
		t.Fatal("responder actor differing from the authenticated principal was accepted")
	}
	responder.Pin.PrincipalID = "kuro"
	responder.ResponderActorID = "kuro"
	if !validBoardInboxScope(responder) {
		t.Fatal("valid Kuro CHAT inbox scope rejected")
	}
	responder.ResponderActorID = "heavy"
	if validBoardInboxScope(responder) {
		t.Fatal("unsupported Heavy alias was accepted as a responder actor")
	}
}

type boardInboxTrace struct {
	mu    sync.Mutex
	steps []string
}

func (trace *boardInboxTrace) add(step string) {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.steps = append(trace.steps, step)
}

func (trace *boardInboxTrace) snapshot() []string {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	return append([]string(nil), trace.steps...)
}

type boardInboxWireHarness struct {
	t                 *testing.T
	trace             *boardInboxTrace
	identity          boardcommunication.ConnectionIdentity
	contexts          []boardcommunication.BoardContext
	wireIdentity      boardcommunication.ConnectionIdentity
	identityStatus    int
	inbox             func(read int, cursor string) boardcommunication.InboxPage
	thread            boardcommunication.ThreadPage
	threadByID        map[string]boardcommunication.ThreadPage
	threadStatus      int
	claimResults      []boardcommunication.ClaimResult
	claimErrorsByItem map[string]string
	sendErrorCode     string
	expectedItemIDs   map[string]struct{}
	claimStatus       int
	ackStatus         int
	events            []boardcommunication.BoardEvent
	blockEventStream  bool
	eventStreamOpened chan struct{}
	mu                sync.Mutex
	inboxReads        int
	claimCalls        int
	claimIDs          []string
	claimItemIDs      []string
	ackCalls          int
	ackItemIDs        []string
	threadReads       int
	server            *httptest.Server
	client            *boardcommunication.Client
}

func newBoardInboxWireHarness(t *testing.T, pin eventstore.BoardConnectionPin) *boardInboxWireHarness {
	t.Helper()
	h := &boardInboxWireHarness{
		t:                 t,
		trace:             &boardInboxTrace{},
		identity:          boardcommunication.ConnectionIdentity{PrincipalID: pin.PrincipalID, JournalInstanceID: pin.JournalInstanceID},
		contexts:          []boardcommunication.BoardContext{{ID: "channel-alpha", Label: "Channel alpha"}},
		wireIdentity:      boardcommunication.ConnectionIdentity{PrincipalID: pin.PrincipalID, JournalInstanceID: pin.JournalInstanceID},
		identityStatus:    http.StatusOK,
		threadStatus:      http.StatusOK,
		claimStatus:       http.StatusOK,
		ackStatus:         http.StatusOK,
		threadByID:        make(map[string]boardcommunication.ThreadPage),
		claimErrorsByItem: make(map[string]string),
		expectedItemIDs:   map[string]struct{}{"item-alpha": {}},
		eventStreamOpened: make(chan struct{}, 1),
	}
	h.server = httptest.NewServer(http.HandlerFunc(h.serveHTTP))
	client, err := boardcommunication.NewClient(boardcommunication.Options{
		URL: h.server.URL,
		ExpectedConnection: boardcommunication.ConnectionIdentity{
			PrincipalID: pin.PrincipalID, JournalInstanceID: pin.JournalInstanceID,
		},
		TokenSource: func(context.Context) ([]byte, error) { return []byte(boardInboxTestBearer), nil },
	})
	if err != nil {
		t.Fatalf("NewClient(synthetic pinned fixture): %v", err)
	}
	h.client = client
	t.Cleanup(func() {
		h.client.Close()
		h.server.Close()
	})
	return h
}

func (h *boardInboxWireHarness) allowInboxItems(ids ...string) {
	h.expectedItemIDs = make(map[string]struct{}, len(ids))
	for _, id := range ids {
		h.expectedItemIDs[id] = struct{}{}
	}
}

func (h *boardInboxWireHarness) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+boardInboxTestBearer ||
		r.Header.Get("X-BOARD-Principal-ID") != h.wireIdentity.PrincipalID ||
		r.Header.Get("X-BOARD-Journal-Instance-ID") != h.wireIdentity.JournalInstanceID {
		h.trace.add("bad_auth_or_pin")
		writeBoardInboxJSON(w, http.StatusForbidden, map[string]any{"error": "synthetic auth/pin mismatch"})
		return
	}
	w.Header().Set("X-Correlation-ID", "correlation-synthetic")
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/connection":
		h.trace.add("identity")
		writeBoardInboxJSON(w, h.identityStatus, map[string]any{
			"principal_id": h.identity.PrincipalID, "journal_instance_id": h.identity.JournalInstanceID,
			"correlation_id": "correlation-synthetic",
		})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/contexts":
		h.trace.add("contexts")
		writeBoardInboxJSON(w, http.StatusOK, map[string]any{
			"contexts": h.contexts, "correlation_id": "correlation-synthetic",
		})
	case r.Method == http.MethodGet && r.URL.Path == "/api/events":
		h.trace.add("events")
		if r.Header.Get("Last-Event-ID") == "" {
			h.t.Errorf("OpenEvents omitted Last-Event-ID")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		select {
		case h.eventStreamOpened <- struct{}{}:
		default:
		}
		for _, event := range h.events {
			encoded, err := json.Marshal(event)
			if err != nil {
				h.t.Errorf("marshal synthetic SSE event: %v", err)
				return
			}
			_, _ = fmt.Fprintf(w, "id: %d\nevent: board\ndata: %s\n\n", event.Seq, encoded)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		if h.blockEventStream {
			<-r.Context().Done()
		}
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/inbox":
		if r.URL.Query().Get("limit") != fmt.Sprint(boardInboxPageSize) {
			h.t.Errorf("Inbox limit = %q, want bounded page limit %d", r.URL.Query().Get("limit"), boardInboxPageSize)
		}
		h.mu.Lock()
		h.inboxReads++
		read := h.inboxReads
		h.mu.Unlock()
		h.trace.add("inbox")
		page := boardcommunication.InboxPage{Items: []boardcommunication.InboxItem{}}
		if h.inbox != nil {
			page = h.inbox(read, r.URL.Query().Get("cursor"))
		}
		writeBoardInboxJSON(w, http.StatusOK, map[string]any{
			"items": page.Items, "next_cursor": page.NextCursor, "correlation_id": "correlation-synthetic",
		})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/threads/"):
		if r.URL.Query().Get("limit") != fmt.Sprint(boardInboxPageSize) {
			h.t.Errorf("ReadThread limit = %q, want bounded page limit %d", r.URL.Query().Get("limit"), boardInboxPageSize)
		}
		h.mu.Lock()
		h.threadReads++
		h.mu.Unlock()
		h.trace.add("thread")
		threadID := strings.TrimPrefix(r.URL.Path, "/api/v1/threads/")
		thread := h.thread
		if configured, ok := h.threadByID[threadID]; ok {
			thread = configured
		}
		response := map[string]any{
			"thread": thread.Thread, "next_cursor": thread.NextCursor, "correlation_id": "correlation-synthetic",
		}
		writeBoardInboxJSON(w, h.threadStatus, response)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/processing-claim"):
		itemID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/inbox/"), "/processing-claim")
		if _, expected := h.expectedItemIDs[itemID]; !expected {
			h.t.Errorf("ClaimProcessing item identity = %q; want one of the exact synthetic inbox item IDs", itemID)
		}
		var request boardcommunication.ClaimRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			h.t.Errorf("decode synthetic claim request: %v", err)
		}
		if request.Receipt != "synthetic-ack-receipt" || request.LeaseSeconds != boardInboxClaimLeaseSeconds {
			h.t.Errorf("claim request = %#v; want item inbox receipt and bounded lease", request)
		}
		h.mu.Lock()
		h.claimCalls++
		claimCall := h.claimCalls
		h.claimIDs = append(h.claimIDs, request.ClaimID)
		h.claimItemIDs = append(h.claimItemIDs, itemID)
		h.mu.Unlock()
		h.trace.add("claim")
		if errorCode := h.claimErrorsByItem[itemID]; errorCode != "" {
			writeBoardInboxJSON(w, http.StatusConflict, map[string]any{"error": errorCode})
			return
		}
		var result boardcommunication.ClaimResult
		if len(h.claimResults) > 0 {
			index := claimCall - 1
			if index >= len(h.claimResults) {
				index = len(h.claimResults) - 1
			}
			result = h.claimResults[index]
		}
		if request.ClaimID != "" {
			result.ClaimID = request.ClaimID
		}
		writeBoardInboxJSON(w, h.claimStatus, map[string]any{
			"message_id": result.MessageID, "claim_id": result.ClaimID, "claim_receipt": result.ClaimReceipt,
			"status": result.Status, "lease_until": result.LeaseUntil, "can_process": result.CanProcess,
			"completed_at": result.CompletedAt, "correlation_id": "correlation-synthetic",
		})
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/threads/thread-alpha/messages":
		writeBoardInboxJSON(w, http.StatusConflict, map[string]any{"error": h.sendErrorCode})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/ack"):
		itemID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/inbox/"), "/ack")
		var request struct {
			Receipt string `json:"receipt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			h.t.Errorf("decode synthetic ACK request: %v", err)
		}
		if _, expected := h.expectedItemIDs[itemID]; !expected || request.Receipt != "synthetic-ack-receipt" {
			h.t.Errorf("ACK route/body = %q/%q; want exact inbox item and receipt", itemID, request.Receipt)
		}
		h.mu.Lock()
		h.ackCalls++
		h.ackItemIDs = append(h.ackItemIDs, itemID)
		h.mu.Unlock()
		h.trace.add("ack")
		writeBoardInboxJSON(w, h.ackStatus, map[string]string{
			"item_id": itemID, "status": "acked", "correlation_id": "correlation-synthetic",
		})
	default:
		h.trace.add("unexpected_route")
		h.t.Errorf("unexpected synthetic BOARD request %s %s", r.Method, r.URL.String())
		http.NotFound(w, r)
	}
}

func writeBoardInboxJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Correlation-ID", "correlation-synthetic")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func boardInboxTestItem(id, messageID string, createdAt time.Time, body string) boardcommunication.InboxItem {
	return boardcommunication.InboxItem{
		ItemID: id, MessageID: messageID, ChannelID: "channel-alpha", ThreadID: "thread-alpha",
		SenderID: "peer-alpha", CreatedAt: createdAt, Body: body,
		Mentions: []string{}, Recipients: []string{boardInboxTestIdentity.PrincipalID}, AckReceipt: "synthetic-ack-receipt",
	}
}

func boardInboxTestThread(item boardcommunication.InboxItem, wireKind, replyTo string) boardcommunication.ThreadPage {
	return boardcommunication.ThreadPage{Thread: boardcommunication.BoardThread{
		ID: item.ThreadID, Space: item.ChannelID, Title: "Synthetic thread", Summary: "fixture",
		Status: "open", Participants: []string{boardInboxTestIdentity.PrincipalID, item.SenderID},
		CreatedAt: "2026-11-05T03:00:00Z", UpdatedAt: "2026-11-05T03:06:00Z",
		Messages: []boardcommunication.BoardMessage{{
			ID: item.MessageID, Seq: 7, By: item.SenderID, At: item.CreatedAt.UTC().Format(time.RFC3339Nano),
			Body: item.Body, Kind: wireKind, Mentions: item.Mentions, Recipients: item.Recipients,
			ChannelID: item.ChannelID, ThreadID: item.ThreadID, ReplyTo: replyTo,
			CausationID: "cause-alpha", HopCount: 1,
		}},
	}, NextCursor: ""}
}

func boardInboxTestAcceptedRequest(source boardInboxSource, ownerID string) conversationruntime.ReadViewRequest {
	actorID, _ := source.Binding.SourceActorID()
	return conversationruntime.ReadViewRequest{
		Principal: conversationruntime.Principal{OwnerID: ownerID, ActorID: "mio"},
		Session:   conversationruntime.SessionState{OwnerID: ownerID},
		Accepted: conversationruntime.AcceptedTurn{
			ActorID: actorID, TriggerKind: "external_peer_message",
			InputEvent: modulecore.EventEnvelope{Payload: map[string]any{
				"external_peer_binding": source.Binding.ToPayload(),
			}},
		},
	}
}

type boardInboxTestOwner struct {
	trace               *boardInboxTrace
	ownerID             string
	accepted            conversationruntime.ReadViewRequest
	acceptedFound       bool
	outcome             bool
	outcomeAfterProcess bool
	terminalMatch       bool
	processResult       conversationruntime.TurnResult
	findErr             error
	acceptErr           error
	processErr          error
	outcomeErr          error
	terminalErr         error
	acceptCalls         int
	processCalls        int
	findCalls           int
	outcomeCalls        int
	terminalCalls       int
	lastSource          boardInboxSource
	claimHandoffs       []boardInboxClaimHandoff
}

type boardInboxClaimHandoff struct {
	Source boardInboxSource
	Claim  boardInboxClaim
}

func newBoardInboxTestOwner(trace *boardInboxTrace, pin eventstore.BoardConnectionPin) *boardInboxTestOwner {
	return &boardInboxTestOwner{trace: trace, ownerID: pin.OwnerID}
}

func (owner *boardInboxTestOwner) FindAcceptedBoardRequest(_ context.Context, source boardInboxSource) (conversationruntime.ReadViewRequest, bool, error) {
	owner.findCalls++
	owner.lastSource = source
	owner.trace.add("owner.find_accepted")
	return owner.accepted, owner.acceptedFound, owner.findErr
}

func (owner *boardInboxTestOwner) AcceptBoardRequest(_ context.Context, source boardInboxSource) (conversationruntime.ReadViewRequest, error) {
	owner.acceptCalls++
	owner.lastSource = source
	owner.trace.add("owner.accept_durable")
	if owner.acceptErr != nil {
		return conversationruntime.ReadViewRequest{}, owner.acceptErr
	}
	owner.accepted = boardInboxTestAcceptedRequest(source, owner.ownerID)
	owner.acceptedFound = true
	return owner.accepted, nil
}

func (owner *boardInboxTestOwner) ProcessAcceptedBoardRequest(_ context.Context, accepted conversationruntime.ReadViewRequest) (conversationruntime.TurnResult, error) {
	owner.processCalls++
	owner.trace.add("owner.process")
	if accepted.Accepted.TriggerKind != "external_peer_message" {
		return conversationruntime.TurnResult{}, errors.New("synthetic wrong trigger")
	}
	if owner.processErr != nil {
		return conversationruntime.TurnResult{}, owner.processErr
	}
	if owner.processResult.Status == "" {
		owner.processResult = conversationruntime.TurnResult{Status: conversationruntime.TurnStatusDelivered, Committed: true}
	}
	owner.outcome = owner.outcomeAfterProcess
	return owner.processResult, nil
}

func (owner *boardInboxTestOwner) ProcessClaimedBoardRequest(ctx context.Context, accepted conversationruntime.ReadViewRequest, source boardInboxSource, claim boardInboxClaim) (conversationruntime.TurnResult, error) {
	owner.claimHandoffs = append(owner.claimHandoffs, boardInboxClaimHandoff{Source: source, Claim: claim})
	owner.lastSource = source
	owner.trace.add("owner.process_claimed")
	return owner.ProcessAcceptedBoardRequest(ctx, accepted)
}

func (owner *boardInboxTestOwner) HasDurableBoardOutcome(_ context.Context, _ conversationruntime.ReadViewRequest) (bool, error) {
	owner.outcomeCalls++
	owner.trace.add("owner.durable_outcome")
	return owner.outcome, owner.outcomeErr
}

func (owner *boardInboxTestOwner) CorrelateTerminalBoardReply(_ context.Context, source boardInboxSource) (bool, error) {
	owner.terminalCalls++
	owner.lastSource = source
	owner.trace.add("owner.correlate_terminal")
	return owner.terminalMatch, owner.terminalErr
}

func boardInboxClaimResult(item boardcommunication.InboxItem, status string) boardcommunication.ClaimResult {
	result := boardcommunication.ClaimResult{MessageID: item.MessageID, ClaimID: "claim-alpha", Status: status}
	switch status {
	case "claimed", "reacquired":
		result.ClaimReceipt = "synthetic-claim-receipt"
		leaseUntil := time.Date(2026, 11, 5, 3, 8, 0, 0, time.UTC)
		result.LeaseUntil = &leaseUntil
		result.CanProcess = true
	case "already_claimed":
		result.ClaimReceipt = "synthetic-claim-receipt"
		leaseUntil := time.Date(2026, 11, 5, 3, 8, 0, 0, time.UTC)
		result.LeaseUntil = &leaseUntil
	case "already_processed":
		completedAt := time.Date(2026, 11, 5, 3, 8, 0, 0, time.UTC)
		result.CompletedAt = &completedAt
	}
	return result
}

func boardInboxSSEEvent(seq int64) boardcommunication.BoardEvent {
	return boardcommunication.BoardEvent{Seq: seq, Type: "message_posted", Time: "2026-11-05T03:07:00Z"}
}

func findBoardInboxStep(steps []string, wanted string) int {
	for index, step := range steps {
		if step == wanted {
			return index
		}
	}
	return -1
}

func countBoardInboxSteps(steps []string, wanted string) int {
	count := 0
	for _, step := range steps {
		if step == wanted {
			count++
		}
	}
	return count
}

func TestCatchUpBoardInboxWithDueReportsEligibleClaimExpiryOnly(t *testing.T) {
	t.Run("already claimed lease is returned while following item is processed", func(t *testing.T) {
		pin := boardInboxTestPin()
		h := newBoardInboxWireHarness(t, pin)
		activeItem := boardInboxTestItem("item-active", "message-active", pin.CutoverAt.Add(time.Second), "held by another worker")
		readyItem := boardInboxTestItem("item-ready", "message-ready", pin.CutoverAt.Add(2*time.Second), "available request")
		activeItem.ThreadID = "thread-active"
		readyItem.ThreadID = "thread-ready"
		h.allowInboxItems(activeItem.ItemID, readyItem.ItemID)
		h.inbox = func(int, string) boardcommunication.InboxPage {
			return boardcommunication.InboxPage{Items: []boardcommunication.InboxItem{activeItem, readyItem}}
		}
		h.threadByID[activeItem.ThreadID] = boardInboxTestThread(activeItem, "request", "")
		h.threadByID[readyItem.ThreadID] = boardInboxTestThread(readyItem, "request", "")
		leaseUntil := time.Now().UTC().Add(2 * time.Minute)
		activeClaim := boardInboxClaimResult(activeItem, "already_claimed")
		activeClaim.LeaseUntil = &leaseUntil
		h.claimResults = []boardcommunication.ClaimResult{
			activeClaim,
			boardInboxClaimResult(readyItem, "claimed"),
		}
		owner := newBoardInboxTestOwner(h.trace, pin)
		owner.outcomeAfterProcess = true

		sawItem, due, err := catchUpBoardInboxWithDue(context.Background(), h.client, owner, boardInboxTestScope(pin), make(map[string]boardcommunication.InboxItem))
		if err != nil {
			t.Fatalf("catchUpBoardInboxWithDue(already claimed followed by ready item): %v", err)
		}
		if !sawItem || !due.Equal(leaseUntil) {
			t.Fatalf("catch-up saw item/due = %t/%s; want true/%s", sawItem, due, leaseUntil)
		}
		if h.claimCalls != 2 || len(h.claimItemIDs) != 2 || h.claimItemIDs[0] != activeItem.ItemID || h.claimItemIDs[1] != readyItem.ItemID ||
			owner.acceptCalls != 1 || owner.processCalls != 1 || h.ackCalls != 1 || h.ackItemIDs[0] != readyItem.ItemID {
			t.Fatalf("sweep claims=%#v accept/process=%d/%d ACK=%#v; want active lease captured and following row processed once", h.claimItemIDs, owner.acceptCalls, owner.processCalls, h.ackItemIDs)
		}
	})

	t.Run("claimed but not durably completed uses its lease", func(t *testing.T) {
		pin := boardInboxTestPin()
		h := newBoardInboxWireHarness(t, pin)
		item := boardInboxTestItem("item-alpha", "message-alpha", pin.CutoverAt.Add(time.Second), "awaiting durable BOARD outcome")
		h.inbox = func(int, string) boardcommunication.InboxPage {
			return boardcommunication.InboxPage{Items: []boardcommunication.InboxItem{item}}
		}
		h.thread = boardInboxTestThread(item, "request", "")
		leaseUntil := time.Now().UTC().Add(2 * time.Minute)
		claim := boardInboxClaimResult(item, "claimed")
		claim.LeaseUntil = &leaseUntil
		h.claimResults = []boardcommunication.ClaimResult{claim}
		owner := newBoardInboxTestOwner(h.trace, pin)

		sawItem, due, err := catchUpBoardInboxWithDue(context.Background(), h.client, owner, boardInboxTestScope(pin), make(map[string]boardcommunication.InboxItem))
		if err != nil {
			t.Fatalf("catchUpBoardInboxWithDue(pending durable outcome): %v", err)
		}
		if !sawItem || !due.Equal(leaseUntil) || owner.acceptCalls != 1 || owner.processCalls != 1 || owner.outcomeCalls != 1 || h.ackCalls != 0 {
			t.Fatalf("catch-up saw/due=%t/%s owner accept/process/outcome=%d/%d/%d ACK=%d; want lease due and no ACK before durable outcome", sawItem, due, owner.acceptCalls, owner.processCalls, owner.outcomeCalls, h.ackCalls)
		}
	})

	t.Run("expired claim with pending outcome gets a short retry", func(t *testing.T) {
		pin := boardInboxTestPin()
		h := newBoardInboxWireHarness(t, pin)
		item := boardInboxTestItem("item-expired", "message-expired", pin.CutoverAt.Add(time.Second), "claim expired during processing")
		h.inbox = func(int, string) boardcommunication.InboxPage {
			return boardcommunication.InboxPage{Items: []boardcommunication.InboxItem{item}}
		}
		h.allowInboxItems(item.ItemID)
		h.thread = boardInboxTestThread(item, "request", "")
		leaseUntil := time.Now().UTC().Add(-time.Second)
		claim := boardInboxClaimResult(item, "claimed")
		claim.LeaseUntil = &leaseUntil
		h.claimResults = []boardcommunication.ClaimResult{claim}
		owner := newBoardInboxTestOwner(h.trace, pin)
		before := time.Now().UTC()

		sawItem, due, err := catchUpBoardInboxWithDue(context.Background(), h.client, owner, boardInboxTestScope(pin), make(map[string]boardcommunication.InboxItem))
		after := time.Now().UTC()
		if err != nil {
			t.Fatalf("catchUpBoardInboxWithDue(expired claim with pending outcome): %v", err)
		}
		if !sawItem || due.Before(before.Add(time.Second)) || due.After(after.Add(time.Second)) {
			t.Fatalf("catch-up saw/due=%t/%s over %s..%s; want a one-second retry", sawItem, due, before, after)
		}
		if owner.acceptCalls != 1 || owner.processCalls != 1 || owner.outcomeCalls != 1 || h.ackCalls != 0 {
			t.Fatalf("expired claim accept/process/outcome/ACK=%d/%d/%d/%d; want one processing attempt, pending outcome, and no ACK", owner.acceptCalls, owner.processCalls, owner.outcomeCalls, h.ackCalls)
		}
	})

	t.Run("claim conflict uses a conservative lease fallback without generation", func(t *testing.T) {
		pin := boardInboxTestPin()
		h := newBoardInboxWireHarness(t, pin)
		item := boardInboxTestItem("item-alpha", "message-alpha", pin.CutoverAt.Add(time.Second), "eligible request with another active claim")
		h.inbox = func(int, string) boardcommunication.InboxPage {
			return boardcommunication.InboxPage{Items: []boardcommunication.InboxItem{item}}
		}
		h.thread = boardInboxTestThread(item, "request", "")
		h.claimErrorsByItem[item.ItemID] = "processing_claim_active"
		owner := newBoardInboxTestOwner(h.trace, pin)
		before := time.Now().UTC()

		sawItem, due, err := catchUpBoardInboxWithDue(context.Background(), h.client, owner, boardInboxTestScope(pin), make(map[string]boardcommunication.InboxItem))
		after := time.Now().UTC()
		if err != nil {
			t.Fatalf("catchUpBoardInboxWithDue(active claim conflict): %v", err)
		}
		if !sawItem || due.Before(before.Add(boardInboxClaimLeaseSeconds*time.Second)) || due.After(after.Add(boardInboxClaimLeaseSeconds*time.Second)) {
			t.Fatalf("catch-up saw/due=%t/%s over %s..%s; want conservative %d-second fallback", sawItem, due, before, after, boardInboxClaimLeaseSeconds)
		}
		if h.claimCalls != 1 || owner.acceptCalls != 0 || owner.processCalls != 0 || h.ackCalls != 0 {
			t.Fatalf("active conflict caused claims/accept/process/ACK=%d/%d/%d/%d; want no duplicate generation", h.claimCalls, owner.acceptCalls, owner.processCalls, h.ackCalls)
		}
	})

	t.Run("terminal active claim contributes its actual lease", func(t *testing.T) {
		pin := boardInboxTestPin()
		h := newBoardInboxWireHarness(t, pin)
		item := boardInboxTestItem("item-alpha", "message-alpha", pin.CutoverAt.Add(time.Second), "correlated terminal reply")
		h.inbox = func(int, string) boardcommunication.InboxPage {
			return boardcommunication.InboxPage{Items: []boardcommunication.InboxItem{item}}
		}
		h.thread = boardInboxTestThread(item, "reply", "outbound-alpha")
		leaseUntil := time.Now().UTC().Add(2 * time.Minute)
		claim := boardInboxClaimResult(item, "already_claimed")
		claim.LeaseUntil = &leaseUntil
		h.claimResults = []boardcommunication.ClaimResult{claim}
		owner := newBoardInboxTestOwner(h.trace, pin)
		owner.terminalMatch = true

		sawItem, due, err := catchUpBoardInboxWithDue(context.Background(), h.client, owner, boardInboxTestScope(pin), make(map[string]boardcommunication.InboxItem))
		if err != nil {
			t.Fatalf("catchUpBoardInboxWithDue(active terminal claim): %v", err)
		}
		if !sawItem || !due.Equal(leaseUntil) || owner.terminalCalls != 1 || h.claimCalls != 1 || h.ackCalls != 0 {
			t.Fatalf("terminal catch-up saw/due=%t/%s correlate/claim/ACK=%d/%d/%d; want exact active lease and no ACK", sawItem, due, owner.terminalCalls, h.claimCalls, h.ackCalls)
		}
	})

	t.Run("already processed without durable outcome stays unscheduled", func(t *testing.T) {
		pin := boardInboxTestPin()
		h := newBoardInboxWireHarness(t, pin)
		item := boardInboxTestItem("item-alpha", "message-alpha", pin.CutoverAt.Add(time.Second), "awaiting CORE durable outcome")
		h.inbox = func(int, string) boardcommunication.InboxPage {
			return boardcommunication.InboxPage{Items: []boardcommunication.InboxItem{item}}
		}
		h.thread = boardInboxTestThread(item, "request", "")
		h.claimResults = []boardcommunication.ClaimResult{boardInboxClaimResult(item, "already_processed")}
		owner := newBoardInboxTestOwner(h.trace, pin)
		owner.accepted = boardInboxTestAcceptedRequest(boardInboxSource{
			Item: item, Message: h.thread.Thread.Messages[0],
			Binding: boardInboxBinding(pin, item, h.thread.Thread.Messages[0], domainconversation.ExternalPeerClassificationRequest),
		}, pin.OwnerID)
		owner.acceptedFound = true
		owner.outcome = false

		sawItem, due, err := catchUpBoardInboxWithDue(context.Background(), h.client, owner, boardInboxTestScope(pin), make(map[string]boardcommunication.InboxItem))
		if err != nil {
			t.Fatalf("catchUpBoardInboxWithDue(already processed without CORE outcome): %v", err)
		}
		if !sawItem || !due.IsZero() || owner.findCalls != 1 || owner.outcomeCalls != 1 || owner.acceptCalls != 0 || owner.processCalls != 0 || h.ackCalls != 0 {
			t.Fatalf("already-processed catch-up saw/due=%t/%s owner find/outcome/accept/process=%d/%d/%d/%d ACK=%d; want pending without retry due or generation", sawItem, due, owner.findCalls, owner.outcomeCalls, owner.acceptCalls, owner.processCalls, h.ackCalls)
		}
	})
}

func TestRunBoardInboxWakeUsesOneSSEHintAndPagedDurableCatchUpBeforeAcceptAndAck(t *testing.T) {
	pin := boardInboxTestPin()
	h := newBoardInboxWireHarness(t, pin)
	oldItems := make([]boardcommunication.InboxItem, 0, boardInboxPageSize)
	for index := 0; index < boardInboxPageSize; index++ {
		id := fmt.Sprintf("old-item-%02d", index)
		messageID := fmt.Sprintf("message-old-%02d", index)
		oldItems = append(oldItems, boardInboxTestItem(id, messageID, pin.CutoverAt.Add(-time.Second), "must remain before cutover"))
	}
	newItem := boardInboxTestItem("item-alpha", "message-alpha", pin.CutoverAt.Add(time.Second), "A synthetic inbound request")
	h.events = []boardcommunication.BoardEvent{boardInboxSSEEvent(11), boardInboxSSEEvent(12)}
	h.inbox = func(read int, cursor string) boardcommunication.InboxPage {
		switch {
		case read == 1:
			return boardcommunication.InboxPage{Items: []boardcommunication.InboxItem{}}
		case cursor == "":
			return boardcommunication.InboxPage{Items: oldItems, NextCursor: oldItems[len(oldItems)-1].ItemID}
		case cursor == oldItems[len(oldItems)-1].ItemID:
			return boardcommunication.InboxPage{Items: []boardcommunication.InboxItem{newItem}}
		default:
			t.Errorf("unexpected synthetic inbox cursor %q", cursor)
			return boardcommunication.InboxPage{Items: []boardcommunication.InboxItem{}}
		}
	}
	h.thread = boardInboxTestThread(newItem, "", "")
	h.claimResults = []boardcommunication.ClaimResult{boardInboxClaimResult(newItem, "claimed")}
	owner := newBoardInboxTestOwner(h.trace, pin)
	owner.outcomeAfterProcess = true

	next, err := runBoardInboxWake(context.Background(), h.client, owner, boardInboxTestScope(pin), 10)
	if err != nil {
		t.Fatalf("runBoardInboxWake(): %v", err)
	}
	if next != 11 {
		t.Fatalf("SSE hint cursor = %d, want only the first event sequence 11", next)
	}
	if countBoardInboxSteps(h.trace.snapshot(), "events") != 1 {
		t.Fatal("one wake cycle opened more than one authenticated SSE stream")
	}
	if h.inboxReads != 3 || h.threadReads != 1 || h.claimCalls != 1 || h.ackCalls != 1 {
		t.Fatalf("wire counts inbox/thread/claim/ack=%d/%d/%d/%d; want initial empty scan, one full page plus its continuation, one canonical thread, one claim and one ACK", h.inboxReads, h.threadReads, h.claimCalls, h.ackCalls)
	}
	if owner.acceptCalls != 1 || owner.processCalls != 1 || owner.outcomeCalls != 1 || owner.lastSource.Binding.WireKind != "" ||
		owner.lastSource.Binding.Classification != domainconversation.ExternalPeerClassificationRequest {
		t.Fatalf("owner calls/source = accept:%d process:%d outcome:%d binding:%#v; want one empty-wire-kind request root", owner.acceptCalls, owner.processCalls, owner.outcomeCalls, owner.lastSource.Binding)
	}
	steps := h.trace.snapshot()
	acceptIndex := findBoardInboxStep(steps, "owner.accept_durable")
	processIndex := findBoardInboxStep(steps, "owner.process")
	outcomeIndex := findBoardInboxStep(steps, "owner.durable_outcome")
	ackIndex := findBoardInboxStep(steps, "ack")
	if acceptIndex < 0 || processIndex <= acceptIndex || outcomeIndex <= processIndex || ackIndex <= outcomeIndex {
		t.Fatalf("durable/generate/outcome/ACK order = %#v; want accept < process < durable outcome < ACK", steps)
	}
	if findBoardInboxStep(steps, "bad_auth_or_pin") >= 0 || findBoardInboxStep(steps, "unexpected_route") >= 0 {
		t.Fatalf("synthetic wire fixture observed unexpected request: %#v", steps)
	}
}

func TestRunBoardInboxWakeDoesNotRegenerateAlreadyProcessedItemWithoutDurableOutcome(t *testing.T) {
	pin := boardInboxTestPin()
	h := newBoardInboxWireHarness(t, pin)
	item := boardInboxTestItem("item-alpha", "message-alpha", pin.CutoverAt.Add(time.Second), "accepted before restart")
	h.events = []boardcommunication.BoardEvent{boardInboxSSEEvent(21)}
	h.inbox = func(int, string) boardcommunication.InboxPage {
		return boardcommunication.InboxPage{Items: []boardcommunication.InboxItem{item}}
	}
	h.thread = boardInboxTestThread(item, "request", "")
	h.claimResults = []boardcommunication.ClaimResult{boardInboxClaimResult(item, "already_processed")}
	owner := newBoardInboxTestOwner(h.trace, pin)
	owner.accepted = boardInboxTestAcceptedRequest(boardInboxSource{
		Item: item, Message: h.thread.Thread.Messages[0],
		Binding: boardInboxBinding(pin, item, h.thread.Thread.Messages[0], domainconversation.ExternalPeerClassificationRequest),
	}, pin.OwnerID)
	owner.acceptedFound = true
	owner.outcome = false

	if _, err := runBoardInboxWake(context.Background(), h.client, owner, boardInboxTestScope(pin), 20); err != nil {
		t.Fatalf("runBoardInboxWake(already processed without outcome): %v", err)
	}
	if owner.findCalls != 1 || owner.outcomeCalls != 1 || owner.acceptCalls != 0 || owner.processCalls != 0 || h.ackCalls != 0 {
		t.Fatalf("already-processed recovery calls find/outcome/accept/process/ACK=%d/%d/%d/%d/%d; want read-only pending without regeneration or ACK", owner.findCalls, owner.outcomeCalls, owner.acceptCalls, owner.processCalls, h.ackCalls)
	}
	if len(owner.claimHandoffs) != 0 {
		t.Fatalf("already-processed item forwarded claim proof %#v; only an active exact claim may reach processing", owner.claimHandoffs)
	}
}

func TestRunBoardInboxWakeWaitsForDurableBoardPublicationAfterMioCommit(t *testing.T) {
	pin := boardInboxTestPin()
	h := newBoardInboxWireHarness(t, pin)
	item := boardInboxTestItem("item-alpha", "message-alpha", pin.CutoverAt.Add(time.Second), "accepted before board publication")
	h.inbox = func(int, string) boardcommunication.InboxPage {
		return boardcommunication.InboxPage{Items: []boardcommunication.InboxItem{item}}
	}
	h.thread = boardInboxTestThread(item, "request", "")
	h.events = []boardcommunication.BoardEvent{boardInboxSSEEvent(26)}
	h.claimResults = []boardcommunication.ClaimResult{
		boardInboxClaimResult(item, "claimed"), boardInboxClaimResult(item, "already_processed"),
	}
	owner := newBoardInboxTestOwner(h.trace, pin)
	owner.processResult = conversationruntime.TurnResult{Status: conversationruntime.TurnStatusPublicationPending, Committed: true}

	firstCursor, err := runBoardInboxWake(context.Background(), h.client, owner, boardInboxTestScope(pin), 25)
	if err != nil {
		t.Fatalf("runBoardInboxWake(Mio intent committed, BOARD publication pending): %v", err)
	}
	if firstCursor != 26 || owner.acceptCalls != 1 || owner.processCalls != 1 || owner.outcomeCalls != 1 || h.ackCalls != 0 {
		t.Fatalf("pending publication cursor=%d accept/process/outcome=%d/%d/%d ACK=%d; want durable acceptance and one generation, no BOARD ACK", firstCursor, owner.acceptCalls, owner.processCalls, owner.outcomeCalls, h.ackCalls)
	}

	owner.outcome = true // Synthetic B2 durable BOARD receipt is now readable.
	h.events = []boardcommunication.BoardEvent{boardInboxSSEEvent(27)}
	secondCursor, err := runBoardInboxWake(context.Background(), h.client, owner, boardInboxTestScope(pin), firstCursor)
	if err != nil {
		t.Fatalf("runBoardInboxWake(durable BOARD publication receipt): %v", err)
	}
	if secondCursor != 27 || owner.acceptCalls != 1 || owner.processCalls != 1 || owner.outcomeCalls != 2 || h.ackCalls != 1 {
		t.Fatalf("receipt recovery cursor=%d accept/process/outcome=%d/%d/%d ACK=%d; want ACK after B2 receipt without regenerating", secondCursor, owner.acceptCalls, owner.processCalls, owner.outcomeCalls, h.ackCalls)
	}
}

func TestRunBoardInboxWakeDuplicateReconnectReusesDurableOutcome(t *testing.T) {
	pin := boardInboxTestPin()
	h := newBoardInboxWireHarness(t, pin)
	item := boardInboxTestItem("item-alpha", "message-alpha", pin.CutoverAt.Add(time.Second), "one durable request")
	h.inbox = func(int, string) boardcommunication.InboxPage {
		return boardcommunication.InboxPage{Items: []boardcommunication.InboxItem{item}}
	}
	h.thread = boardInboxTestThread(item, "request", "")
	h.events = []boardcommunication.BoardEvent{boardInboxSSEEvent(31)}
	h.claimResults = []boardcommunication.ClaimResult{
		boardInboxClaimResult(item, "claimed"), boardInboxClaimResult(item, "already_processed"),
	}
	owner := newBoardInboxTestOwner(h.trace, pin)
	owner.outcomeAfterProcess = true
	firstCursor, err := runBoardInboxWake(context.Background(), h.client, owner, boardInboxTestScope(pin), 30)
	if err != nil {
		t.Fatalf("runBoardInboxWake(first delivery): %v", err)
	}
	h.events = []boardcommunication.BoardEvent{boardInboxSSEEvent(32)}
	secondCursor, err := runBoardInboxWake(context.Background(), h.client, owner, boardInboxTestScope(pin), firstCursor)
	if err != nil {
		t.Fatalf("runBoardInboxWake(reconnect duplicate): %v", err)
	}
	if firstCursor != 31 || secondCursor != 32 || owner.acceptCalls != 1 || owner.processCalls != 1 || owner.outcomeCalls != 2 || h.ackCalls != 2 {
		t.Fatalf("duplicate recovery cursor=%d/%d owner accept/process/outcome=%d/%d/%d ACK=%d; want one generation and two durable ACK checks", firstCursor, secondCursor, owner.acceptCalls, owner.processCalls, owner.outcomeCalls, h.ackCalls)
	}
	wantClaimID := expectedBoardInboxClaimID(pin, item)
	if len(h.claimIDs) != 2 || h.claimIDs[0] != wantClaimID || h.claimIDs[1] != wantClaimID {
		t.Fatalf("claim IDs across reconnect = %#v; want stable scoped ID %q", h.claimIDs, wantClaimID)
	}
	if len(owner.claimHandoffs) != 1 {
		t.Fatalf("claim proof handoffs = %d; want one private proof for the claimed first attempt", len(owner.claimHandoffs))
	}
	handoff := owner.claimHandoffs[0]
	wantLease := time.Date(2026, 11, 5, 3, 8, 0, 0, time.UTC)
	wantBinding := boardInboxBinding(pin, item, h.thread.Thread.Messages[0], domainconversation.ExternalPeerClassificationRequest)
	claimIDMatches := handoff.Claim.ClaimID == wantClaimID
	claimReceiptPresent := handoff.Claim.ClaimReceipt == "synthetic-claim-receipt"
	leaseMatches := handoff.Claim.LeaseUntil.Equal(wantLease)
	sourceMatches := handoff.Source.Binding.Equal(wantBinding) && handoff.Source.Item.ItemID == item.ItemID && handoff.Source.Message.ID == item.MessageID
	if !claimIDMatches || !claimReceiptPresent || !leaseMatches || !sourceMatches {
		t.Fatalf("private claim handoff matches ID/receipt/lease/source = %t/%t/%t/%t; want true for each", claimIDMatches, claimReceiptPresent, leaseMatches, sourceMatches)
	}
}

func TestRunBoardInboxWakeClaimIDIncludesPinnedPrincipalAndJournal(t *testing.T) {
	basePin := boardInboxTestPin()
	baseItem := boardInboxTestItem("item-alpha", "message-alpha", basePin.CutoverAt.Add(time.Second), "stable claim scope")
	runAndReadClaimID := func(pin eventstore.BoardConnectionPin, principalID string) string {
		t.Helper()
		h := newBoardInboxWireHarness(t, pin)
		item := baseItem
		item.Recipients = []string{principalID}
		h.events = []boardcommunication.BoardEvent{boardInboxSSEEvent(91)}
		h.inbox = func(int, string) boardcommunication.InboxPage {
			return boardcommunication.InboxPage{Items: []boardcommunication.InboxItem{item}}
		}
		thread := boardInboxTestThread(item, "request", "")
		thread.Thread.Participants = []string{principalID, item.SenderID}
		h.thread = thread
		h.claimResults = []boardcommunication.ClaimResult{boardInboxClaimResult(item, "claimed")}
		owner := newBoardInboxTestOwner(h.trace, pin)
		owner.outcomeAfterProcess = true
		if _, err := runBoardInboxWake(context.Background(), h.client, owner, boardInboxTestScope(pin), 90); err != nil {
			t.Fatalf("runBoardInboxWake(scoped claim ID): %v", err)
		}
		if len(h.claimIDs) != 1 || h.claimIDs[0] == "" {
			t.Fatalf("claim ID for pin %q = %#v; want one nonempty deterministic value", principalID, h.claimIDs)
		}
		return h.claimIDs[0]
	}

	baseID := runAndReadClaimID(basePin, basePin.PrincipalID)
	otherPrincipalPin := basePin
	otherPrincipalPin.PrincipalID = "principal-beta"
	otherPrincipalID := runAndReadClaimID(otherPrincipalPin, otherPrincipalPin.PrincipalID)
	otherJournalPin := basePin
	otherJournalPin.JournalInstanceID = "journal_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	otherJournalID := runAndReadClaimID(otherJournalPin, otherJournalPin.PrincipalID)
	if baseID == otherPrincipalID || baseID == otherJournalID || len(baseID) != 64 {
		t.Fatalf("scoped claim IDs base/principal/journal = %q/%q/%q; want 64-char hashes separated by pinned principal and journal", baseID, otherPrincipalID, otherJournalID)
	}
	if _, err := hex.DecodeString(baseID); err != nil {
		t.Fatalf("scoped claim ID is not lowercase hexadecimal: %v", err)
	}
}

func TestRunBoardInboxWakeLeavesItemAtExactCutoverPending(t *testing.T) {
	pin := boardInboxTestPin()
	h := newBoardInboxWireHarness(t, pin)
	item := boardInboxTestItem("item-alpha", "message-alpha", pin.CutoverAt, "at the exact cutover")
	h.events = []boardcommunication.BoardEvent{boardInboxSSEEvent(92)}
	h.inbox = func(int, string) boardcommunication.InboxPage {
		return boardcommunication.InboxPage{Items: []boardcommunication.InboxItem{item}}
	}
	h.thread = boardInboxTestThread(item, "request", "")
	h.thread.Thread.Messages[0].At = pin.CutoverAt.Add(time.Second).Format(time.RFC3339Nano)
	h.claimResults = []boardcommunication.ClaimResult{boardInboxClaimResult(item, "claimed")}
	owner := newBoardInboxTestOwner(h.trace, pin)
	owner.outcomeAfterProcess = true

	if _, err := runBoardInboxWake(context.Background(), h.client, owner, boardInboxTestScope(pin), 91); err != nil {
		t.Fatalf("runBoardInboxWake(exact cutover): %v", err)
	}
	if h.threadReads != 0 || h.claimCalls != 0 || h.ackCalls != 0 || owner.acceptCalls != 0 || owner.processCalls != 0 {
		t.Fatalf("exact-cutover item caused thread/claim/ACK/accept/process=%d/%d/%d/%d/%d; want strict post-cutover only", h.threadReads, h.claimCalls, h.ackCalls, owner.acceptCalls, owner.processCalls)
	}
}

func TestReadCanonicalBoardInboxSourceUsesThreadCreatedAtAndAllowsDistinctMessageTime(t *testing.T) {
	t.Run("distinct valid inbox and journal timestamps", func(t *testing.T) {
		pin := boardInboxTestPin()
		h := newBoardInboxWireHarness(t, pin)
		inboxAt := pin.CutoverAt.Add(time.Second)
		messageAt := inboxAt.Add(410 * time.Millisecond)
		threadAt := pin.CutoverAt.Add(-4 * time.Hour)
		item := boardInboxTestItem("item-alpha", "message-alpha", inboxAt, "same canonical request body")
		h.thread = boardInboxTestThread(item, "request", "")
		h.thread.Thread.CreatedAt = threadAt.Format(time.RFC3339Nano)
		h.thread.Thread.Messages[0].At = messageAt.Format(time.RFC3339Nano)

		source, err := readCanonicalBoardInboxSource(context.Background(), h.client, pin, item)
		if err != nil {
			t.Fatalf("readCanonicalBoardInboxSource(distinct valid timestamps): %v", err)
		}
		if !source.Item.CreatedAt.Equal(inboxAt) || source.Message.At != messageAt.Format(time.RFC3339Nano) ||
			!source.ThreadCreatedAt.Equal(threadAt) {
			t.Fatalf("canonical source timestamps inbox/message/thread=%s/%q/%s, want %s/%s/%s",
				source.Item.CreatedAt, source.Message.At, source.ThreadCreatedAt, inboxAt, messageAt.Format(time.RFC3339Nano), threadAt)
		}
		if source.Item.ItemID != item.ItemID || source.Message.ID != item.MessageID || source.Message.Body != item.Body ||
			source.Binding.ChannelID != item.ChannelID || source.Binding.ThreadID != item.ThreadID ||
			source.Binding.MessageID != item.MessageID || source.Binding.SenderPrincipalID != item.SenderID {
			t.Fatalf("distinct timestamps changed canonical identity/body/routing: source=%+v item=%+v", source, item)
		}
	})

	t.Run("malformed canonical message timestamp remains invalid", func(t *testing.T) {
		pin := boardInboxTestPin()
		h := newBoardInboxWireHarness(t, pin)
		item := boardInboxTestItem("item-alpha", "message-alpha", pin.CutoverAt.Add(time.Second), "same canonical request body")
		h.thread = boardInboxTestThread(item, "request", "")
		h.thread.Thread.Messages[0].At = "not-a-valid-timestamp"

		if _, err := readCanonicalBoardInboxSource(context.Background(), h.client, pin, item); err == nil {
			t.Fatal("readCanonicalBoardInboxSource accepted a malformed canonical message timestamp")
		}
	})
}

func TestHandleBoardInboxItemBindsResponderOnlyAfterCanonicalMessageValidation(t *testing.T) {
	for _, actorID := range []string{"shiro", "kuro"} {
		t.Run(actorID, func(t *testing.T) {
			pin := boardInboxTestPin()
			pin.PrincipalID = actorID
			wire := newBoardInboxWireHarness(t, pin)
			item := boardInboxTestItem("item-"+actorID, "message-"+actorID, pin.CutoverAt.Add(time.Second), "A canonical request for "+actorID+".")
			item.Recipients = []string{actorID}
			wire.thread = boardInboxTestThread(item, "request", "")
			wire.thread.Thread.Participants[0] = actorID
			wire.thread.Thread.Messages[0].Recipients = append([]string(nil), item.Recipients...)
			wire.allowInboxItems(item.ItemID)
			wire.claimResults = []boardcommunication.ClaimResult{boardInboxClaimResult(item, "claimed")}
			owner := newBoardInboxTestOwner(wire.trace, pin)
			owner.outcomeAfterProcess = true
			scope := boardInboxTestScope(pin)
			scope.ResponderActorID = actorID

			if _, err := handleBoardInboxItemWithDue(context.Background(), wire.client, owner, scope, item); err != nil {
				t.Fatalf("handleBoardInboxItemWithDue(): %v", err)
			}
			if wire.threadReads != 1 || owner.acceptCalls != 1 || owner.processCalls != 1 || wire.ackCalls != 1 {
				t.Fatalf("canonical read/accept/process/ACK = %d/%d/%d/%d, want one each", wire.threadReads, owner.acceptCalls, owner.processCalls, wire.ackCalls)
			}
			payload, ok := owner.accepted.Accepted.InputEvent.Payload["external_peer_binding"].(map[string]any)
			if !ok || owner.lastSource.Binding.ResponderActorID != actorID || owner.lastSource.Binding.RecipientPrincipalID != actorID || payload["responder_actor_id"] != actorID {
				t.Fatalf("responder actor was not bound to the authenticated principal before durable acceptance: binding=%+v input=%#v", owner.lastSource.Binding, owner.accepted.Accepted.InputEvent.Payload)
			}
		})
	}
}

func TestRunBoardInboxWakeSkipsOnlyActiveClaimItemAndProcessesFollowingRow(t *testing.T) {
	pin := boardInboxTestPin()
	h := newBoardInboxWireHarness(t, pin)
	activeItem := boardInboxTestItem("item-active", "message-active", pin.CutoverAt.Add(time.Second), "held by another worker")
	validItem := boardInboxTestItem("item-valid", "message-valid", pin.CutoverAt.Add(2*time.Second), "unclaimed valid request")
	activeItem.ThreadID = "thread-active"
	validItem.ThreadID = "thread-valid"
	h.allowInboxItems(activeItem.ItemID, validItem.ItemID)
	h.events = []boardcommunication.BoardEvent{boardInboxSSEEvent(94)}
	h.inbox = func(int, string) boardcommunication.InboxPage {
		return boardcommunication.InboxPage{Items: []boardcommunication.InboxItem{activeItem, validItem}}
	}
	h.threadByID[activeItem.ThreadID] = boardInboxTestThread(activeItem, "request", "")
	h.threadByID[validItem.ThreadID] = boardInboxTestThread(validItem, "request", "")
	h.claimErrorsByItem[activeItem.ItemID] = "processing_claim_active"
	h.claimResults = []boardcommunication.ClaimResult{boardInboxClaimResult(validItem, "claimed")}
	owner := newBoardInboxTestOwner(h.trace, pin)
	owner.outcomeAfterProcess = true

	if _, err := runBoardInboxWake(context.Background(), h.client, owner, boardInboxTestScope(pin), 93); err != nil {
		t.Fatalf("runBoardInboxWake(active claim followed by available item): %v", err)
	}
	wantActiveClaimID := expectedBoardInboxClaimID(pin, activeItem)
	wantValidClaimID := expectedBoardInboxClaimID(pin, validItem)
	if h.claimCalls != 2 || len(h.claimItemIDs) != 2 || h.claimItemIDs[0] != activeItem.ItemID || h.claimItemIDs[1] != validItem.ItemID {
		t.Fatalf("claim item sequence = %#v; want active row skipped and following valid row claimed", h.claimItemIDs)
	}
	if h.claimIDs[0] != wantActiveClaimID || h.claimIDs[1] != wantValidClaimID || owner.acceptCalls != 1 || owner.processCalls != 1 {
		t.Fatalf("claim IDs/owner calls = %#v/%d/%d; want scoped claims and one valid generation", h.claimIDs, owner.acceptCalls, owner.processCalls)
	}
	if len(h.ackItemIDs) != 1 || h.ackItemIDs[0] != validItem.ItemID || len(owner.claimHandoffs) != 1 ||
		owner.claimHandoffs[0].Source.Item.ItemID != validItem.ItemID {
		t.Fatalf("ACK/handoff = %#v/%#v; want valid item only", h.ackItemIDs, owner.claimHandoffs)
	}
}

func TestRunBoardInboxWakeClaimActiveMapsToClaimOnlyError(t *testing.T) {
	pin := boardInboxTestPin()
	h := newBoardInboxWireHarness(t, pin)
	h.claimErrorsByItem["item-alpha"] = "processing_claim_active"
	h.sendErrorCode = "processing_claim_active"
	claimID := strings.Repeat("a", 64)
	_, err := h.client.ClaimProcessing(context.Background(), "item-alpha", boardcommunication.ClaimRequest{
		Receipt: "synthetic-ack-receipt", ClaimID: claimID, LeaseSeconds: boardInboxClaimLeaseSeconds,
	})
	if !errors.Is(err, boardcommunication.ErrClaimActive) || errors.Is(err, boardcommunication.ErrConflict) || errors.Is(err, boardcommunication.ErrClaimFence) {
		t.Errorf("ClaimProcessing(processing_claim_active) error = %v; want claim-only ErrClaimActive", err)
	}
	prepared, prepareErr := h.client.PrepareSend(boardcommunication.SendRequest{
		ChannelID: "channel-alpha", ThreadID: "thread-alpha", Body: "synthetic outbound",
		Recipients: []string{"peer-alpha"}, IdempotencyKey: "synthetic-idempotency-alpha",
	})
	if prepareErr != nil {
		t.Fatalf("PrepareSend(synthetic outbound): %v", prepareErr)
	}
	if _, sendErr := h.client.SendPrepared(context.Background(), prepared); sendErr == nil || errors.Is(sendErr, boardcommunication.ErrClaimActive) {
		t.Fatalf("SendPrepared(processing_claim_active) error = %v; want no claim-only classification", sendErr)
	}
}

func TestRunBoardInboxWakeLeavesInvalidAndUnknownRepliesPending(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		wireKind  string
		replyTo   string
		terminal  bool
		wantClaim bool
		wantAck   bool
	}{
		{name: "reply kind without reply_to", wireKind: "reply"},
		{name: "request kind with reply_to", wireKind: "request", replyTo: "outbound-alpha"},
		{name: "empty kind with reply_to unknown correlation", wireKind: "", replyTo: "outbound-alpha"},
		{name: "unknown reply correlation", wireKind: "reply", replyTo: "outbound-alpha"},
		{name: "exact correlated terminal reply", wireKind: "reply", replyTo: "outbound-alpha", terminal: true, wantClaim: true, wantAck: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			pin := boardInboxTestPin()
			h := newBoardInboxWireHarness(t, pin)
			item := boardInboxTestItem("item-alpha", "message-alpha", pin.CutoverAt.Add(time.Second), "reply text is untrusted")
			h.events = []boardcommunication.BoardEvent{boardInboxSSEEvent(41)}
			h.inbox = func(int, string) boardcommunication.InboxPage {
				return boardcommunication.InboxPage{Items: []boardcommunication.InboxItem{item}}
			}
			h.thread = boardInboxTestThread(item, scenario.wireKind, scenario.replyTo)
			h.claimResults = []boardcommunication.ClaimResult{boardInboxClaimResult(item, "claimed")}
			owner := newBoardInboxTestOwner(h.trace, pin)
			owner.terminalMatch = scenario.terminal

			if _, err := runBoardInboxWake(context.Background(), h.client, owner, boardInboxTestScope(pin), 40); err != nil {
				t.Fatalf("runBoardInboxWake(%s): %v", scenario.name, err)
			}
			if owner.acceptCalls != 0 || owner.processCalls != 0 || len(owner.claimHandoffs) != 0 || h.claimCalls != boolCount(scenario.wantClaim) || h.ackCalls != boolCount(scenario.wantAck) {
				t.Fatalf("reply handling accept/process/claim/ACK=%d/%d/%d/%d; want no generation and claim/ACK=%t/%t", owner.acceptCalls, owner.processCalls, h.claimCalls, h.ackCalls, scenario.wantClaim, scenario.wantAck)
			}
			if owner.terminalCalls != boolCount(scenario.replyTo != "" && (scenario.wireKind == "" || scenario.wireKind == "reply")) {
				t.Fatalf("terminal correlation calls = %d; want exact reply candidates only", owner.terminalCalls)
			}
			if owner.terminalCalls > 0 && (owner.lastSource.Binding.WireKind != scenario.wireKind || owner.lastSource.Binding.ReplyTo != scenario.replyTo) {
				t.Fatalf("source binding lost raw BOARD metadata: %#v", owner.lastSource.Binding)
			}
			if scenario.terminal {
				steps := h.trace.snapshot()
				correlateIndex := findBoardInboxStep(steps, "owner.correlate_terminal")
				claimIndex := findBoardInboxStep(steps, "claim")
				ackIndex := findBoardInboxStep(steps, "ack")
				if correlateIndex < 0 || ackIndex <= correlateIndex || ackIndex <= claimIndex {
					t.Fatalf("durable terminal receipt/claim/ACK order = %#v; want receipt and claim < ACK", steps)
				}
			}
		})
	}
}

func TestRunBoardInboxWakeFailsClosedOnIdentityACLAndCanonicalMismatch(t *testing.T) {
	for _, scenario := range []struct {
		name           string
		identity       boardcommunication.ConnectionIdentity
		identityStatus int
		threadStatus   int
		channelID      string
		senderID       string
		itemRecipient  string
		itemBody       string
		messageBody    string
		wantInbox      bool
		wantThread     bool
		wantError      bool
	}{
		{name: "mismatched durable identity", identity: boardcommunication.ConnectionIdentity{PrincipalID: "principal-other", JournalInstanceID: "journal_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, identityStatus: http.StatusOK, wantError: true},
		{name: "thread ACL revoked", identity: boardInboxTestIdentity, identityStatus: http.StatusOK, threadStatus: http.StatusForbidden, wantInbox: true, wantThread: true, wantError: true},
		{name: "channel outside configured ACL", identity: boardInboxTestIdentity, identityStatus: http.StatusOK, channelID: "channel-other", wantInbox: true},
		{name: "recipient does not include pinned principal", identity: boardInboxTestIdentity, identityStatus: http.StatusOK, itemRecipient: "peer-alpha", wantInbox: true},
		{name: "peer outside configured allowlist", identity: boardInboxTestIdentity, identityStatus: http.StatusOK, senderID: "peer-other", wantInbox: true},
		{name: "inbox and canonical body mismatch", identity: boardInboxTestIdentity, identityStatus: http.StatusOK, itemBody: "inbox body", messageBody: "thread body", wantInbox: true, wantThread: true, wantError: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			pin := boardInboxTestPin()
			h := newBoardInboxWireHarness(t, pin)
			if scenario.identity.PrincipalID != "" {
				h.identity = scenario.identity
			}
			h.identityStatus = scenario.identityStatus
			if scenario.threadStatus != 0 {
				h.threadStatus = scenario.threadStatus
			}
			h.events = []boardcommunication.BoardEvent{boardInboxSSEEvent(51)}
			channel := scenario.channelID
			if channel == "" {
				channel = "channel-alpha"
			}
			recipient := scenario.itemRecipient
			if recipient == "" {
				recipient = boardInboxTestIdentity.PrincipalID
			}
			body := scenario.itemBody
			if body == "" {
				body = "synthetic source body"
			}
			createdAt := pin.CutoverAt.Add(time.Second)
			item := boardInboxTestItem("item-alpha", "message-alpha", createdAt, body)
			item.ChannelID = channel
			item.Recipients = []string{recipient}
			if scenario.senderID != "" {
				item.SenderID = scenario.senderID
			}
			canonicalBody := scenario.messageBody
			if canonicalBody == "" {
				canonicalBody = body
			}
			h.inbox = func(int, string) boardcommunication.InboxPage {
				return boardcommunication.InboxPage{Items: []boardcommunication.InboxItem{item}}
			}
			h.thread = boardInboxTestThread(item, "request", "")
			h.thread.Thread.Messages[0].Body = canonicalBody
			h.claimResults = []boardcommunication.ClaimResult{boardInboxClaimResult(item, "claimed")}
			owner := newBoardInboxTestOwner(h.trace, pin)

			scope := boardInboxTestScope(pin)
			_, err := runBoardInboxWake(context.Background(), h.client, owner, scope, 50)
			if scenario.wantError && err == nil {
				t.Fatal("runBoardInboxWake accepted a mismatched identity, ACL, or canonical source")
			}
			if !scenario.wantError && err != nil {
				t.Fatalf("runBoardInboxWake(%s) error = %v; want safe pending/no action", scenario.name, err)
			}
			if owner.acceptCalls != 0 || owner.processCalls != 0 || h.claimCalls != 0 || h.ackCalls != 0 {
				t.Fatalf("mismatched source caused owner/claim/ACK effects: %d/%d/%d/%d", owner.acceptCalls, owner.processCalls, h.claimCalls, h.ackCalls)
			}
			steps := h.trace.snapshot()
			if h.inboxReads != boolCount(scenario.wantInbox) || h.threadReads != boolCount(scenario.wantThread) {
				t.Fatalf("identity/ACL read counts inbox/thread=%d/%d; want %t/%t", h.inboxReads, h.threadReads, scenario.wantInbox, scenario.wantThread)
			}
			if findBoardInboxStep(steps, "ack") >= 0 || findBoardInboxStep(steps, "claim") >= 0 {
				t.Fatalf("mismatched source reached claim or ACK: %#v", steps)
			}
		})
	}
}

func TestRunBoardInboxWakeDoesNotAckClaimBoundToAnotherMessage(t *testing.T) {
	pin := boardInboxTestPin()
	h := newBoardInboxWireHarness(t, pin)
	item := boardInboxTestItem("item-alpha", "message-alpha", pin.CutoverAt.Add(time.Second), "synthetic request")
	h.events = []boardcommunication.BoardEvent{boardInboxSSEEvent(61)}
	h.inbox = func(int, string) boardcommunication.InboxPage {
		return boardcommunication.InboxPage{Items: []boardcommunication.InboxItem{item}}
	}
	h.thread = boardInboxTestThread(item, "request", "")
	claim := boardInboxClaimResult(item, "claimed")
	claim.MessageID = "message-other"
	h.claimResults = []boardcommunication.ClaimResult{claim}
	owner := newBoardInboxTestOwner(h.trace, pin)

	_, err := runBoardInboxWake(context.Background(), h.client, owner, boardInboxTestScope(pin), 60)
	if err == nil || owner.acceptCalls != 0 || owner.processCalls != 0 || h.ackCalls != 0 {
		t.Fatalf("mismatched claim message result err=%v owner=%d/%d ACK=%d; want fail closed", err, owner.acceptCalls, owner.processCalls, h.ackCalls)
	}
	if len(owner.claimHandoffs) != 0 {
		t.Fatalf("claim for another message forwarded proof %#v", owner.claimHandoffs)
	}
}

func TestRunBoardInboxWakeDoesNotForwardUnprocessableOrMalformedClaim(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		status    string
		malformed string
		wantError bool
	}{
		{name: "another active claim", status: "already_claimed"},
		{name: "missing lease", status: "claimed", malformed: "lease", wantError: true},
		{name: "false can_process", status: "claimed", malformed: "can_process", wantError: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			pin := boardInboxTestPin()
			h := newBoardInboxWireHarness(t, pin)
			item := boardInboxTestItem("item-alpha", "message-alpha", pin.CutoverAt.Add(time.Second), "synthetic request")
			h.events = []boardcommunication.BoardEvent{boardInboxSSEEvent(65)}
			h.inbox = func(int, string) boardcommunication.InboxPage {
				return boardcommunication.InboxPage{Items: []boardcommunication.InboxItem{item}}
			}
			h.thread = boardInboxTestThread(item, "request", "")
			claim := boardInboxClaimResult(item, scenario.status)
			switch scenario.malformed {
			case "lease":
				claim.LeaseUntil = nil
			case "can_process":
				claim.CanProcess = false
			}
			h.claimResults = []boardcommunication.ClaimResult{claim}
			owner := newBoardInboxTestOwner(h.trace, pin)

			_, err := runBoardInboxWake(context.Background(), h.client, owner, boardInboxTestScope(pin), 64)
			if scenario.wantError && err == nil {
				t.Fatal("runBoardInboxWake accepted a malformed claim proof")
			}
			if !scenario.wantError && err != nil {
				t.Fatalf("runBoardInboxWake(%s): %v", scenario.name, err)
			}
			if len(owner.claimHandoffs) != 0 || owner.acceptCalls != 0 || owner.processCalls != 0 || h.ackCalls != 0 {
				t.Fatalf("unusable claim caused proof/accept/process/ACK=%d/%d/%d/%d; want no handoff or effects", len(owner.claimHandoffs), owner.acceptCalls, owner.processCalls, h.ackCalls)
			}
		})
	}
}

func TestRunBoardInboxWakeCancellationStopsOneBlockedSSERead(t *testing.T) {
	pin := boardInboxTestPin()
	h := newBoardInboxWireHarness(t, pin)
	h.blockEventStream = true
	h.inbox = func(int, string) boardcommunication.InboxPage {
		return boardcommunication.InboxPage{Items: []boardcommunication.InboxItem{}}
	}
	owner := newBoardInboxTestOwner(h.trace, pin)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := runBoardInboxWake(ctx, h.client, owner, boardInboxTestScope(pin), 70)
		done <- err
	}()
	select {
	case <-h.eventStreamOpened:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("bounded SSE stream did not open")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("runBoardInboxWake(canceled) error = %v; want context canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runBoardInboxWake did not stop after cancellation")
	}
	if owner.acceptCalls != 0 || owner.processCalls != 0 || h.claimCalls != 0 || h.ackCalls != 0 {
		t.Fatalf("canceled wake caused owner/claim/ACK effects: %d/%d/%d/%d", owner.acceptCalls, owner.processCalls, h.claimCalls, h.ackCalls)
	}
}

func TestRunBoardInboxWakeBoundsIdleSSERead(t *testing.T) {
	pin := boardInboxTestPin()
	h := newBoardInboxWireHarness(t, pin)
	h.blockEventStream = true
	h.inbox = func(int, string) boardcommunication.InboxPage {
		return boardcommunication.InboxPage{Items: []boardcommunication.InboxItem{}}
	}
	owner := newBoardInboxTestOwner(h.trace, pin)
	scope := boardInboxTestScope(pin)
	scope.WakeTimeout = 40 * time.Millisecond
	started := time.Now()
	sequence, err := runBoardInboxWake(context.Background(), h.client, owner, scope, 80)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, boardcommunication.ErrUnavailable) {
		t.Fatalf("runBoardInboxWake(idle stream) error = %v; want timeout or bounded idle completion", err)
	}
	if time.Since(started) > time.Second || sequence != 80 {
		t.Fatalf("idle wake duration/cursor = %v/%d; want bounded completion at existing cursor", time.Since(started), sequence)
	}
	if owner.acceptCalls != 0 || owner.processCalls != 0 || h.claimCalls != 0 || h.ackCalls != 0 {
		t.Fatalf("idle wake caused owner/claim/ACK effects: %d/%d/%d/%d", owner.acceptCalls, owner.processCalls, h.claimCalls, h.ackCalls)
	}
}

func boardInboxBinding(pin eventstore.BoardConnectionPin, item boardcommunication.InboxItem, message boardcommunication.BoardMessage, classification domainconversation.ExternalPeerClassification) domainconversation.ExternalPeerBinding {
	return domainconversation.ExternalPeerBinding{
		BoardInstanceID: pin.JournalInstanceID, RecipientPrincipalID: pin.PrincipalID,
		ChannelID: item.ChannelID, ThreadID: item.ThreadID, MessageID: item.MessageID,
		SenderPrincipalID: item.SenderID, WireKind: message.Kind, Classification: classification, ReplyTo: message.ReplyTo,
	}
}

func expectedBoardInboxClaimID(pin eventstore.BoardConnectionPin, item boardcommunication.InboxItem) string {
	preimage := "rencrow.board.processing-claim.v1\x00" + pin.JournalInstanceID + "\x00" + pin.PrincipalID + "\x00" + item.ItemID + "\x00" + item.MessageID
	digest := sha256.Sum256([]byte(preimage))
	return hex.EncodeToString(digest[:])
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

var _ boardInboxOwner = (*boardInboxTestOwner)(nil)
