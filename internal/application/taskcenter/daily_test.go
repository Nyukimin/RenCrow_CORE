package taskcenter

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	tool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestDailyReadWorksWithoutManagedGoalService(t *testing.T) {
	now := time.Date(2026, time.November, 17, 12, 0, 0, 0, time.UTC)
	definitions := []DailyDefinitionDescriptor{
		dailyDefinitionDescriptor("routine.disabled", tool.DataScopePublic, "", DailyAxis{State: DailyImplementationImplemented, SourceRef: "owner.heartbeat"}),
		dailyDefinitionDescriptor("routine.unimplemented", tool.DataScopePublic, "", DailyAxis{State: DailyImplementationUnimplemented, SourceRef: "owner.meeting"}),
	}
	source := &dailyTestSource{descriptor: DailySourceDescriptor{SourceID: "daily.owner", Definitions: definitions}}
	source.snapshot = DailySourceSnapshot{
		ObservedAt: now,
		Observations: []DailyObservation{
			dailyObservation("routine.disabled", DailyAxis{State: DailyConfiguredDisabled, SourceRef: "heartbeat.config"}, DailyAxis{State: DailyEffectiveDisabled, SourceRef: "heartbeat.gate"}, DailyAxis{State: DailyObservedNotObserved, SourceRef: "heartbeat.receipt", ReasonCode: "disabled"}),
			dailyObservation("routine.unimplemented", DailyAxis{State: DailyConfiguredUnconfigured, SourceRef: "meeting.config"}, DailyAxis{State: DailyEffectiveUnsupported, SourceRef: "meeting.runtime", ReasonCode: "not_implemented"}, DailyAxis{State: DailyObservedUnsupported, SourceRef: "meeting.history", ReasonCode: "not_implemented"}),
		},
	}
	center, err := NewCenter(nil, []DailySource{source}, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewCenter() error = %v", err)
	}
	ctx := dailyTestAgentContext(t, "daily-no-managed", "mio", nil)
	result, err := center.ListDaily(ctx, DailyQuery{})
	if err != nil {
		t.Fatalf("ListDaily() error = %v", err)
	}
	if result.Availability != DailyAvailabilityAvailable || result.Partial || len(result.Definitions) != 2 {
		t.Fatalf("ListDaily() = %#v, want both registered definitions with available status", result)
	}
	if result.Definitions[0].DefinitionID != "routine.disabled" || result.Definitions[0].Configured.State != DailyConfiguredDisabled {
		t.Fatalf("disabled owner definition was lost or changed: %#v", result.Definitions[0])
	}
	if result.Definitions[1].Implementation.State != DailyImplementationUnimplemented || result.Definitions[1].Configured.State != DailyConfiguredUnconfigured || result.Definitions[1].Observed.State != DailyObservedUnsupported {
		t.Fatalf("unimplemented owner definition was lost or promoted: %#v", result.Definitions[1])
	}
	if !result.Definitions[0].TargetCount.Known() || *result.Definitions[0].TargetCount.Value != 0 {
		t.Fatalf("zero target count = %#v, want known zero", result.Definitions[0].TargetCount)
	}
	if result.Definitions[0].LatestEvidence.State != DailyValueUnknown || result.Definitions[0].LatestEvidence.LastSuccessAt.State != DailyValueUnknown {
		t.Fatalf("empty retained history was promoted: %#v", result.Definitions[0].LatestEvidence)
	}
	caps, err := center.Capabilities(ctx)
	if err != nil {
		t.Fatalf("Capabilities() error = %v", err)
	}
	if caps.ManagedAvailable || !caps.DailyAvailable || caps.CanSubmit || caps.CanClaim || caps.ManagedReasonCode != "managed_owner_unavailable" {
		t.Fatalf("managed-nil capabilities = %#v", caps)
	}
	if got := source.calls; got != 1 {
		t.Fatalf("DailySource Snapshot calls = %d, want one read", got)
	}
}

func TestDailyReadPreservesBoundedNativeScheduleAndOccurrenceMetadata(t *testing.T) {
	definition := dailyDefinitionDescriptor("routine.refs", tool.DataScopePublic, "", DailyAxis{State: DailyImplementationImplemented, SourceRef: "owner.refs"})
	definition.SourceRefs = []DailyResourceRef{{
		OwnerRef: definition.OwnerRef, ResourceType: "config", Reference: "heartbeat.config",
		DisplayName: "Heartbeat configuration", Filename: "heartbeat.yaml", LinkRef: "owner:heartbeat/config", Revision: "r3",
	}}
	observation := dailyObservation("routine.refs", DailyAxis{State: DailyConfiguredConfigured, SourceRef: "config"}, DailyAxis{State: DailyEffectiveEnabled, SourceRef: "runtime"}, DailyAxis{State: DailyObservedAvailable, SourceRef: "history"})
	observation.NativeRefs = []DailyResourceRef{{
		OwnerRef: definition.OwnerRef, ResourceType: "schedule", NativeID: "heartbeat-schedule-1", DataScope: tool.DataScopePublic,
	}}
	observation.NativeRefCount = DailyValue[int]{State: DailyValueKnown, SourceRef: "schedules.count", Value: dailyPointer(3)}
	observation.NativeRefsTruncated = true
	occurrence := DailyOccurrence{
		OccurrenceID: "run-20261117-01", State: DailyOccurrenceSucceeded,
		NextRunAt:     DailyValue[time.Time]{State: DailyValueUnsupported, SourceRef: "runs.next", ReasonCode: "one_shot_run"},
		LastAttemptAt: DailyValue[time.Time]{State: DailyValueUnknown, SourceRef: "runs.last_attempt", ReasonCode: "retention_unknown"},
		LastSuccessAt: DailyValue[time.Time]{State: DailyValueUnknown, SourceRef: "runs.last_success", ReasonCode: "retention_unknown"},
		Result:        DailyValue[DailyOutcome]{State: DailyValueKnown, SourceRef: "runs.result", Value: dailyPointer(DailyOutcomeSuccess)},
		NativeRefs:    []DailyResourceRef{{OwnerRef: definition.OwnerRef, ResourceType: "run", NativeID: "native-run-1", DataScope: tool.DataScopePublic}},
	}
	observation.Occurrences = []DailyOccurrence{occurrence}
	observation.OccurrenceCount = DailyValue[int]{State: DailyValueKnown, SourceRef: "runs.count", Value: dailyPointer(10)}
	observation.OccurrencesTruncated = true
	source := &dailyTestSource{
		descriptor: DailySourceDescriptor{SourceID: "daily.refs", Definitions: []DailyDefinitionDescriptor{definition}},
		snapshot:   DailySourceSnapshot{Observations: []DailyObservation{observation}},
	}
	center, err := NewCenter(nil, []DailySource{source}, time.Now)
	if err != nil {
		t.Fatalf("NewCenter() error = %v", err)
	}
	result, err := center.ListDaily(dailyTestAgentContext(t, "daily-ref-counts", "mio", nil), DailyQuery{})
	if err != nil || len(result.Definitions) != 1 {
		t.Fatalf("ListDaily() = %#v, error = %v", result, err)
	}
	row := result.Definitions[0]
	if *row.NativeRefCount.Value != 3 || !row.NativeRefsTruncated || *row.OccurrenceCount.Value != 10 || !row.OccurrencesTruncated {
		t.Fatalf("native reference / occurrence count metadata lost: %#v", row)
	}
	if len(row.Occurrences) != 1 || row.Occurrences[0].OccurrenceID != occurrence.OccurrenceID || row.Occurrences[0].State != DailyOccurrenceSucceeded {
		t.Fatalf("native occurrence projection = %#v", row.Occurrences)
	}
	if row.SourceRefs[0].Filename != "heartbeat.yaml" || row.SourceRefs[0].LinkRef != "owner:heartbeat/config" || row.NativeRefs[0].NativeID != "heartbeat-schedule-1" {
		t.Fatalf("owner reference metadata = source:%#v native:%#v", row.SourceRefs, row.NativeRefs)
	}
	encoded := string(mustJSON(t, row))
	if strings.Contains(encoded, `"goal_id"`) {
		t.Fatalf("daily definition invented a managed Goal id: %s", encoded)
	}
	if strings.Contains(encoded, `"scheduled_at"`) {
		t.Fatalf("legacy occurrence without a scheduled slot was populated: %s", encoded)
	}
}

func TestDailyOccurrenceScheduledAtUsesDailyValueValidation(t *testing.T) {
	scheduledTime := time.Date(2026, time.November, 18, 9, 30, 0, 0, time.UTC)
	zeroTime := time.Time{}
	cases := []struct {
		name        string
		scheduledAt *DailyValue[time.Time]
		valid       bool
	}{
		{name: "legacy omitted", valid: true},
		{name: "known timestamp", scheduledAt: dailyPointer(DailyValue[time.Time]{State: DailyValueKnown, SourceRef: "meeting.scheduled_at", Value: &scheduledTime}), valid: true},
		{name: "none", scheduledAt: dailyPointer(DailyValue[time.Time]{State: DailyValueNone, SourceRef: "meeting.scheduled_at"}), valid: true},
		{name: "unknown", scheduledAt: dailyPointer(DailyValue[time.Time]{State: DailyValueUnknown, SourceRef: "meeting.scheduled_at", ReasonCode: "slot_unknown"}), valid: true},
		{name: "unsupported", scheduledAt: dailyPointer(DailyValue[time.Time]{State: DailyValueUnsupported, SourceRef: "meeting.scheduled_at", ReasonCode: "slot_unsupported"}), valid: true},
		{name: "known nil", scheduledAt: dailyPointer(DailyValue[time.Time]{State: DailyValueKnown, SourceRef: "meeting.scheduled_at"})},
		{name: "known zero timestamp", scheduledAt: dailyPointer(DailyValue[time.Time]{State: DailyValueKnown, SourceRef: "meeting.scheduled_at", Value: &zeroTime})},
		{name: "none with value", scheduledAt: dailyPointer(DailyValue[time.Time]{State: DailyValueNone, SourceRef: "meeting.scheduled_at", Value: &scheduledTime})},
		{name: "unknown with value", scheduledAt: dailyPointer(DailyValue[time.Time]{State: DailyValueUnknown, SourceRef: "meeting.scheduled_at", Value: &scheduledTime})},
		{name: "unsupported with value", scheduledAt: dailyPointer(DailyValue[time.Time]{State: DailyValueUnsupported, SourceRef: "meeting.scheduled_at", Value: &scheduledTime})},
		{name: "missing source reference", scheduledAt: dailyPointer(DailyValue[time.Time]{State: DailyValueUnknown, ReasonCode: "slot_unknown"})},
		{name: "unsafe source reference", scheduledAt: dailyPointer(DailyValue[time.Time]{State: DailyValueUnknown, SourceRef: "file:/Users/person/private.db"})},
		{name: "unsafe reason code", scheduledAt: dailyPointer(DailyValue[time.Time]{State: DailyValueUnknown, SourceRef: "meeting.scheduled_at", ReasonCode: "private reason"})},
		{name: "invalid state", scheduledAt: dailyPointer(DailyValue[time.Time]{State: DailyValueState("invalid"), SourceRef: "meeting.scheduled_at"})},
	}
	definition := dailyDefinitionDescriptor("routine.scheduled-at", tool.DataScopePublic, "", DailyAxis{State: DailyImplementationImplemented, SourceRef: "owner.scheduled-at"})
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			occurrence := dailyCountTestOccurrences(1)[0]
			occurrence.ScheduledAt = test.scheduledAt
			err := validateDailyOccurrence(occurrence, definition)
			if test.valid && err != nil {
				t.Fatalf("valid optional scheduled_at rejected: %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("invalid optional scheduled_at accepted")
			}
		})
	}
}

