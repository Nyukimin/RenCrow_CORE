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
	ErrRunConflict   = errors.New("run state conflict")
)

type Store interface {
	SaveTask(context.Context, domaintask.Task) error
	GetTask(context.Context, modulecore.TaskID) (domaintask.Task, error)
	ListTasks(context.Context, domaintask.Filter) ([]domaintask.Task, error)
	SaveRun(context.Context, domaintask.Run) error
	GetRun(context.Context, modulecore.RunID) (domaintask.Run, error)
	ListRuns(context.Context, domaintask.RunFilter) ([]domaintask.Run, error)
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
	assigneeChanged := task.Assignee != assignee
	task.Assignee = assignee
	task.AssignmentEventID = eventID
	task.UpdatedAt = m.now()
	if err := task.Validate(); err != nil {
		return domaintask.Task{}, err
	}
	if err := m.store.SaveTask(ctx, task); err != nil {
		return domaintask.Task{}, err
	}
	if assigneeChanged && task.Status == domaintask.StatusRunning {
		if err := m.reassignCurrentRun(ctx, taskID, assignee); err != nil {
			return domaintask.Task{}, err
		}
	}
	return task, nil
}

func (m *Manager) Queue(ctx context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	return m.updateStatus(ctx, taskID, domaintask.StatusQueued, "", "", nil)
}

func (m *Manager) Start(ctx context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	if err := taskID.Validate(); err != nil {
		return domaintask.Task{}, fmt.Errorf("task_id is invalid: %w", err)
	}
	runs, err := m.store.ListRuns(ctx, domaintask.RunFilter{TaskID: taskID})
	if err != nil {
		return domaintask.Task{}, err
	}
	reason := domaintask.RunStartReasonFirst
	if len(runs) > 0 {
		reason = domaintask.RunStartReasonExplicitRerun
	}
	started, _, err := m.startWithReason(ctx, taskID, reason)
	return started, err
}

// StartWithReason starts a fresh Run for an existing Task using an explicit
// canonical Step10 reason. The first reason is valid only before any Run exists.
func (m *Manager) StartWithReason(ctx context.Context, taskID modulecore.TaskID, reason domaintask.RunStartReason) (domaintask.Task, error) {
	started, _, err := m.startWithReason(ctx, taskID, reason)
	return started, err
}

// StartRunWithReason starts a fresh canonical Run and returns the exact Run
// persisted for the Task. Callers that need to hand the newly issued Run to a
// queue or orchestrator must use this method rather than listing runs again.
func (m *Manager) StartRunWithReason(ctx context.Context, taskID modulecore.TaskID, reason domaintask.RunStartReason) (domaintask.Run, error) {
	_, run, err := m.startWithReason(ctx, taskID, reason)
	return run, err
}

func (m *Manager) startWithReason(ctx context.Context, taskID modulecore.TaskID, reason domaintask.RunStartReason) (domaintask.Task, domaintask.Run, error) {
	if err := taskID.Validate(); err != nil {
		return domaintask.Task{}, domaintask.Run{}, fmt.Errorf("task_id is invalid: %w", err)
	}
	if !domaintask.ValidRunStartReason(reason) {
		return domaintask.Task{}, domaintask.Run{}, fmt.Errorf("invalid run start reason: %s", reason)
	}
	task, err := m.store.GetTask(ctx, taskID)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	runs, err := m.store.ListRuns(ctx, domaintask.RunFilter{TaskID: taskID})
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	if reason == domaintask.RunStartReasonFirst && len(runs) > 0 {
		return domaintask.Task{}, domaintask.Run{}, fmt.Errorf("first run reason requires no prior runs")
	}
	if reason != domaintask.RunStartReasonFirst && len(runs) == 0 {
		return domaintask.Task{}, domaintask.Run{}, fmt.Errorf("run start reason %s requires a prior run", reason)
	}
	if domaintask.IsTerminal(task.Status) && reason != domaintask.RunStartReasonExplicitRerun {
		return domaintask.Task{}, domaintask.Run{}, fmt.Errorf("terminal task requires explicit_rerun")
	}
	activeRun, err := activeRun(runs)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	if activeRun != nil {
		closeStatus := domaintask.RunStatusInterrupted
		if reason == domaintask.RunStartReasonAgentReassignment {
			closeStatus = domaintask.RunStatusReassigned
		}
		closed, closeErr := activeRun.Close(closeStatus, m.now(), "run superseded by "+string(reason))
		if closeErr != nil {
			return domaintask.Task{}, domaintask.Run{}, closeErr
		}
		if err := m.store.SaveRun(ctx, closed); err != nil {
			return domaintask.Task{}, domaintask.Run{}, err
		}
	}
	if task.Status != domaintask.StatusRunning {
		allowed, limitReason, canStartErr := m.CanStart(ctx, task)
		if canStartErr != nil {
			return domaintask.Task{}, domaintask.Run{}, canStartErr
		}
		if !allowed {
			return domaintask.Task{}, domaintask.Run{}, fmt.Errorf("%w: %s", ErrParallelLimit, limitReason)
		}
	}
	var started domaintask.Task
	if domaintask.IsTerminal(task.Status) {
		started, err = m.reopenTerminalTask(ctx, task)
	} else {
		started, err = m.updateStatus(ctx, taskID, domaintask.StatusRunning, "", "", nil)
	}
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	run := domaintask.Run{
		RunID:       modulecore.NewRunID(),
		TaskID:      taskID,
		StartReason: reason,
		Assignee:    started.Assignee,
		Status:      domaintask.RunStatusRunning,
		StartedAt:   m.now(),
	}
	if err := m.store.SaveRun(ctx, run); err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	return started, run, nil
}

