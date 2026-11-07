package eventstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestBoardPeerThreadSessionBindingIsStableByExactCanonicalThreadDate(t *testing.T) {
	fixture := newBoardPeerIngressFixture(t)
	ctx := context.Background()
	key := boardPeerThreadSessionKey(fixture.source)
	if !fixture.source.InboxCreatedAt.After(fixture.pin.CutoverAt) || !fixture.source.MessageAt.Before(fixture.pin.CutoverAt) ||
		fixture.source.ThreadCreatedAt.Equal(fixture.source.InboxCreatedAt) || fixture.source.ThreadCreatedAt.Equal(fixture.source.MessageAt) {
		t.Fatal("fixture must keep inbox, message, and thread creation times distinct across the cutover boundary")
	}
	if _, found, err := fixture.store.ReadBoardPeerThreadSession(ctx, key); err != nil || found {
		t.Fatalf("ReadBoardPeerThreadSession(before bind) = found %t, err %v; want absent exact mapping", found, err)
	}

	want := BoardPeerThreadSessionBinding{Key: key, SessionID: fixture.sessionID}
	bound, err := fixture.store.BindBoardPeerThreadSession(ctx, want)
	if err != nil || !reflect.DeepEqual(bound, want) {
		t.Fatalf("BindBoardPeerThreadSession(exact existing owner session) = %#v, err %v; want %#v", bound, err, want)
	}
	if replay, err := fixture.store.BindBoardPeerThreadSession(ctx, want); err != nil || !reflect.DeepEqual(replay, want) {
		t.Fatalf("BindBoardPeerThreadSession(exact retry) = %#v, err %v; want immutable idempotent mapping", replay, err)
	}
	fixture.reopen(t)
	if read, found, err := fixture.store.ReadBoardPeerThreadSession(ctx, key); err != nil || !found || !reflect.DeepEqual(read, want) {
		t.Fatalf("ReadBoardPeerThreadSession(after reopen) = %#v, found %t, err %v; want exact persisted binding", read, found, err)
	}

	changedSession := want
	changedSession.SessionID = fixture.otherSessionID
	if _, err := fixture.store.BindBoardPeerThreadSession(ctx, changedSession); err == nil {
		t.Fatal("BindBoardPeerThreadSession accepted a second CORE session for the same exact BOARD thread")
	}
	wrongDate := key
	wrongDate.ThreadCreatedAt = wrongDate.ThreadCreatedAt.Add(time.Hour)
	if _, found, err := fixture.store.ReadBoardPeerThreadSession(ctx, wrongDate); err != nil || found {
		t.Fatalf("ReadBoardPeerThreadSession(wrong canonical thread date) = found %t, err %v; want no mapping", found, err)
	}
	wrongScope := key
	wrongScope.Scope.OwnerID = "owner-other"
	if read, found, err := fixture.store.ReadBoardPeerThreadSession(ctx, wrongScope); found || read.SessionID != "" || err == nil {
		t.Fatalf("ReadBoardPeerThreadSession(wrong owner) = %#v, found %t, err %v; want fail-closed scope error", read, found, err)
	}
}

