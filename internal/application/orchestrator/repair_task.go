package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type ProcessRepairRequest struct {
	TaskID      modulecore.TaskID
	SessionID   string
	Reason      string
	Instruction string
	Recent      int
	TargetRoute string
	TargetAgent string
	Source      string
}

type ProcessRepairResponse struct {
	Response string
	Route    routing.Route
	TaskID   modulecore.TaskID
	RunID    modulecore.RunID
}

func normalizeRepairProcessRequest(req ProcessRepairRequest) ProcessRepairRequest {
	req.SessionID = strings.TrimSpace(req.SessionID)
	if req.SessionID == "" {
		req.SessionID = "repair-" + req.TaskID.String()
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Reason == "" {
		req.Reason = "user-directed-repair"
	}
	req.Instruction = strings.TrimSpace(req.Instruction)
	if req.Instruction == "" {
		req.Instruction = "直近ログを見て、異常を診断し、修復案と必要な実行手順を作成してください。"
	}
	req.TargetRoute = strings.ToUpper(strings.TrimSpace(req.TargetRoute))
	if req.TargetRoute == "" {
		req.TargetRoute = "CHAT"
	}
	req.TargetAgent = strings.ToLower(strings.TrimSpace(req.TargetAgent))
	if req.TargetAgent == "" {
		req.TargetAgent = "mio"
	}
	req.Source = strings.TrimSpace(req.Source)
	if req.Source == "" {
		req.Source = "repair"
	}
	return req
}

func repairTaskMessage(req ProcessRepairRequest) string {
	return fmt.Sprintf(`Repair Task

reason: %s
target_route: %s
target_agent: %s
recent_events: %d
source: %s

Instruction:
%s

Requirements:
- Diagnose the requested RenCrow runtime problem from logs and code.
- Identify concrete existing repository files before proposing file edits.
- Do not create placeholder/example paths such as chat.go, path/to/*, sample/*, or example/* as a repair.
- Do not turn illustrative Markdown code blocks into patches; patch blocks must target real files.
- If no safe concrete code change is identified, return a diagnostic report instead of a patch.
- Produce a concrete plan and patch when code changes are needed.
- Keep Chat/Mio out of the repair intake path; report through Task events.
- Do not perform destructive operations in the Coder proposal; Worker applies executable changes.`, req.Reason, req.TargetRoute, req.TargetAgent, req.Recent, req.Source, req.Instruction)
}

func repairTargetRoute(target string) routing.Route {
	switch strings.ToUpper(strings.TrimSpace(target)) {
	case string(routing.RouteCODE1):
		return routing.RouteCODE1
	case string(routing.RouteCODE3):
		return routing.RouteCODE3
	case string(routing.RouteCODE4):
		return routing.RouteCODE4
	default:
		return routing.RouteCODE2
	}
}

func repairTurnInput(req ProcessRepairRequest, route routing.Route) (conversation.TurnInput, error) {
	address, err := conversation.NewChannelAddress("viewer", "repair")
	if err != nil {
		return conversation.TurnInput{}, err
	}
	if err := req.TaskID.Validate(); err != nil {
		return conversation.TurnInput{}, fmt.Errorf("task_id is invalid: %w", err)
	}
	input, err := conversation.NewTurnInput(req.TaskID, repairTaskMessage(req), address)
	if err != nil {
		return conversation.TurnInput{}, err
	}
	return input.WithSessionID(req.SessionID).WithRoute(route), nil
}

type repairDispatchFunc func(context.Context, conversation.TurnInput, routing.Route, modulecore.TaskID, modulecore.RunID) (string, error)

func processRepair(
	ctx context.Context,
	req ProcessRepairRequest,
	route routing.Route,
	lifecycle *taskLifecycle,
	events taskLifecycleEventPort,
	publicationFailures *eventPublicationFailureTracker,
	dispatch repairDispatchFunc,
) (resp ProcessRepairResponse, err error) {
	input, err := repairTurnInput(req, route)
	if err != nil {
		return ProcessRepairResponse{}, err
	}
	if lifecycle == nil || events == nil || publicationFailures == nil {
		return ProcessRepairResponse{}, fmt.Errorf("repair requires configured task lifecycle and event port")
	}
	if dispatch == nil {
		return ProcessRepairResponse{}, fmt.Errorf("repair dispatch is unavailable")
	}

	traceID := input.TraceID()
	events.BindTrace(req.TaskID.String(), traceID)
	defer events.ReleaseTrace(req.TaskID.String())
	ctx, cancel := context.WithCancelCause(ctx)
	if publicationFailures != nil {
		publicationFailures.Begin(traceID, cancel)
	}
	defer func() {
		var publicationErr error
		if publicationFailures != nil {
			publicationErr = publicationFailures.End(traceID)
		}
		if publicationErr == nil {
			cancel(nil)
			return
		}
		cancel(publicationErr)
		resp = ProcessRepairResponse{}
		wrapped := fmt.Errorf("canonical event publication failed: %w", publicationErr)
		if err == nil {
			err = wrapped
		} else if !errors.Is(err, publicationErr) {
			err = errors.Join(err, wrapped)
		}
	}()

	runStarted := false
	defer func() {
		if !runStarted {
			return
		}
		lifecycleErr := err
		if publicationFailures != nil {
			if publicationErr := publicationFailures.Current(traceID); publicationErr != nil {
				if lifecycleErr == nil {
					lifecycleErr = publicationErr
				} else {
					lifecycleErr = errors.Join(lifecycleErr, publicationErr)
				}
			}
		}
		if finishErr := lifecycle.finish(context.WithoutCancel(ctx), req.TaskID, req.TaskID, resp.Response, lifecycleErr); finishErr != nil {
			if err == nil {
				err = finishErr
			} else {
				err = errors.Join(err, finishErr)
			}
			resp = ProcessRepairResponse{}
		}
	}()

	run, err := lifecycle.startRepairExecution(ctx, events, req, route)
	if err != nil {
		return ProcessRepairResponse{}, err
	}
	runStarted = true
	startedAt := time.Now()
	if _, err := events.Publish(
		"repair.dispatch", "repair", "shiro", "dispatch repair Task to Coder via "+route.String(), route.String(), req.TaskID.String(), req.SessionID, "viewer", "repair", "", nil,
	); err != nil {
		return ProcessRepairResponse{}, err
	}
	response, executionErr := dispatch(ctx, input, route, req.TaskID, run.RunID)
	if executionErr != nil {
		if _, eventErr := events.Publish(
			"repair.failed", "shiro", "repair", executionErr.Error(), route.String(), req.TaskID.String(), req.SessionID, "viewer", "repair", "", nil,
		); eventErr != nil {
			executionErr = errors.Join(executionErr, eventErr)
		}
		return ProcessRepairResponse{}, executionErr
	}
	if _, err := events.Publish(
		"repair.completed", "shiro", "repair", fmt.Sprintf("repair Task completed in %s", time.Since(startedAt).Round(time.Millisecond)), route.String(), req.TaskID.String(), req.SessionID, "viewer", "repair", "", nil,
	); err != nil {
		return ProcessRepairResponse{}, err
	}
	return ProcessRepairResponse{Response: response, Route: route, TaskID: req.TaskID, RunID: run.RunID}, nil
}

func (o *MessageOrchestrator) ProcessRepair(ctx context.Context, req ProcessRepairRequest) (ProcessRepairResponse, error) {
	req = normalizeRepairProcessRequest(req)
	route := repairTargetRoute(req.TargetRoute)
	if o == nil || o.taskLifecycle == nil || o.events == nil {
		return ProcessRepairResponse{}, fmt.Errorf("repair requires configured task lifecycle and event port")
	}
	return processRepair(ctx, req, route, o.taskLifecycle, o.events, o.events.publicationFail,
		func(ctx context.Context, input conversation.TurnInput, route routing.Route, taskID modulecore.TaskID, runID modulecore.RunID) (string, error) {
			return o.routeDispatcher.ExecuteTurnInput(ctx, input, route, taskID, runID, "")
		})
}

func (o *DistributedOrchestrator) ProcessRepair(ctx context.Context, req ProcessRepairRequest) (ProcessRepairResponse, error) {
	req = normalizeRepairProcessRequest(req)
	route := repairTargetRoute(req.TargetRoute)
	if o == nil || o.taskLifecycle == nil || o.events == nil {
		return ProcessRepairResponse{}, fmt.Errorf("repair requires configured task lifecycle and event port")
	}
	return processRepair(ctx, req, route, o.taskLifecycle, o.events, o.events.publicationFail,
		func(ctx context.Context, input conversation.TurnInput, route routing.Route, taskID modulecore.TaskID, runID modulecore.RunID) (string, error) {
			return o.routes.ExecuteTurnInput(ctx, input, route, taskID, runID, "")
		})
}
