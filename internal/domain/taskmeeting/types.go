package taskmeeting

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	TimezoneJST = "Asia/Tokyo"

	StatusScheduled = "scheduled"
	StatusGathering = "gathering"
	StatusReporting = "reporting"
	StatusAnswering = "answering"
	StatusFinishing = "finishing"
	StatusCompleted = "completed"
	StatusPartial   = "partial"
	StatusMissed    = "missed"

	TurnReport = "report"
	TurnAnswer = "answer"

	DeliveryUnsent      = "unsent"
	DeliverySending     = "sending"
	DeliveryUnknown     = "unknown"
	DeliveryDelivered   = "delivered"
	DeliveryUndelivered = "undelivered"

	ReportFieldStated    = "stated"
	ReportFieldNone      = "none"
	ReportFieldUndecided = "undecided"

	ConcernsNotDisclosed = ""
	ConcernsNone         = "none"
	ConcernsListed       = "listed"

	AnswerAdvice             = "advice"
	AnswerClarifyingQuestion = "clarifying_question"
	AnswerConcreteSupport    = "concrete_support"
)

const expectedMeetingSlots = "08:00,12:00,17:00"

// ValidateDailyMeetingTiming keeps the meeting cadence in one domain-owned
// contract for configuration, scheduler registration, and stored occurrences.
func ValidateDailyMeetingTiming(timing modulecore.TaskTiming) error {
	if err := timing.Validate(); err != nil {
		return err
	}
	if timing.Timezone != TimezoneJST || timing.Recurrence == nil || timing.Recurrence.Frequency != modulecore.TaskRecurrenceDaily {
		return errors.New("meeting timing must use the daily Asia/Tokyo recurrence")
	}
	times := append([]string(nil), timing.Recurrence.Times...)
	sort.Strings(times)
	if strings.Join(times, ",") != expectedMeetingSlots || len(timing.Recurrence.Weekdays) != 0 || timing.Recurrence.IntervalSeconds != nil {
		return fmt.Errorf("meeting recurrence must contain exactly %s", expectedMeetingSlots)
	}
	return nil
}

var (
	ErrConflict        = errors.New("meeting occurrence conflict")
	ErrStale           = errors.New("meeting command is stale")
	ErrInvalidState    = errors.New("meeting state does not allow this transition")
	ErrNotReady        = errors.New("meeting readiness is not established")
	ErrUnknownDelivery = errors.New("delivery outcome is unknown; reconcile by readback before proceeding")
)

// OccurrenceKey is the stable native-owner identity for one scheduled slot.
// Schedule revision is deliberately excluded so editing a schedule cannot
// create a second occurrence for the same local date and slot.
type OccurrenceKey struct {
	ScheduleID modulecore.ScheduleID `json:"schedule_id"`
	LocalDate  string                `json:"local_date"`
	Slot       string                `json:"slot"`
	Timezone   string                `json:"timezone"`
}

func (key OccurrenceKey) String() string {
	return string(key.ScheduleID) + "|" + key.LocalDate + "|" + key.Slot + "|" + key.Timezone
}

func (key OccurrenceKey) Validate() error {
	if err := key.ScheduleID.Validate(); err != nil {
		return fmt.Errorf("schedule_id: %w", err)
	}
	if _, err := time.Parse("2006-01-02", key.LocalDate); err != nil {
		return fmt.Errorf("local_date must be YYYY-MM-DD: %w", err)
	}
	if _, err := time.Parse("15:04", key.Slot); err != nil {
		return fmt.Errorf("slot must be HH:MM: %w", err)
	}
	if key.Timezone != TimezoneJST {
		return fmt.Errorf("timezone must be %q", TimezoneJST)
	}
	return nil
}

// RoomRefs are optional until the owner has configured both existing rooms.
// This domain accepts opaque canonical references supplied by that owner; it
// does not infer or create room identifiers.
type RoomRefs struct {
	Living string `json:"living,omitempty"`
	Dining string `json:"dining,omitempty"`
}