func TestAcceptBoardPeerRequestPersistsExactSourceAndReplaysAfterReopen(t *testing.T) {
	fixture := newBoardPeerIngressFixture(t)
	ctx := context.Background()
	key := boardPeerThreadSessionKey(fixture.source)
	if _, err := fixture.store.BindBoardPeerThreadSession(ctx, BoardPeerThreadSessionBinding{Key: key, SessionID: fixture.sessionID}); err != nil {
		t.Fatalf("BindBoardPeerThreadSession(exact thread owner mapping): %v", err)
	}
	if _, found, err := fixture.store.FindAcceptedBoardPeerRequest(ctx, fixture.source); err != nil || found {
		t.Fatalf("FindAcceptedBoardPeerRequest(before acceptance) = found %t, err %v; want exact valid source absent", found, err)
	}

	input := BoardPeerRequestAcceptInput{
		Source: fixture.source, SessionID: fixture.sessionID, RuntimeVersion: "v2", GenerationEpoch: fixture.generationEpoch,
	}
	accepted, err := fixture.store.AcceptBoardPeerRequest(ctx, input)
	if err != nil {
		t.Fatalf("AcceptBoardPeerRequest(authenticated canonical BOARD root request): %v", err)
	}
	if err := assertBoardPeerAcceptedInput(accepted, input); err != nil {
		t.Fatalf("accepted BOARD peer input: %v", err)
	}

	fixture.reopen(t)
	loaded, found, err := fixture.store.FindAcceptedBoardPeerRequest(ctx, fixture.source)
	if err != nil || !found || !sameBoardPeerJSON(loaded, accepted) {
		t.Fatalf("FindAcceptedBoardPeerRequest(after reopen) = %#v, found %t, err %v; want exact durable acceptance", loaded, found, err)
	}
	replayed, err := fixture.store.AcceptBoardPeerRequest(ctx, input)
	if err != nil || !sameBoardPeerJSON(replayed, accepted) {
		t.Fatalf("AcceptBoardPeerRequest(exact retry after reopen) = %#v, err %v; want original acceptance", replayed, err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*BoardPeerRequestSource)
	}{
		{name: "body changed", mutate: func(source *BoardPeerRequestSource) { source.Body += " changed" }},
		{name: "inbox time changed", mutate: func(source *BoardPeerRequestSource) { source.InboxCreatedAt = source.InboxCreatedAt.Add(time.Second) }},
		{name: "recipient metadata changed", mutate: func(source *BoardPeerRequestSource) { source.Recipients = []string{"peer-other"} }},
		{name: "BOARD cause changed", mutate: func(source *BoardPeerRequestSource) { source.CausationID = "board-root-other" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := fixture.source
			test.mutate(&changed)
			if result, found, err := fixture.store.FindAcceptedBoardPeerRequest(ctx, changed); found || err == nil {
				t.Fatalf("FindAcceptedBoardPeerRequest(changed %s) = %#v, found %t, err %v; want conflict, never absence", test.name, result, found, err)
			}
		})
	}

	wrongScope := fixture.source
	wrongScope.Scope.OwnerID = "owner-other"
	if result, found, err := fixture.store.FindAcceptedBoardPeerRequest(ctx, wrongScope); found || err == nil {
		t.Fatalf("FindAcceptedBoardPeerRequest(wrong owner) = %#v, found %t, err %v; want fail-closed scope error", result, found, err)
	}
	wrongSession := input
	wrongSession.SessionID = fixture.otherSessionID
	wrongSession.GenerationEpoch = fixture.otherGenerationEpoch
	if _, err := fixture.store.AcceptBoardPeerRequest(ctx, wrongSession); err == nil {
		t.Fatal("AcceptBoardPeerRequest accepted the exact BOARD source into a different CORE session")
	}

	tail, err := fixture.store.ReadCompleteSessionTail(ctx, fixture.sessionID, 0)
	if err != nil || !tail.Complete {
		t.Fatalf("ReadCompleteSessionTail(accepted BOARD input) = complete %t, err %v", tail.Complete, err)
	}
	var canonical *CanonicalMessage
	for index := range tail.Messages {
		if tail.Messages[index].Index.MessageID == accepted.InputMessageID {
			copy := tail.Messages[index]
			canonical = &copy
		}
	}
	if canonical == nil || !sameBoardPeerJSON(canonical.Event, accepted.InputEvent) ||
		canonical.Index.MessageID != accepted.InputMessageID || canonical.Index.EventID != accepted.InputEvent.EventID ||
		canonical.Index.SessionID != accepted.SessionID || canonical.Index.TurnID != accepted.TurnID ||
		canonical.Index.FromActor != "board:"+fixture.source.Binding.BoardInstanceID+":"+fixture.source.Binding.SenderPrincipalID ||
		canonical.Index.ToKind != "agent" || canonical.Index.ToActor != fixture.source.Binding.EffectiveResponderActorID() || canonical.Index.Kind != "external_peer_message" ||
		canonical.Index.Visibility != "public" || canonical.Index.ReplyToMessageID != "" {
		t.Fatalf("canonical accepted BOARD message/index = %#v; want exact source-bound public message", canonical)
	}
}

