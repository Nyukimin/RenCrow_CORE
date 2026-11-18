package taskmeeting

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	evidenceKindRegistration    = "registration_snapshot"
	evidenceKindWorldScope      = "world_scope"
	evidenceKindPresence        = "presence"
	evidenceKindSpeaker         = "speaker_utterance"
	evidenceKindDelivery        = "delivery"
	evidenceKindDeliveryUnknown = "delivery_unknown"
	evidenceKindResume          = "resume"
	evidenceKindFinish          = "finish"
	evidenceKindPartial         = "partial"
	evidenceKindPartialMinutes  = "partial_minutes"
	evidenceKindAnswerAttempt   = "answer_attempt"
)

type registrationSnapshotPayload struct {
	ScheduleID         modulecore.ScheduleID `json:"schedule_id"`
	Revision           uint64                `json:"revision"`
	ConfiguredActors   []string              `json:"configured_actors"`
	WorldActorBindings []WorldActorBinding   `json:"world_actor_bindings"`
	RegisteredActors   []string              `json:"registered_actors"`
	WorldBaseURL       string                `json:"world_base_url,omitempty"`
	WorldCredentialEnv string                `json:"world_credential_env,omitempty"`
	WorldID            string                `json:"world_id,omitempty"`
	TimelineID         string                `json:"timeline_id,omitempty"`
	TimelineEpoch      string                `json:"timeline_epoch,omitempty"`
	Rooms              RoomRefs              `json:"rooms,omitempty"`
	Timing             modulecore.TaskTiming `json:"timing,omitempty"`
	DeadlineAfterSecs  int64                 `json:"meeting_deadline_after_seconds,omitempty"`
}

type speakerReportClaimPayload struct {
	Evidence SpeakerEvidence `json:"evidence"`
	Report   Report          `json:"report"`
}

type speakerAnswerClaimPayload struct {
	Evidence SpeakerEvidence `json:"evidence"`
	Answer   Answer          `json:"answer"`
}

type answerAttemptClaimPayload struct {
	Attempt AnswerAttempt `json:"attempt"`
}

// NewOccurrence snapshots the configured roster and schedule for one stable
// slot. It does not allocate a TaskID, RunID, room, attendee, or World record.
func NewOccurrence(config ConfigSnapshot, key OccurrenceKey) (Meeting, error) {
	if err := validateConfig(config); err != nil {
		return Meeting{}, err
	}
	if err := key.Validate(); err != nil {
		return Meeting{}, err
	}
	if key.ScheduleID != config.ScheduleID || key.Timezone != config.Timing.Timezone {
		return Meeting{}, errors.New("occurrence key does not match schedule snapshot")
	}
	if !contains(config.Timing.Recurrence.Times, key.Slot) {
		return Meeting{}, errors.New("occurrence slot is not present in the schedule timing")
	}
	location, err := time.LoadLocation(key.Timezone)
	if err != nil {
		return Meeting{}, fmt.Errorf("load occurrence timezone: %w", err)
	}
	date, err := time.Parse("2006-01-02", key.LocalDate)
	if err != nil || date.Format("2006-01-02") != key.LocalDate {
		return Meeting{}, errors.New("local_date must be a canonical YYYY-MM-DD date")
	}
	clock, err := time.Parse("15:04", key.Slot)
	if err != nil || clock.Format("15:04") != key.Slot {
		return Meeting{}, errors.New("slot must be a canonical HH:MM time")
	}
	scheduledAt := time.Date(date.Year(), date.Month(), date.Day(), clock.Hour(), clock.Minute(), 0, 0, location)
	localScheduledAt := scheduledAt.In(location)
	if localScheduledAt.Year() != date.Year() || localScheduledAt.YearDay() != date.YearDay() ||
		localScheduledAt.Hour() != clock.Hour() || localScheduledAt.Minute() != clock.Minute() {
		return Meeting{}, errors.New("scheduled local time is not representable in the configured timezone")
	}
	if !config.Timing.Ready(scheduledAt) {
		return Meeting{}, errors.New("scheduled slot is not ready under TaskTiming")
	}
	deadlineAt, err := deriveMeetingDeadline(scheduledAt, config.Timing, config.MeetingDeadlineAfterSecs)
	if err != nil {
		return Meeting{}, err
	}
	meeting := Meeting{
		Key:                      key,
		ScheduleRevision:         config.Revision,
		Timing:                   cloneTiming(config.Timing),
		WorldBaseURL:             config.WorldBaseURL,
		WorldCredentialEnv:       config.WorldCredentialEnv,
		WorldID:                  config.WorldID,
		TimelineID:               config.TimelineID,
		TimelineEpoch:            config.TimelineEpoch,
		Participants:             append([]string(nil), config.ConfiguredActors...),
		WorldActorBindings:       append([]WorldActorBinding(nil), config.WorldActorBindings...),
		RegistrationEvidenceID:   config.RegistrationEvidenceID,
		Rooms:                    config.Rooms,
		MeetingDeadlineAfterSecs: config.MeetingDeadlineAfterSecs,
		ScheduledAt:              scheduledAt,
		DeadlineAt:               deadlineAt,
		Status:                   StatusScheduled,
		Generation:               1,
		Epoch:                    1,
		ProgressionRevision:      1,
		CheckpointSequence:       1,
		Attendance:               []Attendance{},
		Reports:                  []Report{},
		Answers:                  []Answer{},
		EvidenceClaims:           []EvidenceClaim{},
	}
	registrationSnapshot := registrationSnapshotPayload{
		ScheduleID: config.ScheduleID, Revision: config.Revision,
		ConfiguredActors:   append([]string(nil), config.ConfiguredActors...),
		WorldActorBindings: append([]WorldActorBinding(nil), config.WorldActorBindings...),
		RegisteredActors:   append([]string(nil), config.RegisteredActors...),
		WorldBaseURL:       config.WorldBaseURL, WorldCredentialEnv: config.WorldCredentialEnv,
		WorldID: config.WorldID, TimelineID: config.TimelineID, TimelineEpoch: config.TimelineEpoch,
		Rooms: config.Rooms, Timing: cloneTiming(config.Timing), DeadlineAfterSecs: config.MeetingDeadlineAfterSecs,
	}
	claim, err := newEvidenceClaim(config.RegistrationEvidenceID, evidenceKindRegistration, registrationSnapshot)
	if err != nil {
		return Meeting{}, err
	}
	meeting.EvidenceClaims = append(meeting.EvidenceClaims, claim)
	return meeting, meeting.Validate()
}

func validateConfig(config ConfigSnapshot) error {
	if err := config.ScheduleID.Validate(); err != nil {
		return fmt.Errorf("schedule_id: %w", err)
	}
	if config.Revision == 0 {
		return errors.New("schedule revision must be positive")
	}
	if err := config.RegistrationEvidenceID.Validate(); err != nil {
		return fmt.Errorf("registration_evidence_id: %w", err)
	}
	if config.MeetingDeadlineAfterSecs <= 0 {
		return errors.New("meeting deadline must be a positive bounded duration")
	}
	if err := config.Rooms.Validate(); err != nil {
		return err
	}
	if config.WorldBaseURL != "" || config.WorldCredentialEnv != "" || config.WorldID != "" || config.TimelineID != "" || config.TimelineEpoch != "" {
		if !validTaskMeetingWorldEndpoint(config.WorldBaseURL) || !validTaskMeetingCredentialEnvName(config.WorldCredentialEnv) ||
			!canonicalOwnerValue(config.WorldID) || !canonicalOwnerValue(config.TimelineID) || !canonicalOwnerValue(config.TimelineEpoch) {
			return errors.New("frozen World read scope is incomplete or invalid")
		}
	}
	if err := ValidateDailyMeetingTiming(config.Timing); err != nil {
		return fmt.Errorf("timing: %w", err)
	}
	if len(config.ConfiguredActors) != 8 {
		return errors.New("configured meeting roster must contain exactly 8 actors")
	}
	configured := make(map[string]struct{}, len(config.ConfiguredActors))
	for _, actorID := range config.ConfiguredActors {
		if err := validateActorID(actorID); err != nil {
			return fmt.Errorf("configured roster: %w", err)
		}
		if _, exists := configured[actorID]; exists {
			return fmt.Errorf("configured roster contains duplicate actor %q", actorID)
		}
		configured[actorID] = struct{}{}
	}
	registered := make(map[string]struct{}, len(config.RegisteredActors))
	for _, actorID := range config.RegisteredActors {
		if err := validateActorID(actorID); err != nil {
			return fmt.Errorf("registered actor snapshot: %w", err)
		}
		if _, exists := registered[actorID]; exists {
			return fmt.Errorf("registered actor snapshot contains duplicate actor %q", actorID)
		}
		registered[actorID] = struct{}{}
	}
	for actorID := range configured {
		if _, exists := registered[actorID]; !exists {
			return fmt.Errorf("configured actor %q is not registered", actorID)
		}
	}
	bindings := make(map[string]string, len(config.WorldActorBindings))
	worldActors := make(map[string]struct{}, len(config.WorldActorBindings))
	for _, binding := range config.WorldActorBindings {
		if err := validateActorID(binding.CoreActorID); err != nil {
			return fmt.Errorf("World binding CORE actor: %w", err)
		}
		if strings.TrimSpace(binding.WorldActorID) == "" || strings.TrimSpace(binding.WorldActorID) != binding.WorldActorID {
			return errors.New("World actor binding requires an exact canonical WORLD actor ID")
		}
		if _, ok := configured[binding.CoreActorID]; !ok {
			return fmt.Errorf("World binding refers to unconfigured CORE actor %q", binding.CoreActorID)
		}
		if _, duplicate := bindings[binding.CoreActorID]; duplicate {
			return fmt.Errorf("duplicate World binding for CORE actor %q", binding.CoreActorID)
		}
		if _, duplicate := worldActors[binding.WorldActorID]; duplicate {
			return fmt.Errorf("duplicate World actor binding %q", binding.WorldActorID)
		}
		bindings[binding.CoreActorID] = binding.WorldActorID
		worldActors[binding.WorldActorID] = struct{}{}
	}
	if len(bindings) != len(configured) {
		return errors.New("every configured CORE actor requires one explicit WORLD actor binding")
	}
	return nil
}

func deriveMeetingDeadline(start time.Time, timing modulecore.TaskTiming, deadlineAfterSecs int64) (time.Time, error) {
	if deadlineAfterSecs <= 0 || deadlineAfterSecs > int64((time.Duration(1<<63-1))/time.Second) {
		return time.Time{}, errors.New("meeting deadline duration is outside the supported range")
	}
	deadline := start.Add(time.Duration(deadlineAfterSecs) * time.Second)
	if !deadline.After(start) {
		return time.Time{}, errors.New("meeting deadline must be after its scheduled time")
	}
	if timing.DeadlineAt != nil && timing.DeadlineAt.Before(deadline) {
		deadline = *timing.DeadlineAt
	}
	if !deadline.After(start) {
		return time.Time{}, errors.New("TaskTiming deadline must be after the meeting slot")
	}
	return deadline, nil
}

func (meeting Meeting) Guard() Guard {
	return Guard{Generation: meeting.Generation, Epoch: meeting.Epoch, ProgressionRevision: meeting.ProgressionRevision}
}