func TestDailyOccurrenceScheduledAtProjectsAndDeepClones(t *testing.T) {
	now := time.Date(2026, time.November, 18, 10, 0, 0, 0, time.UTC)
	scheduledTime := time.Date(2026, time.November, 20, 9, 30, 0, 0, time.UTC)
	originalScheduledTime := scheduledTime
	scheduledAt := DailyValue[time.Time]{State: DailyValueKnown, SourceRef: "meeting.scheduled_at", Value: &scheduledTime}
	definition := dailyDefinitionDescriptor("routine.scheduled-at", tool.DataScopePublic, "", DailyAxis{State: DailyImplementationImplemented, SourceRef: "owner.scheduled-at"})
	observation := dailyObservation(definition.DefinitionID,
		DailyAxis{State: DailyConfiguredConfigured, SourceRef: "meeting.config"},
		DailyAxis{State: DailyEffectiveEnabled, SourceRef: "meeting.runtime"},
		DailyAxis{State: DailyObservedAvailable, SourceRef: "meeting.history"})
	occurrence := dailyCountTestOccurrences(1)[0]
	occurrence.ScheduledAt = &scheduledAt
	observation.Occurrences = []DailyOccurrence{occurrence}
	observation.OccurrenceCount = DailyValue[int]{State: DailyValueKnown, SourceRef: "meeting.occurrences", Value: dailyPointer(1)}
	source := &dailyTestSource{
		descriptor: DailySourceDescriptor{SourceID: "daily.meeting", Definitions: []DailyDefinitionDescriptor{definition}},
		snapshot:   DailySourceSnapshot{ObservedAt: now, Observations: []DailyObservation{observation}},
	}
	center, err := NewCenter(nil, []DailySource{source}, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewCenter() error = %v", err)
	}
	result, err := center.ListDaily(dailyTestAgentContext(t, "daily-scheduled-at", "mio", nil), DailyQuery{})
	if err != nil || len(result.Definitions) != 1 || len(result.Definitions[0].Occurrences) != 1 {
		t.Fatalf("ListDaily() = %#v, error = %v", result, err)
	}
	projected := result.Definitions[0].Occurrences[0].ScheduledAt
	if projected == nil || projected == occurrence.ScheduledAt || projected.Value == nil || projected.Value == occurrence.ScheduledAt.Value {
		t.Fatalf("scheduled_at wrapper/value was not deeply cloned: input=%#v output=%#v", occurrence.ScheduledAt, projected)
	}
	if projected.State != DailyValueKnown || !projected.Value.Equal(originalScheduledTime) {
		t.Fatalf("projected scheduled_at = %#v, want known %s", projected, originalScheduledTime)
	}
	encoded := string(mustJSON(t, result.Definitions[0].Occurrences[0]))
	if !strings.Contains(encoded, "\"scheduled_at\":{\"state\":\"known\"") || !strings.Contains(encoded, scheduledTime.Format(time.RFC3339Nano)) {
		t.Fatalf("scheduled_at was not serialized in the occurrence DTO: %s", encoded)
	}
	inputTime := originalScheduledTime.Add(time.Hour)
	scheduledAt.State = DailyValueUnknown
	*scheduledAt.Value = inputTime
	if projected.State != DailyValueKnown || !projected.Value.Equal(originalScheduledTime) {
		t.Fatalf("input mutation changed projected scheduled_at: %#v", projected)
	}
	outputTime := originalScheduledTime.Add(2 * time.Hour)
	projected.State = DailyValueUnsupported
	*projected.Value = outputTime
	if scheduledAt.State != DailyValueUnknown || !scheduledAt.Value.Equal(inputTime) {
		t.Fatalf("projected mutation changed source scheduled_at: %#v", scheduledAt)
	}
}

func TestDailyMeetingProgressPreservesKnownUnknownAndLegacyStates(t *testing.T) {
	now := time.Date(2026, time.November, 18, 10, 0, 0, 0, time.UTC)
	description := dailyDefinitionDescriptor("meeting.progress", tool.DataScopeUser, "owner-a", DailyAxis{State: DailyImplementationImplemented, SourceRef: "owner.taskmeeting"})
	observation := dailyObservation(description.DefinitionID,
		DailyAxis{State: DailyConfiguredConfigured, SourceRef: "meeting.config"},
		DailyAxis{State: DailyEffectiveEnabled, SourceRef: "meeting.scheduler"},
		DailyAxis{State: DailyObservedAvailable, SourceRef: "meeting.workstream"})
	occurrence := dailyCountTestOccurrences(1)[0]
	attendance, attendanceTotal := 3, 8
	reports, reportTotal := 5, 8
	answered, concernTotal, unresolved := 1, 2, 1
	minutesRef := DailyResourceRef{
		SourceID: "daily.meeting", OwnerRef: description.OwnerRef, ResourceType: "meeting_minutes",
		Reference: "workstream.taskmeeting.minutes", NativeID: "artifact-meeting-minutes",
		DataScope: tool.DataScopeUser, UserID: "owner-a", DisplayName: "Minutes reference",
	}
	progress := DailyMeetingProgress{
		Attendance: DailyProgressCount{
			Current: DailyValue[int]{State: DailyValueKnown, SourceRef: "meeting.attendance", Value: &attendance},
			Total:   DailyValue[int]{State: DailyValueKnown, SourceRef: "meeting.roster", Value: &attendanceTotal},
		},
		DeliveredReports: DailyProgressCount{
			Current: DailyValue[int]{State: DailyValueKnown, SourceRef: "meeting.reports.delivered", Value: &reports},
			Total:   DailyValue[int]{State: DailyValueKnown, SourceRef: "meeting.roster", Value: &reportTotal},
		},
		AnsweredConcerns: DailyProgressCount{
			Current: DailyValue[int]{State: DailyValueKnown, SourceRef: "meeting.concerns.answered", Value: &answered},
			Total:   DailyValue[int]{State: DailyValueKnown, SourceRef: "meeting.concerns.reported", Value: &concernTotal},
		},
		UnresolvedConcerns: DailyValue[int]{State: DailyValueKnown, SourceRef: "meeting.concerns.unresolved", Value: &unresolved},
		Minutes:            DailyValue[DailyResourceRef]{State: DailyValueKnown, SourceRef: "workstream.taskmeeting.minutes", Value: &minutesRef},
	}
	occurrence.MeetingProgress = &progress
	observation.Occurrences = []DailyOccurrence{occurrence}
	observation.OccurrenceCount = DailyValue[int]{State: DailyValueKnown, SourceRef: "meeting.occurrences", Value: dailyPointer(1)}
	source := &dailyTestSource{
		descriptor: DailySourceDescriptor{SourceID: "daily.meeting", Definitions: []DailyDefinitionDescriptor{description}},
		snapshot:   DailySourceSnapshot{ObservedAt: now, Observations: []DailyObservation{observation}},
	}
	center, err := NewCenter(nil, []DailySource{source}, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewCenter() error = %v", err)
	}
	result, err := center.ListDaily(dailyTestUserContext(t, "meeting-progress-read", "owner-a"), DailyQuery{})
	if err != nil || len(result.Definitions) != 1 || len(result.Definitions[0].Occurrences) != 1 {
		t.Fatalf("ListDaily() = %#v, error = %v", result, err)
	}
	projected := result.Definitions[0].Occurrences[0].MeetingProgress
	if projected == nil || projected.Attendance.Current.Value == nil || *projected.Attendance.Current.Value != 3 ||
		projected.DeliveredReports.Current.Value == nil || *projected.DeliveredReports.Current.Value != 5 ||
		projected.AnsweredConcerns.Total.Value == nil || *projected.AnsweredConcerns.Total.Value != 2 ||
		projected.UnresolvedConcerns.Value == nil || *projected.UnresolvedConcerns.Value != 1 ||
		projected.Minutes.Value == nil || projected.Minutes.Value.NativeID != "artifact-meeting-minutes" {
		t.Fatalf("meeting progress projection = %#v", projected)
	}
	attendance = 7
	minutesRef.NativeID = "artifact-changed-after-snapshot"
	if *projected.Attendance.Current.Value != 3 || projected.Minutes.Value.NativeID != "artifact-meeting-minutes" {
		t.Fatalf("source mutation changed copied meeting progress: %#v", projected)
	}
	encoded := string(mustJSON(t, projected))
	if !strings.Contains(encoded, `"current":{"state":"known"`) || !strings.Contains(encoded, `"value":3`) || !strings.Contains(encoded, `"native_id":"artifact-meeting-minutes"`) {
		t.Fatalf("meeting progress JSON lost state/ref details: %s", encoded)
	}

	unknown := DailyMeetingProgress{
		Attendance: DailyProgressCount{
			Current: DailyValue[int]{State: DailyValueUnknown, SourceRef: "meeting.attendance", ReasonCode: "scope_not_observed"},
			Total:   DailyValue[int]{State: DailyValueKnown, SourceRef: "meeting.roster", Value: dailyPointer(8)},
		},
		DeliveredReports: DailyProgressCount{
			Current: DailyValue[int]{State: DailyValueUnknown, SourceRef: "meeting.reports", ReasonCode: "not_exposed"},
			Total:   DailyValue[int]{State: DailyValueKnown, SourceRef: "meeting.roster", Value: dailyPointer(8)},
		},
		AnsweredConcerns: DailyProgressCount{
			Current: DailyValue[int]{State: DailyValueUnknown, SourceRef: "meeting.concerns.answered", ReasonCode: "not_exposed"},
			Total:   DailyValue[int]{State: DailyValueUnknown, SourceRef: "meeting.concerns.reported", ReasonCode: "not_exposed"},
		},
		UnresolvedConcerns: DailyValue[int]{State: DailyValueUnknown, SourceRef: "meeting.concerns.unresolved", ReasonCode: "not_exposed"},
		Minutes:            DailyValue[DailyResourceRef]{State: DailyValueUnknown, SourceRef: "workstream.taskmeeting.minutes", ReasonCode: "not_observed"},
	}
	encodedUnknown := string(mustJSON(t, unknown))
	if !strings.Contains(encodedUnknown, `"state":"unknown"`) || !strings.Contains(encodedUnknown, `"value":null`) || strings.Contains(encodedUnknown, `"value":0`) {
		t.Fatalf("unknown meeting values were converted to zero: %s", encodedUnknown)
	}
	legacyOccurrence := dailyCountTestOccurrences(1)[0]
	if encodedLegacy := string(mustJSON(t, legacyOccurrence)); strings.Contains(encodedLegacy, `"meeting_progress"`) {
		t.Fatalf("legacy occurrence without owner progress should omit the optional field: %s", encodedLegacy)
	}
}