func TestFindAcceptedBoardPeerRequestBeforeFirstThreadBindingReturnsNotFound(t *testing.T) {
	fixture := newBoardPeerIngressFixture(t)
	ctx := context.Background()

	if result, found, err := fixture.store.FindAcceptedBoardPeerRequest(ctx, fixture.source); err != nil || found || result.TurnID != "" {
		t.Fatalf("FindAcceptedBoardPeerRequest(before first thread binding) = %#v, found %t, err %v; want clean not found", result, found, err)
	}

	wrongScope := fixture.source
	wrongScope.Scope.OwnerID = "owner-other"
	if result, found, err := fixture.store.FindAcceptedBoardPeerRequest(ctx, wrongScope); found || !errors.Is(err, ErrBoardPeerIngressScopeMismatch) {
		t.Fatalf("FindAcceptedBoardPeerRequest(invalid scope without thread binding) = %#v, found %t, err %v; want scope error", result, found, err)
	}
}

func TestBoardPeerThreadSessionLegacyMioIgnoresUnrelatedScopedPin(t *testing.T) {
	fixture := newBoardPeerIngressFixture(t)
	ctx := context.Background()
	unrelated := fixture.pin
	unrelated.OwnerID = "owner-other"
	unrelated.JournalInstanceID = "journal_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := fixture.store.PinBoardConnectionForPrincipal(ctx, unrelated); err != nil {
		t.Fatalf("PinBoardConnectionForPrincipal(unrelated same-principal receipt): %v", err)
	}

	key := boardPeerThreadSessionKey(fixture.source)
	if _, found, err := fixture.store.ReadBoardPeerThreadSession(ctx, key); err != nil || found {
		t.Fatalf("ReadBoardPeerThreadSession(legacy Mio with unrelated scoped pin) = found %t, err %v; want valid absent mapping", found, err)
	}
	want := BoardPeerThreadSessionBinding{Key: key, SessionID: fixture.sessionID}
	if got, err := fixture.store.BindBoardPeerThreadSession(ctx, want); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("BindBoardPeerThreadSession(legacy Mio with unrelated scoped pin) = %#v, %v; want legacy-pin binding", got, err)
	}
}

func TestAcceptBoardPeerRequestUsesScopedResponderAndPersistsItAcrossReplay(t *testing.T) {
	fixture := newBoardPeerIngressFixture(t)
	ctx := context.Background()
	source := fixture.source
	source.Scope.PrincipalID = "shiro"
	source.Binding.RecipientPrincipalID = "shiro"
	source.Binding.ResponderActorID = "shiro"
	source.Recipients = []string{"shiro"}
	source.Mentions = []string{"shiro"}
	fixture.source = source
	key := boardPeerThreadSessionKeyForSource(source)
	if _, _, err := fixture.store.FindAcceptedBoardPeerRequest(ctx, source); !errors.Is(err, ErrBoardPeerIngressPinMismatch) {
		t.Fatalf("FindAcceptedBoardPeerRequest(with only legacy pin) error = %v; want scoped-pin-required failure", err)
	}

	scopedPin := fixture.pin
	scopedPin.PrincipalID = "shiro"
	if _, err := fixture.store.PinBoardConnectionForPrincipal(ctx, scopedPin); err != nil {
		t.Fatalf("PinBoardConnectionForPrincipal(Shiro): %v", err)
	}
	if _, err := fixture.store.BindBoardPeerThreadSession(ctx, BoardPeerThreadSessionBinding{Key: key, SessionID: fixture.sessionID}); err != nil {
		t.Fatalf("BindBoardPeerThreadSession(Shiro scoped identity): %v", err)
	}
	input := BoardPeerRequestAcceptInput{Source: source, SessionID: fixture.sessionID, RuntimeVersion: "v2", GenerationEpoch: fixture.generationEpoch}
	accepted, err := fixture.store.AcceptBoardPeerRequest(ctx, input)
	if err != nil {
		t.Fatalf("AcceptBoardPeerRequest(Shiro responder): %v", err)
	}
	if err := assertBoardPeerAcceptedInput(accepted, input); err != nil {
		t.Fatalf("accepted Shiro peer input: %v", err)
	}
	fixture.reopen(t)
	loaded, found, err := fixture.store.FindAcceptedBoardPeerRequest(ctx, source)
	if err != nil || !found || !sameBoardPeerJSON(loaded, accepted) {
		t.Fatalf("FindAcceptedBoardPeerRequest(Shiro replay) = %#v, found %t, err %v; want exact accepted receipt", loaded, found, err)
	}
	threadBinding, found, err := fixture.store.ReadBoardPeerThreadSession(ctx, key)
	if err != nil || !found || threadBinding.Key != key || threadBinding.SessionID != fixture.sessionID {
		t.Fatalf("ReadBoardPeerThreadSession(Shiro after reopen) = %#v, found %t, err %v; want explicit scoped key", threadBinding, found, err)
	}
}

