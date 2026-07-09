package orchestrator

import (
	"context"
	"fmt"
	"log"
	"time"

	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
)

func recordHeavyWorkflowEvent(ctx context.Context, recorder WorkflowEventRecorder, status, summary string, jobID string) {
	if recorder == nil {
		return
	}
	now := time.Now().UTC()
	eventType := "heavy_worker_" + status
	event := domainai.WorkflowEvent{
		EventID:     fmt.Sprintf("evt_heavy_%s_%s_%d", status, jobID, now.UnixNano()),
		EventType:   eventType,
		Agent:       "Heavy",
		CommandName: "ANALYZE",
		Status:      status,
		CreatedAt:   now,
		CompletedAt: now,
		Summary:     summary,
	}
	if err := recorder.SaveWorkflowEvent(ctx, event); err != nil {
		log.Printf("failed to record Heavy workflow event job_id=%s status=%s: %v", jobID, status, err)
	}
}
