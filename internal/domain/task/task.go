package task

import (
	"errors"
	"fmt"
	"strings"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var ErrNotFound = errors.New("task not found")

type Status string

const (
	StatusQueued     Status = "queued"
	StatusRunning    Status = "running"
	StatusWaiting    Status = "waiting"
	StatusBlocked    Status = "blocked"
	StatusFailed     Status = "failed"
	StatusSucceeded  Status = "succeeded"
	StatusCancelled  Status = "cancelled"
	StatusSuperseded Status = "superseded"
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
	RouteCHAT       Route = "CHAT"
	RoutePLAN       Route = "PLAN"
	RouteANALYZE    Route = "ANALYZE"
	RouteWILD       Route = "WILD"
	RouteCODE1      Route = "CODE1"
	RouteCODE2      Route = "CODE2"
	RouteCODE3      Route = "CODE3"
	RouteCODE4      Route = "CODE4"
)

// Task is the durable executable work aggregate. The canonical TaskID and
// origin identities are kept separate from runtime trace, run, and transport
// identities owned by their respective boundaries.
type Task struct {
	TaskID            modulecore.TaskID       `json:"task_id"`
	Title             string                  `json:"title"`
	ModuleID          string                  `json:"module_id,omitempty"`
	ModuleRoot        string                  `json:"module_root,omitempty"`
	Route             Route                   `json:"route"`
	RoutingEventID    modulecore.EventID      `json:"routing_event_id,omitempty"`
	OwnerID           string                  `json:"owner_id,omitempty"`
	Assignee          string                  `json:"assignee,omitempty"`
	AssignmentEventID modulecore.EventID      `json:"assignment_event_id,omitempty"`
	CoderRoles        []string                `json:"coder_roles,omitempty"`
	Status            Status                  `json:"status"`
	Priority          Priority                `json:"priority"`
	ParentTaskID      modulecore.TaskID       `json:"parent_task_id,omitempty"`
	DependencyTaskIDs []modulecore.TaskID     `json:"dependency_task_ids,omitempty"`
	OriginSessionID   modulecore.SessionID    `json:"origin_session_id,omitempty"`
	OriginThreadID    modulecore.ThreadID     `json:"origin_thread_id,omitempty"`
	OriginTurnID      modulecore.TurnID       `json:"origin_turn_id,omitempty"`
	OriginMessageID   modulecore.MessageID    `json:"origin_message_id,omitempty"`
	WorkstreamID      modulecore.WorkstreamID `json:"workstream_id,omitempty"`
	GoalID            modulecore.GoalID       `json:"goal_id,omitempty"`
	SupersedesTaskID  modulecore.TaskID       `json:"supersedes_task_id,omitempty"`
	InterruptPolicy   InterruptPolicy         `json:"interrupt_policy"`
	ReadOnly          bool                    `json:"read_only"`
	CreatedAt         time.Time               `json:"created_at"`
	UpdatedAt         time.Time               `json:"updated_at"`
	StartedAt         *time.Time              `json:"started_at,omitempty"`
	FinishedAt        *time.Time              `json:"finished_at,omitempty"`
	Summary           string                  `json:"summary,omitempty"`
	WaitingReason     string                  `json:"waiting_reason,omitempty"`
	NextActions       []string                `json:"next_actions,omitempty"`
	Evidence          []string                `json:"evidence,omitempty"`
	Artifacts         []string                `json:"artifacts,omitempty"`
}

type SharedRoleContext struct {
	TaskID        modulecore.TaskID `json:"task_id"`
	UserIntent    string            `json:"user_intent,omitempty"`
	ModuleID      string            `json:"module_id,omitempty"`
	ModuleRoot    string            `json:"module_root,omitempty"`
	RelevantFiles []string          `json:"relevant_files,omitempty"`
	Decisions     []string          `json:"decisions,omitempty"`
	Constraints   []string          `json:"constraints,omitempty"`
	CurrentPlan   string            `json:"current_plan,omitempty"`
	LatestStatus  string            `json:"latest_status,omitempty"`
	Artifacts     []string          `json:"artifacts,omitempty"`
	HandoffNotes  string            `json:"handoff_notes,omitempty"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

type Notification struct {
	Type        string            `json:"type"`
	Level       NotificationLevel `json:"level"`
	TaskID      modulecore.TaskID `json:"task_id"`
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

func (t *Task) ApplyDefaults(now time.Time) {
	if t.TaskID == "" {
		t.TaskID = modulecore.NewTaskID()
	}
	if t.Status == "" {
		t.Status = StatusQueued
	}
	if t.Priority == "" {
		t.Priority = PriorityNormal
	}
	if t.Route == "" {
		t.Route = RouteGeneral
	}
	if t.InterruptPolicy == "" {
		t.InterruptPolicy = InterruptNotifyDoneOrBlocked
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	t.UpdatedAt = now
}

func (t Task) Validate() error {
	if err := t.TaskID.Validate(); err != nil {
		return fmt.Errorf("task_id is invalid: %w", err)
	}
	if strings.TrimSpace(t.Title) == "" {
		return fmt.Errorf("title is required")
	}
	if !ValidStatus(t.Status) {
		return fmt.Errorf("invalid status: %s", t.Status)
	}
	if !ValidPriority(t.Priority) {
		return fmt.Errorf("invalid priority: %s", t.Priority)
	}
	if !ValidRoute(t.Route) {
		return fmt.Errorf("invalid route: %s", t.Route)
	}
	if !ValidInterruptPolicy(t.InterruptPolicy) {
		return fmt.Errorf("invalid interrupt_policy: %s", t.InterruptPolicy)
	}
	if t.CreatedAt.IsZero() || t.UpdatedAt.IsZero() {
		return fmt.Errorf("created_at and updated_at are required")
	}
	if t.UpdatedAt.Before(t.CreatedAt) {
		return fmt.Errorf("updated_at must not precede created_at")
	}
	if t.Status == StatusWaiting && strings.TrimSpace(t.WaitingReason) == "" {
		return fmt.Errorf("waiting_reason is required while waiting")
	}
	if err := validateOptionalID(t.ParentTaskID); err != nil {
		return fmt.Errorf("parent_task_id is invalid: %w", err)
	}
	if t.ParentTaskID == t.TaskID {
		return fmt.Errorf("task cannot parent itself")
	}
	seenDependencies := make(map[modulecore.TaskID]struct{}, len(t.DependencyTaskIDs))
	for _, dependency := range t.DependencyTaskIDs {
		if err := dependency.Validate(); err != nil {
			return fmt.Errorf("dependency_task_id is invalid: %w", err)
		}
		if dependency == t.TaskID {
			return fmt.Errorf("task cannot depend on itself")
		}
		if _, exists := seenDependencies[dependency]; exists {
			return fmt.Errorf("duplicate dependency_task_id")
		}
		seenDependencies[dependency] = struct{}{}
	}
	if err := validateOptionalID(t.OriginSessionID); err != nil {
		return fmt.Errorf("origin_session_id is invalid: %w", err)
	}
	if err := validateOptionalID(t.OriginThreadID); err != nil {
		return fmt.Errorf("origin_thread_id is invalid: %w", err)
	}
	if err := validateOptionalID(t.OriginTurnID); err != nil {
		return fmt.Errorf("origin_turn_id is invalid: %w", err)
	}
	if err := validateOptionalID(t.OriginMessageID); err != nil {
		return fmt.Errorf("origin_message_id is invalid: %w", err)
	}
	if err := validateOptionalID(t.WorkstreamID); err != nil {
		return fmt.Errorf("workstream_id is invalid: %w", err)
	}
	if err := validateOptionalID(t.GoalID); err != nil {
		return fmt.Errorf("goal_id is invalid: %w", err)
	}
	if err := validateOptionalID(t.RoutingEventID); err != nil {
		return fmt.Errorf("routing_event_id is invalid: %w", err)
	}
	if err := validateOptionalID(t.AssignmentEventID); err != nil {
		return fmt.Errorf("assignment_event_id is invalid: %w", err)
	}
	if err := validateOptionalID(t.SupersedesTaskID); err != nil {
		return fmt.Errorf("supersedes_task_id is invalid: %w", err)
	}
	if t.SupersedesTaskID == t.TaskID {
		return fmt.Errorf("task cannot supersede itself")
	}
	return nil
}

func validateOptionalID[T interface {
	comparable
	Validate() error
}](id T) error {
	var zero T
	if id == zero {
		return nil
	}
	return id.Validate()
}

func ValidStatus(status Status) bool {
	switch status {
	case StatusQueued, StatusRunning, StatusWaiting, StatusBlocked, StatusFailed, StatusSucceeded, StatusCancelled, StatusSuperseded:
		return true
	default:
		return false
	}
}

func ValidPriority(priority Priority) bool {
	switch priority {
	case PriorityLow, PriorityNormal, PriorityHigh, PriorityCritical:
		return true
	default:
		return false
	}
}

func ValidRoute(route Route) bool {
	switch route {
	case RouteCode, RouteResearch, RouteOperations, RouteGeneral,
		RouteCHAT, RoutePLAN, RouteANALYZE, RouteWILD, RouteCODE1, RouteCODE2, RouteCODE3, RouteCODE4:
		return true
	default:
		return false
	}
}

func ValidInterruptPolicy(policy InterruptPolicy) bool {
	switch policy {
	case InterruptNotifyDoneOrBlocked, InterruptSilent:
		return true
	default:
		return false
	}
}

func IsTerminal(status Status) bool {
	switch status {
	case StatusFailed, StatusSucceeded, StatusCancelled, StatusSuperseded:
		return true
	default:
		return false
	}
}

func CanTransition(from, to Status) bool {
	if from == to {
		return true
	}
	if IsTerminal(from) {
		return false
	}
	switch from {
	case StatusQueued:
		return to == StatusRunning || to == StatusFailed || to == StatusCancelled || to == StatusSuperseded
	case StatusRunning:
		return to == StatusWaiting || to == StatusBlocked || to == StatusFailed || to == StatusSucceeded || to == StatusCancelled || to == StatusSuperseded
	case StatusWaiting:
		return to == StatusQueued || to == StatusRunning || to == StatusBlocked || to == StatusCancelled || to == StatusSuperseded
	case StatusBlocked:
		return to == StatusQueued || to == StatusRunning || to == StatusFailed || to == StatusCancelled || to == StatusSuperseded
	default:
		return false
	}
}

func ShouldNotify(t Task) bool {
	if t.InterruptPolicy == InterruptSilent {
		return false
	}
	switch t.Status {
	case StatusSucceeded, StatusFailed, StatusBlocked, StatusWaiting:
		return true
	default:
		return false
	}
}

func NotificationLevelForStatus(status Status, priority Priority) NotificationLevel {
	if priority == PriorityCritical {
		return NotificationCritical
	}
	switch status {
	case StatusSucceeded:
		return NotificationDone
	case StatusFailed:
		return NotificationFailed
	case StatusBlocked, StatusWaiting:
		return NotificationBlocked
	default:
		return NotificationProgress
	}
}

func NewNotification(t Task, now time.Time) Notification {
	return Notification{
		Type: "task.notification", Level: NotificationLevelForStatus(t.Status, t.Priority), TaskID: t.TaskID,
		Title: t.Title, Assignee: t.Assignee, Route: t.Route, ModuleID: t.ModuleID, Status: t.Status,
		Summary: t.Summary, NextActions: append([]string(nil), t.NextActions...), Interrupt: ShouldNotify(t), CreatedAt: now,
	}
}