func TestAcceptBoardKuroRequestUsesOnlyKuroPrincipalPinAndReplaysExactSource(t *testing.T) {
	fixture := newBoardPeerIngressFixture(t)
	ctx := context.Background()
	source := fixture.source
	source.Scope.PrincipalID = "kuro"
	source.Binding.RecipientPrincipalID = "kuro"
	source.Binding.ResponderActorID = "kuro"
	source.Recipients = []string{"kuro"}
	source.Mentions = []string{"kuro"}
	fixture.source = source
	key := boardPeerThreadSessionKeyForSource(source)
	if _, _, err := fixture.store.FindAcceptedBoardPeerRequest(ctx, source); !errors.Is(err, ErrBoardPeerIngressPinMismatch) {
		t.Fatalf("FindAcceptedBoardPeerRequest(Kuro with only legacy pin) error = %v; want scoped-pin-required failure", err)
	}

	scopedPin := fixture.pin
	scopedPin.PrincipalID = "kuro"
	if _, err := fixture.store.PinBoardConnectionForPrincipal(ctx, scopedPin); err != nil {
		t.Fatalf("PinBoardConnectionForPrincipal(Kuro): %v", err)
	}
	if _, err := fixture.store.BindBoardPeerThreadSession(ctx, BoardPeerThreadSessionBinding{Key: key, SessionID: fixture.sessionID}); err != nil {
		t.Fatalf("BindBoardPeerThreadSession(Kuro scoped identity): %v", err)
	}
	input := BoardPeerRequestAcceptInput{Source: source, SessionID: fixture.sessionID, RuntimeVersion: "v2", GenerationEpoch: fixture.generationEpoch}
	accepted, err := fixture.store.AcceptBoardPeerRequest(ctx, input)
	if err != nil {
		t.Fatalf("AcceptBoardPeerRequest(Kuro CHAT responder): %v", err)
	}
	if err := assertBoardPeerAcceptedInput(accepted, input); err != nil {
		t.Fatalf("accepted Kuro peer input: %v", err)
	}
	fixture.reopen(t)
	loaded, found, err := fixture.store.FindAcceptedBoardPeerRequest(ctx, source)
	if err != nil || !found || !sameBoardPeerJSON(loaded, accepted) || loaded.ActorID != accepted.ActorID {
		t.Fatalf("FindAcceptedBoardPeerRequest(Kuro exact replay) = %#v, found %t, err %v; want original accepted receipt", loaded, found, err)
	}
	threadBinding, found, err := fixture.store.ReadBoardPeerThreadSession(ctx, key)
	if err != nil || !found || threadBinding.Key != key || threadBinding.SessionID != fixture.sessionID {
		t.Fatalf("ReadBoardPeerThreadSession(Kuro after reopen) = %#v, found %t, err %v; want isolated Kuro scope", threadBinding, found, err)
	}
}

