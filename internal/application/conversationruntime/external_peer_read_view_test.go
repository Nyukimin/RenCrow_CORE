package conversationruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestBuildReadViewReturnsOnlyTheBoundExternalPeerSnapshot(t *testing.T) {
	request, tail, wantTexts := externalPeerReadViewFixture(t)
	view, err := NewReadViewService(&fakeTailReader{tail: tail}).BuildReadView(context.Background(), request)
	if err != nil {
		t.Fatalf("BuildReadView(bound external peer): %v", err)
	}
	gotTexts := make([]string, len(view.Messages))
	for index, message := range view.Messages {
		gotTexts[index] = message.Text
	}
	if len(gotTexts) != len(wantTexts) {
		t.Fatalf("external prompt message count = %d, want %d; messages=%#v", len(gotTexts), len(wantTexts), view.Messages)
	}
	for index, want := range wantTexts {
		if gotTexts[index] != want {
			t.Fatalf("external prompt message[%d] = %q, want %q; all=%#v", index, gotTexts[index], want, gotTexts)
		}
		if strings.Contains(gotTexts[index], "PRIVATE_") || strings.Contains(gotTexts[index], "FOREIGN_") {
			t.Fatalf("external prompt contains an out-of-scope marker: %#v", view.Messages)
		}
	}
}

func TestBuildReadViewScopesExternalHistoryAndRepliesToResponderActor(t *testing.T) {
	request, tail, wantTexts := externalPeerReadViewFixture(t)
	request = externalPeerRequestWithResponderActor(t, request, "shiro")
	priorInput := tail.Messages[1].Event
	setExternalPeerResponderActor(t, &priorInput, "shiro")
	tail.Messages[1].Event = priorInput
	tail.Messages[1].Index.ToActor = "shiro"
	tail.Messages[2] = externalPeerReplyEvent(priorInput, "peer-a", 7, "Earlier Shiro answer.")
	wantTexts[1] = "Earlier Shiro answer."
	acceptedIndex := len(tail.Messages) - 1
	tail.Messages[acceptedIndex].Event = request.Accepted.InputEvent
	tail.Messages[acceptedIndex].Index.ToActor = "shiro"

	view, err := NewReadViewService(&fakeTailReader{tail: tail}).BuildReadView(context.Background(), request)
	if err != nil {
		t.Fatalf("BuildReadView(Shiro external peer): %v", err)
	}
	if len(view.Messages) != len(wantTexts) {
		t.Fatalf("Shiro prompt message count = %d, want %d; messages=%#v", len(view.Messages), len(wantTexts), view.Messages)
	}
	for index, want := range wantTexts {
		if view.Messages[index].Text != want {
			t.Fatalf("Shiro prompt message[%d] = %q, want %q; all=%#v", index, view.Messages[index].Text, want, view.Messages)
		}
	}
}

func externalPeerReadViewFixture(t *testing.T) (ReadViewRequest, CanonicalTail, []string) {
	t.Helper()
	request, _ := runtimeInput(t, modulecore.NewSessionID(), 9, "unused user input")
	const boardInstance = "test-instance"
	const recipient = "rencrow-comm"
	const peer = "peer-a"
	const channel = "channel-a"
	const thread = "thread-a"
	const sourceActor = "board:test-instance:peer-a"
	acceptedBody := "  Please explain the current result.\n"
	request.Principal.ActorID = sourceActor
	request.Accepted.ActorID = sourceActor
	request.Accepted.TriggerKind = "external_peer_message"
	request.Accepted.ClientSubmissionID = "external-submission-1"
	request.Accepted.InputEvent.EventType = "conversation.external_peer_message"
	request.Accepted.InputEvent.ComponentID = "orchestrator"
	request.Accepted.InputEvent.ActorKind = ""
	request.Accepted.InputEvent.ActorID = ""
	request.Accepted.InputEvent.Payload = externalPeerPayload(boardInstance, recipient, peer, channel, thread, "request-2", "", "", acceptedBody)

	priorTurn := modulecore.NewTurnID()
	sameContext := externalPeerMessageEvent(request.Session.SessionID, priorTurn, 4, sourceActor, boardInstance, recipient, peer, channel, thread, "request-1", "", "", "Earlier question in this thread.")
	priorReply := externalPeerReplyEvent(sameContext, peer, 7, "Earlier Mio answer.")
	otherThread := externalPeerMessageEvent(request.Session.SessionID, modulecore.NewTurnID(), 5, sourceActor, boardInstance, recipient, peer, channel, "thread-b", "foreign-thread", "", "", "FOREIGN_THREAD_CONTEXT")
	otherPeer := externalPeerMessageEvent(request.Session.SessionID, modulecore.NewTurnID(), 6, "board:test-instance:peer-b", boardInstance, recipient, "peer-b", channel, thread, "foreign-peer", "", "", "FOREIGN_PEER_CONTEXT")
	ownerPrivate := userEvent(request.Session.SessionID, modulecore.NewTurnID(), 3, request.Session.OwnerID, "PRIVATE_OWNER_CONTEXT")
	ownerPrivate.Payload["visibility"] = "private"
	ownerPrivateMessage := canonicalMessage(ownerPrivate, request.Session.OwnerID, "agent", "mio", "user_message")
	ownerPrivateMessage.Index.Visibility = "private"
	legacyPrivate := legacyMessage(request.Session.SessionID, modulecore.NewTurnID(), "legacy-private-owner-message", "PRIVATE_LEGACY_CONTEXT", request.Session.OwnerID, "agent", "mio", "user_message", 4, 1)
	legacyPrivate.Index.Visibility = "private"
	request.Legacy = LegacyProjection{
		AppliedThrough: 0,
		Complete:       true,
		Messages:       []LegacyMessage{legacyPrivate},
	}
	request.Session.ProjectionWatermark = 0
	request.Session.AdmissionSeq = request.Accepted.AdmissionSeq
	request.Accepted.State = "accepted"
	tail := CanonicalTail{
		ThroughSeq: request.Accepted.AdmissionSeq,
		Complete:   true,
		Messages: []CanonicalMessage{
			ownerPrivateMessage,
			canonicalMessage(sameContext, sourceActor, "agent", "mio", "external_peer_message"),
			priorReply,
			canonicalMessage(otherThread, sourceActor, "agent", "mio", "external_peer_message"),
			canonicalMessage(otherPeer, "board:test-instance:peer-b", "agent", "mio", "external_peer_message"),
			canonicalMessage(request.Accepted.InputEvent, sourceActor, "agent", "mio", "external_peer_message"),
		},
	}
	return request, tail, []string{"Earlier question in this thread.", "Earlier Mio answer.", acceptedBody}
}

