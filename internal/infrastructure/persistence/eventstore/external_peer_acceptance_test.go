package eventstore

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestAcceptTurnAcceptsBoundExternalPeerMessageWithStableIdentity(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "external-peer.sqlite")
	store, err := NewSQLiteStore(databasePath)
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close(): %v", err)
		}
	})
	ctx := context.Background()
	sessionID := modulecore.NewSessionID()
	input := externalPeerAcceptInput(sessionID, "board-message-1", "Please explain this result.")

	accepted, err := store.AcceptTurn(ctx, input)
	if err != nil {
		t.Fatalf("AcceptTurn(valid bound external peer message): %v", err)
	}
	if accepted.InputEvent.EventType != "conversation.external_peer_message" {
		t.Fatalf("accepted event_type = %q, want conversation.external_peer_message", accepted.InputEvent.EventType)
	}
	if accepted.TriggerKind != "external_peer_message" {
		t.Fatalf("accepted trigger_kind = %q, want external_peer_message", accepted.TriggerKind)
	}
	if accepted.ActorID != "board:test-instance:grok" {
		t.Fatalf("accepted actor_id = %q, want namespaced external source identity", accepted.ActorID)
	}
	if accepted.InputEvent.ActorKind != "" || accepted.InputEvent.ActorID != "" {
		t.Fatalf("accepted event execution actor = (%q, %q), want no external execution actor", accepted.InputEvent.ActorKind, accepted.InputEvent.ActorID)
	}
	if got, want := accepted.InputEvent.Payload["from"], "external_peer"; got != want {
		t.Fatalf("accepted event display source = %v, want %q", got, want)
	}
	if got, want := accepted.InputEvent.Payload["external_peer_binding"], input.InputEvent.Payload["external_peer_binding"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("accepted external peer binding = %#v, want %#v", got, want)
	}
	if got, want := accepted.InputEvent.Payload["body"], input.InputEvent.Payload["body"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("accepted untrusted body = %#v, want exact body %#v", got, want)
	}
	if accepted.InputEvent.EventSeq != accepted.AdmissionSeq {
		t.Fatalf("accepted event sequence = %d, admission sequence = %d", accepted.InputEvent.EventSeq, accepted.AdmissionSeq)
	}

	binding := input.InputEvent.Payload["external_peer_binding"].(map[string]any)
	externalIDs := []string{
		binding["board_instance_id"].(string), binding["recipient_principal_id"].(string),
		binding["channel_id"].(string), binding["thread_id"].(string), binding["message_id"].(string),
		binding["sender_principal_id"].(string), binding["wire_kind"].(string), binding["classification"].(string), accepted.ActorID,
	}
	coreIDs := []string{
		string(accepted.SessionID), string(accepted.TurnID), string(accepted.InputMessageID),
		string(accepted.InputEvent.EventID), string(accepted.InputEvent.TraceID),
	}
	if accepted.InputEvent.ThreadID != "" {
		coreIDs = append(coreIDs, string(accepted.InputEvent.ThreadID))
	}
	for _, coreID := range coreIDs {
		for _, externalID := range externalIDs {
			if coreID == externalID {
				t.Fatalf("CORE identity %q reused BOARD reference %q", coreID, externalID)
			}
		}
	}

	crossOwner := externalPeerAcceptInput(sessionID, "board-message-1", "Please explain this result.")
	crossOwner.OperationID = "core-external-peer-other-owner-operation"
	crossOwner.ClientSubmissionID = "core-external-peer-other-owner-submission"
	crossOwner.OwnerID = "other-owner"
	crossOwner.CreateNewSession = false
	if _, err := store.AcceptTurn(ctx, crossOwner); !errors.Is(err, ErrConversationSessionConflict) {
		t.Fatalf("AcceptTurn(external peer under another owner) error = %v, want ErrConversationSessionConflict", err)
	}

	// A retry may mint different provisional CORE IDs, but the accepted receipt
	// and all issuer-owned identities must resolve to the original commit.
	retry := externalPeerAcceptInput(sessionID, "board-message-1", "Please explain this result.")
	retry.CreateNewSession = false
	replayed, err := store.AcceptTurn(ctx, retry)
	if err != nil {
		t.Fatalf("AcceptTurn(same source retry): %v", err)
	}
	assertJSONEqual(t, replayed, accepted)

	changedBody := externalPeerAcceptInput(sessionID, "board-message-1", "Changed body.")
	if _, err := store.AcceptTurn(ctx, changedBody); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("AcceptTurn(changed body under same operation) error = %v, want ErrOperationConflict", err)
	}
	changedSource := externalPeerAcceptInput(sessionID, "board-message-2", "Please explain this result.")
	changedSource.OperationID = "core-external-peer-operation-2"
	if _, err := store.AcceptTurn(ctx, changedSource); !errors.Is(err, ErrClientSubmissionConflict) {
		t.Fatalf("AcceptTurn(changed source under same submission) error = %v, want ErrClientSubmissionConflict", err)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close(before pending read): %v", err)
	}
	store, err = NewSQLiteStore(databasePath)
	if err != nil {
		t.Fatalf("NewSQLiteStore(reopen): %v", err)
	}
	pending, err := store.ReadPendingAcceptedTurns(ctx, sessionID, input.OwnerID)
	if err != nil {
		t.Fatalf("ReadPendingAcceptedTurns(reopened external acceptance): %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending accepted turns = %d, want 1", len(pending))
	}
	assertJSONEqual(t, pending[0], accepted)

	tail, err := store.ReadCompleteSessionTail(ctx, sessionID, 0)
	if err != nil {
		t.Fatalf("ReadCompleteSessionTail(reopened external acceptance): %v", err)
	}
	if !tail.Complete {
		t.Fatal("ReadCompleteSessionTail(external acceptance) returned an incomplete canonical snapshot")
	}
	var foundExternalInput bool
	for _, message := range tail.Messages {
		if message.Index.MessageID != accepted.InputMessageID {
			continue
		}
		foundExternalInput = true
		if message.Event.EventType != "conversation.external_peer_message" || message.Event.EventSeq != accepted.AdmissionSeq ||
			message.Index.SessionID != accepted.SessionID || message.Index.TurnID != accepted.TurnID || message.Index.FromActor != accepted.ActorID {
			t.Fatalf("canonical external tail input = %#v, want the accepted owner-bound source row", message)
		}
	}
	if !foundExternalInput {
		t.Fatal("canonical session tail omitted the accepted external peer input")
	}
}

