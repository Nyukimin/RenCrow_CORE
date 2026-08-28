package workstream

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
)

const (
	StatusDraft     = "draft"
	StatusActive    = "active"
	StatusPaused    = "paused"
	StatusWaiting   = "waiting"
	StatusCompleted = "completed"
	StatusArchived  = "archived"
)

const (
	VaultReviewPending  = "pending"
	VaultReviewAdopted  = "adopted"
	VaultReviewRejected = "rejected"
)

type Workstream struct {
	WorkstreamID string    `json:"workstream_id"`
	Name         string    `json:"name"`
	Description  string    `json:"description,omitempty"`
	Status       string    `json:"status"`
	PrimaryAgent string    `json:"primary_agent,omitempty"`
	VaultPath    string    `json:"vault_path,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at,omitempty"`
}

type Goal struct {
	GoalID          string    `json:"goal_id"`
	TraceID         string    `json:"trace_id,omitempty"`
	WorkstreamID    string    `json:"workstream_id"`
	Title           string    `json:"title"`
	Description     string    `json:"description,omitempty"`
	SuccessCriteria []string  `json:"success_criteria,omitempty"`
	Verification    []string  `json:"verification,omitempty"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"created_at"`
	CompletedAt     time.Time `json:"completed_at,omitempty"`
}

type Artifact struct {
	ArtifactID   string `json:"artifact_id"`
	TraceID      string `json:"trace_id,omitempty"`
	WorkstreamID string `json:"workstream_id"`
	Type         string `json:"artifact_type"`
	FilePath     string `json:"file_path,omitempty"`
	Title        string `json:"title,omitempty"`
	Status       string `json:"status"`
	// Payload carries bounded, owner-validated typed artifact content. It is
	// stored in the existing Workstream artifact record so Atlas methodology
	// does not create a second ledger or physical database.
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at,omitempty"`
}

