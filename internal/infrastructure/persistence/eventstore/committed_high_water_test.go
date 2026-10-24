package eventstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestChatLeaseCapturesCommittedOrchestratorHighWaterInRAM(t *testing.T) {
	store, queue, _, _ := newChatAdmissionStore(t, 8)
	ctx := context.Background()
	first, err := store.AppendSequenced(ctx, eventFixture(modulecore.NewTraceID(), "relay.predecessor", "orchestrator"))
	if err != nil {
		t.Fatalf("AppendSequenced(orchestrator): %v", err)
	}
	private, err := store.AppendSequenced(ctx, eventFixture(modulecore.NewTraceID(), "relay.private", "superagent"))
	if err != nil {
		t.Fatalf("AppendSequenced(private): %v", err)
	}
	if private.EventSeq <= first.EventSeq {
		t.Fatalf("private EventSeq=%d, want after orchestrator EventSeq=%d", private.EventSeq, first.EventSeq)
	}
	if got := store.OrchestratorCommittedHighWater(); got != first.EventSeq {
		t.Fatalf("committed orchestrator high-water=%d, want %d (private seq %d must not affect it)", got, first.EventSeq, private.EventSeq)
	}

	chatCtx, _ := newChatAdmissionContext(t, queue, 1)
	lease, err := store.TryBeginChat(chatCtx)
	if err != nil {
		t.Fatalf("TryBeginChat: %v", err)
	}
	defer lease.EndChat()
	if got := lease.OrchestratorCommittedHighWater(); got != first.EventSeq {
		t.Fatalf("lease orchestrator high-water=%d, want %d", got, first.EventSeq)
	}
}

func TestAdmittedOrchestratorHighWaterAdvancesOnlyAfterDurableCommit(t *testing.T) {
	store, queue, _, _ := newChatAdmissionStore(t, 8)
	ctx, admission := newChatAdmissionContext(t, queue, 1)
	releaseBlocker := enqueueChatAdmissionBlocker(t, queue)
	lease, err := store.TryBeginChat(ctx)
	if err != nil {
		t.Fatalf("TryBeginChat with non-EventSeq queue head: %v", err)
	}
	defer lease.EndChat()
	event, _, err := store.AdmitSequenced(ctx, eventFixture(modulecore.NewTraceID(), "chat.admitted", "orchestrator"))
	if err != nil {
		t.Fatalf("AdmitSequenced: %v", err)
	}
	if got := store.OrchestratorCommittedHighWater(); got != 0 {
		t.Fatalf("high-water after RAM admission=%d, want 0 until durable commit", got)
	}
	if got := lease.OrchestratorCommittedHighWater(); got != 0 {
		t.Fatalf("lease high-water after RAM admission=%d, want immutable acquisition snapshot 0", got)
	}

	releaseBlocker()
	barrierCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := admission.Barrier(barrierCtx); err != nil {
		t.Fatalf("wait for admitted durable commit: %v", err)
	}
	if got := store.OrchestratorCommittedHighWater(); got != event.EventSeq {
		t.Fatalf("high-water after durable commit=%d, want %d", got, event.EventSeq)
	}
}

func TestCommittedOrchestratorHighWaterCoversBatchAndV2TurnCommitsAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "committed-high-water.sqlite")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	ctx := context.Background()

	batch := []modulecore.EventEnvelope{
		eventFixture(modulecore.NewTraceID(), "migration.orchestrator.first", "orchestrator"),
		eventFixture(modulecore.NewTraceID(), "migration.orchestrator.last", "orchestrator"),
		eventFixture(modulecore.NewTraceID(), "migration.private", "superagent"),
	}
	if err := store.AppendBatch(ctx, batch); err != nil {
		t.Fatalf("AppendBatch migration graph: %v", err)
	}
	var batchOrchestratorHighWater modulecore.EventSeq
	var privateSequence modulecore.EventSeq
	for _, event := range batch {
		stored, found, err := store.GetByID(ctx, event.EventID)
		if err != nil || !found {
			t.Fatalf("GetByID(%s): found=%t err=%v", event.EventID, found, err)
		}
		if event.ComponentID == "orchestrator" && stored.EventSeq > batchOrchestratorHighWater {
			batchOrchestratorHighWater = stored.EventSeq
		}
		if event.ComponentID == "superagent" {
			privateSequence = stored.EventSeq
		}
	}
	if got := store.OrchestratorCommittedHighWater(); got != batchOrchestratorHighWater {
		t.Fatalf("high-water after AppendBatch=%d, want latest orchestrator %d (private seq=%d)", got, batchOrchestratorHighWater, privateSequence)
	}
	if privateSequence <= batchOrchestratorHighWater {
		t.Fatalf("fixture private sequence=%d, want after orchestrator high-water=%d", privateSequence, batchOrchestratorHighWater)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close before restart: %v", err)
	}
	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore after restart: %v", err)
	}
	defer store.Close()
	if got := store.OrchestratorCommittedHighWater(); got != batchOrchestratorHighWater {
		t.Fatalf("startup high-water=%d, want durable orchestrator max %d", got, batchOrchestratorHighWater)
	}

	sessionID, turnID := modulecore.NewSessionID(), modulecore.NewTurnID()
	input := liveInputEvent(sessionID, turnID, "accepted v2 input")
	accepted, err := store.AcceptTurn(ctx, AcceptInput{
		OperationID: "high-water-accept-v2", OwnerID: "user-1", RuntimeVersion: "v2",
		ClientSubmissionID: "high-water-submission", ActorID: "user-1", TriggerKind: "user_message",
		GenerationEpoch: 1, CreateNewSession: true, InputEvent: input,
		InputMessage: MessageIndex{
			MessageID: input.MessageID, EventID: input.EventID, SessionID: sessionID, TurnID: turnID,
			FromActor: "user-1", ToKind: "agent", ToActor: "mio", Kind: "user_message", Visibility: "public",
		},
	})
	if err != nil {
		t.Fatalf("AcceptTurn v2 input: %v", err)
	}
	if got := store.OrchestratorCommittedHighWater(); got != accepted.InputEvent.EventSeq {
		t.Fatalf("high-water after v2 accepted input=%d, want %d", got, accepted.InputEvent.EventSeq)
	}

	reply := eventFixture(input.TraceID, "agent.response", "orchestrator")
	reply.SessionID, reply.TurnID, reply.MessageID = sessionID, turnID, modulecore.NewMessageID()
	reply.CausationEventID = accepted.InputEvent.EventID
	reply.Payload = map[string]any{"from": "mio", "source_actor_id": "mio", "to": "user", "content": "v2 response"}
	committed, err := store.CommitTurnBundle(ctx, CommitInput{
		OperationID: "high-water-commit-v2", SessionID: sessionID, TurnID: turnID,
		GenerationEpoch: 1, ResultState: "completed", Events: []modulecore.EventEnvelope{reply},
		MessageIndexes: []MessageIndex{{
			MessageID: reply.MessageID, EventID: reply.EventID, SessionID: sessionID, TurnID: turnID,
			FromActor: "mio", ToKind: "user", ToActor: "user-1", Kind: "assistant_message", Visibility: "public",
		}},
	})
	if err != nil {
		t.Fatalf("CommitTurnBundle v2 response: %v", err)
	}
	if len(committed.Events) != 1 {
		t.Fatalf("committed v2 event count=%d, want 1", len(committed.Events))
	}
	if got := store.OrchestratorCommittedHighWater(); got != committed.Events[0].EventSeq {
		t.Fatalf("high-water after v2 committed response=%d, want %d", got, committed.Events[0].EventSeq)
	}
}
