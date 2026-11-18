package eventstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestTaskMeetingNativeGenerationAndEightDurableConsumers(t *testing.T) {
	ctx := context.Background()
	eventPath := filepath.Join(t.TempDir(), "meeting.sqlite")
	store, err := NewSQLiteStore(eventPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	taskRoot, err := os.MkdirTemp(".", ".taskmeeting-native-owner-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(taskRoot) })
	tasks, err := taskpersistence.NewJSONLStore(taskRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer tasks.Close()
	manager := taskmanager.New(tasks, taskmanager.DefaultParallelLimits())
	registrationRequest, err := taskMeetingRegistrationFixture(1)
	if err != nil {
		t.Fatal(err)
	}
	registration, err := store.EnsureTaskMeetingScheduleRegistration(ctx, registrationRequest, func(context.Context, domainmeeting.TaskMeetingDeclaration) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	key := domainmeeting.OccurrenceKey{ScheduleID: registration.ScheduleID, LocalDate: "2026-11-18", Slot: "08:00", Timezone: domainmeeting.TimezoneJST}
	frozenConfig, configSHA, err := domainmeeting.CanonicalTaskMeetingDeclarationJSON(registrationRequest.Declaration)
	if err != nil {
		t.Fatal(err)
	}
	frozenRootInput := []byte(`{"operation":"taskmeeting.root"}`)
	inputDigest := sha256.Sum256(frozenRootInput)
	rootRequest := domaintask.TaskMeetingInvocationRequest{
		OccurrenceKey: key.String(), Purpose: domaintask.TaskMeetingPurposeRoot,
		CoordinatorActorID: registrationRequest.Declaration.CoordinatorActorID, ActorID: registrationRequest.Declaration.CoordinatorActorID,
		ConfiguredOwnerUserID: registration.OwnerUserID, ConfigRevision: registration.Revision, RegistrationEvidenceID: registration.RegistrationEvidenceID,
		FrozenConfig: frozenConfig, Audience: append([]string(nil), registrationRequest.Declaration.Audience...), FrozenInput: frozenRootInput,
		Timing: registrationRequest.Declaration.Timing,
	}
	stateAuth := func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error { return nil }
	root, rootRun, rootProof, err := manager.EnsureTaskMeetingRootInvocation(ctx, rootRequest,
		func(context.Context, domaintask.TaskMeetingInvocationRequest, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error {
			return nil
		},
		func(context.Context) error { return nil }, stateAuth)
	if err != nil {
		t.Fatalf("ensure native root: %v", err)
	}
	if rootProof.ConfigSHA256 != configSHA {
		t.Fatal("root Task did not pin exact registration bytes")
	}
	if _, err := store.PinTaskMeetingOccurrenceRegistration(ctx, registration, key, func(_ context.Context, actual TaskMeetingScheduleRegistrationReceipt, got domainmeeting.OccurrenceKey) error {
		if actual.RegistrationEvidenceID != registration.RegistrationEvidenceID || got != key {
			return ErrTaskMeetingForbidden
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	groupRequest := TaskMeetingGroupSessionRequest{OccurrenceKey: key.String(), TaskID: root.TaskID, RunID: rootRun.RunID,
		OwnerUserID: registration.OwnerUserID, CoordinatorActorID: root.Assignee, RegistrationEvidenceID: registration.RegistrationEvidenceID,
		ConfigRevision: registration.Revision, ConfigSHA256: configSHA, InputSHA256: hex.EncodeToString(inputDigest[:]),
		RequestID: "meeting-session:" + key.String(), Audience: append([]string(nil), rootRequest.Audience...)}
	session, err := store.EnsureTaskMeetingGroupSession(ctx, groupRequest, tasks, stateAuth, domaintask.TaskMeetingStateAuthorizationCheck(stateAuth))
	if err != nil {
		t.Fatalf("ensure native Session/Thread: %v", err)
	}
	resolvedSession, found, err := store.FindTaskMeetingGroupSessionForRoot(ctx, key.String(), root.TaskID, rootRun.RunID, tasks, stateAuth)
	if err != nil || !found || resolvedSession.SessionID != session.SessionID || resolvedSession.ThreadID != session.ThreadID {
		t.Fatalf("resolve canonical native Session from root identity: found=%t receipt=%+v err=%v", found, resolvedSession, err)
	}
	contexts := make(map[string]struct {
		task domaintask.Task
		run  domaintask.Run
	}, len(rootRequest.Audience))
	for _, actor := range rootRequest.Audience {
		request := rootRequest
		request.Purpose, request.ActorID = domaintask.TaskMeetingPurposeRecipientContext, actor
		request.ParentTaskID, request.ParentRunID = root.TaskID, rootRun.RunID
		request.FrozenInput = []byte(`{"purpose":"taskmeeting.recipient_context"}`)
		child, childRun, _, ensureErr := manager.EnsureTaskMeetingRecipientContext(ctx, request, rootProof,
			func(context.Context, domaintask.TaskMeetingInvocationRequest, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error {
				return nil
			}, stateAuth,
			func(context.Context) error { return nil }, stateAuth)
		if ensureErr != nil {
			t.Fatalf("ensure context %s: %v", actor, ensureErr)
		}
		contexts[actor] = struct {
			task domaintask.Task
			run  domaintask.Run
		}{child, childRun}
	}
	worldActors := make([]domainmeeting.WorldActorSnapshot, 0, 8)
	for _, binding := range registrationRequest.Declaration.WorldActorBindings {
		worldActors = append(worldActors, domainmeeting.WorldActorSnapshot{WorldActorID: binding.WorldActorID, Present: true, RoomID: registrationRequest.Declaration.Rooms.Living})
	}
	observedAt := time.Now().UTC()
	worldRequest := domainmeeting.WorldObservationRequest{Key: key, TaskID: root.TaskID, RunID: rootRun.RunID,
		ConfigRevision: registration.Revision, ConfigSHA256: configSHA, OwnerUserID: registration.OwnerUserID,
		RegistrationEvidenceID: registration.RegistrationEvidenceID, CoordinatorActorID: root.Assignee, Session: session.SessionID, Thread: session.ThreadID,
		Generation: 1, Epoch: 1, Audience: append([]string(nil), rootRequest.Audience...),
		ActorBindings: append([]domainmeeting.WorldActorBinding(nil), registrationRequest.Declaration.WorldActorBindings...), Rooms: registrationRequest.Declaration.Rooms,
		Snapshot: domainmeeting.WorldSnapshotObservation{WorldID: registrationRequest.Declaration.WorldID, TimelineID: registrationRequest.Declaration.TimelineID,
			TimelineEpoch: registrationRequest.Declaration.TimelineEpoch, StateSeq: 9, StateHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Actors: worldActors},
		ObservedAt: observedAt, ValidUntil: observedAt.Add(time.Hour), RequestID: "world:" + key.String() + ":1:1"}
	world, err := store.RecordTaskMeetingWorldObservation(ctx, worldRequest, tasks, stateAuth,
		func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, domainmeeting.WorldObservationRequest) error {
			return nil
		})
	if err != nil {
		t.Fatalf("record authenticated-source observation: %v", err)
	}
	lookupRequest := worldRequest
	lookupRequest.Snapshot = domainmeeting.WorldSnapshotObservation{}
	lookupRequest.ObservedAt, lookupRequest.ValidUntil = time.Time{}, time.Time{}
	worldReplay, found, err := store.FindTaskMeetingWorldObservation(ctx, lookupRequest, tasks, stateAuth)
	if err != nil || !found || worldReplay.EvidenceID != world.EvidenceID || worldReplay.Scope.EvidenceID != world.Scope.EvidenceID {
		t.Fatalf("receipt-first lookup without WORLD data: found=%t receipt=%+v err=%v", found, worldReplay, err)
	}
	actor := root.Assignee
	contextOwner := contexts[actor]
	_, _, shared, err := tasks.GetTaskMeetingInvocationState(ctx, contextOwner.task.TaskID, contextOwner.run.RunID, stateAuth)
	if err != nil {
		t.Fatal(err)
	}
	sourceHash, err := domaintask.TaskMeetingConsumedMessagesSHA256(shared.TaskMeetingConsumedMessages)
	if err != nil {
		t.Fatal(err)
	}
	scope := domaintask.TaskMeetingGenerationScope{ScopeEvidenceID: world.Scope.EvidenceID, Generation: 1, Epoch: 1}
	frozenInput, err := domaintask.EncodeTaskMeetingGenerationInput(scope, json.RawMessage(`{"kind":"report"}`))
	if err != nil {
		t.Fatal(err)
	}
	generationRequest := rootRequest
	generationRequest.Purpose, generationRequest.ActorID = domaintask.TaskMeetingPurposeReport, actor
	generationRequest.ParentTaskID, generationRequest.ParentRunID = contextOwner.task.TaskID, contextOwner.run.RunID
	generationRequest.GenerationRequestID = modulecore.NewRequestID()
	generationRequest.SourceContextSHA256, generationRequest.GenerationScope = sourceHash, scope
	generationRequest.FrozenInput = frozenInput
	generation, generationRun, err := manager.EnsureTaskMeetingGeneration(ctx, generationRequest, contextOwner.task.TaskID, contextOwner.run.RunID,
		func(context.Context, domaintask.TaskMeetingInvocationRequest, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error {
			return nil
		}, stateAuth,
		func(context.Context) error { return nil }, stateAuth)
	if err != nil {
		t.Fatalf("ensure typed report Task: %v", err)
	}
	resultBody := "A short meeting update."
	var stableMessageID modulecore.MessageID
	resultJSON, _ := json.Marshal(struct {
		Schema  string               `json:"schema"`
		Message modulecore.MessageID `json:"message_id"`
		Body    string               `json:"body"`
		Concern string               `json:"concern"`
	}{"taskmeeting.report.v1", "", resultBody, "What support would help?"})
	effectCalls := 0
	output, err := manager.ExecuteTaskMeetingGeneration(ctx, generation.TaskID, generationRun.RunID, stateAuth, stateAuth, stateAuth,
		func(_ context.Context, task domaintask.Task, run domaintask.Run, _, _ domaintask.SharedRoleContext, input json.RawMessage) (json.RawMessage, error) {
			effectCalls++
			if task.Assignee != actor || run.RunID != generationRun.RunID || string(input) != string(frozenInput) {
				return nil, domaintask.ErrTaskMeetingConflict
			}
			stableMessageID = modulecore.NewMessageID()
			resultJSON, _ = json.Marshal(struct {
				Schema  string               `json:"schema"`
				Message modulecore.MessageID `json:"message_id"`
				Body    string               `json:"body"`
				Concern string               `json:"concern"`
			}{"taskmeeting.report.v1", stableMessageID, resultBody, "What support would help?"})
			return resultJSON, nil
		})
	if err != nil || string(output) != string(resultJSON) {
		t.Fatalf("execute persisted closed effect: %s err=%v", output, err)
	}
	replayedOutput, err := manager.ExecuteTaskMeetingGeneration(ctx, generation.TaskID, generationRun.RunID, stateAuth, stateAuth, stateAuth,
		func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, domaintask.SharedRoleContext, json.RawMessage) (json.RawMessage, error) {
			effectCalls++
			return nil, errors.New("replayed generation invoked effect")
		})
	if err != nil || string(replayedOutput) != string(resultJSON) || effectCalls != 1 {
		t.Fatalf("generation receipt replay output=%s calls=%d err=%v", replayedOutput, effectCalls, err)
	}
	_, resultRef, err := manager.GetTaskMeetingGenerationOutput(ctx, generation.TaskID, generationRun.RunID, stateAuth)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.FinishTaskMeetingInvocation(ctx, generation.TaskID, generationRun.RunID, resultRef, stateAuth,
		func(_ context.Context, task domaintask.Task, run domaintask.Run, _ domaintask.SharedRoleContext, ref domaintask.TaskMeetingGenerationResultReference) error {
			if task.TaskID != generation.TaskID || run.RunID != generationRun.RunID || ref != resultRef {
				return domaintask.ErrTaskMeetingConflict
			}
			return nil
		}); err != nil {
		t.Fatalf("finish generation from persisted result: %v", err)
	}
	messageRequest := TaskMeetingGroupMessageRequest{Session: session, TaskID: generation.TaskID, RunID: generationRun.RunID, ActorID: actor,
		MessageID: stableMessageID, Body: resultBody, GenerationResult: resultRef, AttemptGeneration: 1, AttemptEpoch: 1, ScopeEvidenceID: world.Scope.EvidenceID}
	readMessage := func(_ context.Context, task domaintask.Task, run domaintask.Run, _ domaintask.SharedRoleContext, ref domaintask.TaskMeetingGenerationResultReference, output json.RawMessage, body string, persisted *domaintask.TaskMeetingMessageReference) error {
		var decoded struct {
			Message modulecore.MessageID `json:"message_id"`
			Body    string               `json:"body"`
		}
		_ = json.Unmarshal(output, &decoded)
		if task.TaskID != generation.TaskID || run.RunID != generationRun.RunID || ref != resultRef || decoded.Message != messageRequest.MessageID || decoded.Body != body {
			return domaintask.ErrTaskMeetingConflict
		}
		if persisted != nil && (persisted.MessageID != decoded.Message || persisted.BodySHA256 != taskMeetingBodySHA256(decoded.Body)) {
			return domaintask.ErrTaskMeetingConflict
		}
		return nil
	}
	var sequenceBeforeLookup int64
	if err := store.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(event_seq),0) FROM event_envelope`).Scan(&sequenceBeforeLookup); err != nil {
		t.Fatal(err)
	}
	missingMessage, foundMessage, err := store.FindTaskMeetingGroupMessage(ctx, messageRequest, tasks, manager, stateAuth, readMessage)
	if err != nil || foundMessage {
		t.Fatalf("receipt-first read before publication: receipt=%+v found=%t err=%v", missingMessage, foundMessage, err)
	}
	var sequenceAfterLookup int64
	if err := store.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(event_seq),0) FROM event_envelope`).Scan(&sequenceAfterLookup); err != nil || sequenceAfterLookup != sequenceBeforeLookup {
		t.Fatalf("read-only missing receipt created an Event: before=%d after=%d err=%v", sequenceBeforeLookup, sequenceAfterLookup, err)
	}
	// A queued send may wait long enough for the occurrence to become partial.
	// The send callback is therefore checked again by the queue worker before
	// the EventStore transaction begins.
	queue := &delayedMeetingWriteQueue{enqueued: make(chan func(context.Context) error, 1), submitted: make(chan struct{}), release: make(chan struct{})}
	store.writeQueue = queue
	var allowed atomic.Bool
	allowed.Store(true)
	var sendAuthorizationCalls atomic.Int32
	firstAuthorization := make(chan struct{}, 1)
	var outboxBeforeRejectedSend int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM live_work_outbox`).Scan(&outboxBeforeRejectedSend); err != nil {
		t.Fatal(err)
	}
	queuedResult := make(chan error, 1)
	go func() {
		_, sendErr := store.EnsureTaskMeetingGroupMessage(ctx, messageRequest, tasks, manager, stateAuth,
			func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, domaintask.TaskMeetingGenerationResultReference, json.RawMessage, string) error {
				sendAuthorizationCalls.Add(1)
				firstAuthorization <- struct{}{}
				if !allowed.Load() {
					return domaintask.ErrTaskMeetingOwnerRequired
				}
				return nil
			})
		queuedResult <- sendErr
	}()
	select {
	case <-firstAuthorization:
	case sendErr := <-queuedResult:
		t.Fatalf("queued send returned before authorization callback: %v", sendErr)
	case <-time.After(time.Second):
		t.Fatal("queued send did not complete its initial authorization")
	}
	select {
	case <-queue.submitted:
	case <-time.After(time.Second):
		t.Fatal("send command was not queued")
	}
	allowed.Store(false)
	close(queue.release)
	if err := <-queuedResult; !errors.Is(err, ErrTaskMeetingForbidden) {
		t.Fatalf("queued send after live occurrence revocation = %v, want forbidden", err)
	}
	store.writeQueue = nil
	var sequenceAfterRejectedSend int64
	if err := store.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(event_seq),0) FROM event_envelope`).Scan(&sequenceAfterRejectedSend); err != nil || sequenceAfterRejectedSend != sequenceBeforeLookup {
		t.Fatalf("revoked queued send wrote Event sequence: before=%d after=%d err=%v", sequenceBeforeLookup, sequenceAfterRejectedSend, err)
	}
	var outboxAfterRejectedSend int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM live_work_outbox`).Scan(&outboxAfterRejectedSend); err != nil || outboxAfterRejectedSend != outboxBeforeRejectedSend {
		t.Fatalf("revoked queued send wrote outbox: before=%d after=%d err=%v", outboxBeforeRejectedSend, outboxAfterRejectedSend, err)
	}
	if calls := sendAuthorizationCalls.Load(); calls != 2 {
		t.Fatalf("send authorizer calls=%d, want initial and queued-execution checks", calls)
	}
	message, err := store.EnsureTaskMeetingGroupMessage(ctx, messageRequest, tasks, manager, stateAuth,
		func(_ context.Context, task domaintask.Task, run domaintask.Run, _ domaintask.SharedRoleContext, ref domaintask.TaskMeetingGenerationResultReference, output json.RawMessage, body string) error {
			var decoded struct {
				Message modulecore.MessageID `json:"message_id"`
				Body    string               `json:"body"`
			}
			_ = json.Unmarshal(output, &decoded)
			if task.TaskID != generation.TaskID || run.RunID != generationRun.RunID || ref != resultRef || decoded.Message != messageRequest.MessageID || decoded.Body != body {
				return domaintask.ErrTaskMeetingConflict
			}
			return nil
		})
	if err != nil {
		t.Fatalf("persist canonical EventStore body: %v", err)
	}
	messageReplay, err := store.EnsureTaskMeetingGroupMessage(ctx, messageRequest, tasks, manager, stateAuth, nil)
	if err != nil || messageReplay.Reference != message.Reference || messageReplay.EvidenceID != message.EvidenceID {
		t.Fatalf("canonical message replay changed: first=%+v retry=%+v err=%v", message, messageReplay, err)
	}
	messageReadback, messageFound, err := store.FindTaskMeetingGroupMessage(ctx, messageRequest, tasks, manager, stateAuth, readMessage)
	if err != nil || !messageFound || messageReadback.Reference != message.Reference || messageReadback.EvidenceID != message.EvidenceID {
		t.Fatalf("read-only canonical message receipt changed: receipt=%+v found=%t err=%v", messageReadback, messageFound, err)
	}
	if _, _, err := store.FindTaskMeetingGroupMessage(ctx, messageRequest, tasks, manager, stateAuth,
		func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, domaintask.TaskMeetingGenerationResultReference, json.RawMessage, string, *domaintask.TaskMeetingMessageReference) error {
			return domaintask.ErrTaskMeetingOwnerRequired
		}); !errors.Is(err, ErrTaskMeetingForbidden) {
		t.Fatalf("revoked message read scope did not block receipt: %v", err)
	}
	wrongMessageID := messageRequest
	wrongMessageID.MessageID = modulecore.NewMessageID()
	if _, err := store.EnsureTaskMeetingGroupMessage(ctx, wrongMessageID, tasks, manager, stateAuth,
		func(_ context.Context, _ domaintask.Task, _ domaintask.Run, _ domaintask.SharedRoleContext, _ domaintask.TaskMeetingGenerationResultReference, output json.RawMessage, _ string) error {
			var decoded struct {
				Message modulecore.MessageID `json:"message_id"`
			}
			_ = json.Unmarshal(output, &decoded)
			if decoded.Message != wrongMessageID.MessageID {
				return domaintask.ErrTaskMeetingConflict
			}
			return nil
		}); !errors.Is(err, ErrTaskMeetingForbidden) {
		t.Fatalf("alternate message ID accepted for frozen native output: %v", err)
	}
	wrong := messageRequest
	wrong.ActorID = "outsider"
	if _, err := store.EnsureTaskMeetingGroupMessage(ctx, wrong, tasks, manager, stateAuth, nil); !errors.Is(err, ErrTaskMeetingForbidden) {
		t.Fatalf("wrong actor accepted: %v", err)
	}
	changedBody := messageRequest
	changedBody.Body += " changed"
	if _, err := store.EnsureTaskMeetingGroupMessage(ctx, changedBody, tasks, manager, stateAuth,
		func(_ context.Context, _ domaintask.Task, _ domaintask.Run, _ domaintask.SharedRoleContext, _ domaintask.TaskMeetingGenerationResultReference, _ json.RawMessage, _ string) error {
			return domaintask.ErrTaskMeetingConflict
		}); !errors.Is(err, ErrTaskMeetingConflict) {
		t.Fatalf("body not matching persisted Task output accepted: %v", err)
	}

	for _, recipient := range rootRequest.Audience {
		entry := contexts[recipient]
		claim, err := store.ClaimTaskMeetingGroupDelivery(ctx, recipient, WorkClaimInput{Limit: 1, LeaseDuration: time.Minute, Assignee: recipient},
			func(checkCtx context.Context, actualActor string, ref domaintask.TaskMeetingMessageReference) error {
				if actualActor != recipient || ref != message.Reference {
					return ErrTaskMeetingForbidden
				}
				_, _, _, readErr := tasks.GetTaskMeetingInvocationState(checkCtx, entry.task.TaskID, entry.run.RunID, stateAuth)
				return readErr
			})
		if err != nil || len(claim) != 1 {
			t.Fatalf("claim for %s: count=%d err=%v", recipient, len(claim), err)
		}
		lease := WorkLeaseInput{WorkID: claim[0].Work.WorkID, LeaseToken: claim[0].Work.LeaseToken, Version: claim[0].Work.Version}
		if err := store.AckTaskMeetingGroupDelivery(ctx, lease); !errors.Is(err, ErrWorkResultNotPersisted) {
			t.Fatalf("premature ack for %s: %v", recipient, err)
		}
		authorizeRecipient := func(_ context.Context, task domaintask.Task, run domaintask.Run, shared domaintask.SharedRoleContext, ref domaintask.TaskMeetingMessageReference) error {
			if task.TaskID != entry.task.TaskID || run.RunID != entry.run.RunID || task.Assignee != recipient || shared.TaskID != entry.task.TaskID || ref != message.Reference {
				return ErrTaskMeetingForbidden
			}
			return nil
		}
		body, err := store.ReadTaskMeetingGroupMessage(ctx, message.Reference, recipient, entry.task.TaskID, entry.run.RunID, tasks, authorizeRecipient)
		if err != nil || body != resultBody {
			t.Fatalf("authorized recipient body read %s: body=%q err=%v", recipient, body, err)
		}
		proof, err := manager.AppendTaskMeetingConsumedMessage(ctx, entry.task.TaskID, entry.run.RunID, message.Reference, body, authorizeRecipient)
		if err != nil {
			t.Fatalf("persist TaskManager consumption %s: %v", recipient, err)
		}
		proofReplay, err := manager.AppendTaskMeetingConsumedMessage(ctx, entry.task.TaskID, entry.run.RunID, message.Reference, body, authorizeRecipient)
		if err != nil || proofReplay.EvidenceID != proof.EvidenceID {
			t.Fatalf("TaskManager consumption replay %s: first=%+v retry=%+v err=%v", recipient, proof, proofReplay, err)
		}
		consumption, err := store.AppendTaskMeetingGroupConsumption(ctx, proof, tasks, manager, authorizeRecipient)
		if err != nil {
			t.Fatalf("persist EventStore consumption %s: %v", recipient, err)
		}
		consumptionReplay, err := store.AppendTaskMeetingGroupConsumption(ctx, proof, tasks, manager, authorizeRecipient)
		if err != nil || consumptionReplay.EventID != consumption.EventID || consumptionReplay.EvidenceID != consumption.EvidenceID {
			t.Fatalf("EventStore consumption replay %s changed receipt: first=%+v retry=%+v err=%v", recipient, consumption, consumptionReplay, err)
		}
		if err := store.AckTaskMeetingGroupDelivery(ctx, lease); err != nil {
			t.Fatalf("ack durable consumption %s: %v", recipient, err)
		}
	}
	consumptionAuth := func(_ context.Context, task domaintask.Task, run domaintask.Run, shared domaintask.SharedRoleContext, ref domaintask.TaskMeetingMessageReference) error {
		if task.TaskMeetingInvocation == nil || task.Assignee != task.TaskMeetingInvocation.ActorID || run.TaskID != task.TaskID || shared.TaskID != task.TaskID || ref != message.Reference {
			return ErrTaskMeetingForbidden
		}
		return nil
	}
	delivery, found, err := store.ResolveTaskMeetingGroupDeliveryReceipt(ctx, message.Reference, manager, consumptionAuth)
	if err != nil || !found || len(delivery.ConsumptionProofs) != 8 {
		t.Fatalf("resolve eight persisted proofs: found=%t proofs=%d err=%v", found, len(delivery.ConsumptionProofs), err)
	}
	latest := time.Time{}
	for _, proof := range delivery.ConsumptionProofs {
		if proof.AcceptedAt.After(latest) {
			latest = proof.AcceptedAt
		}
	}
	if !delivery.Evidence.ObservedAt.Equal(latest) {
		t.Fatalf("canonical delivery ObservedAt=%s, latest acceptance=%s", delivery.Evidence.ObservedAt, latest)
	}
	// A second actor can issue a separate answer generation against the exact
	// accepted message context. This is a new owner receipt, not a replay of the
	// coordinator's report Task.
	answerActor := "shiro"
	answerContext := contexts[answerActor]
	_, _, answerShared, err := tasks.GetTaskMeetingInvocationState(ctx, answerContext.task.TaskID, answerContext.run.RunID, stateAuth)
	if err != nil {
		t.Fatal(err)
	}
	answerContextHash, err := domaintask.TaskMeetingContextSHA256(answerShared)
	if err != nil {
		t.Fatal(err)
	}
	concernID := domainmeeting.ConcernID{ReportMessageID: message.Reference.MessageID, Ordinal: 1}
	answerPayload, _ := json.Marshal(struct {
		Purpose   string `json:"purpose"`
		ConcernID string `json:"concern_id"`
	}{"answer", concernID.String()})
	answerInput, err := domaintask.EncodeTaskMeetingGenerationInput(scope, answerPayload)
	if err != nil {
		t.Fatal(err)
	}
	answerRequest := rootRequest
	answerRequest.Purpose, answerRequest.ActorID = domaintask.TaskMeetingPurposeAnswer, answerActor
	answerRequest.GenerationRequestID = modulecore.NewRequestID()
	answerRequest.GenerationScope, answerRequest.SourceContextSHA256 = scope, answerContextHash
	answerRequest.FrozenSourceMessages = append([]domaintask.TaskMeetingConsumedMessage(nil), answerShared.TaskMeetingConsumedMessages...)
	answerRequest.FrozenInput = answerInput
	answerRequest.ParentTaskID, answerRequest.ParentRunID = answerContext.task.TaskID, answerContext.run.RunID
	answerTask, answerRun, err := manager.EnsureTaskMeetingGeneration(ctx, answerRequest, answerContext.task.TaskID, answerContext.run.RunID,
		func(context.Context, domaintask.TaskMeetingInvocationRequest, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error {
			return nil
		}, stateAuth,
		func(context.Context) error { return nil }, stateAuth)
	if err != nil {
		t.Fatalf("ensure second actor's concern answer Task: %v", err)
	}
	answerOutput, err := manager.ExecuteTaskMeetingGeneration(ctx, answerTask.TaskID, answerRun.RunID, stateAuth, stateAuth, stateAuth,
		func(_ context.Context, task domaintask.Task, run domaintask.Run, _, _ domaintask.SharedRoleContext, input json.RawMessage) (json.RawMessage, error) {
			if task.Assignee != answerActor || run.RunID != answerRun.RunID || string(input) != string(answerInput) {
				return nil, domaintask.ErrTaskMeetingConflict
			}
			return json.RawMessage(`{"schema":"taskmeeting.answer.v1","body":"I can help with the next step."}`), nil
		})
	if err != nil || !json.Valid(answerOutput) {
		t.Fatalf("execute answer generation from accepted context: output=%s err=%v", answerOutput, err)
	}
	_, answerResult, err := manager.GetTaskMeetingGenerationOutput(ctx, answerTask.TaskID, answerRun.RunID, stateAuth)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.FinishTaskMeetingInvocation(ctx, answerTask.TaskID, answerRun.RunID, answerResult, stateAuth,
		func(_ context.Context, task domaintask.Task, run domaintask.Run, _ domaintask.SharedRoleContext, ref domaintask.TaskMeetingGenerationResultReference) error {
			if task.TaskID != answerTask.TaskID || run.RunID != answerRun.RunID || ref != answerResult {
				return domaintask.ErrTaskMeetingConflict
			}
			return nil
		}); err != nil {
		t.Fatalf("finish answer from persisted Task output: %v", err)
	}
	replay, found, err := store.ResolveTaskMeetingGroupDeliveryReceipt(ctx, message.Reference, manager, consumptionAuth)
	if err != nil || !found || replay.EventID != delivery.EventID || !replay.Evidence.ObservedAt.Equal(delivery.Evidence.ObservedAt) {
		t.Fatalf("delivery replay changed: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewSQLiteStore(eventPath)
	if err != nil {
		t.Fatalf("reopen EventStore: %v", err)
	}
	defer reopened.Close()
	messageAfterRestart, err := reopened.EnsureTaskMeetingGroupMessage(ctx, messageRequest, tasks, manager, stateAuth, nil)
	if err != nil || messageAfterRestart.Reference != message.Reference || messageAfterRestart.EvidenceID != message.EvidenceID {
		t.Fatalf("message replay after restart changed: %v", err)
	}
	deliveryAfterRestart, found, err := reopened.ResolveTaskMeetingGroupDeliveryReceipt(ctx, message.Reference, manager, consumptionAuth)
	if err != nil || !found || deliveryAfterRestart.EventID != delivery.EventID || deliveryAfterRestart.Evidence.EvidenceID != delivery.Evidence.EvidenceID || !deliveryAfterRestart.Evidence.ObservedAt.Equal(delivery.Evidence.ObservedAt) {
		t.Fatalf("delivery replay after restart changed: found=%t receipt=%+v err=%v", found, deliveryAfterRestart, err)
	}
}

type delayedMeetingWriteQueue struct {
	enqueued  chan func(context.Context) error
	submitted chan struct{}
	release   chan struct{}
}

func (q *delayedMeetingWriteQueue) Enqueue(ctx context.Context, _ int64, apply func(context.Context) error) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	q.enqueued <- apply
	close(q.submitted)
	return 1, nil
}

func (q *delayedMeetingWriteQueue) Wait(ctx context.Context, _ uint64) error {
	var apply func(context.Context) error
	select {
	case apply = <-q.enqueued:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-q.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return apply(context.Background())
}
