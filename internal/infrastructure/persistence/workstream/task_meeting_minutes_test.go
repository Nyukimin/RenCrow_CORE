package workstream

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	tool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestTaskMeetingMinutesArtifactsAndAnnotationsStayOutOfGenericReaders(t *testing.T) {
	ctx := context.Background()
	store := taskPlanTestStore(t, filepath.Join(t.TempDir(), "minutes-private.db"))
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	artifact := domainworkstream.Artifact{ArtifactID: "artifact-private-meeting-minutes", WorkstreamID: "ws-private-meeting", Type: taskMeetingMinutesArtifactType, Status: "stored", CreatedAt: now}
	seedSQLiteMeetingMinutesArtifact(t, store, artifact)
	annotation := domainworkstream.ArtifactAnnotation{AnnotationID: "annotation-private-meeting-minutes", ArtifactID: artifact.ArtifactID, Comment: "private transcript note", Status: "open", CreatedAt: now}
	annotationJSON, err := json.Marshal(annotation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO artifact_annotation (annotation_id, artifact_id, created_at, payload) VALUES (?, ?, ?, ?)`, annotation.AnnotationID, annotation.ArtifactID, now.Format(timeFormatRFC3339Nano), string(annotationJSON)); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveArtifactAnnotation(ctx, annotation); !errors.Is(err, domainworkstream.ErrTaskPlanForbidden) {
		t.Fatalf("generic annotation write error = %v, want forbidden", err)
	}
	artifacts, err := store.ListArtifacts(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range artifacts {
		if item.ArtifactID == artifact.ArtifactID {
			t.Fatalf("generic artifact list exposed private minutes: %#v", item)
		}
	}
	annotations, err := store.ListArtifactAnnotations(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range annotations {
		if item.ArtifactID == artifact.ArtifactID {
			t.Fatalf("generic annotation list exposed private minutes annotation: %#v", item)
		}
	}
}

func TestTaskMeetingMinutesKeepsPartialOutputsAndMissedOutcomesHonest(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	audience := []string{"mio", "shiro", "aka", "ao", "kin", "gin", "midori", "kuro"}
	messageID := modulecore.NewMessageID()
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	partial := TaskMeetingMinutesTranscript{
		WorkstreamID: modulecore.NewWorkstreamID(), OwnerUserID: "owner-ren", CoordinatorActorID: "mio",
		OccurrenceKey:          domainmeeting.OccurrenceKey{ScheduleID: modulecore.NewScheduleID(), LocalDate: "2026-11-18", Slot: "08:00", Timezone: domainmeeting.TimezoneJST},
		TranscriptRequestID:    "partial-request",
		RegistrationEvidenceID: modulecore.NewEvidenceID(), ConfigRevision: 3, ConfigSHA256: strings.Repeat("a", 64), Audience: audience,
		Guard: domainmeeting.Guard{Generation: 1, Epoch: 1, ProgressionRevision: 5}, ScheduledAt: now,
		Outcome: domainmeeting.StatusPartial, ReasonCode: "deadline_elapsed", Unresolved: []string{"report_delivery:mio"}, MessageOrder: []modulecore.MessageID{messageID},
		Reports:  []TaskMeetingMinutesReport{{MessageID: messageID, ActorID: "mio", Body: "generated but not delivered", SpeakerEvidenceID: modulecore.NewEvidenceID(), SpeakerEvidenceSHA: strings.Repeat("b", 64), SourceTaskID: taskID, SourceRunID: runID}},
		Messages: []TaskMeetingMinutesMessageProof{{DeliveryState: domainmeeting.DeliveryUnknown, MessageID: messageID, SenderActorID: "mio", SenderTaskID: taskID, SenderRunID: runID}},
	}
	if err := validateTaskMeetingMinutesTranscript(partial); err != nil {
		t.Fatalf("honest partial generation transcript rejected: %v", err)
	}
	completed := partial
	completed.Outcome = domainmeeting.StatusCompleted
	completed.ReasonCode = ""
	completed.Unresolved = nil
	if err := validateTaskMeetingMinutesTranscript(completed); !errors.Is(err, domainworkstream.ErrTaskPlanUnavailable) {
		t.Fatalf("partial generation relabeled completed: %v", err)
	}
	missed := TaskMeetingMinutesTranscript{
		WorkstreamID: modulecore.NewWorkstreamID(), OwnerUserID: "owner-ren", CoordinatorActorID: "mio",
		OccurrenceKey: partial.OccurrenceKey, TranscriptRequestID: "missed-request", RegistrationEvidenceID: modulecore.NewEvidenceID(), ConfigRevision: 3, ConfigSHA256: strings.Repeat("a", 64),
		Audience: append([]string(nil), audience...), Guard: domainmeeting.Guard{Generation: 1, Epoch: 1, ProgressionRevision: 2}, ScheduledAt: now,
		Outcome: domainmeeting.StatusMissed, ReasonCode: "missed",
	}
	if err := validateTaskMeetingMinutesTranscript(missed); err != nil {
		t.Fatalf("zero-message missed transcript rejected: %v", err)
	}
}

func TestTaskMeetingMissedMinutesPersistReadbackAndReplayUseRealArtifact(t *testing.T) {
	ctx, access, _, key, meeting := meetingStoreFixture(t)
	store := taskPlanTestStore(t, filepath.Join(t.TempDir(), "meeting-missed-minutes.db"))
	nativeTaskID, nativeRunID, admissionID := modulecore.NewTaskID(), modulecore.NewRunID(), modulecore.NewEvidenceID()
	meeting, err := meeting.BindNativeExecution(meeting.Guard(), nativeTaskID, nativeRunID, admissionID)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.LoadOrCreateMeetingOccurrence(ctx, access, key, meeting)
	if err != nil {
		t.Fatal(err)
	}
	missed, err := meeting.MarkPartial(meeting.Guard(), domainmeeting.PartialEvidence{EvidenceID: modulecore.NewEvidenceID(), Key: key,
		Generation: meeting.Generation, Epoch: meeting.Epoch, ObservedAt: meeting.DeadlineAt, ReasonCode: "missed"})
	if err != nil {
		t.Fatal(err)
	}
	requestID := key.String() + "|partial-minutes|" + string(missed.PartialEvidence.EvidenceID)
	if err := missed.Validate(); err != nil {
		t.Fatalf("fixture terminal meeting validation: %v", err)
	}
	checkpoint, err := store.SaveMeetingOccurrence(ctx, access, key, created.Meeting.Guard(), created.PayloadHash, missed)
	if err != nil {
		t.Fatal(err)
	}
	missed, err = checkpoint.Meeting.PrepareTranscript(checkpoint.Meeting.Guard(), requestID)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.SaveMeetingOccurrence(ctx, access, key, checkpoint.Meeting.Guard(), checkpoint.PayloadHash, missed)
	if err != nil {
		t.Fatal(err)
	}
	request := TaskMeetingMinutesRequest{OwnerUserID: access.OwnerUserID, CoordinatorActorID: access.CoordinatorActorID,
		RegistrationEvidenceID: missed.RegistrationEvidenceID, ConfigSHA256: strings.Repeat("c", 64), Meeting: stored.Meeting,
		Messages: []TaskMeetingMinutesMessageProof{}}
	verify := func(_ context.Context, got TaskMeetingMinutesRequest) error {
		if got.Meeting.Status != domainmeeting.StatusMissed || got.Meeting.TranscriptRequestID != requestID || len(got.Messages) != 0 {
			return domainworkstream.ErrTaskPlanConflict
		}
		return nil
	}
	reference, err := store.PersistTaskMeetingMinutes(ctx, request, verify)
	if err != nil {
		t.Fatalf("persist owner-derived missed artifact: %v", err)
	}
	if reference.ArtifactID.Validate() != nil || reference.WorkstreamID.Validate() != nil || reference.ArtifactID == modulecore.ArtifactID(key.ScheduleID) || reference.CreatedAt.IsZero() {
		t.Fatalf("minutes reference is not an actual Workstream artifact: %+v", reference)
	}
	replayed, err := store.PersistTaskMeetingMinutes(ctx, request, verify)
	if err != nil || replayed.ArtifactID != reference.ArtifactID || !replayed.CreatedAt.Equal(reference.CreatedAt) {
		t.Fatalf("exact artifact replay changed reference: first=%+v retry=%+v err=%v", reference, replayed, err)
	}
	var authCalls int
	found, ok, err := store.FindTaskMeetingMinutes(ctx, access.OwnerUserID, key, requestID, stored.Meeting.Guard(),
		func(_ context.Context, owner string, gotKey domainmeeting.OccurrenceKey, gotRequest string, gotGuard domainmeeting.Guard, actual *TaskMeetingMinutesReference) error {
			authCalls++
			if owner != access.OwnerUserID || gotKey != key || gotRequest != requestID || gotGuard != stored.Meeting.Guard() {
				return domainworkstream.ErrTaskPlanForbidden
			}
			if actual != nil && !sameTaskMeetingMinutesReference(*actual, reference) {
				return domainworkstream.ErrTaskPlanForbidden
			}
			return nil
		})
	if err != nil || !ok || !sameTaskMeetingMinutesReference(found, reference) || authCalls != 2 {
		t.Fatalf("exact minutes receipt readback: found=%+v ok=%t authCalls=%d err=%v", found, ok, authCalls, err)
	}
	if _, err := store.ReadTaskMeetingMinutes(ctx, reference, func(_ context.Context, actual TaskMeetingMinutesReference) error {
		if !sameTaskMeetingMinutesReference(actual, reference) {
			return domainworkstream.ErrTaskPlanForbidden
		}
		return nil
	}); err != nil {
		t.Fatalf("authorized exact artifact read: %v", err)
	}
	if _, err := store.ListTaskMeetingOccurrenceSummaries(ctx, reference.WorkstreamID, access.OwnerUserID, key.ScheduleID, 10,
		func(context.Context, modulecore.WorkstreamID, string, modulecore.ScheduleID, *TaskMeetingOccurrenceSummary) error {
			return nil
		}); !errors.Is(err, domainworkstream.ErrTaskPlanUnavailable) {
		t.Fatalf("artifact without aggregate proof was reported as terminal success: %v", err)
	}
	allowTerminal := func(context.Context, string, domainmeeting.OccurrenceKey, string, domainmeeting.Guard, *TaskMeetingMinutesReference) error {
		return nil
	}
	if _, _, err := store.FindTerminalTaskMeetingMinutes(ctx, access.OwnerUserID, key, requestID, stored.Meeting.Guard(), allowTerminal); !errors.Is(err, domainworkstream.ErrTaskPlanUnavailable) {
		t.Fatalf("terminal publication accepted minutes before the aggregate pinned its proof: %v", err)
	}
	partialMinutes := domainmeeting.PartialMinutesEvidence{EvidenceID: modulecore.NewEvidenceID(), Reference: domainmeeting.MinutesArtifactReference{
		RequestID: reference.OccurrenceKey + "|partial-minutes|" + string(missed.PartialEvidence.EvidenceID), ArtifactID: reference.ArtifactID,
		WorkstreamID: reference.WorkstreamID, OwnerUserID: reference.OwnerUserID, CoordinatorActorID: reference.CoordinatorActorID, Key: key,
		RegistrationEvidenceID: reference.RegistrationEvidenceID, ConfigRevision: reference.ConfigRevision, ConfigSHA256: reference.ConfigSHA256,
		Audience: reference.Audience, AudienceSHA256: reference.AudienceSHA256, Guard: reference.Guard, TranscriptSHA256: reference.TranscriptSHA256, CreatedAt: reference.CreatedAt}}
	missedWithMinutes, err := stored.Meeting.RecordPartialMinutes(stored.Meeting.Guard(), partialMinutes)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.SaveMeetingOccurrence(ctx, access, key, stored.Meeting.Guard(), stored.PayloadHash, missedWithMinutes)
	if err != nil {
		t.Fatal(err)
	}
	terminalRef, terminalFound, err := store.FindTerminalTaskMeetingMinutes(ctx, access.OwnerUserID, key, requestID, missedWithMinutes.Guard(), allowTerminal)
	if err != nil || !terminalFound || !sameTaskMeetingMinutesReference(terminalRef, reference) {
		t.Fatalf("terminal lookup must reuse the exact pinned earlier-guard minutes: found=%t err=%v", terminalFound, err)
	}
	if _, _, err := store.FindTerminalTaskMeetingMinutes(ctx, access.OwnerUserID, key, requestID, stored.Meeting.Guard(), allowTerminal); !errors.Is(err, domainworkstream.ErrTaskPlanNotFound) {
		t.Fatalf("terminal lookup accepted stale caller guard: %v", err)
	}
	if _, found, err := store.FindTerminalTaskMeetingMinutes(ctx, "another-owner", key, requestID, missedWithMinutes.Guard(), allowTerminal); err == nil || found {
		t.Fatalf("terminal lookup exposed another owner's minutes: found=%t err=%v", found, err)
	}
	deniedCalls := 0
	if _, _, err := store.FindTerminalTaskMeetingMinutes(ctx, access.OwnerUserID, key, requestID, missedWithMinutes.Guard(), func(context.Context, string, domainmeeting.OccurrenceKey, string, domainmeeting.Guard, *TaskMeetingMinutesReference) error {
		deniedCalls++
		return domainworkstream.ErrTaskPlanForbidden
	}); !errors.Is(err, domainworkstream.ErrTaskPlanForbidden) || deniedCalls != 1 {
		t.Fatalf("terminal authorization must reject before private body lookup: calls=%d err=%v", deniedCalls, err)
	}
	list, err := store.ListTaskMeetingOccurrenceSummaries(ctx, reference.WorkstreamID, access.OwnerUserID, key.ScheduleID, 10,
		func(context.Context, modulecore.WorkstreamID, string, modulecore.ScheduleID, *TaskMeetingOccurrenceSummary) error {
			return nil
		})
	if err != nil || len(list.Occurrences) != 1 || list.Occurrences[0].Minutes == nil || list.Occurrences[0].Minutes.ArtifactID != reference.ArtifactID {
		t.Fatalf("minutes summary not bound to persisted partial proof: list=%+v err=%v", list, err)
	}
	artifacts, err := store.ListArtifacts(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range artifacts {
		if artifact.ArtifactID == string(reference.ArtifactID) {
			t.Fatal("generic Workstream artifact listing exposed private meeting minutes")
		}
	}
	changed := request
	changed.ConfigSHA256 = strings.Repeat("d", 64)
	if _, err := store.PersistTaskMeetingMinutes(ctx, changed, verify); !errors.Is(err, domainworkstream.ErrTaskPlanConflict) {
		t.Fatalf("changed minutes inputs error = %v, want conflict", err)
	}
}

func TestFinishingOccurrenceCannotDowngradeAfterCompletedMinutesCommit(t *testing.T) {
	ctx, access, _, key, meeting := meetingStoreFixture(t)
	store := taskPlanTestStore(t, filepath.Join(t.TempDir(), "meeting-finish-recovery.db"))
	_, err := store.LoadOrCreateMeetingOccurrence(ctx, access, key, meeting)
	if err != nil {
		t.Fatal(err)
	}
	workstreamID, err := store.FindTaskCenterWorkstreamID(ctx, func() tool.ToolExecutionScope {
		scope, ok := tool.ToolExecutionScopeFromContext(ctx)
		if !ok {
			t.Fatal("fixture execution scope missing")
		}
		return scope
	}())
	if err != nil {
		t.Fatal(err)
	}
	configSHA := strings.Repeat("a", 64)
	transcript := completedMinutesRecoveryFixture(key, meeting, "finish-request", workstreamID, meeting.RegistrationEvidenceID, configSHA)
	transcriptJSON, err := json.Marshal(transcript)
	if err != nil {
		t.Fatal(err)
	}
	minutes := TaskMeetingMinutesArtifact{SchemaVersion: taskMeetingMinutesSchemaVersion, Transcript: transcript, InputSHA256: sha256Hex(transcriptJSON)}
	payload, err := json.Marshal(minutes)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := meeting.ScheduledAt.Add(time.Minute)
	artifact := domainworkstream.Artifact{ArtifactID: string(modulecore.NewArtifactID()), WorkstreamID: string(workstreamID), Type: taskMeetingMinutesArtifactType,
		Status: "stored", Payload: payload, CreatedAt: createdAt}
	if err := domainworkstream.ValidateArtifact(artifact); err != nil {
		t.Fatalf("recovery fixture artifact: %v", err)
	}
	seedSQLiteMeetingMinutesArtifact(t, store, artifact)
	artifactReference := taskMeetingMinutesReference(artifact, createdAt)
	completedAggregate := meeting
	completedAggregate.Status = domainmeeting.StatusCompleted
	completedAggregate.Generation, completedAggregate.Epoch, completedAggregate.ProgressionRevision = 1, 1, 8
	completedAggregate.TranscriptRequestID = transcript.TranscriptRequestID
	completedAggregate.FinishEvidence = &domainmeeting.FinalizationEvidence{EvidenceID: modulecore.NewEvidenceID(), Key: key, Generation: 1, Epoch: 1,
		ProgressionRevision: 7, TranscriptArtifactID: artifactReference.ArtifactID, PersistedAt: artifactReference.CreatedAt}
	if err := validateTaskMeetingMinutesOccurrenceProof(completedAggregate, minutes, artifactReference); err != nil {
		t.Fatalf("matching stored FinishEvidence and completed artifact: %v", err)
	}
	wrongFinishArtifact := completedAggregate
	wrongFinishArtifact.FinishEvidence = cloneFinalizationForTest(*completedAggregate.FinishEvidence)
	wrongFinishArtifact.FinishEvidence.TranscriptArtifactID = modulecore.NewArtifactID()
	if err := validateTaskMeetingMinutesOccurrenceProof(wrongFinishArtifact, minutes, artifactReference); !errors.Is(err, domainworkstream.ErrTaskPlanUnavailable) {
		t.Fatalf("mismatched FinishEvidence artifact error = %v, want unavailable", err)
	}
	wrongFinishTime := completedAggregate
	wrongFinishTime.FinishEvidence = cloneFinalizationForTest(*completedAggregate.FinishEvidence)
	wrongFinishTime.FinishEvidence.PersistedAt = artifactReference.CreatedAt.Add(time.Second)
	if err := validateTaskMeetingMinutesOccurrenceProof(wrongFinishTime, minutes, artifactReference); !errors.Is(err, domainworkstream.ErrTaskPlanUnavailable) {
		t.Fatalf("mismatched FinishEvidence timestamp error = %v, want unavailable", err)
	}
	current := domainmeeting.Meeting{Status: domainmeeting.StatusFinishing, Key: key, Generation: 1, Epoch: 1, ProgressionRevision: 7, TranscriptRequestID: transcript.TranscriptRequestID}
	partial := current
	partial.Status = domainmeeting.StatusPartial
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = validateMeetingDowngradeAgainstMinutesTx(ctx, tx, key, workstreamID, current, partial)
	_ = tx.Rollback()
	if !errors.Is(err, domainmeeting.ErrConflict) {
		t.Fatalf("Finishing→Partial after completed artifact error = %v, want conflict", err)
	}
}

func cloneFinalizationForTest(value domainmeeting.FinalizationEvidence) *domainmeeting.FinalizationEvidence {
	copyValue := value
	return &copyValue
}

func completedMinutesRecoveryFixture(key domainmeeting.OccurrenceKey, meeting domainmeeting.Meeting, requestID string, workstreamID modulecore.WorkstreamID, registrationID modulecore.EvidenceID, configSHA string) TaskMeetingMinutesTranscript {
	audience := append([]string(nil), meeting.Participants...)
	audienceHash, _ := domaintask.TaskMeetingAudienceSHA256(audience)
	messageID, taskID, runID := modulecore.NewMessageID(), modulecore.NewTaskID(), modulecore.NewRunID()
	threadID, sessionID, eventID, scopeID := modulecore.NewThreadID(), modulecore.NewSessionID(), modulecore.NewEventID(), modulecore.NewEvidenceID()
	body := "completed report"
	ref := domaintask.TaskMeetingMessageReference{OccurrenceKey: key.String(), SessionID: sessionID, ThreadID: threadID, MessageID: messageID, EventID: eventID,
		BodySHA256: taskMeetingMinutesBodySHA256(body), AudienceSHA256: audienceHash, ConfigRevision: meeting.ScheduleRevision, ConfigSHA256: strings.Repeat("a", 64),
		AttemptGeneration: 1, AttemptEpoch: 1, ScopeEvidenceID: scopeID}
	at := meeting.ScheduledAt.Add(2 * time.Minute)
	consumption := make([]domaintask.TaskMeetingConsumedMessage, 0, len(audience))
	for _, actor := range audience {
		consumption = append(consumption, domaintask.TaskMeetingConsumedMessage{Reference: ref, ActorID: actor, ConsumerTaskID: modulecore.NewTaskID(), ConsumerRunID: modulecore.NewRunID(),
			ContextSHA256: strings.Repeat("b", 64), AcceptedAt: at, EvidenceID: modulecore.NewEvidenceID()})
	}
	delivery := domainmeeting.DeliveryEvidence{EvidenceID: modulecore.NewEvidenceID(), Key: key, Generation: 1, Epoch: 1, ScopeEvidenceID: scopeID, ThreadID: threadID,
		MessageID: messageID, Delivered: true, Audience: audience, ObservedAt: at}
	return TaskMeetingMinutesTranscript{WorkstreamID: workstreamID, OwnerUserID: "owner-1", CoordinatorActorID: "mio", OccurrenceKey: key,
		TranscriptRequestID: requestID, RegistrationEvidenceID: registrationID, ConfigRevision: meeting.ScheduleRevision, ConfigSHA256: configSHA, Audience: audience,
		Guard: domainmeeting.Guard{Generation: 1, Epoch: 1, ProgressionRevision: 7}, ScheduledAt: meeting.ScheduledAt, Outcome: domainmeeting.StatusCompleted,
		MessageOrder: []modulecore.MessageID{messageID}, Reports: []TaskMeetingMinutesReport{{MessageID: messageID, ActorID: audience[0], Body: body,
			SpeakerEvidenceID: modulecore.NewEvidenceID(), SpeakerEvidenceSHA: strings.Repeat("c", 64), SourceTaskID: taskID, SourceRunID: runID}},
		Messages: []TaskMeetingMinutesMessageProof{{DeliveryState: domainmeeting.DeliveryDelivered, MessageID: messageID, Reference: ref, SenderActorID: audience[0], SenderTaskID: taskID, SenderRunID: runID,
			Delivery: delivery, ConsumptionProofs: consumption}}}
}

func TestFindTaskCenterWorkstreamIDIsReadOnlyAndUserScoped(t *testing.T) {
	ctx := context.Background()
	store := taskPlanTestStore(t, filepath.Join(t.TempDir(), "meeting-workstream-read.db"))
	scope := taskPlanTestUserScope(t, "meeting-owner-read")
	scopedCtx := tool.WithToolExecutionScope(ctx, scope)
	if _, err := store.FindTaskCenterWorkstreamID(scopedCtx, scope); !errors.Is(err, domainworkstream.ErrTaskPlanNotFound) {
		t.Fatalf("missing Workstream read = %v, want typed not-found without creation", err)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workstream`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("missing lookup created Workstream: count=%d err=%v", count, err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	firstInserted, secondInserted := string(modulecore.NewWorkstreamID()), string(modulecore.NewWorkstreamID())
	if firstInserted < secondInserted {
		firstInserted, secondInserted = secondInserted, firstInserted
	}
	if err := ensureTaskCenterWorkstream(ctx, tx, firstInserted, domainworkstream.TaskPlanDataScopeUser, scope.AuthenticatedUserID, time.Now().UTC()); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := ensureTaskCenterWorkstream(ctx, tx, secondInserted, domainworkstream.TaskPlanDataScopeUser, scope.AuthenticatedUserID, time.Now().UTC()); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	got, err := store.FindTaskCenterWorkstreamID(scopedCtx, scope)
	if err != nil || string(got) != firstInserted {
		t.Fatalf("owner Workstream read = %q, %v; want first-created %q (the larger lexical ID)", got, err, firstInserted)
	}
	writeTx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	writerChoice, err := findOrCreateTaskCenterWorkstream(ctx, writeTx, domainworkstream.TaskPlanDataScopeUser, scope.AuthenticatedUserID, time.Now().UTC())
	_ = writeTx.Rollback()
	if err != nil || writerChoice != firstInserted {
		t.Fatalf("TaskMeeting writer Workstream selection = %q, %v; want read selection %q", writerChoice, err, firstInserted)
	}
	other := taskPlanTestAgentScope(t, "meeting-other-owner", "mio", "other-user", tool.DataScopeUser)
	if _, err := store.FindTaskCenterWorkstreamID(tool.WithToolExecutionScope(ctx, other), other); !errors.Is(err, domainworkstream.ErrTaskPlanNotFound) {
		t.Fatalf("other user Workstream read = %v, want not-found", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workstream`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("read-only lookups changed Workstream count: count=%d err=%v", count, err)
	}
}

func TestJSONLTaskMeetingMinutesAnnotationsStayOutOfGenericReaders(t *testing.T) {
	ctx := context.Background()
	store := NewJSONLStore(filepath.Join(t.TempDir(), "minutes-jsonl"))
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	artifact := domainworkstream.Artifact{ArtifactID: "artifact-private-meeting-minutes-jsonl", WorkstreamID: "ws-private-meeting", Type: taskMeetingMinutesArtifactType, Status: "stored", CreatedAt: now}
	if err := appendJSONL(store.artifactPath, artifact); err != nil {
		t.Fatal(err)
	}
	annotation := domainworkstream.ArtifactAnnotation{AnnotationID: "annotation-private-meeting-minutes-jsonl", ArtifactID: artifact.ArtifactID, Comment: "private transcript note", Status: "open", CreatedAt: now}
	if err := appendJSONL(store.annotationPath, annotation); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveArtifactAnnotation(ctx, annotation); !errors.Is(err, domainworkstream.ErrTaskPlanForbidden) {
		t.Fatalf("JSONL generic annotation write error = %v, want forbidden", err)
	}
	artifacts, err := store.ListArtifacts(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range artifacts {
		if item.ArtifactID == artifact.ArtifactID {
			t.Fatalf("JSONL generic artifact list exposed private minutes: %#v", item)
		}
	}
	annotations, err := store.ListArtifactAnnotations(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range annotations {
		if item.ArtifactID == artifact.ArtifactID {
			t.Fatalf("JSONL generic annotation list exposed private minutes annotation: %#v", item)
		}
	}
}

func TestListTaskMeetingOccurrenceSummariesIsBoundedAndOwnerScoped(t *testing.T) {
	ctx, access, config, firstKey, firstMeeting := meetingStoreFixture(t)
	store := taskPlanTestStore(t, filepath.Join(t.TempDir(), "meeting-summaries.db"))
	if _, err := store.LoadOrCreateMeetingOccurrence(ctx, access, firstKey, firstMeeting); err != nil {
		t.Fatal(err)
	}
	secondKey := firstKey
	secondKey.Slot = "12:00"
	secondMeeting, err := domainmeeting.NewOccurrence(config, secondKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadOrCreateMeetingOccurrence(ctx, access, secondKey, secondMeeting); err != nil {
		t.Fatal(err)
	}
	var workstreamID modulecore.WorkstreamID
	if err := store.db.QueryRowContext(ctx, `SELECT workstream_id FROM artifact WHERE json_extract(payload,'$.artifact_type')=? LIMIT 1`, taskMeetingOccurrenceArtifactType).Scan(&workstreamID); err != nil {
		t.Fatal(err)
	}
	var authCalls int
	list, err := store.ListTaskMeetingOccurrenceSummaries(ctx, workstreamID, access.OwnerUserID, config.ScheduleID, 1, func(_ context.Context, gotWorkstream modulecore.WorkstreamID, owner string, schedule modulecore.ScheduleID, summary *TaskMeetingOccurrenceSummary) error {
		if gotWorkstream != workstreamID || owner != access.OwnerUserID || schedule != config.ScheduleID {
			t.Fatalf("authorization scope = %q/%q/%q", gotWorkstream, owner, schedule)
		}
		if summary != nil {
			authCalls++
			if summary.OwnerUserID != access.OwnerUserID || summary.WorkstreamID != workstreamID || summary.Key.ScheduleID != config.ScheduleID || len(summary.Audience) != 8 {
				t.Fatalf("unsafe occurrence summary authorization snapshot: %#v", summary)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if list.TotalCount != 2 || !list.HasMore || len(list.Occurrences) != 1 || list.Occurrences[0].Key != secondKey || list.Occurrences[0].Status != domainmeeting.StatusScheduled || list.Occurrences[0].LastCheckpointAt.IsZero() || authCalls != 1 {
		t.Fatalf("bounded occurrence list = %#v, authCalls=%d", list, authCalls)
	}
	if list.Occurrences[0].TaskRef != nil || list.Occurrences[0].RunRef != nil || list.Occurrences[0].Minutes != nil {
		t.Fatalf("summary invented native execution or minutes references: %#v", list.Occurrences[0])
	}
}

func TestTaskMeetingOccurrenceSummaryUsesOnlyObservedCheckpointProofs(t *testing.T) {
	_, access, _, key, meeting := meetingStoreFixture(t)
	scopeID := modulecore.NewEvidenceID()
	observedAt := meeting.ScheduledAt.Add(time.Minute)
	meeting.ScopeEvidence = &domainmeeting.WorldScopeEvidence{EvidenceID: scopeID, Key: key, Generation: meeting.Generation, Epoch: meeting.Epoch, ObservedAt: observedAt}
	for index, actorID := range meeting.Participants[:len(meeting.Participants)-1] {
		meeting.Attendance = append(meeting.Attendance, domainmeeting.Attendance{Evidence: domainmeeting.PresenceEvidence{ActorID: actorID, ScopeEvidenceID: scopeID, Present: index < 5}})
	}
	envelope := meetingOccurrenceEnvelope{Key: key, Access: access, Meeting: meeting}
	summary := taskMeetingOccurrenceSummary(envelope, modulecore.NewWorkstreamID(), observedAt)
	if summary.AttendanceCount != nil || summary.AttendanceScopeID != "" || summary.AttendanceObservedAt != nil {
		t.Fatalf("partial evidence was promoted to a complete attendance count: %+v", summary)
	}
	meeting.Attendance = append(meeting.Attendance, domainmeeting.Attendance{Evidence: domainmeeting.PresenceEvidence{ActorID: meeting.Participants[len(meeting.Participants)-1], ScopeEvidenceID: scopeID, Present: false}})
	messageOne, messageTwo := modulecore.NewMessageID(), modulecore.NewMessageID()
	delivered := &domainmeeting.DeliveryEvidence{Delivered: true}
	meeting.Reports = []domainmeeting.Report{
		{Utterance: domainmeeting.Utterance{MessageID: messageOne, ActorID: "mio", Body: "private body one", DeliveryEvidence: delivered}, Concerns: []domainmeeting.Concern{{Description: "private concern one"}, {Description: "private concern two"}}},
		{Utterance: domainmeeting.Utterance{MessageID: messageTwo, ActorID: "shiro", Body: "private body two"}, Concerns: []domainmeeting.Concern{{Description: "private concern three"}}},
	}
	firstConcern := domainmeeting.ConcernID{ReportMessageID: messageOne, Ordinal: 1}
	thirdConcern := domainmeeting.ConcernID{ReportMessageID: messageTwo, Ordinal: 1}
	meeting.Answers = []domainmeeting.Answer{
		{Utterance: domainmeeting.Utterance{ActorID: "shiro", DeliveryEvidence: delivered}, ConcernID: firstConcern, ReporterID: "mio", Kind: "response"},
		{Utterance: domainmeeting.Utterance{ActorID: "ao", DeliveryEvidence: delivered}, ConcernID: firstConcern, ReporterID: "mio", Kind: "response"},
		// A valid-looking delivered answer cannot make an undelivered report's
		// concern count as reported to the meeting.
		{Utterance: domainmeeting.Utterance{ActorID: "ao", DeliveryEvidence: delivered}, ConcernID: thirdConcern, ReporterID: "shiro", Kind: "response"},
	}
	envelope.Meeting = meeting
	summary = taskMeetingOccurrenceSummary(envelope, modulecore.NewWorkstreamID(), observedAt)
	if summary.AttendanceCount == nil || *summary.AttendanceCount != 5 || summary.AttendanceScopeID != scopeID || summary.AttendanceObservedAt == nil || !summary.AttendanceObservedAt.Equal(observedAt) {
		t.Fatalf("complete exact-scope attendance projection = count %v scope %q at %v", summary.AttendanceCount, summary.AttendanceScopeID, summary.AttendanceObservedAt)
	}
	if summary.DeliveredReportCount != 1 || summary.ReportedConcernCount != 2 || summary.AnsweredConcernCount != 1 || summary.UnresolvedConcernCount != 1 {
		t.Fatalf("owner-derived concern/report counts = delivered %d reported %d answered %d unresolved %d", summary.DeliveredReportCount, summary.ReportedConcernCount, summary.AnsweredConcernCount, summary.UnresolvedConcernCount)
	}
	meeting.Attendance = append([]domainmeeting.Attendance(nil), meeting.Attendance...)
	for index := range meeting.Attendance {
		meeting.Attendance[index].Evidence.Present = false
	}
	envelope.Meeting = meeting
	zero := taskMeetingOccurrenceSummary(envelope, modulecore.NewWorkstreamID(), observedAt)
	if zero.AttendanceCount == nil || *zero.AttendanceCount != 0 {
		t.Fatalf("observed zero must differ from unknown attendance: %v", zero.AttendanceCount)
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private body") || strings.Contains(string(encoded), "private concern") {
		t.Fatal("owner summary exposed private transcript text")
	}
}

func seedSQLiteMeetingMinutesArtifact(t *testing.T, store *SQLiteStore, artifact domainworkstream.Artifact) {
	t.Helper()
	payload, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO artifact (artifact_id, workstream_id, created_at, payload) VALUES (?, ?, ?, ?)`, artifact.ArtifactID, artifact.WorkstreamID, artifact.CreatedAt.Format(timeFormatRFC3339Nano), string(payload)); err != nil {
		t.Fatal(err)
	}
}