func (meeting Meeting) Validate() error {
	if err := meeting.Key.Validate(); err != nil {
		return err
	}
	if meeting.ScheduleRevision == 0 || meeting.Generation == 0 || meeting.Epoch == 0 ||
		meeting.ProgressionRevision == 0 || meeting.CheckpointSequence == 0 {
		return errors.New("meeting revision, generation, epoch, and checkpoint sequence must be positive")
	}
	claimIDs := make(map[modulecore.EvidenceID]struct{}, len(meeting.EvidenceClaims))
	for _, claim := range meeting.EvidenceClaims {
		if err := claim.EvidenceID.Validate(); err != nil {
			return fmt.Errorf("evidence claim id: %w", err)
		}
		if _, exists := claimIDs[claim.EvidenceID]; exists {
			return errors.New("meeting contains duplicate evidence claim IDs")
		}
		claimIDs[claim.EvidenceID] = struct{}{}
		if !validEvidenceKind(claim.Kind) || len(claim.SHA256) != 64 || strings.ToLower(claim.SHA256) != claim.SHA256 {
			return errors.New("meeting contains malformed evidence claim")
		}
		if _, err := hex.DecodeString(claim.SHA256); err != nil {
			return errors.New("meeting evidence claim hash must be hexadecimal")
		}
	}
	if err := meeting.RegistrationEvidenceID.Validate(); err != nil {
		return fmt.Errorf("registration_evidence_id: %w", err)
	}
	if claimed, err := meeting.hasEvidenceClaim(meeting.RegistrationEvidenceID, evidenceKindRegistration, nil); err != nil || !claimed {
		return errors.New("registration snapshot evidence claim is missing")
	}
	if err := ValidateDailyMeetingTiming(meeting.Timing); err != nil {
		return fmt.Errorf("timing: %w", err)
	}
	if meeting.Timing.Timezone != meeting.Key.Timezone {
		return errors.New("meeting timing must use the daily occurrence timezone")
	}
	if meeting.WorldBaseURL != "" || meeting.WorldCredentialEnv != "" || meeting.WorldID != "" || meeting.TimelineID != "" || meeting.TimelineEpoch != "" {
		if !validTaskMeetingWorldEndpoint(meeting.WorldBaseURL) || !validTaskMeetingCredentialEnvName(meeting.WorldCredentialEnv) ||
			!canonicalOwnerValue(meeting.WorldID) || !canonicalOwnerValue(meeting.TimelineID) || !canonicalOwnerValue(meeting.TimelineEpoch) {
			return errors.New("frozen World read scope is incomplete or invalid")
		}
	}
	times := meeting.Timing.Recurrence.Times
	if err := meeting.Rooms.Validate(); err != nil {
		return err
	}
	if meeting.SharedThreadID != "" {
		if err := meeting.SharedThreadID.Validate(); err != nil {
			return fmt.Errorf("shared_thread_id: %w", err)
		}
	}
	for name, requestID := range map[string]string{"thread_request_id": meeting.ThreadRequestID, "gather_request_id": meeting.GatherRequestID, "transcript_request_id": meeting.TranscriptRequestID} {
		if requestID != "" && (strings.TrimSpace(requestID) != requestID || !strings.HasPrefix(requestID, meeting.Key.String()+"|")) {
			return fmt.Errorf("%s must be a canonical occurrence-scoped request key", name)
		}
	}
	if len(meeting.Participants) != 8 {
		return errors.New("meeting snapshot must contain exactly 8 participants")
	}
	if len(meeting.WorldActorBindings) != len(meeting.Participants) {
		return errors.New("meeting snapshot requires an explicit World actor binding for every CORE participant")
	}
	boundCore, boundWorld := map[string]bool{}, map[string]bool{}
	for _, binding := range meeting.WorldActorBindings {
		if !contains(meeting.Participants, binding.CoreActorID) || binding.CoreActorID == "" || binding.WorldActorID == "" ||
			strings.TrimSpace(binding.WorldActorID) != binding.WorldActorID || boundCore[binding.CoreActorID] || boundWorld[binding.WorldActorID] {
			return errors.New("meeting World actor bindings must be one-to-one and match the frozen roster")
		}
		boundCore[binding.CoreActorID], boundWorld[binding.WorldActorID] = true, true
	}
	seen := make(map[string]struct{}, len(meeting.Participants))
	for _, actorID := range meeting.Participants {
		if err := validateActorID(actorID); err != nil {
			return err
		}
		if _, exists := seen[actorID]; exists {
			return fmt.Errorf("meeting snapshot contains duplicate participant %q", actorID)
		}
		seen[actorID] = struct{}{}
	}
	if meeting.MeetingDeadlineAfterSecs <= 0 || meeting.MeetingDeadlineAfterSecs > int64((time.Duration(1<<63-1))/time.Second) ||
		meeting.ScheduledAt.IsZero() || !meeting.DeadlineAt.After(meeting.ScheduledAt) {
		return errors.New("meeting schedule and deadline are invalid")
	}
	location, err := time.LoadLocation(meeting.Key.Timezone)
	if err != nil {
		return fmt.Errorf("load meeting timezone: %w", err)
	}
	date, err := time.Parse("2006-01-02", meeting.Key.LocalDate)
	if err != nil || date.Format("2006-01-02") != meeting.Key.LocalDate {
		return errors.New("meeting local date is not canonical")
	}
	clock, err := time.Parse("15:04", meeting.Key.Slot)
	if err != nil || clock.Format("15:04") != meeting.Key.Slot {
		return errors.New("meeting slot is not canonical")
	}
	expectedStart := time.Date(date.Year(), date.Month(), date.Day(), clock.Hour(), clock.Minute(), 0, 0, location)
	expectedDeadline, err := deriveMeetingDeadline(expectedStart, meeting.Timing, meeting.MeetingDeadlineAfterSecs)
	if err != nil {
		return err
	}
	if !meeting.ScheduledAt.Equal(expectedStart) ||
		!meeting.DeadlineAt.Equal(expectedDeadline) ||
		!contains(times, meeting.Key.Slot) || !meeting.Timing.Ready(expectedStart) {
		return errors.New("meeting occurrence does not match its stable schedule key and deadline")
	}
	if meeting.TaskRef != nil {
		if err := meeting.TaskRef.Validate(); err != nil {
			return fmt.Errorf("task_ref: %w", err)
		}
	}
	if meeting.RunRef != nil {
		if err := meeting.RunRef.Validate(); err != nil {
			return fmt.Errorf("run_ref: %w", err)
		}
	}
	if meeting.TaskManagerEvidenceID != "" {
		if err := meeting.TaskManagerEvidenceID.Validate(); err != nil {
			return fmt.Errorf("task_manager_evidence_id: %w", err)
		}
		if meeting.TaskRef == nil || meeting.RunRef == nil {
			return errors.New("TaskManager root evidence requires both native Task and Run references")
		}
	}
	switch meeting.Status {
	case StatusScheduled, StatusGathering, StatusReporting, StatusAnswering, StatusFinishing, StatusCompleted, StatusPartial, StatusMissed:
	default:
		return fmt.Errorf("invalid meeting status %q", meeting.Status)
	}
	if meeting.CurrentReportIndex < 0 || meeting.CurrentReportIndex > len(meeting.Participants) {
		return errors.New("current_report_index is out of range")
	}
	if meeting.ScopeEvidence == nil && len(meeting.Attendance) > 0 {
		return errors.New("attendance requires a current World scope attestation")
	}
	scopes := meeting.scopeEvidenceSnapshots()
	for _, evidence := range scopes {
		if err := validateWorldSnapshotEvidence(evidence.Snapshot); err != nil {
			return fmt.Errorf("world scope source snapshot: %w", err)
		}
		if err := evidence.EvidenceID.Validate(); err != nil {
			return fmt.Errorf("world scope evidence id: %w", err)
		}
		if err := evidence.ThreadID.Validate(); err != nil {
			return fmt.Errorf("world scope thread_id: %w", err)
		}
		isCurrentScope := meeting.ScopeEvidence != nil && evidence.EvidenceID == meeting.ScopeEvidence.EvidenceID
		if evidence.Key != meeting.Key || evidence.Generation == 0 || evidence.Generation > meeting.Generation ||
			evidence.Epoch == 0 || evidence.Epoch > meeting.Epoch || (isCurrentScope && (evidence.Generation != meeting.Generation || evidence.Epoch != meeting.Epoch)) ||
			evidence.ThreadID != meeting.SharedThreadID ||
			evidence.LivingRoomRef != meeting.Rooms.Living || evidence.DiningRoomRef != meeting.Rooms.Dining ||
			!sameActors(evidence.Audience, meeting.Participants) || evidence.ObservedAt.Before(meeting.ScheduledAt) ||
			!evidence.ObservedAt.Before(meeting.DeadlineAt) || !evidence.ValidUntil.After(evidence.ObservedAt) ||
			evidence.ValidUntil.After(meeting.DeadlineAt) {
			return errors.New("World scope evidence does not match this occurrence snapshot")
		}
		claimed, claimErr := meeting.hasEvidenceClaim(evidence.EvidenceID, evidenceKindWorldScope, evidence)
		if claimErr != nil {
			return fmt.Errorf("World scope evidence integrity claim: %w", claimErr)
		}
		if !claimed {
			return errors.New("World scope evidence is missing its integrity claim")
		}
	}
	if turn := meeting.CurrentTurn; turn != nil && turn.GenerationRequestID != "" && turn.Generation != 0 {
		scope, found := meeting.scopeEvidenceByID(turn.ScopeEvidenceID)
		if !found || turn.Generation > meeting.Generation || turn.Epoch > meeting.Epoch ||
			scope.Generation != turn.Generation || scope.Epoch != turn.Epoch {
			return errors.New("pending generation intent lost its original World scope proof")
		}
	}
	attendanceByActor := make(map[string]Attendance, len(meeting.Attendance))
	for _, attendance := range append(append([]Attendance(nil), meeting.AttendanceHistory...), meeting.Attendance...) {
		evidence := attendance.Evidence
		if !contains(meeting.Participants, evidence.ActorID) {
			return errors.New("attendance actor is not in the configured roster")
		}
		if err := evidence.EvidenceID.Validate(); err != nil {
			return fmt.Errorf("attendance evidence id: %w", err)
		}
		if err := validateWorldSnapshotEvidence(evidence.Snapshot); err != nil {
			return fmt.Errorf("attendance source snapshot: %w", err)
		}
		scope, scopeFound := meeting.scopeEvidenceByID(evidence.ScopeEvidenceID)
		if !scopeFound || evidence.Key != meeting.Key || evidence.Generation != scope.Generation || evidence.Epoch != scope.Epoch ||
			evidence.Snapshot != scope.Snapshot || evidence.ObservedAt.IsZero() || evidence.ObservedAt.Before(scope.ObservedAt) ||
			!evidence.ObservedAt.Before(meeting.DeadlineAt) || !evidence.ValidUntil.After(evidence.ObservedAt) ||
			evidence.ValidUntil.After(scope.ValidUntil) {
			return errors.New("attendance evidence is outside the World scope validity interval")
		}
		attendanceKey := string(evidence.ScopeEvidenceID) + "|" + evidence.ActorID
		if _, exists := attendanceByActor[attendanceKey]; exists {
			return errors.New("meeting contains duplicate attendance entries for a World scope")
		}
		if evidence.Present {
			if evidence.RoomRef != meeting.Rooms.Living && evidence.RoomRef != meeting.Rooms.Dining {
				return errors.New("present attendee has an unconfigured room reference")
			}
		} else if evidence.RoomRef != "" {
			return errors.New("absent attendee has a room reference")
		}
		claimed, err := meeting.hasEvidenceClaim(evidence.EvidenceID, evidenceKindPresence, evidence)
		if err != nil || !claimed {
			return errors.New("attendance evidence is missing its integrity claim")
		}
		attendanceByActor[attendanceKey] = attendance
	}
	messageIDs := make(map[modulecore.MessageID]struct{}, len(meeting.Reports)+len(meeting.Answers))
	for index, report := range meeting.Reports {
		if index >= len(meeting.Participants) || report.Utterance.ActorID != meeting.Participants[index] {
			return errors.New("reports must preserve the configured participant order")
		}
		if err := validateStoredUtterance(report.Utterance, meeting.Generation, meeting.Epoch); err != nil {
			return fmt.Errorf("report %d: %w", index, err)
		}
		if err := report.SpeakerEvidenceID.Validate(); err != nil {
			return fmt.Errorf("report %d speaker evidence: %w", index, err)
		}
		speakerScope, foundScope := meeting.scopeEvidenceByID(report.SpeakerScopeEvidenceID)
		if !foundScope || speakerScope.Generation != report.Utterance.Generation || speakerScope.Epoch != report.Utterance.Epoch {
			return fmt.Errorf("report %d speaker World scope does not match its generation", index)
		}
		if claimed, err := meeting.hasEvidenceClaim(report.SpeakerEvidenceID, evidenceKindSpeaker, nil); err != nil || !claimed {
			return fmt.Errorf("report %d speaker evidence is missing its integrity claim", index)
		}
		if err := meeting.validateUtteranceEvidence(report.Utterance); err != nil {
			return fmt.Errorf("report %d: %w", index, err)
		}
		if err := validateReport(report); err != nil {
			return fmt.Errorf("report %d: %w", index, err)
		}
		if _, exists := messageIDs[report.Utterance.MessageID]; exists {
			return errors.New("meeting contains a duplicate utterance message_id")
		}
		messageIDs[report.Utterance.MessageID] = struct{}{}
	}
	for index, answer := range meeting.Answers {
		if err := validateStoredUtterance(answer.Utterance, meeting.Generation, meeting.Epoch); err != nil {
			return fmt.Errorf("answer %d: %w", index, err)
		}
		if err := answer.SpeakerEvidenceID.Validate(); err != nil {
			return fmt.Errorf("answer %d speaker evidence: %w", index, err)
		}
		speakerScope, foundScope := meeting.scopeEvidenceByID(answer.SpeakerScopeEvidenceID)
		if !foundScope || speakerScope.Generation != answer.Utterance.Generation || speakerScope.Epoch != answer.Utterance.Epoch {
			return fmt.Errorf("answer %d speaker World scope does not match its generation", index)
		}
		if claimed, err := meeting.hasEvidenceClaim(answer.SpeakerEvidenceID, evidenceKindSpeaker, nil); err != nil || !claimed {
			return fmt.Errorf("answer %d speaker evidence is missing its integrity claim", index)
		}
		if err := meeting.validateUtteranceEvidence(answer.Utterance); err != nil {
			return fmt.Errorf("answer %d: %w", index, err)
		}
		if !contains(meeting.Participants, answer.Utterance.ActorID) || answer.Utterance.ActorID == answer.ReporterID ||
			!validAnswerKind(answer.Kind) || isAcknowledgementOnly(answer.Utterance.Body) {
			return fmt.Errorf("answer %d is not a substantive response from another participant", index)
		}
		report, found := meeting.findReport(answer.ConcernID.ReportMessageID)
		if !found || report.Utterance.ActorID != answer.ReporterID || answer.ConcernID.Ordinal == 0 || int(answer.ConcernID.Ordinal) > len(report.Concerns) {
			return fmt.Errorf("answer %d references an unknown concern", index)
		}
		if _, exists := messageIDs[answer.Utterance.MessageID]; exists {
			return errors.New("meeting contains a duplicate utterance message_id")
		}
		messageIDs[answer.Utterance.MessageID] = struct{}{}
		for priorIndex := 0; priorIndex < index; priorIndex++ {
			prior := meeting.Answers[priorIndex]
			if prior.ConcernID == answer.ConcernID && prior.Utterance.ActorID == answer.Utterance.ActorID {
				return errors.New("participant answered the same concern more than once")
			}
		}
	}
	seenAttempts := make(map[string]struct{}, len(meeting.AnswerAttempts))
	seenAttemptRuns := make(map[string]struct{}, len(meeting.AnswerAttempts))
	for index, attempt := range meeting.AnswerAttempts {
		if err := meeting.validateAnswerAttempt(attempt); err != nil {
			return fmt.Errorf("answer attempt %d: %w", index, err)
		}
		actorKey := attempt.ConcernID.String() + "|" + attempt.UnknownReceipt.ActorID
		if _, exists := seenAttempts[actorKey]; exists {
			return errors.New("participant has multiple terminal attempts for the same concern")
		}
		seenAttempts[actorKey] = struct{}{}
		runKey := string(attempt.UnknownReceipt.TaskID) + "|" + string(attempt.UnknownReceipt.RunID)
		if _, exists := seenAttemptRuns[runKey]; exists {
			return errors.New("one native Task/Run is recorded for multiple answer attempts")
		}
		seenAttemptRuns[runKey] = struct{}{}
		for _, answer := range meeting.Answers {
			if answer.ConcernID == attempt.ConcernID && answer.Utterance.ActorID == attempt.UnknownReceipt.ActorID {
				return errors.New("participant cannot have both an answer and terminal attempt for the same concern")
			}
		}
		if meeting.CurrentTurn != nil && meeting.CurrentTurn.Kind == TurnAnswer && meeting.CurrentTurn.ConcernID != nil &&
			meeting.CurrentTurn.ActorID == attempt.UnknownReceipt.ActorID && *meeting.CurrentTurn.ConcernID == attempt.ConcernID {
			return errors.New("answer attempt cannot coexist with its current answer turn")
		}
		if claimed, err := meeting.hasEvidenceClaim(attempt.UnknownReceipt.EvidenceID, evidenceKindAnswerAttempt, answerAttemptClaimPayload{Attempt: attempt}); err != nil || !claimed {
			return fmt.Errorf("answer attempt %d is missing its integrity claim", index)
		}
	}
	if err := meeting.validateProgressState(); err != nil {
		return err
	}
	if meeting.Status == StatusCompleted {
		if meeting.FinishEvidence == nil || meeting.PartialEvidence != nil || meeting.PartialMinutes != nil || !meeting.allUtterancesDelivered() || !meeting.allConcernsAnswered() {
			return errors.New("completed meeting requires delivered reports, answered concerns, and finish evidence")
		}
		evidence := meeting.FinishEvidence
		if err := evidence.EvidenceID.Validate(); err != nil {
			return fmt.Errorf("finish evidence id: %w", err)
		}
		if err := evidence.TranscriptArtifactID.Validate(); err != nil {
			return fmt.Errorf("transcript artifact id: %w", err)
		}
		if evidence.Key != meeting.Key || evidence.Generation != meeting.Generation || evidence.Epoch != meeting.Epoch ||
			evidence.ProgressionRevision+1 != meeting.ProgressionRevision || evidence.PersistedAt.IsZero() ||
			!evidence.PersistedAt.Before(meeting.DeadlineAt) {
			return errors.New("finish evidence does not match the completed checkpoint")
		}
		if claimed, err := meeting.hasEvidenceClaim(evidence.EvidenceID, evidenceKindFinish, *evidence); err != nil || !claimed {
			return errors.New("finish evidence is missing its integrity claim")
		}
	} else if meeting.FinishEvidence != nil {
		return errors.New("non-completed meeting must not have finish evidence")
	}
	if meeting.Status == StatusPartial || meeting.Status == StatusMissed {
		if meeting.PartialEvidence == nil || meeting.FinishEvidence != nil || meeting.CurrentTurn != nil {
			return errors.New("partial meeting requires terminal partial evidence and no current turn")
		}
		evidence := meeting.PartialEvidence
		if err := evidence.EvidenceID.Validate(); err != nil {
			return fmt.Errorf("partial evidence id: %w", err)
		}
		if evidence.Key != meeting.Key || evidence.Generation != meeting.Generation || evidence.Epoch != meeting.Epoch ||
			evidence.ObservedAt.IsZero() || evidence.ObservedAt.Before(meeting.DeadlineAt) || strings.TrimSpace(evidence.ReasonCode) == "" {
			return errors.New("partial evidence does not match the terminal occurrence")
		}
		if claimed, err := meeting.hasEvidenceClaim(evidence.EvidenceID, evidenceKindPartial, *evidence); err != nil || !claimed {
			return errors.New("partial evidence is missing its integrity claim")
		}
		if meeting.Status == StatusMissed && (evidence.ReasonCode != "missed" || len(meeting.Reports) != 0) {
			return errors.New("missed occurrence must have a missed reason and no reports")
		}
		if err := meeting.validateInterruptedTurn(evidence.InterruptedTurn); err != nil {
			return fmt.Errorf("partial interrupted turn: %w", err)
		}
		if meeting.PartialMinutes != nil {
			if err := meeting.validateStoredPartialMinutes(*meeting.PartialMinutes); err != nil {
				return fmt.Errorf("partial minutes evidence: %w", err)
			}
			if claimed, err := meeting.hasEvidenceClaim(meeting.PartialMinutes.EvidenceID, evidenceKindPartialMinutes, *meeting.PartialMinutes); err != nil || !claimed {
				return errors.New("partial minutes evidence is missing its integrity claim")
			}
		}
	} else if meeting.PartialEvidence != nil {
		return errors.New("active meeting must not have terminal partial evidence")
	}
	if meeting.Status != StatusPartial && meeting.Status != StatusMissed && meeting.PartialMinutes != nil {
		return errors.New("partial minutes evidence requires a partial or missed meeting")
	}
	return nil
}