func (rooms RoomRefs) Validate() error {
	if rooms.Living == "" && rooms.Dining == "" {
		return nil
	}
	if strings.TrimSpace(rooms.Living) != rooms.Living || rooms.Living == "" {
		return errors.New("living room reference must be a non-empty canonical reference")
	}
	if strings.TrimSpace(rooms.Dining) != rooms.Dining || rooms.Dining == "" {
		return errors.New("dining room reference must be a non-empty canonical reference")
	}
	if rooms.Living == rooms.Dining {
		return errors.New("living and dining room references must be distinct")
	}
	return nil
}

// ConfigSnapshot carries the exact configured roster and trusted registration
// observation used for a single occurrence. Extra registered agents are
// allowed; this package never selects participants from them.
type ConfigSnapshot struct {
	ScheduleID               modulecore.ScheduleID `json:"schedule_id"`
	Revision                 uint64                `json:"revision"`
	Timing                   modulecore.TaskTiming `json:"timing"`
	WorldBaseURL             string                `json:"world_base_url,omitempty"`
	WorldCredentialEnv       string                `json:"world_credential_env,omitempty"`
	WorldID                  string                `json:"world_id,omitempty"`
	TimelineID               string                `json:"timeline_id,omitempty"`
	TimelineEpoch            string                `json:"timeline_epoch,omitempty"`
	ConfiguredActors         []string              `json:"configured_actors"`
	WorldActorBindings       []WorldActorBinding   `json:"world_actor_bindings"`
	RegisteredActors         []string              `json:"registered_actors"`
	RegistrationEvidenceID   modulecore.EvidenceID `json:"registration_evidence_id"`
	Rooms                    RoomRefs              `json:"rooms"`
	MeetingDeadlineAfterSecs int64                 `json:"meeting_deadline_after_seconds"`
}

// WorldActorBinding is an explicit owner-configured mapping between a CORE
// actor identity and a distinct WORLD entity identity.
type WorldActorBinding struct {
	CoreActorID  string `json:"core_actor_id"`
	WorldActorID string `json:"world_actor_id"`
}

type Guard struct {
	Generation          uint64 `json:"generation"`
	Epoch               uint64 `json:"epoch"`
	ProgressionRevision uint64 `json:"progression_revision"`
}

// StoreAccess is a snapshot returned by the application's trusted authority
// port. Persistence still compares it with the authenticated execution scope
// and with the stored envelope on every read or write.
type StoreAccess struct {
	CoordinatorActorID string                `json:"coordinator_actor_id"`
	OwnerUserID        string                `json:"owner_user_id"`
	Audience           []string              `json:"audience"`
	AuthorityEvidence  modulecore.EvidenceID `json:"authority_evidence"`
}

type OccurrenceRecord struct {
	Meeting     Meeting
	PayloadHash string
	Created     bool
}

type Meeting struct {
	Key                      OccurrenceKey         `json:"occurrence_key"`
	ScheduleRevision         uint64                `json:"schedule_revision"`
	Timing                   modulecore.TaskTiming `json:"timing"`
	WorldBaseURL             string                `json:"world_base_url,omitempty"`
	WorldCredentialEnv       string                `json:"world_credential_env,omitempty"`
	WorldID                  string                `json:"world_id,omitempty"`
	TimelineID               string                `json:"timeline_id,omitempty"`
	TimelineEpoch            string                `json:"timeline_epoch,omitempty"`
	Participants             []string              `json:"participants"`
	WorldActorBindings       []WorldActorBinding   `json:"world_actor_bindings"`
	RegistrationEvidenceID   modulecore.EvidenceID `json:"registration_evidence_id"`
	Rooms                    RoomRefs              `json:"rooms"`
	MeetingDeadlineAfterSecs int64                 `json:"meeting_deadline_after_seconds"`
	ScheduledAt              time.Time             `json:"scheduled_at"`
	DeadlineAt               time.Time             `json:"deadline_at"`
	Status                   string                `json:"status"`
	Generation               uint64                `json:"generation"`
	Epoch                    uint64                `json:"epoch"`
	ProgressionRevision      uint64                `json:"progression_revision"`
	CheckpointSequence       uint64                `json:"checkpoint_sequence"`
	EvidenceClaims           []EvidenceClaim       `json:"evidence_claims"`
	TaskRef                  *modulecore.TaskID    `json:"task_ref,omitempty"`
	RunRef                   *modulecore.RunID     `json:"run_ref,omitempty"`
	TaskManagerEvidenceID    modulecore.EvidenceID `json:"task_manager_evidence_id,omitempty"`
	ScopeEvidence            *WorldScopeEvidence   `json:"scope_evidence,omitempty"`
	ScopeEvidenceHistory     []WorldScopeEvidence  `json:"scope_evidence_history,omitempty"`
	SharedThreadID           modulecore.ThreadID   `json:"shared_thread_id,omitempty"`
	ThreadRequestID          string                `json:"thread_request_id,omitempty"`
	GatherRequestID          string                `json:"gather_request_id,omitempty"`
	TranscriptRequestID      string                `json:"transcript_request_id,omitempty"`
	Attendance               []Attendance          `json:"attendance"`
	AttendanceHistory        []Attendance          `json:"attendance_history,omitempty"`
	CurrentReportIndex       int                   `json:"current_report_index"`
	Reports                  []Report              `json:"reports"`
	Answers                  []Answer              `json:"answers"`
	AnswerAttempts           []AnswerAttempt       `json:"answer_attempts,omitempty"`
	CurrentTurn              *Turn                 `json:"current_turn,omitempty"`
	FinishEvidence           *FinalizationEvidence `json:"finish_evidence,omitempty"`
	PartialEvidence          *PartialEvidence      `json:"partial_evidence,omitempty"`
	PartialMinutes           *PartialMinutesEvidence `json:"partial_minutes,omitempty"`
}