func (m *Manager) reopenTerminalTask(ctx context.Context, task domaintask.Task) (domaintask.Task, error) {
	now := m.now()
	task.Status = domaintask.StatusRunning
	task.FinishedAt = nil
	task.WaitingReason = ""
	task.UpdatedAt = now
	if err := task.Validate(); err != nil {
		return domaintask.Task{}, err
	}
	if err := m.store.SaveTask(ctx, task); err != nil {
		return domaintask.Task{}, err
	}
	return task, nil
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
	if status == domaintask.StatusRunning {
		return m.Start(ctx, taskID)
	}
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
	if closeStatus, shouldClose := runStatusForTaskStatus(status); shouldClose {
		if err := m.closeCurrentRun(ctx, taskID, closeStatus, strings.TrimSpace(summary)); err != nil {
			return domaintask.Task{}, err
		}
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

func (m *Manager) ListRuns(ctx context.Context, filter domaintask.RunFilter) ([]domaintask.Run, error) {
	return m.store.ListRuns(ctx, filter)
}

func (m *Manager) GetRun(ctx context.Context, runID modulecore.RunID) (domaintask.Run, error) {
	return m.store.GetRun(ctx, runID)
}

// InterruptRun closes exactly one issued Run without changing its Task. It is
// used when a downstream lease/queue CAS loses the reservation after the Task
// owner has already persisted a new canonical Run.
func (m *Manager) InterruptRun(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, summary string) (domaintask.Run, error) {
	if err := taskID.Validate(); err != nil {
		return domaintask.Run{}, fmt.Errorf("task_id is invalid: %w", err)
	}
	if err := runID.Validate(); err != nil {
		return domaintask.Run{}, fmt.Errorf("run_id is invalid: %w", err)
	}
	run, err := m.store.GetRun(ctx, runID)
	if err != nil {
		return domaintask.Run{}, err
	}
	if run.TaskID != taskID {
		return domaintask.Run{}, fmt.Errorf("%w: run %s belongs to task %s, want %s", ErrRunConflict, runID, run.TaskID, taskID)
	}
	if run.Status == domaintask.RunStatusInterrupted {
		return run, nil
	}
	if run.Status != domaintask.RunStatusRunning {
		return domaintask.Run{}, fmt.Errorf("%w: run %s is already terminal with status %s", ErrRunConflict, runID, run.Status)
	}
	closed, err := run.Close(domaintask.RunStatusInterrupted, m.now(), strings.TrimSpace(summary))
	if err != nil {
		return domaintask.Run{}, err
	}
	if err := m.store.SaveRun(ctx, closed); err != nil {
		return domaintask.Run{}, err
	}
	return closed, nil
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
		// A running parent is the coordination container for this candidate,
		// not a second execution consuming the same parallel capacity.
		if candidate.ParentTaskID != "" && item.TaskID == candidate.ParentTaskID {
			continue
		}
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

func runStatusForTaskStatus(status domaintask.Status) (domaintask.RunStatus, bool) {
	switch status {
	case domaintask.StatusQueued:
		return domaintask.RunStatusInterrupted, true
	case domaintask.StatusWaiting:
		return domaintask.RunStatusWaiting, true
	case domaintask.StatusBlocked:
		return domaintask.RunStatusBlocked, true
	case domaintask.StatusFailed:
		return domaintask.RunStatusFailed, true
	case domaintask.StatusSucceeded:
		return domaintask.RunStatusSucceeded, true
	case domaintask.StatusCancelled:
		return domaintask.RunStatusCancelled, true
	case domaintask.StatusSuperseded:
		return domaintask.RunStatusSuperseded, true
	default:
		return "", false
	}
}

func activeRun(runs []domaintask.Run) (*domaintask.Run, error) {
	var active *domaintask.Run
	for index := range runs {
		if !runs[index].IsActive() {
			continue
		}
		if active != nil {
			return nil, fmt.Errorf("task has multiple active runs")
		}
		candidate := runs[index]
		active = &candidate
	}
	return active, nil
}

func (m *Manager) closeCurrentRun(ctx context.Context, taskID modulecore.TaskID, status domaintask.RunStatus, summary string) error {
	runs, err := m.store.ListRuns(ctx, domaintask.RunFilter{TaskID: taskID, Status: domaintask.RunStatusRunning})
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		return nil
	}
	if len(runs) > 1 {
		return fmt.Errorf("task has multiple active runs")
	}
	closed, err := runs[0].Close(status, m.now(), summary)
	if err != nil {
		return err
	}
	return m.store.SaveRun(ctx, closed)
}

func (m *Manager) reassignCurrentRun(ctx context.Context, taskID modulecore.TaskID, assignee string) error {
	runs, err := m.store.ListRuns(ctx, domaintask.RunFilter{TaskID: taskID, Status: domaintask.RunStatusRunning})
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		return nil
	}
	if len(runs) > 1 {
		return fmt.Errorf("task has multiple active runs")
	}
	now := m.now()
	closed, err := runs[0].Close(domaintask.RunStatusReassigned, now, "agent reassigned to "+assignee)
	if err != nil {
		return err
	}
	if err := m.store.SaveRun(ctx, closed); err != nil {
		return err
	}
	return m.store.SaveRun(ctx, domaintask.Run{
		RunID:       modulecore.NewRunID(),
		TaskID:      taskID,
		StartReason: domaintask.RunStartReasonAgentReassignment,
		Assignee:    assignee,
		Status:      domaintask.RunStatusRunning,
		StartedAt:   now,
	})
}
