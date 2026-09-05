package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestTaskLifecycleCreatesFixedRootAndBoundedUserIntent(t *testing.T) {
	manager := newRecordingTaskLifecycleManager()
	lifecycle := newTaskLifecycle(manager)
	rootID := modulecore.NewTaskID()
	sessionID := modulecore.NewSessionID()
	turnID := modulecore.NewTurnID()
	messageID := modulecore.NewMessageID()
	message := strings.Repeat("あ", 170)
	req := ProcessMessageRequest{
		RootTaskID:  string(rootID),
		SessionID:   string(sessionID),
		TurnID:      string(turnID),
		MessageID:   string(messageID),
		UserMessage: "  " + message + "  ",
	}

	created, err := lifecycle.createRoot(context.Background(), req)
	if err != nil {
		t.Fatalf("createRoot: %v", err)
	}
	if created.TaskID != rootID || created.OwnerID != "mio" || created.Route != domaintask.RouteGeneral || created.Status != domaintask.StatusQueued {
		t.Fatalf("root = %#v", created)
	}
	if got, want := []rune(created.Title), []rune(message)[:160]; string(got) != string(want) {
		t.Fatalf("bounded title length/value = %d/%q, want %d/%q", len(got), string(got), len(want), string(want))
	}
	if created.OriginSessionID != sessionID || created.OriginTurnID != turnID || created.OriginMessageID != messageID {
		t.Fatalf("origin IDs = session=%q turn=%q message=%q", created.OriginSessionID, created.OriginTurnID, created.OriginMessageID)
	}
	shared := manager.contexts[rootID]
	if shared.TaskID != rootID || shared.UserIntent != strings.TrimSpace(req.UserMessage) {
		t.Fatalf("root shared context = %#v", shared)
	}
}

func TestTaskLifecycleIgnoresMalformedOptionalOriginIDs(t *testing.T) {
	manager := newRecordingTaskLifecycleManager()
	lifecycle := newTaskLifecycle(manager)
	rootID := modulecore.NewTaskID()
	created, err := lifecycle.createRoot(context.Background(), ProcessMessageRequest{
		RootTaskID:  string(rootID),
		SessionID:   "not-a-session",
		TurnID:      "not-a-turn",
		MessageID:   "not-a-message",
		UserMessage: "request",
	})
	if err != nil {
		t.Fatalf("createRoot: %v", err)
	}
	if created.OriginSessionID != "" || created.OriginTurnID != "" || created.OriginMessageID != "" {
		t.Fatalf("malformed optional origins were persisted: %#v", created)
	}
}

func TestTaskLifecyclePreparesChildAndRunsRootThenChild(t *testing.T) {
	manager := newRecordingTaskLifecycleManager()
	lifecycle := newTaskLifecycle(manager)
	rootID := modulecore.NewTaskID()
	root, err := lifecycle.createRoot(context.Background(), ProcessMessageRequest{RootTaskID: string(rootID), UserMessage: "implement feature"})
	if err != nil {
		t.Fatalf("createRoot: %v", err)
	}
	routingEventID := modulecore.NewEventID()
	childID, err := lifecycle.prepareExecution(context.Background(), root.TaskID, routing.RouteCODE2, routingEventID, "ShIrO")
	if err != nil {
		t.Fatalf("prepareExecution: %v", err)
	}
	if childID == root.TaskID {
		t.Fatal("non-Mio execution reused root task")
	}
	child := manager.tasks[childID]
	if child.ParentTaskID != root.TaskID || child.Route != domaintask.RouteCODE2 || child.OwnerID != "mio" || child.Assignee != "shiro" || child.RoutingEventID != routingEventID || child.Status != domaintask.StatusQueued {
		t.Fatalf("child = %#v", child)
	}
	childContext := manager.contexts[childID]
	if childContext.TaskID != childID || childContext.UserIntent != strings.TrimSpace("implement feature") {
		t.Fatalf("child shared context = %#v", childContext)
	}

	assignmentEventID := modulecore.NewEventID()
	if err := lifecycle.recordAssignmentAndStart(context.Background(), root.TaskID, childID, "SHIRO", assignmentEventID); err != nil {
		t.Fatalf("recordAssignmentAndStart: %v", err)
	}
	if got, want := manager.calls, []string{"Create", "RecordRouting", "Create", "RecordAssignment", "Start", "Start"}; !equalStrings(got, want) {
		t.Fatalf("calls = %#v, want %#v", got, want)
	}
	if manager.tasks[root.TaskID].Status != domaintask.StatusRunning || manager.tasks[childID].Status != domaintask.StatusRunning {
		t.Fatalf("started tasks = root=%s child=%s", manager.tasks[root.TaskID].Status, manager.tasks[childID].Status)
	}

	if err := lifecycle.finish(context.Background(), root.TaskID, childID, "done", nil); err != nil {
		t.Fatalf("finish success: %v", err)
	}
	if manager.tasks[childID].Status != domaintask.StatusSucceeded || manager.tasks[root.TaskID].Status != domaintask.StatusSucceeded {
		t.Fatalf("finished tasks = root=%s child=%s", manager.tasks[root.TaskID].Status, manager.tasks[childID].Status)
	}
	if _, ok := lifecycle.rootContexts[root.TaskID]; ok {
		t.Fatalf("root context was not released after child success")
	}
}

