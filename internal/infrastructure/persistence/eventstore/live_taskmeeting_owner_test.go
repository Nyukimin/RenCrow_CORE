package eventstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type taskMeetingStateReaderStub struct {
	task   domaintask.Task
	run    domaintask.Run
	shared domaintask.SharedRoleContext
}

func (reader taskMeetingStateReaderStub) GetTaskMeetingInvocationState(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, authorize domaintask.TaskMeetingReadAuthorizationCheck) (domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, error) {
	if reader.task.TaskID != taskID || reader.run.RunID != runID || reader.run.TaskID != taskID {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, domaintask.ErrTaskMeetingNotFound
	}
	if authorize != nil {
		if err := authorize(ctx, reader.task, reader.run, reader.shared); err != nil {
			return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, err
		}
	}
	return reader.task, reader.run, reader.shared, nil
}

func taskMeetingRootStateFixture(t *testing.T, request TaskMeetingRegistrationRequest, receipt TaskMeetingScheduleRegistrationReceipt) taskMeetingStateReaderStub {
	t.Helper()
	configBytes, configSHA, err := domainmeeting.CanonicalTaskMeetingDeclarationJSON(request.Declaration)
	if err != nil || configSHA != receipt.ConfigSHA256 {
		t.Fatalf("canonical registration config = %s, %v; want %s", configSHA, err, receipt.ConfigSHA256)
	}
	input := []byte(`{"occurrence":"frozen"}`)
	inputDigest := sha256.Sum256(input)
	audienceHash, err := domaintask.TaskMeetingAudienceSHA256(request.Declaration.Audience)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	timing := request.Declaration.Timing
	task := domaintask.Task{
		TaskID: taskID, Assignee: request.Declaration.CoordinatorActorID, Status: domaintask.StatusRunning,
		ReadOnly: true, Timing: &timing, CreatedAt: now, UpdatedAt: now,
		TaskMeetingInvocation: &domaintask.TaskMeetingInvocation{
			OccurrenceKey: "", Purpose: domaintask.TaskMeetingPurposeRoot,
			CoordinatorActorID: request.Declaration.CoordinatorActorID, ActorID: request.Declaration.CoordinatorActorID,
			ConfiguredOwnerUserID: request.Declaration.OwnerUserID, ConfigRevision: request.Declaration.Revision,
			ConfigSHA256: receipt.ConfigSHA256, RegistrationEvidenceID: receipt.RegistrationEvidenceID,
			FrozenConfig: configBytes, Audience: append([]string(nil), request.Declaration.Audience...), AudienceSHA256: audienceHash,
			InputSHA256: hex.EncodeToString(inputDigest[:]), FrozenInput: input, AdmissionEvidenceID: modulecore.NewEvidenceID(), Timing: timing,
		},
	}
	run := domaintask.Run{RunID: runID, TaskID: taskID, StartReason: domaintask.RunStartReasonFirst, Assignee: task.Assignee, Status: domaintask.RunStatusRunning, StartedAt: now}
	return taskMeetingStateReaderStub{task: task, run: run}
}

