package dcimigration

import (
	"context"
	"fmt"
	"time"

	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/dci"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// materializeMigrationRecords converts the retained source snapshot into the
// owner package's canonical historical records. It consumes the one retained
// migration plan; it never allocates IDs or reclassifies actors.
func materializeMigrationRecords(ctx context.Context, snapshot sourceSnapshot, plan migrationPlan) ([]dci.MigrationRecord, error) {
	if ctx == nil {
		return nil, fmt.Errorf("migration materialization context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := validateMaterializationPlan(snapshot, plan); err != nil {
		return nil, err
	}
	legacyEvidenceByCanonicalID := make(map[modulecore.EvidenceID]string, len(plan.evidence))
	for legacyID, ids := range plan.evidence {
		if prior, exists := legacyEvidenceByCanonicalID[ids.evidenceID]; exists {
			return nil, fmt.Errorf("migration evidence IDs %q and %q resolve to the same canonical evidence ID", prior, legacyID)
		}
		legacyEvidenceByCanonicalID[ids.evidenceID] = legacyID
	}
	records := make([]dci.MigrationRecord, 0, len(snapshot.Searches))
	for _, searchID := range sortedSearchIDs(snapshot.Searches) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		search := snapshot.Searches[searchID]
		ids := plan.searches[searchID]
		scope := append([]string{}, search.CorpusScope...)
		trace := domaindci.SearchTrace{
			TraceID:          ids.traceID,
			ActionID:         ids.actionID,
			StartedAt:        search.StartedAt,
			EndedAt:          search.EndedAt,
			ActorAttribution: ids.actorAttribution,
			ActorKind:        ids.actorKind,
			ActorID:          ids.actorID,
			Mode:             search.Mode,
			UserQuery:        search.Query,
			CorpusScope:      scope,
			Status:           search.Status,
			ErrorMessage:     search.ErrorMessage,
		}
		pack := domaindci.EvidencePack{
			ActionID:    ids.actionID,
			Query:       search.Query,
			CorpusScope: append([]string{}, search.CorpusScope...),
		}
		for _, stepNo := range sortedStepNos(search.Steps) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			legacy := search.Steps[stepNo]
			if legacy.Tool == "limit" {
				pack.Limitations = []string{"legacy_limit_projection"}
				continue
			}
			stepEventID := plan.readEvents[readEventKey{searchID: searchID, stepNo: stepNo}]
			trace.Steps = append(trace.Steps, domaindci.SearchStep{
				StepNo:       legacy.StepNo,
				EventID:      stepEventID,
				EventType:    "dci.file.read",
				Tool:         legacy.Tool,
				CommandText:  legacy.CommandText,
				FilePath:     legacy.FilePath,
				ResultCount:  legacy.ResultCount,
				Status:       legacy.Status,
				ErrorMessage: legacy.ErrorMessage,
				CreatedAt:    legacy.CreatedAt,
			})
		}
		for _, evidenceID := range sortedEvidenceIDs(snapshot.Evidence) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			legacy := snapshot.Evidence[evidenceID]
			if legacy.SearchID != searchID {
				continue
			}
			ids := plan.evidence[evidenceID]
			if legacy.CreatedAt.IsZero() {
				return nil, fmt.Errorf("migration evidence %q created_at is required", evidenceID)
			}
			pack.Evidence = append(pack.Evidence, domaindci.Evidence{
				EvidenceID:       ids.evidenceID,
				CreatedByEventID: ids.createdEventID,
				SourceID:         legacy.SourceID,
				FilePath:         legacy.FilePath,
				Heading:          legacy.Heading,
				LineStart:        legacy.LineStart,
				LineEnd:          legacy.LineEnd,
				Snippet:          legacy.Snippet,
				Reason:           legacy.Reason,
				Confidence:       legacy.Confidence,
			})
		}
		trace.FinalEvidenceCount = len(pack.Evidence)
		result := domaindci.SearchResult{Trace: trace, Pack: pack}
		if err := domaindci.ValidateStoredSearchResult(result); err != nil {
			return nil, fmt.Errorf("materialized DCI search %q: %w", searchID, err)
		}
		evidenceCreatedAt := make(map[modulecore.EvidenceID]time.Time, len(pack.Evidence))
		for _, evidence := range pack.Evidence {
			legacyID := legacyEvidenceByCanonicalID[evidence.EvidenceID]
			if legacyID == "" {
				return nil, fmt.Errorf("materialized DCI evidence %q has no legacy mapping", evidence.EvidenceID)
			}
			evidenceCreatedAt[evidence.EvidenceID] = snapshot.Evidence[legacyID].CreatedAt
		}
		records = append(records, dci.MigrationRecord{Result: result, EvidenceCreatedAt: evidenceCreatedAt})
	}
	return records, nil
}

