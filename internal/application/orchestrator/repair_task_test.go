package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestRepairTargetRouteUsesExplicitCoderSlot(t *testing.T) {
	for input, want := range map[string]routing.Route{
		"CODE1": routing.RouteCODE1,
		"CODE2": routing.RouteCODE2,
		"code3": routing.RouteCODE3,
		"CODE4": routing.RouteCODE4,
		"CHAT":  routing.RouteCODE2,
		"":      routing.RouteCODE2,
	} {
		if got := repairTargetRoute(input); got != want {
			t.Fatalf("repairTargetRoute(%q)=%s want=%s", input, got, want)
		}
	}
}

func TestRepairTurnInputUsesOwnerTaskIDAsRoot(t *testing.T) {
	sessionID := string(modulecore.NewSessionID())
	taskID := modulecore.NewTaskID()
	got, err := repairTurnInput(ProcessRepairRequest{
		TaskID:    taskID,
		SessionID: sessionID,
	}, routing.RouteCODE2)
	if err != nil {
		t.Fatalf("repairTurnInput() error = %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("repair turn input validation failed: %v", err)
	}
	if got.RootTaskID().Validate() != nil || got.TurnID().Validate() != nil || got.TraceID().Validate() != nil || got.UserMessageID().Validate() != nil || got.AgentMessageID().Validate() != nil {
		t.Fatalf("repair turn input has invalid canonical identity: %#v", got)
	}
	if got.RootTaskID() != taskID {
		t.Fatalf("root TaskID = %q, want owner TaskID %q", got.RootTaskID(), taskID)
	}
	if got.MessageText() == "" || !strings.Contains(got.MessageText(), "Repair Task") {
		t.Fatalf("repair message = %q", got.MessageText())
	}
	if got.SessionID() != sessionID {
		t.Fatalf("repair turn input session=%q, want %q", got.SessionID(), sessionID)
	}
	address := got.ChannelAddress()
	if address.ChannelType() != "viewer" || address.ExternalConversationID() != "repair" {
		t.Fatalf("repair turn input address = %#v", address)
	}
	if got.Route() != routing.RouteCODE2 || got.HasForcedRoute() {
		t.Fatalf("repair turn input route=%q forced=%t", got.Route(), got.HasForcedRoute())
	}
}

func TestTaskLifecycleStartsRepairWithExactRunAndActualShiroOwner(t *testing.T) {
	manager := newRecordingTaskLifecycleManager()
	lifecycle := newTaskLifecycle(manager)
	events := newRecordingTaskLifecycleEventPort()
	taskID := modulecore.NewTaskID()
	req := normalizeRepairProcessRequest(ProcessRepairRequest{
		TaskID:      taskID,
		Reason:      "storage recovery",
		Instruction: "inspect the canonical route",
		TargetRoute: "CODE2",
	})

	run, err := lifecycle.startRepairExecution(context.Background(), events, req, routing.RouteCODE2)
	if err != nil {
		t.Fatalf("startRepairExecution() error = %v", err)
	}
	if err := run.Validate(); err != nil {
		t.Fatalf("repair Run is invalid: %v", err)
	}
	if run.TaskID != taskID || run.Assignee != taskLifecycleShiro || run.Status != domaintask.RunStatusRunning {
		t.Fatalf("repair Run = %#v, want task=%s assignee=%s running", run, taskID, taskLifecycleShiro)
	}
	task, err := manager.Get(context.Background(), taskID)
	if err != nil {
		t.Fatalf("Get repair Task failed: %v", err)
	}
	if task.Route != domaintask.RouteCODE2 || task.OwnerID != taskLifecycleMio || task.Assignee != taskLifecycleShiro || task.Status != domaintask.StatusRunning {
		t.Fatalf("repair Task = %#v", task)
	}
	if len(events.events) != 2 || events.events[0].Type != "routing.decision" || events.events[1].Type != "agent.assignment" || events.events[1].To != taskLifecycleShiro {
		t.Fatalf("repair lifecycle events = %#v", events.events)
	}
	if _, err := lifecycle.startRepairExecution(context.Background(), events, req, routing.RouteCODE2); err == nil {
		t.Fatal("existing repair Task was accepted")
	}
}

type cancelAwareRepairTaskLifecycleManager struct {
	*recordingTaskLifecycleManager
}

func (m *cancelAwareRepairTaskLifecycleManager) Fail(ctx context.Context, taskID modulecore.TaskID, summary string, nextActions []string) (domaintask.Task, error) {
	if err := ctx.Err(); err != nil {
		return domaintask.Task{}, err
	}
	return m.recordingTaskLifecycleManager.Fail(ctx, taskID, summary, nextActions)
}

func repairProcessTestRequest() ProcessRepairRequest {
	return normalizeRepairProcessRequest(ProcessRepairRequest{
		TaskID:      modulecore.NewTaskID(),
		Reason:      "repair event failure",
		Instruction: "verify the canonical repair route",
		TargetRoute: "CODE2",
	})
}

func TestProcessRepairStopsBeforeDispatchOnRoutingPublicationFailure(t *testing.T) {
	wantErr := errors.New("routing append unavailable")
	listener := &failOnEventListener{failType: "routing.decision", err: wantErr}
	events := newMessageEventPort(listener)
	base := newRecordingTaskLifecycleManager()
	manager := &cancelAwareRepairTaskLifecycleManager{recordingTaskLifecycleManager: base}
	req := repairProcessTestRequest()
	dispatched := false

	_, err := processRepair(
		context.Background(), req, routing.RouteCODE2, newTaskLifecycle(manager), events, events.publicationFail,
		func(context.Context, conversation.TurnInput, routing.Route, modulecore.TaskID, modulecore.RunID) (string, error) {
			dispatched = true
			return "must not dispatch", nil
		},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("processRepair() error = %v, want %v", err, wantErr)
	}
	if dispatched {
		t.Fatal("repair dispatch ran after routing publication failure")
	}
	task, getErr := manager.Get(context.Background(), req.TaskID)
	if getErr != nil {
		t.Fatalf("Get() error = %v", getErr)
	}
	if task.Status != domaintask.StatusFailed {
		t.Fatalf("startup-failed repair task status = %s, want failed", task.Status)
	}
}

func TestProcessRepairStopsBeforeDispatchOnDispatchPublicationFailure(t *testing.T) {
	wantErr := errors.New("dispatch append unavailable")
	listener := &failOnEventListener{failType: "repair.dispatch", err: wantErr}
	events := newMessageEventPort(listener)
	manager := newRecordingTaskLifecycleManager()
	req := repairProcessTestRequest()
	dispatched := false

	_, err := processRepair(
		context.Background(), req, routing.RouteCODE2, newTaskLifecycle(manager), events, events.publicationFail,
		func(context.Context, conversation.TurnInput, routing.Route, modulecore.TaskID, modulecore.RunID) (string, error) {
			dispatched = true
			return "must not dispatch", nil
		},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("processRepair() error = %v, want %v", err, wantErr)
	}
	if dispatched {
		t.Fatal("repair dispatch ran after dispatch publication failure")
	}
	assertRepairTaskAndRunFailed(t, manager, req.TaskID)
}

func TestProcessRepairSuppressesSuccessWhenCompletionPublicationFails(t *testing.T) {
	wantErr := errors.New("completion append unavailable")
	listener := &failOnEventListener{failType: "repair.completed", err: wantErr}
	events := newMessageEventPort(listener)
	manager := newRecordingTaskLifecycleManager()
	req := repairProcessTestRequest()
	dispatchCalls := 0

	resp, err := processRepair(
		context.Background(), req, routing.RouteCODE2, newTaskLifecycle(manager), events, events.publicationFail,
		func(context.Context, conversation.TurnInput, routing.Route, modulecore.TaskID, modulecore.RunID) (string, error) {
			dispatchCalls++
			return "repair response", nil
		},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("processRepair() error = %v, want %v", err, wantErr)
	}
	if dispatchCalls != 1 {
		t.Fatalf("dispatch calls = %d, want 1", dispatchCalls)
	}
	if resp.Response != "" {
		t.Fatalf("success response escaped completion publication failure: %#v", resp)
	}
	assertRepairTaskAndRunFailed(t, manager, req.TaskID)
}

func assertRepairTaskAndRunFailed(t *testing.T, manager *recordingTaskLifecycleManager, taskID modulecore.TaskID) {
	t.Helper()
	task, err := manager.Get(context.Background(), taskID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if task.Status != domaintask.StatusFailed {
		t.Fatalf("repair task status = %s, want failed", task.Status)
	}
	runs, err := manager.ListRuns(context.Background(), domaintask.RunFilter{TaskID: taskID})
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	if len(runs) != 1 || runs[0].Status != domaintask.RunStatusFailed {
		t.Fatalf("repair runs = %#v, want one failed run", runs)
	}
}
