package eventstore

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestRecordBoardTerminalReplyWaitsForExactNewRootOutcomeAndPersistsPrivateEvent(t *testing.T) {
	fixture := newBoardOwnerRequestPublicationTestFixture(t)
	ctx := context.Background()
	parentBoardMessageID := "board-root-terminal-alpha"
	source := boardTerminalReplySourceForTest(fixture, parentBoardMessageID)
	beforePending, err := boardReadbackSnapshot(fixture.store)
	if err != nil {
		t.Fatalf("snapshot before pending terminal lookup: %v", err)
	}
	if receipt, recorded, err := fixture.store.RecordBoardTerminalReply(ctx, source); err != nil || recorded || receipt.EventID != "" {
		t.Fatalf("RecordBoardTerminalReply(before new-root outcome) = %#v, recorded %t, err %v; want explicit pending with no mutation", receipt, recorded, err)
	}
	afterPending, err := boardReadbackSnapshot(fixture.store)
	if err != nil || afterPending != beforePending {
		t.Fatalf("pending terminal lookup changed canonical state: before=%#v after=%#v err=%v", beforePending, afterPending, err)
	}

	outcome := completeNewRootPublicationForReadbackTest(t, fixture, fixture.lease, fixture.now, parentBoardMessageID, source.Binding.ThreadID)
	if outcome.CanonicalMessage.ReplyTo != "" || outcome.CanonicalMessage.CausationID != parentBoardMessageID || outcome.CanonicalMessage.HopCount != 0 ||
		outcome.CanonicalMessage.ThreadID != source.Binding.ThreadID || outcome.CanonicalMessage.ChannelID != source.Binding.ChannelID ||
		outcome.ExpectedPeerPrincipalID != source.Binding.SenderPrincipalID {
		t.Fatalf("fixture did not create the exact completed BOARD new-root parent: %#v", outcome)
	}
	beforeTerminal, err := boardReadbackSnapshot(fixture.store)
	if err != nil {
		t.Fatalf("snapshot before terminal record: %v", err)
	}
	receipt, recorded, err := fixture.store.RecordBoardTerminalReply(ctx, source)
	if err != nil || !recorded {
		t.Fatalf("RecordBoardTerminalReply(exact current reply to completed new-root) = %#v, recorded %t, err %v; want durable terminal", receipt, recorded, err)
	}
	if receipt.OperationID == "" || receipt.Scope != fixture.scope || receipt.BoardMessageID != source.Binding.MessageID ||
		receipt.ParentBoardMessageID != parentBoardMessageID || receipt.BoardPublicationWorkID != outcome.WorkID ||
		receipt.SessionID != fixture.lease.CanonicalResponse.SessionID || receipt.TurnID != fixture.lease.CanonicalResponse.TurnID ||
		receipt.ParentCoreEventID != fixture.lease.CanonicalResponse.EventID || receipt.ParentCoreMessageID != fixture.lease.CanonicalResponse.MessageID ||
		receipt.EventID == "" || receipt.EventSeq <= fixture.lease.CanonicalResponse.EventSeq || len(receipt.SourceSHA256) != 64 || len(receipt.BodySHA256) != 64 {
		t.Fatalf("terminal receipt does not bind the exact BOARD and canonical CORE parent: %#v", receipt)
	}
	event := readBoardTerminalEventForTest(t, fixture.store, receipt.EventID)
	bodyPayload, bodyPayloadOK := event.Payload["body"].(map[string]any)
	if event.EventType != "conversation.external_peer_terminal_reply" || event.ComponentID == "orchestrator" ||
		event.SessionID != fixture.lease.CanonicalResponse.SessionID || event.TurnID != fixture.lease.CanonicalResponse.TurnID ||
		event.TraceID != fixture.lease.CanonicalResponse.TraceID || event.CausationEventID != fixture.lease.CanonicalResponse.EventID ||
		event.Payload["visibility"] != "private" || !bodyPayloadOK || bodyPayload["text"] != source.Body ||
		event.Payload["text"] != source.Body || event.Payload["content"] != source.Body ||
		event.Payload["parent_board_message_id"] != parentBoardMessageID ||
		event.Payload["parent_core_event_id"] != string(fixture.lease.CanonicalResponse.EventID) ||
		event.Payload["parent_core_message_id"] != string(fixture.lease.CanonicalResponse.MessageID) {
		t.Fatalf("private terminal event lost exact body, identity, or canonical causal references: %#v", event)
	}
	var messageIndexCount int
	if err := fixture.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM live_message_index WHERE event_id=?`, string(receipt.EventID)).Scan(&messageIndexCount); err != nil {
		t.Fatalf("count public message indexes for private terminal event: %v", err)
	}
	if messageIndexCount != 0 {
		t.Fatalf("private terminal event has %d public message indexes, want none", messageIndexCount)
	}
	var receiptJSON string
	if err := fixture.store.db.QueryRowContext(ctx, `SELECT result_json FROM live_operation_receipt WHERE operation_id=?`, receipt.OperationID).Scan(&receiptJSON); err != nil {
		t.Fatalf("read immutable terminal receipt: %v", err)
	}
	if strings.Contains(receiptJSON, source.Body) {
		t.Fatalf("terminal receipt duplicated authoritative reply body: %s", receiptJSON)
	}
	afterTerminal, err := boardReadbackSnapshot(fixture.store)
	if err != nil || afterTerminal.Events != beforeTerminal.Events+1 || afterTerminal.Turns != beforeTerminal.Turns ||
		afterTerminal.Work != beforeTerminal.Work || afterTerminal.Receipts != beforeTerminal.Receipts+1 {
		t.Fatalf("terminal record created a turn/work or failed to append one event and one receipt: before=%#v after=%#v err=%v", beforeTerminal, afterTerminal, err)
	}

	if err := fixture.reopen(t); err != nil {
		t.Fatalf("reopen terminal readback store: %v", err)
	}
	replayed, replayedFound, err := fixture.store.RecordBoardTerminalReply(ctx, source)
	if err != nil || !replayedFound || !reflect.DeepEqual(replayed, receipt) {
		t.Fatalf("RecordBoardTerminalReply(exact replay after reopen) = %#v, recorded %t, err %v; want exact immutable receipt", replayed, replayedFound, err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*BoardTerminalReplySource)
	}{
		{name: "body changed", mutate: func(source *BoardTerminalReplySource) { source.Body += " changed" }},
		{name: "cause changed", mutate: func(source *BoardTerminalReplySource) { source.CausationID = "board-root-other" }},
		{name: "thread changed", mutate: func(source *BoardTerminalReplySource) { source.Binding.ThreadID = "thread-other" }},
		{name: "scope changed", mutate: func(source *BoardTerminalReplySource) { source.Scope.OwnerID = "owner-other" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := source
			test.mutate(&changed)
			if _, found, err := fixture.store.RecordBoardTerminalReply(ctx, changed); found || err == nil {
				t.Fatalf("RecordBoardTerminalReply(changed %s) = found %t, err %v; want fail-closed conflict", test.name, found, err)
			}
		})
	}
	final, err := boardReadbackSnapshot(fixture.store)
	if err != nil || final != afterTerminal {
		t.Fatalf("conflicting terminal replies changed durable state: before=%#v after=%#v err=%v", afterTerminal, final, err)
	}
}

func TestRecordBoardTerminalReplyAcceptsScopedPeerReplyParentAndPreservesBoardRootCausation(t *testing.T) {
	fixture := newBoardPublicationDeliveryFixtureForResponder(t, "shiro", true)
	ctx := context.Background()
	parent, parentLease := completePeerReplyPublicationForReadbackTest(t, fixture)
	rootBoardMessageID := fixture.bundle.PublicationIntent.Target.ReplyTo
	if parentLease.TargetKind != boardPublicationTargetReply || parentLease.Title != "" ||
		parent.CanonicalMessage.ThreadID != parentLease.PublicationIntent.Target.ThreadID ||
		parent.CanonicalMessage.ReplyTo != rootBoardMessageID || parent.CanonicalMessage.CausationID != rootBoardMessageID ||
		parent.CanonicalMessage.HopCount != 1 || parent.ExpectedPeerPrincipalID != fixture.lookup.SourceBinding.SenderPrincipalID {
		t.Fatalf("fixture did not create the exact published peer reply parent: lease=%#v outcome=%#v", parentLease, parent)
	}
	source := BoardTerminalReplySource{
		Scope: fixture.scope,
		Binding: domainconversation.ExternalPeerBinding{
			BoardInstanceID: fixture.scope.JournalInstanceID, RecipientPrincipalID: fixture.scope.PrincipalID, ResponderActorID: "shiro",
			ChannelID: parent.CanonicalMessage.ChannelID, ThreadID: parent.CanonicalMessage.ThreadID,
			MessageID: "board-peer-terminal-reply-beta", SenderPrincipalID: parent.ExpectedPeerPrincipalID,
			WireKind: "reply", Classification: domainconversation.ExternalPeerClassificationCorrelatedReplyTerminal,
			ReplyTo: parent.CanonicalMessage.MessageID,
		},
		InboxItemID: "inbox-peer-terminal-beta", InboxCreatedAt: fixture.now.Add(time.Second),
		MessageAt: fixture.now.Add(2 * time.Second), ThreadCreatedAt: fixture.accepted.InputEvent.OccurredAt,
		Body:       "A synthetic terminal reply to the published Shiro reply.",
		Recipients: []string{fixture.scope.PrincipalID}, Mentions: []string{fixture.scope.PrincipalID},
		CausationID: parent.CanonicalMessage.CausationID, HopCount: parent.CanonicalMessage.HopCount + 1,
	}
	before, err := boardReadbackSnapshot(fixture.store)
	if err != nil {
		t.Fatalf("snapshot before peer-reply terminal record: %v", err)
	}
	receipt, recorded, err := fixture.store.RecordBoardTerminalReply(ctx, source)
	if err != nil || !recorded {
		t.Fatalf("RecordBoardTerminalReply(exact peer reply to published Shiro reply) = %#v, recorded %t, err %v; want durable terminal without another generation", receipt, recorded, err)
	}
	if receipt.Scope != fixture.scope || receipt.BoardMessageID != source.Binding.MessageID ||
		receipt.ParentBoardMessageID != parent.CanonicalMessage.MessageID || receipt.BoardPublicationWorkID != parent.WorkID ||
		receipt.SessionID != fixture.accepted.SessionID || receipt.TurnID != fixture.accepted.TurnID ||
		receipt.ParentCoreEventID != fixture.bundle.CanonicalEvents[0].EventID ||
		receipt.ParentCoreMessageID != fixture.bundle.CanonicalEvents[0].MessageID {
		t.Fatalf("peer-reply terminal receipt does not bind the immediate BOARD parent and exact canonical CORE response: %#v", receipt)
	}
	event := readBoardTerminalEventForTest(t, fixture.store, receipt.EventID)
	boardSource, boardSourceOK := event.Payload["board_source"].(map[string]any)
	if event.CausationEventID != receipt.ParentCoreEventID || event.SessionID != fixture.accepted.SessionID || event.TurnID != fixture.accepted.TurnID ||
		event.Payload["parent_board_message_id"] != parent.CanonicalMessage.MessageID || !boardSourceOK ||
		boardSource["causation_id"] != rootBoardMessageID || boardSource["hop_count"] != float64(2) {
		t.Fatalf("terminal event did not preserve separate CORE parent and BOARD root-causation chains: %#v", event)
	}
	after, err := boardReadbackSnapshot(fixture.store)
	if err != nil || after.Events != before.Events+1 || after.Turns != before.Turns || after.Work != before.Work || after.Receipts != before.Receipts+1 {
		t.Fatalf("peer-reply terminal record generated another turn/work or failed to append one event and receipt: before=%#v after=%#v err=%v", before, after, err)
	}

	fixture.reopen(t)
	replayed, found, err := fixture.store.RecordBoardTerminalReply(ctx, source)
	if err != nil || !found || !reflect.DeepEqual(replayed, receipt) {
		t.Fatalf("RecordBoardTerminalReply(exact peer-reply replay after reopen) = %#v, found %t, err %v; want immutable receipt", replayed, found, err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*BoardTerminalReplySource)
	}{
		{name: "root causation", mutate: func(value *BoardTerminalReplySource) { value.CausationID = "board-cause-other" }},
		{name: "hop count", mutate: func(value *BoardTerminalReplySource) { value.HopCount++ }},
		{name: "immediate parent", mutate: func(value *BoardTerminalReplySource) { value.Binding.ReplyTo = "board-parent-other" }},
	} {
		changed := source
		test.mutate(&changed)
		if _, found, err := fixture.store.RecordBoardTerminalReply(ctx, changed); found || err == nil {
			t.Fatalf("RecordBoardTerminalReply(peer-reply %s mismatch) = found %t, err %v; want fail-closed rejection", test.name, found, err)
		}
	}
	final, err := boardReadbackSnapshot(fixture.store)
	if err != nil || final != after {
		t.Fatalf("mismatched peer-reply terminal retries changed durable state: before=%#v after=%#v err=%v", after, final, err)
	}
}

func TestBoardPublicationOutcomeByTurnAndNextDueUseScopedOutbox(t *testing.T) {
	fixture := newBoardOwnerRequestPublicationTestFixture(t)
	ctx := context.Background()
	lookup := BoardPublicationTurnLookupInput{
		Scope: fixture.scope, SessionID: fixture.lease.CanonicalResponse.SessionID, TurnID: fixture.lease.CanonicalResponse.TurnID,
	}
	if outcome, found, err := fixture.store.LoadBoardPublicationByTurn(ctx, lookup); err != nil || found || outcome.WorkID != "" {
		t.Fatalf("LoadBoardPublicationByTurn(before completed BOARD result) = %#v, found %t, err %v; want pending", outcome, found, err)
	}
	wrongScope := lookup
	wrongScope.Scope.PrincipalID = "principal-other"
	if _, found, err := fixture.store.LoadBoardPublicationByTurn(ctx, wrongScope); found || err == nil {
		t.Fatalf("LoadBoardPublicationByTurn(wrong principal) = found %t, err %v; want scope error", found, err)
	}
	due, err := fixture.store.NextBoardPublicationDue(ctx, fixture.scope, fixture.now)
	if err != nil || !due.Equal(fixture.lease.LeaseExpiresAt) {
		t.Fatalf("NextBoardPublicationDue(active lease) = %s, err %v; want the exact lease expiry %s", due, err, fixture.lease.LeaseExpiresAt)
	}
	if err := fixture.store.RetryBoardPublication(ctx, BoardPublicationRetryInput{Lease: fixture.lease, Now: fixture.now}); err != nil {
		t.Fatalf("RetryBoardPublication(synthetic pre-send retry): %v", err)
	}
	wantBackoff := fixture.now.Add(time.Second)
	due, err = fixture.store.NextBoardPublicationDue(ctx, fixture.scope, fixture.now)
	if err != nil || !due.Equal(wantBackoff) {
		t.Fatalf("NextBoardPublicationDue(pending retry) = %s, err %v; want backoff deadline %s", due, err, wantBackoff)
	}
	lease, available, err := fixture.store.ClaimBoardPublication(ctx, BoardPublicationClaimInput{
		Scope: fixture.scope, Now: wantBackoff, LeaseDuration: time.Minute,
	})
	if err != nil || !available {
		t.Fatalf("ClaimBoardPublication(after due backoff) = available %t, err %v", available, err)
	}
	due, err = fixture.store.NextBoardPublicationDue(ctx, fixture.scope, wantBackoff)
	if err != nil || !due.Equal(lease.LeaseExpiresAt) {
		t.Fatalf("NextBoardPublicationDue(reclaimed lease) = %s, err %v; want fresh lease expiry %s", due, err, lease.LeaseExpiresAt)
	}

	parentBoardMessageID := "board-root-turn-lookup"
	outcome := completeNewRootPublicationForReadbackTest(t, fixture, lease, wantBackoff, parentBoardMessageID, "thread-root-turn-lookup")
	loaded, found, err := fixture.store.LoadBoardPublicationByTurn(ctx, lookup)
	if err != nil || !found || !reflect.DeepEqual(loaded, outcome) {
		t.Fatalf("LoadBoardPublicationByTurn(completed new-root) = %#v, found %t, err %v; want exact outcome", loaded, found, err)
	}
	due, err = fixture.store.NextBoardPublicationDue(ctx, fixture.scope, wantBackoff)
	if err != nil || !due.IsZero() {
		t.Fatalf("NextBoardPublicationDue(after completion) = %s, err %v; want zero only with no eligible work", due, err)
	}
}

func TestReadPendingBoardOwnerRequestsReturnsValidatedOwnerScopedPage(t *testing.T) {
	ctx := context.Background()
	store := newBoardOwnerRequestStore(t, filepath.Join(t.TempDir(), "board-owner-pending-readback.sqlite"))
	pin := pinBoardOwnerRequestConnection(t, store)
	reservationInput := boardOwnerRequestReservationFixture(pin)
	reservation, err := store.ReserveBoardOwnerRequest(ctx, reservationInput)
	if err != nil {
		t.Fatalf("ReserveBoardOwnerRequest(pending readback fixture): %v", err)
	}
	sessionID := seedBoardOwnerRequestSession(t, store, pin.OwnerID, reservation.ActorID)
	accepted, err := store.AcceptBoardOwnerRequest(ctx, BoardOwnerRequestAcceptInput{
		Reservation: reservation, ActorID: reservation.ActorID, Instruction: reservationInput.Instruction,
		SessionID: sessionID, RuntimeVersion: conversationruntime.ConversationRuntimeV2, GenerationEpoch: 1,
	})
	if err != nil {
		t.Fatalf("AcceptBoardOwnerRequest(pending readback fixture): %v", err)
	}
	scope := BoardPublicationScope{OwnerID: pin.OwnerID, JournalInstanceID: pin.JournalInstanceID, PrincipalID: pin.PrincipalID}
	page, err := store.ReadPendingBoardOwnerRequests(ctx, scope, 0, 1)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("ReadPendingBoardOwnerRequests(exact owner scope) = %#v, err %v; want one accepted request", page, err)
	}
	pending := page.Items[0]
	wantFingerprint, err := boardOwnerRequestFingerprint(reservation.Source, reservationInput.Instruction)
	if err != nil {
		t.Fatalf("hash exact pending owner request fingerprint: %v", err)
	}
	if pending.Accepted.OperationID != accepted.OperationID || pending.Accepted.TurnID != accepted.TurnID ||
		pending.Accepted.SessionID != accepted.SessionID || pending.Accepted.InputMessageID != accepted.InputMessageID ||
		!reflect.DeepEqual(pending.Reservation, reservation) || pending.Instruction != reservationInput.Instruction ||
		pending.Lookup.Source != reservation.Source || pending.Lookup.OwnerID != pin.OwnerID || pending.Lookup.SessionID != accepted.SessionID ||
		pending.Lookup.TurnID != accepted.TurnID || pending.Lookup.GenerationEpoch != accepted.GenerationEpoch ||
		pending.Lookup.OperationID != boardOwnerRequestOutputOperationID(accepted.TurnID) || pending.Lookup.SourceFingerprint != wantFingerprint ||
		page.NextAdmissionSeq != accepted.AdmissionSeq {
		t.Fatalf("pending owner request omitted exact acceptance, reservation, or generation lookup: %#v", pending)
	}
	secondPage, err := store.ReadPendingBoardOwnerRequests(ctx, scope, page.NextAdmissionSeq, 1)
	if err != nil || len(secondPage.Items) != 0 {
		t.Fatalf("ReadPendingBoardOwnerRequests(after exact admission cursor) = %#v, err %v; want exhausted page", secondPage, err)
	}
	if _, err := store.ReadPendingBoardOwnerRequests(ctx, scope, 0, 0); err == nil {
		t.Fatal("ReadPendingBoardOwnerRequests accepted an unbounded empty page size")
	}
	wrongScope := scope
	wrongScope.OwnerID = "owner-other"
	if _, err := store.ReadPendingBoardOwnerRequests(ctx, wrongScope, 0, 1); err == nil {
		t.Fatal("ReadPendingBoardOwnerRequests exposed another owner's accepted request")
	}

	completed := newBoardOwnerRequestPublicationTestFixture(t)
	completedPage, err := completed.store.ReadPendingBoardOwnerRequests(ctx, completed.scope, 0, 1)
	if err != nil || len(completedPage.Items) != 0 {
		t.Fatalf("ReadPendingBoardOwnerRequests(after committed generated result) = %#v, err %v; want no pending generation work", completedPage, err)
	}
}

type boardReadbackCounts struct {
	Events   int
	Turns    int
	Work     int
	Receipts int
}

func boardReadbackSnapshotForStore(store *SQLiteStore) (boardReadbackCounts, error) {
	var snapshot boardReadbackCounts
	for query, destination := range map[string]*int{
		`SELECT COUNT(*) FROM event_envelope`:         &snapshot.Events,
		`SELECT COUNT(*) FROM live_conversation_turn`: &snapshot.Turns,
		`SELECT COUNT(*) FROM live_work_outbox`:       &snapshot.Work,
		`SELECT COUNT(*) FROM live_operation_receipt`: &snapshot.Receipts,
	} {
		if err := store.db.QueryRow(query).Scan(destination); err != nil {
			return boardReadbackCounts{}, err
		}
	}
	return snapshot, nil
}

func TestBoardPublicationByTurnReadbackUsesResponderScopedPin(t *testing.T) {
	fixture := newBoardPublicationDeliveryFixtureForResponder(t, "shiro", true)
	wantOutcome, lease := completePeerReplyPublicationForReadbackTest(t, fixture)
	lookup := BoardPublicationTurnLookupInput{
		Scope: fixture.scope, SessionID: lease.CanonicalResponse.SessionID, TurnID: lease.CanonicalResponse.TurnID,
		ResponderActorID: "shiro",
	}
	outcome, found, err := fixture.store.LoadBoardPublicationByTurn(context.Background(), lookup)
	if err != nil || !found || outcome.WorkID != wantOutcome.WorkID {
		t.Fatalf("LoadBoardPublicationByTurn(explicit Shiro responder with scoped pin) = %#v, found %t, err %v; want ACK-visible outcome", outcome, found, err)
	}
	legacyLookup := lookup
	legacyLookup.ResponderActorID = ""
	if _, found, err := fixture.store.LoadBoardPublicationByTurn(context.Background(), legacyLookup); found || !errors.Is(err, ErrBoardCommunicationReadbackScopeMismatch) {
		t.Fatalf("LoadBoardPublicationByTurn(omitted responder with scoped pin only) = found %t, err %v; want legacy pin rejection", found, err)
	}
}

func TestNextBoardPublicationDueForResponderFiltersOtherActorWork(t *testing.T) {
	ctx := context.Background()
	fixture := newBoardPublicationDeliveryFixtureForResponder(t, "shiro", true)
	otherInput := externalPeerAcceptInput(modulecore.NewSessionID(), "board-message-midori-delivery", "Synthetic Midori publication request.")
	otherInput.OperationID = "core-external-peer-midori-publication-operation"
	otherInput.ClientSubmissionID = "core-external-peer-midori-publication-submission"
	otherInput.ExternalPeerBinding.BoardInstanceID = fixture.scope.JournalInstanceID
	otherInput.ExternalPeerBinding.RecipientPrincipalID = "midori"
	otherInput.ExternalPeerBinding.ResponderActorID = "midori"
	otherInput.InputEvent.Payload["external_peer_binding"] = externalPeerBindingPayload(*otherInput.ExternalPeerBinding)
	otherInput.InputEvent.Payload["to"] = "midori"
	otherSourceActorID := "board:" + fixture.scope.JournalInstanceID + ":" + otherInput.ExternalPeerBinding.SenderPrincipalID
	otherInput.ActorID = otherSourceActorID
	otherInput.InputEvent.Payload["source_actor_id"] = otherSourceActorID
	otherInput.InputMessage.FromActor = otherSourceActorID
	otherInput.InputMessage.ToActor = "midori"
	accepted, err := fixture.store.AcceptTurn(ctx, otherInput)
	if err != nil {
		t.Fatalf("AcceptTurn(Midori publication source): %v", err)
	}
	_, bundle := externalPublicationCommitFixture(t, otherInput, accepted)
	otherPin := BoardConnectionPin{
		OwnerID: fixture.scope.OwnerID, JournalInstanceID: fixture.scope.JournalInstanceID,
		PrincipalID: "midori", CutoverAt: accepted.InputEvent.OccurredAt.UTC(),
	}
	if _, err := fixture.store.PinBoardConnectionForPrincipal(ctx, otherPin); err != nil {
		t.Fatalf("PinBoardConnectionForPrincipal(Midori): %v", err)
	}
	if _, err := fixture.store.CommitExternalTurnBundle(ctx, bundle); err != nil {
		t.Fatalf("CommitExternalTurnBundle(Midori publication): %v", err)
	}
	due, err := fixture.store.NextBoardPublicationDueForResponder(ctx, fixture.scope, "shiro", fixture.now)
	if err != nil || !due.Equal(fixture.now) {
		t.Fatalf("NextBoardPublicationDueForResponder(Shiro with Midori work for same owner) = %v, err %v; want Shiro due time %v", due, err, fixture.now)
	}
}

func boardReadbackSnapshot(store *SQLiteStore) (boardReadbackCounts, error) {
	return boardReadbackSnapshotForStore(store)
}

func boardTerminalReplySourceForTest(fixture *boardOwnerRequestPublicationTestFixture, parentBoardMessageID string) BoardTerminalReplySource {
	target := fixture.lease.PublicationIntent.Target
	return BoardTerminalReplySource{
		Scope: fixture.scope,
		Binding: domainconversation.ExternalPeerBinding{
			BoardInstanceID: fixture.scope.JournalInstanceID, RecipientPrincipalID: fixture.scope.PrincipalID,
			ChannelID: target.ChannelID, ThreadID: "thread-root-terminal-alpha", MessageID: "board-peer-terminal-reply-alpha",
			SenderPrincipalID: target.PeerPrincipalID, WireKind: "reply",
			Classification: domainconversation.ExternalPeerClassificationCorrelatedReplyTerminal, ReplyTo: parentBoardMessageID,
		},
		InboxItemID: "inbox-terminal-alpha", InboxCreatedAt: fixture.now.Add(time.Second),
		MessageAt: fixture.now.Add(2 * time.Second), ThreadCreatedAt: fixture.now,
		Body: "A synthetic correlated BOARD terminal reply.", Recipients: []string{fixture.scope.PrincipalID},
		Mentions: []string{fixture.scope.PrincipalID}, CausationID: parentBoardMessageID, HopCount: 1,
	}
}

func completeNewRootPublicationForReadbackTest(
	t *testing.T,
	fixture *boardOwnerRequestPublicationTestFixture,
	lease BoardPublicationLease,
	now time.Time,
	boardMessageID, boardThreadID string,
) BoardPublicationOutcome {
	t.Helper()
	prepared := boardOwnerRequestRootAttemptBytes(lease, lease.Title, "", "")
	attempt, err := fixture.store.RecordBoardPublicationAttempt(context.Background(), BoardPublicationAttemptInput{
		Lease: lease, Now: now, LogicalKey: lease.LogicalKey, PreparedBytes: prepared,
		PayloadSHA256: boardPublicationPayloadHash(prepared),
	})
	if err != nil {
		t.Fatalf("RecordBoardPublicationAttempt(synthetic new-root request): %v", err)
	}
	message := BoardPublicationCanonicalMessage{
		MessageID: boardMessageID, ThreadID: boardThreadID, ChannelID: lease.PublicationIntent.Target.ChannelID,
		SenderPrincipalID: lease.PublicationIntent.Target.CorePrincipalID, Recipients: []string{lease.PublicationIntent.Target.PeerPrincipalID},
		CausationID: boardMessageID, HopCount: 0,
	}
	receipt := BoardPublicationSendReceipt{MessageID: boardMessageID, ThreadID: boardThreadID, StoredAt: now, Status: "stored"}
	outcome, err := fixture.store.CompleteBoardPublication(context.Background(), BoardPublicationCompletionInput{
		Lease: lease, Now: now, AttemptOrdinal: attempt.Ordinal, AttemptSHA256: attempt.PayloadSHA256,
		SendReceipt: receipt, CanonicalMessage: message,
	})
	if err != nil {
		t.Fatalf("CompleteBoardPublication(synthetic exact new-root receipt): %v", err)
	}
	return outcome
}

func completePeerReplyPublicationForReadbackTest(t *testing.T, fixture *boardPublicationDeliveryFixture) (BoardPublicationOutcome, BoardPublicationLease) {
	t.Helper()
	ctx := context.Background()
	lease, available, err := fixture.store.ClaimBoardPublication(ctx, BoardPublicationClaimInput{
		Scope: fixture.scope, Now: fixture.now, LeaseDuration: time.Minute,
	})
	if err != nil || !available {
		t.Fatalf("ClaimBoardPublication(peer reply fixture) = available %t, err %v", available, err)
	}
	prepared := preparedBoardPublicationBytes(lease, "synthetic-peer-delivery-claim", "synthetic-private-peer-claim-receipt")
	attempt, err := fixture.store.RecordBoardPublicationAttempt(ctx, BoardPublicationAttemptInput{
		Lease: lease, Now: fixture.now, LogicalKey: lease.LogicalKey, PreparedBytes: prepared,
		PayloadSHA256: boardPublicationPayloadHash(prepared), ProcessingClaimLeaseUntil: fixture.now.Add(30 * time.Second),
	})
	if err != nil {
		t.Fatalf("RecordBoardPublicationAttempt(peer reply fixture): %v", err)
	}
	message := boardPublicationCanonicalMessage(lease)
	returnReceipt := BoardPublicationSendReceipt{MessageID: message.MessageID, ThreadID: message.ThreadID, StoredAt: fixture.now, Status: "stored"}
	outcome, err := fixture.store.CompleteBoardPublication(ctx, BoardPublicationCompletionInput{
		Lease: lease, Now: fixture.now, AttemptOrdinal: attempt.Ordinal, AttemptSHA256: attempt.PayloadSHA256,
		SendReceipt: returnReceipt, CanonicalMessage: message,
	})
	if err != nil {
		t.Fatalf("CompleteBoardPublication(peer reply fixture): %v", err)
	}
	return outcome, lease
}

func readBoardTerminalEventForTest(t *testing.T, store *SQLiteStore, eventID modulecore.EventID) modulecore.EventEnvelope {
	t.Helper()
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin read transaction for private terminal event: %v", err)
	}
	defer tx.Rollback()
	event, err := loadEventEnvelopeTx(context.Background(), tx, eventID)
	if err != nil {
		t.Fatalf("load private canonical terminal event %q: %v", eventID, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit private terminal event read: %v", err)
	}
	return event
}
