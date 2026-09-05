package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	taskLifecycleMio    = "mio"
	taskLifecycleShiro  = "shiro"
	taskLifecycleMidori = "midori"
	taskLifecycleKuro   = "kuro"
	maxTaskTitleRunes   = 160
)

var errAttachedTaskMismatch = errors.New("attached task execution mismatch")

func attachedTaskMismatchf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errAttachedTaskMismatch, fmt.Sprintf(format, args...))
}

// TaskLifecycleManager is the minimal task-manager contract required by the
// orchestrator lifecycle. Get/Wait are included so an internal resume can
// validate and preserve the canonical Task/Run boundary before execution.
type TaskLifecycleManager interface {
	Create(context.Context, domaintask.Task, domaintask.SharedRoleContext) (domaintask.Task, error)
	Get(context.Context, modulecore.TaskID) (domaintask.Task, error)
	Start(context.Context, modulecore.TaskID) (domaintask.Task, error)
	ListRuns(context.Context, domaintask.RunFilter) ([]domaintask.Run, error)
	Wait(context.Context, modulecore.TaskID, string) (domaintask.Task, error)
	Succeed(context.Context, modulecore.TaskID, string) (domaintask.Task, error)
	Fail(context.Context, modulecore.TaskID, string, []string) (domaintask.Task, error)
	RecordRouting(context.Context, modulecore.TaskID, domaintask.Route, modulecore.EventID) (domaintask.Task, error)
	RecordAssignment(context.Context, modulecore.TaskID, string, modulecore.EventID) (domaintask.Task, error)
}

// taskLifecycle owns the root/child task transitions around one orchestrator
// request.  The context map is only a handoff cache between createRoot and
// prepareExecution; durable task state remains owned by TaskLifecycleManager.
type taskLifecycle struct {
	manager      TaskLifecycleManager
	mu           sync.RWMutex
	rootContexts map[modulecore.TaskID]domaintask.SharedRoleContext
}

type taskLifecycleEventPort interface {
	Publish(eventType, from, to, content, route, taskID, sessionID, channel, chatID string, causationEventID modulecore.EventID, dependencyEventIDs []modulecore.EventID) (OrchestratorEvent, error)
	BindTrace(taskID string, traceID modulecore.TraceID)
	ReleaseTrace(taskID string)
	BindExecutionIdentity(taskID modulecore.TaskID, runID modulecore.RunID, actorKind, actorID string) error
	ReleaseExecutionIdentity(taskID modulecore.TaskID)
	BindResponseMessageID(taskID string, messageID modulecore.MessageID)
	ReleaseResponseMessageID(taskID string)
}

// taskLifecycleActivation is the single routing/assignment boundary shared by
// all ProcessMessage branches. A later real-Agent handoff reuses the original
// routing decision and adds a child task; it never invents another root.
type taskLifecycleActivation struct {
	lifecycle      *taskLifecycle
	events         taskLifecycleEventPort
	req            ProcessMessageRequest
	attachedTask   *domaintask.Task
	rootTaskID     modulecore.TaskID
	traceID        modulecore.TraceID
	routingEventID modulecore.EventID
	route          routing.Route
	actorTasks     map[string]modulecore.TaskID
	cleanups       []func()
}

func newTaskLifecycleActivation(lifecycle *taskLifecycle, events taskLifecycleEventPort, req ProcessMessageRequest, attachedTask *domaintask.Task) (*taskLifecycleActivation, func()) {
	activation := &taskLifecycleActivation{
		lifecycle:    lifecycle,
		events:       events,
		req:          req,
		attachedTask: attachedTask,
		rootTaskID:   modulecore.TaskID(req.RootTaskID),
		traceID:      modulecore.TraceID(req.TraceID),
		actorTasks:   make(map[string]modulecore.TaskID),
	}
	return activation, func() {
		for index := len(activation.cleanups) - 1; index >= 0; index-- {
			activation.cleanups[index]()
		}
	}
}