func TestAcceptTurnPersistsExplicitExternalPeerResponder(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	sessionID := modulecore.NewSessionID()
	input := externalPeerAcceptInput(sessionID, "board-message-shiro", "Please review this result.")
	input.ExternalPeerBinding.ResponderActorID = "shiro"
	input.ExternalPeerBinding.RecipientPrincipalID = "shiro"
	input.InputEvent.Payload["external_peer_binding"] = externalPeerBindingPayload(*input.ExternalPeerBinding)
	input.InputEvent.Payload["to"] = "shiro"
	input.InputMessage.ToActor = "shiro"

	accepted, err := store.AcceptTurn(ctx, input)
	if err != nil {
		t.Fatalf("AcceptTurn(explicit Shiro responder): %v", err)
	}
	if accepted.ActorID != "board:test-instance:grok" || accepted.InputEvent.Payload["to"] != "shiro" {
		t.Fatalf("accepted external source/responder = %q/%v; want BOARD source identity and Shiro target", accepted.ActorID, accepted.InputEvent.Payload["to"])
	}
	if replayed, err := store.AcceptTurn(ctx, input); err != nil || !reflect.DeepEqual(replayed, accepted) {
		t.Fatalf("AcceptTurn(explicit Shiro responder retry) = %#v, %v; want original receipt", replayed, err)
	}
	tail, err := store.ReadCompleteSessionTail(ctx, sessionID, 0)
	if err != nil || !tail.Complete {
		t.Fatalf("ReadCompleteSessionTail(explicit Shiro responder) = complete %t, err %v", tail.Complete, err)
	}
	for _, message := range tail.Messages {
		if message.Index.MessageID == accepted.InputMessageID {
			if message.Index.ToKind != "agent" || message.Index.ToActor != "shiro" || message.Event.Payload["to"] != "shiro" {
				t.Fatalf("canonical explicit responder message = %#v; want target Shiro in event and index", message)
			}
			return
		}
	}
	t.Fatal("canonical session tail omitted the explicit-responder input")
}

