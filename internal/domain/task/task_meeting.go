package task

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var (
	ErrTaskMeetingOwnerRequired    = errors.New("TaskMeeting work requires its registered native owner")
	ErrTaskMeetingConflict         = errors.New("TaskMeeting invocation or receipt conflicts with the frozen occurrence")
	ErrTaskMeetingNotFound         = errors.New("TaskMeeting owner receipt was not found")
	ErrTaskMeetingNotReady         = errors.New("TaskMeeting generation timing is not ready")
	ErrTaskMeetingExecutionUnknown = errors.New("TaskMeeting generation outcome is unknown and requires owner reconciliation")
)

type TaskMeetingPurpose string

type TaskMeetingGenerationScope struct {
	ScopeEvidenceID modulecore.EvidenceID `json:"scope_evidence_id"`
	Generation      uint64                `json:"generation"`
	Epoch           uint64                `json:"epoch"`
}

// TaskMeetingGenerationResultReference identifies the immutable typed output
// receipt committed by the Task owner before a generation Run can succeed.
// Delivery/session/event identities belong to TaskMeetingMessageReference and
// are intentionally absent here.
type TaskMeetingGenerationResultReference struct {
	TaskID              modulecore.TaskID          `json:"task_id"`
	RunID               modulecore.RunID           `json:"run_id"`
	ReceiptID           string                     `json:"receipt_id"`
	GenerationRequestID modulecore.RequestID       `json:"generation_request_id"`
	Purpose             TaskMeetingPurpose         `json:"purpose"`
	ActorID             string                     `json:"actor_id"`
	OccurrenceKey       string                     `json:"occurrence_key"`
	ConfigRevision      uint64                     `json:"config_revision"`
	ConfigSHA256        string                     `json:"config_sha256"`
	InputSHA256         string                     `json:"input_sha256"`
	SourceContextSHA256 string                     `json:"source_context_sha256"`
	GenerationScope     TaskMeetingGenerationScope `json:"generation_scope"`
	ResultSHA256        string                     `json:"result_sha256"`
	ReceiptEvidenceID   modulecore.EvidenceID      `json:"receipt_evidence_id"`
	RecordedAt          time.Time                  `json:"recorded_at"`
}

// TaskMeetingGenerationUnknownReference is an immutable owner receipt proving
// that a registered generation effect returned without a durable output. It
// prevents the same request from being sent again and carries no error text or
// partial generated content.
type TaskMeetingGenerationUnknownReference struct {
	TaskID              modulecore.TaskID          `json:"task_id"`
	RunID               modulecore.RunID           `json:"run_id"`
	ReceiptID           string                     `json:"receipt_id"`
	GenerationRequestID modulecore.RequestID       `json:"generation_request_id"`
	Purpose             TaskMeetingPurpose         `json:"purpose"`
	ActorID             string                     `json:"actor_id"`
	OccurrenceKey       string                     `json:"occurrence_key"`
	ConfigRevision      uint64                     `json:"config_revision"`
	ConfigSHA256        string                     `json:"config_sha256"`
	InputSHA256         string                     `json:"input_sha256"`
	SourceContextSHA256 string                     `json:"source_context_sha256"`
	GenerationScope     TaskMeetingGenerationScope `json:"generation_scope"`
	Reason              string                     `json:"reason"`
	EvidenceID          modulecore.EvidenceID      `json:"evidence_id"`
	RecordedAt          time.Time                  `json:"recorded_at"`
}

// TaskMeetingGenerationStartedReference is the immutable pre-effect intent.
// Its existence alone forbids replaying the external effect; a canonical output
// or terminal unknown receipt must be found before the request is complete.
type TaskMeetingGenerationStartedReference struct {
	TaskID              modulecore.TaskID          `json:"task_id"`
	RunID               modulecore.RunID           `json:"run_id"`
	ReceiptID           string                     `json:"receipt_id"`
	GenerationRequestID modulecore.RequestID       `json:"generation_request_id"`
	Purpose             TaskMeetingPurpose         `json:"purpose"`
	ActorID             string                     `json:"actor_id"`
	OccurrenceKey       string                     `json:"occurrence_key"`
	ConfigRevision      uint64                     `json:"config_revision"`
	ConfigSHA256        string                     `json:"config_sha256"`
	InputSHA256         string                     `json:"input_sha256"`
	SourceContextSHA256 string                     `json:"source_context_sha256"`
	GenerationScope     TaskMeetingGenerationScope `json:"generation_scope"`
	EvidenceID          modulecore.EvidenceID      `json:"evidence_id"`
	StartedAt           time.Time                  `json:"started_at"`
}

func (ref TaskMeetingGenerationStartedReference) Validate() error {
	if ref.TaskID.Validate() != nil || ref.RunID.Validate() != nil || ref.GenerationRequestID.Validate() != nil ||
		ref.ReceiptID != TaskMeetingGenerationEffectStartedReceiptID(ref.TaskID, ref.RunID) ||
		(ref.Purpose != TaskMeetingPurposeReport && ref.Purpose != TaskMeetingPurposeAnswer) || !canonicalTaskMeetingActor(ref.ActorID) ||
		ref.OccurrenceKey == "" || len(ref.OccurrenceKey) > 128 || strings.TrimSpace(ref.OccurrenceKey) != ref.OccurrenceKey ||
		ref.ConfigRevision == 0 || !canonicalTaskMeetingSHA256(ref.ConfigSHA256) || !canonicalTaskMeetingSHA256(ref.InputSHA256) ||
		!canonicalTaskMeetingSHA256(ref.SourceContextSHA256) || ref.GenerationScope.Validate() != nil ||
		ref.EvidenceID.Validate() != nil || ref.StartedAt.IsZero() {
		return ErrTaskMeetingConflict
	}
	return nil
}