func (a *taskLifecycleActivation) Activate(ctx context.Context, route routing.Route, assignee, routingContent string) (modulecore.TaskID, error) {
	if a == nil || a.events == nil {
		return "", fmt.Errorf("task lifecycle activation event port is unavailable")
	}
	actor, err := canonicalCoreActor(assignee)
	if err != nil {
		return "", err
	}
	if a.attachedTask != nil {
		domainRoute, err := taskRouteForOrchestratorRoute(route)
		if err != nil {
			return "", attachedTaskMismatchf("decided route is invalid: %v", err)
		}
		if a.attachedTask.TaskID != a.rootTaskID {
			return "", attachedTaskMismatchf("attached task %s does not match root task %s", a.attachedTask.TaskID, a.rootTaskID)
		}
		if a.attachedTask.Route != domainRoute {
			return "", attachedTaskMismatchf("attached task route mismatch: saved=%s decided=%s", a.attachedTask.Route, domainRoute)
		}
		savedActor, err := canonicalCoreActor(a.attachedTask.Assignee)
		if err != nil {
			return "", attachedTaskMismatchf("attached task assignee is invalid: %v", err)
		}
		if a.attachedTask.Assignee != savedActor {
			return "", attachedTaskMismatchf("attached task assignee is not canonical: saved=%q", a.attachedTask.Assignee)
		}
		if savedActor != actor {
			return "", attachedTaskMismatchf("attached task assignee mismatch: saved=%s decided=%s", savedActor, actor)
		}
	}
	if existing := a.actorTasks[actor]; existing != "" {
		return existing, nil
	}
	if a.routingEventID == "" {
		routingEvent, err := a.events.Publish(
			"routing.decision", taskLifecycleMio, "", routingContent,
			string(route), a.rootTaskID.String(), a.req.SessionID, a.req.Channel, a.req.ChatID, "", nil,
		)
		if err != nil {
			return "", err
		}
		a.routingEventID = routingEvent.EventID
		a.route = route
	} else if route != a.route {
		return "", fmt.Errorf("task lifecycle route changed after activation: %s -> %s", a.route, route)
	}

	// A nil lifecycle is the intentionally unconfigured path. It still uses the
	// same routing publisher as configured requests, but has no durable Task.
	if a.lifecycle == nil {
		if a.attachedTask != nil {
			return "", fmt.Errorf("attached task requires configured lifecycle")
		}
		a.actorTasks[actor] = a.rootTaskID
		return a.rootTaskID, nil
	}
	if a.attachedTask != nil {
		if _, err := a.lifecycle.manager.RecordRouting(ctx, a.rootTaskID, a.attachedTask.Route, a.routingEventID); err != nil {
			return "", fmt.Errorf("record attached task routing: %w", err)
		}
		assignmentEvent, err := a.events.Publish(
			"agent.assignment", taskLifecycleMio, actor, fmt.Sprintf("assigned route=%s", route),
			string(route), a.rootTaskID.String(), a.req.SessionID, a.req.Channel, a.req.ChatID, a.routingEventID, nil,
		)
		if err != nil {
			return a.rootTaskID, err
		}
		if _, err := a.lifecycle.manager.RecordAssignment(ctx, a.rootTaskID, actor, assignmentEvent.EventID); err != nil {
			return a.rootTaskID, fmt.Errorf("record attached task assignment: %w", err)
		}
		if err := a.bindExecutionIdentity(ctx, a.rootTaskID, actor); err != nil {
			return a.rootTaskID, err
		}
		a.actorTasks[actor] = a.rootTaskID
		return a.rootTaskID, nil
	}

	var taskID modulecore.TaskID
	if len(a.actorTasks) == 0 {
		taskID, err = a.lifecycle.prepareExecution(ctx, a.rootTaskID, route, a.routingEventID, actor)
	} else if actor == taskLifecycleMio {
		taskID = a.rootTaskID
	} else {
		taskID, err = a.lifecycle.createExecutionChild(ctx, a.rootTaskID, route, a.routingEventID, actor)
	}
	if err != nil {
		return "", err
	}
	if taskID != a.rootTaskID {
		a.events.BindTrace(taskID.String(), a.traceID)
		a.events.BindResponseMessageID(taskID.String(), modulecore.MessageID(a.req.AgentMessageID))
		childTaskID := taskID
		a.cleanups = append(a.cleanups, func() {
			a.events.ReleaseTrace(childTaskID.String())
			a.events.ReleaseResponseMessageID(childTaskID.String())
		})
	}
	assignmentEvent, err := a.events.Publish(
		"agent.assignment", taskLifecycleMio, actor, fmt.Sprintf("assigned route=%s", route),
		string(route), taskID.String(), a.req.SessionID, a.req.Channel, a.req.ChatID, a.routingEventID, nil,
	)
	if err != nil {
		return taskID, err
	}
	if len(a.actorTasks) == 0 {
		err = a.lifecycle.recordAssignmentAndStart(ctx, a.rootTaskID, taskID, actor, assignmentEvent.EventID)
	} else {
		err = a.lifecycle.recordChildAssignmentAndStart(ctx, taskID, actor, assignmentEvent.EventID)
	}
	if err != nil {
		return taskID, err
	}
	if err := a.bindExecutionIdentity(ctx, taskID, actor); err != nil {
		return taskID, err
	}
	a.actorTasks[actor] = taskID
	return taskID, nil
}