func TestAcceptTurnRejectsMalformedExternalPeerBindingAndSourceSpoof(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*AcceptInput)
	}{
		{
			name: "missing typed binding",
			mutate: func(input *AcceptInput) {
				input.ExternalPeerBinding = nil
			},
		},
		{
			name: "namespace delimiter collision",
			mutate: func(input *AcceptInput) {
				input.ExternalPeerBinding.BoardInstanceID = "test:instance"
			},
		},
		{
			name: "actor does not match trusted source",
			mutate: func(input *AcceptInput) {
				input.ActorID = "board:test-instance:other"
			},
		},
		{
			name: "explicit responder target mismatch",
			mutate: func(input *AcceptInput) {
				input.ExternalPeerBinding.ResponderActorID = "shiro"
				input.ExternalPeerBinding.RecipientPrincipalID = "shiro"
				input.InputEvent.Payload["external_peer_binding"] = externalPeerBindingPayload(*input.ExternalPeerBinding)
			},
		},
		{
			name: "payload source spoof",
			mutate: func(input *AcceptInput) {
				spoofedActor := "board:test-instance:other"
				input.InputEvent.Payload["source_actor_id"] = spoofedActor
				input.InputMessage.FromActor = spoofedActor
			},
		},
		{
			name: "BOARD message id reused as CORE message id",
			mutate: func(input *AcceptInput) {
				input.ExternalPeerBinding.MessageID = string(input.InputEvent.MessageID)
				input.InputEvent.Payload["external_peer_binding"] = externalPeerBindingPayload(*input.ExternalPeerBinding)
			},
		},
		{
			name: "current accepted-only lane rejects unbound execution lineage",
			mutate: func(input *AcceptInput) {
				input.InputEvent.TaskID = modulecore.NewTaskID()
			},
		},
		{
			name: "reply cannot enter new request lane",
			mutate: func(input *AcceptInput) {
				input.ExternalPeerBinding.WireKind = "reply"
				input.ExternalPeerBinding.ReplyTo = "board-prior-message"
				input.InputEvent.Payload["external_peer_binding"] = externalPeerBindingPayload(*input.ExternalPeerBinding)
			},
		},
		{
			name: "self-declared terminal classification cannot enter request lane",
			mutate: func(input *AcceptInput) {
				input.ExternalPeerBinding.Classification = domainconversation.ExternalPeerClassificationCorrelatedReplyTerminal
				input.InputEvent.Payload["external_peer_binding"] = externalPeerBindingPayload(*input.ExternalPeerBinding)
			},
		},
		{
			name: "legacy kind-only binding schema is rejected",
			mutate: func(input *AcceptInput) {
				binding := input.InputEvent.Payload["external_peer_binding"].(map[string]any)
				delete(binding, "wire_kind")
				delete(binding, "classification")
				binding["kind"] = "request"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			input := externalPeerAcceptInput(modulecore.NewSessionID(), "board-message-1", "Please explain this result.")
			test.mutate(&input)
			if _, err := store.AcceptTurn(context.Background(), input); err == nil {
				t.Fatal("AcceptTurn() error = nil, want invalid external peer input rejection")
			}
		})
	}
}

func TestAcceptTurnRejectsUnboundAuxiliaryCOREIDs(t *testing.T) {
	tests := []struct {
		name  string
		setID func(*modulecore.EventEnvelope)
	}{
		{name: "request_id", setID: func(event *modulecore.EventEnvelope) { event.RequestID = modulecore.NewRequestID() }},
		{name: "response_id", setID: func(event *modulecore.EventEnvelope) { event.ResponseID = modulecore.NewResponseID() }},
		{name: "workstream_id", setID: func(event *modulecore.EventEnvelope) { event.WorkstreamID = modulecore.NewWorkstreamID() }},
		{name: "goal_id", setID: func(event *modulecore.EventEnvelope) { event.GoalID = modulecore.NewGoalID() }},
		{name: "utterance_id", setID: func(event *modulecore.EventEnvelope) { event.UtteranceID = modulecore.NewUtteranceID() }},
		{name: "artifact_id", setID: func(event *modulecore.EventEnvelope) { event.ArtifactID = modulecore.NewArtifactID() }},
		{name: "evidence_id", setID: func(event *modulecore.EventEnvelope) { event.EvidenceID = modulecore.NewEvidenceID() }},
		{name: "memory_id", setID: func(event *modulecore.EventEnvelope) { event.MemoryID = modulecore.NewMemoryID() }},
		{name: "relation_id", setID: func(event *modulecore.EventEnvelope) { event.RelationID = modulecore.NewRelationID() }},
		{name: "schedule_id", setID: func(event *modulecore.EventEnvelope) { event.ScheduleID = modulecore.NewScheduleID() }},
		{name: "queue_item_id", setID: func(event *modulecore.EventEnvelope) { event.QueueItemID = modulecore.NewQueueItemID() }},
		{name: "checkpoint_id", setID: func(event *modulecore.EventEnvelope) { event.CheckpointID = modulecore.NewCheckpointID() }},
		{name: "receipt_id", setID: func(event *modulecore.EventEnvelope) { event.ReceiptID = modulecore.NewReceiptID() }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			input := externalPeerAcceptInput(modulecore.NewSessionID(), "board-message-1", "Please explain this result.")
			test.setID(&input.InputEvent)
			if _, err := store.AcceptTurn(context.Background(), input); err == nil {
				t.Fatal("AcceptTurn() error = nil, want unbound auxiliary CORE identity rejection")
			}
		})
	}
}