func externalPeerReplyEvent(input modulecore.EventEnvelope, peer string, sequence modulecore.EventSeq, body string) CanonicalMessage {
	binding, err := domainconversation.ParseExternalPeerBinding(input.Payload)
	if err != nil {
		panic(err)
	}
	responderActorID := binding.EffectiveResponderActorID()
	event := modulecore.NewEventEnvelope(input.TraceID, input.EventID, nil, "conversation_runtime", "agent.response", time.Unix(int64(sequence), 0).UTC(), map[string]any{
		"kind": "reply", "from": responderActorID, "source_actor_id": responderActorID, "to": "external_peer", "visibility": "public",
		"text": body, "content": body, "reply_to_message_id": string(input.MessageID),
		"external_peer_binding": binding.ToPayload(),
	})
	event.EventSeq, event.SessionID, event.TurnID, event.MessageID = sequence, input.SessionID, input.TurnID, modulecore.NewMessageID()
	event.ActorKind, event.ActorID = "agent", responderActorID
	message := canonicalMessage(event, responderActorID, "external_peer", peer, "assistant_message")
	message.Index.ReplyToMessageID = input.MessageID
	return message
}

func externalPeerMessageEvent(sessionID modulecore.SessionID, turnID modulecore.TurnID, sequence modulecore.EventSeq, sourceActor, boardInstance, recipient, peer, channel, thread, messageID, wireKind, replyTo, body string) modulecore.EventEnvelope {
	event := userEvent(sessionID, turnID, sequence, sourceActor, body)
	event.EventType = "conversation.external_peer_message"
	event.ComponentID = "orchestrator"
	event.Payload = externalPeerPayload(boardInstance, recipient, peer, channel, thread, messageID, wireKind, replyTo, body)
	return event
}

func externalPeerPayload(boardInstance, recipient, peer, channel, thread, messageID, wireKind, replyTo, body string) map[string]any {
	sourceActor := "board:" + boardInstance + ":" + peer
	rawBody := map[string]any{"text": body, "from": "external_peer", "actor_id": peer, "owner_id": "FOREIGN_UNTRUSTED_OWNER"}
	binding := domainconversation.ExternalPeerBinding{
		BoardInstanceID: boardInstance, RecipientPrincipalID: recipient,
		ChannelID: channel, ThreadID: thread, MessageID: messageID,
		SenderPrincipalID: peer, WireKind: wireKind,
		Classification: domainconversation.ExternalPeerClassificationRequest, ReplyTo: replyTo,
	}
	return map[string]any{
		"from": "external_peer", "source_actor_id": sourceActor, "to": binding.EffectiveResponderActorID(), "visibility": "public",
		"text": body, "content": body, "body": rawBody,
		"external_peer_binding": binding.ToPayload(),
	}
}

func externalPeerRequestWithResponderActor(t *testing.T, request ReadViewRequest, responderActorID string) ReadViewRequest {
	t.Helper()
	setExternalPeerResponderActor(t, &request.Accepted.InputEvent, responderActorID)
	return request
}

func setExternalPeerResponderActor(t *testing.T, event *modulecore.EventEnvelope, responderActorID string) {
	t.Helper()
	payload := make(map[string]any, len(event.Payload))
	for key, value := range event.Payload {
		payload[key] = value
	}
	event.Payload = payload
	binding, err := domainconversation.ParseExternalPeerBinding(event.Payload)
	if err != nil {
		t.Fatalf("ParseExternalPeerBinding(before responder update): %v", err)
	}
	binding.RecipientPrincipalID = responderActorID
	binding.ResponderActorID = responderActorID
	event.Payload["to"] = responderActorID
	event.Payload["external_peer_binding"] = binding.ToPayload()
}