func TestTaskMeetingGroupSessionBindsRegistrationAndReplaysAfterRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "task-meeting-group.sqlite")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	registrationRequest, err := taskMeetingRegistrationFixture(1)
	if err != nil {
		t.Fatal(err)
	}
	currentAuth := func(context.Context, domainmeeting.TaskMeetingDeclaration) error { return nil }
	registration, err := store.EnsureTaskMeetingScheduleRegistration(ctx, registrationRequest, currentAuth)
	if err != nil {
		t.Fatalf("register meeting: %v", err)
	}
	reader := taskMeetingRootStateFixture(t, registrationRequest, registration)
	key := domainmeeting.OccurrenceKey{ScheduleID: registration.ScheduleID, LocalDate: "2026-11-18", Slot: "08:00", Timezone: domainmeeting.TimezoneJST}
	reader.task.TaskMeetingInvocation.OccurrenceKey = key.String()
	currentPinAuth := func(_ context.Context, source TaskMeetingScheduleRegistrationReceipt, occurrence domainmeeting.OccurrenceKey) error {
		if source.RegistrationEvidenceID != registration.RegistrationEvidenceID || occurrence != key {
			return ErrTaskMeetingForbidden
		}
		return nil
	}
	pin, err := store.PinTaskMeetingOccurrenceRegistration(ctx, registration, key, currentPinAuth)
	if err != nil || pin.ScheduleID != registration.ScheduleID || pin.ConfigRevision != registration.Revision || pin.RegistrationEvidenceID != registration.RegistrationEvidenceID {
		t.Fatalf("PinTaskMeetingOccurrenceRegistration = %#v, %v", pin, err)
	}
	pinRetry, err := store.PinTaskMeetingOccurrenceRegistration(ctx, registration, key, currentPinAuth)
	if err != nil || pinRetry.PinEventID != pin.PinEventID || pinRetry.PinnedAt != pin.PinnedAt {
		t.Fatalf("exact occurrence pin retry = %#v, %v; want stable pin receipt", pinRetry, err)
	}
	request := TaskMeetingGroupSessionRequest{
		OccurrenceKey: key.String(), TaskID: reader.task.TaskID, RunID: reader.run.RunID,
		OwnerUserID: registration.OwnerUserID, CoordinatorActorID: registrationRequest.Declaration.CoordinatorActorID,
		RegistrationEvidenceID: registration.RegistrationEvidenceID, ConfigRevision: registration.Revision,
		ConfigSHA256: registration.ConfigSHA256, InputSHA256: reader.task.TaskMeetingInvocation.InputSHA256,
		RequestID: "meeting-session:" + key.String(), Audience: append([]string(nil), registrationRequest.Declaration.Audience...),
	}
	readAuthorization := func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error { return nil }
	createAuthorization := func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error { return nil }
	opened, err := store.EnsureTaskMeetingGroupSession(ctx, request, reader, readAuthorization, createAuthorization)
	if err != nil {
		t.Fatalf("EnsureTaskMeetingGroupSession: %v", err)
	}
	if opened.SessionID.Validate() != nil || opened.ThreadID.Validate() != nil || opened.TaskID != reader.task.TaskID || opened.RunID != reader.run.RunID || opened.AdmissionEvidence != reader.task.TaskMeetingInvocation.AdmissionEvidenceID {
		t.Fatalf("group session receipt did not bind issued identities: %#v", opened)
	}
	retry, err := store.EnsureTaskMeetingGroupSession(ctx, request, reader, readAuthorization, nil)
	if err != nil || retry.SessionID != opened.SessionID || retry.ThreadID != opened.ThreadID || retry.OpenedEventID != opened.OpenedEventID || retry.EvidenceID != opened.EvidenceID {
		t.Fatalf("exact session retry = %#v, %v; want one identical mapping", retry, err)
	}

	now := time.Now().UTC()
	worldActors := make([]domainmeeting.WorldActorSnapshot, len(registrationRequest.Declaration.WorldActorBindings))
	for index, binding := range registrationRequest.Declaration.WorldActorBindings {
		worldActors[index] = domainmeeting.WorldActorSnapshot{WorldActorID: binding.WorldActorID, Present: true, RoomID: registrationRequest.Declaration.Rooms.Living}
	}
	worldActors[7].RoomID = "room-bedroom"
	worldRequest := domainmeeting.WorldObservationRequest{
		Key: key, TaskID: reader.task.TaskID, RunID: reader.run.RunID,
		ConfigRevision: registration.Revision, ConfigSHA256: registration.ConfigSHA256, OwnerUserID: registration.OwnerUserID,
		RegistrationEvidenceID: registration.RegistrationEvidenceID, CoordinatorActorID: registrationRequest.Declaration.CoordinatorActorID,
		Session: opened.SessionID, Thread: opened.ThreadID, Generation: 1, Epoch: 1,
		Audience:      append([]string(nil), registrationRequest.Declaration.Audience...),
		ActorBindings: append([]domainmeeting.WorldActorBinding(nil), registrationRequest.Declaration.WorldActorBindings...),
		Rooms:         registrationRequest.Declaration.Rooms,
		Snapshot:      domainmeeting.WorldSnapshotObservation{WorldID: registrationRequest.Declaration.WorldID, TimelineID: registrationRequest.Declaration.TimelineID, TimelineEpoch: registrationRequest.Declaration.TimelineEpoch, StateSeq: 7, StateHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Actors: worldActors},
		ObservedAt:    now, ValidUntil: now.Add(30 * time.Minute), RequestID: "world-read:" + key.String() + ":1:1",
	}
	authorizeObserve := func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, domainmeeting.WorldObservationRequest) error {
		return nil
	}
	worldReceipt, err := store.RecordTaskMeetingWorldObservation(ctx, worldRequest, reader, readAuthorization, authorizeObserve)
	if err != nil {
		t.Fatalf("RecordTaskMeetingWorldObservation: %v", err)
	}
	if worldReceipt.Scope.Snapshot.OwnerProofID != worldReceipt.EvidenceID || len(worldReceipt.Presence) != 8 || worldReceipt.Presence[7].Present || worldReceipt.Presence[7].RoomRef != "" || worldReceipt.Presence[0].Present != true {
		t.Fatalf("WORLD venue evidence = %#v; want exact snapshot ref and room-derived presence", worldReceipt)
	}
	var worldEventID string
	var worldEnvelopeJSON string
	if err := store.db.QueryRowContext(ctx, `SELECT event_id,envelope_json FROM event_envelope WHERE component_id=? AND event_type=? AND json_extract(envelope_json,'$.evidence_id')=?`, taskMeetingComponent, taskMeetingWorldEvent, string(worldReceipt.EvidenceID)).Scan(&worldEventID, &worldEnvelopeJSON); err != nil {
		t.Fatal(err)
	}
	var worldEvent modulecore.EventEnvelope
	if err := json.Unmarshal([]byte(worldEnvelopeJSON), &worldEvent); err != nil {
		t.Fatal(err)
	}
	snapshotBytes, err := json.Marshal(worldEvent.Payload["world_snapshot"])
	if err != nil {
		t.Fatal(err)
	}
	var storedSnapshot domainmeeting.WorldSnapshotObservation
	if err := json.Unmarshal(snapshotBytes, &storedSnapshot); err != nil || storedSnapshot.Actors[7].Present != true || storedSnapshot.Actors[7].RoomID != "room-bedroom" {
		t.Fatalf("stored raw World snapshot = %#v, %v; owner receipt must preserve non-venue source position", storedSnapshot, err)
	}
	if _, found, err := store.GetByID(ctx, modulecore.EventID(worldEventID)); err != nil || found {
		t.Fatalf("generic event reader exposed TaskMeeting WORLD observation: found=%t err=%v", found, err)
	}
	worldReplay, found, err := store.FindTaskMeetingWorldObservation(ctx, worldRequest, reader, readAuthorization)
	if err != nil || !found || worldReplay.EvidenceID != worldReceipt.EvidenceID || worldReplay.Scope.EvidenceID != worldReceipt.Scope.EvidenceID {
		t.Fatalf("exact World observation replay = %#v, found=%t err=%v", worldReplay, found, err)
	}
	changedWorld := worldRequest
	changedWorld.Snapshot.StateSeq++
	if _, err := store.RecordTaskMeetingWorldObservation(ctx, changedWorld, reader, readAuthorization, authorizeObserve); !errors.Is(err, ErrTaskMeetingConflict) {
		t.Fatalf("changed World snapshot under same request key error = %v, want conflict", err)
	}
	if _, found, err := store.GetByID(ctx, opened.OpenedEventID); err != nil || found {
		t.Fatalf("generic event reader exposed TaskMeeting open event: found=%t err=%v", found, err)
	}
	var traceID string
	if err := store.db.QueryRowContext(ctx, `SELECT trace_id FROM event_envelope WHERE event_id = ?`, string(opened.OpenedEventID)).Scan(&traceID); err != nil {
		t.Fatal(err)
	}
	if events, err := store.ListByTraceID(ctx, modulecore.TraceID(traceID), 10); err != nil || len(events) != 0 {
		t.Fatalf("generic trace reader exposed TaskMeeting event: events=%d err=%v", len(events), err)
	}
	if events, _, err := store.ReadComponentPage(ctx, taskMeetingComponent, 0, 0, 10); err != nil || len(events) != 0 {
		t.Fatalf("generic component replay exposed TaskMeeting event: events=%d err=%v", len(events), err)
	}
	if _, err := store.ReadCompleteSessionTail(ctx, opened.SessionID, 0); !errors.Is(err, ErrTaskMeetingForbidden) {
		t.Fatalf("generic session tail error = %v, want TaskMeeting denial", err)
	}
	if _, err := store.ReadLegacySessionProjection(ctx, opened.SessionID, registration.OwnerUserID, 0); !errors.Is(err, ErrTaskMeetingForbidden) {
		t.Fatalf("legacy session projection error = %v, want TaskMeeting denial", err)
	}
	if events, _, err := store.ReadSessionPublicMessagesPage(ctx, opened.SessionID, registration.OwnerUserID, "mio", 0, 0, 10); err == nil || events != nil {
		t.Fatalf("public v2 reader accepted TaskMeeting session: events=%#v err=%v", events, err)
	}
	if event, found, err := store.ReadSessionPublicMessageByEventID(ctx, opened.SessionID, registration.OwnerUserID, "mio", opened.OpenedEventID, 1); err != nil || found || event.EventID != "" {
		t.Fatalf("public SSE event reader exposed TaskMeeting event: %#v found=%t err=%v", event, found, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(reopen): %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	recovered, found, err := reopened.FindTaskMeetingGroupSession(ctx, request, reader, readAuthorization)
	if err != nil || !found || recovered.SessionID != opened.SessionID || recovered.ThreadID != opened.ThreadID || recovered.OpenedEventID != opened.OpenedEventID {
		t.Fatalf("reopened session recovery = %#v, found=%t err=%v", recovered, found, err)
	}
	recoveredPin, found, err := reopened.FindTaskMeetingOccurrenceRegistration(ctx, key, registration.OwnerUserID, func(context.Context, string, string) error { return nil })
	if err != nil || !found || recoveredPin.PinEventID != pin.PinEventID || recoveredPin.ConfigRevision != registration.Revision {
		t.Fatalf("reopened occurrence registration pin = %#v, found=%t err=%v", recoveredPin, found, err)
	}
	nextDeclaration, err := taskMeetingRegistrationFixture(4)
	if err != nil {
		t.Fatal(err)
	}
	nextDeclaration.Declaration.TimelineEpoch = "epoch-next"
	nextDeclaration.FrozenDeclaration, _, err = domainmeeting.CanonicalTaskMeetingDeclarationJSON(nextDeclaration.Declaration)
	if err != nil {
		t.Fatal(err)
	}
	nextRegistration, err := reopened.EnsureTaskMeetingScheduleRegistration(ctx, nextDeclaration, currentAuth)
	if err != nil || nextRegistration.ScheduleID != registration.ScheduleID {
		t.Fatalf("next registration revision = %#v, %v", nextRegistration, err)
	}
	if _, err := reopened.PinTaskMeetingOccurrenceRegistration(ctx, nextRegistration, key, func(context.Context, TaskMeetingScheduleRegistrationReceipt, domainmeeting.OccurrenceKey) error {
		return nil
	}); !errors.Is(err, ErrTaskMeetingConflict) {
		t.Fatalf("attempt to rebind frozen occurrence error = %v, want conflict", err)
	}

	wrongEvidence := request
	wrongEvidence.RegistrationEvidenceID = modulecore.NewEvidenceID()
	if _, found, err := reopened.FindTaskMeetingGroupSession(ctx, wrongEvidence, reader, readAuthorization); found || !errors.Is(err, ErrTaskMeetingForbidden) {
		t.Fatalf("changed registration evidence lookup = found %t, err %v; want denied", found, err)
	}
}