func (ref TaskMeetingGenerationStartedReference) ValidateRun(task Task, run Run) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	inv := task.TaskMeetingInvocation
	if inv == nil || task.TaskID != ref.TaskID || run.TaskID != ref.TaskID || run.RunID != ref.RunID || run.Assignee != ref.ActorID ||
		inv.GenerationRequestID != ref.GenerationRequestID || inv.Purpose != ref.Purpose || inv.ActorID != ref.ActorID || inv.OccurrenceKey != ref.OccurrenceKey ||
		inv.ConfigRevision != ref.ConfigRevision || inv.ConfigSHA256 != ref.ConfigSHA256 || inv.InputSHA256 != ref.InputSHA256 ||
		inv.SourceContextSHA256 != ref.SourceContextSHA256 || inv.GenerationScope != ref.GenerationScope {
		return ErrTaskMeetingConflict
	}
	return nil
}

const TaskMeetingGenerationUnknownReasonEffectReturned = "effect_returned_without_output"

func (ref TaskMeetingGenerationUnknownReference) Validate() error {
	if ref.TaskID.Validate() != nil || ref.RunID.Validate() != nil || ref.GenerationRequestID.Validate() != nil ||
		ref.ReceiptID != TaskMeetingGenerationUnknownReceiptID(ref.TaskID, ref.RunID) ||
		(ref.Purpose != TaskMeetingPurposeReport && ref.Purpose != TaskMeetingPurposeAnswer) || !canonicalTaskMeetingActor(ref.ActorID) ||
		ref.OccurrenceKey == "" || len(ref.OccurrenceKey) > 128 || strings.TrimSpace(ref.OccurrenceKey) != ref.OccurrenceKey ||
		ref.ConfigRevision == 0 || !canonicalTaskMeetingSHA256(ref.ConfigSHA256) || !canonicalTaskMeetingSHA256(ref.InputSHA256) ||
		!canonicalTaskMeetingSHA256(ref.SourceContextSHA256) || ref.GenerationScope.Validate() != nil ||
		ref.Reason != TaskMeetingGenerationUnknownReasonEffectReturned || ref.EvidenceID.Validate() != nil || ref.RecordedAt.IsZero() {
		return ErrTaskMeetingConflict
	}
	return nil
}

func (ref TaskMeetingGenerationUnknownReference) ValidateRun(task Task, run Run) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	inv := task.TaskMeetingInvocation
	if inv == nil || task.TaskID != ref.TaskID || run.TaskID != ref.TaskID || run.RunID != ref.RunID || run.Assignee != ref.ActorID ||
		inv.GenerationRequestID != ref.GenerationRequestID || inv.Purpose != ref.Purpose || inv.ActorID != ref.ActorID || inv.OccurrenceKey != ref.OccurrenceKey ||
		inv.ConfigRevision != ref.ConfigRevision || inv.ConfigSHA256 != ref.ConfigSHA256 || inv.InputSHA256 != ref.InputSHA256 ||
		inv.SourceContextSHA256 != ref.SourceContextSHA256 || inv.GenerationScope != ref.GenerationScope {
		return ErrTaskMeetingConflict
	}
	return nil
}

func (ref TaskMeetingGenerationResultReference) Validate() error {
	if ref.TaskID.Validate() != nil || ref.RunID.Validate() != nil || ref.GenerationRequestID.Validate() != nil ||
		ref.ReceiptID != TaskMeetingGenerationOutputReceiptID(ref.TaskID, ref.RunID) ||
		(ref.Purpose != TaskMeetingPurposeReport && ref.Purpose != TaskMeetingPurposeAnswer) || !canonicalTaskMeetingActor(ref.ActorID) ||
		ref.OccurrenceKey == "" || len(ref.OccurrenceKey) > 128 || strings.TrimSpace(ref.OccurrenceKey) != ref.OccurrenceKey ||
		ref.ConfigRevision == 0 || !canonicalTaskMeetingSHA256(ref.ConfigSHA256) || !canonicalTaskMeetingSHA256(ref.InputSHA256) ||
		!canonicalTaskMeetingSHA256(ref.SourceContextSHA256) || ref.GenerationScope.Validate() != nil || !canonicalTaskMeetingSHA256(ref.ResultSHA256) ||
		ref.ReceiptEvidenceID.Validate() != nil || ref.RecordedAt.IsZero() {
		return ErrTaskMeetingConflict
	}
	return nil
}

func TaskMeetingGenerationOutputReceiptID(taskID modulecore.TaskID, runID modulecore.RunID) string {
	if taskID.Validate() != nil || runID.Validate() != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(string(taskID) + "\x00" + string(runID)))
	return "taskmeeting-output:" + hex.EncodeToString(sum[:])
}

func TaskMeetingGenerationUnknownReceiptID(taskID modulecore.TaskID, runID modulecore.RunID) string {
	if taskID.Validate() != nil || runID.Validate() != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(string(taskID) + "\x00" + string(runID)))
	return "taskmeeting-unknown:" + hex.EncodeToString(sum[:])
}

func TaskMeetingGenerationEffectStartedReceiptID(taskID modulecore.TaskID, runID modulecore.RunID) string {
	if taskID.Validate() != nil || runID.Validate() != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(string(taskID) + "\x00" + string(runID)))
	return "taskmeeting-started:" + hex.EncodeToString(sum[:])
}

func (ref TaskMeetingGenerationResultReference) ValidateTask(task Task) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	inv := task.TaskMeetingInvocation
	if inv == nil || task.TaskID != ref.TaskID || inv.Purpose != ref.Purpose || inv.ActorID != ref.ActorID || inv.OccurrenceKey != ref.OccurrenceKey ||
		inv.GenerationRequestID != ref.GenerationRequestID || inv.ConfigRevision != ref.ConfigRevision || inv.ConfigSHA256 != ref.ConfigSHA256 ||
		inv.InputSHA256 != ref.InputSHA256 || inv.SourceContextSHA256 != ref.SourceContextSHA256 || inv.GenerationScope != ref.GenerationScope {
		return ErrTaskMeetingConflict
	}
	return nil
}

