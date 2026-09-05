package orchestrator

import (
	"context"
	"log"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func recordHeavyCanonicalEvent(ctx context.Context, recorder CanonicalEventRecorder, status, summary string, taskID string) {
	if recorder == nil {
		return
	}
	now := time.Now().UTC()
	event := modulecore.NewEventEnvelope(canonicalTraceFromContext(ctx), "", nil, "ai_workflow", "heavy_worker."+status, now, map[string]any{
		"task_reference": taskID, "agent_label": "Kuro", "command_name": "ANALYZE",
		"status": status, "summary": summary,
	})
	if err := recorder.Append(ctx, event); err != nil {
		log.Printf("failed to record Kuro workflow event task_id=%s status=%s: %v", taskID, status, err)
	}
}
