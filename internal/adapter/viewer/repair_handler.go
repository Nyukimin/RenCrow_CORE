package viewer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
)

type repairEventListener interface {
	OnEvent(orchestrator.OrchestratorEvent)
}

type RepairJobRunner interface {
	StartRepairJob(ctx context.Context, req RepairJobRequest) error
}

type RepairJobRequest struct {
	JobID       string
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
	JobID   string `json:"job_id"`
	Reason  string `json:"reason"`
	Summary string `json:"summary"`
}

func HandleRepairRun(listener repairEventListener) http.HandlerFunc {
	return HandleRepairRunWithRunner(listener, nil)
}

func HandleRepairRunWithRunner(listener repairEventListener, runner RepairJobRunner) http.HandlerFunc {
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
		jobID := fmt.Sprintf("repair-%d", time.Now().UnixNano())
		summary := fmt.Sprintf("repair requested: %s / recent=%d / target=%s:%s", req.Reason, req.Recent, req.TargetRoute, req.TargetAgent)
		payload, _ := json.Marshal(map[string]any{
			"job_id":       jobID,
			"reason":       req.Reason,
			"instruction":  req.Instruction,
			"recent":       req.Recent,
			"target_route": req.TargetRoute,
			"target_agent": req.TargetAgent,
			"status":       "requested",
		})
		if listener != nil {
			listener.OnEvent(orchestrator.NewEvent("repair.requested", "user", "repair", string(payload), "OPS", jobID, "", "viewer", "repair"))
			listener.OnEvent(orchestrator.NewEvent("job.notification", "shiro", "mio", repairNotificationContent(req, jobID), "OPS", jobID, "", "viewer", "repair"))
		}
		if runner != nil {
			runReq := RepairJobRequest{
				JobID:       jobID,
				Reason:      req.Reason,
				Instruction: req.Instruction,
				Recent:      req.Recent,
				TargetRoute: req.TargetRoute,
				TargetAgent: req.TargetAgent,
				Source:      "viewer",
			}
			if err := runner.StartRepairJob(r.Context(), runReq); err != nil {
				log.Printf("repair job start failed job=%s: %v", jobID, err)
				if listener != nil {
					errPayload, _ := json.Marshal(map[string]any{
						"job_id": jobID,
						"status": "start_failed",
						"error":  err.Error(),
					})
					listener.OnEvent(orchestrator.NewEvent("repair.start_failed", "repair", "shiro", string(errPayload), "OPS", jobID, "", "viewer", "repair"))
				}
			}
		}
		writeMonitorJSON(w, repairRunResponse{
			OK:      true,
			JobID:   jobID,
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

func repairNotificationContent(req repairRunRequest, jobID string) string {
	return strings.Join([]string{
		"修復ジョブを受け付けました",
		"status: requested",
		"job_id: " + jobID,
		"reason: " + req.Reason,
		"instruction: " + req.Instruction,
	}, "\n")
}