func (ref TaskMeetingGenerationResultReference) ValidateRun(task Task, run Run) error {
	if err := ref.ValidateTask(task); err != nil {
		return err
	}
	if run.TaskID != ref.TaskID || run.RunID != ref.RunID || run.Assignee != ref.ActorID {
		return ErrTaskMeetingConflict
	}
	return nil
}

func (scope TaskMeetingGenerationScope) Validate() error {
	if scope.ScopeEvidenceID.Validate() != nil || scope.Generation == 0 || scope.Epoch == 0 {
		return ErrTaskMeetingConflict
	}
	return nil
}

const TaskMeetingGenerationInputSchema = "task_meeting.generation.v1"

type TaskMeetingGenerationInput struct {
	Schema  string                     `json:"schema"`
	Scope   TaskMeetingGenerationScope `json:"scope"`
	Payload json.RawMessage            `json:"payload"`
}

func EncodeTaskMeetingGenerationInput(scope TaskMeetingGenerationScope, payload json.RawMessage) (json.RawMessage, error) {
	if scope.Validate() != nil || len(payload) == 0 || !json.Valid(payload) {
		return nil, ErrTaskMeetingConflict
	}
	encoded, err := json.Marshal(TaskMeetingGenerationInput{Schema: TaskMeetingGenerationInputSchema, Scope: scope, Payload: append(json.RawMessage(nil), payload...)})
	return encoded, err
}

func ValidateTaskMeetingGenerationInput(encoded json.RawMessage, expected TaskMeetingGenerationScope) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var value TaskMeetingGenerationInput
	if err := decoder.Decode(&value); err != nil || value.Schema != TaskMeetingGenerationInputSchema || value.Scope != expected || value.Scope.Validate() != nil || len(value.Payload) == 0 || !json.Valid(value.Payload) {
		return ErrTaskMeetingConflict
	}
	canonical, err := json.Marshal(value)
	if err != nil || !bytes.Equal(canonical, encoded) {
		return ErrTaskMeetingConflict
	}
	return nil
}

const (
	TaskMeetingPurposeRoot             TaskMeetingPurpose = "root"
	TaskMeetingPurposeRecipientContext TaskMeetingPurpose = "recipient_context"
	TaskMeetingPurposeReport           TaskMeetingPurpose = "report"
	TaskMeetingPurposeAnswer           TaskMeetingPurpose = "answer"
	TaskMeetingMaxFrozenConfigBytes                       = 32 << 10
	TaskMeetingMaxFrozenInputBytes                        = 48 << 10
	TaskMeetingAudienceSize                               = 8
	TaskMeetingMaxConsumedMessages                        = 128
)

// TaskMeetingInvocation is immutable Task provenance for one scheduled
// meeting invocation. The occurrence key intentionally excludes the
// configuration revision; ConfigSHA256 pins the first accepted revision.
type TaskMeetingInvocation struct {
	OccurrenceKey          string                       `json:"occurrence_key"`
	Purpose                TaskMeetingPurpose           `json:"purpose"`
	CoordinatorActorID     string                       `json:"coordinator_actor_id"`
	ActorID                string                       `json:"actor_id"`
	ConfiguredOwnerUserID  string                       `json:"configured_owner_user_id"`
	ConfigRevision         uint64                       `json:"config_revision"`
	ConfigSHA256           string                       `json:"config_sha256"`
	RegistrationEvidenceID modulecore.EvidenceID        `json:"registration_evidence_id"`
	FrozenConfig           json.RawMessage              `json:"frozen_config"`
	Audience               []string                     `json:"audience"`
	AudienceSHA256         string                       `json:"audience_sha256"`
	InputSHA256            string                       `json:"input_sha256"`
	SourceContextSHA256    string                       `json:"source_context_sha256,omitempty"`
	FrozenInput            json.RawMessage              `json:"frozen_input"`
	FrozenSourceMessages   []TaskMeetingConsumedMessage `json:"frozen_source_messages,omitempty"`
	GenerationScope        TaskMeetingGenerationScope   `json:"generation_scope,omitempty"`
	AdmissionEvidenceID    modulecore.EvidenceID        `json:"admission_evidence_id"`
	GenerationRequestID    modulecore.RequestID         `json:"generation_request_id,omitempty"`
	Timing                 modulecore.TaskTiming        `json:"timing"`
	ParentTaskID           modulecore.TaskID            `json:"parent_task_id,omitempty"`
	ParentRunID            modulecore.RunID             `json:"parent_run_id,omitempty"`
}

type TaskMeetingRootProof struct {
	OccurrenceKey          string                `json:"occurrence_key"`
	CoordinatorActorID     string                `json:"coordinator_actor_id"`
	ConfiguredOwnerUserID  string                `json:"configured_owner_user_id"`
	TaskID                 modulecore.TaskID     `json:"task_id"`
	RunID                  modulecore.RunID      `json:"run_id"`
	ConfigRevision         uint64                `json:"config_revision"`
	ConfigSHA256           string                `json:"config_sha256"`
	RegistrationEvidenceID modulecore.EvidenceID `json:"registration_evidence_id"`
	Audience               []string              `json:"audience"`
	AudienceSHA256         string                `json:"audience_sha256"`
	AdmissionEvidenceID    modulecore.EvidenceID `json:"admission_evidence_id"`
	Timing                 modulecore.TaskTiming `json:"timing"`
}

func (proof TaskMeetingRootProof) Validate() error {
	if proof.OccurrenceKey == "" || proof.TaskID.Validate() != nil || proof.RunID.Validate() != nil ||
		!canonicalTaskMeetingActor(proof.CoordinatorActorID) || proof.ConfiguredOwnerUserID == "" ||
		proof.ConfigRevision == 0 || !canonicalTaskMeetingSHA256(proof.ConfigSHA256) || !canonicalTaskMeetingSHA256(proof.AudienceSHA256) ||
		proof.AdmissionEvidenceID.Validate() != nil || proof.RegistrationEvidenceID.Validate() != nil || proof.AdmissionEvidenceID == proof.RegistrationEvidenceID || proof.Timing.Validate() != nil {
		return ErrTaskMeetingConflict
	}
	audienceHash, err := TaskMeetingAudienceSHA256(proof.Audience)
	if err != nil || audienceHash != proof.AudienceSHA256 || !containsTaskMeetingActor(proof.Audience, proof.CoordinatorActorID) {
		return ErrTaskMeetingConflict
	}
	return nil
}