func TestBindAndAcceptBoardPeerRequestCreatesFreshSessionFromRootEvent(t *testing.T) {
	ctx := context.Background()
	fixture := newBoardPeerIngressFreshFixture(t)
	assertFreshBoardSessionAbsent(t, fixture.store, fixture.sessionID)
	key := boardPeerThreadSessionKey(fixture.source)
	binding := BoardPeerThreadSessionBinding{Key: key, SessionID: fixture.sessionID, CreateNewSession: true}
	bound, err := fixture.store.BindBoardPeerThreadSession(ctx, binding)
	if err != nil || !sameBoardPeerJSON(bound, binding) {
		t.Fatalf("BindBoardPeerThreadSession(fresh resolver-minted ID without a session row) = %#v, err %v; want exact fresh binding", bound, err)
	}
	assertFreshBoardSessionAbsent(t, fixture.store, fixture.sessionID)

	resolverReplay := binding
	resolverReplay.CreateNewSession = false
	rebound, err := fixture.store.BindBoardPeerThreadSession(ctx, resolverReplay)
	if err != nil || !sameBoardPeerJSON(rebound, binding) {
		t.Fatalf("BindBoardPeerThreadSession(exact mapping after resolver reports established) = %#v, err %v; want original immutable fresh marker", rebound, err)
	}

	input := BoardPeerRequestAcceptInput{
		Source: fixture.source, SessionID: fixture.sessionID, RuntimeVersion: "v2", GenerationEpoch: fixture.generationEpoch,
	}
	accepted, err := fixture.store.AcceptBoardPeerRequest(ctx, input)
	if err != nil {
		t.Fatalf("AcceptBoardPeerRequest(first canonical BOARD root creates fresh session): %v", err)
	}
	if err := assertBoardPeerAcceptedInput(accepted, input); err != nil {
		t.Fatalf("accepted fresh-session BOARD peer input: %v", err)
	}
	assertFreshBoardSessionCount(t, fixture.store, fixture.sessionID, 1)
	var ownerID, runtimeVersion string
	var epoch int64
	if err := fixture.store.db.QueryRowContext(ctx, `SELECT owner_id,runtime_version,epoch FROM live_conversation_session WHERE session_id=?`, string(fixture.sessionID)).
		Scan(&ownerID, &runtimeVersion, &epoch); err != nil {
		t.Fatalf("read atomically created BOARD peer session: %v", err)
	}
	if ownerID != fixture.pin.OwnerID || runtimeVersion != input.RuntimeVersion || epoch != input.GenerationEpoch {
		t.Fatalf("fresh BOARD peer session owner/runtime/epoch = %q/%q/%d; want resolver scope %q/%q/%d",
			ownerID, runtimeVersion, epoch, fixture.pin.OwnerID, input.RuntimeVersion, input.GenerationEpoch)
	}

	wrongEpoch := input
	wrongEpoch.GenerationEpoch++
	if _, err := fixture.store.AcceptBoardPeerRequest(ctx, wrongEpoch); !errors.Is(err, ErrConversationSessionConflict) {
		t.Fatalf("AcceptBoardPeerRequest(existing fresh session with wrong epoch) error = %v, want session conflict", err)
	}
	wrongOwner := input
	wrongOwner.Source.Scope.OwnerID = "owner-other"
	if _, err := fixture.store.AcceptBoardPeerRequest(ctx, wrongOwner); !errors.Is(err, ErrBoardPeerIngressScopeMismatch) {
		t.Fatalf("AcceptBoardPeerRequest(fresh binding with wrong owner) error = %v, want scope mismatch", err)
	}
	changedSession := binding
	changedSession.SessionID = fixture.otherSessionID
	if _, err := fixture.store.BindBoardPeerThreadSession(ctx, changedSession); !errors.Is(err, ErrBoardPeerIngressConflict) {
		t.Fatalf("BindBoardPeerThreadSession(rebind exact BOARD thread to another session) error = %v, want immutable mapping conflict", err)
	}
	otherSource := fixture.source
	otherSource.Binding.ThreadID = "thread-peer-second"
	otherSource.Binding.MessageID = "board-root-peer-second"
	otherSource.CausationID = otherSource.Binding.MessageID
	otherSource.InboxItemID = "inbox-peer-second"
	otherSource.InboxCreatedAt = otherSource.InboxCreatedAt.Add(time.Minute)
	otherSource.ThreadCreatedAt = otherSource.ThreadCreatedAt.Add(time.Minute)
	if _, err := fixture.store.BindBoardPeerThreadSession(ctx, BoardPeerThreadSessionBinding{
		Key: boardPeerThreadSessionKey(otherSource), SessionID: fixture.sessionID, CreateNewSession: true,
	}); !errors.Is(err, ErrBoardPeerIngressScopeMismatch) {
		t.Fatalf("BindBoardPeerThreadSession(new BOARD thread reusing established session as fresh) error = %v, want scope mismatch", err)
	}
	assertFreshBoardSessionCount(t, fixture.store, fixture.sessionID, 1)

	fixture.reopen(t)
	rebound, err = fixture.store.BindBoardPeerThreadSession(ctx, resolverReplay)
	if err != nil || !sameBoardPeerJSON(rebound, binding) {
		t.Fatalf("BindBoardPeerThreadSession(after restart with resolver freshness false) = %#v, err %v; want original immutable binding", rebound, err)
	}
	replayed, err := fixture.store.AcceptBoardPeerRequest(ctx, input)
	if err != nil || !sameBoardPeerJSON(replayed, accepted) {
		t.Fatalf("AcceptBoardPeerRequest(exact fresh-session replay after restart) = %#v, err %v; want original accepted event", replayed, err)
	}
	assertFreshBoardSessionCount(t, fixture.store, fixture.sessionID, 1)
}

