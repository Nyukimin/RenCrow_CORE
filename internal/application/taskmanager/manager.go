package taskmanager

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var (
	ErrNotFound      = domaintask.ErrNotFound
	ErrParallelLimit = errors.New("parallel limit exceeded")
)

type Store interface {
	SaveTask(context.Context, domaintask.Task) error
	GetTask(context.Context, modulecore.TaskID) (domaintask.Task, error)
	ListTasks(context.Context, domaintask.Filter) ([]domaintask.Task, error)
	SaveContext(context.Context, domaintask.SharedRoleContext) error
	GetContext(context.Context, modulecore.TaskID) (domaintask.SharedRoleContext, error)
	SaveNotification(context.Context, domaintask.Notification) error
	ListNotifications(context.Context, int, bool) ([]domaintask.Notification, error)
}

type ParallelLimits struct {
	Global            int
	PerModule         int
	CodingTasks       int
	LongResearchTasks int
	DestructiveTasks  int
}

func DefaultParallelLimits() ParallelLimits {
	return ParallelLimits{Global: 3, PerModule: 1, CodingTasks: 2, LongResearchTasks: 1, DestructiveTasks: 1}
}

type Manager struct {
	store  Store
	limits ParallelLimits
	now    func() time.Time
}

func New(store Store, limits ParallelLimits) *Manager {
	if limits.Global <= 0 {
		limits = DefaultParallelLimits()
	}
	return &Manager{store: store, limits: limits, now: func() time.Time { return time.Now().UTC() }}
}

func (m *Manager) Create(ctx context.Context, draft domaintask.Task, shared domaintask.SharedRoleContext) (domaintask.Task, error) {
	if err := ctx.Err(); err != nil {
		return domaintask.Task{}, err
	}
	now := m.now()
	draft.ApplyDefaults(now)
	if err := draft.Validate(); err != nil {
		return domaintask.Task{}, err
	}
	if err := m.validateRelationships(ctx, draft); err != nil {
		return domaintask.Task{}, err
	}
	if shared.TaskID != "" && shared.TaskID != draft.TaskID {
		return domaintask.Task{}, fmt.Errorf("context task_id must match created task_id")
	}
	if err := m.store.SaveTask(ctx, draft); err != nil {
		return domaintask.Task{}, err
	}
	if shared.TaskID == "" {
		shared.TaskID = draft.TaskID
	}
	if shared.ModuleID == "" {
		shared.ModuleID = draft.ModuleID
	}
	if shared.ModuleRoot == "" {
		shared.ModuleRoot = draft.ModuleRoot
	}
	shared.UpdatedAt = now
	if err := m.store.SaveContext(ctx, shared); err != nil {
		return domaintask.Task{}, err
	}
	return draft, nil
}

// RecordRouting persists the orchestrator route and the event that produced it.
func (m *Manager) RecordRouting(ctx context.Context, taskID modulecore.TaskID, route domaintask.Route, eventID modulecore.EventID) (domaintask.Task, error) {
	if err := taskID.Validate(); err != nil {
		return domaintask.Task{}, fmt.Errorf("task_id is invalid: %w", err)
	}
	if !domaintask.ValidRoute(route) {
		return domaintask.Task{}, fmt.Errorf("invalid route: %s", route)
	}
	if err := eventID.Validate(); err != nil {
		return domaintask.Task{}, fmt.Errorf("routing_event_id is invalid: %w", err)
	}
	task, err := m.store.GetTask(ctx, taskID)
	if err != nil {
		return domaintask.Task{}, err
	}
	task.Route = route
	task.RoutingEventID = eventID
	task.UpdatedAt = m.now()
	if err := task.Validate(); err != nil {
		return domaintask.Task{}, err
	}
	if err := m.store.SaveTask(ctx, task); err != nil {
		return domaintask.Task{}, err
	}
	return task, nil
}

// RecordAssignment persists the actual CORE Agent assignee and its event reference.
func (m *Manager) RecordAssignment(ctx context.Context, taskID modulecore.TaskID, assignee string, eventID modulecore.EventID) (domaintask.Task, error) {
	if err := taskID.Validate(); err != nil {
		return domaintask.Task{}, fmt.Errorf("task_id is invalid: %w", err)
	}
	assignee = strings.TrimSpace(assignee)
	if assignee == "" {
		return domaintask.Task{}, fmt.Errorf("assignee is required")
	}
	if err := eventID.Validate(); err != nil {
		return domaintask.Task{}, fmt.Errorf("assignment_event_id is invalid: %w", err)
	}
	task, err := m.store.GetTask(ctx, taskID)
	if err != nil {
		return domaintask.Task{}, err
	}
	task.Assignee = assignee
	task.AssignmentEventID = eventID
	task.UpdatedAt = m.now()
	if err := task.Validate(); err != nil {
		return domaintask.Task{}, err
	}
	if err := m.store.SaveTask(ctx, task); err != nil {
		return domaintask.Task{}, err
	}
	return task, nil
}

