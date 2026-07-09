package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
)

type repairProcessor interface {
	ProcessRepair(context.Context, orchestrator.ProcessRepairRequest) (orchestrator.ProcessRepairResponse, error)
}

type asyncRepairJobRunner struct {
	processor repairProcessor
	listener  interface {
		OnEvent(orchestrator.OrchestratorEvent)
	}
}

func newAsyncRepairJobRunner(processor repairProcessor, listener interface {
	OnEvent(orchestrator.OrchestratorEvent)
}) *asyncRepairJobRunner {
	if processor == nil {
		return nil
	}
	return &asyncRepairJobRunner{processor: processor, listener: listener}
}

func (r *asyncRepairJobRunner) StartRepairJob(_ context.Context, req viewer.RepairJobRequest) error {
	if r == nil || r.processor == nil {
		return fmt.Errorf("repair processor unavailable")
	}
	if req.JobID == "" {
		return fmt.Errorf("repair job id is required")
	}
	go r.run(req)
	return nil
}

func (r *asyncRepairJobRunner) run(req viewer.RepairJobRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	started := time.Now()
	r.emit("repair.started", "repair", "shiro", map[string]any{
		"job_id":       req.JobID,
		"status":       "running",
		"target_route": req.TargetRoute,
		"target_agent": req.TargetAgent,
	})
	resp, err := r.processor.ProcessRepair(ctx, orchestrator.ProcessRepairRequest{
		JobID:       req.JobID,
		Reason:      req.Reason,
		Instruction: req.Instruction,
		Recent:      req.Recent,
		TargetRoute: req.TargetRoute,
		TargetAgent: req.TargetAgent,
		Source:      req.Source,
	})
	if err != nil {
		log.Printf("repair job failed job=%s err=%v", req.JobID, err)
		r.emit("repair.failed", "shiro", "repair", map[string]any{
			"job_id":     req.JobID,
			"status":     "failed",
			"error":      err.Error(),
			"elapsed_ms": time.Since(started).Milliseconds(),
		})
		return
	}
	r.emit("repair.completed", "shiro", "repair", map[string]any{
		"job_id":       req.JobID,
		"status":       "completed",
		"route":        resp.Route.String(),
		"response_len": len(resp.Response),
		"elapsed_ms":   time.Since(started).Milliseconds(),
	})
}

func (r *asyncRepairJobRunner) emit(eventType, from, to string, payload map[string]any) {
	if r.listener == nil {
		return
	}
	jobID, _ := payload["job_id"].(string)
	content, _ := json.Marshal(payload)
	r.listener.OnEvent(orchestrator.NewEvent(eventType, from, to, string(content), "CODE2", jobID, "repair-"+jobID, "viewer", "repair"))
}