func TestTaskLifecycleMioUsesRootAndStartsOnce(t *testing.T) {
	manager := newRecordingTaskLifecycleManager()
	lifecycle := newTaskLifecycle(manager)
	rootID := modulecore.NewTaskID()
	root, err := lifecycle.createRoot(context.Background(), ProcessMessageRequest{RootTaskID: string(rootID), UserMessage: "chat"})
	if err != nil {
		t.Fatalf("createRoot: %v", err)
	}
	executionID, err := lifecycle.prepareExecution(context.Background(), root.TaskID, routing.RouteCHAT, modulecore.NewEventID(), "MIO")
	if err != nil {
		t.Fatalf("prepareExecution: %v", err)
	}
	if executionID != root.TaskID {
		t.Fatalf("execution ID = %s, want root %s", executionID, root.TaskID)
	}
	if err := lifecycle.recordAssignmentAndStart(context.Background(), root.TaskID, executionID, "mio", modulecore.NewEventID()); err != nil {
		t.Fatalf("recordAssignmentAndStart: %v", err)
	}
	if got, want := manager.calls, []string{"Create", "RecordRouting", "RecordAssignment", "Start"}; !equalStrings(got, want) {
		t.Fatalf("calls = %#v, want %#v", got, want)
	}
	if err := lifecycle.finish(context.Background(), root.TaskID, executionID, "hello", nil); err != nil {
		t.Fatalf("finish success: %v", err)
	}
	if manager.tasks[root.TaskID].Status != domaintask.StatusSucceeded {
		t.Fatalf("root status = %s", manager.tasks[root.TaskID].Status)
	}
	if _, ok := lifecycle.rootContexts[root.TaskID]; ok {
		t.Fatalf("root context was not released after success")
	}
}

func TestTaskLifecycleReturnsDistinctActiveRunsForRootAndChild(t *testing.T) {
	ctx := context.Background()
	manager := newRecordingTaskLifecycleManager()
	lifecycle := newTaskLifecycle(manager)
	rootID := modulecore.NewTaskID()
	root, err := lifecycle.createRoot(ctx, ProcessMessageRequest{RootTaskID: string(rootID), UserMessage: "run root and child"})
	if err != nil {
		t.Fatalf("createRoot: %v", err)
	}
	childID, err := lifecycle.createExecutionChild(ctx, root.TaskID, routing.RouteOPS, modulecore.NewEventID(), "shiro")
	if err != nil {
		t.Fatalf("createExecutionChild: %v", err)
	}
	if err := lifecycle.recordAssignmentAndStart(ctx, root.TaskID, root.TaskID, "mio", modulecore.NewEventID()); err != nil {
		t.Fatalf("start root: %v", err)
	}
	if err := lifecycle.recordChildAssignmentAndStart(ctx, childID, "shiro", modulecore.NewEventID()); err != nil {
		t.Fatalf("start child: %v", err)
	}

	rootRun, err := lifecycle.activeRunForTask(ctx, rootID)
	if err != nil {
		t.Fatalf("active root run: %v", err)
	}
	childRun, err := lifecycle.activeRunForTask(ctx, childID)
	if err != nil {
		t.Fatalf("active child run: %v", err)
	}
	if rootRun.RunID == childRun.RunID || rootRun.TaskID != rootID || childRun.TaskID != childID {
		t.Fatalf("root/child runs = %#v / %#v", rootRun, childRun)
	}
	if rootRun.Status != domaintask.RunStatusRunning || childRun.Status != domaintask.RunStatusRunning {
		t.Fatalf("root/child statuses = %s / %s", rootRun.Status, childRun.Status)
	}
}