func (m *Manager) Queue(ctx context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	return m.updateStatus(ctx, taskID, domaintask.StatusQueued, "", "", nil)
}

func (m *Manager) Start(ctx context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	task, err := m.store.GetTask(ctx, taskID)
	if err != nil {
		return domaintask.Task{}, err
	}
	allowed, reason, err := m.CanStart(ctx, task)
	if err != nil {
		return domaintask.Task{}, err
	}
	if !allowed {
		return domaintask.Task{}, fmt.Errorf("%w: %s", ErrParallelLimit, reason)
	}
	return m.updateStatus(ctx, taskID, domaintask.StatusRunning, "", "", nil)
}

func (m *Manager) Wait(ctx context.Context, taskID modulecore.TaskID, reason string) (domaintask.Task, error) {
	if strings.TrimSpace(reason) == "" {
		return domaintask.Task{}, fmt.Errorf("waiting reason is required")
	}
	return m.updateStatus(ctx, taskID, domaintask.StatusWaiting, strings.TrimSpace(reason), strings.TrimSpace(reason), nil)
}

func (m *Manager) Block(ctx context.Context, taskID modulecore.TaskID, reason string) (domaintask.Task, error) {
	if strings.TrimSpace(reason) == "" {
		return domaintask.Task{}, fmt.Errorf("blocked reason is required")
	}
	return m.updateStatus(ctx, taskID, domaintask.StatusBlocked, strings.TrimSpace(reason), strings.TrimSpace(reason), nil)
}

func (m *Manager) Resume(ctx context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	return m.updateStatus(ctx, taskID, domaintask.StatusQueued, "", "", nil)
}

func (m *Manager) Succeed(ctx context.Context, taskID modulecore.TaskID, summary string) (domaintask.Task, error) {
	return m.updateStatus(ctx, taskID, domaintask.StatusSucceeded, strings.TrimSpace(summary), "", nil)
}

func (m *Manager) Fail(ctx context.Context, taskID modulecore.TaskID, summary string, nextActions []string) (domaintask.Task, error) {
	return m.updateStatus(ctx, taskID, domaintask.StatusFailed, strings.TrimSpace(summary), "", nextActions)
}

func (m *Manager) Cancel(ctx context.Context, taskID modulecore.TaskID, summary string) (domaintask.Task, error) {
	return m.updateStatus(ctx, taskID, domaintask.StatusCancelled, strings.TrimSpace(summary), "", nil)
}

func (m *Manager) Supersede(ctx context.Context, taskID modulecore.TaskID, replacement modulecore.TaskID) (domaintask.Task, error) {
	if err := taskID.Validate(); err != nil {
		return domaintask.Task{}, err
	}
	if err := replacement.Validate(); err != nil {
		return domaintask.Task{}, fmt.Errorf("replacement task_id is invalid: %w", err)
	}
	if taskID == replacement {
		return domaintask.Task{}, fmt.Errorf("task cannot supersede itself")
	}
	if _, err := m.store.GetTask(ctx, taskID); err != nil {
		return domaintask.Task{}, err
	}
	replacementTask, err := m.store.GetTask(ctx, replacement)
	if err != nil {
		return domaintask.Task{}, err
	}
	if replacementTask.SupersedesTaskID != taskID {
		return domaintask.Task{}, fmt.Errorf("replacement task must declare supersedes_task_id %s", taskID)
	}
	return m.updateStatus(ctx, taskID, domaintask.StatusSuperseded, "superseded by "+string(replacement), "", nil)
}

func (m *Manager) UpdateStatus(ctx context.Context, taskID modulecore.TaskID, status domaintask.Status, summary, waitingReason string, nextActions []string) (domaintask.Task, error) {
	return m.updateStatus(ctx, taskID, status, summary, waitingReason, nextActions)
}

