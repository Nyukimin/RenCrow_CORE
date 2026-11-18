package taskmeeting

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func testConfig(revision uint64) ConfigSnapshot {
	actors := []string{"mio", "shiro", "aka", "ao", "kin", "gin", "midori", "kuro"}
	registered := append(append([]string(nil), actors...), "other")
	bindings := make([]WorldActorBinding, 0, len(actors))
	for _, actorID := range actors {
		bindings = append(bindings, WorldActorBinding{CoreActorID: actorID, WorldActorID: "world-" + actorID})
	}
	return ConfigSnapshot{
		ScheduleID: modulecore.NewScheduleID(),
		Revision:   revision,
		Timing: modulecore.TaskTiming{
			Timezone: TimezoneJST,
			Recurrence: &modulecore.TaskRecurrence{
				Frequency: modulecore.TaskRecurrenceDaily,
				Times:     []string{"08:00", "12:00", "17:00"},
			},
		},
		ConfiguredActors:         actors,
		WorldActorBindings:       bindings,
		RegisteredActors:         registered,
		RegistrationEvidenceID:   modulecore.NewEvidenceID(),
		Rooms:                    RoomRefs{Living: "world-room:living", Dining: "world-room:dining"},
		MeetingDeadlineAfterSecs: 3600,
	}
}

func testKey(config ConfigSnapshot) OccurrenceKey {
	return OccurrenceKey{ScheduleID: config.ScheduleID, LocalDate: "2026-11-17", Slot: "08:00", Timezone: TimezoneJST}
}

var testWorldOwnerProof = modulecore.NewEvidenceID()

func testWorldSnapshot() WorldSnapshotEvidence {
	return WorldSnapshotEvidence{
		WorldID: "world-home", TimelineID: "timeline-main", TimelineEpoch: "epoch-1",
		StateSeq: 42, StateHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", OwnerProofID: testWorldOwnerProof,
	}
}