func (meeting Meeting) validateProgressState() error {
	switch meeting.Status {
	case StatusScheduled:
		if meeting.CurrentReportIndex != 0 || meeting.CurrentTurn != nil || meeting.ScopeEvidence != nil || meeting.SharedThreadID != "" ||
			len(meeting.Attendance) != 0 || len(meeting.Reports) != 0 || len(meeting.Answers) != 0 {
			return errors.New("scheduled meeting cannot contain gathering or turn state")
		}
	case StatusGathering:
		if meeting.CurrentReportIndex != 0 || meeting.CurrentTurn != nil || len(meeting.Reports) != 0 || len(meeting.Answers) != 0 {
			return errors.New("gathering meeting cannot contain report or answer turns")
		}
	case StatusReporting:
		if meeting.CurrentReportIndex >= len(meeting.Participants) || meeting.CurrentTurn == nil ||
			meeting.CurrentTurn.Kind != TurnReport || meeting.CurrentTurn.ActorID != meeting.Participants[meeting.CurrentReportIndex] {
			return errors.New("reporting meeting requires the next configured report turn")
		}
		if meeting.CurrentTurn.MessageID == nil {
			if len(meeting.Reports) != meeting.CurrentReportIndex {
				return errors.New("unsubmitted report turn has an inconsistent report count")
			}
		} else if len(meeting.Reports) != meeting.CurrentReportIndex+1 ||
			meeting.Reports[meeting.CurrentReportIndex].Utterance.MessageID != *meeting.CurrentTurn.MessageID {
			return errors.New("current report utterance does not match the report cursor")
		}
		if err := validateGenerationIntent(meeting.CurrentTurn); err != nil {
			return err
		}
	case StatusAnswering:
		if meeting.CurrentReportIndex >= len(meeting.Participants) || len(meeting.Reports) != meeting.CurrentReportIndex+1 {
			return errors.New("answering meeting requires the current participant report")
		}
		report := meeting.Reports[meeting.CurrentReportIndex]
		if report.ConcernDeclaration != ConcernsListed || report.Utterance.DeliveryState != DeliveryDelivered {
			return errors.New("answering requires a delivered report with explicit concerns")
		}
		if meeting.CurrentTurn != nil {
			if meeting.CurrentTurn.Kind != TurnAnswer || meeting.CurrentTurn.ConcernID == nil ||
				meeting.CurrentTurn.ActorID == report.Utterance.ActorID ||
				meeting.CurrentTurn.ConcernID.ReportMessageID != report.Utterance.MessageID {
				return errors.New("current answer turn does not match the current report")
			}
			if meeting.CurrentTurn.MessageID != nil {
				answer, ok := meeting.findAnswer(*meeting.CurrentTurn.MessageID)
				if !ok || answer.Utterance.ActorID != meeting.CurrentTurn.ActorID || answer.ConcernID != *meeting.CurrentTurn.ConcernID {
					return errors.New("current answer utterance does not match the answer turn")
				}
			}
			if err := validateGenerationIntent(meeting.CurrentTurn); err != nil {
				return err
			}
		}
	case StatusFinishing:
		if meeting.CurrentReportIndex != len(meeting.Participants) || len(meeting.Reports) != len(meeting.Participants) ||
			meeting.CurrentTurn != nil || !meeting.allConcernsAnswered() || !meeting.allUtterancesDelivered() {
			return errors.New("finishing requires all eight delivered reports and answered concerns")
		}
	case StatusCompleted:
		if meeting.CurrentReportIndex != len(meeting.Participants) || len(meeting.Reports) != len(meeting.Participants) || meeting.CurrentTurn != nil {
			return errors.New("completed meeting must contain eight reports and no current turn")
		}
	case StatusPartial, StatusMissed:
		if meeting.CurrentTurn != nil {
			return errors.New("terminal meeting cannot retain a current turn")
		}
	}
	return nil
}

func (meeting Meeting) validateInterruptedTurn(turn *Turn) error {
	if turn == nil {
		return nil
	}
	if (turn.Kind != TurnReport && turn.Kind != TurnAnswer) || !contains(meeting.Participants, turn.ActorID) {
		return errors.New("interrupted turn has an invalid kind or actor")
	}
	if err := validateGenerationIntent(turn); err != nil {
		return err
	}
	if turn.GenerationRequestID != "" {
		if turn.Generation != meeting.Generation || turn.Epoch != meeting.Epoch {
			return errors.New("interrupted generation turn has a stale generation")
		}
		scope, found := meeting.scopeEvidenceByID(turn.ScopeEvidenceID)
		if !found || scope.Generation != turn.Generation || scope.Epoch != turn.Epoch {
			return errors.New("interrupted generation turn lost its World scope reference")
		}
	}
	if turn.Kind == TurnReport {
		if turn.ConcernID != nil {
			return errors.New("interrupted report turn must not name a concern")
		}
		if turn.MessageID != nil {
			report, found := meeting.findReport(*turn.MessageID)
			if !found || report.Utterance.ActorID != turn.ActorID {
				return errors.New("interrupted report turn does not match its recorded report")
			}
		}
		return nil
	}
	if turn.ConcernID == nil {
		return errors.New("interrupted answer turn requires a concern")
	}
	report, found := meeting.findReport(turn.ConcernID.ReportMessageID)
	if !found || turn.ConcernID.Ordinal == 0 || int(turn.ConcernID.Ordinal) > len(report.Concerns) || turn.ActorID == report.Utterance.ActorID {
		return errors.New("interrupted answer turn does not match its report concern")
	}
	if turn.MessageID != nil {
		answer, found := meeting.findAnswer(*turn.MessageID)
		if !found || answer.Utterance.ActorID != turn.ActorID || answer.ConcernID != *turn.ConcernID {
			return errors.New("interrupted answer turn does not match its recorded answer")
		}
	}
	return nil
}

func validateStoredUtterance(utterance Utterance, generation, epoch uint64) error {
	if err := utterance.MessageID.Validate(); err != nil {
		return fmt.Errorf("message_id: %w", err)
	}
	if err := validateActorID(utterance.ActorID); err != nil {
		return err
	}
	if strings.TrimSpace(utterance.Body) == "" || utterance.Generation == 0 || utterance.Generation > generation ||
		utterance.Epoch == 0 || utterance.Epoch > epoch {
		return errors.New("utterance body or generation/epoch is invalid")
	}
	if utterance.SourceTaskRef != nil {
		if err := utterance.SourceTaskRef.Validate(); err != nil {
			return fmt.Errorf("source_task_ref: %w", err)
		}
	}
	if utterance.SourceRunRef != nil {
		if err := utterance.SourceRunRef.Validate(); err != nil {
			return fmt.Errorf("source_run_ref: %w", err)
		}
		if utterance.SourceTaskRef == nil {
			return errors.New("source_run_ref requires a native source_task_ref")
		}
	}
	if (utterance.DeliveryScopeEvidenceID == "") != (utterance.DeliveryGeneration == 0) ||
		(utterance.DeliveryScopeEvidenceID == "") != (utterance.DeliveryEpoch == 0) ||
		utterance.DeliveryGeneration > generation || utterance.DeliveryEpoch > epoch {
		return errors.New("delivery attempt World scope and original generation must be complete")
	}
	switch utterance.DeliveryState {
	case DeliveryUnsent, DeliverySending:
		if utterance.DeliveryEvidence != nil || utterance.UnknownEvidence != nil || utterance.UnknownReason != "" {
			return errors.New("unsent or sending utterance must not have delivery evidence")
		}
	case DeliveryUnknown:
		if strings.TrimSpace(utterance.UnknownReason) == "" {
			return errors.New("unknown delivery requires a reason")
		}
		if utterance.DeliveryEvidence != nil {
			return errors.New("unknown delivery must not have a resolved delivery receipt")
		}
		if utterance.UnknownEvidence != nil {
			if err := utterance.UnknownEvidence.EvidenceID.Validate(); err != nil {
				return fmt.Errorf("unknown evidence id: %w", err)
			}
			if utterance.UnknownEvidence.MessageID != utterance.MessageID || utterance.UnknownEvidence.Reason != utterance.UnknownReason {
				return errors.New("unknown delivery evidence does not match the utterance")
			}
		} else if utterance.UnknownReason != "process_restarted_during_delivery" {
			return errors.New("unknown delivery without a dispatch receipt must be caused by a restart")
		}
	case DeliveryDelivered, DeliveryUndelivered:
		if utterance.DeliveryEvidence == nil {
			return errors.New("resolved delivery requires evidence")
		}
		if err := utterance.DeliveryEvidence.EvidenceID.Validate(); err != nil {
			return fmt.Errorf("delivery evidence id: %w", err)
		}
		if utterance.DeliveryEvidence.MessageID != utterance.MessageID ||
			(utterance.DeliveryState == DeliveryDelivered) != utterance.DeliveryEvidence.Delivered {
			return errors.New("delivery evidence does not match its resolved state")
		}
	default:
		return fmt.Errorf("invalid delivery state %q", utterance.DeliveryState)
	}
	return nil
}

func validateGenerationIntent(turn *Turn) error {
	if turn == nil || (turn.GenerationRequestID == "" && turn.GenerationInputSHA256 == "" && len(turn.GenerationInputJSON) == 0) {
		return nil
	}
	if strings.TrimSpace(turn.GenerationRequestID) == "" || turn.GenerationRequestID != strings.TrimSpace(turn.GenerationRequestID) || len(turn.GenerationInputSHA256) != 64 || strings.ToLower(turn.GenerationInputSHA256) != turn.GenerationInputSHA256 || len(turn.GenerationInputJSON) == 0 || !json.Valid(turn.GenerationInputJSON) {
		return errors.New("generation intent requires a canonical request ID, frozen JSON input, and lowercase SHA-256 digest")
	}
	if turn.Generation != 0 || turn.Epoch != 0 || turn.ScopeEvidenceID != "" {
		if turn.Generation == 0 || turn.Epoch == 0 || turn.ScopeEvidenceID == "" {
			return errors.New("generation intent source generation and World scope must be complete")
		}
	}
	digest := sha256.Sum256(turn.GenerationInputJSON)
	if hex.EncodeToString(digest[:]) != turn.GenerationInputSHA256 {
		return errors.New("generation intent digest does not match frozen JSON input")
	}
	return nil
}

func (meeting Meeting) validateUtteranceEvidence(utterance Utterance) error {
	if evidence := utterance.DeliveryEvidence; evidence != nil {
		if err := evidence.EvidenceID.Validate(); err != nil {
			return fmt.Errorf("delivery evidence id: %w", err)
		}
		if err := evidence.ThreadID.Validate(); err != nil {
			return fmt.Errorf("delivery thread_id: %w", err)
		}
		if evidence.Key != meeting.Key || evidence.MessageID != utterance.MessageID || evidence.ThreadID != meeting.SharedThreadID ||
			(evidence.ScopeEvidenceID != "" || utterance.DeliveryScopeEvidenceID != "") && evidence.ScopeEvidenceID != utterance.DeliveryScopeEvidenceID ||
			evidence.Generation == 0 || evidence.Generation > meeting.Generation || evidence.Epoch == 0 || evidence.Epoch > meeting.Epoch ||
			evidence.ObservedAt.IsZero() || evidence.ObservedAt.Before(meeting.ScheduledAt) || !evidence.ObservedAt.Before(meeting.DeadlineAt) {
			return errors.New("delivery evidence does not match its occurrence checkpoint")
		}
		if evidence.ScopeEvidenceID != "" {
			scope, found := meeting.scopeEvidenceByID(evidence.ScopeEvidenceID)
			if !found || evidence.Generation != scope.Generation || evidence.Epoch != scope.Epoch ||
				evidence.Generation != utterance.DeliveryGeneration || evidence.Epoch != utterance.DeliveryEpoch {
				return errors.New("delivery evidence does not match its frozen World scope")
			}
		}
		if evidence.Delivered && !sameActors(evidence.Audience, meeting.Participants) ||
			!evidence.Delivered && len(evidence.Audience) != 0 && !sameActors(evidence.Audience, meeting.Participants) {
			return errors.New("delivery evidence audience does not match the meeting")
		}
		if claimed, err := meeting.hasEvidenceClaim(evidence.EvidenceID, evidenceKindDelivery, *evidence); err != nil || !claimed {
			return errors.New("delivery evidence is missing its integrity claim")
		}
	}
	if evidence := utterance.UnknownEvidence; evidence != nil {
		if err := evidence.EvidenceID.Validate(); err != nil {
			return fmt.Errorf("unknown evidence id: %w", err)
		}
		if evidence.Key != meeting.Key || evidence.MessageID != utterance.MessageID || evidence.Reason != utterance.UnknownReason ||
			evidence.Generation == 0 || evidence.Generation > meeting.Generation || evidence.Epoch == 0 || evidence.Epoch > meeting.Epoch ||
			evidence.ObservedAt.IsZero() || evidence.ObservedAt.Before(meeting.ScheduledAt) || !evidence.ObservedAt.Before(meeting.DeadlineAt) {
			return errors.New("unknown delivery evidence does not match its occurrence checkpoint")
		}
		if claimed, err := meeting.hasEvidenceClaim(evidence.EvidenceID, evidenceKindDeliveryUnknown, *evidence); err != nil || !claimed {
			return errors.New("unknown delivery evidence is missing its integrity claim")
		}
	}
	return nil
}

func (meeting Meeting) checkGuard(guard Guard) error {
	if err := meeting.Validate(); err != nil {
		return fmt.Errorf("invalid meeting state: %w", err)
	}
	if guard.Generation != meeting.Generation || guard.Epoch != meeting.Epoch || guard.ProgressionRevision != meeting.ProgressionRevision {
		return ErrStale
	}
	return nil
}

func validateWorldSnapshotEvidence(snapshot WorldSnapshotEvidence) error {
	if strings.TrimSpace(snapshot.WorldID) == "" || strings.TrimSpace(snapshot.WorldID) != snapshot.WorldID ||
		strings.TrimSpace(snapshot.TimelineID) == "" || strings.TrimSpace(snapshot.TimelineID) != snapshot.TimelineID ||
		strings.TrimSpace(snapshot.TimelineEpoch) == "" || strings.TrimSpace(snapshot.TimelineEpoch) != snapshot.TimelineEpoch {
		return errors.New("World ID, Timeline ID, and Timeline epoch are required")
	}
	if len(snapshot.StateHash) != 64 || strings.ToLower(snapshot.StateHash) != snapshot.StateHash {
		return errors.New("World state hash must be canonical lowercase SHA-256")
	}
	if _, err := hex.DecodeString(snapshot.StateHash); err != nil {
		return errors.New("World state hash must be hexadecimal")
	}
	if err := snapshot.OwnerProofID.Validate(); err != nil {
		return fmt.Errorf("World snapshot owner proof: %w", err)
	}
	return nil
}

func (meeting Meeting) commit(next Meeting) (Meeting, error) {
	next.ProgressionRevision++
	next.CheckpointSequence++
	if next.ProgressionRevision == 0 || next.CheckpointSequence == 0 {
		return Meeting{}, errors.New("meeting progression counter overflow")
	}
	if err := next.Validate(); err != nil {
		return Meeting{}, fmt.Errorf("invalid meeting transition: %w", err)
	}
	return next, nil
}

// SamePersistedSnapshot compares the canonical serialized owner value. This
// deliberately uses TaskTiming's canonical JSON representation, where nil and
// empty collection values encode identically, while still comparing every
// persisted meeting field.
func SamePersistedSnapshot(left, right Meeting) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func (meeting Meeting) BeginGathering(guard Guard, now time.Time) (Meeting, error) {
	if err := meeting.checkGuard(guard); err != nil {
		return Meeting{}, err
	}
	if meeting.Status != StatusScheduled {
		return Meeting{}, ErrInvalidState
	}
	if now.IsZero() || now.Before(meeting.ScheduledAt) || !now.Before(meeting.DeadlineAt) || !meeting.Timing.Ready(now) {
		return Meeting{}, ErrNotReady
	}
	next := meeting.clone()
	next.Status = StatusGathering
	return meeting.commit(next)
}

