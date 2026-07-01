package scheduler

import "time"

type Job struct {
	JobID       string    `json:"job_id"`
	Name        string    `json:"name"`
	Schedule    string    `json:"schedule"`
	Prompt      string    `json:"prompt,omitempty"`
	Target      string    `json:"target,omitempty"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	LastRunAt   time.Time `json:"last_run_at,omitempty"`
	NextRunAt   time.Time `json:"next_run_at,omitempty"`
	DisabledAt  time.Time `json:"disabled_at,omitempty"`
	DisabledBy  string    `json:"disabled_by,omitempty"`
	Description string    `json:"description,omitempty"`
}

type RunLog struct {
	RunID       string    `json:"run_id"`
	JobID       string    `json:"job_id"`
	Trigger     string    `json:"trigger"`
	Status      string    `json:"status"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at,omitempty"`
	Summary     string    `json:"summary,omitempty"`
	Error       string    `json:"error,omitempty"`
}

type DueJob struct {
	Job       Job       `json:"job"`
	Scheduled time.Time `json:"scheduled"`
}