func assertBoardPeerAcceptedInput(accepted AcceptedTurn, input BoardPeerRequestAcceptInput) error {
	source := input.Source
	event := accepted.InputEvent
	sourceActorID := "board:" + source.Binding.BoardInstanceID + ":" + source.Binding.SenderPrincipalID
	responderActorID := source.Binding.EffectiveResponderActorID()
	if !strings.HasPrefix(accepted.OperationID, boardPeerRequestAcceptanceOperationPrefix) ||
		!strings.HasPrefix(accepted.ClientSubmissionID, boardPeerRequestStableKeyPrefix) ||
		strings.TrimPrefix(accepted.OperationID, boardPeerRequestAcceptanceOperationPrefix) !=
			strings.TrimPrefix(accepted.ClientSubmissionID, boardPeerRequestStableKeyPrefix) || accepted.SessionID != input.SessionID ||
		accepted.TurnID == "" || accepted.ActorID != sourceActorID || accepted.TriggerKind != "external_peer_message" ||
		accepted.InputMessageID != event.MessageID || accepted.GenerationEpoch != input.GenerationEpoch ||
		accepted.State != "accepted" || accepted.AdmissionSeq <= 0 || event.EventSeq != accepted.AdmissionSeq ||
		event.EventType != "conversation.external_peer_message" || event.ComponentID != "orchestrator" ||
		event.ActorKind != "" || event.ActorID != "" || event.CausationEventID != "" || len(event.DependencyEventIDs) != 0 ||
		event.SessionID != input.SessionID || event.TurnID != accepted.TurnID || event.MessageID == "" ||
		!event.OccurredAt.Equal(source.MessageAt) {
		return errors.New("accepted identity or canonical external peer envelope differs from the source contract")
	}
	if source.Binding.WireKind != "" || source.Binding.Classification != domainconversation.ExternalPeerClassificationRequest ||
		source.Binding.ReplyTo != "" || source.CausationID != source.Binding.MessageID || source.HopCount != 0 {
		return errors.New("fixture lost raw BOARD root kind, classification, or root cause separation")
	}
	if len(event.Payload) != 9 || event.Payload["from"] != "external_peer" || event.Payload["source_actor_id"] != sourceActorID ||
		event.Payload["to"] != responderActorID || event.Payload["visibility"] != "public" || event.Payload["text"] != source.Body ||
		event.Payload["content"] != source.Body {
		return errors.New("canonical external peer payload does not preserve the exact A input shape")
	}
	if replyTo, exists := event.Payload["reply_to_message_id"]; exists && replyTo != "" {
		return errors.New("uncorrelated BOARD root request cannot carry a reply target")
	}
	if !reflect.DeepEqual(event.Payload["external_peer_binding"], source.Binding.ToPayload()) ||
		!reflect.DeepEqual(event.Payload["body"], map[string]any{"text": source.Body}) ||
		!sameBoardPeerJSON(event.Payload["board_source"], boardPeerSourcePayload(source)) {
		return errors.New("canonical event did not retain the exact binding and separate authenticated BOARD source metadata")
	}
	encoded, err := json.Marshal(event.Payload)
	if err != nil {
		return err
	}
	lower := bytes.ToLower(encoded)
	for _, forbidden := range [][]byte{[]byte("authorization"), []byte("ack_receipt"), []byte("claim_receipt"), []byte("bearer ")} {
		if bytes.Contains(lower, forbidden) {
			return errors.New("canonical BOARD peer input retained private transport or claim material")
		}
	}
	return nil
}