type WorldScopeEvidence struct {
	EvidenceID    modulecore.EvidenceID `json:"evidence_id"`
	Key           OccurrenceKey         `json:"occurrence_key"`
	Generation    uint64                `json:"generation"`
	Epoch         uint64                `json:"epoch"`
	Snapshot      WorldSnapshotEvidence `json:"snapshot"`
	ThreadID      modulecore.ThreadID   `json:"thread_id"`
	LivingRoomRef string                `json:"living_room_ref"`
	DiningRoomRef string                `json:"dining_room_ref"`
	Audience      []string              `json:"audience"`
	ObservedAt    time.Time             `json:"observed_at"`
	ValidUntil    time.Time             `json:"valid_until"`
}

type PresenceEvidence struct {
	EvidenceID      modulecore.EvidenceID `json:"evidence_id"`
	Key             OccurrenceKey         `json:"occurrence_key"`
	Generation      uint64                `json:"generation"`
	Epoch           uint64                `json:"epoch"`
	ScopeEvidenceID modulecore.EvidenceID `json:"scope_evidence_id,omitempty"`
	Snapshot        WorldSnapshotEvidence `json:"snapshot"`
	ActorID         string                `json:"actor_id"`
	WorldActorID    string                `json:"world_actor_id"`
	Present         bool                  `json:"present"`
	RoomRef         string                `json:"room_ref,omitempty"`
	ObservedAt      time.Time             `json:"observed_at"`
	ValidUntil      time.Time             `json:"valid_until"`
}

// WorldSnapshotEvidence preserves the exact source snapshot used by both the
// meeting scope and each actor's presence proof. EvidenceID on the enclosing
// evidence value remains the owner-issued proof reference.
type WorldSnapshotEvidence struct {
	WorldID       string                `json:"world_id"`
	TimelineID    string                `json:"timeline_id"`
	TimelineEpoch string                `json:"timeline_epoch"`
	StateSeq      uint64                `json:"state_seq"`
	StateHash     string                `json:"state_hash"`
	OwnerProofID  modulecore.EvidenceID `json:"owner_proof_id"`
}

type Attendance struct {
	Evidence PresenceEvidence `json:"evidence"`
}

type ReportField struct {
	State   string             `json:"state"`
	Text    string             `json:"text,omitempty"`
	TaskRef *modulecore.TaskID `json:"task_ref,omitempty"`
}

type Concern struct {
	Description string `json:"description"`
}

// ConcernID is scoped to the report utterance and its one-based position.
// This avoids creating a parallel UUID identity for a nested report field.
type ConcernID struct {
	ReportMessageID modulecore.MessageID `json:"report_message_id"`
	Ordinal         uint32               `json:"ordinal"`
}

func (id ConcernID) String() string {
	return fmt.Sprintf("%s:%d", id.ReportMessageID, id.Ordinal)
}

