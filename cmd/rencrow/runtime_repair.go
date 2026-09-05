package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type repairProcessor interface {
	ProcessRepair(context.Context, orchestrator.ProcessRepairRequest) (orchestrator.ProcessRepairResponse, error)
}

type asyncRepairTaskRunner struct {
	processor repairProcessor
	listener  orchestrator.EventListener
}

func newAsyncRepairTaskRunner(processor repairProcessor, listener orchestrator.EventListener) *asyncRepairTaskRunner {
	if processor == nil {
		return nil
	}
	return &asyncRepairTaskRunner{processor: processor, listener: listener}
}

func (r *asyncRepairTaskRunner) StartRepairTask(_ context.Context, req viewer.RepairTaskRequest) error {
	if r == nil || r.processor == nil {
		return fmt.Errorf("repair processor unavailable")
	}
	if err := req.TaskID.Validate(); err != nil {
		return fmt.Errorf("repair task_id is invalid: %w", err)
	}
	go r.run(req)
	return nil
}

func (r *asyncRepairTaskRunner) run(req viewer.RepairTaskRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	started := time.Now()
	if err := r.emit(req.TargetRoute, "repair.started", "repair", "shiro", map[string]any{
		"task_id":      req.TaskID,
		"status":       "running",
		"target_route": req.TargetRoute,
		"target_agent": req.TargetAgent,
	}); err != nil {
		log.Printf("repair started event publication failed task=%s: %v", req.TaskID, err)
		return
	}
	resp, err := r.processor.ProcessRepair(ctx, orchestrator.ProcessRepairRequest{
		TaskID:      req.TaskID,
		Reason:      req.Reason,
		Instruction: req.Instruction,
		Recent:      req.Recent,
		TargetRoute: req.TargetRoute,
		TargetAgent: req.TargetAgent,
		Source:      req.Source,
	})
	if err != nil {
		log.Printf("repair Task failed task=%s err=%v", req.TaskID, err)
		if emitErr := r.emit(req.TargetRoute, "repair.failed", "shiro", "repair", map[string]any{
			"task_id":    req.TaskID,
			"status":     "failed",
			"error":      err.Error(),
			"elapsed_ms": time.Since(started).Milliseconds(),
		}); emitErr != nil {
			log.Printf("repair failed event publication failed task=%s: %v", req.TaskID, emitErr)
		}
		return
	}
	if err := r.emit(resp.Route.String(), "repair.completed", "shiro", "repair", map[string]any{
		"task_id":      req.TaskID,
		"status":       "completed",
		"route":        resp.Route.String(),
		"response_len": len(resp.Response),
		"elapsed_ms":   time.Since(started).Milliseconds(),
	}); err != nil {
		log.Printf("repair completed event publication failed task=%s: %v", req.TaskID, err)
	}
}

func (r *asyncRepairTaskRunner) emit(route, eventType, from, to string, payload map[string]any) error {
	if r.listener == nil {
		return nil
	}
	taskID, _ := payload["task_id"].(modulecore.TaskID)
	if err := taskID.Validate(); err != nil {
		return fmt.Errorf("repair event task_id is invalid: %w", err)
	}
	content, _ := json.Marshal(payload)
	return r.listener.OnEvent(orchestrator.NewEvent(eventType, from, to, string(content), route, taskID.String(), "", "viewer", "repair"))
}
