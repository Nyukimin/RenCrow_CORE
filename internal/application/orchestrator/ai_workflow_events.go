package orchestrator

import (
	"context"
	"log"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func recordHeavyCanonicalEvent(ctx context.Context, recorder CanonicalEventRecorder, status, summary string, jobID string) {
	if recorder == nil {
		return
	}
	now := time.Now().UTC()
	event := modulecore.NewRootEventEnvelope("ai_workflow", "heavy_worker."+status, now, map[string]any{
		"task_reference": jobID, "agent_label": "Heavy", "command_name": "ANALYZE",
		"status": status, "summary": summary,
	})
	if err := recorder.Append(ctx, event); err != nil {
		log.Printf("failed to record Heavy workflow event job_id=%s status=%s: %v", jobID, status, err)
	}
}
