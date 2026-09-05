package orchestrator

import (
	"context"
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

// TaskLifecycleManager is the minimal task-manager contract required by the
// orchestrator lifecycle.  It intentionally excludes query and store methods;
// the orchestrator only creates, routes, assigns, starts, and finishes work.
type TaskLifecycleManager interface {
	Create(context.Context, domaintask.Task, domaintask.SharedRoleContext) (domaintask.Task, error)
	Start(context.Context, modulecore.TaskID) (domaintask.Task, error)
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
	rootTaskID     modulecore.TaskID
	traceID        modulecore.TraceID
	routingEventID modulecore.EventID
	route          routing.Route
	actorTasks     map[string]modulecore.TaskID
	cleanups       []func()
}

func newTaskLifecycleActivation(lifecycle *taskLifecycle, events taskLifecycleEventPort, req ProcessMessageRequest) (*taskLifecycleActivation, func()) {
	activation := &taskLifecycleActivation{
		lifecycle:  lifecycle,
		events:     events,
		req:        req,
		rootTaskID: modulecore.TaskID(req.RootTaskID),
		traceID:    modulecore.TraceID(req.TraceID),
		actorTasks: make(map[string]modulecore.TaskID),
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
	a.actorTasks[actor] = taskID
	return taskID, nil
}

func newTaskLifecycle(manager TaskLifecycleManager) *taskLifecycle {
	return &taskLifecycle{
		manager:      manager,
		rootContexts: make(map[modulecore.TaskID]domaintask.SharedRoleContext),
	}
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
