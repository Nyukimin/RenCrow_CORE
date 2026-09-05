package viewer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type repairEventListener interface {
	OnEvent(orchestrator.OrchestratorEvent) error
}

type RepairTaskRunner interface {
	StartRepairTask(ctx context.Context, req RepairTaskRequest) error
}

type RepairTaskRequest struct {
	TaskID      modulecore.TaskID
	Reason      string
	Instruction string
	Recent      int
	TargetRoute string
	TargetAgent string
	Source      string
}

type repairRunRequest struct {
	Reason      string `json:"reason"`
	Instruction string `json:"instruction"`
	Recent      int    `json:"recent"`
	TargetRoute string `json:"target_route"`
	TargetAgent string `json:"target_agent"`
}

type repairRunResponse struct {
	OK      bool   `json:"ok"`
	TaskID  string `json:"task_id"`
	Reason  string `json:"reason"`
	Summary string `json:"summary"`
}

func HandleRepairRun(listener repairEventListener) http.HandlerFunc {
	return HandleRepairRunWithRunner(listener, nil)
}

func HandleRepairRunWithRunner(listener repairEventListener, runner RepairTaskRunner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req repairRunRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		req = normalizeRepairRunRequest(req)
		taskID := modulecore.NewTaskID()
		summary := fmt.Sprintf("repair requested: %s / recent=%d / target=%s:%s", req.Reason, req.Recent, req.TargetRoute, req.TargetAgent)
		payload, _ := json.Marshal(map[string]any{
			"task_id":      taskID,
			"reason":       req.Reason,
			"instruction":  req.Instruction,
			"recent":       req.Recent,
			"target_route": req.TargetRoute,
			"target_agent": req.TargetAgent,
			"status":       "requested",
		})
		if listener == nil {
			http.Error(w, "repair event publication unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := listener.OnEvent(orchestrator.NewEvent("repair.requested", "user", "repair", string(payload), "OPS", taskID.String(), "", "viewer", "repair")); err != nil {
			log.Printf("repair requested event publication failed task=%s: %v", taskID, err)
			http.Error(w, "repair event publication failed", http.StatusServiceUnavailable)
			return
		}
		if err := listener.OnEvent(orchestrator.NewEvent("task.notification", "shiro", "mio", repairNotificationContent(req, taskID), "OPS", taskID.String(), "", "viewer", "repair")); err != nil {
			log.Printf("repair notification event publication failed task=%s: %v", taskID, err)
			http.Error(w, "repair event publication failed", http.StatusServiceUnavailable)
			return
		}
		if runner != nil {
			runReq := RepairTaskRequest{
				TaskID:      taskID,
				Reason:      req.Reason,
				Instruction: req.Instruction,
				Recent:      req.Recent,
				TargetRoute: req.TargetRoute,
				TargetAgent: req.TargetAgent,
				Source:      "viewer",
			}
			if err := runner.StartRepairTask(r.Context(), runReq); err != nil {
				log.Printf("repair Task start failed task=%s: %v", taskID, err)
				errPayload, _ := json.Marshal(map[string]any{
					"task_id": taskID,
					"status":  "start_failed",
					"error":   err.Error(),
				})
				if publishErr := listener.OnEvent(orchestrator.NewEvent("repair.start_failed", "repair", "shiro", string(errPayload), "OPS", taskID.String(), "", "viewer", "repair")); publishErr != nil {
					log.Printf("repair start_failed event publication failed task=%s: %v", taskID, publishErr)
				}
			}
		}
		writeMonitorJSON(w, repairRunResponse{
			OK:      true,
			TaskID:  taskID.String(),
			Reason:  req.Reason,
			Summary: summary,
		})
	}
}

func normalizeRepairRunRequest(req repairRunRequest) repairRunRequest {
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Reason == "" {
		req.Reason = "user-directed-repair"
	}
	req.Instruction = strings.TrimSpace(req.Instruction)
	if req.Instruction == "" {
		req.Instruction = "直近ログを見て、Chat経路の異常を診断し、修復案と必要な実行手順を作成してください。"
	}
	if req.Recent <= 0 {
		req.Recent = 100
	}
	if req.Recent > 1000 {
		req.Recent = 1000
	}
	req.TargetRoute = strings.ToUpper(strings.TrimSpace(req.TargetRoute))
	if req.TargetRoute == "" {
		req.TargetRoute = "CHAT"
	}
	req.TargetAgent = strings.ToLower(strings.TrimSpace(req.TargetAgent))
	if req.TargetAgent == "" {
		req.TargetAgent = "mio"
	}
	return req
}

func repairNotificationContent(req repairRunRequest, taskID modulecore.TaskID) string {
	return strings.Join([]string{
		"修復Taskを受け付けました",
		"status: requested",
		"task_id: " + taskID.String(),
		"reason: " + req.Reason,
		"instruction: " + req.Instruction,
	}, "\n")
}