func (meeting Meeting) ConfirmWorldScope(guard Guard, evidence WorldScopeEvidence) (Meeting, error) {
	replayed, err := meeting.hasEvidenceClaim(evidence.EvidenceID, evidenceKindWorldScope, evidence)
	if err != nil {
		return Meeting{}, err
	}
	if replayed {
		return meeting.clone(), nil
	}
	if err := meeting.checkGuard(guard); err != nil {
		return Meeting{}, err
	}
	if meeting.Status == StatusScheduled || isTerminal(meeting.Status) {
		return Meeting{}, ErrInvalidState
	}
	if meeting.Rooms.Living == "" || meeting.Rooms.Dining == "" {
		return Meeting{}, ErrNotReady
	}
	if err := evidence.EvidenceID.Validate(); err != nil {
		return Meeting{}, fmt.Errorf("world scope evidence id: %w", err)
	}
	if err := validateWorldSnapshotEvidence(evidence.Snapshot); err != nil {
		return Meeting{}, fmt.Errorf("world scope source snapshot: %w", err)
	}
	if evidence.Key != meeting.Key || evidence.Generation != meeting.Generation || evidence.Epoch != meeting.Epoch {
		return Meeting{}, ErrStale
	}
	if err := evidence.ThreadID.Validate(); err != nil {
		return Meeting{}, fmt.Errorf("shared conversation thread_id: %w", err)
	}
	if meeting.SharedThreadID != "" && evidence.ThreadID != meeting.SharedThreadID {
		return Meeting{}, ErrConflict
	}
	if evidence.LivingRoomRef != meeting.Rooms.Living || evidence.DiningRoomRef != meeting.Rooms.Dining {
		return Meeting{}, ErrNotReady
	}
	if !sameActors(evidence.Audience, meeting.Participants) {
		return Meeting{}, errors.New("shared World scope must include every configured participant exactly once")
	}
	if evidence.ObservedAt.IsZero() || evidence.ObservedAt.Before(meeting.ScheduledAt) || !evidence.ObservedAt.Before(meeting.DeadlineAt) ||
		!evidence.ValidUntil.After(evidence.ObservedAt) || evidence.ValidUntil.After(meeting.DeadlineAt) {
		return Meeting{}, errors.New("world scope evidence requires a bounded validity interval")
	}
	next := meeting.clone()
	if err := next.addEvidenceClaim(evidence.EvidenceID, evidenceKindWorldScope, evidence); err != nil {
		return Meeting{}, err
	}
	if next.ScopeEvidence != nil && next.referencesWorldScope(next.ScopeEvidence.EvidenceID) {
		next.ScopeEvidenceHistory = append(next.ScopeEvidenceHistory, *cloneWorldScope(next.ScopeEvidence))
		for _, attendance := range next.Attendance {
			if attendance.Evidence.ScopeEvidenceID == next.ScopeEvidence.EvidenceID {
				next.AttendanceHistory = append(next.AttendanceHistory, attendance)
			}
		}
	}
	next.SharedThreadID = evidence.ThreadID
	next.ScopeEvidence = cloneWorldScope(&evidence)
	// Attendance must be re-observed against the newly attested shared scope.
	next.Attendance = []Attendance{}
	return meeting.commit(next)
}

func (meeting Meeting) RecordPresence(guard Guard, evidence PresenceEvidence) (Meeting, error) {
	if evidence.ScopeEvidenceID == "" && meeting.ScopeEvidence != nil {
		evidence.ScopeEvidenceID = meeting.ScopeEvidence.EvidenceID
	}
	replayed, err := meeting.hasEvidenceClaim(evidence.EvidenceID, evidenceKindPresence, evidence)
	if err != nil {
		return Meeting{}, err
	}
	if replayed {
		return meeting.clone(), nil
	}
	if err := meeting.checkGuard(guard); err != nil {
		return Meeting{}, err
	}
	if meeting.Status == StatusScheduled || isTerminal(meeting.Status) {
		return Meeting{}, ErrInvalidState
	}
	if meeting.ScopeEvidence == nil || meeting.ScopeEvidence.Generation != meeting.Generation || meeting.ScopeEvidence.Epoch != meeting.Epoch {
		return Meeting{}, ErrNotReady
	}
	if err := evidence.EvidenceID.Validate(); err != nil {
		return Meeting{}, fmt.Errorf("presence evidence id: %w", err)
	}
	if err := validateWorldSnapshotEvidence(evidence.Snapshot); err != nil {
		return Meeting{}, fmt.Errorf("presence source snapshot: %w", err)
	}
	if evidence.Key != meeting.Key || evidence.Generation != meeting.Generation || evidence.Epoch != meeting.Epoch {
		return Meeting{}, ErrStale
	}
	if evidence.Snapshot != meeting.ScopeEvidence.Snapshot {
		return Meeting{}, ErrStale
	}
	if !contains(meeting.Participants, evidence.ActorID) {
		return Meeting{}, errors.New("presence evidence actor is not in the configured roster")
	}
	if worldActorForCore(meeting.WorldActorBindings, evidence.ActorID) != evidence.WorldActorID || evidence.WorldActorID == "" {
		return Meeting{}, errors.New("presence evidence World actor does not match the frozen CORE-to-World binding")
	}
	if evidence.ObservedAt.IsZero() || evidence.ObservedAt.Before(meeting.ScheduledAt) || !evidence.ObservedAt.Before(meeting.DeadlineAt) ||
		!evidence.ValidUntil.After(evidence.ObservedAt) || evidence.ValidUntil.After(meeting.DeadlineAt) ||
		evidence.ObservedAt.Before(meeting.ScopeEvidence.ObservedAt) || evidence.ValidUntil.After(meeting.ScopeEvidence.ValidUntil) {
		return Meeting{}, errors.New("presence evidence validity must be within the attested World scope")
	}
	if evidence.Present {
		if evidence.RoomRef != meeting.Rooms.Living && evidence.RoomRef != meeting.Rooms.Dining {
			return Meeting{}, errors.New("present actor must have World evidence for living or dining")
		}
	} else if evidence.RoomRef != "" {
		return Meeting{}, errors.New("absent actor must not have a room reference")
	}
	next := meeting.clone()
	if err := next.addEvidenceClaim(evidence.EvidenceID, evidenceKindPresence, evidence); err != nil {
		return Meeting{}, err
	}
	for index, attendance := range next.Attendance {
		prior := attendance.Evidence
		if prior.ActorID != evidence.ActorID {
			continue
		}
		if evidence.ObservedAt.Before(prior.ObservedAt) {
			return Meeting{}, ErrStale
		}
		if evidence.ObservedAt.Equal(prior.ObservedAt) {
			return Meeting{}, ErrConflict
		}
		next.Attendance[index] = attendanceFromEvidence(evidence)
		return meeting.commit(next)
	}
	next.Attendance = append(next.Attendance, attendanceFromEvidence(evidence))
	return meeting.commit(next)
}

func (meeting Meeting) BeginReporting(guard Guard, now time.Time) (Meeting, error) {
	if err := meeting.checkGuard(guard); err != nil {
		return Meeting{}, err
	}
	if meeting.Status != StatusGathering {
		return Meeting{}, ErrInvalidState
	}
	if now.IsZero() || !now.Before(meeting.DeadlineAt) || !meeting.worldReadyAt(now) {
		return Meeting{}, ErrNotReady
	}
	next := meeting.clone()
	next.Status = StatusReporting
	next.CurrentReportIndex = 0
	next.CurrentTurn = &Turn{Kind: TurnReport, ActorID: next.Participants[0]}
	return meeting.commit(next)
}

func (meeting Meeting) RecordReport(guard Guard, report Report, evidence SpeakerEvidence) (Meeting, error) {
	claimPayload := speakerReportClaimPayload{Evidence: evidence, Report: report}
	replayedEvidence, claimErr := meeting.hasEvidenceClaim(evidence.EvidenceID, evidenceKindSpeaker, claimPayload)
	if claimErr != nil {
		return Meeting{}, claimErr
	}
	if existing, found := meeting.findReport(report.Utterance.MessageID); found {
		if sameReportContent(existing, report) && existing.SpeakerEvidenceID == evidence.EvidenceID && replayedEvidence {
			return meeting.clone(), nil
		}
		return Meeting{}, ErrConflict
	}
	if replayedEvidence {
		return Meeting{}, ErrConflict
	}
	if _, found := meeting.findAnswer(report.Utterance.MessageID); found {
		return Meeting{}, ErrConflict
	}
	if err := meeting.checkGuard(guard); err != nil {
		return Meeting{}, err
	}
	if meeting.Status != StatusReporting || meeting.CurrentTurn == nil || meeting.CurrentTurn.Kind != TurnReport ||
		meeting.CurrentTurn.MessageID != nil || meeting.CurrentTurn.ActorID != report.Utterance.ActorID {
		return Meeting{}, ErrInvalidState
	}
	if meeting.CurrentReportIndex >= len(meeting.Participants) || meeting.Participants[meeting.CurrentReportIndex] != report.Utterance.ActorID {
		return Meeting{}, ErrInvalidState
	}
	if err := meeting.validateSpeakerEvidence(evidence, report.Utterance, guard); err != nil {
		return Meeting{}, err
	}
	if err := validateReport(report); err != nil {
		return Meeting{}, err
	}
	if report.Utterance.DeliveryState != "" && report.Utterance.DeliveryState != DeliveryUnsent {
		return Meeting{}, errors.New("new report delivery state must be empty or unsent")
	}
	if report.Utterance.DeliveryEvidence != nil || report.Utterance.UnknownEvidence != nil || report.Utterance.UnknownReason != "" ||
		(report.Utterance.Generation != 0 && report.Utterance.Generation != evidence.Generation) ||
		(report.Utterance.Epoch != 0 && report.Utterance.Epoch != evidence.Epoch) {
		return Meeting{}, errors.New("new report must not supply delivery proof or a stale generation/epoch")
	}
	next := meeting.clone()
	if err := next.addEvidenceClaim(evidence.EvidenceID, evidenceKindSpeaker, claimPayload); err != nil {
		return Meeting{}, err
	}
	report = cloneReport(report)
	if evidence.TaskRef != "" && report.Utterance.SourceTaskRef == nil {
		report.Utterance.SourceTaskRef = clonePointer(&evidence.TaskRef)
	}
	if evidence.RunRef != "" && report.Utterance.SourceRunRef == nil {
		report.Utterance.SourceRunRef = clonePointer(&evidence.RunRef)
	}
	report.Utterance.Generation = evidence.Generation
	report.Utterance.Epoch = evidence.Epoch
	report.Utterance.DeliveryState = DeliveryUnsent
	report.SpeakerEvidenceID = evidence.EvidenceID
	report.SpeakerScopeEvidenceID = evidence.ScopeEvidenceID
	next.Reports = append(next.Reports, report)
	messageID := report.Utterance.MessageID
	next.CurrentTurn.MessageID = &messageID
	return meeting.commit(next)
}

func (meeting Meeting) RequestAnswer(guard Guard, concernID ConcernID, actorID string, additional bool) (Meeting, error) {
	if err := meeting.checkGuard(guard); err != nil {
		return Meeting{}, err
	}
	if meeting.Status != StatusAnswering || meeting.CurrentTurn != nil {
		return Meeting{}, ErrInvalidState
	}
	report, ok := meeting.activeReport()
	if !ok {
		return Meeting{}, ErrInvalidState
	}
	if !contains(meeting.Participants, actorID) || actorID == report.Utterance.ActorID {
		return Meeting{}, errors.New("answerer must be a different configured participant")
	}
	if _, ok := meeting.concern(concernID); !ok {
		return Meeting{}, errors.New("answer concern does not belong to the current report")
	}
	answered := meeting.deliveredAnswers(concernID)
	if additional {
		if len(answered) == 0 {
			return Meeting{}, errors.New("an additional answer requires an earlier delivered answer")
		}
	} else {
		first, exists := meeting.firstAnswerableConcern()
		if !exists || first != concernID {
			return Meeting{}, errors.New("answer turns must address the first concern with an unused responder")
		}
	}
	for _, answer := range answered {
		if answer.Utterance.ActorID == actorID {
			return Meeting{}, errors.New("one participant may answer a concern only once")
		}
	}
	if meeting.hasAnswerAttempt(concernID, actorID) {
		return Meeting{}, errors.New("one participant may have only one terminal generation attempt for a concern")
	}
	next := meeting.clone()
	id := concernID
	next.CurrentTurn = &Turn{Kind: TurnAnswer, ActorID: actorID, ConcernID: &id}
	return meeting.commit(next)
}

// RecordAnswerAttempt records only a canonical TaskManager terminal unknown
// receipt for the currently prepared answer request. It advances the turn to
// another responder without satisfying the concern.
func (meeting Meeting) RecordAnswerAttempt(guard Guard, attempt AnswerAttempt) (Meeting, error) {
	claimPayload := answerAttemptClaimPayload{Attempt: attempt}
	replayed, claimErr := meeting.hasEvidenceClaim(attempt.UnknownReceipt.EvidenceID, evidenceKindAnswerAttempt, claimPayload)
	if claimErr != nil {
		return Meeting{}, claimErr
	}
	if replayed {
		for _, existing := range meeting.AnswerAttempts {
			if existing.UnknownReceipt.EvidenceID == attempt.UnknownReceipt.EvidenceID && sameAnswerAttempt(existing, attempt) {
				return meeting.clone(), nil
			}
		}
		return Meeting{}, ErrConflict
	}
	if err := meeting.checkGuard(guard); err != nil {
		return Meeting{}, err
	}
	if err := meeting.validateAnswerAttempt(attempt); err != nil {
		return Meeting{}, err
	}
	if !meeting.currentTurnMatchesAnswerAttempt(attempt) {
		return Meeting{}, ErrInvalidState
	}
	next := meeting.clone()
	if err := next.addEvidenceClaim(attempt.UnknownReceipt.EvidenceID, evidenceKindAnswerAttempt, claimPayload); err != nil {
		return Meeting{}, err
	}
	next.AnswerAttempts = append(next.AnswerAttempts, cloneAnswerAttempt(attempt))
	next.CurrentTurn = nil
	return meeting.commit(next)
}

// RecordPartialMinutes binds the exact owner-issued private artifact reference
// after a terminal partial/missed transcript has been written. The transcript
// body remains exclusively owned by Workstream.
func (meeting Meeting) RecordPartialMinutes(guard Guard, evidence PartialMinutesEvidence) (Meeting, error) {
	replayed, err := meeting.hasEvidenceClaim(evidence.EvidenceID, evidenceKindPartialMinutes, evidence)
	if err != nil {
		return Meeting{}, err
	}
	if replayed {
		if meeting.PartialMinutes != nil && samePartialMinutes(*meeting.PartialMinutes, evidence) {
			return meeting.clone(), nil
		}
		return Meeting{}, ErrConflict
	}
	if err := meeting.checkGuard(guard); err != nil {
		return Meeting{}, err
	}
	if err := meeting.validatePartialMinutes(evidence); err != nil {
		return Meeting{}, err
	}
	if meeting.PartialMinutes != nil {
		return Meeting{}, ErrConflict
	}
	next := meeting.clone()
	if err := next.addEvidenceClaim(evidence.EvidenceID, evidenceKindPartialMinutes, evidence); err != nil {
		return Meeting{}, err
	}
	copy := clonePartialMinutes(&evidence)
	next.PartialMinutes = copy
	return meeting.commit(next)
}

func (meeting Meeting) validatePartialMinutes(evidence PartialMinutesEvidence) error {
	return meeting.validatePartialMinutesAtGuard(evidence, meeting.Guard())
}

func (meeting Meeting) validateStoredPartialMinutes(evidence PartialMinutesEvidence) error {
	expected := meeting.Guard()
	if expected.ProgressionRevision <= 1 {
		return errors.New("partial minutes receipt has no preceding terminal snapshot")
	}
	expected.ProgressionRevision--
	return meeting.validatePartialMinutesAtGuard(evidence, expected)
}

func (meeting Meeting) validatePartialMinutesAtGuard(evidence PartialMinutesEvidence, expectedGuard Guard) error {
	ref := evidence.Reference
	if evidence.EvidenceID.Validate() != nil || meeting.Status != StatusPartial && meeting.Status != StatusMissed ||
		meeting.PartialEvidence == nil || ref.Key != meeting.Key || ref.RequestID == "" || ref.RequestID != meeting.TranscriptRequestID ||
		!strings.HasPrefix(ref.RequestID, meeting.Key.String()+"|") || ref.ArtifactID.Validate() != nil || ref.WorkstreamID.Validate() != nil ||
		strings.TrimSpace(ref.OwnerUserID) != ref.OwnerUserID || ref.OwnerUserID == "" ||
		!contains(meeting.Participants, ref.CoordinatorActorID) || ref.RegistrationEvidenceID != meeting.RegistrationEvidenceID ||
		ref.ConfigRevision != meeting.ScheduleRevision || !canonicalMeetingSHA256(ref.ConfigSHA256) ||
		ref.Guard != expectedGuard ||
		!canonicalMeetingSHA256(ref.TranscriptSHA256) || ref.CreatedAt.IsZero() {
		return errors.New("partial minutes receipt does not match the terminal occurrence snapshot")
	}
	if audienceSHA, err := domaintask.TaskMeetingAudienceSHA256(meeting.Participants); err != nil || audienceSHA != ref.AudienceSHA256 || !sameActors(ref.Audience, meeting.Participants) {
		return errors.New("partial minutes receipt audience does not match the occurrence")
	}
	return nil
}