func newBoardPeerIngressFixture(t *testing.T) *boardPeerIngressFixture {
	t.Helper()
	databasePath := filepath.Join(t.TempDir(), "board-peer-ingress.sqlite")
	store, err := NewSQLiteStore(databasePath)
	if err != nil {
		t.Fatalf("NewSQLiteStore(synthetic peer ingress): %v", err)
	}
	fixture := &boardPeerIngressFixture{store: store, databasePath: databasePath}
	t.Cleanup(func() {
		if fixture.store != nil {
			if err := fixture.store.Close(); err != nil {
				t.Errorf("Close(): %v", err)
			}
		}
	})
	fixture.pin = BoardConnectionPin{
		OwnerID: "owner-peer", JournalInstanceID: "journal_0123456789abcdef0123456789abcdef",
		PrincipalID: "rencrow-comm", CutoverAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
	}
	if _, err := store.PinBoardConnection(context.Background(), fixture.pin); err != nil {
		t.Fatalf("PinBoardConnection(synthetic authenticated principal): %v", err)
	}
	fixture.scope = BoardPublicationScope{
		OwnerID: fixture.pin.OwnerID, JournalInstanceID: fixture.pin.JournalInstanceID, PrincipalID: fixture.pin.PrincipalID,
	}
	fixture.sessionID, fixture.generationEpoch = seedBoardPeerIngressSession(t, store, "primary")
	fixture.otherSessionID, fixture.otherGenerationEpoch = seedBoardPeerIngressSession(t, store, "alternate")
	fixture.source = BoardPeerRequestSource{
		Scope: fixture.scope,
		Binding: domainconversation.ExternalPeerBinding{
			BoardInstanceID: fixture.pin.JournalInstanceID, RecipientPrincipalID: fixture.pin.PrincipalID,
			ChannelID: "channel-peer-alpha", ThreadID: "thread-peer-alpha", MessageID: "board-root-peer-alpha",
			SenderPrincipalID: "grok", WireKind: "", Classification: domainconversation.ExternalPeerClassificationRequest,
		},
		InboxItemID: "inbox-peer-alpha", InboxCreatedAt: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC),
		MessageAt:       time.Date(2026, 10, 31, 23, 59, 0, 0, time.UTC),
		ThreadCreatedAt: time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC),
		Body:            "A synthetic BOARD root request.", Recipients: []string{fixture.pin.PrincipalID},
		Mentions: []string{fixture.pin.PrincipalID}, CausationID: "board-root-peer-alpha", HopCount: 0,
	}
	return fixture
}

func newBoardPeerIngressFreshFixture(t *testing.T) *boardPeerIngressFixture {
	t.Helper()
	databasePath := filepath.Join(t.TempDir(), "board-peer-fresh-session.sqlite")
	store, err := NewSQLiteStore(databasePath)
	if err != nil {
		t.Fatalf("NewSQLiteStore(fresh BOARD peer session): %v", err)
	}
	fixture := &boardPeerIngressFixture{store: store, databasePath: databasePath}
	t.Cleanup(func() {
		if fixture.store != nil {
			if err := fixture.store.Close(); err != nil {
				t.Errorf("Close(fresh BOARD peer session): %v", err)
			}
		}
	})
	fixture.pin = BoardConnectionPin{
		OwnerID: "owner-peer", JournalInstanceID: "journal_0123456789abcdef0123456789abcdef",
		PrincipalID: "rencrow-comm", CutoverAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
	}
	if _, err := store.PinBoardConnection(context.Background(), fixture.pin); err != nil {
		t.Fatalf("PinBoardConnection(fresh-session synthetic principal): %v", err)
	}
	fixture.scope = BoardPublicationScope{
		OwnerID: fixture.pin.OwnerID, JournalInstanceID: fixture.pin.JournalInstanceID, PrincipalID: fixture.pin.PrincipalID,
	}
	fixture.sessionID, fixture.generationEpoch = modulecore.NewSessionID(), 1
	fixture.otherSessionID, fixture.otherGenerationEpoch = modulecore.NewSessionID(), 2
	fixture.source = BoardPeerRequestSource{
		Scope: fixture.scope,
		Binding: domainconversation.ExternalPeerBinding{
			BoardInstanceID: fixture.pin.JournalInstanceID, RecipientPrincipalID: fixture.pin.PrincipalID,
			ChannelID: "channel-peer-alpha", ThreadID: "thread-peer-alpha", MessageID: "board-root-peer-alpha",
			SenderPrincipalID: "grok", WireKind: "", Classification: domainconversation.ExternalPeerClassificationRequest,
		},
		InboxItemID: "inbox-peer-alpha", InboxCreatedAt: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC),
		MessageAt:       time.Date(2026, 10, 31, 23, 59, 0, 0, time.UTC),
		ThreadCreatedAt: time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC),
		Body:            "A synthetic BOARD root request.", Recipients: []string{fixture.pin.PrincipalID},
		Mentions: []string{fixture.pin.PrincipalID}, CausationID: "board-root-peer-alpha", HopCount: 0,
	}
	return fixture
}

