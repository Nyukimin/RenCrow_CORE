package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/boardcommunication"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestRunBoardPublicationDeliveryUsesCurrentClaimHintAndCommitsCanonicalReadback(t *testing.T) {
	fixture := newBoardPublicationDeliveryTestFixture(t)
	journal := newBoardPublicationDeliveryTestJournal(fixture.lease, fixture.trace)
	input := fixture.input
	input.ClaimHint = &boardPublicationClaimHint{Source: fixture.source, Claim: fixture.claim, CanonicalResponseMessageID: fixture.lease.PublicationIntent.CanonicalResponseMessageID}

	progressed, err := runBoardPublicationDelivery(context.Background(), fixture.wire.client, journal, input)
	if err != nil || !progressed {
		t.Fatalf("runBoardPublicationDelivery(current private C2a claim hint) = progressed %t, err %v; want one completed send", progressed, err)
	}
	if journal.claimCalls != 1 || len(journal.attempts) != 1 || len(journal.fences) != 0 || len(journal.completions) != 1 || len(journal.retries) != 0 {
		t.Fatalf("B2 calls claim/attempt/fence/complete/retry=%d/%d/%d/%d/%d; want 1/1/0/1/0", journal.claimCalls, len(journal.attempts), len(journal.fences), len(journal.completions), len(journal.retries))
	}
	if len(journal.claimInputs) != 1 || journal.claimInputs[0].CanonicalResponseMessageID != fixture.lease.PublicationIntent.CanonicalResponseMessageID {
		t.Fatalf("B2 claim selector = %+v; want exact durable canonical response %q", journal.claimInputs, fixture.lease.PublicationIntent.CanonicalResponseMessageID)
	}
	if fixture.wire.claimCalls != 0 || fixture.wire.inboxReceiptCalls != 0 || fixture.wire.ackCalls != 0 {
		t.Fatalf("current claim hint caused refresh/ACK calls=%d/%d/%d; want none", fixture.wire.claimCalls, fixture.wire.inboxReceiptCalls, fixture.wire.ackCalls)
	}
	if len(fixture.wire.sendBodies) != 1 || !bytes.Equal(fixture.wire.sendBodies[0], journal.attempts[0].PreparedBytes) {
		t.Fatal("BOARD POST body was not byte-for-byte equal to the B2 attempt saved before transport")
	}
	var prepared struct {
		Body                   string   `json:"body"`
		ChannelID              string   `json:"channel_id"`
		ThreadID               string   `json:"thread_id"`
		Recipients             []string `json:"recipients"`
		Mentions               []string `json:"mentions"`
		ReplyTo                string   `json:"reply_to"`
		Kind                   string   `json:"kind"`
		IdempotencyKey         string   `json:"idempotency_key"`
		ProcessingClaimID      string   `json:"processing_claim_id"`
		ProcessingClaimReceipt string   `json:"processing_claim_receipt"`
	}
	if err := json.Unmarshal(journal.attempts[0].PreparedBytes, &prepared); err != nil {
		t.Fatalf("decode exact private prepared BOARD bytes: %v", err)
	}
	if prepared.Body != "Synthetic durable response." || prepared.ChannelID != fixture.target.ChannelID ||
		prepared.ThreadID != fixture.target.ThreadID || prepared.ReplyTo != fixture.target.ReplyTo || prepared.Kind != "reply" ||
		prepared.IdempotencyKey != fixture.lease.LogicalKey || prepared.ProcessingClaimID != fixture.claim.ClaimID ||
		prepared.ProcessingClaimReceipt != fixture.claim.ClaimReceipt || !reflect.DeepEqual(prepared.Recipients, []string{fixture.target.PeerPrincipalID}) {
		t.Fatalf("persisted prepared send identity = %#v; want exact canonical response, route, stable key, and current claim proof", prepared)
	}
	if strings.Contains(strings.ToLower(string(journal.attempts[0].PreparedBytes)), "authorization") ||
		strings.Contains(strings.ToLower(string(journal.attempts[0].PreparedBytes)), "bearer ") {
		t.Fatal("B2 exact prepared attempt contains transport Authorization material")
	}
	steps := fixture.trace.snapshot()
	if !orderedBoardDeliverySteps(steps, "store.record_attempt", "wire.send", "wire.readback", "store.complete") {
		t.Fatalf("delivery steps = %#v; want durable exact attempt before send and canonical readback before completion", steps)
	}
	completed := journal.completions[0]
	if completed.AttemptOrdinal != 1 || completed.AttemptSHA256 != journal.attempts[0].PayloadSHA256 ||
		completed.SendReceipt.MessageID != "board-response-alpha" || completed.SendReceipt.ThreadID != fixture.target.ThreadID ||
		completed.CanonicalMessage.MessageID != "board-response-alpha" || completed.CanonicalMessage.ChannelID != fixture.target.ChannelID ||
		completed.CanonicalMessage.SenderPrincipalID != fixture.target.CorePrincipalID ||
		!reflect.DeepEqual(completed.CanonicalMessage.Recipients, []string{fixture.target.PeerPrincipalID}) ||
		completed.CanonicalMessage.ReplyTo != fixture.target.ReplyTo ||
		completed.CanonicalMessage.CausationID != fixture.target.ReplyTo || completed.CanonicalMessage.HopCount != 1 {
		t.Fatalf("durable B2 completion did not bind receipt and authoritative BOARD tuple: %#v", completed)
	}
	if string(fixture.lease.CanonicalResponse.CausationEventID) == fixture.target.ReplyTo {
		t.Fatal("CORE response event cause must remain distinct from the BOARD root message cause")
	}
	if fixture.wire.ackCalls != 0 {
		t.Fatal("C2b ACKed the inbox item; ACK ownership remains with C2a after durable completion")
	}
}

func TestRunBoardPublicationDeliveryScopesClaimToConfiguredResponder(t *testing.T) {
	fixture := newBoardPublicationDeliveryTestFixture(t)
	journal := newBoardPublicationDeliveryTestJournal(fixture.lease, fixture.trace)
	input := fixture.input
	input.ResponderActorID = "shiro"

	if progressed, err := runBoardPublicationDelivery(context.Background(), fixture.wire.client, journal, input); err != nil || !progressed {
		t.Fatalf("runBoardPublicationDelivery(Shiro scope) = progressed %t, err %v; want one completed send", progressed, err)
	}
	if len(journal.claimInputs) != 1 || journal.claimInputs[0].ResponderActorID != input.ResponderActorID {
		t.Fatalf("claim actor = %#v; want fixed responder %q", journal.claimInputs, input.ResponderActorID)
	}
}