func (inv TaskMeetingInvocation) Validate(task Task) error {
	if inv.OccurrenceKey == "" || len(inv.OccurrenceKey) > 128 || strings.TrimSpace(inv.OccurrenceKey) != inv.OccurrenceKey {
		return errors.New("TaskMeeting occurrence key is invalid")
	}
	if !validTaskMeetingPurpose(inv.Purpose) {
		return errors.New("TaskMeeting purpose is unsupported")
	}
	if !canonicalTaskMeetingActor(inv.CoordinatorActorID) || !canonicalTaskMeetingActor(inv.ActorID) || inv.ConfigRevision == 0 {
		return errors.New("TaskMeeting actor or configuration revision is invalid")
	}
	if inv.ConfiguredOwnerUserID == "" || strings.TrimSpace(inv.ConfiguredOwnerUserID) != inv.ConfiguredOwnerUserID || strings.ContainsAny(inv.ConfiguredOwnerUserID, "\r\n\x00") {
		return errors.New("TaskMeeting configured owner user ID is invalid")
	}
	for _, digest := range []string{inv.ConfigSHA256, inv.AudienceSHA256, inv.InputSHA256} {
		if !canonicalTaskMeetingSHA256(digest) {
			return errors.New("TaskMeeting frozen input digest is invalid")
		}
	}
	if inv.Purpose == TaskMeetingPurposeReport || inv.Purpose == TaskMeetingPurposeAnswer {
		if inv.GenerationScope.Validate() != nil || ValidateTaskMeetingGenerationInput(inv.FrozenInput, inv.GenerationScope) != nil {
			return errors.New("TaskMeeting generation scope is invalid")
		}
		digest, digestErr := TaskMeetingConsumedMessagesSHA256(inv.FrozenSourceMessages)
		if !canonicalTaskMeetingSHA256(inv.SourceContextSHA256) || digestErr != nil || digest != inv.SourceContextSHA256 || validateFrozenTaskMeetingMessages(inv.FrozenSourceMessages, inv.ActorID, inv.ParentTaskID, inv.ParentRunID, inv.OccurrenceKey, inv.ConfigRevision, inv.ConfigSHA256, inv.AudienceSHA256) != nil {
			return errors.New("TaskMeeting generation must pin its consumed recipient context")
		}
	} else if len(inv.FrozenSourceMessages) != 0 || inv.GenerationScope != (TaskMeetingGenerationScope{}) {
		return errors.New("TaskMeeting non-generation invocation cannot pin source messages")
	}
	audienceHash, err := TaskMeetingAudienceSHA256(inv.Audience)
	if err != nil || audienceHash != inv.AudienceSHA256 || !containsTaskMeetingActor(inv.Audience, inv.CoordinatorActorID) || !containsTaskMeetingActor(inv.Audience, inv.ActorID) {
		return errors.New("TaskMeeting invocation audience does not match its frozen digest")
	}
	if inv.RegistrationEvidenceID.Validate() != nil || inv.Timing.Validate() != nil || task.Timing == nil || !sameTaskMeetingTiming(inv.Timing, *task.Timing) || !validFrozenTaskMeetingJSON(inv.FrozenConfig, TaskMeetingMaxFrozenConfigBytes, inv.ConfigSHA256) ||
		!validFrozenTaskMeetingJSON(inv.FrozenInput, TaskMeetingMaxFrozenInputBytes, inv.InputSHA256) {
		return errors.New("TaskMeeting frozen config or input is invalid")
	}
	if err := inv.AdmissionEvidenceID.Validate(); err != nil || inv.AdmissionEvidenceID == inv.RegistrationEvidenceID {
		return fmt.Errorf("TaskMeeting admission evidence ID is invalid: %w", err)
	}
	if task.PrivateAccess != nil || task.WorkstreamID != "" || task.GoalID != "" || task.TaskCenterExecution != nil || task.NativePlanningInvocation != nil || !task.ReadOnly || task.Assignee != inv.ActorID {
		return errors.New("TaskMeeting Task must be read-only, actor-bound, and have no Goal/Workstream/private task access metadata")
	}
	switch inv.Purpose {
	case TaskMeetingPurposeRoot:
		if inv.ActorID != inv.CoordinatorActorID || inv.ParentTaskID != "" || inv.ParentRunID != "" || inv.SourceContextSHA256 != "" || task.ParentTaskID != "" || task.Status != StatusRunning && task.Status != StatusSucceeded && task.Status != StatusFailed {
			return errors.New("TaskMeeting root Task identity is invalid")
		}
	case TaskMeetingPurposeRecipientContext:
		if inv.ParentTaskID.Validate() != nil || inv.ParentRunID.Validate() != nil || task.ParentTaskID != inv.ParentTaskID || inv.ActorID == inv.CoordinatorActorID && task.TaskID == inv.ParentTaskID || inv.GenerationRequestID != "" || inv.SourceContextSHA256 != "" || task.Status != StatusRunning && task.Status != StatusSucceeded && task.Status != StatusFailed {
			return errors.New("TaskMeeting recipient context must bind the actual root Task and Run")
		}
	case TaskMeetingPurposeReport, TaskMeetingPurposeAnswer:
		if inv.ParentTaskID.Validate() != nil || inv.ParentRunID.Validate() != nil || task.ParentTaskID != inv.ParentTaskID || inv.ActorID == inv.CoordinatorActorID && task.TaskID == inv.ParentTaskID {
			return errors.New("TaskMeeting generation Task must bind its actual recipient context Task and Run")
		}
		if inv.GenerationRequestID.Validate() != nil {
			return errors.New("TaskMeeting generation Task requires a stable request ID")
		}
		if task.Status != StatusRunning && task.Status != StatusSucceeded && task.Status != StatusFailed {
			return errors.New("TaskMeeting generation Task lifecycle is invalid")
		}
	}
	return nil
}