func TestTaskLifecycleActiveRunFailsClosedForMissingMultipleAndWrongRuns(t *testing.T) {
	ctx := context.Background()
	manager := newRecordingTaskLifecycleManager()
	lifecycle := newTaskLifecycle(manager)
	taskID := modulecore.NewTaskID()
	if _, err := lifecycle.createRoot(ctx, ProcessMessageRequest{RootTaskID: string(taskID), UserMessage: "run query"}); err != nil {
		t.Fatalf("createRoot: %v", err)
	}
	valid := lifecycleTestRun(taskID, "mio", time.Date(2026, 9, 5, 1, 0, 0, 0, time.UTC))
	tests := []struct {
		name string
		runs []domaintask.Run
	}{
		{name: "missing"},
		{name: "multiple", runs: []domaintask.Run{valid, lifecycleTestRun(taskID, "mio", valid.StartedAt.Add(time.Second))}},
		{name: "wrong ownership", runs: []domaintask.Run{lifecycleTestRun(modulecore.NewTaskID(), "mio", valid.StartedAt)}},
		{name: "wrong status", runs: []domaintask.Run{func() domaintask.Run {
			closed := valid
			completedAt := valid.StartedAt.Add(time.Minute)
			closed.Status = domaintask.RunStatusSucceeded
			closed.CompletedAt = &completedAt
			return closed
		}()}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager.runs = make(map[modulecore.RunID]domaintask.Run, len(test.runs))
			for _, run := range test.runs {
				manager.runs[run.RunID] = run
			}
			if _, err := lifecycle.activeRunForTask(ctx, taskID); err == nil {
				t.Fatal("invalid active run set was accepted")
			}
		})
	}
}

func lifecycleTestRun(taskID modulecore.TaskID, assignee string, startedAt time.Time) domaintask.Run {
	return domaintask.Run{
		RunID:       modulecore.NewRunID(),
		TaskID:      taskID,
		StartReason: domaintask.RunStartReasonFirst,
		Assignee:    assignee,
		Status:      domaintask.RunStatusRunning,
		StartedAt:   startedAt,
	}
}

