package scheduler

import (
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type Schedule struct {
	ScheduleID  modulecore.ScheduleID `json:"schedule_id"`
	Name        string                `json:"name"`
	Schedule    string                `json:"schedule"`
	Prompt      string                `json:"prompt,omitempty"`
	Target      string                `json:"target,omitempty"`
	Enabled     bool                  `json:"enabled"`
	CreatedAt   time.Time             `json:"created_at"`
	UpdatedAt   time.Time             `json:"updated_at"`
	LastRunAt   time.Time             `json:"last_run_at,omitempty"`
	NextRunAt   time.Time             `json:"next_run_at,omitempty"`
	DisabledAt  time.Time             `json:"disabled_at,omitempty"`
	DisabledBy  string                `json:"disabled_by,omitempty"`
	Description string                `json:"description,omitempty"`
}

type RunLog struct {
	RunID       modulecore.RunID      `json:"run_id"`
	ScheduleID  modulecore.ScheduleID `json:"schedule_id"`
	TaskID      modulecore.TaskID     `json:"task_id"`
	Trigger     string                `json:"trigger"`
	Status      string                `json:"status"`
	StartedAt   time.Time             `json:"started_at"`
	CompletedAt time.Time             `json:"completed_at,omitempty"`
	Summary     string                `json:"summary,omitempty"`
	Error       string                `json:"error,omitempty"`
}

type DueSchedule struct {
	Schedule  Schedule  `json:"schedule"`
	Scheduled time.Time `json:"scheduled"`
}