func IsTaskMeetingTask(task Task) bool { return task.TaskMeetingInvocation != nil }

type TaskMeetingMessageReference struct {
	OccurrenceKey     string                `json:"occurrence_key"`
	SessionID         modulecore.SessionID  `json:"session_id"`
	ThreadID          modulecore.ThreadID   `json:"thread_id"`
	MessageID         modulecore.MessageID  `json:"message_id"`
	EventID           modulecore.EventID    `json:"event_id"`
	BodySHA256        string                `json:"body_sha256"`
	AudienceSHA256    string                `json:"audience_sha256"`
	ConfigRevision    uint64                `json:"config_revision"`
	ConfigSHA256      string                `json:"config_sha256"`
	AttemptGeneration uint64                `json:"attempt_generation"`
	AttemptEpoch      uint64                `json:"attempt_epoch"`
	ScopeEvidenceID   modulecore.EvidenceID `json:"scope_evidence_id"`
}

func (ref TaskMeetingMessageReference) Validate() error {
	if ref.OccurrenceKey == "" || len(ref.OccurrenceKey) > 128 || strings.TrimSpace(ref.OccurrenceKey) != ref.OccurrenceKey {
		return errors.New("TaskMeeting message occurrence key is invalid")
	}
	for _, validate := range []func() error{ref.SessionID.Validate, ref.ThreadID.Validate, ref.MessageID.Validate, ref.EventID.Validate, ref.ScopeEvidenceID.Validate} {
		if err := validate(); err != nil {
			return err
		}
	}
	if !canonicalTaskMeetingSHA256(ref.BodySHA256) || !canonicalTaskMeetingSHA256(ref.AudienceSHA256) || !canonicalTaskMeetingSHA256(ref.ConfigSHA256) ||
		ref.ConfigRevision == 0 || ref.AttemptGeneration == 0 || ref.AttemptEpoch == 0 {
		return errors.New("TaskMeeting message reference is incomplete")
	}
	return nil
}

type TaskMeetingConsumedMessage struct {
	Reference      TaskMeetingMessageReference `json:"reference"`
	ActorID        string                      `json:"actor_id"`
	ConsumerTaskID modulecore.TaskID           `json:"consumer_task_id"`
	ConsumerRunID  modulecore.RunID            `json:"consumer_run_id"`
	ContextSHA256  string                      `json:"context_sha256"`
	AcceptedAt     time.Time                   `json:"accepted_at"`
	EvidenceID     modulecore.EvidenceID       `json:"evidence_id"`
}

func (proof TaskMeetingConsumedMessage) Validate(task Task, run Run) error {
	if err := proof.Reference.Validate(); err != nil {
		return err
	}
	if !canonicalTaskMeetingActor(proof.ActorID) || proof.ActorID != task.Assignee || proof.ConsumerTaskID != task.TaskID || proof.ConsumerRunID != run.RunID ||
		run.Validate() != nil || run.TaskID != task.TaskID || run.Assignee != task.Assignee ||
		!canonicalTaskMeetingSHA256(proof.ContextSHA256) || proof.AcceptedAt.IsZero() || proof.EvidenceID.Validate() != nil || !IsTaskMeetingTask(task) {
		return errors.New("TaskMeeting consumption proof does not bind the current native actor Task")
	}
	invocation := task.TaskMeetingInvocation
	if invocation == nil || invocation.OccurrenceKey != proof.Reference.OccurrenceKey || invocation.ConfigRevision != proof.Reference.ConfigRevision ||
		invocation.ConfigSHA256 != proof.Reference.ConfigSHA256 || invocation.AudienceSHA256 != proof.Reference.AudienceSHA256 {
		return errors.New("TaskMeeting consumption proof differs from its frozen invocation")
	}
	return nil
}

func canonicalTaskMeetingActor(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && strings.ToLower(value) == value && !strings.ContainsAny(value, "\r\n\x00")
}