func TestRunBoardPublicationDeliveryReplaysExactBytesAfterLostSendResult(t *testing.T) {
	fixture := newBoardPublicationDeliveryTestFixture(t)
	fixture.wire.dropFirstSend = true
	journal := newBoardPublicationDeliveryTestJournal(fixture.lease, fixture.trace)
	input := fixture.input
	input.ClaimHint = &boardPublicationClaimHint{Source: fixture.source, Claim: fixture.claim, CanonicalResponseMessageID: fixture.lease.PublicationIntent.CanonicalResponseMessageID}

	if progressed, err := runBoardPublicationDelivery(context.Background(), fixture.wire.client, journal, input); err == nil || progressed {
		t.Fatalf("runBoardPublicationDelivery(lost first response) = progressed %t, err %v; want retryable unknown outcome", progressed, err)
	}
	if len(journal.attempts) != 1 || len(journal.retries) != 1 || len(journal.completions) != 0 {
		t.Fatalf("after unknown send: attempts/retries/completions=%d/%d/%d; want 1/1/0", len(journal.attempts), len(journal.retries), len(journal.completions))
	}
	first := append([]byte(nil), journal.attempts[0].PreparedBytes...)
	if progressed, err := runBoardPublicationDelivery(context.Background(), fixture.wire.client, journal, fixture.input); err != nil || !progressed {
		t.Fatalf("runBoardPublicationDelivery(restart replay) = progressed %t, err %v; want durable completion", progressed, err)
	}
	if len(fixture.wire.sendBodies) != 2 || !bytes.Equal(fixture.wire.sendBodies[0], first) || !bytes.Equal(fixture.wire.sendBodies[1], first) {
		t.Fatalf("replayed send bodies are not exact immutable bytes: %q / %q", fixture.wire.sendBodies[0], fixture.wire.sendBodies[1])
	}
	if fixture.wire.claimCalls != 0 || fixture.wire.inboxReceiptCalls != 0 || len(journal.fences) != 0 || len(journal.attempts) != 1 {
		t.Fatalf("unknown result authorized fresh claim/fence/attempt: claims=%d receipts=%d fences=%d attempts=%d", fixture.wire.claimCalls, fixture.wire.inboxReceiptCalls, len(journal.fences), len(journal.attempts))
	}
}

func TestRunBoardPublicationDeliveryFencesOnlyExactSendClaimFailure(t *testing.T) {
	fixture := newBoardPublicationDeliveryTestFixture(t)
	fixture.wire.sendClaimFenceFirst = true
	journal := newBoardPublicationDeliveryTestJournal(fixture.lease, fixture.trace)
	input := fixture.input
	input.ClaimHint = &boardPublicationClaimHint{Source: fixture.source, Claim: fixture.claim, CanonicalResponseMessageID: fixture.lease.PublicationIntent.CanonicalResponseMessageID}

	progressed, err := runBoardPublicationDelivery(context.Background(), fixture.wire.client, journal, input)
	if err != nil || !progressed {
		t.Fatalf("runBoardPublicationDelivery(exact old-send claim fence plus fresh claim) = progressed %t, err %v", progressed, err)
	}
	if fixture.wire.claimCalls != 1 || fixture.wire.inboxReceiptCalls != 1 || fixture.wire.ackCalls != 0 ||
		len(journal.fences) != 1 || len(journal.attempts) != 2 || len(journal.completions) != 1 || len(journal.retries) != 0 {
		t.Fatalf("claim-fence path calls claims/receipts/ACK/fences/attempts/completions/retries=%d/%d/%d/%d/%d/%d/%d",
			fixture.wire.claimCalls, fixture.wire.inboxReceiptCalls, fixture.wire.ackCalls,
			len(journal.fences), len(journal.attempts), len(journal.completions), len(journal.retries))
	}
	first, second := journal.attempts[0], journal.attempts[1]
	if first.LogicalKey != second.LogicalKey || first.Ordinal != 1 || second.Ordinal != 2 ||
		first.PayloadSHA256 == second.PayloadSHA256 || bytes.Equal(first.PreparedBytes, second.PreparedBytes) ||
		journal.fences[0].AttemptSHA256 != first.PayloadSHA256 || journal.fences[0].Proof.PriorClaimID != fixture.claim.ClaimID ||
		journal.fences[0].Proof.FreshClaimID != fixture.claim.ClaimID || journal.fences[0].Proof.FreshReceiptSHA256 == first.ProcessingClaimReceiptSHA256 {
		t.Fatalf("fresh attempt did not follow one exact old-claim fence under the same key: first=%#v second=%#v fence=%#v", first, second, journal.fences[0])
	}
	if !bytes.Equal(fixture.wire.sendBodies[0], first.PreparedBytes) || !bytes.Equal(fixture.wire.sendBodies[1], second.PreparedBytes) {
		t.Fatal("claim-fence requests did not use the exact two prepared attempts in ordinal order")
	}
}

func TestRunBoardPublicationDeliveryDoesNotRefreshOnAuthUnknownOrMismatch(t *testing.T) {
	for _, test := range []struct {
		name   string
		setup  func(*boardPublicationDeliveryTestFixture)
		claims int
	}{{
		name:  "authentication failure",
		setup: func(fixture *boardPublicationDeliveryTestFixture) { fixture.wire.sendStatus = http.StatusUnauthorized },
	}, {
		name:  "unknown transport outcome",
		setup: func(fixture *boardPublicationDeliveryTestFixture) { fixture.wire.dropFirstSend = true },
	}, {
		name: "connection identity mismatch",
		setup: func(fixture *boardPublicationDeliveryTestFixture) {
			fixture.wire.identity.PrincipalID = "principal-other"
		},
	}, {
		name: "fresh claim source mismatch",
		setup: func(fixture *boardPublicationDeliveryTestFixture) {
			fixture.wire.sendClaimFenceFirst = true
			fixture.wire.claimResult.MessageID = "message-other"
		},
		claims: 1,
	}} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBoardPublicationDeliveryTestFixture(t)
			test.setup(fixture)
			journal := newBoardPublicationDeliveryTestJournal(fixture.lease, fixture.trace)
			input := fixture.input
			input.ClaimHint = &boardPublicationClaimHint{Source: fixture.source, Claim: fixture.claim, CanonicalResponseMessageID: fixture.lease.PublicationIntent.CanonicalResponseMessageID}
			progressed, err := runBoardPublicationDelivery(context.Background(), fixture.wire.client, journal, input)
			if err == nil || progressed {
				t.Fatalf("runBoardPublicationDelivery(%s) = progressed %t, err %v; want fail-closed without completion", test.name, progressed, err)
			}
			if fixture.wire.claimCalls != test.claims || fixture.wire.ackCalls != 0 || len(journal.completions) != 0 {
				t.Fatalf("failure path made fresh-claim/ACK/completion calls=%d/%d/%d; want %d/0/0", fixture.wire.claimCalls, fixture.wire.ackCalls, len(journal.completions), test.claims)
			}
			if test.name != "connection identity mismatch" && len(journal.retries) != 1 {
				t.Fatalf("failure path scheduled typed retry %d times; want one", len(journal.retries))
			}
		})
	}
}