func samePartialMinutes(left, right PartialMinutesEvidence) bool {
	left.Reference = cloneMinutesArtifactReference(left.Reference)
	right.Reference = cloneMinutesArtifactReference(right.Reference)
	sort.Strings(left.Reference.Audience)
	sort.Strings(right.Reference.Audience)
	left.Reference.CreatedAt = left.Reference.CreatedAt.UTC().Round(0)
	right.Reference.CreatedAt = right.Reference.CreatedAt.UTC().Round(0)
	return reflect.DeepEqual(left, right)
}

func canonicalMeetingSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (meeting Meeting) validateAnswerAttempt(attempt AnswerAttempt) error {
	proof := attempt.UnknownReceipt
	if proof.Validate() != nil || proof.Purpose != domaintask.TaskMeetingPurposeAnswer ||
		attempt.ReporterID == "" || !contains(meeting.Participants, proof.ActorID) || proof.OccurrenceKey != meeting.Key.String() ||
		proof.ConfigRevision != meeting.ScheduleRevision || proof.RecordedAt.Before(meeting.ScheduledAt) || !proof.RecordedAt.Before(meeting.DeadlineAt) {
		return errors.New("answer attempt does not contain a valid terminal TaskManager receipt for this occurrence")
	}
	report, found := meeting.findReport(attempt.ConcernID.ReportMessageID)
	if !found || attempt.ConcernID.Ordinal == 0 || int(attempt.ConcernID.Ordinal) > len(report.Concerns) ||
		attempt.ReporterID != report.Utterance.ActorID || proof.ActorID == attempt.ReporterID {
		return errors.New("answer attempt must identify another configured participant and a real concern")
	}
	scope, scopeFound := meeting.scopeEvidenceByID(proof.GenerationScope.ScopeEvidenceID)
	if !scopeFound || scope.Generation != proof.GenerationScope.Generation || scope.Epoch != proof.GenerationScope.Epoch {
		return errors.New("answer attempt receipt lost its original World scope")
	}
	return nil
}

func (meeting Meeting) currentTurnMatchesAnswerAttempt(attempt AnswerAttempt) bool {
	proof := attempt.UnknownReceipt
	turn := meeting.CurrentTurn
	return meeting.Status == StatusAnswering && turn != nil && turn.Kind == TurnAnswer && turn.MessageID == nil &&
		turn.ConcernID != nil && *turn.ConcernID == attempt.ConcernID && turn.ActorID == proof.ActorID &&
		turn.GenerationRequestID != "" && modulecore.RequestID(turn.GenerationRequestID).Validate() == nil &&
		proof.GenerationRequestID == modulecore.RequestID(turn.GenerationRequestID) &&
		turn.Generation == proof.GenerationScope.Generation && turn.Epoch == proof.GenerationScope.Epoch &&
		turn.ScopeEvidenceID == proof.GenerationScope.ScopeEvidenceID
}

func (meeting Meeting) hasAnswerAttempt(concernID ConcernID, actorID string) bool {
	for _, attempt := range meeting.AnswerAttempts {
		if attempt.ConcernID == concernID && attempt.UnknownReceipt.ActorID == actorID {
			return true
		}
	}
	return false
}

func samePartialRequest(existing, requested PartialEvidence) bool {
	return existing.EvidenceID == requested.EvidenceID && existing.Key == requested.Key &&
		existing.Generation == requested.Generation && existing.Epoch == requested.Epoch &&
		existing.ReasonCode == strings.TrimSpace(requested.ReasonCode) && existing.ObservedAt.UTC().Round(0).Equal(requested.ObservedAt.UTC().Round(0))
}

func sameAnswerAttempt(left, right AnswerAttempt) bool {
	left.UnknownReceipt.RecordedAt = left.UnknownReceipt.RecordedAt.UTC().Round(0)
	right.UnknownReceipt.RecordedAt = right.UnknownReceipt.RecordedAt.UTC().Round(0)
	return reflect.DeepEqual(left, right)
}

func cloneAnswerAttempt(attempt AnswerAttempt) AnswerAttempt {
	attempt.UnknownReceipt.RecordedAt = attempt.UnknownReceipt.RecordedAt.UTC().Round(0)
	return attempt
}

func (meeting Meeting) RecordAnswer(guard Guard, answer Answer, evidence SpeakerEvidence) (Meeting, error) {
	claimPayload := speakerAnswerClaimPayload{Evidence: evidence, Answer: answer}
	replayedEvidence, claimErr := meeting.hasEvidenceClaim(evidence.EvidenceID, evidenceKindSpeaker, claimPayload)
	if claimErr != nil {
		return Meeting{}, claimErr
	}
	if existing, found := meeting.findAnswer(answer.Utterance.MessageID); found {
		if sameAnswerContent(existing, answer) && existing.SpeakerEvidenceID == evidence.EvidenceID && replayedEvidence {
			return meeting.clone(), nil
		}
		return Meeting{}, ErrConflict
	}
	if replayedEvidence {
		return Meeting{}, ErrConflict
	}
	if _, found := meeting.findReport(answer.Utterance.MessageID); found {
		return Meeting{}, ErrConflict
	}
	if err := meeting.checkGuard(guard); err != nil {
		return Meeting{}, err
	}
	if meeting.Status != StatusAnswering || meeting.CurrentTurn == nil || meeting.CurrentTurn.Kind != TurnAnswer ||
		meeting.CurrentTurn.MessageID != nil || meeting.CurrentTurn.ActorID != answer.Utterance.ActorID ||
		meeting.CurrentTurn.ConcernID == nil || *meeting.CurrentTurn.ConcernID != answer.ConcernID {
		return Meeting{}, ErrInvalidState
	}
	report, ok := meeting.activeReport()
	if !ok || answer.ReporterID != report.Utterance.ActorID || answer.Utterance.ActorID == answer.ReporterID {
		return Meeting{}, errors.New("answer must identify the concern owner and a different answerer")
	}
	if err := meeting.validateSpeakerEvidence(evidence, answer.Utterance, guard); err != nil {
		return Meeting{}, err
	}
	if !validAnswerKind(answer.Kind) {
		return Meeting{}, errors.New("answer kind must be advice, clarifying_question, or concrete_support")
	}
	if isAcknowledgementOnly(answer.Utterance.Body) {
		return Meeting{}, errors.New("acknowledgement alone is not a substantive answer")
	}
	if answer.Utterance.DeliveryState != "" && answer.Utterance.DeliveryState != DeliveryUnsent {
		return Meeting{}, errors.New("new answer delivery state must be empty or unsent")
	}
	if answer.Utterance.DeliveryEvidence != nil || answer.Utterance.UnknownEvidence != nil || answer.Utterance.UnknownReason != "" ||
		(answer.Utterance.Generation != 0 && answer.Utterance.Generation != evidence.Generation) ||
		(answer.Utterance.Epoch != 0 && answer.Utterance.Epoch != evidence.Epoch) {
		return Meeting{}, errors.New("new answer must not supply delivery proof or a stale generation/epoch")
	}
	if err := answer.ConcernID.ReportMessageID.Validate(); err != nil {
		return Meeting{}, fmt.Errorf("concern_id: %w", err)
	}
	if _, exists := meeting.concern(answer.ConcernID); !exists {
		return Meeting{}, errors.New("answer concern does not belong to the current report")
	}
	for _, prior := range meeting.Answers {
		if prior.ConcernID == answer.ConcernID && prior.Utterance.ActorID == answer.Utterance.ActorID {
			return Meeting{}, errors.New("one participant may answer a concern only once")
		}
	}
	if meeting.hasAnswerAttempt(answer.ConcernID, answer.Utterance.ActorID) {
		return Meeting{}, errors.New("participant already has a terminal generation attempt for this concern")
	}
	next := meeting.clone()
	if err := next.addEvidenceClaim(evidence.EvidenceID, evidenceKindSpeaker, claimPayload); err != nil {
		return Meeting{}, err
	}
	answer = cloneAnswer(answer)
	if evidence.TaskRef != "" && answer.Utterance.SourceTaskRef == nil {
		answer.Utterance.SourceTaskRef = clonePointer(&evidence.TaskRef)
	}
	if evidence.RunRef != "" && answer.Utterance.SourceRunRef == nil {
		answer.Utterance.SourceRunRef = clonePointer(&evidence.RunRef)
	}
	answer.Utterance.Generation = evidence.Generation
	answer.Utterance.Epoch = evidence.Epoch
	answer.Utterance.DeliveryState = DeliveryUnsent
	answer.SpeakerEvidenceID = evidence.EvidenceID
	answer.SpeakerScopeEvidenceID = evidence.ScopeEvidenceID
	next.Answers = append(next.Answers, answer)
	messageID := answer.Utterance.MessageID
	next.CurrentTurn.MessageID = &messageID
	return meeting.commit(next)
}

func (meeting Meeting) BeginCurrentDelivery(guard Guard, messageID modulecore.MessageID, now time.Time) (Meeting, error) {
	if err := meeting.checkGuard(guard); err != nil {
		return Meeting{}, err
	}
	if meeting.CurrentTurn == nil || meeting.CurrentTurn.MessageID == nil || *meeting.CurrentTurn.MessageID != messageID {
		return Meeting{}, ErrInvalidState
	}
	utterance, err := meeting.currentUtterance()
	if err != nil || utterance.DeliveryState != DeliveryUnsent {
		return Meeting{}, ErrInvalidState
	}
	if now.IsZero() || !now.Before(meeting.DeadlineAt) || !meeting.worldReadyAt(now) {
		return Meeting{}, ErrNotReady
	}
	next := meeting.clone()
	if err := next.setDeliveryState(messageID, DeliverySending); err != nil {
		return Meeting{}, err
	}
	if err := next.updateUtterance(messageID, func(target *Utterance) {
		target.DeliveryScopeEvidenceID = meeting.ScopeEvidence.EvidenceID
		target.DeliveryGeneration = meeting.Generation
		target.DeliveryEpoch = meeting.Epoch
	}); err != nil {
		return Meeting{}, err
	}
	return meeting.commit(next)
}

func (meeting Meeting) MarkCurrentDeliveryUnknown(guard Guard, evidence DeliveryUnknownEvidence) (Meeting, error) {
	replayed, err := meeting.hasEvidenceClaim(evidence.EvidenceID, evidenceKindDeliveryUnknown, evidence)
	if err != nil {
		return Meeting{}, err
	}
	if replayed {
		return meeting.clone(), nil
	}
	if err := meeting.checkGuard(guard); err != nil {
		return Meeting{}, err
	}
	if meeting.CurrentTurn == nil || meeting.CurrentTurn.MessageID == nil || *meeting.CurrentTurn.MessageID != evidence.MessageID {
		return Meeting{}, ErrInvalidState
	}
	if err := evidence.EvidenceID.Validate(); err != nil {
		return Meeting{}, fmt.Errorf("delivery uncertainty evidence id: %w", err)
	}
	if evidence.Key != meeting.Key || evidence.Generation != meeting.Generation || evidence.Epoch != meeting.Epoch ||
		evidence.ObservedAt.IsZero() || !evidence.ObservedAt.Before(meeting.DeadlineAt) || strings.TrimSpace(evidence.Reason) == "" {
		return Meeting{}, errors.New("delivery uncertainty evidence does not match current meeting state")
	}
	utterance, err := meeting.currentUtterance()
	if err != nil || utterance.DeliveryState != DeliverySending {
		return Meeting{}, ErrInvalidState
	}
	next := meeting.clone()
	if err := next.addEvidenceClaim(evidence.EvidenceID, evidenceKindDeliveryUnknown, evidence); err != nil {
		return Meeting{}, err
	}
	unknownEvidence := cloneDeliveryUnknown(&evidence)
	if err := next.updateUtterance(evidence.MessageID, func(target *Utterance) {
		target.DeliveryState = DeliveryUnknown
		target.UnknownEvidence = unknownEvidence
		target.UnknownReason = strings.TrimSpace(evidence.Reason)
	}); err != nil {
		return Meeting{}, err
	}
	return meeting.commit(next)
}

func (meeting Meeting) ConfirmDelivery(guard Guard, evidence DeliveryEvidence) (Meeting, error) {
	replayed, err := meeting.hasEvidenceClaim(evidence.EvidenceID, evidenceKindDelivery, evidence)
	if err != nil {
		return Meeting{}, err
	}
	if replayed {
		return meeting.clone(), nil
	}
	if err := meeting.checkGuard(guard); err != nil {
		return Meeting{}, err
	}
	if evidence.Key != meeting.Key || evidence.Generation == 0 || evidence.Epoch == 0 ||
		evidence.ObservedAt.IsZero() || !evidence.ObservedAt.Before(meeting.DeadlineAt) {
		return Meeting{}, errors.New("delivery evidence does not match current meeting state")
	}
	if err := evidence.EvidenceID.Validate(); err != nil {
		return Meeting{}, fmt.Errorf("delivery evidence id: %w", err)
	}
	if err := evidence.ThreadID.Validate(); err != nil {
		return Meeting{}, fmt.Errorf("delivery thread_id: %w", err)
	}
	utterance, ok := meeting.findUtterance(evidence.MessageID)
	if !ok {
		return Meeting{}, errors.New("delivery evidence references an unknown utterance")
	}
	if evidence.ThreadID != meeting.SharedThreadID || evidence.ScopeEvidenceID == "" ||
		evidence.ScopeEvidenceID != utterance.DeliveryScopeEvidenceID ||
		evidence.Generation != utterance.DeliveryGeneration || evidence.Epoch != utterance.DeliveryEpoch {
		return Meeting{}, ErrNotReady
	}
	scope, scopeFound := meeting.scopeEvidenceByID(evidence.ScopeEvidenceID)
	if !scopeFound || scope.Generation != evidence.Generation || scope.Epoch != evidence.Epoch {
		return Meeting{}, ErrNotReady
	}
	if !evidence.Delivered {
		if len(evidence.Audience) != 0 && !sameActors(evidence.Audience, meeting.Participants) {
			return Meeting{}, errors.New("undelivered evidence must have no audience or the exact meeting audience")
		}
	} else if !sameActors(evidence.Audience, meeting.Participants) {
		return Meeting{}, errors.New("delivered utterance must reach all configured participants")
	}
	if !meeting.worldReadyForScopeAt(evidence.ScopeEvidenceID, evidence.ObservedAt) {
		return Meeting{}, ErrNotReady
	}
	if utterance.DeliveryState == DeliveryDelivered || utterance.DeliveryState == DeliveryUndelivered {
		return Meeting{}, ErrConflict
	}
	if utterance.DeliveryState != DeliverySending && utterance.DeliveryState != DeliveryUnknown {
		return Meeting{}, ErrInvalidState
	}
	next := meeting.clone()
	if err := next.addEvidenceClaim(evidence.EvidenceID, evidenceKindDelivery, evidence); err != nil {
		return Meeting{}, err
	}
	state := DeliveryUndelivered
	if evidence.Delivered {
		state = DeliveryDelivered
	}
	if err := next.updateUtterance(evidence.MessageID, func(target *Utterance) {
		target.DeliveryState = state
		target.DeliveryEvidence = cloneDeliveryEvidence(&evidence)
	}); err != nil {
		return Meeting{}, err
	}
	if evidence.Delivered {
		if err := next.finishDeliveredTurn(evidence.MessageID); err != nil {
			return Meeting{}, err
		}
	}
	return meeting.commit(next)
}