func canonicalTaskMeetingSHA256(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	for _, char := range value {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func validFrozenTaskMeetingJSON(value json.RawMessage, limit int, digest string) bool {
	if len(value) == 0 || len(value) > limit || !json.Valid(value) {
		return false
	}
	hash := sha256.Sum256(value)
	return hex.EncodeToString(hash[:]) == digest
}

func mustTaskMeetingJSON(value any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}

func sameTaskMeetingTiming(left, right modulecore.TaskTiming) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

// TaskMeetingInvocationRequest is the exact receipt input. OccurrenceKey and
// Purpose form the stable operation identity; config revision and all frozen
// bytes participate in the request hash so changed input conflicts on replay.
type TaskMeetingInvocationRequest struct {
	OccurrenceKey          string                       `json:"occurrence_key"`
	Purpose                TaskMeetingPurpose           `json:"purpose"`
	CoordinatorActorID     string                       `json:"coordinator_actor_id"`
	ActorID                string                       `json:"actor_id"`
	ConfiguredOwnerUserID  string                       `json:"configured_owner_user_id"`
	ConfigRevision         uint64                       `json:"config_revision"`
	RegistrationEvidenceID modulecore.EvidenceID        `json:"registration_evidence_id"`
	FrozenConfig           json.RawMessage              `json:"frozen_config"`
	Audience               []string                     `json:"audience"`
	FrozenInput            json.RawMessage              `json:"frozen_input"`
	SourceContextSHA256    string                       `json:"source_context_sha256,omitempty"`
	FrozenSourceMessages   []TaskMeetingConsumedMessage `json:"frozen_source_messages,omitempty"`
	GenerationScope        TaskMeetingGenerationScope   `json:"generation_scope,omitempty"`
	GenerationRequestID    modulecore.RequestID         `json:"generation_request_id,omitempty"`
	Timing                 modulecore.TaskTiming        `json:"timing"`
	ParentTaskID           modulecore.TaskID            `json:"parent_task_id,omitempty"`
	ParentRunID            modulecore.RunID             `json:"parent_run_id,omitempty"`
}

func (request TaskMeetingInvocationRequest) Validate() error {
	if request.OccurrenceKey == "" || len(request.OccurrenceKey) > 128 || strings.TrimSpace(request.OccurrenceKey) != request.OccurrenceKey || strings.ContainsRune(request.OccurrenceKey, '\x00') ||
		!canonicalTaskMeetingActor(request.CoordinatorActorID) || !canonicalTaskMeetingActor(request.ActorID) || request.ConfiguredOwnerUserID == "" ||
		strings.TrimSpace(request.ConfiguredOwnerUserID) != request.ConfiguredOwnerUserID || strings.ContainsAny(request.ConfiguredOwnerUserID, "\r\n\x00") || request.ConfigRevision == 0 {
		return errors.New("TaskMeeting invocation request identity is invalid")
	}
	if !validTaskMeetingPurpose(request.Purpose) {
		return errors.New("TaskMeeting invocation purpose is unsupported")
	}
	if len(request.FrozenConfig) == 0 || len(request.FrozenConfig) > TaskMeetingMaxFrozenConfigBytes || !json.Valid(request.FrozenConfig) ||
		len(request.FrozenInput) == 0 || len(request.FrozenInput) > TaskMeetingMaxFrozenInputBytes || !json.Valid(request.FrozenInput) {
		return errors.New("TaskMeeting invocation request frozen input is invalid")
	}
	if err := request.RegistrationEvidenceID.Validate(); err != nil {
		return errors.New("TaskMeeting invocation request must pin persisted registration evidence")
	}
	if err := request.Timing.Validate(); err != nil {
		return fmt.Errorf("TaskMeeting timing: %w", err)
	}
	if len(request.Audience) != TaskMeetingAudienceSize {
		return errors.New("TaskMeeting invocation requires its exact eight-actor audience")
	}
	seen := make(map[string]struct{}, len(request.Audience))
	for _, actor := range request.Audience {
		if !canonicalTaskMeetingActor(actor) {
			return errors.New("TaskMeeting audience must contain eight canonical actor IDs")
		}
		if _, exists := seen[actor]; exists {
			return errors.New("TaskMeeting audience contains a duplicate actor")
		}
		seen[actor] = struct{}{}
	}
	if !containsTaskMeetingActor(request.Audience, request.CoordinatorActorID) || !containsTaskMeetingActor(request.Audience, request.ActorID) {
		return errors.New("TaskMeeting coordinator and invocation actor must be in the frozen audience")
	}
	switch request.Purpose {
	case TaskMeetingPurposeRoot:
		if request.ActorID != request.CoordinatorActorID || request.ParentTaskID != "" || request.ParentRunID != "" {
			return errors.New("TaskMeeting root invocation must not fabricate a parent Task or Run")
		}
		if request.GenerationRequestID != "" || request.SourceContextSHA256 != "" || len(request.FrozenSourceMessages) != 0 || request.GenerationScope != (TaskMeetingGenerationScope{}) {
			return errors.New("TaskMeeting root has no generation request ID")
		}
	case TaskMeetingPurposeRecipientContext:
		if request.ParentTaskID.Validate() != nil || request.ParentRunID.Validate() != nil || request.GenerationRequestID != "" || request.SourceContextSHA256 != "" || len(request.FrozenSourceMessages) != 0 || request.GenerationScope != (TaskMeetingGenerationScope{}) {
			return errors.New("TaskMeeting recipient context must bind the actual root Task and Run")
		}
	case TaskMeetingPurposeReport, TaskMeetingPurposeAnswer:
		digest, digestErr := TaskMeetingConsumedMessagesSHA256(request.FrozenSourceMessages)
		configDigest := taskMeetingJSONDigest(request.FrozenConfig)
		audienceDigest, _ := TaskMeetingAudienceSHA256(request.Audience)
		if request.ParentTaskID.Validate() != nil || request.ParentRunID.Validate() != nil || request.GenerationRequestID.Validate() != nil || request.GenerationScope.Validate() != nil || ValidateTaskMeetingGenerationInput(request.FrozenInput, request.GenerationScope) != nil || !canonicalTaskMeetingSHA256(request.SourceContextSHA256) || digestErr != nil || digest != request.SourceContextSHA256 || validateFrozenTaskMeetingMessages(request.FrozenSourceMessages, request.ActorID, request.ParentTaskID, request.ParentRunID, request.OccurrenceKey, request.ConfigRevision, configDigest, audienceDigest) != nil {
			return errors.New("TaskMeeting generation must bind its recipient context and stable request ID")
		}
	}
	return nil
}

func validTaskMeetingPurpose(purpose TaskMeetingPurpose) bool {
	switch purpose {
	case TaskMeetingPurposeRoot, TaskMeetingPurposeRecipientContext, TaskMeetingPurposeReport, TaskMeetingPurposeAnswer:
		return true
	default:
		return false
	}
}

func TaskMeetingAudienceSHA256(audience []string) (string, error) {
	if len(audience) != TaskMeetingAudienceSize {
		return "", errors.New("TaskMeeting audience must contain exactly eight actors")
	}
	actors := append([]string(nil), audience...)
	seen := make(map[string]struct{}, len(actors))
	for _, actor := range actors {
		if !canonicalTaskMeetingActor(actor) {
			return "", errors.New("TaskMeeting audience contains an invalid actor")
		}
		if _, exists := seen[actor]; exists {
			return "", errors.New("TaskMeeting audience contains a duplicate actor")
		}
		seen[actor] = struct{}{}
	}
	encoded, err := json.Marshal(actors)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}

func containsTaskMeetingActor(audience []string, actor string) bool {
	for _, candidate := range audience {
		if candidate == actor {
			return true
		}
	}
	return false
}

type TaskMeetingStateAuthorizationCheck func(context.Context, Task, Run, SharedRoleContext) error
type TaskMeetingReadAuthorizationCheck func(context.Context, Task, Run, SharedRoleContext) error
type TaskMeetingMessageReadAuthorizationCheck func(context.Context, Task, Run, SharedRoleContext, TaskMeetingMessageReference) error
type TaskMeetingAggregateProofCheck func(context.Context, Task, Run, SharedRoleContext, Status) error
type TaskMeetingGenerationResultCheck func(context.Context, Task, Run, SharedRoleContext, TaskMeetingGenerationResultReference) error

type TaskMeetingInvocationWriter interface {
	SaveTaskMeetingInvocationState(context.Context, Task, Run, *SharedRoleContext, TaskMeetingStateAuthorizationCheck) error
}

type TaskMeetingGenerationFinisher interface {
	FinishTaskMeetingInvocation(context.Context, modulecore.TaskID, modulecore.RunID, TaskMeetingGenerationResultReference, TaskMeetingStateAuthorizationCheck, TaskMeetingGenerationResultCheck) error
}

type TaskMeetingEffect func(context.Context, Task, Run, SharedRoleContext, json.RawMessage) (json.RawMessage, error)

type TaskMeetingEffectSettlement func(context.Context, Task, Run, SharedRoleContext) error

type TaskMeetingGenerationResultReader func(context.Context, Task, Run, SharedRoleContext, SharedRoleContext, json.RawMessage) (json.RawMessage, bool, error)

type TaskMeetingGenerationOutputStore interface {
	SaveTaskMeetingGenerationOutput(context.Context, modulecore.TaskID, modulecore.RunID, json.RawMessage, TaskMeetingReadAuthorizationCheck) (TaskMeetingGenerationResultReference, error)
	GetTaskMeetingGenerationOutput(context.Context, modulecore.TaskID, modulecore.RunID, TaskMeetingReadAuthorizationCheck) (json.RawMessage, TaskMeetingGenerationResultReference, error)
}

type TaskMeetingGenerationUnknownStore interface {
	StartTaskMeetingGenerationEffect(context.Context, modulecore.TaskID, modulecore.RunID, TaskMeetingReadAuthorizationCheck) (bool, error)
	RecordTaskMeetingGenerationUnknown(context.Context, TaskMeetingGenerationUnknownReference, TaskMeetingStateAuthorizationCheck) error
	GetTaskMeetingGenerationUnknown(context.Context, modulecore.TaskID, modulecore.RunID, TaskMeetingReadAuthorizationCheck) (TaskMeetingGenerationUnknownReference, error)
}

type TaskMeetingExecutionFencer interface {
	WithTaskMeetingExecutionFence(context.Context, modulecore.TaskID, modulecore.RunID, TaskMeetingReadAuthorizationCheck, TaskMeetingEffect, TaskMeetingEffectSettlement) (json.RawMessage, error)
}

type TaskMeetingExecutionFenceRecovery interface {
	RecoverTaskMeetingExecutionFence(context.Context, modulecore.TaskID, modulecore.RunID, TaskMeetingGenerationResultReference, TaskMeetingReadAuthorizationCheck) error
}

// TaskMeetingUnknownFenceRecovery releases only a generation fence whose exact
// effect-returned unknown receipt and failed Task/Run are already durable.
type TaskMeetingUnknownFenceRecovery interface {
	RecoverTaskMeetingUnknownExecutionFence(context.Context, modulecore.TaskID, modulecore.RunID, TaskMeetingGenerationUnknownReference, TaskMeetingReadAuthorizationCheck) error
}

type TaskMeetingExecutionFenceEvidence struct {
	FenceID                 string
	AcquireWriterGeneration uint64
	ReleaseWriterGeneration uint64
	Active                  bool
	RecoveryRequired        bool
}

// TaskMeetingExecutionFenceStatusReader exposes durable capability identity
// for the exact authorized generation, without exposing generic fence data.
type TaskMeetingExecutionFenceStatusReader interface {
	TaskMeetingExecutionFenceEvidence(context.Context, modulecore.TaskID, modulecore.RunID, TaskMeetingReadAuthorizationCheck) (TaskMeetingExecutionFenceEvidence, error)
}

type TaskMeetingInvocationReader interface {
	GetTaskMeetingInvocationState(context.Context, modulecore.TaskID, modulecore.RunID, TaskMeetingReadAuthorizationCheck) (Task, Run, SharedRoleContext, error)
}

type TaskMeetingActiveGenerationCounter interface {
	CountTaskMeetingActiveGenerations(context.Context) (int, error)
}

type TaskMeetingInvocationReceiptReader interface {
	LookupTaskMeetingInvocationReceipt(context.Context, string, TaskMeetingInvocationReadAuthorizationCheck) (json.RawMessage, bool, error)
}

type TaskMeetingMessageConsumer interface {
	AppendTaskMeetingConsumedMessage(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, reference TaskMeetingMessageReference, body string, authorize TaskMeetingMessageReadAuthorizationCheck) (TaskMeetingConsumedMessage, error)
}

type TaskMeetingMessageConsumptionReader interface {
	FindTaskMeetingConsumedMessage(context.Context, modulecore.TaskID, modulecore.RunID, TaskMeetingMessageReference, TaskMeetingMessageReadAuthorizationCheck) (TaskMeetingConsumedMessage, error)
}

func ValidateTaskMeetingConsumedMessages(task Task, run Run, context SharedRoleContext) error {
	if len(context.TaskMeetingConsumedMessages) > TaskMeetingMaxConsumedMessages {
		return errors.New("TaskMeeting recipient context exceeds its durable message-reference limit")
	}
	seen := make(map[string]struct{}, len(context.TaskMeetingConsumedMessages))
	for _, proof := range context.TaskMeetingConsumedMessages {
		if err := proof.Validate(task, run); err != nil {
			return err
		}
		key := string(proof.Reference.MessageID)
		if _, exists := seen[key]; exists {
			return errors.New("TaskMeeting recipient context contains a duplicate consumed message")
		}
		seen[key] = struct{}{}
	}
	return nil
}

// TaskMeetingContextSHA256 binds one actor generation to the durable, ordered
// set of message references accepted into that actor's recipient context.
func TaskMeetingContextSHA256(context SharedRoleContext) (string, error) {
	return TaskMeetingConsumedMessagesSHA256(context.TaskMeetingConsumedMessages)
}

func TaskMeetingConsumedMessagesSHA256(messages []TaskMeetingConsumedMessage) (string, error) {
	proofs := append([]TaskMeetingConsumedMessage(nil), messages...)
	if proofs == nil {
		proofs = []TaskMeetingConsumedMessage{}
	}
	encoded, err := json.Marshal(proofs)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validateFrozenTaskMeetingMessages(messages []TaskMeetingConsumedMessage, actor string, taskID modulecore.TaskID, runID modulecore.RunID, occurrence string, revision uint64, configSHA, audienceSHA string) error {
	if len(messages) > TaskMeetingMaxConsumedMessages {
		return errors.New("TaskMeeting frozen source context exceeds its message limit")
	}
	seen := make(map[modulecore.MessageID]struct{}, len(messages))
	for _, proof := range messages {
		if err := proof.Reference.Validate(); err != nil {
			return err
		}
		if proof.ActorID != actor || proof.ConsumerTaskID != taskID || proof.ConsumerRunID != runID || !canonicalTaskMeetingSHA256(proof.ContextSHA256) || proof.AcceptedAt.IsZero() || proof.EvidenceID.Validate() != nil || proof.Reference.OccurrenceKey != occurrence || proof.Reference.ConfigRevision != revision || proof.Reference.ConfigSHA256 != configSHA || proof.Reference.AudienceSHA256 != audienceSHA {
			return errors.New("TaskMeeting frozen source proof does not match its recipient context")
		}
		if _, exists := seen[proof.Reference.MessageID]; exists {
			return errors.New("TaskMeeting frozen source context contains a duplicate message")
		}
		seen[proof.Reference.MessageID] = struct{}{}
	}
	return nil
}

func taskMeetingJSONDigest(raw json.RawMessage) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

type TaskMeetingInvocationOperationAuthorizer func(context.Context) error
type TaskMeetingInvocationReadAuthorizationCheck func(context.Context, string, TaskMeetingPurpose, string) error
type TaskMeetingInvocationAdmissionCheck func(context.Context, TaskMeetingInvocationRequest, Task, Run, SharedRoleContext) error

type TaskMeetingInvocationOperationStore interface {
	ExecuteIdempotentTaskMeetingOperation(context.Context, string, string, TaskMeetingInvocationOperationAuthorizer, func(Store) (json.RawMessage, error)) (json.RawMessage, error)
	LookupTaskMeetingOperationReceipt(context.Context, string, TaskMeetingInvocationOperationAuthorizer) (json.RawMessage, string, bool, error)
}

func TaskMeetingInvocationOperationIdentity(request TaskMeetingInvocationRequest) (operationID, requestHash string, err error) {
	if err := request.Validate(); err != nil {
		return "", "", err
	}
	operationID, err = TaskMeetingInvocationOperationID(request.OccurrenceKey, request.Purpose, request.ActorID, request.GenerationRequestID)
	if err != nil {
		return "", "", err
	}
	audienceHash, err := TaskMeetingAudienceSHA256(request.Audience)
	if err != nil {
		return "", "", err
	}
	parts := [][]byte{
		[]byte(request.OccurrenceKey), []byte(request.Purpose), []byte(request.CoordinatorActorID), []byte(request.ActorID), []byte(request.ConfiguredOwnerUserID),
		[]byte(fmt.Sprint(request.ConfigRevision)), request.FrozenConfig, []byte(audienceHash), request.FrozenInput, []byte(request.RegistrationEvidenceID), []byte(request.GenerationRequestID), []byte(request.SourceContextSHA256), mustTaskMeetingJSON(canonicalTaskMeetingMessages(request.FrozenSourceMessages)), mustTaskMeetingJSON(request.GenerationScope),
		mustTaskMeetingJSON(request.Timing),
		[]byte(request.ParentTaskID), []byte(request.ParentRunID),
	}
	hasher := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprintf(hasher, "%d:", len(part))
		_, _ = hasher.Write(part)
	}
	requestHash = hex.EncodeToString(hasher.Sum(nil))
	return operationID, requestHash, nil
}

// TaskMeetingInvocationOperationID is the receipt-first stable key. It does
// not require the caller to build a current prompt or frozen config.
func TaskMeetingInvocationOperationID(occurrence string, purpose TaskMeetingPurpose, actor string, requestID modulecore.RequestID) (string, error) {
	if occurrence == "" || len(occurrence) > 128 || strings.TrimSpace(occurrence) != occurrence || strings.ContainsRune(occurrence, '\x00') || !validTaskMeetingPurpose(purpose) || !canonicalTaskMeetingActor(actor) {
		return "", ErrTaskMeetingConflict
	}
	identity := []string{occurrence, string(purpose), actor}
	if purpose == TaskMeetingPurposeReport || purpose == TaskMeetingPurposeAnswer {
		if requestID.Validate() != nil {
			return "", ErrTaskMeetingConflict
		}
		identity = append(identity, string(requestID))
	} else if requestID != "" {
		return "", ErrTaskMeetingConflict
	}
	operationDigest := sha256.Sum256([]byte(strings.Join(identity, "\x00")))
	return "taskmeeting:" + hex.EncodeToString(operationDigest[:]), nil
}

func canonicalTaskMeetingMessages(messages []TaskMeetingConsumedMessage) []TaskMeetingConsumedMessage {
	if messages == nil {
		return []TaskMeetingConsumedMessage{}
	}
	return messages
}