func readyMeeting(t *testing.T) Meeting {
	t.Helper()
	config := testConfig(1)
	meeting, err := NewOccurrence(config, testKey(config))
	if err != nil {
		t.Fatal(err)
	}
	meeting, err = meeting.BeginGathering(meeting.Guard(), meeting.ScheduledAt)
	if err != nil {
		t.Fatal(err)
	}
	base := meeting.ScheduledAt.Add(time.Minute)
	meeting, err = meeting.ConfirmWorldScope(meeting.Guard(), WorldScopeEvidence{
		EvidenceID:    modulecore.NewEvidenceID(),
		Key:           meeting.Key,
		Generation:    meeting.Generation,
		Epoch:         meeting.Epoch,
		Snapshot:      testWorldSnapshot(),
		ThreadID:      modulecore.NewThreadID(),
		LivingRoomRef: meeting.Rooms.Living,
		DiningRoomRef: meeting.Rooms.Dining,
		Audience:      append([]string(nil), meeting.Participants...),
		ObservedAt:    base,
		ValidUntil:    base.Add(45 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, actorID := range meeting.Participants {
		meeting, err = meeting.RecordPresence(meeting.Guard(), PresenceEvidence{
			EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key, Generation: meeting.Generation, Epoch: meeting.Epoch,
			Snapshot: testWorldSnapshot(),
			ActorID:  actorID, WorldActorID: "world-" + actorID, Present: true, RoomRef: meeting.Rooms.Living, ObservedAt: base.Add(time.Second), ValidUntil: base.Add(40 * time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	meeting, err = meeting.BeginReporting(meeting.Guard(), base.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return meeting
}

func noConcernReport(actorID string) Report {
	return Report{
		Utterance:          Utterance{MessageID: modulecore.NewMessageID(), ActorID: actorID, Body: "現在と次の予定を定時報告します。"},
		Current:            ReportField{State: ReportFieldNone},
		Next:               ReportField{State: ReportFieldNone},
		ConcernDeclaration: ConcernsNone,
		Concerns:           []Concern{},
	}
}

func TestValidateReportFieldsSupportsUndecidedCurrentAndPreservesNextReference(t *testing.T) {
	if err := ValidateReportFields(
		ReportField{State: ReportFieldUndecided},
		ReportField{State: ReportFieldUndecided},
	); err != nil {
		t.Fatalf("empty partial-view fields should be undecided: %v", err)
	}
	taskRef := modulecore.NewTaskID()
	if err := ValidateReportFields(
		ReportField{State: ReportFieldUndecided},
		ReportField{State: ReportFieldUndecided, Text: "Next step remains under review.", TaskRef: &taskRef},
	); err != nil {
		t.Fatalf("next undecided reference from an existing report should remain valid: %v", err)
	}
	if err := ValidateReportFields(
		ReportField{State: ReportFieldUndecided, TaskRef: &taskRef},
		ReportField{State: ReportFieldUndecided},
	); err == nil {
		t.Fatal("undecided current field accepted a task reference")
	}
	if err := ValidateReportFields(
		ReportField{State: "blocked", Text: "Blocked without a native Task"},
		ReportField{State: ReportFieldUndecided},
	); err == nil {
		t.Fatal("blocked current field without its native Task reference was accepted")
	}
	if err := ValidateReportFields(
		ReportField{State: ReportFieldUndecided},
		ReportField{State: ReportFieldUndecided, TaskRef: &taskRef},
	); err == nil {
		t.Fatal("undecided next reference without explanatory text was accepted")
	}
}

func speakerEvidence(meeting Meeting, utterance Utterance) SpeakerEvidence {
	return SpeakerEvidence{
		EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key, Generation: meeting.Generation, Epoch: meeting.Epoch,
		ScopeEvidenceID: meeting.ScopeEvidence.EvidenceID,
		ThreadID:        meeting.ScopeEvidence.ThreadID,
		MessageID:       utterance.MessageID, ActorID: utterance.ActorID, Body: utterance.Body,
		ObservedAt: meeting.ScheduledAt.Add(2 * time.Minute),
	}
}

func recordAndDeliverReport(t *testing.T, meeting Meeting, report Report) Meeting {
	t.Helper()
	var err error
	meeting, err = meeting.RecordReport(meeting.Guard(), report, speakerEvidence(meeting, report.Utterance))
	if err != nil {
		t.Fatal(err)
	}
	return beginAndConfirmDelivery(t, meeting, report.Utterance.MessageID, true)
}

func beginAndConfirmDelivery(t *testing.T, meeting Meeting, messageID modulecore.MessageID, delivered bool) Meeting {
	t.Helper()
	var err error
	meeting, err = meeting.BeginCurrentDelivery(meeting.Guard(), messageID, meeting.ScheduledAt.Add(2*time.Minute+30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	evidence := DeliveryEvidence{
		EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key, Generation: meeting.Generation, Epoch: meeting.Epoch,
		ScopeEvidenceID: meeting.ScopeEvidence.EvidenceID,
		ThreadID:        meeting.ScopeEvidence.ThreadID,
		MessageID:       messageID, Delivered: delivered, ObservedAt: meeting.ScheduledAt.Add(3 * time.Minute),
	}
	if delivered {
		evidence.Audience = append([]string(nil), meeting.Participants...)
	}
	meeting, err = meeting.ConfirmDelivery(meeting.Guard(), evidence)
	if err != nil {
		t.Fatal(err)
	}
	return meeting
}

func finishNoConcernMeeting(t *testing.T, meeting Meeting) Meeting {
	t.Helper()
	for meeting.Status == StatusReporting {
		if meeting.CurrentTurn == nil || meeting.CurrentTurn.Kind != TurnReport {
			t.Fatalf("missing next report turn: %#v", meeting.CurrentTurn)
		}
		meeting = recordAndDeliverReport(t, meeting, noConcernReport(meeting.CurrentTurn.ActorID))
	}
	if meeting.Status != StatusFinishing {
		t.Fatalf("expected finishing, got %q", meeting.Status)
	}
	evidence := FinalizationEvidence{
		EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key, Generation: meeting.Generation, Epoch: meeting.Epoch,
		ProgressionRevision: meeting.ProgressionRevision, TranscriptArtifactID: modulecore.NewArtifactID(),
		PersistedAt: meeting.ScheduledAt.Add(20 * time.Minute),
	}
	completed, err := meeting.Finish(meeting.Guard(), evidence)
	if err != nil {
		t.Fatal(err)
	}
	return completed
}

func TestNewOccurrenceUsesStableSlotKeyAndConfiguredRoster(t *testing.T) {
	config := testConfig(3)
	meeting, err := NewOccurrence(config, testKey(config))
	if err != nil {
		t.Fatal(err)
	}
	if len(meeting.Participants) != 8 || meeting.Participants[0] != "mio" || meeting.Participants[7] != "kuro" {
		t.Fatalf("configured order was not preserved: %#v", meeting.Participants)
	}
	if meeting.Status != StatusScheduled || meeting.TaskRef != nil || meeting.RunRef != nil {
		t.Fatalf("unexpected initial occurrence: %#v", meeting)
	}
	changedRevision := config
	changedRevision.Revision++
	second, err := NewOccurrence(changedRevision, testKey(changedRevision))
	if err != nil {
		t.Fatal(err)
	}
	if meeting.Key.String() != second.Key.String() {
		t.Fatalf("schedule revision changed stable key: %q != %q", meeting.Key, second.Key)
	}
}

func TestNewOccurrenceRejectsUnregisteredOrInventedRoster(t *testing.T) {
	config := testConfig(1)
	config.ConfiguredActors = config.ConfiguredActors[:7]
	if _, err := NewOccurrence(config, testKey(config)); err == nil {
		t.Fatal("expected exactly eight configured actors to be required")
	}

	config = testConfig(1)
	config.ConfiguredActors[0] = "invented"
	if _, err := NewOccurrence(config, testKey(config)); err == nil {
		t.Fatal("expected unregistered configured actor to be rejected")
	}

	config = testConfig(1)
	config.ConfiguredActors[0] = config.ConfiguredActors[1]
	if _, err := NewOccurrence(config, testKey(config)); err == nil {
		t.Fatal("expected duplicate configured actor to be rejected")
	}
}

func TestTaskTimingStartGatesAndDeadlineAreApplied(t *testing.T) {
	location, err := time.LoadLocation(TimezoneJST)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 17, 8, 0, 0, 0, location)
	deadline := start.Add(30 * time.Minute)
	estimated := int64(900)
	tests := []struct {
		name   string
		update func(*modulecore.TaskTiming)
	}{
		{name: "scheduled_start_at", update: func(timing *modulecore.TaskTiming) { value := start.Add(time.Minute); timing.ScheduledStartAt = &value }},
		{name: "not_before", update: func(timing *modulecore.TaskTiming) { value := start.Add(time.Minute); timing.NotBefore = &value }},
		{name: "allowed_windows", update: func(timing *modulecore.TaskTiming) {
			timing.AllowedWindows = []modulecore.TaskTimeWindow{{Start: "09:00", End: "10:00"}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := testConfig(1)
			test.update(&config.Timing)
			if _, err := NewOccurrence(config, testKey(config)); err == nil {
				t.Fatal("slot outside TaskTiming start conditions must be rejected")
			}
		})
	}
	config := testConfig(1)
	config.Timing.DeadlineAt = &deadline
	config.Timing.EstimatedDurationSeconds = &estimated
	meeting, err := NewOccurrence(config, testKey(config))
	if err != nil {
		t.Fatal(err)
	}
	if !meeting.DeadlineAt.Equal(deadline) {
		t.Fatalf("deadline_at must cap the meeting deadline: got %s want %s", meeting.DeadlineAt, deadline)
	}
	if meeting.Timing.EstimatedDurationSeconds == nil || *meeting.Timing.EstimatedDurationSeconds != estimated {
		t.Fatal("estimated duration metadata must be preserved")
	}
}

func TestBeginGatheringEnforcesTimingAtActualStart(t *testing.T) {
	config := testConfig(1)
	config.Timing.AllowedWindows = []modulecore.TaskTimeWindow{{Start: "08:00", End: "09:00"}}
	meeting, err := NewOccurrence(config, testKey(config))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := meeting.BeginGathering(meeting.Guard(), meeting.ScheduledAt.Add(-time.Second)); !errors.Is(err, ErrNotReady) {
		t.Fatalf("gathering before its scheduled slot must be rejected: %v", err)
	}
	if _, err := meeting.BeginGathering(meeting.Guard(), meeting.ScheduledAt.Add(time.Hour)); !errors.Is(err, ErrNotReady) {
		t.Fatalf("gathering outside the allowed window must be rejected: %v", err)
	}
	if _, err := meeting.BeginGathering(meeting.Guard(), meeting.ScheduledAt.Add(30*time.Minute)); err != nil {
		t.Fatalf("gathering inside the allowed window should start: %v", err)
	}
}

func TestMeetingDeadlineDurationOverflowIsRejected(t *testing.T) {
	config := testConfig(1)
	config.MeetingDeadlineAfterSecs = int64((time.Duration(1<<63-1))/time.Second) + 1
	if _, err := NewOccurrence(config, testKey(config)); err == nil {
		t.Fatal("overflowing meeting deadline duration must be rejected")
	}
}

func TestReportingRequiresWorldScopeAndEveryAttendee(t *testing.T) {
	config := testConfig(1)
	meeting, err := NewOccurrence(config, testKey(config))
	if err != nil {
		t.Fatal(err)
	}
	meeting, err = meeting.BeginGathering(meeting.Guard(), meeting.ScheduledAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := meeting.BeginReporting(meeting.Guard(), meeting.ScheduledAt.Add(time.Minute)); !errors.Is(err, ErrNotReady) {
		t.Fatalf("without World evidence, want not ready; got %v", err)
	}
	base := meeting.ScheduledAt.Add(time.Minute)
	evidence := WorldScopeEvidence{
		EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key, Generation: meeting.Generation, Epoch: meeting.Epoch,
		Snapshot: testWorldSnapshot(),
		ThreadID: modulecore.NewThreadID(), LivingRoomRef: meeting.Rooms.Living, DiningRoomRef: meeting.Rooms.Dining,
		Audience: append([]string(nil), meeting.Participants...), ObservedAt: base, ValidUntil: meeting.DeadlineAt.Add(-time.Second),
	}
	meeting, err = meeting.ConfirmWorldScope(meeting.Guard(), evidence)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := meeting.BeginReporting(meeting.Guard(), base.Add(time.Second)); !errors.Is(err, ErrNotReady) {
		t.Fatalf("without attendance evidence, want not ready; got %v", err)
	}
	for _, actorID := range meeting.Participants[:7] {
		meeting, err = meeting.RecordPresence(meeting.Guard(), PresenceEvidence{
			EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key, Generation: meeting.Generation, Epoch: meeting.Epoch,
			Snapshot: testWorldSnapshot(),
			ActorID:  actorID, WorldActorID: "world-" + actorID, Present: true, RoomRef: meeting.Rooms.Living, ObservedAt: base.Add(time.Second), ValidUntil: base.Add(30 * time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := meeting.BeginReporting(meeting.Guard(), base.Add(2*time.Second)); !errors.Is(err, ErrNotReady) {
		t.Fatalf("without the eighth attendee, want not ready; got %v", err)
	}
	meeting, err = meeting.RecordPresence(meeting.Guard(), PresenceEvidence{
		EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key, Generation: meeting.Generation, Epoch: meeting.Epoch,
		Snapshot: testWorldSnapshot(),
		ActorID:  meeting.Participants[7], WorldActorID: "world-" + meeting.Participants[7], Present: false, ObservedAt: base.Add(2 * time.Second), ValidUntil: base.Add(30 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := meeting.BeginReporting(meeting.Guard(), base.Add(3*time.Second)); !errors.Is(err, ErrNotReady) {
		t.Fatalf("absent attendee must not count as report-ready: %v", err)
	}
}

func TestPresenceEvidenceReplayChecksTheFullPayload(t *testing.T) {
	meeting := readyMeeting(t)
	evidence := meeting.Attendance[0].Evidence
	replayed, err := meeting.RecordPresence(meeting.Guard(), evidence)
	if err != nil {
		t.Fatalf("exact presence evidence replay should be a no-op: %v", err)
	}
	if replayed.ProgressionRevision != meeting.ProgressionRevision {
		t.Fatal("presence replay must not advance progression")
	}
	changed := evidence
	changed.ValidUntil = changed.ValidUntil.Add(-time.Second)
	if _, err := meeting.RecordPresence(meeting.Guard(), changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("same presence evidence id with changed payload must conflict: %v", err)
	}
}

func TestWorldScopeEvidenceReplayChecksTheFullPayload(t *testing.T) {
	meeting := readyMeeting(t)
	evidence := *meeting.ScopeEvidence
	replayed, err := meeting.ConfirmWorldScope(meeting.Guard(), evidence)
	if err != nil {
		t.Fatalf("exact World scope evidence replay should be a no-op: %v", err)
	}
	if replayed.ProgressionRevision != meeting.ProgressionRevision {
		t.Fatal("World scope replay must not advance progression")
	}
	changed := evidence
	changed.ThreadID = modulecore.NewThreadID()
	if _, err := meeting.ConfirmWorldScope(meeting.Guard(), changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("same World scope evidence id with changed thread must conflict: %v", err)
	}
}

func TestSpeakerEvidenceRemainsRecordableAfterWorldScopeRefresh(t *testing.T) {
	meeting := readyMeeting(t)
	oldScope := *meeting.ScopeEvidence
	oldEvidenceTime := oldScope.ObservedAt.Add(time.Minute)
	if meeting.NeedsWorldRefreshAt(oldScope.ValidUntil.Add(time.Second)) == false {
		t.Fatal("expired current World evidence did not request refresh")
	}
	report := noConcernReport(meeting.Participants[0])
	speaker := speakerEvidence(meeting, report.Utterance)
	speaker.ObservedAt = oldEvidenceTime
	prepared, err := meeting.PrepareCurrentGeneration(meeting.Guard(), meeting.Key.String()+"|report|"+speaker.ActorID, []byte(`{"frozen":"old scope"}`))
	if err != nil {
		t.Fatal(err)
	}
	now := oldScope.ValidUntil.Add(time.Second)
	refreshRequestID := meeting.Key.String() + "|gather|" + string(oldScope.EvidenceID)
	meeting, err = prepared.PrepareWorldRefresh(prepared.Guard(), refreshRequestID, now)
	if err != nil {
		t.Fatal(err)
	}
	newSnapshot := oldScope.Snapshot
	newSnapshot.StateSeq++
	newScope := WorldScopeEvidence{EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key,
		Generation: meeting.Generation, Epoch: meeting.Epoch, Snapshot: newSnapshot,
		ThreadID: oldScope.ThreadID, LivingRoomRef: meeting.Rooms.Living, DiningRoomRef: meeting.Rooms.Dining,
		Audience: append([]string(nil), meeting.Participants...), ObservedAt: now, ValidUntil: meeting.DeadlineAt.Add(-time.Minute)}
	meeting, err = meeting.ConfirmWorldScope(meeting.Guard(), newScope)
	if err != nil {
		t.Fatal(err)
	}
	for _, actorID := range meeting.Participants {
		meeting, err = meeting.RecordPresence(meeting.Guard(), PresenceEvidence{EvidenceID: modulecore.NewEvidenceID(),
			Key: meeting.Key, Generation: meeting.Generation, Epoch: meeting.Epoch, ScopeEvidenceID: newScope.EvidenceID,
			Snapshot: newSnapshot, ActorID: actorID, WorldActorID: "world-" + actorID, Present: true,
			RoomRef: meeting.Rooms.Living, ObservedAt: now.Add(time.Second), ValidUntil: newScope.ValidUntil})
		if err != nil {
			t.Fatal(err)
		}
	}
	if meeting.NeedsWorldRefreshAt(now.Add(2 * time.Second)) {
		t.Fatal("new complete World evidence should be fresh")
	}
	if got := string(meeting.CurrentTurn.GenerationInputJSON); got != `{"frozen":"old scope"}` {
		t.Fatalf("refresh changed frozen generation input: %s", got)
	}
	meeting, err = meeting.RecordReport(meeting.Guard(), report, speaker)
	if err != nil {
		t.Fatalf("old valid speaker proof was not recordable after refresh: %v", err)
	}
	if len(meeting.ScopeEvidenceHistory) != 1 || len(meeting.AttendanceHistory) != len(meeting.Participants) {
		t.Fatalf("prior proofs were not retained exactly: scopes=%d attendance=%d", len(meeting.ScopeEvidenceHistory), len(meeting.AttendanceHistory))
	}
}

func TestCloneTimingOwnsNestedCollectionsAndOptionalValues(t *testing.T) {
	start := time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC)
	duration := int64(30)
	original := modulecore.TaskTiming{
		ScheduledStartAt:         &start,
		AllowedWindows:           []modulecore.TaskTimeWindow{{Start: "08:00", End: "09:00"}},
		Recurrence:               &modulecore.TaskRecurrence{Frequency: modulecore.TaskRecurrenceDaily, Times: []string{"08:00"}, Weekdays: []time.Weekday{time.Monday}},
		EstimatedDurationSeconds: &duration,
	}
	cloned := cloneTiming(original)
	*cloned.ScheduledStartAt = cloned.ScheduledStartAt.Add(time.Minute)
	*cloned.EstimatedDurationSeconds = 60
	cloned.AllowedWindows[0].Start = "10:00"
	cloned.Recurrence.Times[0] = "12:00"
	cloned.Recurrence.Weekdays[0] = time.Tuesday
	if !original.ScheduledStartAt.Equal(start) || *original.EstimatedDurationSeconds != duration ||
		original.AllowedWindows[0].Start != "08:00" || original.Recurrence.Times[0] != "08:00" || original.Recurrence.Weekdays[0] != time.Monday {
		t.Fatal("cloneTiming shares mutable optional pointers or nested slices with its source")
	}
}

func TestWorldFreshnessRequiresEveryParticipantPresent(t *testing.T) {
	meeting := readyMeeting(t)
	now := meeting.ScopeEvidence.ObservedAt.Add(2 * time.Second)
	if meeting.NeedsWorldRefreshAt(now) {
		t.Fatal("complete current attendance unexpectedly requires refresh")
	}
	meeting.Attendance = meeting.Attendance[:len(meeting.Attendance)-1]
	if !meeting.NeedsWorldRefreshAt(now) {
		t.Fatal("missing participant attendance did not require refresh")
	}
}

func TestIncompleteFreshWorldCheckpointReusesObservationButAbsenceOrExpiryRefreshes(t *testing.T) {
	meeting := readyMeeting(t)
	now := meeting.ScopeEvidence.ObservedAt.Add(2 * time.Second)
	meeting.Attendance = meeting.Attendance[:len(meeting.Attendance)-1]
	if !meeting.NeedsWorldRefreshAt(now) {
		t.Fatal("missing attendance checkpoint should block new World-dependent actions")
	}
	if meeting.NeedsNewWorldObservationAt(now) {
		t.Fatal("missing attendance checkpoint alone should replay the original World observation")
	}
	meeting.Attendance[0].Evidence.Present = false
	if !meeting.NeedsNewWorldObservationAt(now) {
		t.Fatal("known absence should permit requesting a fresh World observation")
	}
	if !meeting.NeedsNewWorldObservationAt(meeting.ScopeEvidence.ValidUntil) {
		t.Fatal("expired World observation should permit requesting a fresh observation")
	}
}

func TestRefreshedWorldAbsenceBlocksNewDelivery(t *testing.T) {
	meeting := readyMeeting(t)
	report := noConcernReport(meeting.Participants[0])
	var err error
	meeting, err = meeting.RecordReport(meeting.Guard(), report, speakerEvidence(meeting, report.Utterance))
	if err != nil {
		t.Fatal(err)
	}
	oldScope := *meeting.ScopeEvidence
	now := oldScope.ValidUntil.Add(time.Second)
	requestID := meeting.Key.String() + "|gather|" + string(oldScope.EvidenceID)
	meeting, err = meeting.PrepareWorldRefresh(meeting.Guard(), requestID, now)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := oldScope.Snapshot
	snapshot.StateSeq++
	newScope := WorldScopeEvidence{EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key, Generation: meeting.Generation,
		Epoch: meeting.Epoch, Snapshot: snapshot, ThreadID: oldScope.ThreadID, LivingRoomRef: meeting.Rooms.Living,
		DiningRoomRef: meeting.Rooms.Dining, Audience: append([]string(nil), meeting.Participants...),
		ObservedAt: now, ValidUntil: meeting.DeadlineAt.Add(-time.Minute)}
	meeting, err = meeting.ConfirmWorldScope(meeting.Guard(), newScope)
	if err != nil {
		t.Fatal(err)
	}
	for index, actorID := range meeting.Participants {
		present := index != 0
		evidence := PresenceEvidence{EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key, Generation: meeting.Generation,
			Epoch: meeting.Epoch, ScopeEvidenceID: newScope.EvidenceID, Snapshot: snapshot, ActorID: actorID,
			WorldActorID: "world-" + actorID, Present: present, ObservedAt: now, ValidUntil: newScope.ValidUntil}
		if present {
			evidence.RoomRef = meeting.Rooms.Living
		}
		meeting, err = meeting.RecordPresence(meeting.Guard(), evidence)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !meeting.NeedsWorldRefreshAt(now.Add(time.Second)) {
		t.Fatal("participant departure did not invalidate current meeting readiness")
	}
	if _, err := meeting.BeginCurrentDelivery(meeting.Guard(), report.Utterance.MessageID, now.Add(time.Second)); !errors.Is(err, ErrNotReady) {
		t.Fatalf("delivery started without every participant present: %v", err)
	}
}

func TestResumePreservesPendingGenerationScopeAndRequiresFreshScopeForNewSend(t *testing.T) {
	meeting := readyMeeting(t)
	oldScope := *meeting.ScopeEvidence
	requestID := meeting.Key.String() + "|report|" + meeting.CurrentTurn.ActorID
	meeting, err := meeting.PrepareCurrentGeneration(meeting.Guard(), requestID, []byte(`{"source":"old"}`))
	if err != nil {
		t.Fatal(err)
	}
	meeting, err = meeting.Resume(meeting.Guard(), ResumeEvidence{EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key, ObservedAt: meeting.ScheduledAt.Add(5 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if meeting.ScopeEvidence != nil || len(meeting.ScopeEvidenceHistory) != 1 || meeting.CurrentTurn.ScopeEvidenceID != oldScope.EvidenceID {
		t.Fatal("resume did not preserve the pending intent's original World proof")
	}
	newScopeSnapshot := oldScope.Snapshot
	newScopeSnapshot.StateSeq++
	newScope := WorldScopeEvidence{EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key, Generation: meeting.Generation, Epoch: meeting.Epoch,
		Snapshot: newScopeSnapshot, ThreadID: meeting.SharedThreadID, LivingRoomRef: meeting.Rooms.Living, DiningRoomRef: meeting.Rooms.Dining,
		Audience: append([]string(nil), meeting.Participants...), ObservedAt: meeting.ScheduledAt.Add(6 * time.Minute), ValidUntil: meeting.DeadlineAt.Add(-time.Minute)}
	meeting, err = meeting.ConfirmWorldScope(meeting.Guard(), newScope)
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range meeting.Participants {
		meeting, err = meeting.RecordPresence(meeting.Guard(), PresenceEvidence{EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key,
			Generation: meeting.Generation, Epoch: meeting.Epoch, ScopeEvidenceID: newScope.EvidenceID, Snapshot: newScopeSnapshot,
			ActorID: actor, WorldActorID: "world-" + actor, Present: true, RoomRef: meeting.Rooms.Living,
			ObservedAt: newScope.ObservedAt, ValidUntil: newScope.ValidUntil})
		if err != nil {
			t.Fatal(err)
		}
	}
	report := noConcernReport(meeting.CurrentTurn.ActorID)
	evidence := SpeakerEvidence{EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key, Generation: oldScope.Generation, Epoch: oldScope.Epoch,
		ScopeEvidenceID: oldScope.EvidenceID, ThreadID: oldScope.ThreadID, MessageID: report.Utterance.MessageID,
		ActorID: report.Utterance.ActorID, Body: report.Utterance.Body, ObservedAt: oldScope.ObservedAt.Add(time.Minute),
		GenerationRequestID: requestID, GenerationInputSHA256: meeting.CurrentTurn.GenerationInputSHA256,
		TaskRef: modulecore.NewTaskID(), RunRef: modulecore.NewRunID(), TaskAssignee: report.Utterance.ActorID, AdmissionEvidenceID: modulecore.NewEvidenceID()}
	wrongScope := evidence
	wrongScope.ScopeEvidenceID = modulecore.NewEvidenceID()
	if _, err := meeting.RecordReport(meeting.Guard(), report, wrongScope); err == nil {
		t.Fatal("generation result with a different World scope was accepted")
	}
	meeting, err = meeting.RecordReport(meeting.Guard(), report, evidence)
	if err != nil {
		t.Fatalf("receipt from resumed generation was not reconciled against original scope: %v", err)
	}
	if meeting.Reports[0].Utterance.Generation != oldScope.Generation || meeting.Reports[0].SpeakerScopeEvidenceID != oldScope.EvidenceID {
		t.Fatal("reconciled report was relabeled to the resumed generation or refreshed scope")
	}
	if _, err := meeting.BeginCurrentDelivery(meeting.Guard(), report.Utterance.MessageID, newScope.ObservedAt.Add(time.Second)); err != nil {
		t.Fatalf("fresh scope should authorize a new delivery attempt: %v", err)
	}
}

func TestResumeReconcilesOldDeliveryReceiptWithoutResending(t *testing.T) {
	meeting := readyMeeting(t)
	report := noConcernReport(meeting.Participants[0])
	var err error
	meeting, err = meeting.RecordReport(meeting.Guard(), report, speakerEvidence(meeting, report.Utterance))
	if err != nil {
		t.Fatal(err)
	}
	meeting, err = meeting.BeginCurrentDelivery(meeting.Guard(), report.Utterance.MessageID, meeting.ScheduledAt.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	oldAttempt := meeting.Reports[0].Utterance
	meeting = refreshWorldReadiness(t, meeting)
	meeting, err = meeting.Resume(meeting.Guard(), ResumeEvidence{EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key, ObservedAt: meeting.ScheduledAt.Add(6 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if len(meeting.ScopeEvidenceHistory) != 1 || meeting.ScopeEvidenceHistory[0].EvidenceID != oldAttempt.DeliveryScopeEvidenceID {
		t.Fatalf("resume lost the old delivery attempt's World proof: %+v", meeting.ScopeEvidenceHistory)
	}
	receipt := DeliveryEvidence{EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key,
		Generation: oldAttempt.DeliveryGeneration, Epoch: oldAttempt.DeliveryEpoch, ScopeEvidenceID: oldAttempt.DeliveryScopeEvidenceID,
		ThreadID: meeting.SharedThreadID, MessageID: report.Utterance.MessageID, Delivered: true,
		Audience: append([]string(nil), meeting.Participants...), ObservedAt: meeting.ScheduledAt.Add(4 * time.Minute)}
	meeting, err = meeting.ConfirmDelivery(meeting.Guard(), receipt)
	if err != nil {
		t.Fatalf("old canonical delivery receipt was not reconciled after resume: %v", err)
	}
	if meeting.Reports[0].Utterance.DeliveryState != DeliveryDelivered || meeting.NeedsWorldRefreshAt(meeting.ScheduledAt.Add(7*time.Minute)) == false {
		t.Fatal("receipt reconciliation must complete old delivery while leaving current readiness unproven")
	}
}

func TestReportRequiresExplicitCurrentNextAndConcernDisclosure(t *testing.T) {
	meeting := readyMeeting(t)
	report := noConcernReport(meeting.Participants[0])
	report.ConcernDeclaration = ConcernsNotDisclosed
	_, err := meeting.RecordReport(meeting.Guard(), report, speakerEvidence(meeting, report.Utterance))
	if err == nil {
		t.Fatal("missing concern disclosure must not become explicit none")
	}

	report = noConcernReport(meeting.Participants[0])
	report.Next = ReportField{}
	_, err = meeting.RecordReport(meeting.Guard(), report, speakerEvidence(meeting, report.Utterance))
	if err == nil {
		t.Fatal("missing next field must be rejected")
	}
}

func TestReportReplayIsIdempotentAndChangedBodyConflicts(t *testing.T) {
	meeting := readyMeeting(t)
	report := noConcernReport(meeting.Participants[0])
	evidence := speakerEvidence(meeting, report.Utterance)
	first, err := meeting.RecordReport(meeting.Guard(), report, evidence)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := first.RecordReport(first.Guard(), report, evidence)
	if err != nil {
		t.Fatalf("same utterance replay should be idempotent: %v", err)
	}
	if replayed.ProgressionRevision != first.ProgressionRevision || len(replayed.Reports) != 1 {
		t.Fatal("replay changed meeting progression")
	}
	changed := report
	changed.Utterance.Body = "別の本文に置き換えます。"
	if _, err := first.RecordReport(first.Guard(), changed, speakerEvidence(first, changed.Utterance)); !errors.Is(err, ErrConflict) {
		t.Fatalf("same utterance id with changed body must conflict; got %v", err)
	}
}

func TestConcernNeedsOtherSubstantiveAnswerBeforeNextReport(t *testing.T) {
	meeting := readyMeeting(t)
	report := noConcernReport(meeting.Participants[0])
	report.ConcernDeclaration = ConcernsListed
	report.Concerns = []Concern{{Description: "release verification has no owner"}}
	meeting = recordAndDeliverReport(t, meeting, report)
	if meeting.Status != StatusAnswering {
		t.Fatalf("concerned report should enter answering, got %q", meeting.Status)
	}
	if _, err := meeting.AdvanceReporter(meeting.Guard(), meeting.ScheduledAt.Add(10*time.Minute)); !errors.Is(err, ErrNotReady) {
		t.Fatalf("must not skip unanswered concern: %v", err)
	}
	concernID := ConcernID{ReportMessageID: report.Utterance.MessageID, Ordinal: 1}
	if _, err := meeting.RequestAnswer(meeting.Guard(), concernID, meeting.Participants[0], false); err == nil {
		t.Fatal("report owner must not answer their own concern")
	}
	meeting, err := meeting.RequestAnswer(meeting.Guard(), concernID, meeting.Participants[1], false)
	if err != nil {
		t.Fatal(err)
	}
	answer := Answer{
		Utterance: Utterance{MessageID: modulecore.NewMessageID(), ActorID: meeting.Participants[1], Body: "関連する検証記録を確認し、担当者と期限を決める案を出します。"},
		ConcernID: concernID, ReporterID: meeting.Participants[0], Kind: AnswerAdvice,
	}
	answerEvidence := speakerEvidence(meeting, answer.Utterance)
	badAck := answer
	badAck.Utterance.MessageID = modulecore.NewMessageID()
	badAck.Utterance.Body = "了解です。"
	badAckEvidence := speakerEvidence(meeting, badAck.Utterance)
	if _, err := meeting.RecordAnswer(meeting.Guard(), badAck, badAckEvidence); err == nil {
		t.Fatal("acknowledgement alone must not satisfy a concern")
	}
	meeting, err = meeting.RecordAnswer(meeting.Guard(), answer, answerEvidence)
	if err != nil {
		t.Fatal(err)
	}
	meeting = beginAndConfirmDelivery(t, meeting, answer.Utterance.MessageID, true)
	meeting, err = meeting.AdvanceReporter(meeting.Guard(), meeting.ScheduledAt.Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if meeting.CurrentTurn == nil || meeting.CurrentTurn.ActorID != meeting.Participants[1] {
		t.Fatalf("reports must follow the configured order: %#v", meeting.CurrentTurn)
	}
}

func TestMultipleAnswersAllowedInOrderAndEachNeedsDelivery(t *testing.T) {
	meeting := readyMeeting(t)
	report := noConcernReport(meeting.Participants[0])
	report.ConcernDeclaration = ConcernsListed
	report.Concerns = []Concern{{Description: "first"}, {Description: "second"}}
	meeting = recordAndDeliverReport(t, meeting, report)
	first := ConcernID{ReportMessageID: report.Utterance.MessageID, Ordinal: 1}
	second := ConcernID{ReportMessageID: report.Utterance.MessageID, Ordinal: 2}
	if _, err := meeting.RequestAnswer(meeting.Guard(), second, meeting.Participants[2], false); err == nil {
		t.Fatal("later concern cannot be answered before first concern")
	}
	meeting, err := answerConcern(t, meeting, first, meeting.Participants[1], "検証担当を決め、結果を記録する手順にできます。", AnswerConcreteSupport)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := meeting.AdvanceReporter(meeting.Guard(), meeting.ScheduledAt.Add(10*time.Minute)); err == nil {
		t.Fatal("undelivered or unanswered second concern must hold the report sequence")
	}
	meeting, err = meeting.RequestAnswer(meeting.Guard(), first, meeting.Participants[2], true)
	if err != nil {
		t.Fatalf("additional answer from another participant should be allowed: %v", err)
	}
	// Cancel no external effect in the value model by completing this answer before the next concern.
	meeting = recordAndDeliverAnswerFromCurrentTurn(t, meeting, first, meeting.Participants[2], "別の観点では期限前の再確認も有効です。", AnswerAdvice)
	meeting, err = meeting.RequestAnswer(meeting.Guard(), second, meeting.Participants[3], false)
	if err != nil {
		t.Fatal(err)
	}
	meeting = recordAndDeliverAnswerFromCurrentTurn(t, meeting, second, meeting.Participants[3], "確認質問として、必要な証拠の範囲を先に揃えますか。", AnswerClarifyingQuestion)
	if _, err := meeting.AdvanceReporter(meeting.Guard(), meeting.ScheduledAt.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalAnswerAttemptsAdvanceRespondersButNeverAnswerConcerns(t *testing.T) {
	meeting := answerableMeetingWithConcerns(t, 2)
	first := ConcernID{ReportMessageID: meeting.Reports[0].Utterance.MessageID, Ordinal: 1}
	second := ConcernID{ReportMessageID: meeting.Reports[0].Utterance.MessageID, Ordinal: 2}
	meeting, err := meeting.RequestAnswer(meeting.Guard(), first, meeting.Participants[1], false)
	if err != nil {
		t.Fatal(err)
	}
	meeting, err = meeting.PrepareCurrentGeneration(meeting.Guard(), string(modulecore.NewRequestID()), []byte(`{"scope":"first responder"}`))
	if err != nil {
		t.Fatal(err)
	}
	attempt := terminalAnswerAttempt(meeting)
	failed, err := meeting.RecordAnswerAttempt(meeting.Guard(), attempt)
	if err != nil {
		t.Fatal(err)
	}
	if failed.CurrentTurn != nil || len(failed.AnswerAttempts) != 1 || failed.AllConcernsAnswered() {
		t.Fatalf("terminal generation receipt must advance only the turn, not answer the concern: %#v", failed)
	}
	replayed, err := failed.RecordAnswerAttempt(meeting.Guard(), attempt)
	if err != nil || replayed.ProgressionRevision != failed.ProgressionRevision {
		t.Fatalf("exact historical attempt must replay without changing progression: err=%v", err)
	}
	changed := attempt
	changed.UnknownReceipt.SourceContextSHA256 = strings.Repeat("d", 64)
	if _, err := failed.RecordAnswerAttempt(failed.Guard(), changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("same evidence ID with changed proof must conflict: %v", err)
	}
	if got, ok := failed.firstAnswerableConcern(); !ok || got != first {
		t.Fatalf("another participant should still be offered the same concern first: got=%v ok=%v", got, ok)
	}
	meeting, err = failed.RequestAnswer(failed.Guard(), first, meeting.Participants[2], false)
	if err != nil {
		t.Fatal(err)
	}
	meeting, err = meeting.PrepareCurrentGeneration(meeting.Guard(), string(modulecore.NewRequestID()), []byte(`{"scope":"second responder"}`))
	if err != nil {
		t.Fatal(err)
	}
	meeting, err = meeting.RecordAnswerAttempt(meeting.Guard(), terminalAnswerAttempt(meeting))
	if err != nil {
		t.Fatal(err)
	}
	for _, actorID := range meeting.Participants[3:] {
		meeting, err = meeting.RequestAnswer(meeting.Guard(), first, actorID, false)
		if err != nil {
			t.Fatal(err)
		}
		meeting, err = meeting.PrepareCurrentGeneration(meeting.Guard(), string(modulecore.NewRequestID()), []byte(`{"scope":"remaining responder"}`))
		if err != nil {
			t.Fatal(err)
		}
		meeting, err = meeting.RecordAnswerAttempt(meeting.Guard(), terminalAnswerAttempt(meeting))
		if err != nil {
			t.Fatal(err)
		}
	}
	if got, ok := meeting.firstAnswerableConcern(); !ok || got != second {
		t.Fatalf("exhausted first concern must not block an available later concern: got=%v ok=%v", got, ok)
	}
}

func TestExhaustedAnswerRespondersLeaveConcernUnresolved(t *testing.T) {
	meeting := answerableMeetingWithConcerns(t, 1)
	for _, actorID := range meeting.Participants[1:] {
		concern := ConcernID{ReportMessageID: meeting.Reports[0].Utterance.MessageID, Ordinal: 1}
		var err error
		meeting, err = meeting.RequestAnswer(meeting.Guard(), concern, actorID, false)
		if err != nil {
			t.Fatal(err)
		}
		meeting, err = meeting.PrepareCurrentGeneration(meeting.Guard(), string(modulecore.NewRequestID()), []byte(`{"scope":"terminal attempt"}`))
		if err != nil {
			t.Fatal(err)
		}
		meeting, err = meeting.RecordAnswerAttempt(meeting.Guard(), terminalAnswerAttempt(meeting))
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := meeting.firstAnswerableConcern(); ok {
		t.Fatal("no responder should remain after every other participant has a terminal attempt")
	}
	if meeting.AllConcernsAnswered() {
		t.Fatal("terminal attempts must never satisfy an unresolved concern")
	}
	if _, err := meeting.AdvanceReporter(meeting.Guard(), meeting.ScheduledAt.Add(20*time.Minute)); !errors.Is(err, ErrNotReady) {
		t.Fatalf("exhausted concern must still block reporter advancement until partial deadline: %v", err)
	}
}

func TestPartialPersistsInterruptedGenerationIdentityAndImmutableReplay(t *testing.T) {
	meeting := readyMeeting(t)
	prepared, err := meeting.PrepareCurrentGeneration(meeting.Guard(), string(modulecore.NewRequestID()), []byte(`{"frozen":"report input"}`))
	if err != nil {
		t.Fatal(err)
	}
	evidence := PartialEvidence{EvidenceID: modulecore.NewEvidenceID(), Key: prepared.Key, Generation: prepared.Generation, Epoch: prepared.Epoch,
		ObservedAt: prepared.DeadlineAt.Add(time.Second), ReasonCode: "deadline_elapsed", InterruptedTurn: &Turn{Kind: TurnReport, ActorID: "spoof"}}
	partial, err := prepared.MarkPartial(prepared.Guard(), evidence)
	if err != nil {
		t.Fatal(err)
	}
	stored := partial.PartialEvidence.InterruptedTurn
	if stored == nil || stored.GenerationRequestID != prepared.CurrentTurn.GenerationRequestID || stored.GenerationInputSHA256 != prepared.CurrentTurn.GenerationInputSHA256 ||
		string(stored.GenerationInputJSON) != string(prepared.CurrentTurn.GenerationInputJSON) || stored.ScopeEvidenceID != prepared.CurrentTurn.ScopeEvidenceID {
		t.Fatalf("partial must retain the exact preterminal generation identity, got %#v", stored)
	}
	cloned := partial.clone()
	cloned.PartialEvidence.InterruptedTurn.GenerationInputJSON[0] = 'x'
	if string(partial.PartialEvidence.InterruptedTurn.GenerationInputJSON) == string(cloned.PartialEvidence.InterruptedTurn.GenerationInputJSON) {
		t.Fatal("partial evidence clone must not share frozen turn bytes")
	}
	replay, err := partial.MarkPartial(partial.Guard(), evidence)
	if err != nil || replay.ProgressionRevision != partial.ProgressionRevision || replay.PartialEvidence.InterruptedTurn.GenerationRequestID != prepared.CurrentTurn.GenerationRequestID {
		t.Fatalf("exact partial evidence replay must preserve the stored turn snapshot: err=%v", err)
	}
	changed := evidence
	changed.ReasonCode = "different_terminal_reason"
	if _, err := partial.MarkPartial(partial.Guard(), changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("same partial evidence ID with changed request reason must conflict: %v", err)
	}
}

func TestPartialRetainsLegacyInterruptedIntentWithoutReissuingIt(t *testing.T) {
	meeting := readyMeeting(t)
	legacyID := meeting.Key.String() + "|report|mio"
	prepared, err := meeting.PrepareCurrentGeneration(meeting.Guard(), legacyID, []byte(`{"legacy":"historical intent"}`))
	if err != nil {
		t.Fatal(err)
	}
	partial, err := prepared.MarkPartial(prepared.Guard(), PartialEvidence{
		EvidenceID: modulecore.NewEvidenceID(), Key: prepared.Key, Generation: prepared.Generation, Epoch: prepared.Epoch,
		ObservedAt: prepared.DeadlineAt, ReasonCode: "deadline_elapsed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if partial.PartialEvidence.InterruptedTurn == nil || partial.PartialEvidence.InterruptedTurn.GenerationRequestID != legacyID {
		t.Fatalf("historical text request identity must remain visible for cleanup without remint: %#v", partial.PartialEvidence.InterruptedTurn)
	}
}

func TestPartialMinutesReferenceIsBoundAndReplayedWithoutBodyCopy(t *testing.T) {
	meeting := readyMeeting(t)
	partial, err := meeting.MarkPartial(meeting.Guard(), PartialEvidence{
		EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key, Generation: meeting.Generation, Epoch: meeting.Epoch,
		ObservedAt: meeting.DeadlineAt, ReasonCode: "deadline_elapsed",
	})
	if err != nil {
		t.Fatal(err)
	}
	requestID := partial.Key.String() + "|partial-minutes|" + string(partial.PartialEvidence.EvidenceID)
	partial, err = partial.PrepareTranscript(partial.Guard(), requestID)
	if err != nil {
		t.Fatal(err)
	}
	audienceSHA, err := domaintask.TaskMeetingAudienceSHA256(partial.Participants)
	if err != nil {
		t.Fatal(err)
	}
	evidence := PartialMinutesEvidence{EvidenceID: modulecore.NewEvidenceID(), Reference: MinutesArtifactReference{
		RequestID: requestID, ArtifactID: modulecore.NewArtifactID(), WorkstreamID: modulecore.NewWorkstreamID(),
		OwnerUserID: "owner-1", CoordinatorActorID: partial.Participants[0], Key: partial.Key,
		RegistrationEvidenceID: partial.RegistrationEvidenceID, ConfigRevision: partial.ScheduleRevision,
		ConfigSHA256: strings.Repeat("a", 64), Audience: append([]string(nil), partial.Participants...), AudienceSHA256: audienceSHA,
		Guard: partial.Guard(), TranscriptSHA256: strings.Repeat("b", 64), CreatedAt: partial.DeadlineAt.Add(time.Minute),
	}}
	withMinutes, err := partial.RecordPartialMinutes(partial.Guard(), evidence)
	if err != nil {
		t.Fatal(err)
	}
	if withMinutes.PartialMinutes == nil || withMinutes.PartialMinutes.Reference.ArtifactID != evidence.Reference.ArtifactID || withMinutes.Status != StatusPartial {
		t.Fatalf("partial minutes should attach an exact reference without changing outcome: %#v", withMinutes.PartialMinutes)
	}
	replayed, err := withMinutes.RecordPartialMinutes(partial.Guard(), evidence)
	if err != nil || replayed.ProgressionRevision != withMinutes.ProgressionRevision {
		t.Fatalf("exact receipt replay should be a no-op: err=%v", err)
	}
	changed := evidence
	changed.Reference.TranscriptSHA256 = strings.Repeat("c", 64)
	if _, err := withMinutes.RecordPartialMinutes(withMinutes.Guard(), changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("same evidence ID with changed minutes ref must conflict: %v", err)
	}
}

func answerableMeetingWithConcerns(t *testing.T, count int) Meeting {
	t.Helper()
	meeting := readyMeeting(t)
	report := noConcernReport(meeting.Participants[0])
	report.ConcernDeclaration = ConcernsListed
	report.Concerns = make([]Concern, count)
	for index := range report.Concerns {
		report.Concerns[index] = Concern{Description: fmt.Sprintf("concern %d requires an owner", index+1)}
	}
	return recordAndDeliverReport(t, meeting, report)
}

func terminalAnswerAttempt(meeting Meeting) AnswerAttempt {
	turn := meeting.CurrentTurn
	configDigest := strings.Repeat("a", 64)
	inputDigest := strings.Repeat("b", 64)
	sourceDigest := strings.Repeat("c", 64)
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	proof := domaintask.TaskMeetingGenerationUnknownReference{
		TaskID: taskID, RunID: runID, ReceiptID: domaintask.TaskMeetingGenerationUnknownReceiptID(taskID, runID),
		GenerationRequestID: modulecore.RequestID(turn.GenerationRequestID), Purpose: domaintask.TaskMeetingPurposeAnswer,
		ActorID: turn.ActorID, OccurrenceKey: meeting.Key.String(), ConfigRevision: meeting.ScheduleRevision,
		ConfigSHA256: configDigest, InputSHA256: inputDigest, SourceContextSHA256: sourceDigest,
		GenerationScope: domaintask.TaskMeetingGenerationScope{ScopeEvidenceID: turn.ScopeEvidenceID, Generation: turn.Generation, Epoch: turn.Epoch},
		Reason:          domaintask.TaskMeetingGenerationUnknownReasonEffectReturned, EvidenceID: modulecore.NewEvidenceID(),
		RecordedAt: meeting.ScheduledAt.Add(5 * time.Minute),
	}
	return AnswerAttempt{ConcernID: *turn.ConcernID, ReporterID: meeting.Reports[meeting.CurrentReportIndex].Utterance.ActorID, UnknownReceipt: proof}
}

func answerConcern(t *testing.T, meeting Meeting, concernID ConcernID, actorID, body, kind string) (Meeting, error) {
	t.Helper()
	var err error
	meeting, err = meeting.RequestAnswer(meeting.Guard(), concernID, actorID, false)
	if err != nil {
		return Meeting{}, err
	}
	meeting = recordAndDeliverAnswerFromCurrentTurn(t, meeting, concernID, actorID, body, kind)
	return meeting, nil
}

func recordAndDeliverAnswerFromCurrentTurn(t *testing.T, meeting Meeting, concernID ConcernID, actorID, body, kind string) Meeting {
	t.Helper()
	answer := Answer{
		Utterance: Utterance{MessageID: modulecore.NewMessageID(), ActorID: actorID, Body: body},
		ConcernID: concernID, ReporterID: meeting.Participants[0], Kind: kind,
	}
	var err error
	meeting, err = meeting.RecordAnswer(meeting.Guard(), answer, speakerEvidence(meeting, answer.Utterance))
	if err != nil {
		t.Fatal(err)
	}
	return beginAndConfirmDelivery(t, meeting, answer.Utterance.MessageID, true)
}

func TestUnknownDeliveryIsNeverRedrivenAndResumeBumpsEpoch(t *testing.T) {
	meeting := readyMeeting(t)
	report := noConcernReport(meeting.Participants[0])
	meeting, err := meeting.RecordReport(meeting.Guard(), report, speakerEvidence(meeting, report.Utterance))
	if err != nil {
		t.Fatal(err)
	}
	meeting, err = meeting.BeginCurrentDelivery(meeting.Guard(), report.Utterance.MessageID, meeting.ScheduledAt.Add(2*time.Minute+30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	oldGuard := meeting.Guard()
	meeting, err = meeting.Resume(oldGuard, ResumeEvidence{EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key, ObservedAt: meeting.ScheduledAt.Add(4 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	stored, ok := meeting.findReport(report.Utterance.MessageID)
	if !ok || stored.Utterance.DeliveryState != DeliveryUnknown {
		t.Fatalf("restart during send must become unknown: %#v", stored)
	}
	if meeting.Generation != oldGuard.Generation+1 || meeting.Epoch != oldGuard.Epoch+1 {
		t.Fatal("resume must bump generation and epoch")
	}
	if _, err := meeting.BeginCurrentDelivery(meeting.Guard(), report.Utterance.MessageID, meeting.ScheduledAt.Add(5*time.Minute)); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("unknown delivery must not be sent again: %v", err)
	}
	if _, err := meeting.BeginCurrentDelivery(oldGuard, report.Utterance.MessageID, meeting.ScheduledAt.Add(5*time.Minute)); !errors.Is(err, ErrStale) {
		t.Fatalf("old epoch must be rejected: %v", err)
	}
}

func TestUnknownDeliveryCanOnlyAdvanceAfterReadbackEvidence(t *testing.T) {
	meeting := readyMeeting(t)
	report := noConcernReport(meeting.Participants[0])
	meeting, err := meeting.RecordReport(meeting.Guard(), report, speakerEvidence(meeting, report.Utterance))
	if err != nil {
		t.Fatal(err)
	}
	meeting, err = meeting.BeginCurrentDelivery(meeting.Guard(), report.Utterance.MessageID, meeting.ScheduledAt.Add(2*time.Minute+30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	unknown := DeliveryUnknownEvidence{
		EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key, Generation: meeting.Generation, Epoch: meeting.Epoch,
		MessageID: report.Utterance.MessageID, Reason: "timeout_after_world_write", ObservedAt: meeting.ScheduledAt.Add(4 * time.Minute),
	}
	meeting, err = meeting.MarkCurrentDeliveryUnknown(meeting.Guard(), unknown)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := meeting.MarkCurrentDeliveryUnknown(meeting.Guard(), unknown)
	if err != nil {
		t.Fatalf("exact unknown-delivery evidence replay should be a no-op: %v", err)
	}
	if replayed.ProgressionRevision != meeting.ProgressionRevision {
		t.Fatal("unknown-delivery replay must not advance progression")
	}
	changedUnknown := unknown
	changedUnknown.Reason = "different_timeout_class"
	if _, err := meeting.MarkCurrentDeliveryUnknown(meeting.Guard(), changedUnknown); !errors.Is(err, ErrConflict) {
		t.Fatalf("same unknown-delivery evidence id with changed reason must conflict: %v", err)
	}
	if _, err := meeting.RequestAnswer(meeting.Guard(), ConcernID{}, meeting.Participants[1], false); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("uncertain report must block progression: %v", err)
	}
	// A fresh readback needs a fresh scope and attendance observation.
	meeting = refreshWorldReadiness(t, meeting)
	meeting, err = meeting.ConfirmDelivery(meeting.Guard(), DeliveryEvidence{
		EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key, Generation: meeting.Generation, Epoch: meeting.Epoch,
		ScopeEvidenceID: meeting.Reports[0].Utterance.DeliveryScopeEvidenceID,
		ThreadID:        meeting.ScopeEvidence.ThreadID,
		MessageID:       report.Utterance.MessageID, Delivered: true, Audience: append([]string(nil), meeting.Participants...),
		ObservedAt: meeting.ScheduledAt.Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if meeting.CurrentTurn.ActorID != meeting.Participants[1] || meeting.Status != StatusReporting {
		t.Fatalf("readback-confirmed delivery should advance once: %#v", meeting.CurrentTurn)
	}
}

func TestDeliveryEvidenceReplayChecksFullSemanticPayload(t *testing.T) {
	meeting := readyMeeting(t)
	report := noConcernReport(meeting.Participants[0])
	var err error
	meeting, err = meeting.RecordReport(meeting.Guard(), report, speakerEvidence(meeting, report.Utterance))
	if err != nil {
		t.Fatal(err)
	}
	meeting, err = meeting.BeginCurrentDelivery(meeting.Guard(), report.Utterance.MessageID, meeting.ScheduledAt.Add(2*time.Minute+30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	receipt := DeliveryEvidence{
		EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key, Generation: meeting.Generation, Epoch: meeting.Epoch,
		ScopeEvidenceID: meeting.ScopeEvidence.EvidenceID,
		ThreadID:        meeting.ScopeEvidence.ThreadID, MessageID: report.Utterance.MessageID,
		Delivered: true, Audience: append([]string(nil), meeting.Participants...),
		ObservedAt: meeting.ScheduledAt.Add(3 * time.Minute),
	}
	meeting, err = meeting.ConfirmDelivery(meeting.Guard(), receipt)
	if err != nil {
		t.Fatal(err)
	}
	reversedAudience := receipt
	reversedAudience.Audience = append([]string(nil), receipt.Audience...)
	for left, right := 0, len(reversedAudience.Audience)-1; left < right; left, right = left+1, right-1 {
		reversedAudience.Audience[left], reversedAudience.Audience[right] = reversedAudience.Audience[right], reversedAudience.Audience[left]
	}
	replayed, err := meeting.ConfirmDelivery(meeting.Guard(), reversedAudience)
	if err != nil {
		t.Fatalf("audience ordering should not change set identity on exact replay: %v", err)
	}
	if replayed.ProgressionRevision != meeting.ProgressionRevision {
		t.Fatal("delivery evidence replay must not advance progression")
	}
	changed := receipt
	changed.ThreadID = modulecore.NewThreadID()
	if _, err := meeting.ConfirmDelivery(meeting.Guard(), changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("same delivery evidence id with changed thread must conflict: %v", err)
	}
	changed = receipt
	changed.ObservedAt = changed.ObservedAt.Add(time.Second)
	if _, err := meeting.ConfirmDelivery(meeting.Guard(), changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("same delivery evidence id with changed observation time must conflict: %v", err)
	}
}

func refreshWorldReadiness(t *testing.T, meeting Meeting) Meeting {
	t.Helper()
	var err error
	base := meeting.ScheduledAt.Add(4 * time.Minute)
	meeting, err = meeting.ConfirmWorldScope(meeting.Guard(), WorldScopeEvidence{
		EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key, Generation: meeting.Generation, Epoch: meeting.Epoch,
		Snapshot: testWorldSnapshot(),
		ThreadID: meeting.SharedThreadID, LivingRoomRef: meeting.Rooms.Living, DiningRoomRef: meeting.Rooms.Dining,
		Audience: append([]string(nil), meeting.Participants...), ObservedAt: base, ValidUntil: base.Add(30 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, actorID := range meeting.Participants {
		meeting, err = meeting.RecordPresence(meeting.Guard(), PresenceEvidence{
			EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key, Generation: meeting.Generation, Epoch: meeting.Epoch,
			Snapshot: testWorldSnapshot(),
			ActorID:  actorID, WorldActorID: "world-" + actorID, Present: true, RoomRef: meeting.Rooms.Dining, ObservedAt: base.Add(time.Second), ValidUntil: base.Add(25 * time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return meeting
}

func TestPresenceMustMatchExactWorldSnapshotProvenance(t *testing.T) {
	meeting := readyMeeting(t)
	actorID := meeting.Participants[0]
	source := testWorldSnapshot()
	source.StateSeq++
	_, err := meeting.RecordPresence(meeting.Guard(), PresenceEvidence{
		EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key, Generation: meeting.Generation, Epoch: meeting.Epoch,
		Snapshot: source, ActorID: actorID, WorldActorID: "world-" + actorID, Present: true, RoomRef: meeting.Rooms.Living,
		ObservedAt: meeting.ScopeEvidence.ObservedAt.Add(2 * time.Second), ValidUntil: meeting.ScopeEvidence.ValidUntil.Add(-time.Second),
	})
	if !errors.Is(err, ErrStale) {
		t.Fatalf("presence from a different source snapshot must be rejected as stale: %v", err)
	}
}

func TestPresenceMustMatchExplicitCoreToWorldActorBinding(t *testing.T) {
	meeting := readyMeeting(t)
	actorID := meeting.Participants[0]
	_, err := meeting.RecordPresence(meeting.Guard(), PresenceEvidence{
		EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key, Generation: meeting.Generation, Epoch: meeting.Epoch,
		Snapshot: testWorldSnapshot(), ActorID: actorID, WorldActorID: "another-world-entity", Present: true,
		RoomRef: meeting.Rooms.Living, ObservedAt: meeting.ScopeEvidence.ObservedAt.Add(2 * time.Second), ValidUntil: meeting.ScopeEvidence.ValidUntil.Add(-time.Second),
	})
	if err == nil {
		t.Fatal("presence for a different WORLD entity must be rejected")
	}
}

func TestGenerationIntentIsDurableAndStableAcrossExplicitResume(t *testing.T) {
	meeting := readyMeeting(t)
	requestID := meeting.Key.String() + "|report|" + meeting.CurrentTurn.ActorID
	input := []byte(`{"meeting":"frozen"}`)
	digestBytes := sha256.Sum256(input)
	digest := hex.EncodeToString(digestBytes[:])
	prepared, err := meeting.PrepareCurrentGeneration(meeting.Guard(), requestID, input)
	if err != nil {
		t.Fatal(err)
	}
	progression := prepared.ProgressionRevision
	replay, err := prepared.PrepareCurrentGeneration(prepared.Guard(), requestID, input)
	if err != nil {
		t.Fatal(err)
	}
	if replay.ProgressionRevision != progression {
		t.Fatal("replaying an existing intent must not create another checkpoint")
	}
	if _, err := prepared.PrepareCurrentGeneration(prepared.Guard(), requestID, []byte(`{"meeting":"changed"}`)); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed intent error = %v, want ErrConflict", err)
	}
	resumed, err := prepared.Resume(prepared.Guard(), ResumeEvidence{EvidenceID: modulecore.NewEvidenceID(), Key: prepared.Key, ObservedAt: prepared.ScheduledAt.Add(3 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.CurrentTurn == nil || resumed.CurrentTurn.GenerationRequestID != requestID || resumed.CurrentTurn.GenerationInputSHA256 != digest || string(resumed.CurrentTurn.GenerationInputJSON) != string(input) {
		t.Fatal("explicit recovery did not preserve the exact pending native request")
	}
}

func TestCompletionRequiresAllReportsAnswersAndTerminalArtifactEvidence(t *testing.T) {
	meeting := readyMeeting(t)
	var err error
	for meeting.Status == StatusReporting {
		actorID := meeting.CurrentTurn.ActorID
		report := noConcernReport(actorID)
		if actorID == meeting.Participants[0] {
			report.ConcernDeclaration = ConcernsListed
			report.Concerns = []Concern{{Description: "needs a second actor answer"}}
		}
		meeting = recordAndDeliverReport(t, meeting, report)
		if meeting.Status == StatusAnswering {
			concernID := ConcernID{ReportMessageID: report.Utterance.MessageID, Ordinal: 1}
			meeting, err = answerConcern(t, meeting, concernID, meeting.Participants[1], "確認資料と担当者を明記して再確認します。", AnswerConcreteSupport)
			if err != nil {
				t.Fatal(err)
			}
			meeting, err = meeting.AdvanceReporter(meeting.Guard(), meeting.ScheduledAt.Add(10*time.Minute))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if meeting.Status != StatusFinishing || len(meeting.Reports) != 8 || !meeting.allUtterancesDelivered() {
		t.Fatalf("meeting does not satisfy completion preconditions: status=%s reports=%d", meeting.Status, len(meeting.Reports))
	}
	bad := FinalizationEvidence{
		EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key, Generation: meeting.Generation, Epoch: meeting.Epoch,
		ProgressionRevision: meeting.ProgressionRevision - 1, TranscriptArtifactID: modulecore.NewArtifactID(),
		PersistedAt: meeting.ScheduledAt.Add(20 * time.Minute),
	}
	if _, err := meeting.Finish(meeting.Guard(), bad); err == nil {
		t.Fatal("stale terminal evidence must not complete the meeting")
	}
	good := bad
	good.EvidenceID = modulecore.NewEvidenceID()
	good.ProgressionRevision = meeting.ProgressionRevision
	completed, err := meeting.Finish(meeting.Guard(), good)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != StatusCompleted || completed.FinishEvidence == nil {
		t.Fatal("expected persisted terminal evidence to complete meeting")
	}
}

func TestDeadlineCreatesTerminalPartialAndLateReportCannotReopen(t *testing.T) {
	meeting := readyMeeting(t)
	evidence := PartialEvidence{
		EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key, Generation: meeting.Generation, Epoch: meeting.Epoch,
		ObservedAt: meeting.DeadlineAt.Add(time.Second), ReasonCode: "deadline",
	}
	partial, err := meeting.MarkPartial(meeting.Guard(), evidence)
	if err != nil {
		t.Fatal(err)
	}
	if partial.Status != StatusPartial || len(partial.PartialEvidence.Unresolved) == 0 {
		t.Fatalf("partial record should preserve unresolved work: %#v", partial.PartialEvidence)
	}
	late := noConcernReport(meeting.Participants[0])
	if _, err := partial.RecordReport(partial.Guard(), late, speakerEvidence(partial, late.Utterance)); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("late report must not reopen terminal occurrence: %v", err)
	}
	if _, err := partial.Finish(partial.Guard(), FinalizationEvidence{}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("partial occurrence must not become completed later: %v", err)
	}
}

func TestMissedOccurrenceIsTerminalWithoutCatchUp(t *testing.T) {
	config := testConfig(1)
	meeting, err := NewOccurrence(config, testKey(config))
	if err != nil {
		t.Fatal(err)
	}
	missed, err := meeting.MarkPartial(meeting.Guard(), PartialEvidence{
		EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key, Generation: meeting.Generation, Epoch: meeting.Epoch,
		ObservedAt: meeting.DeadlineAt, ReasonCode: "missed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if missed.Status != StatusMissed {
		t.Fatalf("scheduled occurrence that passed deadline should be missed: %q", missed.Status)
	}
	if _, err := missed.BeginGathering(missed.Guard(), missed.ScheduledAt); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("missed slot must not catch up: %v", err)
	}
}

func TestTaskAndRunRefsBindWithoutDomainIDGeneration(t *testing.T) {
	meeting := readyMeeting(t)
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	bound, err := meeting.BindNativeReferences(meeting.Guard(), &taskID, &runID)
	if err != nil {
		t.Fatal(err)
	}
	if bound.TaskRef == nil || *bound.TaskRef != taskID || bound.RunRef == nil || *bound.RunRef != runID {
		t.Fatal("native task manager references were not bound")
	}
	if fmt.Sprint(bound.Key) == "" {
		t.Fatal("stable occurrence identity must be present")
	}
}