func TestRunBoardPublicationDeliveryRequiresAuthoritativeCanonicalTuple(t *testing.T) {
	for _, field := range []string{"body", "mentions", "peer", "channel", "thread", "reply", "causation", "hop"} {
		t.Run(field, func(t *testing.T) {
			fixture := newBoardPublicationDeliveryTestFixture(t)
			fixture.wire.readbackMutation = field
			journal := newBoardPublicationDeliveryTestJournal(fixture.lease, fixture.trace)
			input := fixture.input
			input.ClaimHint = &boardPublicationClaimHint{Source: fixture.source, Claim: fixture.claim, CanonicalResponseMessageID: fixture.lease.PublicationIntent.CanonicalResponseMessageID}
			progressed, err := runBoardPublicationDelivery(context.Background(), fixture.wire.client, journal, input)
			if err == nil || progressed {
				t.Fatalf("runBoardPublicationDelivery(readback %s mismatch) = progressed %t, err %v; want fail-closed", field, progressed, err)
			}
			if len(journal.completions) != 0 || len(journal.retries) != 1 || fixture.wire.ackCalls != 0 {
				t.Fatalf("canonical %s mismatch completion/retry/ACK=%d/%d/%d; want 0/1/0", field, len(journal.completions), len(journal.retries), fixture.wire.ackCalls)
			}
		})
	}
}

func TestRunBoardPublicationDeliveryPublishesAndReplaysNewRootWithoutPeerClaim(t *testing.T) {
	fixture := newBoardPublicationDeliveryTestFixture(t)
	fixture.lease.TargetKind = "new_root"
	fixture.lease.Title = "Synthetic owner request title"
	fixture.lease.PublicationIntent.Target.ThreadID = ""
	fixture.lease.PublicationIntent.Target.ReplyTo = ""
	fixture.lease.CanonicalResponse.Payload["mentions"] = []string{fixture.target.PeerPrincipalID}
	fixture.target.ThreadID = ""
	fixture.target.ReplyTo = ""
	fixture.wire.newRoot = &boardPublicationNewRootTestTarget{
		ThreadID: "thread-root-alpha", MessageID: "board-root-alpha", Title: fixture.lease.Title,
		ChannelID: fixture.target.ChannelID, CorePrincipalID: fixture.target.CorePrincipalID,
		PeerPrincipalID: fixture.target.PeerPrincipalID,
	}
	fixture.wire.dropFirstSend = true
	journal := newBoardPublicationDeliveryTestJournal(fixture.lease, fixture.trace)

	if progressed, err := runBoardPublicationDelivery(context.Background(), fixture.wire.client, journal, fixture.input); err == nil || progressed {
		t.Fatalf("runBoardPublicationDelivery(new-root lost receipt) = progressed %t, err %v; want retryable unknown outcome", progressed, err)
	}
	if len(journal.attempts) != 1 || len(journal.retries) != 1 || len(journal.completions) != 0 {
		t.Fatalf("after unknown root send: attempts/retries/completions=%d/%d/%d; want 1/1/0", len(journal.attempts), len(journal.retries), len(journal.completions))
	}
	first := append([]byte(nil), journal.attempts[0].PreparedBytes...)
	firstDigest := sha256.Sum256(first)
	firstHash := hex.EncodeToString(firstDigest[:])
	if journal.attempts[0].PayloadSHA256 != firstHash {
		t.Fatalf("new-root recorded payload hash = %q; want SHA-256 %q", journal.attempts[0].PayloadSHA256, firstHash)
	}
	_, prepared, err := restoreBoardPublicationAttempt(fixture.wire.client, fixture.lease, journal.attempts[0])
	if err != nil || prepared.Path() != "/api/v1/threads" || prepared.IdempotencyKey() != fixture.lease.LogicalKey || !bytes.Equal(prepared.JSONBytes(), first) {
		t.Fatalf("restored new-root attempt path/key = %q/%q, err %v; want pinned root endpoint and stable key", prepared.Path(), prepared.IdempotencyKey(), err)
	}

	if progressed, err := runBoardPublicationDelivery(context.Background(), fixture.wire.client, journal, fixture.input); err != nil || !progressed {
		t.Fatalf("runBoardPublicationDelivery(new-root exact replay) = progressed %t, err %v; want durable completion", progressed, err)
	}
	if len(fixture.wire.sendBodies) != 2 || !bytes.Equal(fixture.wire.sendBodies[0], first) || !bytes.Equal(fixture.wire.sendBodies[1], first) {
		t.Fatalf("new-root replay changed prepared bytes: %q / %q", fixture.wire.sendBodies[0], fixture.wire.sendBodies[1])
	}
	if len(journal.attempts) != 1 || journal.attempts[0].PayloadSHA256 != firstHash || !bytes.Equal(journal.attempts[0].PreparedBytes, first) {
		t.Fatalf("new-root replay changed durable attempt bytes/hash: bytes=%q hash=%q; want original %q/%q", journal.attempts[0].PreparedBytes, journal.attempts[0].PayloadSHA256, first, firstHash)
	}
	if !reflect.DeepEqual(fixture.wire.sendPaths, []string{"/api/v1/threads", "/api/v1/threads"}) {
		t.Fatalf("new-root POST paths = %#v; want only POST /api/v1/threads", fixture.wire.sendPaths)
	}
	if len(journal.attempts) != 1 || len(journal.completions) != 1 || len(journal.retries) != 1 {
		t.Fatalf("root replay attempts/completions/retries=%d/%d/%d; want 1/1/1 without a new generation", len(journal.attempts), len(journal.completions), len(journal.retries))
	}
	if fixture.wire.inboxCalls != 0 || fixture.wire.claimCalls != 0 || fixture.wire.inboxReceiptCalls != 0 || fixture.wire.ackCalls != 0 {
		t.Fatalf("new-root path touched inbox/claim/receipt/ACK=%d/%d/%d/%d; want none", fixture.wire.inboxCalls, fixture.wire.claimCalls, fixture.wire.inboxReceiptCalls, fixture.wire.ackCalls)
	}
	if fixture.wire.rootReadCalls != 1 || !orderedBoardDeliverySteps(fixture.trace.snapshot(), "store.record_attempt", "wire.root_send", "wire.root_readback", "store.complete") {
		t.Fatalf("root attempt/readback sequence = %#v; want persisted attempt before root POST, canonical readback, then completion", fixture.trace.snapshot())
	}
	var request boardPublicationDeliverySendWire
	if err := json.Unmarshal(first, &request); err != nil {
		t.Fatalf("decode immutable root attempt: %v", err)
	}
	var requestFields map[string]json.RawMessage
	if err := json.Unmarshal(first, &requestFields); err != nil {
		t.Fatalf("decode root attempt fields: %v", err)
	}
	if request.ChannelID != fixture.target.ChannelID || request.Title != fixture.lease.Title ||
		request.Body != boardPublicationResponseText(fixture.lease.CanonicalResponse) || request.Kind != "request" ||
		!reflect.DeepEqual(request.Recipients, []string{fixture.target.PeerPrincipalID}) ||
		!reflect.DeepEqual(request.Mentions, []string{fixture.target.PeerPrincipalID}) || request.IdempotencyKey != fixture.lease.LogicalKey ||
		request.ThreadID != "" || request.ReplyTo != "" || request.ProcessingClaimID != "" || request.ProcessingClaimReceipt != "" {
		t.Fatalf("new-root request = %#v; want one pinned recipient, authored response, title, and no thread/reply/claim proof", request)
	}
	for _, absent := range []string{"thread_id", "reply_to", "processing_claim_id", "processing_claim_receipt"} {
		if _, exists := requestFields[absent]; exists {
			t.Fatalf("new-root request includes forbidden %q field", absent)
		}
	}
	completed := journal.completions[0]
	if completed.SendReceipt.MessageID != fixture.wire.newRoot.MessageID || completed.SendReceipt.ThreadID != fixture.wire.newRoot.ThreadID ||
		completed.CanonicalMessage.MessageID != fixture.wire.newRoot.MessageID || completed.CanonicalMessage.ThreadID != fixture.wire.newRoot.ThreadID ||
		completed.CanonicalMessage.ChannelID != fixture.target.ChannelID || completed.CanonicalMessage.SenderPrincipalID != fixture.target.CorePrincipalID ||
		!reflect.DeepEqual(completed.CanonicalMessage.Recipients, []string{fixture.target.PeerPrincipalID}) ||
		completed.CanonicalMessage.ReplyTo != "" || completed.CanonicalMessage.CausationID != fixture.wire.newRoot.MessageID || completed.CanonicalMessage.HopCount != 0 {
		t.Fatalf("new-root completion did not bind BOARD root receipt and canonical tuple: %#v", completed)
	}
	if fixture.wire.ackCalls != 0 {
		t.Fatal("new-root C2b delivery ACKed an inbox item; ACK ownership remains with C2a")
	}
}

