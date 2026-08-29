package identitymigration

import (
	"fmt"
	"strings"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type LegacyEvent struct {
	SourceTable   string
	EventID       string
	ParentEventID string
	RunID         string
	WorkstreamID  string
	EventType     string
	OccurredAt    time.Time
	Payload       map[string]any
}

type EventMigrationManifest struct {
	InputCount               int    `json:"input_count"`
	ConvertedCount           int    `json:"converted_count"`
	DroppedRunAsParentCount  int    `json:"dropped_run_as_parent_count"`
	DroppedRunAsParentReason string `json:"dropped_run_as_parent_reason,omitempty"`
}

type EventMigrationResult struct {
	Events   []modulecore.EventEnvelope `json:"events"`
	Manifest EventMigrationManifest     `json:"manifest"`
}

type legacyTraceSource struct {
	field string
	value string
}

func ConvertLegacyEvents(componentID string, legacy []LegacyEvent) (EventMigrationResult, error) {
	componentID = strings.TrimSpace(componentID)
	if componentID == "" {
		return EventMigrationResult{}, fmt.Errorf("component_id is required")
	}
	byLegacyID := make(map[string]LegacyEvent, len(legacy))
	canonicalEventIDs := make(map[string]modulecore.EventID, len(legacy))
	for _, item := range legacy {
		if strings.TrimSpace(item.SourceTable) == "" || strings.TrimSpace(item.EventID) == "" {
			return EventMigrationResult{}, fmt.Errorf("source table and legacy event ID are required")
		}
		if _, exists := byLegacyID[item.EventID]; exists {
			return EventMigrationResult{}, fmt.Errorf("duplicate legacy event ID %q", item.EventID)
		}
		mapped, err := modulecore.NewMigrationID(modulecore.CanonicalEventID, item.SourceTable, "event_id", item.EventID)
		if err != nil {
			return EventMigrationResult{}, err
		}
		byLegacyID[item.EventID] = item
		canonicalEventIDs[item.EventID] = modulecore.EventID(mapped)
	}

	rootMemo := make(map[string]legacyTraceSource, len(legacy))
	visiting := make(map[string]bool, len(legacy))
	var traceSource func(LegacyEvent) (legacyTraceSource, error)
	traceSource = func(item LegacyEvent) (legacyTraceSource, error) {
		if item.RunID != "" {
			return legacyTraceSource{field: "run_id", value: item.RunID}, nil
		}
		if root, ok := rootMemo[item.EventID]; ok {
			return root, nil
		}
		if visiting[item.EventID] {
			return legacyTraceSource{}, fmt.Errorf("legacy event graph contains a cycle at %q", item.EventID)
		}
		visiting[item.EventID] = true
		defer func() { visiting[item.EventID] = false }()
		if item.ParentEventID != "" {
			if parent, ok := byLegacyID[item.ParentEventID]; ok {
				root, err := traceSource(parent)
				if err != nil {
					return legacyTraceSource{}, err
				}
				rootMemo[item.EventID] = root
				return root, nil
			}
		}
		root := legacyTraceSource{field: "event_id", value: item.EventID}
		rootMemo[item.EventID] = root
		return root, nil
	}

	result := EventMigrationResult{Events: make([]modulecore.EventEnvelope, 0, len(legacy))}
	result.Manifest.InputCount = len(legacy)
	for _, item := range legacy {
		traceKey, err := traceSource(item)
		if err != nil {
			return EventMigrationResult{}, err
		}
		traceID, err := modulecore.NewMigrationID(modulecore.CanonicalTraceID, item.SourceTable, traceKey.field, traceKey.value)
		if err != nil {
			return EventMigrationResult{}, err
		}
		event := modulecore.EventEnvelope{
			SchemaVersion: modulecore.EventEnvelopeSchemaVersion,
			EventID:       canonicalEventIDs[item.EventID],
			TraceID:       modulecore.TraceID(traceID),
			EventType:     strings.TrimSpace(item.EventType),
			ComponentID:   componentID,
			OccurredAt:    item.OccurredAt.UTC(),
			Payload:       clonePayload(item.Payload),
		}
		if item.RunID != "" {
			mapped, mapErr := modulecore.NewMigrationID(modulecore.CanonicalRunID, item.SourceTable, "run_id", item.RunID)
			if mapErr != nil {
				return EventMigrationResult{}, mapErr
			}
			event.RunID = modulecore.RunID(mapped)
		}
		if item.WorkstreamID != "" {
			mapped, mapErr := modulecore.NewMigrationID(modulecore.CanonicalWorkstreamID, item.SourceTable, "workstream_id", item.WorkstreamID)
			if mapErr != nil {
				return EventMigrationResult{}, mapErr
			}
			event.WorkstreamID = modulecore.WorkstreamID(mapped)
		}
		if item.ParentEventID != "" {
			if parentEventID, exists := canonicalEventIDs[item.ParentEventID]; exists {
				event.CausationEventID = parentEventID
			} else if item.RunID != "" && item.ParentEventID == item.RunID {
				result.Manifest.DroppedRunAsParentCount++
				result.Manifest.DroppedRunAsParentReason = "legacy_parent_event_id_referenced_run_id"
			} else {
				return EventMigrationResult{}, fmt.Errorf("legacy event %q references missing parent event %q", item.EventID, item.ParentEventID)
			}
		}
		result.Events = append(result.Events, event)
	}
	if err := modulecore.ValidateEventEnvelopeGraph(result.Events); err != nil {
		return EventMigrationResult{}, fmt.Errorf("converted event graph: %w", err)
	}
	result.Manifest.ConvertedCount = len(result.Events)
	return result, nil
}

func clonePayload(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}
