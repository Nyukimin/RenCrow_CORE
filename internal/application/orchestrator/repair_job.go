package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

type ProcessRepairRequest struct {
	JobID       string
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
	JobID    string
}

func normalizeRepairProcessRequest(req ProcessRepairRequest) ProcessRepairRequest {
	req.JobID = strings.TrimSpace(req.JobID)
	if req.JobID == "" {
		req.JobID = task.NewJobID().String()
	}
	req.SessionID = strings.TrimSpace(req.SessionID)
	if req.SessionID == "" {
		req.SessionID = "repair-" + req.JobID
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
	return fmt.Sprintf(`Repair Job

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
- Keep Chat/Mio out of the repair intake path; report through job events.
- Do not perform destructive operations in the Coder proposal; Worker applies executable changes.`, req.Reason, req.TargetRoute, req.TargetAgent, req.Recent, req.Source, req.Instruction)
}

func repairTask(req ProcessRepairRequest) task.Task {
	return task.NewTask(task.JobIDFromString(req.JobID), repairTaskMessage(req), "viewer", "repair").WithRoute(routing.RouteCODE2)
}

func (o *MessageOrchestrator) ProcessRepair(ctx context.Context, req ProcessRepairRequest) (ProcessRepairResponse, error) {
	req = normalizeRepairProcessRequest(req)
	t := repairTask(req)
	startedAt := time.Now()
	if o.events != nil {
		o.events.Emit("repair.dispatch", "repair", "shiro", "dispatch repair job to Coder via CODE2", string(routing.RouteCODE2), req.JobID, req.SessionID, "viewer", "repair")
	}
	response, err := o.routeDispatcher.ExecuteTask(ctx, t, routing.RouteCODE2, req.SessionID, "viewer", "repair", "")
	if err != nil {
		if o.events != nil {
			o.events.Emit("repair.failed", "shiro", "repair", err.Error(), string(routing.RouteCODE2), req.JobID, req.SessionID, "viewer", "repair")
		}
		return ProcessRepairResponse{}, err
	}
	if o.events != nil {
		o.events.Emit("repair.completed", "shiro", "repair", fmt.Sprintf("repair job completed in %s", time.Since(startedAt).Round(time.Millisecond)), string(routing.RouteCODE2), req.JobID, req.SessionID, "viewer", "repair")
	}
	return ProcessRepairResponse{Response: response, Route: routing.RouteCODE2, JobID: req.JobID}, nil
}

func (o *DistributedOrchestrator) ProcessRepair(ctx context.Context, req ProcessRepairRequest) (ProcessRepairResponse, error) {
	req = normalizeRepairProcessRequest(req)
	t := repairTask(req)
	startedAt := time.Now()
	o.emit("repair.dispatch", "repair", "shiro", "dispatch repair job to Coder via CODE2", string(routing.RouteCODE2), req.JobID, req.SessionID, "viewer", "repair")
	response, err := o.routes.ExecuteTask(ctx, t, routing.RouteCODE2, req.SessionID, "")
	if err != nil {
		o.emit("repair.failed", "shiro", "repair", err.Error(), string(routing.RouteCODE2), req.JobID, req.SessionID, "viewer", "repair")
		return ProcessRepairResponse{}, err
	}
	o.emit("repair.completed", "shiro", "repair", fmt.Sprintf("repair job completed in %s", time.Since(startedAt).Round(time.Millisecond)), string(routing.RouteCODE2), req.JobID, req.SessionID, "viewer", "repair")
	return ProcessRepairResponse{Response: response, Route: routing.RouteCODE2, JobID: req.JobID}, nil
}