func TestRuntimeExternalPeerRequiresTypedAtomicJournal(t *testing.T) {
	request, _, _ := externalPeerReadViewFixture(t)
	acceptedText := request.Accepted.InputEvent.Payload["text"].(string)
	view := ReadView{
		SessionID: request.Session.SessionID,
		Messages: []PromptMessage{{
			Role: "user", Text: acceptedText, MessageID: string(request.Accepted.InputMessageID),
			TurnID: request.Accepted.TurnID, EventSeq: request.Accepted.AdmissionSeq,
		}},
	}
	lookup := externalLookupFixture(t, request)
	provider := &fakeConversationProvider{response: `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"external_peer","text":"The requested answer is available."}}`}
	legacyJournal, publisher := &fakeJournal{}, &fakePublisher{}

	t.Run("missing typed journal fails closed before generation", func(t *testing.T) {
		runtime := newExternalRuntimeForTest(t, view, provider, legacyJournal, publisher, nil)
		result := runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: request})
		if result.Status != TurnStatusFailedBeforeCommit || result.Committed || result.Cause == nil {
			t.Fatalf("external turn without typed journal = %#v, want failed-before-commit", result)
		}
		if !errors.Is(result.Cause, ErrExternalTurnJournalRequired) {
			t.Fatalf("external turn without typed journal error = %v, want ErrExternalTurnJournalRequired", result.Cause)
		}
		if provider.callCount() != 0 || legacyJournal.loadCount() != 0 || legacyJournal.commitCount() != 0 || publisher.publishCount() != 0 {
			t.Fatalf("missing typed journal reached generation or legacy path: generation=%d loads=%d commits=%d publishes=%d",
				provider.callCount(), legacyJournal.loadCount(), legacyJournal.commitCount(), publisher.publishCount())
		}
	})

	t.Run("typed commit captures Mio response and publication intent", func(t *testing.T) {
		provider := &fakeConversationProvider{response: `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"external_peer","text":"The requested answer is available."}}`}
		journal := &fakeExternalTurnJournal{}
		legacyJournal, publisher := &fakeJournal{}, &fakePublisher{}
		runtime := newExternalRuntimeForTest(t, view, provider, legacyJournal, publisher, journal)
		result := runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: request})
		if result.Status != TurnStatusPublicationPending || !result.Committed {
			t.Fatalf("external turn result = %#v, want committed publication_pending", result)
		}
		if journal.loadCalls != 1 || journal.commitCalls != 1 || journal.loadedLookup != lookup {
			t.Fatalf("typed journal calls/load scope = %d/%d %#v, want 1/1 %#v", journal.loadCalls, journal.commitCalls, journal.loadedLookup, lookup)
		}
		if provider.callCount() != 1 || provider.lastScope().ActorID != request.Accepted.ActorID || provider.lastScope().Purpose != domainconversation.PurposeExternalPeer {
			t.Fatalf("Mio generation calls/scope = %d %#v, want one external-peer generation", provider.callCount(), provider.lastScope())
		}
		if journal.commit.ExpectedLookup != lookup || journal.commit.ResultState != "completed" {
			t.Fatalf("typed commit lookup/state = %#v/%q, want %#v/completed", journal.commit.ExpectedLookup, journal.commit.ResultState, lookup)
		}
		if len(journal.commit.CanonicalEvents) != 2 || len(journal.commit.MessageIndexes) != 1 {
			t.Fatalf("typed commit canonical rows = %d events, %d indexes, want response, invocation receipt, and one response index", len(journal.commit.CanonicalEvents), len(journal.commit.MessageIndexes))
		}
		event, index := journal.commit.CanonicalEvents[0], journal.commit.MessageIndexes[0]
		if event.EventType != "agent.response" || event.ComponentID != "conversation_runtime" || event.ActorKind != "agent" || event.ActorID != "mio" ||
			payloadString(event.Payload, "from") != "mio" || payloadString(event.Payload, "to") != "external_peer" ||
			payloadString(event.Payload, "text") != "The requested answer is available." ||
			payloadString(event.Payload, "reply_to_message_id") != string(request.Accepted.InputMessageID) {
			t.Fatalf("external canonical response event = %#v, want public Mio response to the bound peer", event)
		}
		receipt := journal.commit.CanonicalEvents[1]
		invocation := journal.commit.InvocationReceipt
		if receipt.EventType != "conversation.mio_emission_receipt" || receipt.ComponentID != "conversation_runtime" ||
			receipt.ActorKind != "" || receipt.ActorID != "" || receipt.TraceID != request.Accepted.InputEvent.TraceID ||
			receipt.CausationEventID != request.Accepted.InputEvent.EventID ||
			payloadString(receipt.Payload, "status") != invocation.Status ||
			payloadString(receipt.Payload, "request_id") != invocation.RequestID ||
			payloadString(receipt.Payload, "response_id") != invocation.ResponseID ||
			invocation.Status != "completed" || invocation.RequestID == "" || invocation.ResponseID == "" {
			t.Fatalf("external invocation receipt event=%#v actual invocation=%#v, want exact accepted trace/cause and provider receipt", receipt, invocation)
		}
		if index.FromActor != "mio" || index.ToKind != "external_peer" || index.ToActor != lookup.SourceBinding.SenderPrincipalID ||
			index.ReplyToMessageID != request.Accepted.InputMessageID {
			t.Fatalf("external response index = %#v, want canonical Mio reply to accepted input", index)
		}
		intent := journal.commit.PublicationIntent
		if intent.CanonicalResponseMessageID != index.MessageID || intent.StableLogicalKey == "" ||
			intent.Target.BoardInstanceID != lookup.SourceBinding.BoardInstanceID ||
			intent.Target.CorePrincipalID != lookup.SourceBinding.RecipientPrincipalID ||
			intent.Target.PeerPrincipalID != lookup.SourceBinding.SenderPrincipalID ||
			intent.Target.ChannelID != lookup.SourceBinding.ChannelID ||
			intent.Target.ThreadID != lookup.SourceBinding.ThreadID ||
			intent.Target.ReplyTo != lookup.SourceBinding.MessageID {
			t.Fatalf("external publication intent = %#v, want target bound to accepted source", intent)
		}
		if legacyJournal.loadCount() != 0 || legacyJournal.commitCount() != 0 || publisher.publishCount() != 0 {
			t.Fatalf("external turn used legacy journal or Viewer publisher: loads=%d commits=%d publishes=%d",
				legacyJournal.loadCount(), legacyJournal.commitCount(), publisher.publishCount())
		}
		result = runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: request})
		if result.Status != TurnStatusPublicationPending || provider.callCount() != 1 || journal.commitCalls != 1 || journal.loadCalls != 2 {
			t.Fatalf("committed external replay result=%#v generation=%d typed loads/commits=%d/%d; want stored outcome reuse without regeneration",
				result, provider.callCount(), journal.loadCalls, journal.commitCalls)
		}
	})

	t.Run("typed lookup error fails closed before generation", func(t *testing.T) {
		provider := &fakeConversationProvider{response: `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"external_peer","text":"Must not be generated."}}`}
		journal := &fakeExternalTurnJournal{loadErr: errors.New("synthetic outcome lookup unavailable")}
		legacyJournal, publisher := &fakeJournal{}, &fakePublisher{}
		runtime := newExternalRuntimeForTest(t, view, provider, legacyJournal, publisher, journal)
		result := runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: request})
		if result.Status != TurnStatusCommitUncertain || provider.callCount() != 0 || journal.commitCalls != 0 ||
			legacyJournal.loadCount() != 0 || legacyJournal.commitCount() != 0 || publisher.publishCount() != 0 {
			t.Fatalf("typed lookup error reached generation or legacy path: result=%#v generation=%d typed commits=%d legacy=%d/%d publishes=%d",
				result, provider.callCount(), journal.commitCalls, legacyJournal.loadCount(), legacyJournal.commitCount(), publisher.publishCount())
		}
	})

	t.Run("commit unknown recovers only through the exact typed lookup", func(t *testing.T) {
		provider := &fakeConversationProvider{response: `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"external_peer","text":"The requested answer is available."}}`}
		journal := &fakeExternalTurnJournal{commitErr: errors.New("synthetic unknown commit result"), persistOnError: true}
		runtime := newExternalRuntimeForTest(t, view, provider, &fakeJournal{}, &fakePublisher{}, journal)
		result := runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: request})
		if result.Status != TurnStatusPublicationPending || !result.Committed || journal.loadCalls != 2 || journal.commitCalls != 1 || provider.callCount() != 1 || journal.loadedLookup != lookup {
			t.Fatalf("unknown commit recovery=%#v typed loads/commits=%d/%d generation=%d lookup=%#v",
				result, journal.loadCalls, journal.commitCalls, provider.callCount(), journal.loadedLookup)
		}
	})

	t.Run("safe error keeps the bound peer destination without claiming generation success", func(t *testing.T) {
		provider := &fakeConversationProvider{err: errors.New("synthetic private provider failure")}
		journal := &fakeExternalTurnJournal{}
		runtime := newExternalRuntimeForTest(t, view, provider, &fakeJournal{}, &fakePublisher{}, journal)
		result := runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: request})
		if result.Status != TurnStatusPublicationPending || !result.Committed || !result.SafeError || !journal.commit.SafeError ||
			journal.commit.FailureStage != ExternalFailureStageGeneration || provider.callCount() != 1 {
			t.Fatalf("external safe-error result=%#v commit=%#v generation=%d", result, journal.commit, provider.callCount())
		}
		if len(journal.commit.CanonicalEvents) != 2 || journal.commit.InvocationReceipt.Status != "generation_failed" {
			t.Fatalf("safe-error events/receipt = %d/%#v, want public error plus actual failed invocation receipt", len(journal.commit.CanonicalEvents), journal.commit.InvocationReceipt)
		}
		event, index := journal.commit.CanonicalEvents[0], journal.commit.MessageIndexes[0]
		if event.EventType != "agent.response" || event.ComponentID != "conversation_runtime" || event.ActorKind != "" || event.ActorID != "" ||
			payloadString(event.Payload, "kind") != "turn_error" || payloadString(event.Payload, "from") != "mio" ||
			payloadString(event.Payload, "to") != "external_peer" || payloadString(event.Payload, "reply_to_message_id") != string(request.Accepted.InputMessageID) ||
			strings.Contains(payloadString(event.Payload, "text"), "synthetic private provider failure") ||
			index.FromActor != "mio" || index.ToKind != "external_peer" || index.ToActor != lookup.SourceBinding.SenderPrincipalID ||
			index.Kind != "turn_error" || index.ReplyToMessageID != request.Accepted.InputMessageID {
			t.Fatalf("external safe-error response/index = %#v/%#v", event, index)
		}
	})
}

