package workstream

import "time"

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
	VaultReviewApproved = "approved"
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