type Utterance struct {
	MessageID               modulecore.MessageID     `json:"message_id"`
	ActorID                 string                   `json:"actor_id"`
	Body                    string                   `json:"body"`
	SourceTaskRef           *modulecore.TaskID       `json:"source_task_ref,omitempty"`
	SourceRunRef            *modulecore.RunID        `json:"source_run_ref,omitempty"`
	Generation              uint64                   `json:"generation"`
	Epoch                   uint64                   `json:"epoch"`
	DeliveryState           string                   `json:"delivery_state"`
	DeliveryScopeEvidenceID modulecore.EvidenceID    `json:"delivery_scope_evidence_id,omitempty"`
	DeliveryGeneration      uint64                   `json:"delivery_generation,omitempty"`
	DeliveryEpoch           uint64                   `json:"delivery_epoch,omitempty"`
	DeliveryEvidence        *DeliveryEvidence        `json:"delivery_evidence,omitempty"`
	UnknownEvidence         *DeliveryUnknownEvidence `json:"unknown_evidence,omitempty"`
	UnknownReason           string                   `json:"unknown_reason,omitempty"`
}

// EvidenceClaim is an aggregate-local collision fingerprint. The referenced
// evidence source remains owned by the external World or storage owner.
type EvidenceClaim struct {
	EvidenceID modulecore.EvidenceID `json:"evidence_id"`
	Kind       string                `json:"kind"`
	SHA256     string                `json:"sha256"`
}

type Report struct {
	Utterance              Utterance             `json:"utterance"`
	Current                ReportField           `json:"current"`
	Next                   ReportField           `json:"next"`
	ConcernDeclaration     string                `json:"concern_declaration"`
	Concerns               []Concern             `json:"concerns"`
	SpeakerEvidenceID      modulecore.EvidenceID `json:"speaker_evidence_id"`
	SpeakerScopeEvidenceID modulecore.EvidenceID `json:"speaker_scope_evidence_id,omitempty"`
}

type Answer struct {
	Utterance              Utterance             `json:"utterance"`
	ConcernID              ConcernID             `json:"concern_id"`
	ReporterID             string                `json:"reporter_id"`
	Kind                   string                `json:"kind"`
	SpeakerEvidenceID      modulecore.EvidenceID `json:"speaker_evidence_id"`
	SpeakerScopeEvidenceID modulecore.EvidenceID `json:"speaker_scope_evidence_id,omitempty"`
}

// AnswerAttempt records a terminal native generation result that could not
// produce a known answer. The owner-issued unknown receipt is retained intact;
// attempts never satisfy a concern or imply successful delivery.
type AnswerAttempt struct {
	ConcernID      ConcernID                                        `json:"concern_id"`
	ReporterID     string                                           `json:"reporter_id"`
	UnknownReceipt domaintask.TaskMeetingGenerationUnknownReference `json:"unknown_receipt"`
}

type Turn struct {
	Kind                  string                `json:"kind"`
	ActorID               string                `json:"actor_id"`
	ConcernID             *ConcernID            `json:"concern_id,omitempty"`
	MessageID             *modulecore.MessageID `json:"message_id,omitempty"`
	GenerationRequestID   string                `json:"generation_request_id,omitempty"`
	GenerationInputSHA256 string                `json:"generation_input_sha256,omitempty"`
	GenerationInputJSON   json.RawMessage       `json:"generation_input_json,omitempty"`
	Generation            uint64                `json:"generation,omitempty"`
	Epoch                 uint64                `json:"epoch,omitempty"`
	ScopeEvidenceID       modulecore.EvidenceID `json:"scope_evidence_id,omitempty"`
}

type SpeakerEvidence struct {
	EvidenceID            modulecore.EvidenceID `json:"evidence_id"`
	Key                   OccurrenceKey         `json:"occurrence_key"`
	Generation            uint64                `json:"generation"`
	Epoch                 uint64                `json:"epoch"`
	ScopeEvidenceID       modulecore.EvidenceID `json:"scope_evidence_id,omitempty"`
	ThreadID              modulecore.ThreadID   `json:"thread_id"`
	MessageID             modulecore.MessageID  `json:"message_id"`
	ActorID               string                `json:"actor_id"`
	Body                  string                `json:"body"`
	ObservedAt            time.Time             `json:"observed_at"`
	GenerationRequestID   string                `json:"generation_request_id,omitempty"`
	GenerationInputSHA256 string                `json:"generation_input_sha256,omitempty"`
	TaskRef               modulecore.TaskID     `json:"task_ref,omitempty"`
	RunRef                modulecore.RunID      `json:"run_ref,omitempty"`
	TaskAssignee          string                `json:"task_assignee,omitempty"`
	AdmissionEvidenceID   modulecore.EvidenceID `json:"admission_evidence_id,omitempty"`
}