type ArtifactAnnotation struct {
	AnnotationID string    `json:"annotation_id"`
	ArtifactID   string    `json:"artifact_id"`
	Target       string    `json:"target,omitempty"`
	Comment      string    `json:"comment"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	ResolvedAt   time.Time `json:"resolved_at,omitempty"`
}

type SteeringItem struct {
	SteeringID       string    `json:"steering_id"`
	WorkstreamID     string    `json:"workstream_id"`
	TargetArtifactID string    `json:"target_artifact_id,omitempty"`
	Instruction      string    `json:"instruction"`
	Priority         string    `json:"priority,omitempty"`
	Status           string    `json:"status"`
	CreatedAt        time.Time `json:"created_at"`
	AppliedAt        time.Time `json:"applied_at,omitempty"`
}

type HeartbeatSchedule struct {
	HeartbeatID  string    `json:"heartbeat_id"`
	WorkstreamID string    `json:"workstream_id"`
	ScheduleText string    `json:"schedule_text"`
	Task         string    `json:"task"`
	Status       string    `json:"status"`
	LastRunAt    time.Time `json:"last_run_at,omitempty"`
	NextRunAt    time.Time `json:"next_run_at,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// ImplementationLease is the persisted singleton lease used by Atlas. It is
// stored alongside Workstream records so recovery observes one durable state.
type ImplementationLease struct {
	LeaseName          string    `json:"lease_name"`
	HolderUnitID       string    `json:"holder_unit_id"`
	HolderWorkstreamID string    `json:"holder_workstream_id"`
	Stage              string    `json:"stage,omitempty"`
	Revision           string    `json:"revision,omitempty"`
	AcquiredAt         time.Time `json:"acquired_at"`
	HeartbeatAt        time.Time `json:"heartbeat_at"`
}

// QueueFreeze is the durable queue-stop record for a blocked Atlas unit.  It
// intentionally lives in the existing Workstream store but keeps the
// backlog-owned EvidenceRef shape so owner evidence is not flattened.
type QueueFreeze struct {
	FreezeID              string                      `json:"freeze_id"`
	BlockedUnitID         string                      `json:"blocked_unit_id"`
	BlockedRevision       int                         `json:"blocked_revision"`
	FreezeRevision        int                         `json:"freeze_revision,omitempty"`
	ReasonCode            string                      `json:"reason_code"`
	InvalidatedFromStage  string                      `json:"invalidated_from_stage"`
	EvidenceRefs          []domainbacklog.EvidenceRef `json:"evidence_refs,omitempty"`
	Status                string                      `json:"status,omitempty"`
	ResolutionRequestID   string                      `json:"resolution_request_id,omitempty"`
	ReplacementUnitID     string                      `json:"replacement_unit_id,omitempty"`
	ReplacementLease      ImplementationLease         `json:"replacement_lease,omitempty"`
	ResolutionAcquired    bool                        `json:"resolution_acquired,omitempty"`
	SupersedesUnitID      string                      `json:"supersedes_unit_id,omitempty"`
	BlockerResolutionRefs []domainbacklog.EvidenceRef `json:"blocker_resolution_refs,omitempty"`
	ResolutionPayloadHash string                      `json:"resolution_payload_hash,omitempty"`
	CreatedAt             time.Time                   `json:"created_at"`
	UpdatedAt             time.Time                   `json:"updated_at,omitempty"`
	ResolvedAt            time.Time                   `json:"resolved_at,omitempty"`
}

// QueueFreezeResolution is the complete, already CORE-verified resolution
// payload that must be persisted together with the replacement lease.  The
// persistence owner receives this value as one operation so a resolved freeze
// can never be observed without its supersedes relation, blocker evidence, or
// payload identity.
type QueueFreezeResolution struct {
	ExpectedFreezeRevision int                         `json:"expected_freeze_revision"`
	ResolutionRequestID    string                      `json:"resolution_request_id"`
	ReplacementUnitID      string                      `json:"replacement_unit_id"`
	SupersedesUnitID       string                      `json:"supersedes_unit_id"`
	BlockerResolutionRefs  []domainbacklog.EvidenceRef `json:"blocker_resolution_refs"`
	ResolutionPayloadHash  string                      `json:"resolution_payload_hash"`
}

const (
	QueueFreezeActive   = "active"
	QueueFreezeResolved = "resolved"
)

var (
	ErrQueueFreezeNotFound           = errors.New("queue freeze not found")
	ErrQueueFreezeRevisionConflict   = errors.New("queue freeze revision conflict")
	ErrQueueFreezeResolutionConflict = errors.New("queue freeze resolution request conflict")
	ErrImplementationLeaseHeld       = errors.New("implementation lease is held by another unit")
	ErrQueueFrozen                   = errors.New("implementation queue is frozen")
)

// StageRunReceipt records one idempotent unit/revision/stage execution.
type StageRunReceipt struct {
	ReceiptID              string    `json:"receipt_id"`
	IdempotencyKey         string    `json:"idempotency_key"`
	RequestID              string    `json:"request_id,omitempty"`
	UnitID                 string    `json:"unit_id"`
	ItemID                 string    `json:"item_id,omitempty"`
	ImplementationRevision int       `json:"implementation_revision"`
	TargetStage            string    `json:"target_stage"`
	PayloadHash            string    `json:"payload_hash"`
	Status                 string    `json:"status"`
	DeliveryState          string    `json:"delivery_state,omitempty"`
	ResultJSON             string    `json:"result_json,omitempty"`
	ReasonCode             string    `json:"reason_code,omitempty"`
	Error                  string    `json:"error,omitempty"`
	CreatedAt              time.Time `json:"created_at"`
	CompletedAt            time.Time `json:"completed_at,omitempty"`
}

const (
	StageRunPrepared  = "prepared"
	StageRunCompleted = "completed"
	StageRunFailed    = "failed"
)

// ClosureReceipt is the durable phase marker for LIVE_VERIFIED -> DONE.
// Prepared receipts are intentionally replayable after a process restart.
type ClosureReceipt struct {
	ReceiptID              string    `json:"receipt_id"`
	IdempotencyKey         string    `json:"idempotency_key"`
	RequestID              string    `json:"request_id,omitempty"`
	UnitID                 string    `json:"unit_id"`
	ItemID                 string    `json:"item_id,omitempty"`
	ImplementationRevision int       `json:"implementation_revision"`
	Phase                  string    `json:"phase"`
	Status                 string    `json:"status"`
	WorkstreamID           string    `json:"workstream_id,omitempty"`
	GoalID                 string    `json:"goal_id,omitempty"`
	ArtifactID             string    `json:"artifact_id,omitempty"`
	LeaseName              string    `json:"lease_name,omitempty"`
	LeaseReleased          bool      `json:"lease_released,omitempty"`
	Error                  string    `json:"error,omitempty"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at,omitempty"`
	CompletedAt            time.Time `json:"completed_at,omitempty"`
}

const (
	ClosurePhasePrepared   = "prepared"
	ClosurePhaseResources  = "resources_completed"
	ClosurePhaseLease      = "lease_released"
	ClosurePhaseDone       = "done"
	ClosureStatusPrepared  = "prepared"
	ClosureStatusCompleted = "completed"
	ClosureStatusFailed    = "failed"
)

func ValidateQueueFreeze(item QueueFreeze) error {
	if strings.TrimSpace(item.FreezeID) == "" || strings.TrimSpace(item.BlockedUnitID) == "" {
		return fmt.Errorf("freeze_id and blocked_unit_id are required")
	}
	if item.BlockedRevision < 1 || item.CreatedAt.IsZero() {
		return fmt.Errorf("blocked_revision and created_at are required")
	}
	return nil
}

func ValidateQueueFreezeResolution(item QueueFreezeResolution, replacement ImplementationLease) error {
	if item.ExpectedFreezeRevision < 1 {
		return fmt.Errorf("expected_freeze_revision is required")
	}
	if strings.TrimSpace(item.ResolutionRequestID) == "" || strings.TrimSpace(item.ReplacementUnitID) == "" || strings.TrimSpace(item.SupersedesUnitID) == "" {
		return fmt.Errorf("queue freeze resolution identity is required")
	}
	if strings.TrimSpace(item.ResolutionPayloadHash) == "" {
		return fmt.Errorf("resolution_payload_hash is required")
	}
	if len(item.BlockerResolutionRefs) == 0 {
		return fmt.Errorf("blocker resolution evidence is required")
	}
	for _, ref := range item.BlockerResolutionRefs {
		if err := domainbacklog.ValidateEvidenceRef(ref); err != nil {
			return fmt.Errorf("invalid blocker resolution evidence: %w", err)
		}
	}
	if err := ValidateImplementationLease(replacement); err != nil {
		return err
	}
	if strings.TrimSpace(replacement.HolderUnitID) != strings.TrimSpace(item.ReplacementUnitID) {
		return fmt.Errorf("replacement lease holder does not match replacement unit")
	}
	return nil
}

// MatchesResolution reports whether a persisted freeze carries the exact
// resolution payload.  It is also used for an active pending record left by
// a crash between the replacement lease append and the resolved freeze
// append.
func (item QueueFreeze) MatchesResolution(resolution QueueFreezeResolution) bool {
	if item.FreezeRevision != resolution.ExpectedFreezeRevision || item.ResolutionRequestID != resolution.ResolutionRequestID || item.ReplacementUnitID != resolution.ReplacementUnitID || item.SupersedesUnitID != resolution.SupersedesUnitID || item.ResolutionPayloadHash != resolution.ResolutionPayloadHash {
		return false
	}
	if len(item.BlockerResolutionRefs) != len(resolution.BlockerResolutionRefs) {
		return false
	}
	for index := range item.BlockerResolutionRefs {
		if item.BlockerResolutionRefs[index] != resolution.BlockerResolutionRefs[index] {
			return false
		}
	}
	return true
}

// MatchesResolved reports whether a persisted resolved freeze is the exact
// replay of resolution.  Request ID alone is insufficient because it would
// allow a conflicting payload to be acknowledged as the original operation.
func (item QueueFreeze) MatchesResolved(resolution QueueFreezeResolution) bool {
	return item.Status == QueueFreezeResolved && item.MatchesResolution(resolution)
}

func ValidateStageRunReceipt(item StageRunReceipt) error {
	if strings.TrimSpace(item.ReceiptID) == "" || strings.TrimSpace(item.IdempotencyKey) == "" || strings.TrimSpace(item.UnitID) == "" {
		return fmt.Errorf("receipt identity is required")
	}
	if item.ImplementationRevision < 1 || strings.TrimSpace(item.TargetStage) == "" || item.CreatedAt.IsZero() {
		return fmt.Errorf("stage receipt revision, target_stage, and created_at are required")
	}
	return nil
}

func ValidateClosureReceipt(item ClosureReceipt) error {
	if strings.TrimSpace(item.ReceiptID) == "" || strings.TrimSpace(item.IdempotencyKey) == "" || strings.TrimSpace(item.UnitID) == "" {
		return fmt.Errorf("closure receipt identity is required")
	}
	if item.ImplementationRevision < 1 || item.CreatedAt.IsZero() {
		return fmt.Errorf("closure receipt revision and created_at are required")
	}
	return nil
}

func ValidateImplementationLease(item ImplementationLease) error {
	if item.LeaseName == "" {
		return fmt.Errorf("lease_name is required")
	}
	if item.HolderUnitID == "" {
		return fmt.Errorf("holder_unit_id is required")
	}
	if item.AcquiredAt.IsZero() {
		return fmt.Errorf("acquired_at is required")
	}
	if item.HeartbeatAt.IsZero() {
		return fmt.Errorf("heartbeat_at is required")
	}
	return nil
}

type VaultUpdateLog struct {
	UpdateID          string    `json:"update_id"`
	WorkstreamID      string    `json:"workstream_id"`
	FilePath          string    `json:"file_path"`
	UpdateType        string    `json:"update_type,omitempty"`
	ProposedContent   string    `json:"proposed_content,omitempty"`
	ContentHashBefore string    `json:"content_hash_before,omitempty"`
	ContentHashAfter  string    `json:"content_hash_after,omitempty"`
	ReviewStatus      string    `json:"review_status"`
	Applied           bool      `json:"applied,omitempty"`
	AppliedPath       string    `json:"applied_path,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
}

type VaultUpdatePreview struct {
	UpdateID        string `json:"update_id"`
	FilePath        string `json:"file_path"`
	CurrentContent  string `json:"current_content"`
	ProposedContent string `json:"proposed_content"`
	CurrentMissing  bool   `json:"current_missing"`
	AddedLines      int    `json:"added_lines"`
	RemovedLines    int    `json:"removed_lines"`
	UnifiedDiff     string `json:"unified_diff"`
}