func (m *Manager) updateStatus(ctx context.Context, taskID modulecore.TaskID, status domaintask.Status, summary, waitingReason string, nextActions []string) (domaintask.Task, error) {
	if err := taskID.Validate(); err != nil {
		return domaintask.Task{}, err
	}
	task, err := m.store.GetTask(ctx, taskID)
	if err != nil {
		return domaintask.Task{}, err
	}
	if !domaintask.CanTransition(task.Status, status) {
		return domaintask.Task{}, fmt.Errorf("invalid status transition: %s -> %s", task.Status, status)
	}
	if status == domaintask.StatusWaiting && strings.TrimSpace(waitingReason) == "" {
		return domaintask.Task{}, fmt.Errorf("waiting reason is required")
	}
	now := m.now()
	task.Status = status
	task.WaitingReason = strings.TrimSpace(waitingReason)
	task.UpdatedAt = now
	if status == domaintask.StatusRunning && task.StartedAt == nil {
		started := now
		task.StartedAt = &started
	}
	if domaintask.IsTerminal(status) && task.FinishedAt == nil {
		finished := now
		task.FinishedAt = &finished
	}
	if strings.TrimSpace(summary) != "" {
		task.Summary = strings.TrimSpace(summary)
	}
	if nextActions != nil {
		task.NextActions = append([]string(nil), nextActions...)
	}
	if err := task.Validate(); err != nil {
		return domaintask.Task{}, err
	}
	if err := m.store.SaveTask(ctx, task); err != nil {
		return domaintask.Task{}, err
	}
	if domaintask.ShouldNotify(task) {
		if err := m.store.SaveNotification(ctx, domaintask.NewNotification(task, now)); err != nil {
			return domaintask.Task{}, err
		}
	}
	return task, nil
}

func (m *Manager) UpdateContext(ctx context.Context, shared domaintask.SharedRoleContext) error {
	if err := shared.TaskID.Validate(); err != nil {
		return fmt.Errorf("task_id is invalid: %w", err)
	}
	if _, err := m.store.GetTask(ctx, shared.TaskID); err != nil {
		return err
	}
	shared.UpdatedAt = m.now()
	return m.store.SaveContext(ctx, shared)
}

func (m *Manager) List(ctx context.Context, filter domaintask.Filter) ([]domaintask.Task, error) {
	return m.store.ListTasks(ctx, filter)
}

func (m *Manager) Get(ctx context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	return m.store.GetTask(ctx, taskID)
}

func (m *Manager) Context(ctx context.Context, taskID modulecore.TaskID) (domaintask.SharedRoleContext, error) {
	return m.store.GetContext(ctx, taskID)
}

func (m *Manager) Notifications(ctx context.Context, limit int, interruptOnly bool) ([]domaintask.Notification, error) {
	return m.store.ListNotifications(ctx, limit, interruptOnly)
}

func (m *Manager) CanStart(ctx context.Context, candidate domaintask.Task) (bool, string, error) {
	if err := candidate.Validate(); err != nil {
		return false, "", err
	}
	for _, dependencyID := range candidate.DependencyTaskIDs {
		dependency, err := m.store.GetTask(ctx, dependencyID)
		if err != nil {
			return false, "", err
		}
		if dependency.Status != domaintask.StatusSucceeded {
			return false, "dependency task is not succeeded", nil
		}
	}
	running, err := m.store.ListTasks(ctx, domaintask.Filter{Status: domaintask.StatusRunning, Limit: 10000})
	if err != nil {
		return false, "", err
	}
	global, sameModule, coding, research, operations := 0, 0, 0, 0, 0
	for _, item := range running {
		global++
		if candidate.ModuleID != "" && item.ModuleID == candidate.ModuleID && !candidate.ReadOnly {
			sameModule++
		}
		switch item.Route {
		case domaintask.RouteCode:
			coding++
		case domaintask.RouteResearch:
			research++
		case domaintask.RouteOperations:
			operations++
		}
	}
	if m.limits.Global > 0 && global >= m.limits.Global {
		return false, "global running task limit reached", nil
	}
	if m.limits.PerModule > 0 && sameModule >= m.limits.PerModule {
		return false, "module running task limit reached", nil
	}
	switch candidate.Route {
	case domaintask.RouteCode:
		if m.limits.CodingTasks > 0 && coding >= m.limits.CodingTasks {
			return false, "coding task limit reached", nil
		}
	case domaintask.RouteResearch:
		if m.limits.LongResearchTasks > 0 && research >= m.limits.LongResearchTasks {
			return false, "long research task limit reached", nil
		}
	case domaintask.RouteOperations:
		if m.limits.DestructiveTasks > 0 && operations >= m.limits.DestructiveTasks {
			return false, "operations task limit reached", nil
		}
	}
	return true, "", nil
}

func (m *Manager) validateRelationships(ctx context.Context, value domaintask.Task) error {
	if value.ParentTaskID != "" {
		if _, err := m.store.GetTask(ctx, value.ParentTaskID); err != nil {
			return fmt.Errorf("parent task is unavailable: %w", err)
		}
	}
	for _, dependencyID := range value.DependencyTaskIDs {
		if _, err := m.store.GetTask(ctx, dependencyID); err != nil {
			return fmt.Errorf("dependency task is unavailable: %w", err)
		}
	}
	if value.SupersedesTaskID != "" {
		if _, err := m.store.GetTask(ctx, value.SupersedesTaskID); err != nil {
			return fmt.Errorf("superseded task is unavailable: %w", err)
		}
	}
	return nil
}