func TestDailyMeetingProgressRejectsInvalidCountsAndReferences(t *testing.T) {
	descriptor := dailyDefinitionDescriptor("meeting.progress-invalid", tool.DataScopeUser, "owner-a", DailyAxis{State: DailyImplementationImplemented, SourceRef: "owner.taskmeeting"})
	observation := dailyObservation(descriptor.DefinitionID,
		DailyAxis{State: DailyConfiguredConfigured, SourceRef: "meeting.config"},
		DailyAxis{State: DailyEffectiveEnabled, SourceRef: "meeting.scheduler"},
		DailyAxis{State: DailyObservedAvailable, SourceRef: "meeting.workstream"})
	occurrence := dailyCountTestOccurrences(1)[0]
	current, total := 2, 1
	minutes := DailyResourceRef{OwnerRef: descriptor.OwnerRef, ResourceType: "meeting_minutes", Reference: "javascript:alert(1)", DataScope: tool.DataScopeUser, UserID: "owner-a"}
	occurrence.MeetingProgress = &DailyMeetingProgress{
		Attendance: DailyProgressCount{
			Current: DailyValue[int]{State: DailyValueKnown, SourceRef: "meeting.attendance", Value: &current},
			Total:   DailyValue[int]{State: DailyValueKnown, SourceRef: "meeting.roster", Value: &total},
		},
		DeliveredReports:   DailyProgressCount{Current: DailyValue[int]{State: DailyValueNone, SourceRef: "meeting.reports"}, Total: DailyValue[int]{State: DailyValueKnown, SourceRef: "meeting.roster", Value: &total}},
		AnsweredConcerns:   DailyProgressCount{Current: DailyValue[int]{State: DailyValueNone, SourceRef: "meeting.answered"}, Total: DailyValue[int]{State: DailyValueKnown, SourceRef: "meeting.concerns", Value: dailyPointer(0)}},
		UnresolvedConcerns: DailyValue[int]{State: DailyValueKnown, SourceRef: "meeting.unresolved", Value: dailyPointer(0)},
		Minutes:            DailyValue[DailyResourceRef]{State: DailyValueKnown, SourceRef: "meeting.minutes", Value: &minutes},
	}
	observation.Occurrences = []DailyOccurrence{occurrence}
	if err := validateDailyObservation(observation, descriptor); err == nil {
		t.Fatal("invalid meeting counts/reference were accepted")
	}
}

func TestDailyMeetingProgressFiltersForeignMinutesReference(t *testing.T) {
	now := time.Date(2026, time.November, 18, 10, 0, 0, 0, time.UTC)
	descriptor := dailyDefinitionDescriptor("meeting.progress-private", tool.DataScopeUser, "owner-a", DailyAxis{State: DailyImplementationImplemented, SourceRef: "owner.taskmeeting"})
	observation := dailyObservation(descriptor.DefinitionID,
		DailyAxis{State: DailyConfiguredConfigured, SourceRef: "meeting.config"},
		DailyAxis{State: DailyEffectiveEnabled, SourceRef: "meeting.scheduler"},
		DailyAxis{State: DailyObservedAvailable, SourceRef: "meeting.workstream"})
	occurrence := dailyCountTestOccurrences(1)[0]
	zero, eight := 0, 8
	foreignMinutes := DailyResourceRef{
		OwnerRef: descriptor.OwnerRef, ResourceType: "meeting_minutes", Reference: "workstream.taskmeeting.minutes",
		NativeID: "artifact-private-owner-b", DataScope: tool.DataScopeUser, UserID: "owner-b",
	}
	occurrence.MeetingProgress = &DailyMeetingProgress{
		Attendance: DailyProgressCount{
			Current: DailyValue[int]{State: DailyValueUnknown, SourceRef: "meeting.attendance", ReasonCode: "not_observed"},
			Total:   DailyValue[int]{State: DailyValueKnown, SourceRef: "meeting.roster", Value: &eight},
		},
		DeliveredReports: DailyProgressCount{
			Current: DailyValue[int]{State: DailyValueKnown, SourceRef: "meeting.reports", Value: &zero},
			Total:   DailyValue[int]{State: DailyValueKnown, SourceRef: "meeting.roster", Value: &eight},
		},
		AnsweredConcerns: DailyProgressCount{
			Current: DailyValue[int]{State: DailyValueKnown, SourceRef: "meeting.answered", Value: &zero},
			Total:   DailyValue[int]{State: DailyValueKnown, SourceRef: "meeting.concerns", Value: &zero},
		},
		UnresolvedConcerns: DailyValue[int]{State: DailyValueKnown, SourceRef: "meeting.unresolved", Value: &zero},
		Minutes:            DailyValue[DailyResourceRef]{State: DailyValueKnown, SourceRef: "meeting.minutes", Value: &foreignMinutes},
	}
	observation.Occurrences = []DailyOccurrence{occurrence}
	observation.OccurrenceCount = DailyValue[int]{State: DailyValueKnown, SourceRef: "meeting.occurrences", Value: dailyPointer(1)}
	source := &dailyTestSource{
		descriptor: DailySourceDescriptor{SourceID: "daily.meeting", Definitions: []DailyDefinitionDescriptor{descriptor}},
		snapshot:   DailySourceSnapshot{ObservedAt: now, Observations: []DailyObservation{observation}},
	}
	center, err := NewCenter(nil, []DailySource{source}, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewCenter() error = %v", err)
	}
	result, err := center.ListDaily(dailyTestUserContext(t, "meeting-progress-scope", "owner-a"), DailyQuery{})
	if err != nil || len(result.Definitions) != 1 || len(result.Definitions[0].Occurrences) != 1 {
		t.Fatalf("ListDaily() = %#v, error = %v", result, err)
	}
	minutes := result.Definitions[0].Occurrences[0].MeetingProgress.Minutes
	if minutes.State != DailyValueUnknown || minutes.Value != nil || minutes.ReasonCode != "scope_filtered" {
		t.Fatalf("foreign minutes ref was not hidden as unknown: %#v", minutes)
	}
	if encoded := string(mustJSON(t, result.Definitions[0])); strings.Contains(encoded, "artifact-private-owner-b") {
		t.Fatalf("foreign artifact reference leaked in Daily JSON: %s", encoded)
	}
}

func TestDailyReadBoundedCountStateMatrix(t *testing.T) {
	knownOne, knownTwo, knownThree, knownNegative := 1, 2, 3, -1
	cases := []struct {
		name          string
		state         DailyValueState
		value         *int
		reason        string
		truncated     bool
		payloadLength int
		valid         bool
	}{
		{name: "truncated known equal", state: DailyValueKnown, value: &knownTwo, truncated: true, payloadLength: 2, valid: true},
		{name: "truncated known greater", state: DailyValueKnown, value: &knownThree, truncated: true, payloadLength: 2, valid: true},
		{name: "truncated known less", state: DailyValueKnown, value: &knownOne, truncated: true, payloadLength: 2},
		{name: "truncated known negative", state: DailyValueKnown, value: &knownNegative, truncated: true, payloadLength: 2},
		{name: "truncated unknown", state: DailyValueUnknown, reason: "total_unknown", truncated: true, payloadLength: 2, valid: true},
		{name: "truncated unsupported", state: DailyValueUnsupported, reason: "total_unsupported", truncated: true, payloadLength: 2, valid: true},
		{name: "truncated none empty", state: DailyValueNone, truncated: true},
		{name: "truncated none nonempty", state: DailyValueNone, truncated: true, payloadLength: 2},
		{name: "truncated unknown with value", state: DailyValueUnknown, value: &knownTwo, truncated: true, payloadLength: 2},
		{name: "truncated unsupported with value", state: DailyValueUnsupported, value: &knownTwo, truncated: true, payloadLength: 2},
		{name: "untruncated known exact", state: DailyValueKnown, value: &knownTwo, payloadLength: 2, valid: true},
		{name: "untruncated known mismatch", state: DailyValueKnown, value: &knownThree, payloadLength: 2},
		{name: "untruncated known negative", state: DailyValueKnown, value: &knownNegative, payloadLength: 2},
		{name: "untruncated unknown", state: DailyValueUnknown, reason: "total_unknown", payloadLength: 2, valid: true},
		{name: "untruncated unsupported", state: DailyValueUnsupported, reason: "total_unsupported", payloadLength: 2, valid: true},
		{name: "untruncated none empty", state: DailyValueNone, value: nil, payloadLength: 0, valid: true},
		{name: "untruncated none nonempty", state: DailyValueNone, payloadLength: 2},
		{name: "untruncated unknown with value", state: DailyValueUnknown, value: &knownTwo, payloadLength: 2},
		{name: "untruncated unsupported with value", state: DailyValueUnsupported, value: &knownTwo, payloadLength: 2},
	}
	if DailyMaxNativeRefs != 64 || DailyMaxOccurrences != 50 {
		t.Fatalf("daily bounds = native refs %d, occurrences %d; want 64 and 50", DailyMaxNativeRefs, DailyMaxOccurrences)
	}
	definition := dailyDefinitionDescriptor("routine.count-matrix", tool.DataScopePublic, "", DailyAxis{State: DailyImplementationImplemented, SourceRef: "owner.count-matrix"})
	for _, collection := range []string{"native_refs", "occurrences"} {
		for _, test := range cases {
			t.Run(collection+"/"+test.name, func(t *testing.T) {
				observation := dailyObservation(definition.DefinitionID,
					DailyAxis{State: DailyConfiguredConfigured, SourceRef: "config.count-matrix"},
					DailyAxis{State: DailyEffectiveEnabled, SourceRef: "runtime.count-matrix"},
					DailyAxis{State: DailyObservedAvailable, SourceRef: "history.count-matrix"})
				if collection == "native_refs" {
					observation.NativeRefs = dailyCountTestRefs(definition.OwnerRef, test.payloadLength)
					observation.NativeRefCount = DailyValue[int]{State: test.state, SourceRef: "counts.native", ReasonCode: test.reason, Value: test.value}
					observation.NativeRefsTruncated = test.truncated
				} else {
					observation.Occurrences = dailyCountTestOccurrences(test.payloadLength)
					observation.OccurrenceCount = DailyValue[int]{State: test.state, SourceRef: "counts.occurrences", ReasonCode: test.reason, Value: test.value}
					observation.OccurrencesTruncated = test.truncated
				}
				err := validateDailyObservation(observation, definition)
				if test.valid && err != nil {
					t.Fatalf("valid count projection rejected: %v", err)
				}
				if !test.valid && err == nil {
					t.Fatal("invalid count projection accepted")
				}
			})
		}
	}
	t.Run("native refs above exported bound", func(t *testing.T) {
		observation := dailyObservation(definition.DefinitionID,
			DailyAxis{State: DailyConfiguredConfigured, SourceRef: "config.count-matrix"},
			DailyAxis{State: DailyEffectiveEnabled, SourceRef: "runtime.count-matrix"},
			DailyAxis{State: DailyObservedAvailable, SourceRef: "history.count-matrix"})
		observation.NativeRefs = dailyCountTestRefs(definition.OwnerRef, DailyMaxNativeRefs+1)
		observation.NativeRefCount = DailyValue[int]{State: DailyValueKnown, SourceRef: "counts.native", Value: dailyPointer(DailyMaxNativeRefs + 1)}
		if err := validateDailyObservation(observation, definition); err == nil {
			t.Fatal("native reference payload above exported bound accepted")
		}
	})
	t.Run("occurrences above exported bound", func(t *testing.T) {
		observation := dailyObservation(definition.DefinitionID,
			DailyAxis{State: DailyConfiguredConfigured, SourceRef: "config.count-matrix"},
			DailyAxis{State: DailyEffectiveEnabled, SourceRef: "runtime.count-matrix"},
			DailyAxis{State: DailyObservedAvailable, SourceRef: "history.count-matrix"})
		observation.Occurrences = dailyCountTestOccurrences(DailyMaxOccurrences + 1)
		observation.OccurrenceCount = DailyValue[int]{State: DailyValueKnown, SourceRef: "counts.occurrences", Value: dailyPointer(DailyMaxOccurrences + 1)}
		if err := validateDailyObservation(observation, definition); err == nil {
			t.Fatal("occurrence payload above exported bound accepted")
		}
	})
}