type DeliveryUnknownEvidence struct {
	EvidenceID modulecore.EvidenceID `json:"evidence_id"`
	Key        OccurrenceKey         `json:"occurrence_key"`
	Generation uint64                `json:"generation"`
	Epoch      uint64                `json:"epoch"`
	MessageID  modulecore.MessageID  `json:"message_id"`
	Reason     string                `json:"reason"`
	ObservedAt time.Time             `json:"observed_at"`
}

type DeliveryEvidence struct {
	EvidenceID      modulecore.EvidenceID `json:"evidence_id"`
	Key             OccurrenceKey         `json:"occurrence_key"`
	Generation      uint64                `json:"generation"`
	Epoch           uint64                `json:"epoch"`
	ScopeEvidenceID modulecore.EvidenceID `json:"scope_evidence_id,omitempty"`
	ThreadID        modulecore.ThreadID   `json:"thread_id"`
	MessageID       modulecore.MessageID  `json:"message_id"`
	Delivered       bool                  `json:"delivered"`
	Audience        []string              `json:"audience"`
	ObservedAt      time.Time             `json:"observed_at"`
}

type FinalizationEvidence struct {
	EvidenceID           modulecore.EvidenceID `json:"evidence_id"`
	Key                  OccurrenceKey         `json:"occurrence_key"`
	Generation           uint64                `json:"generation"`
	Epoch                uint64                `json:"epoch"`
	ProgressionRevision  uint64                `json:"progression_revision"`
	TranscriptArtifactID modulecore.ArtifactID `json:"transcript_artifact_id"`
	PersistedAt          time.Time             `json:"persisted_at"`
}

type PartialEvidence struct {
	EvidenceID      modulecore.EvidenceID `json:"evidence_id"`
	Key             OccurrenceKey         `json:"occurrence_key"`
	Generation      uint64                `json:"generation"`
	Epoch           uint64                `json:"epoch"`
	ObservedAt      time.Time             `json:"observed_at"`
	ReasonCode      string                `json:"reason_code"`
	Unresolved      []string              `json:"unresolved"`
	InterruptedTurn *Turn                 `json:"interrupted_turn,omitempty"`
}

// MinutesArtifactReference is the exact private Workstream owner receipt used
// to recover persisted meeting minutes. It contains no transcript body.
type MinutesArtifactReference struct {
	RequestID              string                `json:"request_id"`
	ArtifactID             modulecore.ArtifactID `json:"artifact_id"`
	WorkstreamID           modulecore.WorkstreamID `json:"workstream_id"`
	OwnerUserID            string                `json:"owner_user_id"`
	CoordinatorActorID     string                `json:"coordinator_actor_id"`
	Key                    OccurrenceKey         `json:"occurrence_key"`
	RegistrationEvidenceID modulecore.EvidenceID `json:"registration_evidence_id"`
	ConfigRevision         uint64                `json:"config_revision"`
	ConfigSHA256           string                `json:"config_sha256"`
	Audience               []string              `json:"audience"`
	AudienceSHA256         string                `json:"audience_sha256"`
	Guard                  Guard                 `json:"guard"`
	TranscriptSHA256       string                `json:"transcript_sha256"`
	CreatedAt              time.Time             `json:"created_at"`
}

type PartialMinutesEvidence struct {
	EvidenceID modulecore.EvidenceID      `json:"evidence_id"`
	Reference  MinutesArtifactReference  `json:"reference"`
}

type ResumeEvidence struct {
	EvidenceID modulecore.EvidenceID `json:"evidence_id"`
	Key        OccurrenceKey         `json:"occurrence_key"`
	ObservedAt time.Time             `json:"observed_at"`
}