func TestTaskLifecycleActivationReusesRootRouteAndAddsActualShiroChild(t *testing.T) {
	manager := newRecordingTaskLifecycleManager()
	lifecycle := newTaskLifecycle(manager)
	rootID := modulecore.NewTaskID()
	traceID := modulecore.NewTraceID()
	messageID := modulecore.NewMessageID()
	req := ProcessMessageRequest{
		RootTaskID:     string(rootID),
		TraceID:        string(traceID),
		AgentMessageID: string(messageID),
		SessionID:      "activation-session",
		Channel:        "viewer",
		ChatID:         "ren",
		UserMessage:    "今朝のニュースを教えて",
	}
	if _, err := lifecycle.createRoot(context.Background(), req); err != nil {
		t.Fatalf("createRoot: %v", err)
	}
	events := newRecordingTaskLifecycleEventPort()
	events.BindTrace(rootID.String(), traceID)
	activation, cleanup := newTaskLifecycleActivation(lifecycle, events, req)

	mioTaskID, err := activation.Activate(context.Background(), routing.RouteCHAT, "mio", "daily news brief")
	if err != nil {
		t.Fatalf("activate Mio: %v", err)
	}
	shiroTaskID, err := activation.Activate(context.Background(), routing.RouteCHAT, "shiro", "ignored second route content")
	if err != nil {
		t.Fatalf("activate Shiro: %v", err)
	}
	if mioTaskID != rootID || shiroTaskID == rootID {
		t.Fatalf("activation tasks = Mio %s Shiro %s root %s", mioTaskID, shiroTaskID, rootID)
	}
	if got, want := manager.calls, []string{"Create", "RecordRouting", "RecordAssignment", "Start", "Create", "RecordAssignment", "Start"}; !equalStrings(got, want) {
		t.Fatalf("lifecycle calls = %#v, want %#v", got, want)
	}
	if manager.tasks[rootID].Assignee != "mio" || manager.tasks[shiroTaskID].Assignee != "shiro" || manager.tasks[shiroTaskID].ParentTaskID != rootID {
		t.Fatalf("root/child assignment = %#v / %#v", manager.tasks[rootID], manager.tasks[shiroTaskID])
	}
	if len(events.events) != 3 || events.events[0].Type != "routing.decision" || events.events[1].Type != "agent.assignment" || events.events[2].Type != "agent.assignment" {
		t.Fatalf("activation events = %#v", events.events)
	}
	for _, assignment := range events.events[1:] {
		if assignment.CausationEventID != events.events[0].EventID {
			t.Fatalf("assignment causation = %s, want %s", assignment.CausationEventID, events.events[0].EventID)
		}
	}
	if events.events[0].TaskID != rootID || events.events[2].TaskID != shiroTaskID || events.events[2].TraceID != traceID {
		t.Fatalf("event identities = %#v", events.events)
	}

	cleanup()
	if _, ok := events.traces[shiroTaskID]; ok {
		t.Fatalf("child trace binding was not released")
	}
}

func TestTaskLifecycleActivationReturnsCreatedChildWhenAssignmentPublicationFails(t *testing.T) {
	manager := newRecordingTaskLifecycleManager()
	lifecycle := newTaskLifecycle(manager)
	rootID := modulecore.NewTaskID()
	req := ProcessMessageRequest{RootTaskID: string(rootID), TraceID: string(modulecore.NewTraceID()), UserMessage: "run operation"}
	if _, err := lifecycle.createRoot(context.Background(), req); err != nil {
		t.Fatalf("createRoot: %v", err)
	}
	events := newRecordingTaskLifecycleEventPort()
	events.failType = "agent.assignment"
	wantErr := errors.New("event store unavailable")
	events.failErr = wantErr
	activation, cleanup := newTaskLifecycleActivation(lifecycle, events, req)
	defer cleanup()

	childID, err := activation.Activate(context.Background(), routing.RouteOPS, "shiro", "ops")
	if !errors.Is(err, wantErr) {
		t.Fatalf("Activate error = %v, want %v", err, wantErr)
	}
	if childID == "" || childID == rootID || manager.tasks[childID].ParentTaskID != rootID {
		t.Fatalf("created child = %q task=%#v", childID, manager.tasks[childID])
	}
	if finishErr := lifecycle.finish(context.Background(), rootID, childID, "", err); finishErr != nil {
		t.Fatalf("finish after publication failure: %v", finishErr)
	}
	if manager.tasks[rootID].Status != domaintask.StatusFailed || manager.tasks[childID].Status != domaintask.StatusFailed {
		t.Fatalf("terminal tasks = root %s child %s", manager.tasks[rootID].Status, manager.tasks[childID].Status)
	}
}

func TestTaskLifecycleFailsQueuedRootAndChildOnExecutionError(t *testing.T) {
	manager := newRecordingTaskLifecycleManager()
	lifecycle := newTaskLifecycle(manager)
	rootID := modulecore.NewTaskID()
	root, err := lifecycle.createRoot(context.Background(), ProcessMessageRequest{RootTaskID: string(rootID), UserMessage: "fail"})
	if err != nil {
		t.Fatalf("createRoot: %v", err)
	}
	childID, err := lifecycle.prepareExecution(context.Background(), root.TaskID, routing.RouteOPS, modulecore.NewEventID(), "shiro")
	if err != nil {
		t.Fatalf("prepareExecution: %v", err)
	}
	original := fmt.Errorf("worker unavailable")
	if err := lifecycle.finish(context.Background(), root.TaskID, childID, "ignored", original); err != nil {
		t.Fatalf("finish failure: %v", err)
	}
	if manager.tasks[childID].Status != domaintask.StatusFailed || manager.tasks[root.TaskID].Status != domaintask.StatusFailed {
		t.Fatalf("failed tasks = root=%s child=%s", manager.tasks[root.TaskID].Status, manager.tasks[childID].Status)
	}
	if manager.tasks[childID].Summary != original.Error() || manager.tasks[root.TaskID].Summary != original.Error() {
		t.Fatalf("failure summaries = root=%q child=%q", manager.tasks[root.TaskID].Summary, manager.tasks[childID].Summary)
	}
	if _, ok := lifecycle.rootContexts[root.TaskID]; ok {
		t.Fatalf("root context was not released after failure")
	}
}