func (a *taskLifecycleActivation) bindExecutionIdentity(ctx context.Context, taskID modulecore.TaskID, actor string) error {
	if a == nil || a.lifecycle == nil {
		return nil
	}
	if err := taskID.Validate(); err != nil {
		return fmt.Errorf("execution task ID is invalid: %w", err)
	}
	canonicalActor, err := canonicalCoreActor(actor)
	if err != nil {
		return err
	}
	task, err := a.lifecycle.manager.Get(ctx, taskID)
	if err != nil {
		return fmt.Errorf("resolve execution task %s: %w", taskID, err)
	}
	if task.TaskID != taskID {
		return fmt.Errorf("execution task identity mismatch: got %s want %s", task.TaskID, taskID)
	}
	taskActor, err := canonicalCoreActor(task.Assignee)
	if err != nil {
		return fmt.Errorf("execution task %s assignee is invalid: %w", taskID, err)
	}
	if taskActor != canonicalActor {
		return fmt.Errorf("execution task %s assignee mismatch: saved=%s want=%s", taskID, taskActor, canonicalActor)
	}
	run, err := a.lifecycle.activeRunForTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("resolve execution Run for task %s: %w", taskID, err)
	}
	if run.TaskID != taskID {
		return fmt.Errorf("execution Run %s belongs to task %s, want %s", run.RunID, run.TaskID, taskID)
	}
	runActor, err := canonicalCoreActor(run.Assignee)
	if err != nil {
		return fmt.Errorf("execution Run %s assignee is invalid: %w", run.RunID, err)
	}
	if runActor != canonicalActor {
		return fmt.Errorf("execution Run %s assignee mismatch: run=%s want=%s", run.RunID, runActor, canonicalActor)
	}
	if err := a.events.BindExecutionIdentity(taskID, run.RunID, canonicalExecutionActorKind, canonicalActor); err != nil {
		return fmt.Errorf("bind execution identity for task %s: %w", taskID, err)
	}
	boundTaskID := taskID
	a.cleanups = append(a.cleanups, func() {
		a.events.ReleaseExecutionIdentity(boundTaskID)
	})
	return nil
}

func newTaskLifecycle(manager TaskLifecycleManager) *taskLifecycle {
	return &taskLifecycle{
		manager:      manager,
		rootContexts: make(map[modulecore.TaskID]domaintask.SharedRoleContext),
	}
}

