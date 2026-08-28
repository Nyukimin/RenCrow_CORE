package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	backlogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/backlog"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
)

type developmentEventLogSink struct{ store *viewer.EventLogStore }

func (s developmentEventLogSink) AppendDevelopmentEvent(_ context.Context, event backlogapp.DevelopmentEvent) error {
	if s.store == nil {
		return nil
	}
	content, err := json.Marshal(map[string]any{"unit_id": event.UnitID, "artifact_id": event.ArtifactID, "fields": event.Fields})
	if err != nil {
		return err
	}
	digest := sha256.Sum256(content)
	messageID := event.ArtifactID + ":" + strings.TrimSpace(event.Type) + ":" + fmt.Sprintf("%x", digest[:8])
	return s.store.Append(orchestrator.OrchestratorEvent{Type: strings.TrimSpace(event.Type), From: "rencrow_core", Content: string(content), MessageID: messageID, JobID: event.UnitID, TraceID: event.TraceID, Timestamp: event.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")})
}