func TestTaskLifecycleFinishCleansContextWhenManagerFails(t *testing.T) {
	manager := newRecordingTaskLifecycleManager()
	manager.finishErr = errors.New("task store unavailable")
	lifecycle := newTaskLifecycle(manager)
	rootID := modulecore.NewTaskID()
	root, err := lifecycle.createRoot(context.Background(), ProcessMessageRequest{RootTaskID: string(rootID), UserMessage: "already terminal"})
	if err != nil {
		t.Fatalf("createRoot: %v", err)
	}
	value := manager.tasks[root.TaskID]
	value.Status = domaintask.StatusSucceeded
	manager.tasks[root.TaskID] = value
	if err := lifecycle.finish(context.Background(), root.TaskID, root.TaskID, "ignored", nil); err == nil {
		t.Fatal("finish unexpectedly succeeded for terminal root")
	}
	if _, ok := lifecycle.rootContexts[root.TaskID]; ok {
		t.Fatalf("root context was not released after manager error")
	}
}

func TestActualCoreActorForRoute(t *testing.T) {
	tests := []struct {
		route     routing.Route
		recipient string
		want      string
	}{
		{route: routing.RouteCHAT, recipient: " KURO ", want: "kuro"},
		{route: routing.RoutePLAN, want: "mio"},
		{route: routing.RouteRESEARCH, want: "mio"},
		{route: routing.RouteOPS, want: "shiro"},
		{route: routing.RouteCODE, want: "shiro"},
		{route: routing.RouteCODE1, want: "shiro"},
		{route: routing.RouteCODE2, want: "shiro"},
		{route: routing.RouteCODE3, want: "shiro"},
		{route: routing.RouteCODE4, want: "shiro"},
		{route: routing.RouteWILD, want: "midori"},
		{route: routing.RouteANALYZE, want: "kuro"},
	}
	for _, tt := range tests {
		t.Run(string(tt.route), func(t *testing.T) {
			got, err := actualCoreActorForRoute(tt.route, tt.recipient)
			if err != nil {
				t.Fatalf("actualCoreActorForRoute: %v", err)
			}
			if got != tt.want {
				t.Fatalf("actor = %q, want %q", got, tt.want)
			}
		})
	}
	for _, route := range []routing.Route{"", routing.Route("UNKNOWN")} {
		if _, err := actualCoreActorForRoute(route, "mio"); err == nil {
			t.Fatalf("unknown route %q was accepted", route)
		}
	}
	if _, err := actualCoreActorForRoute(routing.RouteCHAT, "coder1"); err == nil {
		t.Fatal("unknown Viewer recipient was accepted")
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

type recordingTaskLifecycleManager struct {
	tasks     map[modulecore.TaskID]domaintask.Task
	contexts  map[modulecore.TaskID]domaintask.SharedRoleContext
	runs      map[modulecore.RunID]domaintask.Run
	calls     []string
	callIDs   []string
	finishErr error
}

func newRecordingTaskLifecycleManager() *recordingTaskLifecycleManager {
	return &recordingTaskLifecycleManager{
		tasks:    make(map[modulecore.TaskID]domaintask.Task),
		contexts: make(map[modulecore.TaskID]domaintask.SharedRoleContext),
		runs:     make(map[modulecore.RunID]domaintask.Run),
	}
}

func (m *recordingTaskLifecycleManager) Create(_ context.Context, draft domaintask.Task, shared domaintask.SharedRoleContext) (domaintask.Task, error) {
	m.calls = append(m.calls, "Create")
	draft.ApplyDefaults(time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC))
	if err := draft.Validate(); err != nil {
		return domaintask.Task{}, err
	}
	if shared.TaskID == "" {
		shared.TaskID = draft.TaskID
	}
	m.tasks[draft.TaskID] = draft
	m.contexts[draft.TaskID] = shared
	return draft, nil
}

func (m *recordingTaskLifecycleManager) Start(_ context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	m.calls = append(m.calls, "Start")
	m.callIDs = append(m.callIDs, "Start:"+taskID.String())
	task, ok := m.tasks[taskID]
	if !ok {
		return domaintask.Task{}, domaintask.ErrNotFound
	}
	if !domaintask.CanTransition(task.Status, domaintask.StatusRunning) {
		return domaintask.Task{}, fmt.Errorf("cannot start %s from %s", taskID, task.Status)
	}
	task.Status = domaintask.StatusRunning
	m.tasks[taskID] = task
	if m.runs == nil {
		m.runs = make(map[modulecore.RunID]domaintask.Run)
	}
	startedAt := task.UpdatedAt
	if startedAt.IsZero() {
		startedAt = time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	}
	for runID, run := range m.runs {
		if run.TaskID != taskID || run.Status != domaintask.RunStatusRunning {
			continue
		}
		completedAt := startedAt
		run.Status = domaintask.RunStatusInterrupted
		run.CompletedAt = &completedAt
		m.runs[runID] = run
	}
	reason := domaintask.RunStartReasonFirst
	for _, run := range m.runs {
		if run.TaskID == taskID {
			reason = domaintask.RunStartReasonExplicitRerun
			break
		}
	}
	run := lifecycleTestRun(taskID, task.Assignee, startedAt)
	run.StartReason = reason
	m.runs[run.RunID] = run
	return task, nil
}

func (m *recordingTaskLifecycleManager) Succeed(_ context.Context, taskID modulecore.TaskID, summary string) (domaintask.Task, error) {
	m.calls = append(m.calls, "Succeed")
	m.callIDs = append(m.callIDs, "Succeed:"+taskID.String())
	return m.finish(taskID, domaintask.StatusSucceeded, summary)
}

func (m *recordingTaskLifecycleManager) Fail(_ context.Context, taskID modulecore.TaskID, summary string, _ []string) (domaintask.Task, error) {
	m.calls = append(m.calls, "Fail")
	m.callIDs = append(m.callIDs, "Fail:"+taskID.String())
	return m.finish(taskID, domaintask.StatusFailed, summary)
}

func (m *recordingTaskLifecycleManager) finish(taskID modulecore.TaskID, status domaintask.Status, summary string) (domaintask.Task, error) {
	if m.finishErr != nil {
		return domaintask.Task{}, m.finishErr
	}
	task, ok := m.tasks[taskID]
	if !ok {
		return domaintask.Task{}, domaintask.ErrNotFound
	}
	if !domaintask.CanTransition(task.Status, status) {
		return domaintask.Task{}, fmt.Errorf("cannot finish %s from %s", taskID, task.Status)
	}
	task.Status = status
	task.Summary = summary
	m.tasks[taskID] = task
	if m.runs != nil {
		runStatus := domaintask.RunStatusSucceeded
		if status == domaintask.StatusFailed {
			runStatus = domaintask.RunStatusFailed
		}
		completedAt := task.UpdatedAt
		for runID, run := range m.runs {
			if run.TaskID != taskID || run.Status != domaintask.RunStatusRunning {
				continue
			}
			closed, err := run.Close(runStatus, completedAt, summary)
			if err != nil {
				return domaintask.Task{}, err
			}
			m.runs[runID] = closed
		}
	}
	return task, nil
}

func (m *recordingTaskLifecycleManager) ListRuns(_ context.Context, filter domaintask.RunFilter) ([]domaintask.Run, error) {
	if filter.TaskID != "" {
		if err := filter.TaskID.Validate(); err != nil {
			return nil, err
		}
	}
	if filter.Status != "" && !domaintask.ValidRunStatus(filter.Status) {
		return nil, fmt.Errorf("invalid run status: %s", filter.Status)
	}
	items := make([]domaintask.Run, 0, len(m.runs))
	for _, run := range m.runs {
		if filter.TaskID != "" && run.TaskID != filter.TaskID {
			continue
		}
		if filter.Status != "" && run.Status != filter.Status {
			continue
		}
		items = append(items, run)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].StartedAt.Equal(items[j].StartedAt) {
			return string(items[i].RunID) < string(items[j].RunID)
		}
		return items[i].StartedAt.Before(items[j].StartedAt)
	})
	if filter.Limit > 0 && len(items) > filter.Limit {
		items = items[:filter.Limit]
	}
	return items, nil
}