// activeRunForTask returns the one canonical active Run for a started Task.
// The lifecycle boundary validates the returned identity instead of trusting a
// store implementation's filter to enforce ownership.
func (l *taskLifecycle) activeRunForTask(ctx context.Context, taskID modulecore.TaskID) (domaintask.Run, error) {
	if l == nil || l.manager == nil {
		return domaintask.Run{}, fmt.Errorf("task lifecycle manager is unavailable")
	}
	if err := taskID.Validate(); err != nil {
		return domaintask.Run{}, fmt.Errorf("task ID is invalid: %w", err)
	}
	runs, err := l.manager.ListRuns(ctx, domaintask.RunFilter{TaskID: taskID, Status: domaintask.RunStatusRunning})
	if err != nil {
		return domaintask.Run{}, fmt.Errorf("list active task runs: %w", err)
	}
	if len(runs) != 1 {
		return domaintask.Run{}, fmt.Errorf("task %s must have exactly one active run, got %d", taskID, len(runs))
	}
	run := runs[0]
	if err := run.Validate(); err != nil {
		return domaintask.Run{}, fmt.Errorf("active run is invalid: %w", err)
	}
	if run.TaskID != taskID {
		return domaintask.Run{}, fmt.Errorf("active run %s belongs to task %s, want %s", run.RunID, run.TaskID, taskID)
	}
	if run.Status != domaintask.RunStatusRunning {
		return domaintask.Run{}, fmt.Errorf("run %s is not active: %s", run.RunID, run.Status)
	}
	return run, nil
}

// startRepairExecution creates and starts the supplied repair Task through the
// same durable owner used by normal message execution. Repair is an out-of-band
// entry point, but it must still publish the routing and actual-Agent
// assignment before the Task owner issues its first Run.
func (l *taskLifecycle) startRepairExecution(ctx context.Context, events taskLifecycleEventPort, req ProcessRepairRequest, route routing.Route) (domaintask.Run, error) {
	if l == nil || l.manager == nil {
		return domaintask.Run{}, fmt.Errorf("task lifecycle manager is unavailable")
	}
	if events == nil {
		return domaintask.Run{}, fmt.Errorf("task lifecycle event port is unavailable")
	}
	if err := req.TaskID.Validate(); err != nil {
		return domaintask.Run{}, fmt.Errorf("repair task ID is invalid: %w", err)
	}
	domainRoute, err := taskRouteForOrchestratorRoute(route)
	if err != nil {
		return domaintask.Run{}, fmt.Errorf("repair route is invalid: %w", err)
	}
	if _, err := l.manager.Get(ctx, req.TaskID); err == nil {
		return domaintask.Run{}, fmt.Errorf("repair task %s already exists", req.TaskID)
	} else if !errors.Is(err, domaintask.ErrNotFound) {
		return domaintask.Run{}, fmt.Errorf("check repair task %s: %w", req.TaskID, err)
	}

	draft := domaintask.Task{
		TaskID:          req.TaskID,
		Title:           boundedTaskTitle("Repair: " + req.Reason),
		Route:           domainRoute,
		OwnerID:         taskLifecycleMio,
		Status:          domaintask.StatusQueued,
		Priority:        domaintask.PriorityNormal,
		InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked,
	}
	shared := domaintask.SharedRoleContext{
		TaskID:     req.TaskID,
		UserIntent: repairTaskMessage(req),
	}
	if _, err := l.manager.Create(ctx, draft, shared); err != nil {
		return domaintask.Run{}, fmt.Errorf("create repair task: %w", err)
	}

	failCreated := func(cause error) (domaintask.Run, error) {
		if _, failErr := l.manager.Fail(context.WithoutCancel(ctx), req.TaskID, cause.Error(), nil); failErr != nil {
			return domaintask.Run{}, errors.Join(cause, fmt.Errorf("fail repair task after startup error: %w", failErr))
		}
		return domaintask.Run{}, cause
	}

	routingEvent, err := events.Publish(
		"routing.decision", taskLifecycleMio, "",
		fmt.Sprintf("repair route=%s", route), string(route), req.TaskID.String(), req.SessionID, "viewer", "repair", "", nil,
	)
	if err != nil {
		return failCreated(fmt.Errorf("publish repair routing decision: %w", err))
	}
	if _, err := l.manager.RecordRouting(ctx, req.TaskID, domainRoute, routingEvent.EventID); err != nil {
		return failCreated(fmt.Errorf("record repair routing decision: %w", err))
	}
	assignmentEvent, err := events.Publish(
		"agent.assignment", taskLifecycleMio, taskLifecycleShiro,
		fmt.Sprintf("assigned route=%s", route), string(route), req.TaskID.String(), req.SessionID, "viewer", "repair", routingEvent.EventID, nil,
	)
	if err != nil {
		return failCreated(fmt.Errorf("publish repair Agent assignment: %w", err))
	}
	if _, err := l.manager.RecordAssignment(ctx, req.TaskID, taskLifecycleShiro, assignmentEvent.EventID); err != nil {
		return failCreated(fmt.Errorf("record repair Agent assignment: %w", err))
	}
	if _, err := l.manager.Start(ctx, req.TaskID); err != nil {
		return failCreated(fmt.Errorf("start repair Task: %w", err))
	}
	run, err := l.activeRunForTask(ctx, req.TaskID)
	if err != nil {
		return failCreated(fmt.Errorf("resolve repair Task Run: %w", err))
	}
	if run.TaskID != req.TaskID || run.Assignee != taskLifecycleShiro {
		return failCreated(fmt.Errorf("repair Run owner mismatch: task=%s run_task=%s assignee=%s", req.TaskID, run.TaskID, run.Assignee))
	}
	return run, nil
}