func TestDailyReadKeepsOwnerMetadataForUnknownTruncatedTotals(t *testing.T) {
	now := time.Date(2026, time.November, 17, 12, 0, 0, 0, time.UTC)
	definition := dailyDefinitionDescriptor("routine.truncated", tool.DataScopePublic, "", DailyAxis{State: DailyImplementationImplemented, SourceRef: "owner.truncated"})
	definition.SourceRefs = append(definition.SourceRefs, DailyResourceRef{
		OwnerRef: definition.OwnerRef, ResourceType: "config", Reference: "settings.daily",
		DataScope: tool.DataScopePublic,
	})
	observation := dailyObservation(definition.DefinitionID,
		DailyAxis{State: DailyConfiguredConfigured, SourceRef: "config.truncated"},
		DailyAxis{State: DailyEffectiveEnabled, SourceRef: "runtime.truncated"},
		DailyAxis{State: DailyObservedAvailable, SourceRef: "history.truncated"})
	observation.SourceRefs = []DailyResourceRef{{
		OwnerRef: definition.OwnerRef, ResourceType: "config", Reference: "runtime.daily",
		DataScope: tool.DataScopePublic,
	}}
	observation.NativeRefs = dailyCountTestRefs(definition.OwnerRef, DailyMaxNativeRefs)
	observation.NativeRefCount = DailyValue[int]{State: DailyValueUnknown, SourceRef: "records.total", ReasonCode: "total_unknown"}
	observation.NativeRefsTruncated = true
	observation.Occurrences = dailyCountTestOccurrences(DailyMaxOccurrences)
	observation.OccurrenceCount = DailyValue[int]{State: DailyValueUnsupported, SourceRef: "runs.total", ReasonCode: "total_unsupported"}
	observation.OccurrencesTruncated = true
	observation.Issues = []DailyIssue{{SourceID: "daily.truncated", OwnerRef: definition.OwnerRef, Code: DailyIssueSourceReported}}
	source := &dailyTestSource{
		descriptor: DailySourceDescriptor{SourceID: "daily.truncated", Definitions: []DailyDefinitionDescriptor{definition}},
		snapshot:   DailySourceSnapshot{ObservedAt: now, Observations: []DailyObservation{observation}},
	}
	center, err := NewCenter(nil, []DailySource{source}, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewCenter() error = %v", err)
	}
	result, err := center.ListDaily(dailyTestAgentContext(t, "daily-truncated-counts", "mio", nil), DailyQuery{})
	if err != nil || len(result.Definitions) != 1 {
		t.Fatalf("ListDaily() = %#v, error = %v", result, err)
	}
	row := result.Definitions[0]
	if result.Availability != DailyAvailabilityPartial || !result.Partial || len(result.Issues) != 1 || result.Issues[0].Code != DailyIssueSourceReported || result.Issues[0].OwnerRef != definition.OwnerRef {
		t.Fatalf("owner-reported partial status = %#v, want scoped source issue", result)
	}
	if row.Configured.State != DailyConfiguredConfigured || row.Effective.State != DailyEffectiveEnabled || row.Observed.State != DailyObservedAvailable {
		t.Fatalf("known source axes were discarded: configured=%#v effective=%#v observed=%#v", row.Configured, row.Effective, row.Observed)
	}
	seenRefs := make(map[string]bool, len(row.SourceRefs))
	for _, ref := range row.SourceRefs {
		seenRefs[ref.Reference] = true
	}
	if len(seenRefs) != 3 || !seenRefs["config.routine.truncated"] || !seenRefs["settings.daily"] || !seenRefs["runtime.daily"] {
		t.Fatalf("known configuration references were lost: %#v", row.SourceRefs)
	}
	if len(row.NativeRefs) != DailyMaxNativeRefs || !row.NativeRefsTruncated || row.NativeRefCount.State != DailyValueUnknown || row.NativeRefCount.ReasonCode != "total_unknown" || row.NativeRefCount.Value != nil {
		t.Fatalf("unknown truncated native refs were changed: refs=%d truncated=%t count=%#v", len(row.NativeRefs), row.NativeRefsTruncated, row.NativeRefCount)
	}
	if len(row.Occurrences) != DailyMaxOccurrences || !row.OccurrencesTruncated || row.OccurrenceCount.State != DailyValueUnsupported || row.OccurrenceCount.ReasonCode != "total_unsupported" || row.OccurrenceCount.Value != nil {
		t.Fatalf("unsupported truncated occurrences were changed: rows=%d truncated=%t count=%#v", len(row.Occurrences), row.OccurrencesTruncated, row.OccurrenceCount)
	}
}