func TestRunBoardPublicationDeliveryKeepsLegacyNewRootUnmentioned(t *testing.T) {
	fixture := newBoardPublicationDeliveryTestFixture(t)
	fixture.lease.TargetKind = "new_root"
	fixture.lease.Title = "Legacy owner request title"
	fixture.lease.PublicationIntent.Target.ThreadID = ""
	fixture.lease.PublicationIntent.Target.ReplyTo = ""
	fixture.lease.CanonicalResponse.Payload["text"] = "A legacy response containing @peer-other as plain text."
	fixture.lease.CanonicalResponse.Payload["content"] = fixture.lease.CanonicalResponse.Payload["text"]
	delete(fixture.lease.CanonicalResponse.Payload, "mentions")
	fixture.target.ThreadID = ""
	fixture.target.ReplyTo = ""
	fixture.wire.newRoot = &boardPublicationNewRootTestTarget{
		ThreadID: "thread-root-legacy", MessageID: "board-root-legacy", Title: fixture.lease.Title,
		ChannelID: fixture.target.ChannelID, CorePrincipalID: fixture.target.CorePrincipalID,
		PeerPrincipalID: fixture.target.PeerPrincipalID,
	}
	journal := newBoardPublicationDeliveryTestJournal(fixture.lease, fixture.trace)

	progressed, err := runBoardPublicationDelivery(context.Background(), fixture.wire.client, journal, fixture.input)
	if err != nil || !progressed {
		t.Fatalf("runBoardPublicationDelivery(legacy new-root) = progressed %t, err %v; want successful delivery", progressed, err)
	}
	if len(fixture.wire.sendBodies) != 1 {
		t.Fatalf("legacy new-root send count = %d; want one", len(fixture.wire.sendBodies))
	}
	var request boardPublicationDeliverySendWire
	if err := json.Unmarshal(fixture.wire.sendBodies[0], &request); err != nil {
		t.Fatalf("decode legacy new-root send: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(fixture.wire.sendBodies[0], &fields); err != nil {
		t.Fatalf("decode legacy new-root fields: %v", err)
	}
	if !reflect.DeepEqual(request.Recipients, []string{fixture.target.PeerPrincipalID}) || len(request.Mentions) != 0 {
		t.Fatalf("legacy new-root recipients/mentions = %#v/%#v; want canonical recipient and no structured mentions", request.Recipients, request.Mentions)
	}
	if _, exists := fields["mentions"]; exists {
		t.Fatal("legacy response without a durable explicit recipient gained a mentions field")
	}
}

func TestRunBoardPublicationDeliveryRejectsNewRootMentionOutsideCanonicalRecipient(t *testing.T) {
	fixture := newBoardPublicationDeliveryTestFixture(t)
	fixture.lease.TargetKind = "new_root"
	fixture.lease.Title = "Synthetic owner request title"
	fixture.lease.PublicationIntent.Target.ThreadID = ""
	fixture.lease.PublicationIntent.Target.ReplyTo = ""
	fixture.lease.CanonicalResponse.Payload["mentions"] = []string{"peer-other"}
	fixture.target.ThreadID = ""
	fixture.target.ReplyTo = ""
	fixture.wire.newRoot = &boardPublicationNewRootTestTarget{
		ThreadID: "thread-root-alpha", MessageID: "board-root-alpha", Title: fixture.lease.Title,
		ChannelID: fixture.target.ChannelID, CorePrincipalID: fixture.target.CorePrincipalID,
		PeerPrincipalID: fixture.target.PeerPrincipalID,
	}
	journal := newBoardPublicationDeliveryTestJournal(fixture.lease, fixture.trace)

	progressed, err := runBoardPublicationDelivery(context.Background(), fixture.wire.client, journal, fixture.input)
	if progressed || !errors.Is(err, ErrBoardInboxCanonicalMismatch) {
		t.Fatalf("runBoardPublicationDelivery(new-root recipient mismatch) = progressed %t, err %v; want canonical mismatch", progressed, err)
	}
	if len(journal.attempts) != 0 || len(journal.retries) != 1 || len(fixture.wire.sendBodies) != 0 {
		t.Fatalf("mismatched mention side effects attempts/retries/sends=%d/%d/%d; want 0/1/0", len(journal.attempts), len(journal.retries), len(fixture.wire.sendBodies))
	}
}

type boardPublicationDeliveryTestFixture struct {
	input  boardPublicationDeliveryInput
	scope  eventstore.BoardPublicationScope
	pin    eventstore.BoardConnectionPin
	item   boardcommunication.InboxItem
	source boardInboxSource
	claim  boardInboxClaim
	target conversationruntimeExternalTarget
	lease  eventstore.BoardPublicationLease
	trace  *boardDeliveryTrace
	wire   *boardPublicationDeliveryWire
}

// Keep the fixture's target fields local and route-neutral for the B2 peer lane.
type conversationruntimeExternalTarget struct {
	BoardInstanceID string
	CorePrincipalID string
	PeerPrincipalID string
	ChannelID       string
	ThreadID        string
	ReplyTo         string
}

func newBoardPublicationDeliveryTestFixture(t *testing.T) *boardPublicationDeliveryTestFixture {
	t.Helper()
	pin := boardInboxTestPin()
	now := time.Date(2026, 11, 5, 3, 8, 0, 0, time.UTC)
	item := boardInboxTestItem("item-delivery", "message-request", now.Add(-2*time.Minute), "Please explain this synthetic result.")
	sourceMessage := boardcommunication.BoardMessage{
		ID: item.MessageID, Seq: 8, By: item.SenderID, At: item.CreatedAt.UTC().Format(time.RFC3339Nano), Body: item.Body,
		Kind: "request", Mentions: []string{}, Recipients: []string{pin.PrincipalID},
		ChannelID: item.ChannelID, ThreadID: item.ThreadID, CausationID: item.MessageID, HopCount: 0,
	}
	binding := boardInboxBinding(pin, item, sourceMessage, "request")
	source := boardInboxSource{Item: item, Message: sourceMessage, Binding: binding}
	claim := boardInboxClaim{ClaimID: boardInboxClaimID(pin, item), ClaimReceipt: "synthetic-private-processing-receipt-current", LeaseUntil: now.Add(time.Minute)}
	target := conversationruntimeExternalTarget{
		BoardInstanceID: pin.JournalInstanceID, CorePrincipalID: pin.PrincipalID, PeerPrincipalID: item.SenderID,
		ChannelID: item.ChannelID, ThreadID: item.ThreadID, ReplyTo: item.MessageID,
	}
	response := modulecore.NewEventEnvelope(modulecore.NewTraceID(), modulecore.NewEventID(), nil, "conversation_runtime", "agent.response", now.Add(-time.Minute), map[string]any{
		"kind": "reply", "visibility": "public", "text": "Synthetic durable response.", "content": "Synthetic durable response.",
	})
	response.SessionID, response.TurnID, response.MessageID = modulecore.NewSessionID(), modulecore.NewTurnID(), modulecore.NewMessageID()
	response.ActorKind, response.ActorID = "agent", "mio"
	responseIndex := eventstore.MessageIndex{
		MessageID: response.MessageID, EventID: response.EventID, SessionID: response.SessionID, TurnID: response.TurnID,
		FromActor: "mio", ToKind: "external_peer", ToActor: item.SenderID, Kind: "assistant_message",
		ReplyToMessageID: modulecore.MessageID(item.MessageID), Visibility: "public",
	}
	publicationIntent := conversationruntime.ExternalPublicationIntent{
		CanonicalResponseMessageID: response.MessageID,
		Target: conversationruntime.ExternalReplyTarget{
			BoardInstanceID: target.BoardInstanceID, CorePrincipalID: target.CorePrincipalID, PeerPrincipalID: target.PeerPrincipalID,
			ChannelID: target.ChannelID, ThreadID: target.ThreadID, ReplyTo: target.ReplyTo,
		},
		StableLogicalKey: "external-publication:conversation.output:turn-delivery-test",
	}
	lease := eventstore.BoardPublicationLease{
		Scope:  eventstore.BoardPublicationScope{OwnerID: pin.OwnerID, JournalInstanceID: pin.JournalInstanceID, PrincipalID: pin.PrincipalID},
		WorkID: "board-publication:conversation.output:turn-delivery-test", LogicalKey: publicationIntent.StableLogicalKey,
		LeaseToken: "lease-delivery-test", LeaseExpiresAt: now.Add(2 * time.Minute), Version: 4, QueueAttempt: 1,
		CanonicalResponse: response, ResponseIndex: responseIndex, PublicationIntent: publicationIntent,
	}
	trace := &boardDeliveryTrace{}
	wire := newBoardPublicationDeliveryWire(t, pin, item, sourceMessage, response, trace)
	return &boardPublicationDeliveryTestFixture{
		input: boardPublicationDeliveryInput{Scope: lease.Scope, Now: now, LeaseDuration: time.Minute},
		scope: lease.Scope, pin: pin, item: item, source: source, claim: claim, target: target, lease: lease, trace: trace, wire: wire,
	}
}

type boardDeliveryTrace struct {
	mu    sync.Mutex
	steps []string
}

func (trace *boardDeliveryTrace) add(step string) {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.steps = append(trace.steps, step)
}

func (trace *boardDeliveryTrace) snapshot() []string {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	return append([]string(nil), trace.steps...)
}

type boardPublicationDeliveryTestJournal struct {
	mu          sync.Mutex
	trace       *boardDeliveryTrace
	lease       eventstore.BoardPublicationLease
	available   bool
	claimCalls  int
	claimInputs []eventstore.BoardPublicationClaimInput
	attempts    []eventstore.BoardPublicationAttempt
	fences      []eventstore.BoardPublicationClaimFenceInput
	completions []eventstore.BoardPublicationCompletionInput
	retries     []eventstore.BoardPublicationRetryInput
}

func newBoardPublicationDeliveryTestJournal(lease eventstore.BoardPublicationLease, trace *boardDeliveryTrace) *boardPublicationDeliveryTestJournal {
	return &boardPublicationDeliveryTestJournal{lease: lease, trace: trace, available: true}
}

func (journal *boardPublicationDeliveryTestJournal) ClaimBoardPublication(_ context.Context, input eventstore.BoardPublicationClaimInput) (eventstore.BoardPublicationLease, bool, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	journal.claimCalls++
	journal.claimInputs = append(journal.claimInputs, input)
	journal.trace.add("store.claim")
	if !journal.available {
		return eventstore.BoardPublicationLease{}, false, nil
	}
	lease := journal.lease
	lease.Scope = input.Scope
	lease.LeaseToken = fmt.Sprintf("lease-delivery-%d", journal.claimCalls)
	lease.Version++
	lease.LeaseExpiresAt = input.Now.Add(input.LeaseDuration)
	lease.Attempts = append([]eventstore.BoardPublicationAttempt(nil), journal.attempts...)
	return lease, true, nil
}

func (journal *boardPublicationDeliveryTestJournal) RecordBoardPublicationAttempt(_ context.Context, input eventstore.BoardPublicationAttemptInput) (eventstore.BoardPublicationAttempt, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	journal.trace.add("store.record_attempt")
	for _, attempt := range journal.attempts {
		if attempt.LogicalKey == input.LogicalKey && attempt.PayloadSHA256 == input.PayloadSHA256 && bytes.Equal(attempt.PreparedBytes, input.PreparedBytes) {
			return attempt, nil
		}
	}
	var wire struct {
		ProcessingClaimID      string `json:"processing_claim_id"`
		ProcessingClaimReceipt string `json:"processing_claim_receipt"`
	}
	if err := json.Unmarshal(input.PreparedBytes, &wire); err != nil {
		return eventstore.BoardPublicationAttempt{}, err
	}
	hash := sha256.Sum256([]byte(wire.ProcessingClaimReceipt))
	attempt := eventstore.BoardPublicationAttempt{
		WorkID: input.Lease.WorkID, Ordinal: len(journal.attempts) + 1, LogicalKey: input.LogicalKey,
		PreparedBytes: append([]byte(nil), input.PreparedBytes...), PayloadSHA256: input.PayloadSHA256,
		ProcessingClaimID: wire.ProcessingClaimID, ProcessingClaimReceiptSHA256: hex.EncodeToString(hash[:]),
		ProcessingClaimLeaseUntil: input.ProcessingClaimLeaseUntil,
	}
	journal.attempts = append(journal.attempts, attempt)
	return attempt, nil
}

func (journal *boardPublicationDeliveryTestJournal) RecordBoardPublicationClaimFence(_ context.Context, input eventstore.BoardPublicationClaimFenceInput) (eventstore.BoardPublicationClaimFence, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	journal.trace.add("store.record_fence")
	journal.fences = append(journal.fences, input)
	return eventstore.BoardPublicationClaimFence{
		WorkID: input.Lease.WorkID, AttemptOrdinal: input.AttemptOrdinal, AttemptSHA256: input.AttemptSHA256,
		LeaseVersion: input.Lease.Version, FreshClaimID: input.Proof.FreshClaimID,
		FreshReceiptSHA256: input.Proof.FreshReceiptSHA256, FreshLeaseUntil: input.Proof.FreshLeaseUntil,
	}, nil
}

func (journal *boardPublicationDeliveryTestJournal) RetryBoardPublication(_ context.Context, input eventstore.BoardPublicationRetryInput) error {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	journal.trace.add("store.retry")
	journal.retries = append(journal.retries, input)
	return nil
}

func (journal *boardPublicationDeliveryTestJournal) CompleteBoardPublication(_ context.Context, input eventstore.BoardPublicationCompletionInput) (eventstore.BoardPublicationOutcome, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	journal.trace.add("store.complete")
	journal.completions = append(journal.completions, input)
	return eventstore.BoardPublicationOutcome{
		Scope: input.Lease.Scope, WorkID: input.Lease.WorkID, LogicalKey: input.Lease.LogicalKey,
		AttemptOrdinal: input.AttemptOrdinal, PayloadSHA256: input.AttemptSHA256,
		SendReceipt: input.SendReceipt, CanonicalMessage: input.CanonicalMessage,
		ExpectedPeerPrincipalID: input.Lease.PublicationIntent.Target.PeerPrincipalID,
	}, nil
}

type boardPublicationDeliveryWire struct {
	t                   *testing.T
	server              *httptest.Server
	client              *boardcommunication.Client
	trace               *boardDeliveryTrace
	pin                 eventstore.BoardConnectionPin
	item                boardcommunication.InboxItem
	identity            boardcommunication.ConnectionIdentity
	sourceMessage       boardcommunication.BoardMessage
	response            modulecore.EventEnvelope
	mu                  sync.Mutex
	messages            []boardcommunication.BoardMessage
	sendBodies          [][]byte
	sendPaths           []string
	sendCalls           int
	inboxCalls          int
	claimCalls          int
	inboxReceiptCalls   int
	ackCalls            int
	rootReadCalls       int
	dropFirstSend       bool
	sendClaimFenceFirst bool
	sendStatus          int
	claimResult         boardcommunication.ClaimResult
	readbackMutation    string
	newRoot             *boardPublicationNewRootTestTarget
	rootStored          bool
	rootPreparedBytes   []byte
	rootMessage         boardcommunication.BoardMessage
	rootIdempotencyKey  string
}

type boardPublicationNewRootTestTarget struct {
	ThreadID        string
	MessageID       string
	Title           string
	ChannelID       string
	CorePrincipalID string
	PeerPrincipalID string
}

func newBoardPublicationDeliveryWire(t *testing.T, pin eventstore.BoardConnectionPin, item boardcommunication.InboxItem, source boardcommunication.BoardMessage, response modulecore.EventEnvelope, trace *boardDeliveryTrace) *boardPublicationDeliveryWire {
	t.Helper()
	wire := &boardPublicationDeliveryWire{
		t: t, trace: trace, pin: pin, item: item, identity: boardcommunication.ConnectionIdentity{PrincipalID: pin.PrincipalID, JournalInstanceID: pin.JournalInstanceID},
		sourceMessage: source, response: response, messages: []boardcommunication.BoardMessage{source},
		claimResult: boardcommunication.ClaimResult{
			MessageID: item.MessageID, ClaimID: boardInboxClaimID(pin, item), ClaimReceipt: "synthetic-private-processing-receipt-fresh",
			Status: "reacquired", LeaseUntil: timePtr(time.Date(2026, 11, 5, 3, 10, 0, 0, time.UTC)), CanProcess: true,
		},
	}
	wire.server = httptest.NewServer(http.HandlerFunc(wire.serveHTTP))
	client, err := boardcommunication.NewClient(boardcommunication.Options{
		URL:                wire.server.URL,
		ExpectedConnection: boardcommunication.ConnectionIdentity{PrincipalID: pin.PrincipalID, JournalInstanceID: pin.JournalInstanceID},
		TokenSource:        func(context.Context) ([]byte, error) { return []byte(boardInboxTestBearer), nil },
	})
	if err != nil {
		t.Fatalf("NewClient(synthetic pinned BOARD endpoint): %v", err)
	}
	wire.client = client
	t.Cleanup(func() {
		wire.client.Close()
		wire.server.Close()
	})
	return wire
}

func (wire *boardPublicationDeliveryWire) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+boardInboxTestBearer ||
		r.Header.Get("X-BOARD-Principal-ID") != wire.pin.PrincipalID ||
		r.Header.Get("X-BOARD-Journal-Instance-ID") != wire.pin.JournalInstanceID {
		wire.t.Errorf("synthetic BOARD auth or pinned headers differ: principal=%q journal=%q", r.Header.Get("X-BOARD-Principal-ID"), r.Header.Get("X-BOARD-Journal-Instance-ID"))
		writeBoardDeliveryJSON(w, http.StatusForbidden, map[string]any{"error": "authentication_failed"})
		return
	}
	w.Header().Set("X-Correlation-ID", "correlation-delivery")
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/connection":
		wire.trace.add("wire.identity")
		writeBoardDeliveryJSON(w, http.StatusOK, map[string]any{
			"principal_id": wire.identity.PrincipalID, "journal_instance_id": wire.identity.JournalInstanceID,
			"correlation_id": "correlation-delivery",
		})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/contexts":
		wire.trace.add("wire.contexts")
		writeBoardDeliveryJSON(w, http.StatusOK, map[string]any{
			"contexts":       []boardcommunication.BoardContext{{ID: wire.item.ChannelID, Label: "Synthetic channel"}},
			"correlation_id": "correlation-delivery",
		})
	case wire.newRoot != nil && r.Method == http.MethodGet && r.URL.Path == "/api/v1/threads/"+wire.newRoot.ThreadID:
		wire.mu.Lock()
		wire.rootReadCalls++
		message := wire.rootMessage
		target := *wire.newRoot
		wire.mu.Unlock()
		wire.trace.add("wire.root_readback")
		thread := boardcommunication.BoardThread{
			ID: target.ThreadID, Space: target.ChannelID, Title: target.Title, Summary: "synthetic root",
			Status: "open", Participants: []string{target.CorePrincipalID, target.PeerPrincipalID},
			CreatedAt: "2026-11-05T03:08:01Z", UpdatedAt: "2026-11-05T03:08:01Z",
			Messages: []boardcommunication.BoardMessage{message},
		}
		writeBoardDeliveryJSON(w, http.StatusOK, map[string]any{"thread": thread, "next_cursor": "", "correlation_id": "correlation-delivery"})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/threads/"+wire.item.ThreadID:
		wire.trace.add("wire.readback")
		wire.mu.Lock()
		messages := append([]boardcommunication.BoardMessage(nil), wire.messages...)
		mutation := wire.readbackMutation
		wire.mu.Unlock()
		if mutation != "" && len(messages) > 1 {
			mutateBoardDeliveryReadback(&messages[len(messages)-1], mutation)
		}
		thread := boardInboxTestThread(wire.item, "request", "").Thread
		thread.Messages = messages
		thread.UpdatedAt = "2026-11-05T03:09:00Z"
		writeBoardDeliveryJSON(w, http.StatusOK, map[string]any{"thread": thread, "next_cursor": "", "correlation_id": "correlation-delivery"})
	case wire.newRoot != nil && r.Method == http.MethodPost && r.URL.Path == "/api/v1/threads":
		body, err := io.ReadAll(r.Body)
		if err != nil {
			wire.t.Errorf("read synthetic new-root send body: %v", err)
			return
		}
		var request boardPublicationDeliverySendWire
		if err := json.Unmarshal(body, &request); err != nil {
			wire.t.Errorf("decode synthetic new-root send: %v", err)
		}
		wire.mu.Lock()
		wire.sendCalls++
		call := wire.sendCalls
		wire.sendBodies = append(wire.sendBodies, append([]byte(nil), body...))
		wire.sendPaths = append(wire.sendPaths, r.URL.Path)
		drop := wire.dropFirstSend && call == 1
		target := *wire.newRoot
		if r.Header.Get("Idempotency-Key") != request.IdempotencyKey {
			wire.t.Errorf("new-root idempotency header=%q body key=%q", r.Header.Get("Idempotency-Key"), request.IdempotencyKey)
		}
		if request.ThreadID != "" || request.ReplyTo != "" || request.ProcessingClaimID != "" || request.ProcessingClaimReceipt != "" {
			wire.t.Errorf("new-root wire request carries thread/reply/claim proof: %#v", request)
		}
		if wire.rootStored {
			if !bytes.Equal(body, wire.rootPreparedBytes) || request.IdempotencyKey != wire.rootIdempotencyKey {
				wire.t.Errorf("unknown root send was not replayed with identical bytes and key")
			}
		} else {
			wire.rootStored = true
			wire.rootPreparedBytes = append([]byte(nil), body...)
			wire.rootIdempotencyKey = request.IdempotencyKey
			wire.rootMessage = boardcommunication.BoardMessage{
				ID: target.MessageID, Seq: 1, By: target.CorePrincipalID, At: "2026-11-05T03:08:01Z",
				Body: request.Body, Kind: request.Kind, Mentions: append([]string(nil), request.Mentions...),
				Recipients: append([]string(nil), request.Recipients...), ChannelID: request.ChannelID,
				ThreadID: target.ThreadID, CausationID: target.MessageID, HopCount: 0,
			}
		}
		wire.mu.Unlock()
		wire.trace.add("wire.root_send")
		if drop {
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				wire.t.Errorf("synthetic server cannot hijack to simulate lost root send response")
				return
			}
			conn, _, err := hijacker.Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		w.WriteHeader(http.StatusCreated)
		if err := json.NewEncoder(w).Encode(map[string]any{
			"receipt":        boardcommunication.SendReceipt{MessageID: target.MessageID, ThreadID: target.ThreadID, StoredAt: time.Date(2026, 11, 5, 3, 8, 1, 0, time.UTC), Status: "stored"},
			"correlation_id": "correlation-delivery",
		}); err != nil {
			wire.t.Errorf("write synthetic root send receipt: %v", err)
		}
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/threads/"+wire.item.ThreadID+"/messages":
		body, err := io.ReadAll(r.Body)
		if err != nil {
			wire.t.Errorf("read synthetic send body: %v", err)
			return
		}
		wire.mu.Lock()
		wire.sendCalls++
		call := wire.sendCalls
		wire.sendBodies = append(wire.sendBodies, append([]byte(nil), body...))
		wire.sendPaths = append(wire.sendPaths, r.URL.Path)
		claimFence := wire.sendClaimFenceFirst && call == 1
		drop := wire.dropFirstSend && call == 1
		status := wire.sendStatus
		wire.mu.Unlock()
		wire.trace.add("wire.send")
		var request boardPublicationDeliverySendWire
		if err := json.Unmarshal(body, &request); err != nil {
			wire.t.Errorf("decode synthetic exact send: %v", err)
		}
		if r.Header.Get("Idempotency-Key") != request.IdempotencyKey {
			wire.t.Errorf("send idempotency header=%q body key=%q", r.Header.Get("Idempotency-Key"), request.IdempotencyKey)
		}
		if claimFence {
			writeBoardDeliveryJSON(w, http.StatusBadRequest, map[string]any{"error": "processing_claim_required_or_expired"})
			return
		}
		if status != 0 {
			writeBoardDeliveryJSON(w, status, map[string]any{"error": "authentication_failed"})
			return
		}
		wire.mu.Lock()
		if call == 1 || len(wire.messages) == 1 {
			wire.messages = append(wire.messages, boardcommunication.BoardMessage{
				ID: "board-response-alpha", Seq: 9, By: wire.pin.PrincipalID,
				At: "2026-11-05T03:08:01Z", Body: request.Body, Kind: request.Kind,
				Mentions: append([]string(nil), request.Mentions...), Recipients: append([]string(nil), request.Recipients...),
				ChannelID: request.ChannelID, ThreadID: wire.item.ThreadID, ReplyTo: request.ReplyTo,
				CausationID: wire.sourceMessage.ID, HopCount: 1,
			})
		}
		wire.mu.Unlock()
		if drop {
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				wire.t.Errorf("synthetic server cannot hijack to simulate lost send response")
				return
			}
			conn, _, err := hijacker.Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		w.WriteHeader(http.StatusCreated)
		if err := json.NewEncoder(w).Encode(map[string]any{
			"receipt":        boardcommunication.SendReceipt{MessageID: "board-response-alpha", ThreadID: wire.item.ThreadID, StoredAt: time.Date(2026, 11, 5, 3, 8, 1, 0, time.UTC), Status: "stored"},
			"correlation_id": "correlation-delivery",
		}); err != nil {
			wire.t.Errorf("write synthetic send receipt: %v", err)
		}
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/inbox":
		wire.mu.Lock()
		wire.inboxCalls++
		wire.mu.Unlock()
		wire.trace.add("wire.inbox")
		writeBoardDeliveryJSON(w, http.StatusOK, map[string]any{"items": []boardcommunication.InboxItem{wire.item}, "next_cursor": "", "correlation_id": "correlation-delivery"})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/inbox/"+wire.item.ItemID+"/receipt":
		wire.mu.Lock()
		wire.inboxReceiptCalls++
		wire.mu.Unlock()
		wire.trace.add("wire.inbox_receipt")
		writeBoardDeliveryJSON(w, http.StatusOK, map[string]any{"item_id": wire.item.ItemID, "message_id": wire.item.MessageID, "ack_receipt": "synthetic-ack-receipt-fresh", "correlation_id": "correlation-delivery"})
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/inbox/"+wire.item.ItemID+"/processing-claim":
		var request boardcommunication.ClaimRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			wire.t.Errorf("decode synthetic fresh claim: %v", err)
		}
		wire.mu.Lock()
		wire.claimCalls++
		result := wire.claimResult
		wire.mu.Unlock()
		wire.trace.add("wire.claim")
		if request.Receipt != "synthetic-ack-receipt-fresh" || request.ClaimID != boardInboxClaimID(wire.pin, wire.item) || request.LeaseSeconds != boardInboxClaimLeaseSeconds {
			wire.t.Errorf("fresh claim request = %#v; want exact fresh inbox receipt, stable source claim id, bounded lease", request)
		}
		writeBoardDeliveryJSON(w, http.StatusOK, map[string]any{
			"message_id": result.MessageID, "claim_id": result.ClaimID, "claim_receipt": result.ClaimReceipt,
			"status": result.Status, "lease_until": result.LeaseUntil, "can_process": result.CanProcess,
			"correlation_id": "correlation-delivery",
		})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/ack"):
		wire.mu.Lock()
		wire.ackCalls++
		wire.mu.Unlock()
		wire.trace.add("wire.ack")
		writeBoardDeliveryJSON(w, http.StatusOK, map[string]any{"item_id": wire.item.ItemID, "status": "acked", "correlation_id": "correlation-delivery"})
	default:
		wire.t.Errorf("unexpected synthetic BOARD request: %s %s", r.Method, r.URL.String())
		http.NotFound(w, r)
	}
}