func (l *taskLifecycle) createRoot(ctx context.Context, req ProcessMessageRequest) (domaintask.Task, error) {
	if l == nil || l.manager == nil {
		return domaintask.Task{}, fmt.Errorf("task lifecycle manager is unavailable")
	}
	rootTaskID, err := modulecore.ParseTaskID(req.RootTaskID)
	if err != nil {
		return domaintask.Task{}, fmt.Errorf("root task ID is invalid: %w", err)
	}
	userIntent := strings.TrimSpace(req.UserMessage)
	title := boundedTaskTitle(userIntent)
	if title == "" {
		return domaintask.Task{}, fmt.Errorf("user message is required for root task title")
	}
	draft := domaintask.Task{
		TaskID:   rootTaskID,
		Title:    title,
		OwnerID:  taskLifecycleMio,
		Route:    domaintask.RouteGeneral,
		Status:   domaintask.StatusQueued,
		Priority: domaintask.PriorityNormal,
	}
	setValidTaskOrigins(&draft, req)
	shared := domaintask.SharedRoleContext{
		TaskID:     rootTaskID,
		UserIntent: userIntent,
	}
	created, err := l.manager.Create(ctx, draft, shared)
	if err != nil {
		return domaintask.Task{}, fmt.Errorf("create root task: %w", err)
	}
	l.mu.Lock()
	if l.rootContexts == nil {
		l.rootContexts = make(map[modulecore.TaskID]domaintask.SharedRoleContext)
	}
	l.rootContexts[rootTaskID] = cloneSharedRoleContext(shared, rootTaskID)
	l.mu.Unlock()
	return created, nil
}