func TestRuntimeExternalPeerRejectsCommittedOutcomeOutsideExactSourceAndOwnerScope(t *testing.T) {
	request, _, _ := externalPeerReadViewFixture(t)
	view := ReadView{SessionID: request.Session.SessionID, Messages: []PromptMessage{{
		Role: "user", Text: payloadString(request.Accepted.InputEvent.Payload, "text"),
		MessageID: string(request.Accepted.InputMessageID), TurnID: request.Accepted.TurnID, EventSeq: request.Accepted.AdmissionSeq,
	}}}
	baseProvider := &fakeConversationProvider{response: `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"external_peer","text":"A bounded answer."}}`}
	baseJournal := &fakeExternalTurnJournal{}
	baseRuntime := newExternalRuntimeForTest(t, view, baseProvider, &fakeJournal{}, &fakePublisher{}, baseJournal)
	if result := baseRuntime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: request}); result.Status != TurnStatusPublicationPending || baseJournal.outcome == nil {
		t.Fatalf("create valid committed outcome = %#v", result)
	}

	tests := []struct {
		name   string
		mutate func(*ExternalCommittedBundle, *ReadViewRequest)
	}{
		{name: "owner", mutate: func(bundle *ExternalCommittedBundle, _ *ReadViewRequest) { bundle.Lookup.OwnerID = "different-owner" }},
		{name: "session", mutate: func(bundle *ExternalCommittedBundle, _ *ReadViewRequest) {
			bundle.Lookup.SessionID = modulecore.NewSessionID()
		}},
		{name: "source binding", mutate: func(bundle *ExternalCommittedBundle, _ *ReadViewRequest) {
			bundle.Lookup.SourceBinding.MessageID = "different-board-message"
		}},
		{name: "source responder actor", mutate: func(bundle *ExternalCommittedBundle, _ *ReadViewRequest) {
			bundle.Lookup.SourceBinding.ResponderActorID = "shiro"
		}},
		{name: "source fingerprint", mutate: func(bundle *ExternalCommittedBundle, _ *ReadViewRequest) {
			bundle.Lookup.SourceFingerprint = strings.Repeat("0", 64)
		}},
		{name: "accepted body hash", mutate: func(_ *ExternalCommittedBundle, request *ReadViewRequest) {
			payload := make(map[string]any, len(request.Accepted.InputEvent.Payload))
			for key, value := range request.Accepted.InputEvent.Payload {
				payload[key] = value
			}
			payload["text"], payload["content"] = "changed body", "changed body"
			body := make(map[string]any)
			for key, value := range request.Accepted.InputEvent.Payload["body"].(map[string]any) {
				body[key] = value
			}
			body["text"] = "changed body"
			payload["body"] = body
			request.Accepted.InputEvent.Payload = payload
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			modifiedRequest := request
			outcome := cloneExternalCommittedBundle(*baseJournal.outcome)
			test.mutate(&outcome, &modifiedRequest)
			journal := &fakeExternalTurnJournal{outcome: &outcome}
			provider := &fakeConversationProvider{response: `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"external_peer","text":"Must not regenerate."}}`}
			runtime := newExternalRuntimeForTest(t, view, provider, &fakeJournal{}, &fakePublisher{}, journal)
			result := runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: modifiedRequest})
			if result.Status != TurnStatusCommitUncertain || result.Committed || provider.callCount() != 0 || journal.commitCalls != 0 {
				t.Fatalf("mismatched committed source reused or regenerated: result=%#v generation=%d commits=%d", result, provider.callCount(), journal.commitCalls)
			}
		})
	}
}

