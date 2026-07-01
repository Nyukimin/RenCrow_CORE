package sandbox

import "time"

const (
	SandboxStatusActive = "active"
	SandboxStatusClosed = "closed"
)

type SandboxRecord struct {
	SandboxID    string    `json:"sandbox_id"`
	WorkstreamID string    `json:"workstream_id,omitempty"`
	GoalID       string    `json:"goal_id,omitempty"`
	Type         string    `json:"type"`
	Path         string    `json:"path"`
	BaseRef      string    `json:"base_ref,omitempty"`
	CreatedBy    string    `json:"created_by,omitempty"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	ClosedAt     time.Time `json:"closed_at,omitempty"`
}

type SandboxArtifact struct {
	ArtifactID string    `json:"artifact_id"`
	SandboxID  string    `json:"sandbox_id"`
	Type       string    `json:"artifact_type"`
	FilePath   string    `json:"file_path"`
	Title      string    `json:"title,omitempty"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
}