// attachExisting validates the exact Task/Run pair supplied by an internal
// queue-resume handoff.  It deliberately does not create a Task or start a
// Run: the caller is continuing the already-running canonical execution.
func (l *taskLifecycle) attachExisting(ctx context.Context, req ProcessMessageRequest) (domaintask.Task, error) {
	if l == nil || l.manager == nil {
		return domaintask.Task{}, fmt.Errorf("task lifecycle manager is unavailable")
	}
	rootTaskID, err := modulecore.ParseTaskID(req.RootTaskID)
	if err != nil {
		return domaintask.Task{}, fmt.Errorf("attached root task ID is invalid: %w", err)
	}
	if err := req.CanonicalRunID.Validate(); err != nil {
		return domaintask.Task{}, fmt.Errorf("attached run ID is invalid: %w", err)
	}
	task, err := l.manager.Get(ctx, rootTaskID)
	if err != nil {
		return domaintask.Task{}, fmt.Errorf("get attached task: %w", err)
	}
	if task.TaskID != rootTaskID {
		return domaintask.Task{}, fmt.Errorf("attached task identity mismatch: got %s want %s", task.TaskID, rootTaskID)
	}
	if err := task.Validate(); err != nil {
		return domaintask.Task{}, fmt.Errorf("attached task is invalid: %w", err)
	}
	if task.Status != domaintask.StatusRunning {
		return domaintask.Task{}, fmt.Errorf("attached task %s is not running: %s", rootTaskID, task.Status)
	}
	run, err := l.activeRunForTask(ctx, rootTaskID)
	if err != nil {
		return domaintask.Task{}, err
	}
	if run.RunID != req.CanonicalRunID {
		return domaintask.Task{}, fmt.Errorf("attached run mismatch: active=%s requested=%s", run.RunID, req.CanonicalRunID)
	}
	return task, nil
}

func (l *taskLifecycle) prepareExecution(ctx context.Context, rootTaskID modulecore.TaskID, route routing.Route, routingEventID modulecore.EventID, assignee string) (modulecore.TaskID, error) {
	if l == nil || l.manager == nil {
		return "", fmt.Errorf("task lifecycle manager is unavailable")
	}
	if err := rootTaskID.Validate(); err != nil {
		return "", fmt.Errorf("root task ID is invalid: %w", err)
	}
	domainRoute, err := taskRouteForOrchestratorRoute(route)
	if err != nil {
		return "", err
	}
	actor, err := canonicalCoreActor(assignee)
	if err != nil {
		return "", err
	}
	if _, err := l.manager.RecordRouting(ctx, rootTaskID, domainRoute, routingEventID); err != nil {
		return "", fmt.Errorf("record root task routing: %w", err)
	}
	if actor == taskLifecycleMio {
		return rootTaskID, nil
	}
	return l.createExecutionChild(ctx, rootTaskID, route, routingEventID, actor)
}

func (l *taskLifecycle) createExecutionChild(ctx context.Context, rootTaskID modulecore.TaskID, route routing.Route, routingEventID modulecore.EventID, assignee string) (modulecore.TaskID, error) {
	if l == nil || l.manager == nil {
		return "", fmt.Errorf("task lifecycle manager is unavailable")
	}
	if err := rootTaskID.Validate(); err != nil {
		return "", fmt.Errorf("root task ID is invalid: %w", err)
	}
	domainRoute, err := taskRouteForOrchestratorRoute(route)
	if err != nil {
		return "", err
	}
	actor, err := canonicalCoreActor(assignee)
	if err != nil {
		return "", err
	}
	if actor == taskLifecycleMio {
		return rootTaskID, nil
	}
	childTaskID := modulecore.NewTaskID()
	child := domaintask.Task{
		TaskID:         childTaskID,
		Title:          fmt.Sprintf("%s execution", string(route)),
		Route:          domainRoute,
		RoutingEventID: routingEventID,
		OwnerID:        taskLifecycleMio,
		Assignee:       actor,
		Status:         domaintask.StatusQueued,
		Priority:       domaintask.PriorityNormal,
		ParentTaskID:   rootTaskID,
	}
	shared := l.sharedContextForRoot(rootTaskID, childTaskID)
	if _, err := l.manager.Create(ctx, child, shared); err != nil {
		return "", fmt.Errorf("create execution task: %w", err)
	}
	return childTaskID, nil
}