type boardPublicationDeliverySendWire struct {
	ChannelID              string   `json:"channel_id"`
	ThreadID               string   `json:"thread_id,omitempty"`
	Title                  string   `json:"title,omitempty"`
	Body                   string   `json:"body"`
	Recipients             []string `json:"recipients"`
	Mentions               []string `json:"mentions,omitempty"`
	ReplyTo                string   `json:"reply_to,omitempty"`
	Kind                   string   `json:"kind,omitempty"`
	IdempotencyKey         string   `json:"idempotency_key"`
	ProcessingClaimID      string   `json:"processing_claim_id,omitempty"`
	ProcessingClaimReceipt string   `json:"processing_claim_receipt,omitempty"`
}

func writeBoardDeliveryJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Correlation-ID", "correlation-delivery")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func mutateBoardDeliveryReadback(message *boardcommunication.BoardMessage, field string) {
	switch field {
	case "body":
		message.Body += " changed"
	case "mentions":
		message.Mentions = []string{"mention-other"}
	case "peer":
		message.Recipients = []string{"peer-other"}
	case "channel":
		message.ChannelID = "channel-other"
	case "thread":
		message.ThreadID = "thread-other"
	case "reply":
		message.ReplyTo = "message-other"
	case "causation":
		message.CausationID = "cause-other"
	case "hop":
		message.HopCount++
	}
}

func orderedBoardDeliverySteps(steps []string, first, second, third, fourth string) bool {
	position := -1
	for _, wanted := range []string{first, second, third, fourth} {
		found := -1
		for index := position + 1; index < len(steps); index++ {
			if steps[index] == wanted {
				found = index
				break
			}
		}
		if found < 0 {
			return false
		}
		position = found
	}
	return true
}

func timePtr(value time.Time) *time.Time { return &value }

var _ boardPublicationDeliveryJournal = (*boardPublicationDeliveryTestJournal)(nil)
