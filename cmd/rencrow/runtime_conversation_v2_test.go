package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestToConversationAcceptedTurnPreservesTriggerKind(t *testing.T) {
	stored := eventstore.AcceptedTurn{
		SessionID: modulecore.NewSessionID(), TurnID: modulecore.NewTurnID(), ActorID: "shiro",
		TriggerKind: "report_reply", InputMessageID: modulecore.NewMessageID(),
		AdmissionSeq: 15, GenerationEpoch: 3, State: "accepted",
	}

	got := toConversationAcceptedTurn(stored)
	if got.TriggerKind != stored.TriggerKind || got.ActorID != stored.ActorID || got.InputMessageID != stored.InputMessageID {
		t.Fatalf("accepted turn conversion = %+v, want persisted trigger and trusted report actor", got)
	}
}

func TestResponseFirstProcessRefusesPendingExternalPeerBeforeGeneration(t *testing.T) {
	store, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "viewer-external.sqlite"))
	if err != nil {
		t.Fatalf("open event store: %v", err)
	}
	defer store.Close()
	provider := &responseFirstTestProvider{}
	publisher := newResponseFirstTestPublisher()
	runtime, err := newResponseFirstConversationRuntime(store, agent.NewMioAgent(nil, nil, nil, nil, nil, nil), provider, publisher, responseFirstTestOwnerID)
	if err != nil {
		t.Fatalf("build response-first runtime: %v", err)
	}
	allowResponseFirstTestReservations(runtime)
	user, err := runtime.Accept(context.Background(), responseFirstRequest(modulecore.NewSessionID(), "viewer-before-external", "owner input"))
	if err != nil {
		t.Fatalf("accept owner input: %v", err)
	}
	external := acceptSyntheticExternalPeerForViewer(t, store, user.SessionID)
	accepted := viewer.V2AcceptedTurn{
		SessionID: external.SessionID, TurnID: external.TurnID, TraceID: external.InputEvent.TraceID,
		InputMessageID: external.InputMessageID, ClientSubmissionID: external.ClientSubmissionID, ActorID: external.ActorID,
	}
	if err := runtime.Process(context.Background(), accepted); !errors.Is(err, conversationruntime.ErrExternalTriggerViewerRefused) {
		t.Fatalf("Process(external peer) = %v, want typed Viewer refusal", err)
	}
	pending, err := store.ReadPendingAcceptedTurns(context.Background(), external.SessionID, responseFirstTestOwnerID)
	if err != nil {
		t.Fatalf("read external peer after refusal: %v", err)
	}
	if len(pending) != 2 || provider.calls != 0 || len(publisher.committed) != 0 {
		t.Fatalf("Viewer refusal changed pending/generation/publication: pending=%d generation=%d publications=%d", len(pending), provider.calls, len(publisher.committed))
	}
}

func TestResponseFirstResumeSkipsExternalPeerAndContinuesOwnerTurn(t *testing.T) {
	store, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "viewer-external-resume.sqlite"))
	if err != nil {
		t.Fatalf("open event store: %v", err)
	}
	defer store.Close()
	provider := &responseFirstTestProvider{}
	publisher := newResponseFirstTestPublisher()
	runtime, err := newResponseFirstConversationRuntime(store, agent.NewMioAgent(nil, nil, nil, nil, nil, nil), provider, publisher, responseFirstTestOwnerID)
	if err != nil {
		t.Fatalf("build response-first runtime: %v", err)
	}
	allowResponseFirstTestReservations(runtime)
	user, err := runtime.Accept(context.Background(), responseFirstRequest(modulecore.NewSessionID(), "resume-owner-before-external", "owner input"))
	if err != nil {
		t.Fatalf("accept owner input: %v", err)
	}
	external := acceptSyntheticExternalPeerForViewer(t, store, user.SessionID)
	if err := runtime.ResumePendingAcceptedTurns(context.Background(), user.SessionID); err != nil {
		t.Fatalf("ResumePendingAcceptedTurns(): %v", err)
	}
	select {
	case bundle := <-publisher.committed:
		if bundle.TurnID != user.TurnID {
			t.Fatalf("resume published turn=%s, want ordinary owner turn %s", bundle.TurnID, user.TurnID)
		}
	default:
		t.Fatal("resume did not continue the ordinary owner turn")
	}
	pending, err := store.ReadPendingAcceptedTurns(context.Background(), user.SessionID, responseFirstTestOwnerID)
	if err != nil {
		t.Fatalf("read remaining pending turns: %v", err)
	}
	if len(pending) != 1 || pending[0].TurnID != external.TurnID || pending[0].TriggerKind != "external_peer_message" || provider.calls != 1 || len(publisher.committed) != 0 {
		t.Fatalf("resume external skip/owner continuation = pending=%+v generation=%d publications=%d", pending, provider.calls, len(publisher.committed))
	}
}

func acceptSyntheticExternalPeerForViewer(t *testing.T, store *eventstore.SQLiteStore, sessionID modulecore.SessionID) eventstore.AcceptedTurn {
	t.Helper()
	binding := eventstore.ExternalPeerBinding{
		BoardInstanceID: "synthetic-instance", RecipientPrincipalID: "core-ren", ChannelID: "channel-a",
		ThreadID: "thread-a", MessageID: "board-message-a", SenderPrincipalID: "peer-a",
		Classification: domainconversation.ExternalPeerClassificationRequest,
	}
	sourceActorID, err := binding.SourceActorID()
	if err != nil {
		t.Fatalf("build synthetic BOARD source actor: %v", err)
	}
	body := "synthetic external input"
	event := modulecore.NewRootEventEnvelope("orchestrator", "conversation.external_peer_message", time.Now().UTC(), map[string]any{
		"from": "external_peer", "source_actor_id": sourceActorID, "to": "mio", "visibility": "public",
		"text": body, "content": body, "body": map[string]any{"text": body}, "external_peer_binding": binding.ToPayload(),
	})
	event.SessionID, event.TurnID, event.MessageID = sessionID, modulecore.NewTurnID(), modulecore.NewMessageID()
	accepted, err := store.AcceptTurn(context.Background(), eventstore.AcceptInput{
		OperationID: "synthetic-external-accept:" + string(event.TurnID), OwnerID: responseFirstTestOwnerID,
		RuntimeVersion: responseFirstRuntimeVer, ClientSubmissionID: "synthetic-external:" + string(event.TurnID),
		ActorID: sourceActorID, TriggerKind: "external_peer_message", GenerationEpoch: 1,
		InputEvent: event, ExternalPeerBinding: &binding,
		InputMessage: eventstore.MessageIndex{
			MessageID: event.MessageID, EventID: event.EventID, SessionID: event.SessionID, TurnID: event.TurnID,
			FromActor: sourceActorID, ToKind: "agent", ToActor: "mio", Kind: "external_peer_message", Visibility: "public",
		},
	})
	if err != nil {
		t.Fatalf("accept synthetic external peer turn: %v", err)
	}
	return accepted
}
