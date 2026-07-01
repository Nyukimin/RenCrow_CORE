package job

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrNotFound = errors.New("job not found")

type Status string

const (
	StatusQueued      Status = "queued"
	StatusRunning     Status = "running"
	StatusWaitingUser Status = "waiting_user"
	StatusBlocked     Status = "blocked"
	StatusFailed      Status = "failed"
	StatusSucceeded   Status = "succeeded"
	StatusCancelled   Status = "cancelled"
	StatusSuperseded  Status = "superseded"
)

type Priority string

const (
	PriorityLow      Priority = "low"
	PriorityNormal   Priority = "normal"
	PriorityHigh     Priority = "high"
	PriorityCritical Priority = "critical"
)

type InterruptPolicy string

const (
	InterruptNotifyDoneOrBlocked InterruptPolicy = "notify_on_done_or_blocked"
	InterruptSilent              InterruptPolicy = "silent"
)

type NotificationLevel string

const (
	NotificationDone     NotificationLevel = "done"
	NotificationFailed   NotificationLevel = "failed"
	NotificationBlocked  NotificationLevel = "blocked"
	NotificationCritical NotificationLevel = "critical"
	NotificationProgress NotificationLevel = "progress"
)

type Route string

const (
	RouteCode       Route = "CODE"
	RouteResearch   Route = "RESEARCH"
	RouteOperations Route = "OPS"
	RouteGeneral    Route = "GENERAL"
)

type Job struct {
	JobID                string          `json:"job_id"`
	Title                string          `json:"title"`
	ModuleID             string          `json:"module_id,omitempty"`
	ModuleRoot           string          `json:"module_root,omitempty"`
	Route                Route           `json:"route"`
	Assignee             string          `json:"assignee,omitempty"`
	CoderRoles           []string        `json:"coder_roles,omitempty"`
	Status               Status          `json:"status"`
	Priority             Priority        `json:"priority"`
	CreatedBy            string          `json:"created_by,omitempty"`
	ParentConversationID string          `json:"parent_conversation_id,omitempty"`
	ParentMessageID      string          `json:"parent_message_id,omitempty"`
	InterruptPolicy      InterruptPolicy `json:"interrupt_policy"`
	SupersedesJobID      string          `json:"supersedes_job_id,omitempty"`
	ReadOnly             bool            `json:"read_only"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
	StartedAt            *time.Time      `json:"started_at,omitempty"`
	FinishedAt           *time.Time      `json:"finished_at,omitempty"`
	Summary              string          `json:"summary,omitempty"`
	NextActions          []string        `json:"next_actions,omitempty"`
	Evidence             []string        `json:"evidence,omitempty"`
	Artifacts            []string        `json:"artifacts,omitempty"`
}

type SharedRoleContext struct {
	JobID         string    `json:"job_id"`
	UserIntent    string    `json:"user_intent,omitempty"`
	ModuleID      string    `json:"module_id,omitempty"`
	ModuleRoot    string    `json:"module_root,omitempty"`
	RelevantFiles []string  `json:"relevant_files,omitempty"`
	Decisions     []string  `json:"decisions,omitempty"`
	Constraints   []string  `json:"constraints,omitempty"`
	CurrentPlan   string    `json:"current_plan,omitempty"`
	LatestStatus  string    `json:"latest_status,omitempty"`
	Artifacts     []string  `json:"artifacts,omitempty"`
	HandoffNotes  string    `json:"handoff_notes,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Notification struct {
	Type        string            `json:"type"`
	Level       NotificationLevel `json:"level"`
	JobID       string            `json:"job_id"`
	Title       string            `json:"title"`
	Assignee    string            `json:"assignee,omitempty"`
	Route       Route             `json:"route,omitempty"`
	ModuleID    string            `json:"module_id,omitempty"`
	Status      Status            `json:"status"`
	Summary     string            `json:"summary,omitempty"`
	NextActions []string          `json:"next_actions,omitempty"`
	Interrupt   bool              `json:"interrupt"`
	CreatedAt   time.Time         `json:"created_at"`
}

type Filter struct {
	Status   Status
	ModuleID string
	Assignee string
	Route    Route
	Limit    int
}