type boardPeerIngressFixture struct {
	store                *SQLiteStore
	pin                  BoardConnectionPin
	scope                BoardPublicationScope
	source               BoardPeerRequestSource
	sessionID            modulecore.SessionID
	generationEpoch      int64
	otherSessionID       modulecore.SessionID
	otherGenerationEpoch int64
	databasePath         string
}

func seedBoardPeerIngressSession(t *testing.T, store *SQLiteStore, suffix string) (modulecore.SessionID, int64) {
	t.Helper()
	ctx := context.Background()
	sessionID, turnID := modulecore.NewSessionID(), modulecore.NewTurnID()
	event := liveInputEvent(sessionID, turnID, "synthetic existing CORE session")
	event.OccurredAt = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	accepted, err := store.AcceptTurn(ctx, AcceptInput{
		OperationID: "board-peer-session-seed:" + suffix, OwnerID: "owner-peer", RuntimeVersion: "v2",
		ClientSubmissionID: "board-peer-session-seed-submission:" + suffix, ActorID: "user-1", TriggerKind: "user_message",
		GenerationEpoch: 1, CreateNewSession: true, InputEvent: event,
		InputMessage: MessageIndex{
			MessageID: event.MessageID, EventID: event.EventID, SessionID: sessionID, TurnID: turnID,
			FromActor: "user-1", ToKind: "agent", ToActor: "mio", Kind: "user_message", Visibility: "public",
		},
	})
	if err != nil {
		t.Fatalf("AcceptTurn(seed existing owner session %s): %v", suffix, err)
	}
	return accepted.SessionID, accepted.GenerationEpoch
}

func (fixture *boardPeerIngressFixture) reopen(t *testing.T) {
	t.Helper()
	if err := fixture.store.Close(); err != nil {
		t.Fatalf("Close(before peer ingress recovery): %v", err)
	}
	fixture.store = nil
	store, err := NewSQLiteStore(fixture.databasePath)
	if err != nil {
		t.Fatalf("NewSQLiteStore(reopen peer ingress): %v", err)
	}
	fixture.store = store
}

func boardPeerThreadSessionKey(source BoardPeerRequestSource) BoardPeerThreadSessionKey {
	return BoardPeerThreadSessionKey{
		Scope: source.Scope, ChannelID: source.Binding.ChannelID, ThreadID: source.Binding.ThreadID,
		ThreadCreatedAt: source.ThreadCreatedAt,
	}
}

func boardPeerSourcePayload(source BoardPeerRequestSource) map[string]any {
	return map[string]any{
		"inbox_item_id":     source.InboxItemID,
		"inbox_created_at":  source.InboxCreatedAt.UTC().Format(time.RFC3339Nano),
		"message_at":        source.MessageAt.UTC().Format(time.RFC3339Nano),
		"thread_created_at": source.ThreadCreatedAt.UTC().Format(time.RFC3339Nano),
		"recipients":        append([]string(nil), source.Recipients...), "mentions": append([]string(nil), source.Mentions...),
		"causation_id": source.CausationID, "hop_count": source.HopCount,
	}
}

func sameBoardPeerJSON(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}