func (m *recordingTaskLifecycleManager) RecordRouting(_ context.Context, taskID modulecore.TaskID, route domaintask.Route, eventID modulecore.EventID) (domaintask.Task, error) {
	m.calls = append(m.calls, "RecordRouting")
	task, ok := m.tasks[taskID]
	if !ok {
		return domaintask.Task{}, domaintask.ErrNotFound
	}
	task.Route = route
	task.RoutingEventID = eventID
	m.tasks[taskID] = task
	return task, nil
}

func (m *recordingTaskLifecycleManager) RecordAssignment(_ context.Context, taskID modulecore.TaskID, assignee string, eventID modulecore.EventID) (domaintask.Task, error) {
	m.calls = append(m.calls, "RecordAssignment")
	task, ok := m.tasks[taskID]
	if !ok {
		return domaintask.Task{}, domaintask.ErrNotFound
	}
	task.Assignee = assignee
	task.AssignmentEventID = eventID
	m.tasks[taskID] = task
	return task, nil
}

var _ TaskLifecycleManager = (*recordingTaskLifecycleManager)(nil)

type recordingTaskLifecycleEventPort struct {
	events    []OrchestratorEvent
	traces    map[modulecore.TaskID]modulecore.TraceID
	responses map[modulecore.TaskID]modulecore.MessageID
	failType  string
	failErr   error
}