func NewJobID(now time.Time) string {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("job_%s", now.UTC().Format("20060102_150405"))
	}
	return fmt.Sprintf("job_%s_%s", now.UTC().Format("20060102_150405"), hex.EncodeToString(b[:]))
}

func (j *Job) ApplyDefaults(now time.Time) {
	if j.JobID == "" {
		j.JobID = NewJobID(now)
	}
	if j.Status == "" {
		j.Status = StatusQueued
	}
	if j.Priority == "" {
		j.Priority = PriorityNormal
	}
	if j.Route == "" {
		j.Route = RouteGeneral
	}
	if j.InterruptPolicy == "" {
		j.InterruptPolicy = InterruptNotifyDoneOrBlocked
	}
	if j.CreatedAt.IsZero() {
		j.CreatedAt = now
	}
	j.UpdatedAt = now
}

func (j Job) Validate() error {
	if strings.TrimSpace(j.JobID) == "" {
		return fmt.Errorf("job_id is required")
	}
	if strings.TrimSpace(j.Title) == "" {
		return fmt.Errorf("title is required")
	}
	if !ValidStatus(j.Status) {
		return fmt.Errorf("invalid status: %s", j.Status)
	}
	if !ValidPriority(j.Priority) {
		return fmt.Errorf("invalid priority: %s", j.Priority)
	}
	if strings.TrimSpace(string(j.Route)) == "" {
		return fmt.Errorf("route is required")
	}
	if j.CreatedAt.IsZero() || j.UpdatedAt.IsZero() {
		return fmt.Errorf("created_at and updated_at are required")
	}
	return nil
}

func ValidStatus(s Status) bool {
	switch s {
	case StatusQueued, StatusRunning, StatusWaitingUser, StatusBlocked, StatusFailed, StatusSucceeded, StatusCancelled, StatusSuperseded:
		return true
	default:
		return false
	}
}

func ValidPriority(p Priority) bool {
	switch p {
	case PriorityLow, PriorityNormal, PriorityHigh, PriorityCritical:
		return true
	default:
		return false
	}
}

func IsTerminal(s Status) bool {
	switch s {
	case StatusFailed, StatusSucceeded, StatusCancelled, StatusSuperseded:
		return true
	default:
		return false
	}
}

func CanTransition(from Status, to Status) bool {
	if from == to {
		return true
	}
	if IsTerminal(from) {
		return false
	}
	switch from {
	case StatusQueued:
		return to == StatusRunning || to == StatusCancelled || to == StatusSuperseded
	case StatusRunning:
		return to == StatusWaitingUser || to == StatusBlocked || to == StatusFailed || to == StatusSucceeded || to == StatusCancelled || to == StatusSuperseded
	case StatusWaitingUser:
		return to == StatusQueued || to == StatusRunning || to == StatusBlocked || to == StatusCancelled || to == StatusSuperseded
	case StatusBlocked:
		return to == StatusQueued || to == StatusRunning || to == StatusFailed || to == StatusCancelled || to == StatusSuperseded
	default:
		return false
	}
}

func ShouldNotify(j Job) bool {
	if j.InterruptPolicy == InterruptSilent {
		return false
	}
	switch j.Status {
	case StatusSucceeded, StatusFailed, StatusBlocked, StatusWaitingUser:
		return true
	default:
		return false
	}
}

func NotificationLevelForStatus(s Status, p Priority) NotificationLevel {
	if p == PriorityCritical {
		return NotificationCritical
	}
	switch s {
	case StatusSucceeded:
		return NotificationDone
	case StatusFailed:
		return NotificationFailed
	case StatusBlocked, StatusWaitingUser:
		return NotificationBlocked
	default:
		return NotificationProgress
	}
}

func NewNotification(j Job, now time.Time) Notification {
	return Notification{
		Type:        "job.status",
		Level:       NotificationLevelForStatus(j.Status, j.Priority),
		JobID:       j.JobID,
		Title:       j.Title,
		Assignee:    j.Assignee,
		Route:       j.Route,
		ModuleID:    j.ModuleID,
		Status:      j.Status,
		Summary:     j.Summary,
		NextActions: append([]string(nil), j.NextActions...),
		Interrupt:   ShouldNotify(j),
		CreatedAt:   now,
	}
}