// validateMaterializationPlan proves that every legacy item is represented by
// exactly one retained plan mapping and that all mapped step/evidence events
// are present in the same plan. The returned event index is used by the
// materializer to keep the plan and records tied to one event set.
func validateMaterializationPlan(snapshot sourceSnapshot, plan migrationPlan) (map[modulecore.EventID]modulecore.EventEnvelope, error) {
	events := make(map[modulecore.EventID]modulecore.EventEnvelope, len(plan.Events))
	for _, event := range plan.Events {
		if event.EventID == "" {
			return nil, fmt.Errorf("migration plan contains an empty event ID")
		}
		if _, exists := events[event.EventID]; exists {
			return nil, fmt.Errorf("migration plan contains duplicate event ID %q", event.EventID)
		}
		events[event.EventID] = event
	}
	if len(plan.searches) != len(snapshot.Searches) {
		return nil, fmt.Errorf("migration plan search map coverage mismatch")
	}
	mappedEventIDs := make(map[modulecore.EventID]string)
	reserveEvent := func(eventID modulecore.EventID, owner string) error {
		if prior, exists := mappedEventIDs[eventID]; exists {
			return fmt.Errorf("migration plan event %q is mapped more than once (%s and %s)", eventID, prior, owner)
		}
		mappedEventIDs[eventID] = owner
		return nil
	}
	seenActions := make(map[modulecore.ActionID]string, len(snapshot.Searches))
	seenTraces := make(map[modulecore.TraceID]string, len(snapshot.Searches))
	for searchID := range snapshot.Searches {
		ids, ok := plan.searches[searchID]
		if !ok {
			return nil, fmt.Errorf("migration plan is missing search %q", searchID)
		}
		if ids.actionID == "" || ids.traceID == "" || ids.startedEventID == "" || ids.terminalEventID == "" {
			return nil, fmt.Errorf("migration plan search %q has incomplete IDs", searchID)
		}
		if prior, exists := seenActions[ids.actionID]; exists {
			return nil, fmt.Errorf("migration plan action ID %q is mapped by searches %q and %q", ids.actionID, prior, searchID)
		}
		seenActions[ids.actionID] = searchID
		if prior, exists := seenTraces[ids.traceID]; exists {
			return nil, fmt.Errorf("migration plan trace ID %q is mapped by searches %q and %q", ids.traceID, prior, searchID)
		}
		seenTraces[ids.traceID] = searchID
		started, ok := events[ids.startedEventID]
		if !ok || started.EventType != "dci.search.started" || started.ActionID != ids.actionID || started.TraceID != ids.traceID {
			return nil, fmt.Errorf("migration plan search %q started event is missing or mismatched", searchID)
		}
		terminal, ok := events[ids.terminalEventID]
		if !ok || (terminal.EventType != "dci.search.completed" && terminal.EventType != "dci.search.failed") || terminal.ActionID != ids.actionID || terminal.TraceID != ids.traceID {
			return nil, fmt.Errorf("migration plan search %q terminal event is missing or mismatched", searchID)
		}
		if err := reserveEvent(ids.startedEventID, "search started"); err != nil {
			return nil, err
		}
		if err := reserveEvent(ids.terminalEventID, "search terminal"); err != nil {
			return nil, err
		}
	}
	expectedReads := make(map[readEventKey]struct{})
	for searchID, search := range snapshot.Searches {
		for stepNo, step := range search.Steps {
			if step.Tool == "limit" {
				continue
			}
			if step.Tool != "read_file" {
				return nil, fmt.Errorf("migration search %q has unsupported tool %q", searchID, step.Tool)
			}
			key := readEventKey{searchID: searchID, stepNo: stepNo}
			expectedReads[key] = struct{}{}
			mapped, ok := plan.readEvents[key]
			if !ok || mapped == "" {
				return nil, fmt.Errorf("migration plan is missing read event %q/%d", searchID, stepNo)
			}
			event, ok := events[mapped]
			ids := plan.searches[searchID]
			if !ok || event.EventType != "dci.file.read" || event.ActionID != ids.actionID || event.TraceID != ids.traceID {
				return nil, fmt.Errorf("migration plan read event %q/%d is missing or mismatched", searchID, stepNo)
			}
			if err := reserveEvent(mapped, "file read"); err != nil {
				return nil, err
			}
		}
	}
	if len(plan.readEvents) != len(expectedReads) {
		return nil, fmt.Errorf("migration plan read map coverage mismatch")
	}
	for key := range plan.readEvents {
		if _, ok := expectedReads[key]; !ok {
			return nil, fmt.Errorf("migration plan contains extra read event mapping %q/%d", key.searchID, key.stepNo)
		}
	}
	if len(plan.evidence) != len(snapshot.Evidence) {
		return nil, fmt.Errorf("migration plan evidence map coverage mismatch")
	}
	seenEvidenceIDs := make(map[modulecore.EvidenceID]string, len(snapshot.Evidence))
	for evidenceID, evidence := range snapshot.Evidence {
		ids, ok := plan.evidence[evidenceID]
		if !ok || ids.evidenceID == "" || ids.createdEventID == "" {
			return nil, fmt.Errorf("migration plan is missing evidence %q", evidenceID)
		}
		searchIDs, ok := plan.searches[evidence.SearchID]
		if !ok {
			return nil, fmt.Errorf("migration evidence %q references missing search mapping", evidenceID)
		}
		event, ok := events[ids.createdEventID]
		if !ok || event.EventType != "dci.evidence.created" || event.ActionID != searchIDs.actionID || event.TraceID != searchIDs.traceID || event.EvidenceID != ids.evidenceID {
			return nil, fmt.Errorf("migration evidence %q created event is missing or mismatched", evidenceID)
		}
		if prior, exists := seenEvidenceIDs[ids.evidenceID]; exists {
			return nil, fmt.Errorf("migration plan evidence ID %q is mapped by evidence %q and %q", ids.evidenceID, prior, evidenceID)
		}
		seenEvidenceIDs[ids.evidenceID] = evidenceID
		if err := reserveEvent(ids.createdEventID, "evidence created"); err != nil {
			return nil, err
		}
	}
	for eventID := range events {
		if _, ok := mappedEventIDs[eventID]; !ok {
			return nil, fmt.Errorf("migration plan contains an unmapped event %q", eventID)
		}
	}
	if len(events) != len(mappedEventIDs) {
		return nil, fmt.Errorf("migration plan event coverage mismatch")
	}
	return events, nil
}
