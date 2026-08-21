package workstream

import (
	"fmt"
	"time"
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
	ArtifactID   string    `json:"artifact_id"`
	TraceID      string    `json:"trace_id,omitempty"`
	WorkstreamID string    `json:"workstream_id"`
	Type         string    `json:"artifact_type"`
	FilePath     string    `json:"file_path,omitempty"`
	Title        string    `json:"title,omitempty"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at,omitempty"`
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