func TestDailyTimingProjectionKeepsAllSevenFieldStatesAndKnownEmptyArray(t *testing.T) {
	projection := dailyTestTiming()
	empty := []modulecore.TaskTimeWindow{}
	projection.AllowedWindows = DailyValue[[]modulecore.TaskTimeWindow]{
		State: DailyValueKnown, SourceRef: "heartbeat.windows", Value: &empty,
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		t.Fatalf("MarshalJSON() error = %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("UnmarshalJSON() error = %v", err)
	}
	wantFields := []string{"timezone", "scheduled_start_at", "not_before", "allowed_windows", "deadline_at", "recurrence", "estimated_duration_seconds"}
	for _, field := range wantFields {
		var value map[string]json.RawMessage
		if err := json.Unmarshal(decoded[field], &value); err != nil {
			t.Fatalf("field %q JSON = %s, error = %v", field, decoded[field], err)
		}
		if _, exists := value["state"]; !exists {
			t.Errorf("field %q has no state: %s", field, decoded[field])
		}
		if _, exists := value["source_ref"]; !exists {
			t.Errorf("field %q has no source_ref: %s", field, decoded[field])
		}
		if _, exists := value["value"]; !exists {
			t.Errorf("field %q has no explicit value/null: %s", field, decoded[field])
		}
	}
	var windows map[string]json.RawMessage
	if err := json.Unmarshal(decoded["allowed_windows"], &windows); err != nil {
		t.Fatal(err)
	}
	if got := string(windows["value"]); got != "[]" {
		t.Fatalf("known empty allowed_windows value = %s, want []", got)
	}
	if projection.NotBefore.State != DailyValueUnknown || projection.Recurrence.State != DailyValueUnsupported {
		t.Fatalf("partial field states were changed: %#v", projection)
	}
}

func TestDailyReadFiltersPrivateDefinitionsByTrustedScope(t *testing.T) {
	now := time.Date(2026, time.November, 17, 12, 0, 0, 0, time.UTC)
	definitions := []DailyDefinitionDescriptor{
		dailyDefinitionDescriptor("routine.public", tool.DataScopePublic, "", DailyAxis{State: DailyImplementationImplemented, SourceRef: "owner.public"}),
		dailyDefinitionDescriptor("routine.user-a", tool.DataScopeUser, "user-a", DailyAxis{State: DailyImplementationImplemented, SourceRef: "owner.mail"}),
		dailyDefinitionDescriptor("routine.user-b", tool.DataScopeUser, "user-b", DailyAxis{State: DailyImplementationImplemented, SourceRef: "owner.mail"}),
		dailyDefinitionDescriptor("routine.internal", tool.DataScopeInternal, "", DailyAxis{State: DailyImplementationImplemented, SourceRef: "owner.internal"}),
	}
	source := &dailyTestSource{descriptor: DailySourceDescriptor{SourceID: "daily.scope", Definitions: definitions}}
	for _, definition := range definitions {
		observation := dailyObservation(definition.DefinitionID,
			DailyAxis{State: DailyConfiguredConfigured, SourceRef: "config." + definition.DefinitionID},
			DailyAxis{State: DailyEffectiveEnabled, SourceRef: "runtime." + definition.DefinitionID},
			DailyAxis{State: DailyObservedAvailable, SourceRef: "history." + definition.DefinitionID})
		if definition.DefinitionID == "routine.public" {
			observation.SourceRefs = []DailyResourceRef{{OwnerRef: definition.OwnerRef, ResourceType: "mail_receipt", NativeID: "user-b-receipt", DataScope: tool.DataScopeUser, UserID: "user-b"}}
			observation.NativeRefs = []DailyResourceRef{{OwnerRef: definition.OwnerRef, ResourceType: "mail_receipt", NativeID: "user-b-native", DataScope: tool.DataScopeUser, UserID: "user-b"}}
			observation.NativeRefCount = DailyValue[int]{State: DailyValueKnown, SourceRef: "private.refs.count", Value: dailyPointer(1)}
			observation.LatestEvidence.State = DailyValueKnown
			lastSuccess := time.Date(2026, time.November, 16, 9, 0, 0, 0, time.UTC)
			observation.LatestEvidence.LastSuccessAt = DailyValue[time.Time]{State: DailyValueKnown, SourceRef: "private.history.last_success", Value: &lastSuccess}
			observation.LatestEvidence.Refs = []DailyResourceRef{{OwnerRef: definition.OwnerRef, ResourceType: "mail_receipt", NativeID: "user-b-history", DataScope: tool.DataScopeUser, UserID: "user-b"}}
			observation.Occurrences = []DailyOccurrence{{
				OccurrenceID: "user-b-occurrence", State: DailyOccurrenceSucceeded,
				NextRunAt:     DailyValue[time.Time]{State: DailyValueNone, SourceRef: "user-b-occurrence.next"},
				LastAttemptAt: DailyValue[time.Time]{State: DailyValueUnknown, SourceRef: "user-b-occurrence.last_attempt"},
				LastSuccessAt: DailyValue[time.Time]{State: DailyValueUnknown, SourceRef: "user-b-occurrence.last_success"},
				Result:        DailyValue[DailyOutcome]{State: DailyValueUnknown, SourceRef: "user-b-occurrence.result"},
				NativeRefs:    []DailyResourceRef{{OwnerRef: definition.OwnerRef, ResourceType: "run", NativeID: "user-b-run", DataScope: tool.DataScopeUser, UserID: "user-b"}},
			}}
			observation.OccurrenceCount = DailyValue[int]{State: DailyValueKnown, SourceRef: "private.occurrences.count", Value: dailyPointer(1)}
		}
		source.snapshot.Observations = append(source.snapshot.Observations, observation)
	}
	center, err := NewCenter(nil, []DailySource{source}, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewCenter() error = %v", err)
	}

	userCtx := dailyTestUserContext(t, "daily-private-a", "user-a")
	userResult, err := center.ListDaily(userCtx, DailyQuery{})
	if err != nil {
		t.Fatalf("ListDaily(user) error = %v", err)
	}
	if got, want := dailyDefinitionIDs(userResult.Definitions), []string{"routine.public", "routine.user-a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("user-visible definitions = %#v, want %#v", got, want)
	}
	if len(userResult.Definitions[0].SourceRefs) != 1 || userResult.Definitions[0].SourceRefs[0].DataScope != tool.DataScopePublic {
		t.Fatalf("foreign private source ref was included in visible public definition: %#v", userResult.Definitions[0].SourceRefs)
	}
	if len(userResult.Definitions[0].NativeRefs) != 0 || userResult.Definitions[0].NativeRefCount.State != DailyValueUnknown || userResult.Definitions[0].NativeRefCount.ReasonCode != "scope_filtered" {
		t.Fatalf("foreign private native ref/count leaked: refs=%#v count=%#v", userResult.Definitions[0].NativeRefs, userResult.Definitions[0].NativeRefCount)
	}
	if userResult.Definitions[0].LatestEvidence.State != DailyValueUnknown || userResult.Definitions[0].LatestEvidence.LastSuccessAt.State != DailyValueUnknown || len(userResult.Definitions[0].LatestEvidence.Refs) != 0 {
		t.Fatalf("foreign private history leaked: %#v", userResult.Definitions[0].LatestEvidence)
	}
	if len(userResult.Definitions[0].Occurrences) != 0 || userResult.Definitions[0].OccurrenceCount.State != DailyValueUnknown || userResult.Definitions[0].OccurrenceCount.ReasonCode != "scope_filtered" {
		t.Fatalf("foreign private occurrence/count leaked: rows=%#v count=%#v", userResult.Definitions[0].Occurrences, userResult.Definitions[0].OccurrenceCount)
	}

	publicAgentCtx := dailyTestAgentContext(t, "daily-public-only", "mio", nil)
	publicResult, err := center.ListDaily(publicAgentCtx, DailyQuery{})
	if err != nil {
		t.Fatalf("ListDaily(public agent) error = %v", err)
	}
	if got, want := dailyDefinitionIDs(publicResult.Definitions), []string{"routine.public"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("public-agent-visible definitions = %#v, want %#v", got, want)
	}
	if len(publicResult.Definitions[0].SourceRefs) != 1 || publicResult.Definitions[0].SourceRefs[0].DataScope != tool.DataScopePublic {
		t.Fatalf("private source ref leaked through public agent view: %#v", publicResult.Definitions[0].SourceRefs)
	}
	if len(publicResult.Definitions[0].NativeRefs) != 0 || publicResult.Definitions[0].NativeRefCount.ReasonCode != "scope_filtered" ||
		publicResult.Definitions[0].LatestEvidence.State != DailyValueUnknown || len(publicResult.Definitions[0].Occurrences) != 0 {
		t.Fatalf("foreign private observations leaked through public view: %#v", publicResult.Definitions[0])
	}
	if got := source.lastScope.AuthenticatedUserID; got != "" {
		t.Fatalf("public agent source scope AuthenticatedUserID = %q, want empty", got)
	}
	if strings.Contains(string(mustJSON(t, userResult)), "user-b") {
		t.Fatal("foreign user's private definition leaked through response")
	}
	var response map[string]any
	if err := json.Unmarshal(mustJSON(t, userResult), &response); err != nil {
		t.Fatal(err)
	}
	rows := response["definitions"].([]any)
	userRow := rows[1].(map[string]any)
	if _, exists := userRow["user_id"]; exists {
		t.Fatal("private owner UserID was serialized")
	}
}

func TestDailyReadKeepsDefinitionsWhenProvidersFailWithoutLeakingErrors(t *testing.T) {
	now := time.Date(2026, time.November, 17, 12, 0, 0, 0, time.UTC)
	failed := &dailyTestSource{
		descriptor: DailySourceDescriptor{SourceID: "daily.offline", Definitions: []DailyDefinitionDescriptor{
			dailyDefinitionDescriptor("routine.offline", tool.DataScopePublic, "", DailyAxis{State: DailyImplementationImplemented, SourceRef: "owner.offline"}),
		}},
		err: errors.New("raw provider error contains private mail subject and /Users/person/secret"),
	}
	healthy := &dailyTestSource{
		descriptor: DailySourceDescriptor{SourceID: "daily.healthy", Definitions: []DailyDefinitionDescriptor{
			dailyDefinitionDescriptor("routine.healthy", tool.DataScopePublic, "", DailyAxis{State: DailyImplementationImplemented, SourceRef: "owner.healthy"}),
		}},
		snapshot: DailySourceSnapshot{ObservedAt: now, Observations: []DailyObservation{
			dailyObservation("routine.healthy", DailyAxis{State: DailyConfiguredConfigured, SourceRef: "healthy.config"}, DailyAxis{State: DailyEffectiveEnabled, SourceRef: "healthy.runtime"}, DailyAxis{State: DailyObservedAvailable, SourceRef: "healthy.history"}),
		}},
	}
	center, err := NewCenter(nil, []DailySource{failed, healthy}, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewCenter() error = %v", err)
	}
	result, err := center.ListDaily(dailyTestAgentContext(t, "daily-provider-error", "mio", nil), DailyQuery{})
	if err != nil {
		t.Fatalf("ListDaily() error = %v", err)
	}
	if result.Availability != DailyAvailabilityPartial || !result.Partial || len(result.Definitions) != 2 {
		t.Fatalf("partial provider result = %#v", result)
	}
	var offline DailyDefinition
	for _, definition := range result.Definitions {
		if definition.DefinitionID == "routine.offline" {
			offline = definition
		}
	}
	if offline.DefinitionID == "" || offline.Observed.State != DailyObservedUnavailable || offline.Timing.Timezone.State != DailyValueUnknown || offline.Timing.Timezone.ReasonCode != "source_unavailable" {
		t.Fatalf("offline definition did not remain unknown/unavailable: %#v", offline)
	}
	encoded := string(mustJSON(t, result))
	if strings.Contains(encoded, "raw provider error") || strings.Contains(encoded, "private mail subject") || strings.Contains(encoded, "/Users/person/secret") {
		t.Fatalf("provider error leaked into response: %s", encoded)
	}
	if len(result.Issues) == 0 || result.Issues[0].Code != "source_unavailable" {
		t.Fatalf("source failure lacks safe issue code: %#v", result.Issues)
	}
	if failed.calls != 1 || healthy.calls != 1 {
		t.Fatalf("source calls = failed:%d healthy:%d, want one each", failed.calls, healthy.calls)
	}
}

func TestDailyReadDoesNotExposeIssueOwnerOutsideCurrentScope(t *testing.T) {
	public := dailyDefinitionDescriptor("routine.public", tool.DataScopePublic, "", DailyAxis{State: DailyImplementationImplemented, SourceRef: "owner.public"})
	private := dailyDefinitionDescriptor("routine.user-b", tool.DataScopeUser, "user-b", DailyAxis{State: DailyImplementationImplemented, SourceRef: "owner.private"})
	publicObservation := dailyObservation("routine.public", DailyAxis{State: DailyConfiguredConfigured, SourceRef: "public.config"}, DailyAxis{State: DailyEffectiveEnabled, SourceRef: "public.runtime"}, DailyAxis{State: DailyObservedAvailable, SourceRef: "public.history"})
	source := &dailyTestSource{
		descriptor: DailySourceDescriptor{SourceID: "daily.issue-scope", Definitions: []DailyDefinitionDescriptor{public, private}},
		snapshot: DailySourceSnapshot{
			Observations: []DailyObservation{publicObservation},
			Issues:       []DailyIssue{{OwnerRef: private.OwnerRef, Code: "private_owner_failure"}},
		},
	}
	center, err := NewCenter(nil, []DailySource{source}, time.Now)
	if err != nil {
		t.Fatalf("NewCenter() error = %v", err)
	}
	result, err := center.ListDaily(dailyTestAgentContext(t, "daily-private-issue", "mio", nil), DailyQuery{})
	if err != nil {
		t.Fatalf("ListDaily() error = %v", err)
	}
	encoded := string(mustJSON(t, result))
	if strings.Contains(encoded, "user-b") || strings.Contains(encoded, private.OwnerRef) || strings.Contains(encoded, "private_owner_failure") {
		t.Fatalf("out-of-scope source issue leaked: %s", encoded)
	}
	if result.Availability != DailyAvailabilityUnavailable || len(result.Issues) != 1 || result.Issues[0].Code != DailyIssueInvalidProjection {
		t.Fatalf("invalid scoped issue was not rejected safely: %#v", result)
	}
}

func TestDailyReadAllProvidersUnavailableIsNotEmptySuccess(t *testing.T) {
	source := &dailyTestSource{
		descriptor: DailySourceDescriptor{SourceID: "daily.offline", Definitions: []DailyDefinitionDescriptor{
			dailyDefinitionDescriptor("routine.offline", tool.DataScopePublic, "", DailyAxis{State: DailyImplementationImplemented, SourceRef: "owner.offline"}),
		}},
		err: errors.New("private provider message"),
	}
	center, err := NewCenter(nil, []DailySource{source}, time.Now)
	if err != nil {
		t.Fatalf("NewCenter() error = %v", err)
	}
	result, err := center.ListDaily(dailyTestAgentContext(t, "daily-all-offline", "mio", nil), DailyQuery{})
	if err != nil {
		t.Fatalf("ListDaily() error = %v", err)
	}
	if result.Availability != DailyAvailabilityUnavailable || !result.Partial || len(result.Definitions) != 1 {
		t.Fatalf("all-offline result = %#v, want explicit unavailable with retained definition", result)
	}
	filtered, err := center.ListDaily(dailyTestAgentContext(t, "daily-unknown-kind", "mio", nil), DailyQuery{KindID: "research"})
	if err != nil || filtered.Availability != DailyAvailabilityAvailable || len(filtered.Definitions) != 0 || source.calls != 1 {
		t.Fatalf("unknown kind filter = %#v, error = %v, want scoped empty page without reading unrelated source", filtered, err)
	}
	noSources, err := NewCenter(nil, nil, time.Now)
	if err != nil {
		t.Fatalf("NewCenter(no sources) error = %v", err)
	}
	if _, err := noSources.ListDaily(dailyTestAgentContext(t, "daily-unconfigured", "mio", nil), DailyQuery{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ListDaily(no source) error = %v, want ErrUnavailable", err)
	}
}

func TestDailyReadRejectsMalformedSourceProjectionWithoutCoercion(t *testing.T) {
	cases := []struct {
		name      string
		fieldName string
		mutate    func(*DailyObservation)
	}{
		{
			name:      "known zero timestamp",
			fieldName: "scheduled_start_at",
			mutate: func(observation *DailyObservation) {
				zeroTime := time.Time{}
				observation.Timing.ScheduledStartAt = DailyValue[time.Time]{State: DailyValueKnown, SourceRef: "schedule", Value: &zeroTime}
			},
		},
		{
			name:      "missing source reference",
			fieldName: "timezone",
			mutate: func(observation *DailyObservation) {
				observation.Timing.Timezone.SourceRef = ""
			},
		},
		{
			name:      "known nil array",
			fieldName: "allowed_windows",
			mutate: func(observation *DailyObservation) {
				observation.Timing.AllowedWindows = DailyValue[[]modulecore.TaskTimeWindow]{State: DailyValueKnown, SourceRef: "windows", Value: new([]modulecore.TaskTimeWindow)}
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			base := dailyObservation("routine.bad", DailyAxis{State: DailyConfiguredConfigured, SourceRef: "config"}, DailyAxis{State: DailyEffectiveEnabled, SourceRef: "runtime"}, DailyAxis{State: DailyObservedAvailable, SourceRef: "history"})
			test.mutate(&base)
			source := &dailyTestSource{
				descriptor: DailySourceDescriptor{SourceID: "daily.malformed", Definitions: []DailyDefinitionDescriptor{
					dailyDefinitionDescriptor("routine.bad", tool.DataScopePublic, "", DailyAxis{State: DailyImplementationImplemented, SourceRef: "owner.bad"}),
				}},
				snapshot: DailySourceSnapshot{Observations: []DailyObservation{base}},
			}
			center, err := NewCenter(nil, []DailySource{source}, time.Now)
			if err != nil {
				t.Fatalf("NewCenter() error = %v", err)
			}
			result, err := center.ListDaily(dailyTestAgentContext(t, "daily-malformed", "mio", nil), DailyQuery{})
			if err != nil {
				t.Fatalf("ListDaily() error = %v", err)
			}
			if result.Availability != DailyAvailabilityUnavailable || len(result.Definitions) != 1 || len(result.Issues) == 0 || result.Issues[0].Code != DailyIssueInvalidProjection {
				t.Fatalf("invalid projection result = %#v", result)
			}
			fieldState, fieldReason := dailyTimingFieldState(result.Definitions[0].Timing, test.fieldName)
			if fieldState != DailyValueUnknown || fieldState == DailyValueNone || fieldReason != string(DailyIssueInvalidProjection) {
				t.Fatalf("invalid known field was silently preserved/coerced: state=%q reason=%q", fieldState, fieldReason)
			}
		})
	}
}

func TestDailyReadRejectsAbsoluteAndFileReferencesButKeepsSymbolicReferences(t *testing.T) {
	unsafeReferences := []struct {
		name  string
		value string
		field string
	}{
		{name: "unix absolute path", value: "/Users/person/private.db", field: "reference"},
		{name: "windows drive path", value: "C:/Users/person/private.db", field: "reference"},
		{name: "file URI path", value: "file:/Users/person/private.db", field: "reference"},
		{name: "file drive URI", value: "file:C:/Users/person/private.db", field: "reference"},
		{name: "windows link path", value: "C:/Users/person/private.db", field: "link_ref"},
		{name: "file link URI", value: "file:/Users/person/private.db", field: "link_ref"},
		{name: "file display URI", value: "file:/Users/person/private.db", field: "display_name"},
		{name: "absolute URL", value: "https://example.test/private", field: "reference"},
	}
	for _, test := range unsafeReferences {
		t.Run(test.name, func(t *testing.T) {
			definition := dailyDefinitionDescriptor("routine.reference", tool.DataScopePublic, "", DailyAxis{State: DailyImplementationImplemented, SourceRef: "owner.reference"})
			observation := dailyObservation(definition.DefinitionID,
				DailyAxis{State: DailyConfiguredConfigured, SourceRef: "config.reference"},
				DailyAxis{State: DailyEffectiveEnabled, SourceRef: "runtime.reference"},
				DailyAxis{State: DailyObservedAvailable, SourceRef: "history.reference"})
			ref := DailyResourceRef{OwnerRef: definition.OwnerRef, ResourceType: "config"}
			switch test.field {
			case "reference":
				ref.Reference = test.value
			case "link_ref":
				ref.LinkRef = test.value
			case "display_name":
				ref.DisplayName = test.value
			default:
				t.Fatalf("unsupported test field %q", test.field)
			}
			observation.SourceRefs = []DailyResourceRef{ref}
			source := &dailyTestSource{
				descriptor: DailySourceDescriptor{SourceID: "daily.reference", Definitions: []DailyDefinitionDescriptor{definition}},
				snapshot:   DailySourceSnapshot{Observations: []DailyObservation{observation}},
			}
			center, err := NewCenter(nil, []DailySource{source}, time.Now)
			if err != nil {
				t.Fatalf("NewCenter() error = %v", err)
			}
			result, err := center.ListDaily(dailyTestAgentContext(t, "daily-reference", "mio", nil), DailyQuery{})
			if err != nil {
				t.Fatalf("ListDaily() error = %v", err)
			}
			if result.Availability != DailyAvailabilityUnavailable || len(result.Definitions) != 1 || result.Definitions[0].Observed.State != DailyObservedUnavailable {
				t.Fatalf("unsafe reference result = %#v, want unavailable metadata row", result)
			}
			if strings.Contains(string(mustJSON(t, result)), test.value) {
				t.Fatalf("unsafe reference %q leaked into response", test.value)
			}
		})
	}

	if !dailySafeReference("owner:task-17") {
		t.Fatal("symbolic owner reference with a colon was rejected")
	}
	if !dailySafeLabel("owner:task-17") {
		t.Fatal("symbolic label with a colon was rejected")
	}
	definition := dailyDefinitionDescriptor("routine.symbolic", tool.DataScopePublic, "", DailyAxis{State: DailyImplementationImplemented, SourceRef: "owner.symbolic"})
	definition.OwnerRef = "owner:task-17"
	definition.SourceRefs[0].OwnerRef = definition.OwnerRef
	observation := dailyObservation(definition.DefinitionID,
		DailyAxis{State: DailyConfiguredConfigured, SourceRef: "config.symbolic"},
		DailyAxis{State: DailyEffectiveEnabled, SourceRef: "runtime.symbolic"},
		DailyAxis{State: DailyObservedAvailable, SourceRef: "history.symbolic"})
	observation.SourceRefs = []DailyResourceRef{{OwnerRef: definition.OwnerRef, ResourceType: "config", Reference: "source:task-17"}}
	source := &dailyTestSource{
		descriptor: DailySourceDescriptor{SourceID: "daily.symbolic", Definitions: []DailyDefinitionDescriptor{definition}},
		snapshot:   DailySourceSnapshot{Observations: []DailyObservation{observation}},
	}
	center, err := NewCenter(nil, []DailySource{source}, time.Now)
	if err != nil {
		t.Fatalf("NewCenter(symbolic) error = %v", err)
	}
	result, err := center.ListDaily(dailyTestAgentContext(t, "daily-symbolic", "mio", nil), DailyQuery{})
	if err != nil || result.Availability != DailyAvailabilityAvailable || len(result.Definitions) != 1 {
		t.Fatalf("symbolic reference result = %#v, error = %v", result, err)
	}
	if got := result.Definitions[0].OwnerRef; got != "owner:task-17" {
		t.Fatalf("symbolic owner reference = %q, want owner:task-17", got)
	}
}

func TestDailyReadBoundsOccurrenceReferencesAndPreservesOtherRows(t *testing.T) {
	badDefinition := dailyDefinitionDescriptor("routine.bad", tool.DataScopePublic, "", DailyAxis{State: DailyImplementationImplemented, SourceRef: "owner.bad"})
	goodDefinition := dailyDefinitionDescriptor("routine.good", tool.DataScopePublic, "", DailyAxis{State: DailyImplementationImplemented, SourceRef: "owner.good"})
	badObservation := dailyObservation(badDefinition.DefinitionID,
		DailyAxis{State: DailyConfiguredConfigured, SourceRef: "config.bad"},
		DailyAxis{State: DailyEffectiveEnabled, SourceRef: "runtime.bad"},
		DailyAxis{State: DailyObservedAvailable, SourceRef: "history.bad"})
	refs := make([]DailyResourceRef, DailyMaxNativeRefs+1)
	for index := range refs {
		refs[index] = DailyResourceRef{OwnerRef: badDefinition.OwnerRef, ResourceType: "run", NativeID: "oversized-private-ref", DataScope: tool.DataScopePublic}
	}
	badObservation.Occurrences = []DailyOccurrence{{
		OccurrenceID: "run.bad", State: DailyOccurrenceSucceeded,
		NextRunAt:     DailyValue[time.Time]{State: DailyValueUnknown, SourceRef: "runs.next"},
		LastAttemptAt: DailyValue[time.Time]{State: DailyValueUnknown, SourceRef: "runs.last_attempt"},
		LastSuccessAt: DailyValue[time.Time]{State: DailyValueUnknown, SourceRef: "runs.last_success"},
		Result:        DailyValue[DailyOutcome]{State: DailyValueKnown, SourceRef: "runs.result", Value: dailyPointer(DailyOutcomeSuccess)},
		NativeRefs:    refs,
	}}
	badObservation.OccurrenceCount = DailyValue[int]{State: DailyValueKnown, SourceRef: "runs.count", Value: dailyPointer(1)}
	goodObservation := dailyObservation(goodDefinition.DefinitionID,
		DailyAxis{State: DailyConfiguredConfigured, SourceRef: "config.good"},
		DailyAxis{State: DailyEffectiveEnabled, SourceRef: "runtime.good"},
		DailyAxis{State: DailyObservedAvailable, SourceRef: "history.good"})
	source := &dailyTestSource{
		descriptor: DailySourceDescriptor{SourceID: "daily.bounded", Definitions: []DailyDefinitionDescriptor{badDefinition, goodDefinition}},
		snapshot:   DailySourceSnapshot{Observations: []DailyObservation{badObservation, goodObservation}},
	}
	center, err := NewCenter(nil, []DailySource{source}, time.Now)
	if err != nil {
		t.Fatalf("NewCenter() error = %v", err)
	}
	result, err := center.ListDaily(dailyTestAgentContext(t, "daily-bounded-occurrence", "mio", nil), DailyQuery{})
	if err != nil {
		t.Fatalf("ListDaily() error = %v", err)
	}
	if result.Availability != DailyAvailabilityPartial || len(result.Definitions) != 2 {
		t.Fatalf("bounded occurrence result = %#v, want partial with both metadata rows", result)
	}
	bad, good := result.Definitions[0], result.Definitions[1]
	if bad.DefinitionID != badDefinition.DefinitionID || bad.Observed.State != DailyObservedUnavailable || len(bad.Occurrences) != 0 || len(bad.Issues) == 0 || bad.Issues[0].Code != DailyIssueInvalidProjection {
		t.Fatalf("oversized occurrence row = %#v, want safe invalid projection", bad)
	}
	if good.DefinitionID != goodDefinition.DefinitionID || good.Observed.State != DailyObservedAvailable {
		t.Fatalf("valid sibling row was lost or degraded: %#v", good)
	}
	if strings.Contains(string(mustJSON(t, result)), "oversized-private-ref") {
		t.Fatal("oversized occurrence reference leaked into response")
	}
}

func TestCenterCapabilitiesDailyAvailabilityFollowsVisibleDefinitions(t *testing.T) {
	now := time.Date(2026, time.November, 17, 12, 0, 0, 0, time.UTC)
	private := dailyDefinitionDescriptor("routine.user-b.private", tool.DataScopeUser, "user-b", DailyAxis{State: DailyImplementationImplemented, SourceRef: "owner.private"})
	source := &dailyTestSource{descriptor: DailySourceDescriptor{SourceID: "daily.private", Definitions: []DailyDefinitionDescriptor{private}}}
	for _, withManaged := range []bool{false, true} {
		t.Run(map[bool]string{false: "without_managed_service", true: "with_managed_service"}[withManaged], func(t *testing.T) {
			var managed *Service
			if withManaged {
				policy := taskCenterTestPolicy("mio")
				var err error
				managed, err = NewService(newStateContractStore(), policy, []string{"mio"}, []Kind{{ID: "research", Label: "Research", ExecutorAvailable: true}}, func() time.Time { return now })
				if err != nil {
					t.Fatalf("NewService() error = %v", err)
				}
			}
			center, err := NewCenter(managed, []DailySource{source}, func() time.Time { return now })
			if err != nil {
				t.Fatalf("NewCenter() error = %v", err)
			}

			userACaps, err := center.Capabilities(dailyTestUserContext(t, "daily-capabilities-a", "user-a"))
			if err != nil {
				t.Fatalf("Capabilities(user-a) error = %v", err)
			}
			if userACaps.DailyAvailable || userACaps.DailyReasonCode != "daily_sources_unavailable" {
				t.Fatalf("user-a capabilities expose foreign private definition: %#v", userACaps)
			}
			encoded := string(mustJSON(t, userACaps))
			if strings.Contains(encoded, "user-b") || strings.Contains(encoded, "routine.user-b.private") || strings.Contains(encoded, "definition_count") {
				t.Fatalf("private definition identity/count leaked through capabilities: %s", encoded)
			}

			userBCaps, err := center.Capabilities(dailyTestUserContext(t, "daily-capabilities-b", "user-b"))
			if err != nil {
				t.Fatalf("Capabilities(user-b) error = %v", err)
			}
			if !userBCaps.DailyAvailable || userBCaps.DailyReasonCode != "" {
				t.Fatalf("owner-visible private definition not reflected in capabilities: %#v", userBCaps)
			}
		})
	}
}

func TestDailyReadValidatesScopePaginationAndSnapshotFreshness(t *testing.T) {
	now := time.Date(2026, time.November, 17, 12, 0, 0, 0, time.UTC)
	descriptor := DailySourceDescriptor{SourceID: "daily.pages", Definitions: []DailyDefinitionDescriptor{
		dailyDefinitionDescriptor("routine.a", tool.DataScopePublic, "", DailyAxis{State: DailyImplementationImplemented, SourceRef: "owner.a"}),
		dailyDefinitionDescriptor("routine.b", tool.DataScopePublic, "", DailyAxis{State: DailyImplementationImplemented, SourceRef: "owner.b"}),
		dailyDefinitionDescriptor("routine.c", tool.DataScopePublic, "", DailyAxis{State: DailyImplementationImplemented, SourceRef: "owner.c"}),
	}}
	source := &dailyTestSource{descriptor: descriptor}
	for _, definition := range descriptor.Definitions {
		source.snapshot.Observations = append(source.snapshot.Observations, dailyObservation(definition.DefinitionID,
			DailyAxis{State: DailyConfiguredConfigured, SourceRef: "config." + definition.DefinitionID},
			DailyAxis{State: DailyEffectiveEnabled, SourceRef: "runtime." + definition.DefinitionID},
			DailyAxis{State: DailyObservedAvailable, SourceRef: "history." + definition.DefinitionID}))
	}
	source.snapshot.ObservedAt = now
	center, err := NewCenter(nil, []DailySource{source}, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewCenter() error = %v", err)
	}
	ctx := dailyTestAgentContext(t, "daily-page", "mio", nil)
	first, err := center.ListDaily(ctx, DailyQuery{Limit: 1})
	if err != nil || len(first.Definitions) != 1 || first.NextCursor == "" {
		t.Fatalf("ListDaily(first page) = %#v, error = %v", first, err)
	}
	second, err := center.ListDaily(ctx, DailyQuery{Limit: 1, Cursor: first.NextCursor})
	if err != nil || len(second.Definitions) != 1 || second.Definitions[0].DefinitionID == first.Definitions[0].DefinitionID {
		t.Fatalf("ListDaily(second page) = %#v, error = %v", second, err)
	}
	if _, err := center.ListDaily(ctx, DailyQuery{KindID: "different", Limit: 1, Cursor: first.NextCursor}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cursor reused with another kind filter error = %v, want invalid", err)
	}
	if _, err := center.ListDaily(context.Background(), DailyQuery{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ListDaily(missing scope) error = %v, want forbidden", err)
	}
	if _, err := center.ListDaily(ctx, DailyQuery{Limit: 101}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ListDaily(oversized) error = %v, want invalid", err)
	}
	if got := source.calls; got != 2 {
		t.Fatalf("source snapshot calls = %d, want two fresh reads for two pages", got)
	}
	if err := source.lastScope.Validate(); err != nil {
		t.Fatalf("source received invalid trusted scope: %v", err)
	}
}

func TestDailyReadMetadataIsFrozenAndEachRequestUsesFreshOwnerSnapshot(t *testing.T) {
	now := time.Date(2026, time.November, 17, 12, 0, 0, 0, time.UTC)
	descriptor := DailySourceDescriptor{SourceID: "daily.fresh", Definitions: []DailyDefinitionDescriptor{
		dailyDefinitionDescriptor("routine.fresh", tool.DataScopePublic, "", DailyAxis{State: DailyImplementationImplemented, SourceRef: "owner.fresh"}),
	}}
	source := &dailyTestSource{descriptor: descriptor}
	source.snapshotFn = func(_ context.Context, _ tool.ToolExecutionScope, call int) (DailySourceSnapshot, error) {
		observation := dailyObservation("routine.fresh", DailyAxis{State: DailyConfiguredConfigured, SourceRef: "config.fresh"}, DailyAxis{State: DailyEffectiveEnabled, SourceRef: "runtime.fresh"}, DailyAxis{State: DailyObservedAvailable, SourceRef: "history.fresh"})
		observation.TargetCount = DailyValue[int]{State: DailyValueKnown, SourceRef: "target.count", Value: dailyPointer(call)}
		return DailySourceSnapshot{ObservedAt: now.Add(time.Duration(call) * time.Minute), Observations: []DailyObservation{observation}}, nil
	}
	center, err := NewCenter(nil, []DailySource{source}, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewCenter() error = %v", err)
	}
	source.descriptor.Definitions[0].Label = "mutated after registration"
	ctx := dailyTestAgentContext(t, "daily-fresh", "mio", nil)
	first, err := center.ListDaily(ctx, DailyQuery{})
	if err != nil {
		t.Fatalf("ListDaily(first) error = %v", err)
	}
	second, err := center.ListDaily(ctx, DailyQuery{})
	if err != nil {
		t.Fatalf("ListDaily(second) error = %v", err)
	}
	if first.Definitions[0].Label != "routine.fresh" || second.Definitions[0].Label != "routine.fresh" {
		t.Fatalf("registered definition metadata changed after construction: first=%q second=%q", first.Definitions[0].Label, second.Definitions[0].Label)
	}
	if *first.Definitions[0].TargetCount.Value != 1 || *second.Definitions[0].TargetCount.Value != 2 || source.calls != 2 {
		t.Fatalf("owner snapshots were cached: first=%#v second=%#v calls=%d", first.Definitions[0].TargetCount, second.Definitions[0].TargetCount, source.calls)
	}
	if !first.Definitions[0].SourceObservedAt.Known() || !second.Definitions[0].SourceObservedAt.Known() ||
		!second.Definitions[0].SourceObservedAt.Value.After(*first.Definitions[0].SourceObservedAt.Value) {
		t.Fatalf("source observation times were not preserved: first=%#v second=%#v", first.Definitions[0].SourceObservedAt, second.Definitions[0].SourceObservedAt)
	}
}

func TestDailyReadFailsClosedForDuplicateMetadataAndObservationIDs(t *testing.T) {
	duplicate := dailyDefinitionDescriptor("routine.same", tool.DataScopePublic, "", DailyAxis{State: DailyImplementationImplemented, SourceRef: "owner.same"})
	if _, err := NewCenter(nil, []DailySource{&dailyTestSource{descriptor: DailySourceDescriptor{SourceID: "daily.dup", Definitions: []DailyDefinitionDescriptor{duplicate, duplicate}}}}, time.Now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("NewCenter(duplicate metadata) error = %v, want invalid", err)
	}
	definition := dailyDefinitionDescriptor("routine.one", tool.DataScopePublic, "", DailyAxis{State: DailyImplementationImplemented, SourceRef: "owner.one"})
	source := &dailyTestSource{
		descriptor: DailySourceDescriptor{SourceID: "daily.duplicate-observation", Definitions: []DailyDefinitionDescriptor{definition}},
		snapshot: DailySourceSnapshot{Observations: []DailyObservation{
			dailyObservation("routine.one", DailyAxis{State: DailyConfiguredConfigured, SourceRef: "config"}, DailyAxis{State: DailyEffectiveEnabled, SourceRef: "runtime"}, DailyAxis{State: DailyObservedAvailable, SourceRef: "history"}),
			dailyObservation("routine.one", DailyAxis{State: DailyConfiguredConfigured, SourceRef: "config2"}, DailyAxis{State: DailyEffectiveEnabled, SourceRef: "runtime2"}, DailyAxis{State: DailyObservedAvailable, SourceRef: "history2"}),
		}},
	}
	center, err := NewCenter(nil, []DailySource{source}, time.Now)
	if err != nil {
		t.Fatalf("NewCenter() error = %v", err)
	}
	result, err := center.ListDaily(dailyTestAgentContext(t, "daily-duplicate-observation", "mio", nil), DailyQuery{})
	if err != nil || result.Availability != DailyAvailabilityUnavailable || len(result.Definitions) != 1 || len(result.Issues) == 0 || result.Issues[0].Code != "invalid_projection" {
		t.Fatalf("duplicate observation result = %#v, error = %v", result, err)
	}
}

func TestDailyCenterReadDoesNotTouchManagedStoreWhenManagedIsNil(t *testing.T) {
	source := &dailyTestSource{
		descriptor: DailySourceDescriptor{SourceID: "daily.readonly", Definitions: []DailyDefinitionDescriptor{
			dailyDefinitionDescriptor("routine.readonly", tool.DataScopePublic, "", DailyAxis{State: DailyImplementationImplemented, SourceRef: "owner.readonly"}),
		}},
		snapshot: DailySourceSnapshot{Observations: []DailyObservation{
			dailyObservation("routine.readonly", DailyAxis{State: DailyConfiguredConfigured, SourceRef: "config"}, DailyAxis{State: DailyEffectiveEnabled, SourceRef: "runtime"}, DailyAxis{State: DailyObservedAvailable, SourceRef: "history"}),
		}},
	}
	center, err := NewCenter(nil, []DailySource{source}, time.Now)
	if err != nil {
		t.Fatalf("NewCenter() error = %v", err)
	}
	ctx := dailyTestAgentContext(t, "daily-readonly", "mio", nil)
	if _, err := center.ListDaily(ctx, DailyQuery{}); err != nil {
		t.Fatalf("ListDaily() error = %v", err)
	}
	if _, err := center.Submit(ctx, SubmitInput{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Submit(managed nil) error = %v, want unavailable", err)
	}
	if _, err := center.Submit(context.Background(), SubmitInput{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Submit(managed nil, missing scope) error = %v, want forbidden", err)
	}
	if _, err := center.Claim(ctx, ClaimInput{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Claim(managed nil) error = %v, want unavailable", err)
	}
	if _, err := center.List(ctx, ListQuery{View: ViewRemaining}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("List(managed nil) error = %v, want unavailable", err)
	}
	if got := source.calls; got != 1 {
		t.Fatalf("DailySource Snapshot calls = %d, want one; mutation/query path must not probe or retry", got)
	}
}

func TestCenterDelegatesManagedClaimListGetReceiptAndCapabilities(t *testing.T) {
	now := time.Date(2026, time.November, 17, 12, 0, 0, 0, time.UTC)
	policy := taskCenterTestPolicy("mio")
	policyRevision, err := policy.CanonicalHash()
	if err != nil {
		t.Fatalf("CanonicalHash() error = %v", err)
	}
	goal := taskCenterStateTestGoal("goal-center-delegate", "waiting", "unassigned", "user-a", now, policyRevision)
	store := newStateContractStore(goal)
	managed, err := NewService(store, policy, []string{"mio"}, []Kind{{ID: "research", Label: "Research", ExecutorAvailable: true}}, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	center, err := NewCenter(managed, nil, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewCenter() error = %v", err)
	}
	ctx := dailyTestAgentContext(t, "center-delegate", "mio", nil)
	list, err := center.List(ctx, ListQuery{View: ViewClaimable})
	if err != nil || len(list.Items) != 1 || list.Items[0].GoalID != goal.GoalID {
		t.Fatalf("Center.List() = %#v, error = %v", list, err)
	}
	claimed, err := center.Claim(ctx, ClaimInput{GoalID: goal.GoalID, ExpectedRevision: 1, ExpectedPolicyRevision: policyRevision, IdempotencyKey: "center-claim"})
	if err != nil || claimed.Item.Assignee != "mio" {
		t.Fatalf("Center.Claim() = %#v, error = %v", claimed, err)
	}
	item, err := center.Get(ctx, goal.GoalID)
	if err != nil || item.Assignee != "mio" {
		t.Fatalf("Center.Get() = %#v, error = %v", item, err)
	}
	receipt, found, err := center.Receipt(ctx, "claim", "center-claim")
	if err != nil || !found || !receipt.Replayed || receipt.Item.GoalID != goal.GoalID {
		t.Fatalf("Center.Receipt() = %#v, found=%v, error=%v", receipt, found, err)
	}
	capabilities, err := center.Capabilities(ctx)
	if err != nil || !capabilities.ManagedAvailable || capabilities.DailyAvailable || !capabilities.CanSubmit || !capabilities.CanClaim {
		t.Fatalf("Center.Capabilities() = %#v, error = %v", capabilities, err)
	}
	if _, err := center.List(ctx, ListQuery{View: ViewDaily}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Center.List(daily) error = %v, want separate DailyRead contract", err)
	}
}

func dailyDefinitionDescriptor(id, dataScope, userID string, implementation DailyAxis) DailyDefinitionDescriptor {
	return DailyDefinitionDescriptor{
		DefinitionID: id, KindID: "daily.routine", Label: id, OwnerRef: "owner." + id,
		ResourceType: "schedule", DataScope: dataScope, UserID: userID, Implementation: implementation,
		SourceRefs: []DailyResourceRef{{OwnerRef: "owner." + id, ResourceType: "config", Reference: "config." + id}},
	}
}

func dailyObservation(id string, configured, effective, observed DailyAxis) DailyObservation {
	return DailyObservation{
		DefinitionID: id, Configured: configured, Effective: effective, Observed: observed,
		Timing:         dailyTestTiming(),
		TargetCount:    DailyValue[int]{State: DailyValueKnown, SourceRef: "targets." + id, Value: dailyPointer(0)},
		NextRunAt:      DailyValue[time.Time]{State: DailyValueUnknown, SourceRef: "schedule." + id, ReasonCode: "not_scheduled"},
		NativeRefCount: DailyValue[int]{State: DailyValueKnown, SourceRef: "records." + id, Value: dailyPointer(0)},
		LatestEvidence: DailyEvidence{
			State:         DailyValueUnknown,
			LastAttemptAt: DailyValue[time.Time]{State: DailyValueUnknown, SourceRef: "history." + id, ReasonCode: "empty_or_not_retained"},
			LastSuccessAt: DailyValue[time.Time]{State: DailyValueUnknown, SourceRef: "history." + id, ReasonCode: "empty_or_not_retained"},
			LastResult:    DailyValue[DailyOutcome]{State: DailyValueUnknown, SourceRef: "history." + id, ReasonCode: "empty_or_not_retained"},
			Refs:          []DailyResourceRef{},
		},
		SourceRefs: []DailyResourceRef{}, NativeRefs: []DailyResourceRef{}, Occurrences: []DailyOccurrence{},
		OccurrenceCount: DailyValue[int]{State: DailyValueUnknown, SourceRef: "occurrences." + id, ReasonCode: "no_occurrence_reader"},
		Issues:          []DailyIssue{},
	}
}

func dailyCountTestRefs(ownerRef string, count int) []DailyResourceRef {
	refs := make([]DailyResourceRef, count)
	for index := range refs {
		resourceType := "schedule"
		if index%2 == 1 {
			resourceType = "run"
		}
		refs[index] = DailyResourceRef{
			OwnerRef: ownerRef, ResourceType: resourceType, NativeID: "native-" + strconv.Itoa(index),
			DataScope: tool.DataScopePublic,
		}
	}
	return refs
}

func dailyCountTestOccurrences(count int) []DailyOccurrence {
	occurrences := make([]DailyOccurrence, count)
	for index := range occurrences {
		id := "run-" + strconv.Itoa(index)
		occurrences[index] = DailyOccurrence{
			OccurrenceID: id, State: DailyOccurrenceUnknown,
			NextRunAt:     DailyValue[time.Time]{State: DailyValueUnknown, SourceRef: "runs.next", ReasonCode: "not_retained"},
			LastAttemptAt: DailyValue[time.Time]{State: DailyValueUnknown, SourceRef: "runs.last_attempt", ReasonCode: "not_retained"},
			LastSuccessAt: DailyValue[time.Time]{State: DailyValueUnknown, SourceRef: "runs.last_success", ReasonCode: "not_retained"},
			Result:        DailyValue[DailyOutcome]{State: DailyValueUnknown, SourceRef: "runs.result", ReasonCode: "not_retained"},
			NativeRefs:    []DailyResourceRef{},
		}
	}
	return occurrences
}

func dailyTestTiming() DailyTimingProjection {
	now := time.Date(2026, time.November, 17, 12, 0, 0, 0, time.UTC)
	emptyWindows := []modulecore.TaskTimeWindow{}
	return DailyTimingProjection{
		Timezone:                 DailyValue[string]{State: DailyValueKnown, SourceRef: "config.timezone", Value: dailyPointer("Asia/Tokyo")},
		ScheduledStartAt:         DailyValue[time.Time]{State: DailyValueKnown, SourceRef: "schedule.start", Value: &now},
		NotBefore:                DailyValue[time.Time]{State: DailyValueUnknown, SourceRef: "schedule.not_before", ReasonCode: "not_exposed"},
		AllowedWindows:           DailyValue[[]modulecore.TaskTimeWindow]{State: DailyValueKnown, SourceRef: "schedule.windows", Value: &emptyWindows},
		DeadlineAt:               DailyValue[time.Time]{State: DailyValueNone, SourceRef: "schedule.deadline"},
		Recurrence:               DailyValue[modulecore.TaskRecurrence]{State: DailyValueUnsupported, SourceRef: "schedule.recurrence", ReasonCode: "owner_has_no_recurrence"},
		EstimatedDurationSeconds: DailyValue[int64]{State: DailyValueKnown, SourceRef: "config.duration", Value: dailyPointer(int64(0))},
	}
}

func dailyDefinitionIDs(definitions []DailyDefinition) []string {
	ids := make([]string, len(definitions))
	for index, definition := range definitions {
		ids[index] = definition.DefinitionID
	}
	return ids
}

func dailyTimingFieldState(timing DailyTimingProjection, field string) (DailyValueState, string) {
	switch field {
	case "timezone":
		return timing.Timezone.State, timing.Timezone.ReasonCode
	case "scheduled_start_at":
		return timing.ScheduledStartAt.State, timing.ScheduledStartAt.ReasonCode
	case "not_before":
		return timing.NotBefore.State, timing.NotBefore.ReasonCode
	case "allowed_windows":
		return timing.AllowedWindows.State, timing.AllowedWindows.ReasonCode
	case "deadline_at":
		return timing.DeadlineAt.State, timing.DeadlineAt.ReasonCode
	case "recurrence":
		return timing.Recurrence.State, timing.Recurrence.ReasonCode
	case "estimated_duration_seconds":
		return timing.EstimatedDurationSeconds.State, timing.EstimatedDurationSeconds.ReasonCode
	default:
		return "", ""
	}
}

func dailyPointer[T any](value T) *T { return &value }

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return encoded
}

type dailyTestSource struct {
	descriptor DailySourceDescriptor
	snapshot   DailySourceSnapshot
	err        error
	snapshotFn func(context.Context, tool.ToolExecutionScope, int) (DailySourceSnapshot, error)
	calls      int
	lastScope  tool.ToolExecutionScope
}

func (s *dailyTestSource) Descriptor() DailySourceDescriptor { return s.descriptor }

func (s *dailyTestSource) Snapshot(ctx context.Context, scope tool.ToolExecutionScope) (DailySourceSnapshot, error) {
	s.calls++
	s.lastScope = scope
	if s.snapshotFn != nil {
		return s.snapshotFn(ctx, scope, s.calls)
	}
	if s.err != nil {
		return DailySourceSnapshot{}, s.err
	}
	return s.snapshot, nil
}

func dailyTestUserContext(t *testing.T, requestID, userID string) context.Context {
	t.Helper()
	scope, err := tool.NewToolExecutionScope(requestID, tool.ActorKindUser, userID, userID,
		[]string{tool.DataScopePublic, tool.DataScopeUser}, tool.AuthenticationSourceHTTP)
	if err != nil {
		t.Fatalf("NewToolExecutionScope(user) error = %v", err)
	}
	return tool.WithToolExecutionScope(context.Background(), scope)
}

func dailyTestAgentContext(t *testing.T, requestID, actorID string, scopes []string) context.Context {
	t.Helper()
	if scopes == nil {
		scopes = []string{tool.DataScopePublic}
	}
	scope, err := tool.NewToolExecutionScope(requestID, tool.ActorKindAgent, actorID, "", scopes, tool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		t.Fatalf("NewToolExecutionScope(agent) error = %v", err)
	}
	return tool.WithToolExecutionScope(context.Background(), scope)
}