func (meeting Meeting) Resume(guard Guard, evidence ResumeEvidence) (Meeting, error) {
	replayed, err := meeting.hasEvidenceClaim(evidence.EvidenceID, evidenceKindResume, evidence)
	if err != nil {
		return Meeting{}, err
	}
	if replayed {
		return meeting.clone(), nil
	}
	if err := meeting.checkGuard(guard); err != nil {
		return Meeting{}, err
	}
	if isTerminal(meeting.Status) || evidence.Key != meeting.Key || evidence.ObservedAt.IsZero() || !evidence.ObservedAt.Before(meeting.DeadlineAt) {
		return Meeting{}, ErrInvalidState
	}
	if err := evidence.EvidenceID.Validate(); err != nil {
		return Meeting{}, fmt.Errorf("resume evidence id: %w", err)
	}
	next := meeting.clone()
	if err := next.addEvidenceClaim(evidence.EvidenceID, evidenceKindResume, evidence); err != nil {
		return Meeting{}, err
	}
	if next.ScopeEvidence != nil && next.referencesWorldScope(next.ScopeEvidence.EvidenceID) {
		next.ScopeEvidenceHistory = append(next.ScopeEvidenceHistory, *cloneWorldScope(next.ScopeEvidence))
		for _, attendance := range next.Attendance {
			if attendance.Evidence.ScopeEvidenceID == next.ScopeEvidence.EvidenceID {
				next.AttendanceHistory = append(next.AttendanceHistory, attendance)
			}
		}
	}
	next.Generation++
	next.Epoch++
	if next.Generation == 0 || next.Epoch == 0 {
		return Meeting{}, errors.New("meeting generation or epoch overflow")
	}
	next.ScopeEvidence = nil
	next.Attendance = []Attendance{}
	for _, report := range next.Reports {
		if report.Utterance.DeliveryState == DeliverySending {
			if err := next.updateUtterance(report.Utterance.MessageID, func(target *Utterance) {
				target.DeliveryState = DeliveryUnknown
				target.UnknownEvidence = nil
				target.UnknownReason = "process_restarted_during_delivery"
			}); err != nil {
				return Meeting{}, err
			}
		}
	}
	for _, answer := range next.Answers {
		if answer.Utterance.DeliveryState == DeliverySending {
			if err := next.updateUtterance(answer.Utterance.MessageID, func(target *Utterance) {
				target.DeliveryState = DeliveryUnknown
				target.UnknownEvidence = nil
				target.UnknownReason = "process_restarted_during_delivery"
			}); err != nil {
				return Meeting{}, err
			}
		}
	}
	return meeting.commit(next)
}

func (meeting Meeting) BindNativeReferences(guard Guard, taskRef *modulecore.TaskID, runRef *modulecore.RunID) (Meeting, error) {
	if err := meeting.checkGuard(guard); err != nil {
		return Meeting{}, err
	}
	if isTerminal(meeting.Status) {
		return Meeting{}, ErrInvalidState
	}
	if taskRef != nil {
		if err := taskRef.Validate(); err != nil {
			return Meeting{}, fmt.Errorf("task_ref: %w", err)
		}
	}
	if runRef != nil {
		if err := runRef.Validate(); err != nil {
			return Meeting{}, fmt.Errorf("run_ref: %w", err)
		}
	}
	next := meeting.clone()
	next.TaskRef = clonePointer(taskRef)
	next.RunRef = clonePointer(runRef)
	return meeting.commit(next)
}

// BindNativeExecution records TaskManager-issued root references together with
// the owner admission proof. Scheduler log IDs are not accepted by this API.
func (meeting Meeting) BindNativeExecution(guard Guard, taskRef modulecore.TaskID, runRef modulecore.RunID, evidenceID modulecore.EvidenceID) (Meeting, error) {
	if err := meeting.checkGuard(guard); err != nil {
		return Meeting{}, err
	}
	if isTerminal(meeting.Status) || taskRef.Validate() != nil || runRef.Validate() != nil || evidenceID.Validate() != nil {
		return Meeting{}, ErrInvalidState
	}
	if meeting.TaskRef != nil || meeting.RunRef != nil || meeting.TaskManagerEvidenceID != "" {
		return Meeting{}, ErrConflict
	}
	next := meeting.clone()
	next.TaskRef, next.RunRef = clonePointer(&taskRef), clonePointer(&runRef)
	next.TaskManagerEvidenceID = evidenceID
	return meeting.commit(next)
}

func (meeting Meeting) AdvanceReporter(guard Guard, now time.Time) (Meeting, error) {
	if err := meeting.checkGuard(guard); err != nil {
		return Meeting{}, err
	}
	if meeting.Status != StatusAnswering || meeting.CurrentTurn != nil {
		return Meeting{}, ErrInvalidState
	}
	if now.IsZero() || !now.Before(meeting.DeadlineAt) {
		return Meeting{}, ErrNotReady
	}
	if _, ok := meeting.activeReport(); !ok || !meeting.allConcernsAnswered() {
		return Meeting{}, ErrNotReady
	}
	next := meeting.clone()
	next.CurrentReportIndex++
	if next.CurrentReportIndex == len(next.Participants) {
		next.Status = StatusFinishing
		next.CurrentTurn = nil
	} else {
		next.Status = StatusReporting
		next.CurrentTurn = &Turn{Kind: TurnReport, ActorID: next.Participants[next.CurrentReportIndex]}
	}
	return meeting.commit(next)
}