func TestAcceptTurnRejectsBOARDMessageReusedAsAuxiliaryRequestID(t *testing.T) {
	store := newTestStore(t)
	input := externalPeerAcceptInput(modulecore.NewSessionID(), "board-message-1", "Please explain this result.")
	input.InputEvent.RequestID = modulecore.NewRequestID()
	input.ExternalPeerBinding.MessageID = string(input.InputEvent.RequestID)
	input.InputEvent.Payload["external_peer_binding"] = externalPeerBindingPayload(*input.ExternalPeerBinding)

	if _, err := store.AcceptTurn(context.Background(), input); err == nil {
		t.Fatal("AcceptTurn(BOARD message id equal to typed CORE req_uuid): error = nil, want auxiliary identity rejection")
	}
}

func TestAcceptTurnKeepsExternalPeerLaneDistinctFromUserInput(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	sessionID, turnID := modulecore.NewSessionID(), modulecore.NewTurnID()
	userEvent := liveInputEvent(sessionID, turnID, "ordinary authenticated user input")
	userInput := AcceptInput{
		OperationID: "core-user-control-operation", OwnerID: "owner-1", RuntimeVersion: "v2",
		ClientSubmissionID: "core-user-control-submission", ActorID: "user-1", TriggerKind: "user_message",
		GenerationEpoch: 1, CreateNewSession: true, InputEvent: userEvent,
		InputMessage: MessageIndex{
			MessageID: userEvent.MessageID, EventID: userEvent.EventID, SessionID: sessionID, TurnID: turnID,
			FromActor: "user-1", ToKind: "agent", ToActor: "mio", Kind: "user_message", Visibility: "public",
		},
	}
	if _, err := store.AcceptTurn(ctx, userInput); err != nil {
		t.Fatalf("AcceptTurn(ordinary authenticated user input): %v", err)
	}

	spoofedExternal := externalPeerAcceptInput(modulecore.NewSessionID(), "board-message-2", "Please explain this result.")
	spoofedExternal.TriggerKind = "user_message"
	spoofedExternal.ExternalPeerBinding = nil
	spoofedExternal.InputEvent.Payload["from"] = "user"
	delete(spoofedExternal.InputEvent.Payload, "external_peer_binding")
	if _, err := store.AcceptTurn(ctx, spoofedExternal); err == nil {
		t.Fatal("AcceptTurn(external event labeled as user input) error = nil, want mixed-contract rejection")
	}

	userAsExternal := externalPeerAcceptInput(modulecore.NewSessionID(), "board-message-3", "Please explain this result.")
	userAsExternal.InputEvent.EventType = "conversation.user_message"
	if _, err := store.AcceptTurn(ctx, userAsExternal); err == nil {
		t.Fatal("AcceptTurn(user event labeled as external peer input) error = nil, want mixed-contract rejection")
	}
}

func externalPeerAcceptInput(sessionID modulecore.SessionID, boardMessageID, bodyText string) AcceptInput {
	sourceActorID := "board:test-instance:grok"
	peerBinding := &ExternalPeerBinding{
		BoardInstanceID: "test-instance", RecipientPrincipalID: "rencrow-comm",
		ChannelID: "channel-1", ThreadID: "thread-1", MessageID: boardMessageID,
		SenderPrincipalID: "grok", WireKind: "request", Classification: domainconversation.ExternalPeerClassificationRequest,
	}
	binding := externalPeerBindingPayload(*peerBinding)
	body := map[string]any{
		"text":     bodyText,
		"from":     "user",
		"actor_id": "user-1",
		"owner_id": "other-owner",
	}
	event := liveInputEvent(sessionID, modulecore.NewTurnID(), bodyText)
	event.EventType = "conversation.external_peer_message"
	event.Payload = map[string]any{
		"from":                  "external_peer",
		"source_actor_id":       sourceActorID,
		"to":                    "mio",
		"visibility":            "public",
		"content":               bodyText,
		"text":                  bodyText,
		"external_peer_binding": binding,
		"body":                  body,
	}

	return AcceptInput{
		OperationID:         "core-external-peer-operation-1",
		OwnerID:             "owner-1",
		RuntimeVersion:      "v2",
		ClientSubmissionID:  "core-external-peer-submission-1",
		ActorID:             sourceActorID,
		TriggerKind:         "external_peer_message",
		GenerationEpoch:     1,
		CreateNewSession:    true,
		InputEvent:          event,
		ExternalPeerBinding: peerBinding,
		InputMessage: MessageIndex{
			MessageID: event.MessageID, EventID: event.EventID, SessionID: sessionID,
			TurnID: event.TurnID, FromActor: sourceActorID, ToKind: "agent", ToActor: "mio",
			Kind: "external_peer_message", Visibility: "public",
		},
	}
}

func externalPeerBindingPayload(binding ExternalPeerBinding) map[string]any {
	return binding.ToPayload()
}