func (l *taskLifecycle) recordChildAssignmentAndStart(ctx context.Context, taskID modulecore.TaskID, assignee string, assignmentEventID modulecore.EventID) error {
	if l == nil || l.manager == nil {
		return fmt.Errorf("task lifecycle manager is unavailable")
	}
	actor, err := canonicalCoreActor(assignee)
	if err != nil {
		return err
	}
	if _, err := l.manager.RecordAssignment(ctx, taskID, actor, assignmentEventID); err != nil {
		return fmt.Errorf("record task assignment: %w", err)
	}
	if _, err := l.manager.Start(ctx, taskID); err != nil {
		return fmt.Errorf("start execution task: %w", err)
	}
	return nil
}

func (l *taskLifecycle) recordAssignmentAndStart(ctx context.Context, rootTaskID, executionTaskID modulecore.TaskID, assignee string, assignmentEventID modulecore.EventID) error {
	if l == nil || l.manager == nil {
		return fmt.Errorf("task lifecycle manager is unavailable")
	}
	actor, err := canonicalCoreActor(assignee)
	if err != nil {
		return err
	}
	if _, err := l.manager.RecordAssignment(ctx, executionTaskID, actor, assignmentEventID); err != nil {
		return fmt.Errorf("record task assignment: %w", err)
	}
	if _, err := l.manager.Start(ctx, rootTaskID); err != nil {
		return fmt.Errorf("start root task: %w", err)
	}
	if executionTaskID == rootTaskID {
		return nil
	}
	if _, err := l.manager.Start(ctx, executionTaskID); err != nil {
		return fmt.Errorf("start execution task: %w", err)
	}
	return nil
}

// finish records the terminal state for the execution task and then its root.
// The caller owns the original execution error; this method only returns a
// task-manager error encountered while recording the terminal state.
func (l *taskLifecycle) finish(ctx context.Context, rootTaskID, executionTaskID modulecore.TaskID, response string, executionErr error) error {
	if l == nil {
		return fmt.Errorf("task lifecycle manager is unavailable")
	}
	defer l.deleteRootContext(rootTaskID)
	if l.manager == nil {
		return fmt.Errorf("task lifecycle manager is unavailable")
	}
	var firstErr error
	if executionErr == nil {
		if executionTaskID != rootTaskID {
			if _, err := l.manager.Succeed(ctx, executionTaskID, response); err != nil {
				firstErr = fmt.Errorf("succeed execution task: %w", err)
			}
		}
		if _, err := l.manager.Succeed(ctx, rootTaskID, response); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("succeed root task: %w", err)
		}
		return firstErr
	}

	summary := strings.TrimSpace(executionErr.Error())
	executionTask, err := l.manager.Get(ctx, executionTaskID)
	if err != nil {
		return fmt.Errorf("get execution task before failure: %w", err)
	}
	if executionTask.Status == domaintask.StatusWaiting {
		waitingReason := strings.TrimSpace(executionTask.WaitingReason)
		if waitingReason == "" {
			return fmt.Errorf("waiting execution task %s has no waiting reason", executionTaskID)
		}
		if executionTaskID != rootTaskID {
			rootTask, getErr := l.manager.Get(ctx, rootTaskID)
			if getErr != nil {
				return fmt.Errorf("get root task while preserving wait: %w", getErr)
			}
			if rootTask.Status == domaintask.StatusRunning {
				if _, waitErr := l.manager.Wait(ctx, rootTaskID, waitingReason); waitErr != nil {
					return fmt.Errorf("wait root task after execution pause: %w", waitErr)
				}
			}
		}
		return nil
	}
	if executionTaskID != rootTaskID {
		if _, err := l.manager.Fail(ctx, executionTaskID, summary, nil); err != nil {
			firstErr = fmt.Errorf("fail execution task: %w", err)
		}
	}
	if _, err := l.manager.Fail(ctx, rootTaskID, summary, nil); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("fail root task: %w", err)
	}
	return firstErr
}