func TestRuntimeExternalPeerRejectsCommittedOutcomeWithMismatchedTraceOrReceiptCausation(t *testing.T) {
	request, _, _ := externalPeerReadViewFixture(t)
	view := ReadView{SessionID: request.Session.SessionID, Messages: []PromptMessage{{
		Role: "user", Text: payloadString(request.Accepted.InputEvent.Payload, "text"),
		MessageID: string(request.Accepted.InputMessageID), TurnID: request.Accepted.TurnID, EventSeq: request.Accepted.AdmissionSeq,
	}}}
	provider := &fakeConversationProvider{response: `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"external_peer","text":"A bounded answer."}}`}
	baseJournal := &fakeExternalTurnJournal{}
	baseRuntime := newExternalRuntimeForTest(t, view, provider, &fakeJournal{}, &fakePublisher{}, baseJournal)
	if result := baseRuntime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: request}); result.Status != TurnStatusPublicationPending || baseJournal.outcome == nil {
		t.Fatalf("create valid committed outcome = %#v", result)
	}
	if len(baseJournal.outcome.CanonicalEvents) != 2 {
		t.Fatalf("valid baseline events = %d, want response plus invocation receipt", len(baseJournal.outcome.CanonicalEvents))
	}

	tests := []struct {
		name   string
		mutate func(*ExternalCommittedBundle)
	}{
		{name: "response trace", mutate: func(bundle *ExternalCommittedBundle) {
			for index := range bundle.CanonicalEvents {
				if bundle.CanonicalEvents[index].MessageID == bundle.PublicationIntent.CanonicalResponseMessageID {
					bundle.CanonicalEvents[index].TraceID = modulecore.NewTraceID()
					return
				}
			}
			t.Fatal("valid baseline is missing its canonical response")
		}},
		{name: "receipt trace", mutate: func(bundle *ExternalCommittedBundle) {
			for index := range bundle.CanonicalEvents {
				if bundle.CanonicalEvents[index].EventType == "conversation.mio_emission_receipt" {
					bundle.CanonicalEvents[index].TraceID = modulecore.NewTraceID()
					return
				}
			}
			t.Fatal("valid baseline is missing its invocation receipt")
		}},
		{name: "receipt causation", mutate: func(bundle *ExternalCommittedBundle) {
			for index := range bundle.CanonicalEvents {
				if bundle.CanonicalEvents[index].EventType == "conversation.mio_emission_receipt" {
					bundle.CanonicalEvents[index].CausationEventID = modulecore.NewEventID()
					return
				}
			}
			t.Fatal("valid baseline is missing its invocation receipt")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outcome := cloneExternalCommittedBundle(*baseJournal.outcome)
			test.mutate(&outcome)
			journal := &fakeExternalTurnJournal{outcome: &outcome}
			provider := &fakeConversationProvider{response: `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"external_peer","text":"Must not regenerate."}}`}
			publisher := &fakePublisher{}
			runtime := newExternalRuntimeForTest(t, view, provider, &fakeJournal{}, publisher, journal)
			result := runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: request})
			if result.Status != TurnStatusCommitUncertain || result.Committed || provider.callCount() != 0 || journal.commitCalls != 0 || publisher.publishCount() != 0 {
				t.Fatalf("mismatched stored trace/receipt outcome was reused or published: result=%#v generation=%d commits=%d publishes=%d",
					result, provider.callCount(), journal.commitCalls, publisher.publishCount())
			}
		})
	}
}

type externalReadViewBuilder struct{ view ReadView }

func (builder externalReadViewBuilder) BuildReadView(context.Context, ReadViewRequest) (ReadView, error) {
	return builder.view, nil
}