func newRecordingTaskLifecycleEventPort() *recordingTaskLifecycleEventPort {
	return &recordingTaskLifecycleEventPort{
		traces:    make(map[modulecore.TaskID]modulecore.TraceID),
		responses: make(map[modulecore.TaskID]modulecore.MessageID),
	}
}

func (p *recordingTaskLifecycleEventPort) Publish(eventType, from, to, content, route, taskID, sessionID, channel, chatID string, causationEventID modulecore.EventID, dependencyEventIDs []modulecore.EventID) (OrchestratorEvent, error) {
	typedTaskID := modulecore.TaskID(taskID)
	event := OrchestratorEvent{
		EventID:            modulecore.NewEventID(),
		TraceID:            p.traces[typedTaskID],
		Type:               eventType,
		From:               from,
		To:                 to,
		Content:            content,
		Route:              route,
		TaskID:             typedTaskID,
		SessionID:          modulecore.SessionID(sessionID),
		Channel:            channel,
		ChatID:             chatID,
		CausationEventID:   causationEventID,
		DependencyEventIDs: append([]modulecore.EventID(nil), dependencyEventIDs...),
	}
	p.events = append(p.events, event)
	if eventType == p.failType {
		return event, p.failErr
	}
	return event, nil
}

func (p *recordingTaskLifecycleEventPort) BindTrace(taskID string, traceID modulecore.TraceID) {
	p.traces[modulecore.TaskID(taskID)] = traceID
}

func (p *recordingTaskLifecycleEventPort) ReleaseTrace(taskID string) {
	delete(p.traces, modulecore.TaskID(taskID))
}

func (p *recordingTaskLifecycleEventPort) BindResponseMessageID(taskID string, messageID modulecore.MessageID) {
	p.responses[modulecore.TaskID(taskID)] = messageID
}

func (p *recordingTaskLifecycleEventPort) ReleaseResponseMessageID(taskID string) {
	delete(p.responses, modulecore.TaskID(taskID))
}

var _ taskLifecycleEventPort = (*recordingTaskLifecycleEventPort)(nil)