// actualCoreActorForRoute maps an orchestrator route to a real CORE Agent.
// It accepts only CORE identities; models, coders, providers, and runtimes are
// deliberately not representable as an assignee.
func actualCoreActorForRoute(route routing.Route, viewerRecipient string) (string, error) {
	switch route {
	case routing.RouteCHAT:
		recipient, err := modulechat.NormalizeViewerRecipient(viewerRecipient)
		if err != nil {
			return "", fmt.Errorf("invalid Viewer recipient: %w", err)
		}
		return canonicalCoreActor(string(recipient))
	case routing.RoutePLAN, routing.RouteRESEARCH:
		return taskLifecycleMio, nil
	case routing.RouteOPS, routing.RouteCODE, routing.RouteCODE1, routing.RouteCODE2, routing.RouteCODE3, routing.RouteCODE4:
		return taskLifecycleShiro, nil
	case routing.RouteWILD:
		return taskLifecycleMidori, nil
	case routing.RouteANALYZE:
		return taskLifecycleKuro, nil
	default:
		return "", fmt.Errorf("unsupported orchestrator route %q", route)
	}
}

func actualCoreActorForRequest(route routing.Route, req ProcessMessageRequest) (string, error) {
	return actualCoreActorForRoute(route, req.To)
}

func taskRouteForOrchestratorRoute(route routing.Route) (domaintask.Route, error) {
	converted := domaintask.Route(route)
	if !domaintask.ValidRoute(converted) {
		return "", fmt.Errorf("unsupported orchestrator route %q", route)
	}
	return converted, nil
}

func canonicalCoreActor(raw string) (string, error) {
	actor := strings.ToLower(strings.TrimSpace(raw))
	switch actor {
	case taskLifecycleMio, taskLifecycleShiro, taskLifecycleMidori, taskLifecycleKuro:
		return actor, nil
	default:
		return "", fmt.Errorf("unsupported CORE task assignee %q", raw)
	}
}

func boundedTaskTitle(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > maxTaskTitleRunes {
		runes = runes[:maxTaskTitleRunes]
	}
	return string(runes)
}

func setValidTaskOrigins(task *domaintask.Task, req ProcessMessageRequest) {
	if task == nil {
		return
	}
	if value := modulecore.SessionID(strings.TrimSpace(req.SessionID)); value != "" && value.Validate() == nil {
		task.OriginSessionID = value
	}
	if value := modulecore.TurnID(strings.TrimSpace(req.TurnID)); value != "" && value.Validate() == nil {
		task.OriginTurnID = value
	}
	if value := modulecore.MessageID(strings.TrimSpace(req.MessageID)); value != "" && value.Validate() == nil {
		task.OriginMessageID = value
	}
}

func (l *taskLifecycle) sharedContextForRoot(rootTaskID, childTaskID modulecore.TaskID) domaintask.SharedRoleContext {
	l.mu.RLock()
	shared, ok := l.rootContexts[rootTaskID]
	l.mu.RUnlock()
	if !ok {
		return domaintask.SharedRoleContext{TaskID: childTaskID}
	}
	return cloneSharedRoleContext(shared, childTaskID)
}

func (l *taskLifecycle) deleteRootContext(rootTaskID modulecore.TaskID) {
	if l == nil {
		return
	}
	l.mu.Lock()
	delete(l.rootContexts, rootTaskID)
	l.mu.Unlock()
}

func cloneSharedRoleContext(shared domaintask.SharedRoleContext, taskID modulecore.TaskID) domaintask.SharedRoleContext {
	shared.TaskID = taskID
	shared.RelevantFiles = append([]string(nil), shared.RelevantFiles...)
	shared.Decisions = append([]string(nil), shared.Decisions...)
	shared.Constraints = append([]string(nil), shared.Constraints...)
	shared.Artifacts = append([]string(nil), shared.Artifacts...)
	return shared
}