func newExternalRuntimeForTest(t *testing.T, view ReadView, provider *fakeConversationProvider, legacy *fakeJournal, publisher *fakePublisher, external ExternalTurnJournal) *Runtime {
	t.Helper()
	runtime, err := NewRuntime(RuntimeDependencies{
		ReadViews: externalReadViewBuilder{view: view}, Composer: identityPromptComposer{},
		Generator: agent.NewMioEmissionGenerator(provider), Journal: legacy, ExternalJournal: external, Publisher: publisher,
		Config: RuntimeConfig{PolicyVersion: "policy-test-v1"},
	})
	if err != nil {
		t.Fatalf("NewRuntime(external fixture): %v", err)
	}
	return runtime
}

func TestExternalOnlyRuntimeUsesTypedJournalWithoutGenericViewerDependencies(t *testing.T) {
	request, _, _ := externalPeerReadViewFixture(t)
	acceptedText := payloadString(request.Accepted.InputEvent.Payload, "text")
	view := ReadView{SessionID: request.Session.SessionID, Messages: []PromptMessage{{
		Role: "user", Text: acceptedText, MessageID: string(request.Accepted.InputMessageID),
		TurnID: request.Accepted.TurnID, EventSeq: request.Accepted.AdmissionSeq,
	}}}
	provider := &fakeConversationProvider{response: `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"external_peer","text":"A typed external response."}}`}
	journal := &fakeExternalTurnJournal{}
	runtime, err := NewExternalRuntime(RuntimeDependencies{
		ReadViews: externalReadViewBuilder{view: view}, Composer: identityPromptComposer{},
		Generator: agent.NewMioEmissionGenerator(provider), ExternalJournal: journal,
		Config: RuntimeConfig{PolicyVersion: "policy-external-test-v1"},
	})
	if err != nil {
		t.Fatalf("NewExternalRuntime without generic Journal/Publisher: %v", err)
	}
	result := runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: request})
	if result.Status != TurnStatusPublicationPending || !result.Committed || provider.callCount() != 1 || journal.loadCalls != 1 || journal.commitCalls != 1 {
		t.Fatalf("external-only turn result=%#v generation=%d typed loads/commits=%d/%d", result, provider.callCount(), journal.loadCalls, journal.commitCalls)
	}
	if scope := provider.lastScope(); scope.Purpose != domainconversation.PurposeExternalPeer || scope.ActorID != request.Accepted.ActorID {
		t.Fatalf("external-only Mio scope = %#v, want trusted external-peer scope", scope)
	}

	viewerRequest, _ := runtimeInput(t, modulecore.NewSessionID(), 2, "must not enter the external-only lane")
	viewerResult := runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: viewerRequest})
	if viewerResult.Status != TurnStatusFailedBeforeCommit || !errors.Is(viewerResult.Cause, ErrExternalTriggerViewerRefused) ||
		provider.callCount() != 1 || journal.loadCalls != 1 || journal.commitCalls != 1 {
		t.Fatalf("external-only runtime accepted a non-external trigger: result=%#v generation=%d journal=%d/%d",
			viewerResult, provider.callCount(), journal.loadCalls, journal.commitCalls)
	}
	if _, err := NewRuntime(RuntimeDependencies{
		ReadViews: externalReadViewBuilder{view: view}, Composer: identityPromptComposer{},
		Generator: agent.NewMioEmissionGenerator(provider), ExternalJournal: journal,
		Config: RuntimeConfig{PolicyVersion: "policy-external-test-v1"},
	}); err == nil {
		t.Fatal("ordinary NewRuntime accepted an external-only configuration without generic Journal/Publisher")
	}
}

func TestExternalOnlyRuntimeBindsResponderActorBeforeReplayAndAttributesResponse(t *testing.T) {
	request, _, _ := externalPeerReadViewFixture(t)
	request = externalPeerRequestWithResponderActor(t, request, "shiro")
	view := ReadView{SessionID: request.Session.SessionID, Messages: []PromptMessage{{
		Role: "user", Text: payloadString(request.Accepted.InputEvent.Payload, "text"),
		MessageID: string(request.Accepted.InputMessageID), TurnID: request.Accepted.TurnID, EventSeq: request.Accepted.AdmissionSeq,
	}}}
	provider := &fakeConversationProvider{response: `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"external_peer","text":"A Shiro-attributed response."}}`}
	journal := &fakeExternalTurnJournal{}
	runtime, err := NewExternalRuntime(RuntimeDependencies{
		ReadViews: externalReadViewBuilder{view: view}, Composer: identityPromptComposer{},
		Generator: agent.NewMioEmissionGenerator(provider), ExternalJournal: journal,
		ExternalResponderActorID: "shiro", Config: RuntimeConfig{PolicyVersion: "policy-shiro-test-v1"},
	})
	if err != nil {
		t.Fatalf("NewExternalRuntime(Shiro responder): %v", err)
	}
	result := runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: request})
	if result.Status != TurnStatusPublicationPending || !result.Committed || journal.commitCalls != 1 || provider.callCount() != 1 {
		t.Fatalf("Shiro-bound external result=%#v generation=%d commits=%d", result, provider.callCount(), journal.commitCalls)
	}
	if scope := provider.lastScope(); scope.ActorID != request.Accepted.ActorID || scope.Purpose != domainconversation.PurposeExternalPeer {
		t.Fatalf("Shiro-bound scope = %#v, want sender ActorID and external-peer purpose", scope)
	}
	response, index := journal.commit.CanonicalEvents[0], journal.commit.MessageIndexes[0]
	if response.ActorKind != "agent" || response.ActorID != "shiro" || payloadString(response.Payload, "from") != "shiro" ||
		payloadString(response.Payload, "source_actor_id") != "shiro" || index.FromActor != "shiro" || index.ToKind != "external_peer" {
		t.Fatalf("Shiro-attributed response event/index = %#v/%#v", response, index)
	}
	storedBinding, err := domainconversation.ParseExternalPeerBinding(map[string]any{"external_peer_binding": response.Payload["external_peer_binding"]})
	if err != nil || storedBinding.ResponderActorID != "shiro" || storedBinding.RecipientPrincipalID != "shiro" {
		t.Fatalf("stored response binding = %+v, err %v; want trusted Shiro recipient binding", storedBinding, err)
	}
	if journal.commit.ExpectedLookup.SourceBinding.ResponderActorID != "shiro" {
		t.Fatalf("committed source binding = %+v, want Shiro responder actor", journal.commit.ExpectedLookup.SourceBinding)
	}
	result = runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: request})
	if result.Status != TurnStatusPublicationPending || provider.callCount() != 1 || journal.commitCalls != 1 || journal.loadCalls != 2 {
		t.Fatalf("Shiro-bound replay=%#v generation=%d journal loads/commits=%d/%d", result, provider.callCount(), journal.loadCalls, journal.commitCalls)
	}

	drifted := externalPeerRequestWithResponderActor(t, request, "midori")
	result = runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: drifted})
	if result.Status != TurnStatusFailedBeforeCommit || result.Committed || result.Cause == nil ||
		provider.callCount() != 1 || journal.loadCalls != 2 || journal.commitCalls != 1 {
		t.Fatalf("responder drift reached replay or generation: result=%#v generation=%d journal loads/commits=%d/%d", result, provider.callCount(), journal.loadCalls, journal.commitCalls)
	}
}