// PrepareCurrentGeneration durably freezes the native request identity and
// input digest before the participant executor can submit or recover a run.
func (meeting Meeting) PrepareCurrentGeneration(guard Guard, requestID string, inputJSON []byte) (Meeting, error) {
	if err := meeting.checkGuard(guard); err != nil {
		return Meeting{}, err
	}
	if (meeting.Status != StatusReporting && meeting.Status != StatusAnswering) || meeting.CurrentTurn == nil || meeting.CurrentTurn.MessageID != nil {
		return Meeting{}, ErrInvalidState
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || len(inputJSON) == 0 || !json.Valid(inputJSON) {
		return Meeting{}, errors.New("generation intent requires a stable request ID and frozen valid JSON input")
	}
	digest := sha256.Sum256(inputJSON)
	inputSHA256 := hex.EncodeToString(digest[:])
	if err := validateGenerationIntent(meeting.CurrentTurn); err != nil {
		return Meeting{}, err
	}
	if meeting.CurrentTurn.GenerationRequestID != "" {
		if meeting.CurrentTurn.GenerationRequestID != requestID || meeting.CurrentTurn.GenerationInputSHA256 != inputSHA256 || string(meeting.CurrentTurn.GenerationInputJSON) != string(inputJSON) ||
			meeting.CurrentTurn.Generation != meeting.Generation || meeting.CurrentTurn.Epoch != meeting.Epoch ||
			(meeting.ScopeEvidence != nil && meeting.CurrentTurn.ScopeEvidenceID != meeting.ScopeEvidence.EvidenceID) {
			return Meeting{}, ErrConflict
		}
		return meeting.clone(), nil
	}
	next := meeting.clone()
	next.CurrentTurn.GenerationRequestID = requestID
	next.CurrentTurn.GenerationInputSHA256 = inputSHA256
	next.CurrentTurn.GenerationInputJSON = append([]byte(nil), inputJSON...)
	next.CurrentTurn.Generation = meeting.Generation
	next.CurrentTurn.Epoch = meeting.Epoch
	if meeting.ScopeEvidence != nil {
		next.CurrentTurn.ScopeEvidenceID = meeting.ScopeEvidence.EvidenceID
	}
	return meeting.commit(next)
}

// PrepareSharedThread persists the canonical thread creation key before an
// external conversation owner may create or look up the group thread.
func (meeting Meeting) PrepareSharedThread(guard Guard, requestID string) (Meeting, error) {
	if err := meeting.checkGuard(guard); err != nil {
		return Meeting{}, err
	}
	if isTerminal(meeting.Status) || strings.TrimSpace(requestID) == "" || requestID != strings.TrimSpace(requestID) {
		return Meeting{}, ErrInvalidState
	}
	if meeting.ThreadRequestID != "" {
		if meeting.ThreadRequestID != requestID {
			return Meeting{}, ErrConflict
		}
		return meeting.clone(), nil
	}
	next := meeting.clone()
	next.ThreadRequestID = requestID
	return meeting.commit(next)
}

// PrepareGather persists the owner idempotency key before a potentially
// ambiguous World movement/gather command is issued.
func (meeting Meeting) PrepareGather(guard Guard, requestID string) (Meeting, error) {
	if err := meeting.checkGuard(guard); err != nil {
		return Meeting{}, err
	}
	if meeting.Status == StatusScheduled || isTerminal(meeting.Status) || strings.TrimSpace(requestID) == "" || requestID != strings.TrimSpace(requestID) {
		return Meeting{}, ErrInvalidState
	}
	if meeting.GatherRequestID != "" {
		if meeting.GatherRequestID != requestID {
			return Meeting{}, ErrConflict
		}
		return meeting.clone(), nil
	}
	next := meeting.clone()
	next.GatherRequestID = requestID
	return meeting.commit(next)
}

// PrepareWorldRefresh binds a fresh World read to the prior scope snapshot so
// a retry cannot accidentally replay the expired gather request.
func (meeting Meeting) PrepareWorldRefresh(guard Guard, requestID string, at time.Time) (Meeting, error) {
	if err := meeting.checkGuard(guard); err != nil {
		return Meeting{}, err
	}
	if meeting.Status == StatusScheduled || isTerminal(meeting.Status) || meeting.ScopeEvidence == nil ||
		at.IsZero() || !at.Before(meeting.DeadlineAt) || !meeting.NeedsWorldRefreshAt(at) ||
		requestID != meeting.Key.String()+"|gather|"+string(meeting.ScopeEvidence.EvidenceID) {
		return Meeting{}, ErrInvalidState
	}
	if requestID == meeting.GatherRequestID {
		return meeting.clone(), nil
	}
	next := meeting.clone()
	next.GatherRequestID = requestID
	return meeting.commit(next)
}

// PrepareTranscript persists the idempotency key before the conversation owner
// writes final minutes. Recovery must look up this key before creating again.
func (meeting Meeting) PrepareTranscript(guard Guard, requestID string) (Meeting, error) {
	if err := meeting.checkGuard(guard); err != nil {
		return Meeting{}, err
	}
	if meeting.Status != StatusFinishing && meeting.Status != StatusPartial && meeting.Status != StatusMissed ||
		strings.TrimSpace(requestID) == "" || requestID != strings.TrimSpace(requestID) ||
		(isTerminal(meeting.Status) && meeting.PartialEvidence == nil) {
		return Meeting{}, ErrInvalidState
	}
	if meeting.TranscriptRequestID != "" {
		if meeting.TranscriptRequestID != requestID {
			return Meeting{}, ErrConflict
		}
		return meeting.clone(), nil
	}
	next := meeting.clone()
	next.TranscriptRequestID = requestID
	return meeting.commit(next)
}

func (meeting Meeting) Finish(guard Guard, evidence FinalizationEvidence) (Meeting, error) {
	if meeting.Status == StatusCompleted {
		replayed, err := meeting.hasEvidenceClaim(evidence.EvidenceID, evidenceKindFinish, evidence)
		if err != nil {
			return Meeting{}, err
		}
		if replayed {
			return meeting.clone(), nil
		}
		return Meeting{}, ErrInvalidState
	}
	if isTerminal(meeting.Status) {
		return Meeting{}, ErrInvalidState
	}
	replayed, err := meeting.hasEvidenceClaim(evidence.EvidenceID, evidenceKindFinish, evidence)
	if err != nil {
		return Meeting{}, err
	}
	if replayed {
		return meeting.clone(), nil
	}
	if err := meeting.checkGuard(guard); err != nil {
		return Meeting{}, err
	}
	if meeting.Status != StatusFinishing || len(meeting.Reports) != 8 || !meeting.allConcernsAnswered() || !meeting.allUtterancesDelivered() {
		return Meeting{}, ErrNotReady
	}
	if err := evidence.EvidenceID.Validate(); err != nil {
		return Meeting{}, fmt.Errorf("finish evidence id: %w", err)
	}
	if err := evidence.TranscriptArtifactID.Validate(); err != nil {
		return Meeting{}, fmt.Errorf("transcript artifact id: %w", err)
	}
	if evidence.Key != meeting.Key || evidence.Generation != meeting.Generation || evidence.Epoch != meeting.Epoch ||
		evidence.ProgressionRevision != meeting.ProgressionRevision || evidence.PersistedAt.IsZero() ||
		!evidence.PersistedAt.Before(meeting.DeadlineAt) {
		return Meeting{}, errors.New("finalization evidence does not match the bounded meeting checkpoint")
	}
	next := meeting.clone()
	if err := next.addEvidenceClaim(evidence.EvidenceID, evidenceKindFinish, evidence); err != nil {
		return Meeting{}, err
	}
	next.Status = StatusCompleted
	next.FinishEvidence = cloneFinalization(&evidence)
	return meeting.commit(next)
}

func (meeting Meeting) MarkPartial(guard Guard, evidence PartialEvidence) (Meeting, error) {
	evidence.ReasonCode = strings.TrimSpace(evidence.ReasonCode)
	if isTerminal(meeting.Status) && meeting.PartialEvidence != nil && meeting.PartialEvidence.EvidenceID == evidence.EvidenceID {
		if samePartialRequest(*meeting.PartialEvidence, evidence) {
			return meeting.clone(), nil
		}
		return Meeting{}, ErrConflict
	}
	evidence.Unresolved = meeting.unresolvedItems()
	evidence.InterruptedTurn = cloneTurn(meeting.CurrentTurn)
	replayed, err := meeting.hasEvidenceClaim(evidence.EvidenceID, evidenceKindPartial, evidence)
	if err != nil {
		return Meeting{}, err
	}
	if replayed {
		return meeting.clone(), nil
	}
	if err := meeting.checkGuard(guard); err != nil {
		return Meeting{}, err
	}
	if isTerminal(meeting.Status) || evidence.Key != meeting.Key || evidence.Generation != meeting.Generation ||
		evidence.Epoch != meeting.Epoch || evidence.ObservedAt.IsZero() || evidence.ObservedAt.Before(meeting.DeadlineAt) {
		return Meeting{}, ErrInvalidState
	}
	if err := evidence.EvidenceID.Validate(); err != nil {
		return Meeting{}, fmt.Errorf("partial evidence id: %w", err)
	}
	if strings.TrimSpace(evidence.ReasonCode) == "" {
		return Meeting{}, errors.New("partial reason_code is required")
	}
	next := meeting.clone()
	if err := next.addEvidenceClaim(evidence.EvidenceID, evidenceKindPartial, evidence); err != nil {
		return Meeting{}, err
	}
	if meeting.Status == StatusScheduled && evidence.ReasonCode == "missed" {
		next.Status = StatusMissed
	} else {
		next.Status = StatusPartial
	}
	next.CurrentTurn = nil
	next.PartialEvidence = clonePartial(&evidence)
	return meeting.commit(next)
}

func (meeting Meeting) worldReadyAt(at time.Time) bool {
	if at.IsZero() || meeting.ScopeEvidence == nil || meeting.ScopeEvidence.Generation != meeting.Generation ||
		meeting.ScopeEvidence.Epoch != meeting.Epoch || at.Before(meeting.ScopeEvidence.ObservedAt) ||
		!at.Before(meeting.ScopeEvidence.ValidUntil) || meeting.Rooms.Living == "" || meeting.Rooms.Dining == "" {
		return false
	}
	for _, participant := range meeting.Participants {
		found := false
		for _, attendance := range meeting.Attendance {
			evidence := attendance.Evidence
			if evidence.ActorID == participant && evidence.Present && !at.Before(evidence.ObservedAt) && at.Before(evidence.ValidUntil) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// NeedsWorldRefreshAt reports whether the current scope and complete attendee
// roster remain valid for a new World-dependent action at at.
func (meeting Meeting) NeedsWorldRefreshAt(at time.Time) bool { return !meeting.worldReadyAt(at) }

// NeedsNewWorldObservationAt reports whether the current canonical World
// observation is expired or has already proven a participant absent. Missing
// attendance checkpoints alone do not invalidate a still-fresh observation:
// callers must replay its original request key to recover those receipts.
func (meeting Meeting) NeedsNewWorldObservationAt(at time.Time) bool {
	if at.IsZero() || meeting.ScopeEvidence == nil || meeting.ScopeEvidence.Generation != meeting.Generation ||
		meeting.ScopeEvidence.Epoch != meeting.Epoch || at.Before(meeting.ScopeEvidence.ObservedAt) ||
		!at.Before(meeting.ScopeEvidence.ValidUntil) || meeting.Rooms.Living == "" || meeting.Rooms.Dining == "" {
		return true
	}
	for _, attendance := range meeting.Attendance {
		evidence := attendance.Evidence
		if !evidence.Present || at.Before(evidence.ObservedAt) || !at.Before(evidence.ValidUntil) {
			return true
		}
	}
	return false
}

func (meeting Meeting) scopeEvidenceSnapshots() []WorldScopeEvidence {
	scopes := append([]WorldScopeEvidence(nil), meeting.ScopeEvidenceHistory...)
	if meeting.ScopeEvidence != nil {
		scopes = append(scopes, *meeting.ScopeEvidence)
	}
	return scopes
}

func (meeting Meeting) referencesWorldScope(id modulecore.EvidenceID) bool {
	if id == "" {
		return false
	}
	for _, report := range meeting.Reports {
		if report.SpeakerScopeEvidenceID == id || report.Utterance.DeliveryScopeEvidenceID == id {
			return true
		}
	}
	for _, answer := range meeting.Answers {
		if answer.SpeakerScopeEvidenceID == id || answer.Utterance.DeliveryScopeEvidenceID == id {
			return true
		}
	}
	return meeting.CurrentTurn != nil && meeting.CurrentTurn.GenerationRequestID != "" && meeting.CurrentTurn.ScopeEvidenceID == id
}

func (meeting Meeting) scopeEvidenceByID(id modulecore.EvidenceID) (WorldScopeEvidence, bool) {
	if id == "" {
		return WorldScopeEvidence{}, false
	}
	if meeting.ScopeEvidence != nil && meeting.ScopeEvidence.EvidenceID == id {
		return *meeting.ScopeEvidence, true
	}
	for _, scope := range meeting.ScopeEvidenceHistory {
		if scope.EvidenceID == id {
			return scope, true
		}
	}
	return WorldScopeEvidence{}, false
}

func (meeting Meeting) worldReadyForScopeAt(scopeID modulecore.EvidenceID, at time.Time) bool {
	scope, found := meeting.scopeEvidenceByID(scopeID)
	if !found || at.IsZero() || at.Before(scope.ObservedAt) || !at.Before(scope.ValidUntil) {
		return false
	}
	for _, actorID := range meeting.Participants {
		present := false
		for _, attendance := range append(append([]Attendance(nil), meeting.AttendanceHistory...), meeting.Attendance...) {
			evidence := attendance.Evidence
			if evidence.ScopeEvidenceID == scopeID && evidence.ActorID == actorID && evidence.Present &&
				!at.Before(evidence.ObservedAt) && at.Before(evidence.ValidUntil) {
				present = true
				break
			}
		}
		if !present {
			return false
		}
	}
	return true
}

func (meeting Meeting) validateSpeakerEvidence(evidence SpeakerEvidence, utterance Utterance, guard Guard) error {
	if err := evidence.EvidenceID.Validate(); err != nil {
		return fmt.Errorf("speaker evidence id: %w", err)
	}
	if err := utterance.MessageID.Validate(); err != nil {
		return fmt.Errorf("message_id: %w", err)
	}
	if err := evidence.ThreadID.Validate(); err != nil {
		return fmt.Errorf("speaker thread_id: %w", err)
	}
	if err := validateActorID(utterance.ActorID); err != nil {
		return err
	}
	expectedGeneration, expectedEpoch := guard.Generation, guard.Epoch
	expectedScopeID := modulecore.EvidenceID("")
	if meeting.CurrentTurn != nil && meeting.CurrentTurn.GenerationRequestID != "" && meeting.CurrentTurn.Generation != 0 {
		expectedGeneration, expectedEpoch = meeting.CurrentTurn.Generation, meeting.CurrentTurn.Epoch
		expectedScopeID = meeting.CurrentTurn.ScopeEvidenceID
	}
	if strings.TrimSpace(utterance.Body) == "" || utterance.Body != evidence.Body || evidence.Key != meeting.Key ||
		evidence.Generation != expectedGeneration || evidence.Epoch != expectedEpoch || evidence.MessageID != utterance.MessageID ||
		evidence.ActorID != utterance.ActorID || evidence.ObservedAt.IsZero() || evidence.ObservedAt.Before(meeting.ScheduledAt) ||
		!evidence.ObservedAt.Before(meeting.DeadlineAt) {
		return errors.New("speaker evidence does not verify the current utterance body and turn")
	}
	if !contains(meeting.Participants, utterance.ActorID) || evidence.ThreadID != meeting.SharedThreadID ||
		(expectedScopeID != "" && evidence.ScopeEvidenceID != expectedScopeID) ||
		!meeting.worldReadyForScopeAt(evidence.ScopeEvidenceID, evidence.ObservedAt) {
		return ErrNotReady
	}
	if utterance.SourceTaskRef != nil {
		if err := utterance.SourceTaskRef.Validate(); err != nil {
			return fmt.Errorf("source_task_ref: %w", err)
		}
	}
	if evidence.GenerationRequestID != "" || evidence.GenerationInputSHA256 != "" || evidence.TaskRef != "" || evidence.RunRef != "" {
		if meeting.CurrentTurn == nil || evidence.GenerationRequestID != meeting.CurrentTurn.GenerationRequestID || evidence.GenerationInputSHA256 != meeting.CurrentTurn.GenerationInputSHA256 ||
			evidence.GenerationRequestID == "" || evidence.TaskRef.Validate() != nil || evidence.RunRef.Validate() != nil {
			return errors.New("speaker evidence does not match the durable native generation intent")
		}
		if utterance.SourceTaskRef != nil && *utterance.SourceTaskRef != evidence.TaskRef {
			return errors.New("utterance task reference differs from native execution evidence")
		}
		if utterance.SourceRunRef != nil && *utterance.SourceRunRef != evidence.RunRef {
			return errors.New("utterance run reference differs from native execution evidence")
		}
		if evidence.TaskAssignee != evidence.ActorID || evidence.AdmissionEvidenceID.Validate() != nil {
			return errors.New("participant TaskManager evidence does not prove the current Task assignee and admission")
		}
	}
	return nil
}

func (meeting Meeting) findReport(messageID modulecore.MessageID) (Report, bool) {
	for _, report := range meeting.Reports {
		if report.Utterance.MessageID == messageID {
			return report, true
		}
	}
	return Report{}, false
}

func (meeting Meeting) findAnswer(messageID modulecore.MessageID) (Answer, bool) {
	for _, answer := range meeting.Answers {
		if answer.Utterance.MessageID == messageID {
			return answer, true
		}
	}
	return Answer{}, false
}

func (meeting Meeting) findUtterance(messageID modulecore.MessageID) (Utterance, bool) {
	if report, ok := meeting.findReport(messageID); ok {
		return report.Utterance, true
	}
	if answer, ok := meeting.findAnswer(messageID); ok {
		return answer.Utterance, true
	}
	return Utterance{}, false
}

func (meeting Meeting) currentUtterance() (Utterance, error) {
	if meeting.CurrentTurn == nil || meeting.CurrentTurn.MessageID == nil {
		return Utterance{}, ErrInvalidState
	}
	utterance, ok := meeting.findUtterance(*meeting.CurrentTurn.MessageID)
	if !ok {
		return Utterance{}, ErrInvalidState
	}
	return utterance, nil
}

func (meeting Meeting) activeReport() (Report, bool) {
	if meeting.CurrentReportIndex < 0 || meeting.CurrentReportIndex >= len(meeting.Reports) {
		return Report{}, false
	}
	return meeting.Reports[meeting.CurrentReportIndex], true
}

func (meeting Meeting) concern(id ConcernID) (Concern, bool) {
	report, ok := meeting.activeReport()
	if !ok || report.Utterance.MessageID != id.ReportMessageID || id.Ordinal == 0 || int(id.Ordinal) > len(report.Concerns) {
		return Concern{}, false
	}
	return report.Concerns[id.Ordinal-1], true
}

func (meeting Meeting) deliveredAnswers(concernID ConcernID) []Answer {
	answers := make([]Answer, 0)
	for _, answer := range meeting.Answers {
		if answer.ConcernID == concernID && answer.Utterance.DeliveryState == DeliveryDelivered {
			answers = append(answers, answer)
		}
	}
	return answers
}

func (meeting Meeting) firstUnansweredConcern() (ConcernID, bool) {
	report, ok := meeting.activeReport()
	if !ok {
		return ConcernID{}, false
	}
	for index := range report.Concerns {
		id := ConcernID{ReportMessageID: report.Utterance.MessageID, Ordinal: uint32(index + 1)}
		if len(meeting.deliveredAnswers(id)) == 0 {
			return id, true
		}
	}
	return ConcernID{}, false
}

func (meeting Meeting) firstAnswerableConcern() (ConcernID, bool) {
	report, ok := meeting.activeReport()
	if !ok {
		return ConcernID{}, false
	}
	for index := range report.Concerns {
		id := ConcernID{ReportMessageID: report.Utterance.MessageID, Ordinal: uint32(index + 1)}
		if len(meeting.deliveredAnswers(id)) != 0 {
			continue
		}
		for _, actorID := range meeting.Participants {
			if actorID != report.Utterance.ActorID && !meeting.answerActorUsed(id, actorID) {
				return id, true
			}
		}
	}
	return ConcernID{}, false
}

func (meeting Meeting) answerActorUsed(concernID ConcernID, actorID string) bool {
	for _, answer := range meeting.Answers {
		if answer.ConcernID == concernID && answer.Utterance.ActorID == actorID {
			return true
		}
	}
	return meeting.hasAnswerAttempt(concernID, actorID)
}

func (meeting Meeting) allConcernsAnswered() bool {
	for _, report := range meeting.Reports {
		for index := range report.Concerns {
			id := ConcernID{ReportMessageID: report.Utterance.MessageID, Ordinal: uint32(index + 1)}
			if len(meeting.deliveredAnswers(id)) == 0 {
				return false
			}
		}
	}
	return true
}

// AllConcernsAnswered reports the actual substantive delivered-answer gate.
// Terminal failed/unknown attempts are intentionally not counted as answers.
func (meeting Meeting) AllConcernsAnswered() bool {
	return meeting.allConcernsAnswered()
}

func (meeting Meeting) allUtterancesDelivered() bool {
	for _, report := range meeting.Reports {
		if report.Utterance.DeliveryState != DeliveryDelivered {
			return false
		}
	}
	for _, answer := range meeting.Answers {
		if answer.Utterance.DeliveryState != DeliveryDelivered {
			return false
		}
	}
	return true
}

func (meeting Meeting) unresolvedItems() []string {
	items := []string{}
	if meeting.ScopeEvidence == nil {
		items = append(items, "shared_world_scope")
	}
	for _, actorID := range meeting.Participants {
		if !meeting.hasPresence(actorID) {
			items = append(items, "attendance:"+actorID)
		}
	}
	for _, actorID := range meeting.Participants {
		report, ok := meeting.reportByActor(actorID)
		if !ok {
			items = append(items, "report:"+actorID)
			continue
		}
		if report.Utterance.DeliveryState != DeliveryDelivered {
			items = append(items, "report_delivery:"+actorID)
		}
		for index := range report.Concerns {
			id := ConcernID{ReportMessageID: report.Utterance.MessageID, Ordinal: uint32(index + 1)}
			if len(meeting.deliveredAnswers(id)) == 0 {
				items = append(items, "answer:"+id.String())
			}
		}
	}
	for _, answer := range meeting.Answers {
		if answer.Utterance.DeliveryState != DeliveryDelivered {
			items = append(items, "answer_delivery:"+answer.ConcernID.String())
		}
	}
	return items
}

func (meeting Meeting) reportByActor(actorID string) (Report, bool) {
	for _, report := range meeting.Reports {
		if report.Utterance.ActorID == actorID {
			return report, true
		}
	}
	return Report{}, false
}

func (meeting Meeting) hasPresence(actorID string) bool {
	for _, attendance := range meeting.Attendance {
		if attendance.Evidence.ActorID == actorID && attendance.Evidence.Present {
			return true
		}
	}
	return false
}

func (meeting *Meeting) updateUtterance(messageID modulecore.MessageID, update func(*Utterance)) error {
	for index := range meeting.Reports {
		if meeting.Reports[index].Utterance.MessageID == messageID {
			update(&meeting.Reports[index].Utterance)
			return nil
		}
	}
	for index := range meeting.Answers {
		if meeting.Answers[index].Utterance.MessageID == messageID {
			update(&meeting.Answers[index].Utterance)
			return nil
		}
	}
	return errors.New("utterance not found")
}

func (meeting *Meeting) setDeliveryState(messageID modulecore.MessageID, state string) error {
	return meeting.updateUtterance(messageID, func(target *Utterance) { target.DeliveryState = state })
}

func (meeting *Meeting) finishDeliveredTurn(messageID modulecore.MessageID) error {
	if meeting.CurrentTurn == nil || meeting.CurrentTurn.MessageID == nil || *meeting.CurrentTurn.MessageID != messageID {
		return nil
	}
	switch meeting.CurrentTurn.Kind {
	case TurnReport:
		report, ok := meeting.findReport(messageID)
		if !ok {
			return ErrInvalidState
		}
		if report.ConcernDeclaration == ConcernsNone {
			meeting.advanceToNextReporter()
		} else {
			meeting.Status = StatusAnswering
			meeting.CurrentTurn = nil
		}
	case TurnAnswer:
		meeting.CurrentTurn = nil
	default:
		return ErrInvalidState
	}
	return nil
}

func (meeting *Meeting) advanceToNextReporter() {
	meeting.CurrentReportIndex++
	if meeting.CurrentReportIndex >= len(meeting.Participants) {
		meeting.Status = StatusFinishing
		meeting.CurrentTurn = nil
		return
	}
	meeting.Status = StatusReporting
	meeting.CurrentTurn = &Turn{Kind: TurnReport, ActorID: meeting.Participants[meeting.CurrentReportIndex]}
}

func validateReport(report Report) error {
	if err := ValidateReportFields(report.Current, report.Next); err != nil {
		return err
	}
	switch report.ConcernDeclaration {
	case ConcernsNone:
		if len(report.Concerns) != 0 {
			return errors.New("explicit no-concerns report must not include concerns")
		}
	case ConcernsListed:
		if len(report.Concerns) == 0 {
			return errors.New("listed concerns declaration requires at least one concern")
		}
		for index, concern := range report.Concerns {
			if strings.TrimSpace(concern.Description) == "" {
				return fmt.Errorf("concerns[%d].description is required", index)
			}
		}
	default:
		return errors.New("concerns must be explicitly declared none or listed")
	}
	return nil
}

// ValidateReportFields is shared by native TaskMeeting output parsing and
// persistence so both boundaries enforce the same current/next vocabulary.
func ValidateReportFields(current, next ReportField) error {
	if current.State != "in_progress" && current.State != "completed" && current.State != "blocked" && current.State != ReportFieldNone && current.State != ReportFieldUndecided {
		return errors.New("current report must state in_progress, completed, blocked, none, or undecided")
	}
	if next.State != "planned" && next.State != ReportFieldNone && next.State != ReportFieldUndecided {
		return errors.New("next report must state planned, none, or undecided")
	}
	for _, field := range []struct {
		name string
		data ReportField
	}{{"current", current}, {"next", next}} {
		if field.data.State == ReportFieldNone {
			if field.data.Text != "" || field.data.TaskRef != nil {
				return fmt.Errorf("%s none declaration must not include text or task reference", field.name)
			}
			continue
		}
		if field.data.State == ReportFieldUndecided {
			if field.name == "current" && field.data.TaskRef != nil {
				return errors.New("undecided current work cannot include a task reference")
			}
			if field.data.TaskRef != nil {
				if err := field.data.TaskRef.Validate(); err != nil {
					return fmt.Errorf("%s task_ref: %w", field.name, err)
				}
				if strings.TrimSpace(field.data.Text) == "" {
					return fmt.Errorf("%s report text is required when an undecided task reference is supplied", field.name)
				}
			}
			continue
		}
		if strings.TrimSpace(field.data.Text) == "" {
			return fmt.Errorf("%s report text is required", field.name)
		}
		if field.data.TaskRef != nil {
			if err := field.data.TaskRef.Validate(); err != nil {
				return fmt.Errorf("%s task_ref: %w", field.name, err)
			}
		}
		if field.name == "current" && field.data.State != "in_progress" && field.data.TaskRef == nil {
			return errors.New("completed or blocked current work requires its native task reference")
		}
	}
	return nil
}

func (meeting Meeting) clone() Meeting {
	copy := meeting
	copy.EvidenceClaims = append([]EvidenceClaim(nil), meeting.EvidenceClaims...)
	copy.Timing = cloneTiming(meeting.Timing)
	copy.Participants = append([]string(nil), meeting.Participants...)
	copy.WorldActorBindings = append([]WorldActorBinding(nil), meeting.WorldActorBindings...)
	copy.TaskRef = clonePointer(meeting.TaskRef)
	copy.RunRef = clonePointer(meeting.RunRef)
	copy.ScopeEvidence = cloneWorldScope(meeting.ScopeEvidence)
	copy.ScopeEvidenceHistory = make([]WorldScopeEvidence, len(meeting.ScopeEvidenceHistory))
	for index := range meeting.ScopeEvidenceHistory {
		copy.ScopeEvidenceHistory[index] = *cloneWorldScope(&meeting.ScopeEvidenceHistory[index])
	}
	copy.Attendance = cloneMeetingSlice(meeting.Attendance)
	copy.AttendanceHistory = append([]Attendance(nil), meeting.AttendanceHistory...)
	copy.Reports = make([]Report, len(meeting.Reports))
	for index, report := range meeting.Reports {
		copy.Reports[index] = cloneReport(report)
	}
	copy.Answers = make([]Answer, len(meeting.Answers))
	for index, answer := range meeting.Answers {
		copy.Answers[index] = cloneAnswer(answer)
	}
	copy.AnswerAttempts = make([]AnswerAttempt, len(meeting.AnswerAttempts))
	for index, attempt := range meeting.AnswerAttempts {
		copy.AnswerAttempts[index] = cloneAnswerAttempt(attempt)
	}
	copy.CurrentTurn = cloneTurn(meeting.CurrentTurn)
	copy.FinishEvidence = cloneFinalization(meeting.FinishEvidence)
	copy.PartialEvidence = clonePartial(meeting.PartialEvidence)
	copy.PartialMinutes = clonePartialMinutes(meeting.PartialMinutes)
	return copy
}

func cloneTiming(timing modulecore.TaskTiming) modulecore.TaskTiming {
	copy := timing
	if timing.ScheduledStartAt != nil {
		value := *timing.ScheduledStartAt
		copy.ScheduledStartAt = &value
	}
	if timing.NotBefore != nil {
		value := *timing.NotBefore
		copy.NotBefore = &value
	}
	copy.AllowedWindows = cloneMeetingSlice(timing.AllowedWindows)
	if timing.DeadlineAt != nil {
		value := *timing.DeadlineAt
		copy.DeadlineAt = &value
	}
	if timing.Recurrence != nil {
		recurrence := *timing.Recurrence
		recurrence.Times = cloneMeetingSlice(timing.Recurrence.Times)
		recurrence.Weekdays = cloneMeetingSlice(timing.Recurrence.Weekdays)
		if timing.Recurrence.IntervalSeconds != nil {
			value := *timing.Recurrence.IntervalSeconds
			recurrence.IntervalSeconds = &value
		}
		copy.Recurrence = &recurrence
	}
	if timing.EstimatedDurationSeconds != nil {
		value := *timing.EstimatedDurationSeconds
		copy.EstimatedDurationSeconds = &value
	}
	return copy
}

func cloneMeetingSlice[T any](values []T) []T {
	if values == nil {
		return nil
	}
	cloned := make([]T, len(values))
	copy(cloned, values)
	return cloned
}

func cloneReport(report Report) Report {
	copy := report
	copy.Utterance = cloneUtterance(report.Utterance)
	copy.Current = cloneReportField(report.Current)
	copy.Next = cloneReportField(report.Next)
	copy.Concerns = append([]Concern(nil), report.Concerns...)
	return copy
}

func cloneReportField(field ReportField) ReportField {
	field.TaskRef = clonePointer(field.TaskRef)
	return field
}

func cloneAnswer(answer Answer) Answer {
	answer.Utterance = cloneUtterance(answer.Utterance)
	return answer
}

func cloneUtterance(utterance Utterance) Utterance {
	utterance.SourceTaskRef = clonePointer(utterance.SourceTaskRef)
	utterance.SourceRunRef = clonePointer(utterance.SourceRunRef)
	utterance.DeliveryEvidence = cloneDeliveryEvidence(utterance.DeliveryEvidence)
	utterance.UnknownEvidence = cloneDeliveryUnknown(utterance.UnknownEvidence)
	return utterance
}

func cloneDeliveryEvidence(evidence *DeliveryEvidence) *DeliveryEvidence {
	if evidence == nil {
		return nil
	}
	copy := *evidence
	copy.Audience = append([]string(nil), evidence.Audience...)
	return &copy
}

func cloneDeliveryUnknown(evidence *DeliveryUnknownEvidence) *DeliveryUnknownEvidence {
	if evidence == nil {
		return nil
	}
	copy := *evidence
	return &copy
}

func cloneWorldScope(evidence *WorldScopeEvidence) *WorldScopeEvidence {
	if evidence == nil {
		return nil
	}
	copy := *evidence
	copy.Audience = append([]string(nil), evidence.Audience...)
	return &copy
}

func cloneTurn(turn *Turn) *Turn {
	if turn == nil {
		return nil
	}
	copy := *turn
	copy.GenerationInputJSON = append([]byte(nil), turn.GenerationInputJSON...)
	copy.ConcernID = clonePointer(turn.ConcernID)
	copy.MessageID = clonePointer(turn.MessageID)
	return &copy
}

func cloneFinalization(evidence *FinalizationEvidence) *FinalizationEvidence {
	if evidence == nil {
		return nil
	}
	copy := *evidence
	return &copy
}

func clonePartial(evidence *PartialEvidence) *PartialEvidence {
	if evidence == nil {
		return nil
	}
	copy := *evidence
	copy.Unresolved = append([]string(nil), evidence.Unresolved...)
	copy.InterruptedTurn = cloneTurn(evidence.InterruptedTurn)
	return &copy
}

func cloneMinutesArtifactReference(reference MinutesArtifactReference) MinutesArtifactReference {
	reference.Audience = append([]string(nil), reference.Audience...)
	reference.CreatedAt = reference.CreatedAt.UTC().Round(0)
	return reference
}

func clonePartialMinutes(evidence *PartialMinutesEvidence) *PartialMinutesEvidence {
	if evidence == nil {
		return nil
	}
	copy := *evidence
	copy.Reference = cloneMinutesArtifactReference(evidence.Reference)
	return &copy
}

func attendanceFromEvidence(evidence PresenceEvidence) Attendance {
	return Attendance{Evidence: evidence}
}

func sameReportContent(existing, requested Report) bool {
	left, right := cloneReport(existing), cloneReport(requested)
	left.Utterance.DeliveryState, right.Utterance.DeliveryState = "", ""
	left.Utterance.Generation, right.Utterance.Generation = 0, 0
	left.Utterance.Epoch, right.Utterance.Epoch = 0, 0
	left.Utterance.DeliveryEvidence, right.Utterance.DeliveryEvidence = nil, nil
	left.Utterance.DeliveryScopeEvidenceID, right.Utterance.DeliveryScopeEvidenceID = "", ""
	left.Utterance.DeliveryGeneration, right.Utterance.DeliveryGeneration = 0, 0
	left.Utterance.DeliveryEpoch, right.Utterance.DeliveryEpoch = 0, 0
	left.Utterance.UnknownEvidence, right.Utterance.UnknownEvidence = nil, nil
	left.Utterance.UnknownReason, right.Utterance.UnknownReason = "", ""
	left.SpeakerEvidenceID, right.SpeakerEvidenceID = "", ""
	left.SpeakerScopeEvidenceID, right.SpeakerScopeEvidenceID = "", ""
	return reflect.DeepEqual(left, right)
}

func sameAnswerContent(existing, requested Answer) bool {
	left, right := cloneAnswer(existing), cloneAnswer(requested)
	left.Utterance.DeliveryState, right.Utterance.DeliveryState = "", ""
	left.Utterance.Generation, right.Utterance.Generation = 0, 0
	left.Utterance.Epoch, right.Utterance.Epoch = 0, 0
	left.Utterance.DeliveryEvidence, right.Utterance.DeliveryEvidence = nil, nil
	left.Utterance.DeliveryScopeEvidenceID, right.Utterance.DeliveryScopeEvidenceID = "", ""
	left.Utterance.DeliveryGeneration, right.Utterance.DeliveryGeneration = 0, 0
	left.Utterance.DeliveryEpoch, right.Utterance.DeliveryEpoch = 0, 0
	left.Utterance.UnknownEvidence, right.Utterance.UnknownEvidence = nil, nil
	left.Utterance.UnknownReason, right.Utterance.UnknownReason = "", ""
	left.SpeakerEvidenceID, right.SpeakerEvidenceID = "", ""
	left.SpeakerScopeEvidenceID, right.SpeakerScopeEvidenceID = "", ""
	return reflect.DeepEqual(left, right)
}

func validateActorID(actorID string) error {
	if actorID == "" || strings.TrimSpace(actorID) != actorID || strings.ToLower(actorID) != actorID {
		return fmt.Errorf("actor_id %q must be non-empty canonical lowercase", actorID)
	}
	for _, char := range actorID {
		if char <= ' ' {
			return fmt.Errorf("actor_id %q must not contain whitespace", actorID)
		}
	}
	return nil
}

func validAnswerKind(kind string) bool {
	return kind == AnswerAdvice || kind == AnswerClarifyingQuestion || kind == AnswerConcreteSupport
}

func isAcknowledgementOnly(body string) bool {
	value := strings.TrimSpace(strings.ToLower(body))
	value = strings.TrimRight(value, ".!?。！？、, ")
	switch value {
	case "ok", "okay", "thanks", "thank you", "yes", "no", "了解", "了解です", "はい", "いいえ", "なるほど", "わかりました", "わかりましたです", "大丈夫", "お疲れ様です", "おつかれさまです":
		return true
	default:
		return false
	}
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func worldActorForCore(bindings []WorldActorBinding, coreActorID string) string {
	for _, binding := range bindings {
		if binding.CoreActorID == coreActorID {
			return binding.WorldActorID
		}
	}
	return ""
}

func sameActors(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	leftSet := make(map[string]struct{}, len(left))
	for _, actorID := range left {
		if _, exists := leftSet[actorID]; exists {
			return false
		}
		leftSet[actorID] = struct{}{}
	}
	for _, actorID := range right {
		if _, exists := leftSet[actorID]; !exists {
			return false
		}
	}
	return true
}

func isTerminal(status string) bool {
	return status == StatusCompleted || status == StatusPartial || status == StatusMissed
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (meeting Meeting) hasEvidenceClaim(id modulecore.EvidenceID, kind string, payload any) (bool, error) {
	if err := id.Validate(); err != nil {
		return false, fmt.Errorf("evidence id: %w", err)
	}
	requested, err := newEvidenceClaim(id, kind, payload)
	if err != nil && payload != nil {
		return false, err
	}
	for _, existing := range meeting.EvidenceClaims {
		if existing.EvidenceID != id {
			continue
		}
		if existing.Kind != kind || payload != nil && existing.SHA256 != requested.SHA256 {
			return false, ErrConflict
		}
		return true, nil
	}
	return false, nil
}

func (meeting *Meeting) addEvidenceClaim(id modulecore.EvidenceID, kind string, payload any) error {
	exists, err := meeting.hasEvidenceClaim(id, kind, payload)
	if err != nil {
		return err
	}
	if exists {
		return ErrConflict
	}
	claim, err := newEvidenceClaim(id, kind, payload)
	if err != nil {
		return err
	}
	meeting.EvidenceClaims = append(meeting.EvidenceClaims, claim)
	return nil
}

func newEvidenceClaim(id modulecore.EvidenceID, kind string, payload any) (EvidenceClaim, error) {
	if err := id.Validate(); err != nil {
		return EvidenceClaim{}, fmt.Errorf("evidence id: %w", err)
	}
	if !validEvidenceKind(kind) {
		return EvidenceClaim{}, fmt.Errorf("invalid evidence claim kind %q", kind)
	}
	normalized, err := normalizeEvidencePayload(payload)
	if err != nil {
		return EvidenceClaim{}, err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return EvidenceClaim{}, fmt.Errorf("marshal evidence claim: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return EvidenceClaim{EvidenceID: id, Kind: kind, SHA256: hex.EncodeToString(digest[:])}, nil
}

func normalizeEvidencePayload(payload any) (any, error) {
	utc := func(value time.Time) time.Time { return value.UTC().Round(0) }
	switch value := payload.(type) {
	case registrationSnapshotPayload:
		value.RegisteredActors = append([]string(nil), value.RegisteredActors...)
		sort.Strings(value.RegisteredActors)
		return value, nil
	case WorldScopeEvidence:
		value.Audience = append([]string(nil), value.Audience...)
		sort.Strings(value.Audience)
		value.ObservedAt, value.ValidUntil = utc(value.ObservedAt), utc(value.ValidUntil)
		return value, nil
	case PresenceEvidence:
		value.ObservedAt, value.ValidUntil = utc(value.ObservedAt), utc(value.ValidUntil)
		return value, nil
	case speakerReportClaimPayload:
		value.Evidence.ObservedAt = utc(value.Evidence.ObservedAt)
		value.Report = normalizeReportForClaim(value.Report)
		return value, nil
	case speakerAnswerClaimPayload:
		value.Evidence.ObservedAt = utc(value.Evidence.ObservedAt)
		value.Answer = normalizeAnswerForClaim(value.Answer)
		return value, nil
	case DeliveryEvidence:
		value.Audience = append([]string(nil), value.Audience...)
		sort.Strings(value.Audience)
		value.ObservedAt = utc(value.ObservedAt)
		return value, nil
	case DeliveryUnknownEvidence:
		value.Reason = strings.TrimSpace(value.Reason)
		value.ObservedAt = utc(value.ObservedAt)
		return value, nil
	case ResumeEvidence:
		value.ObservedAt = utc(value.ObservedAt)
		return value, nil
	case FinalizationEvidence:
		value.PersistedAt = utc(value.PersistedAt)
		return value, nil
	case PartialEvidence:
		value.ReasonCode = strings.TrimSpace(value.ReasonCode)
		value.ObservedAt = utc(value.ObservedAt)
		value.Unresolved = append([]string(nil), value.Unresolved...)
		sort.Strings(value.Unresolved)
		value.InterruptedTurn = cloneTurn(value.InterruptedTurn)
		return value, nil
	case PartialMinutesEvidence:
		value.Reference = cloneMinutesArtifactReference(value.Reference)
		value.Reference.CreatedAt = utc(value.Reference.CreatedAt)
		sort.Strings(value.Reference.Audience)
		return value, nil
	case answerAttemptClaimPayload:
		value.Attempt = cloneAnswerAttempt(value.Attempt)
		value.Attempt.UnknownReceipt.RecordedAt = utc(value.Attempt.UnknownReceipt.RecordedAt)
		return value, nil
	default:
		return nil, fmt.Errorf("unsupported evidence claim payload %T", payload)
	}
}

func normalizeReportForClaim(report Report) Report {
	report = cloneReport(report)
	report.SpeakerEvidenceID = ""
	report.Utterance.Generation, report.Utterance.Epoch = 0, 0
	report.Utterance.DeliveryState = ""
	report.Utterance.DeliveryEvidence = nil
	report.Utterance.UnknownEvidence = nil
	report.Utterance.UnknownReason = ""
	return report
}

func normalizeAnswerForClaim(answer Answer) Answer {
	answer = cloneAnswer(answer)
	answer.SpeakerEvidenceID = ""
	answer.Utterance.Generation, answer.Utterance.Epoch = 0, 0
	answer.Utterance.DeliveryState = ""
	answer.Utterance.DeliveryEvidence = nil
	answer.Utterance.UnknownEvidence = nil
	answer.Utterance.UnknownReason = ""
	return answer
}

func validEvidenceKind(kind string) bool {
	switch kind {
	case evidenceKindRegistration, evidenceKindWorldScope, evidenceKindPresence, evidenceKindSpeaker,
		evidenceKindDelivery, evidenceKindDeliveryUnknown, evidenceKindResume, evidenceKindFinish, evidenceKindPartial, evidenceKindAnswerAttempt, evidenceKindPartialMinutes:
		return true
	default:
		return false
	}
}