func TestNewExternalRuntimeRejectsInvalidTrustedResponderActor(t *testing.T) {
	request, _, _ := externalPeerReadViewFixture(t)
	view := externalReadViewBuilder{view: ReadView{SessionID: request.Session.SessionID}}
	for _, actorID := range []string{"mio", "heavy", "wild", "Shiro", " shiro"} {
		_, err := NewExternalRuntime(RuntimeDependencies{
			ReadViews: view, Composer: identityPromptComposer{}, Generator: agent.NewMioEmissionGenerator(&fakeConversationProvider{}),
			ExternalJournal: &fakeExternalTurnJournal{}, ExternalResponderActorID: actorID,
			Config: RuntimeConfig{PolicyVersion: "policy-test-v1"},
		})
		if err == nil {
			t.Errorf("NewExternalRuntime(ExternalResponderActorID=%q) error = nil, want strict validation", actorID)
		}
	}
}

func TestExternalOnlyRuntimeSupportsMidoriResponder(t *testing.T) {
	request, _, _ := externalPeerReadViewFixture(t)
	request = externalPeerRequestWithResponderActor(t, request, "midori")
	view := ReadView{SessionID: request.Session.SessionID, Messages: []PromptMessage{{
		Role: "user", Text: payloadString(request.Accepted.InputEvent.Payload, "text"),
		MessageID: string(request.Accepted.InputMessageID), TurnID: request.Accepted.TurnID, EventSeq: request.Accepted.AdmissionSeq,
	}}}
	provider := &fakeConversationProvider{response: `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"external_peer","text":"A Midori-attributed response."}}`}
	journal := &fakeExternalTurnJournal{}
	runtime, err := NewExternalRuntime(RuntimeDependencies{
		ReadViews: externalReadViewBuilder{view: view}, Composer: identityPromptComposer{},
		Generator: agent.NewMioEmissionGenerator(provider), ExternalJournal: journal,
		ExternalResponderActorID: "midori", Config: RuntimeConfig{PolicyVersion: "policy-midori-test-v1"},
	})
	if err != nil {
		t.Fatalf("NewExternalRuntime(Midori responder): %v", err)
	}
	result := runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: request})
	if result.Status != TurnStatusPublicationPending || len(journal.commit.CanonicalEvents) == 0 || len(journal.commit.MessageIndexes) != 1 {
		t.Fatalf("Midori external result=%#v commit=%#v", result, journal.commit)
	}
	response, index := journal.commit.CanonicalEvents[0], journal.commit.MessageIndexes[0]
	if response.ActorKind != "agent" || response.ActorID != "midori" || payloadString(response.Payload, "from") != "midori" || index.FromActor != "midori" {
		t.Fatalf("Midori-attributed response event/index = %#v/%#v", response, index)
	}
}

func TestExternalOnlyRuntimeSupportsKuroBoardChatWithReplyProvenance(t *testing.T) {
	request, _, _ := externalPeerReadViewFixture(t)
	request = externalPeerRequestWithResponderActor(t, request, "kuro")
	view := ReadView{SessionID: request.Session.SessionID, Messages: []PromptMessage{{
		Role: "user", Text: payloadString(request.Accepted.InputEvent.Payload, "text"),
		MessageID: string(request.Accepted.InputMessageID), TurnID: request.Accepted.TurnID, EventSeq: request.Accepted.AdmissionSeq,
	}}}
	provider := &fakeConversationProvider{response: `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"external_peer","text":"A Kuro-attributed BOARD chat reply."}}`}
	journal := &fakeExternalTurnJournal{}
	runtime, err := NewExternalRuntime(RuntimeDependencies{
		ReadViews: externalReadViewBuilder{view: view}, Composer: identityPromptComposer{},
		Generator: agent.NewMioEmissionGenerator(provider), ExternalJournal: journal,
		ExternalResponderActorID: "kuro", Config: RuntimeConfig{PolicyVersion: "policy-kuro-board-chat-v1"},
	})
	if err != nil {
		t.Fatalf("NewExternalRuntime(Kuro BOARD CHAT): %v", err)
	}
	result := runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: request})
	if result.Status != TurnStatusPublicationPending || !result.Committed || journal.commitCalls != 1 || provider.callCount() != 1 {
		t.Fatalf("Kuro BOARD chat result=%#v generation=%d commits=%d", result, provider.callCount(), journal.commitCalls)
	}
	if got := provider.lastScope(); got.ActorID != request.Accepted.ActorID || got.Purpose != domainconversation.PurposeExternalPeer {
		t.Fatalf("Kuro generation scope = %#v, want canonical sender and external-peer purpose", got)
	}
	if request := provider.lastRequest(); request.OnToken != nil || request.OnLogicalOutcomeEvent != nil {
		t.Fatalf("Kuro BOARD chat request enabled streaming callbacks: token=%t logical=%t", request.OnToken != nil, request.OnLogicalOutcomeEvent != nil)
	}
	response, index := journal.commit.CanonicalEvents[0], journal.commit.MessageIndexes[0]
	if response.ActorKind != "agent" || response.ActorID != "kuro" || payloadString(response.Payload, "from") != "kuro" ||
		payloadString(response.Payload, "to") != "external_peer" || payloadString(response.Payload, "text") != "A Kuro-attributed BOARD chat reply." ||
		index.FromActor != "kuro" || index.ToKind != "external_peer" {
		t.Fatalf("Kuro BOARD reply provenance = event=%#v index=%#v", response, index)
	}
	storedBinding, err := domainconversation.ParseExternalPeerBinding(map[string]any{"external_peer_binding": response.Payload["external_peer_binding"]})
	if err != nil || storedBinding.ResponderActorID != "kuro" || storedBinding.RecipientPrincipalID != "kuro" {
		t.Fatalf("Kuro committed reply binding = %+v, err %v; want authenticated Kuro principal", storedBinding, err)
	}
	replayed := runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: request})
	if replayed.Status != TurnStatusPublicationPending || !replayed.Committed || provider.callCount() != 1 || journal.commitCalls != 1 || journal.loadCalls != 2 {
		t.Fatalf("Kuro BOARD reply replay=%#v generation=%d commits=%d; want persisted reply without regeneration", replayed, provider.callCount(), journal.commitCalls)
	}
}

type fakeExternalTurnJournal struct {
	loadCalls      int
	commitCalls    int
	loadedLookup   ExternalTurnLookup
	commit         ExternalCommitBundle
	outcome        *ExternalCommittedBundle
	loadErr        error
	commitErr      error
	persistOnError bool
}

func (journal *fakeExternalTurnJournal) LoadExternalCommittedOutcome(_ context.Context, lookup ExternalTurnLookup) (ExternalCommittedBundle, bool, error) {
	journal.loadCalls++
	journal.loadedLookup = lookup
	if journal.loadErr != nil {
		return ExternalCommittedBundle{}, false, journal.loadErr
	}
	if journal.outcome == nil {
		return ExternalCommittedBundle{}, false, nil
	}
	return cloneExternalCommittedBundle(*journal.outcome), true, nil
}

func (journal *fakeExternalTurnJournal) CommitExternalTurnBundle(_ context.Context, bundle ExternalCommitBundle) (ExternalCommittedBundle, error) {
	journal.commitCalls++
	journal.commit = bundle
	committed := ExternalCommittedBundle{
		Lookup: bundle.ExpectedLookup, ResultState: bundle.ResultState, SafeError: bundle.SafeError, FailureStage: bundle.FailureStage,
		CanonicalEvents:   append([]modulecore.EventEnvelope(nil), bundle.CanonicalEvents...),
		MessageIndexes:    append([]MessageIndex(nil), bundle.MessageIndexes...),
		InvocationReceipt: bundle.InvocationReceipt, PublicationIntent: bundle.PublicationIntent,
	}
	for index := range committed.CanonicalEvents {
		committed.CanonicalEvents[index].EventSeq = modulecore.EventSeq(20 + index)
	}
	if journal.commitErr == nil || journal.persistOnError {
		journal.outcome = &committed
	}
	if journal.commitErr != nil {
		return ExternalCommittedBundle{}, journal.commitErr
	}
	return committed, nil
}

func cloneExternalCommittedBundle(bundle ExternalCommittedBundle) ExternalCommittedBundle {
	cloned := bundle
	cloned.CanonicalEvents = append([]modulecore.EventEnvelope(nil), bundle.CanonicalEvents...)
	cloned.MessageIndexes = append([]MessageIndex(nil), bundle.MessageIndexes...)
	return cloned
}

func externalLookupFixture(t *testing.T, request ReadViewRequest) ExternalTurnLookup {
	t.Helper()
	values := request.Accepted.InputEvent.Payload["external_peer_binding"].(map[string]any)
	stringField := func(name string) string { return values[name].(string) }
	responderActorID, _ := values["responder_actor_id"].(string)
	binding := domainconversation.ExternalPeerBinding{
		BoardInstanceID: stringField("board_instance_id"), RecipientPrincipalID: stringField("recipient_principal_id"),
		ChannelID: stringField("channel_id"), ThreadID: stringField("thread_id"), MessageID: stringField("message_id"),
		SenderPrincipalID: stringField("sender_principal_id"), WireKind: stringField("wire_kind"),
		ResponderActorID: responderActorID, Classification: domainconversation.ExternalPeerClassification(stringField("classification")), ReplyTo: stringField("reply_to"),
	}
	encoded, err := json.Marshal(request.Accepted.InputEvent.Payload)
	if err != nil {
		t.Fatalf("json.Marshal(accepted external payload): %v", err)
	}
	fingerprint := sha256.Sum256(encoded)
	return ExternalTurnLookup{
		OwnerID: request.Session.OwnerID, SessionID: request.Accepted.SessionID, TurnID: request.Accepted.TurnID,
		GenerationEpoch: request.Accepted.GenerationEpoch, OperationID: turnOperationID(request.Accepted.TurnID),
		SourceBinding: binding, SourceFingerprint: hex.EncodeToString(fingerprint[:]),
	}
}
