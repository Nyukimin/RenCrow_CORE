package dcimigration

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"time"

	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// DryRun validates the five read-only snapshot sources, classifies their DCI
// records, and atomically writes a bounded dry-run receipt.  It never creates
// a target database and never appends to an Event Store.  A blocked validation
// still returns its bounded blocked receipt and a non-nil error.
func DryRun(ctx context.Context, options Options) (Manifest, error) {
	manifest := newBaseManifest(options.Expected)
	if err := ctx.Err(); err != nil {
		manifest.Status = StatusBlocked
		manifest.ErrorCode = "context_canceled"
		return manifest, err
	}
	if err := validateOptions(options); err != nil {
		manifest.Status = StatusBlocked
		manifest.ErrorCode = errorCode(err, "invalid_options")
		return manifest, err
	}
	paths, err := resolvePaths(options)
	if err != nil {
		manifest.Status = StatusBlocked
		manifest.ErrorCode = errorCode(err, "unsafe_path")
		return manifest, err
	}
	report, err := classifySnapshot(ctx, paths, options)
	if err != nil {
		manifest = report.Manifest
		manifest.Status = StatusBlocked
		manifest.ErrorCode = errorCode(err, "source_read")
		if writeErr := writeManifest(paths.manifest, manifest); writeErr != nil {
			return manifest, fmt.Errorf("%w; write blocked DCI migration manifest: %v", err, writeErr)
		}
		return manifest, err
	}
	manifest = report.Manifest
	return persistReadyManifest(paths.manifest, manifest)
}

func persistReadyManifest(path string, manifest Manifest) (Manifest, error) {
	manifest.Status = StatusReady
	if writeErr := writeManifest(path, manifest); writeErr != nil {
		manifest.Status = StatusBlocked
		manifest.ErrorCode = errorCode(writeErr, "manifest_write")
		if validationErr := validateManifest(manifest); validationErr != nil {
			return manifest, fmt.Errorf("%w; blocked DCI migration manifest is invalid: %v", writeErr, validationErr)
		}
		return manifest, writeErr
	}
	return manifest, nil
}

// Run is the explicit dry-run entry point used by the command.  No mode other
// than dry-run is accepted by this package.
func Run(ctx context.Context, options Options) (Manifest, error) {
	return DryRun(ctx, options)
}

type classificationReport struct {
	Manifest Manifest
	Snapshot sourceSnapshot
	Plan     migrationPlan
}

func newBaseManifest(expected ExpectedCounts) Manifest {
	return Manifest{
		SchemaVersion:                 ManifestSchemaVersion,
		Mode:                          ModeDryRun,
		Status:                        StatusBlocked,
		ExpectedCounts:                expected,
		ExclusionReasonCounts:         make(map[string]int),
		LegacyActorLabelCounts:        make(map[string]int),
		LogicalHashAlgorithm:          LogicalHashAlgorithm,
		TextNormalizationAlgorithm:    TextNormalizationAlgorithm,
		SourceDatabaseLogicalSHA256:   make(map[string]string),
		SourceSchemaSHA256:            make(map[string]string),
		SourceDCIClassificationSHA256: make(map[string]string),
		SourceFileSHA256:              make(map[string]string),
		SourceNonDCILogicalSHA256:     make(map[string]string),
	}
}

func classifySnapshot(ctx context.Context, paths sourcePaths, options Options) (classificationReport, error) {
	report := classificationReport{Manifest: newBaseManifest(options.Expected)}
	snapshot := sourceSnapshot{
		Searches:           make(map[string]legacySearch),
		Evidence:           make(map[string]legacyEvidence),
		StagingIDs:         make(map[string]struct{}),
		StagingEvidenceIDs: make(map[string]struct{}),
		ExistingEventIDs:   make(map[string]struct{}),
		SourceHashes:       make(map[string]sourceHashes),
	}

	dciSearches, dciEvidence, dciCounts, dciHashes, err := loadLegacyDCI(ctx, paths.dci)
	snapshot.Counts = dciCounts
	if err != nil {
		report.Manifest.SourceCounts = dciCounts
		return report, err
	}
	snapshot.SourceHashes["source_dci"] = dciHashes
	if err := mergeSearches(snapshot.Searches, dciSearches); err != nil {
		return report, err
	}
	if err := mergeEvidenceMap(snapshot.Evidence, dciEvidence); err != nil {
		return report, err
	}

	jsonSearches, jsonRecords, jsonSteps, jsonHash, err := loadLegacyJSONL(ctx, paths.dciJSONL)
	if err != nil {
		return report, err
	}
	snapshot.Counts.JSONLTraces = jsonRecords
	snapshot.Counts.JSONLSteps = jsonSteps
	snapshot.SourceHashes["source_dci_jsonl"] = sourceHashes{Classification: jsonHash, File: jsonHash}
	if err := mergeSearches(snapshot.Searches, jsonSearches); err != nil {
		return report, err
	}

	current, currentHashes, err := loadL1Current(ctx, paths.l1)
	if err != nil {
		return report, err
	}
	snapshot.currentL1 = current
	snapshot.Counts.CurrentStaging = current.Counts.CurrentStaging
	snapshot.Counts.CurrentDCIStaging = current.DCIStaging
	snapshot.Counts.CurrentRegistry = current.Counts.CurrentRegistry
	snapshot.SourceHashes["source_l1"] = currentHashes
	mergeSourceEvidenceCount := len(snapshot.Evidence)
	if err := mergeEvidenceList(snapshot.Evidence, current.Evidence); err != nil {
		return report, err
	}
	if len(current.Evidence) < len(snapshot.Evidence)-mergeSourceEvidenceCount {
		return report, newCodedError("conflicting_duplicate_evidence", "L1 evidence merge is inconsistent")
	}
	mergeRegistryRefs(&snapshot, current.RegistryRefs)
	for id := range current.StagingIDs {
		snapshot.StagingIDs[id] = struct{}{}
	}
	for id := range current.StagingEvidenceIDs {
		snapshot.StagingEvidenceIDs[id] = struct{}{}
	}

	archive, archiveHashes, err := loadL1Archive(ctx, paths.archive)
	if err != nil {
		return report, err
	}
	snapshot.archiveL1 = archive
	snapshot.Counts.ArchiveStaging = archive.Counts.ArchiveStaging
	snapshot.Counts.ArchiveDCIStaging = archive.DCIStaging
	snapshot.SourceHashes["source_archive"] = archiveHashes
	if err := mergeEvidenceList(snapshot.Evidence, archive.Evidence); err != nil {
		return report, err
	}
	mergeRegistryRefs(&snapshot, archive.RegistryRefs)
	for id := range archive.StagingIDs {
		snapshot.StagingIDs[id] = struct{}{}
	}
	for id := range archive.StagingEvidenceIDs {
		snapshot.StagingEvidenceIDs[id] = struct{}{}
	}

	existingEventIDs, eventCounts, eventHashes, err := loadEventStore(ctx, paths.eventStore)
	if err != nil {
		return report, err
	}
	snapshot.ExistingEventIDs = existingEventIDs
	snapshot.Counts.EventStore = eventCounts.EventStore
	snapshot.SourceHashes["source_event_store"] = eventHashes

	if err := validateMergedSnapshot(snapshot); err != nil {
		return report, err
	}
	normalizeSnapshotEvidence(&snapshot)
	report.Snapshot = snapshot
	plan, err := planMigration(ctx, snapshot, options.AgentIDs)
	if err != nil {
		return report, err
	}
	report.Plan = plan
	if err := validateDerivedDedupeCounts(snapshot, plan.actual); err != nil {
		report.Manifest = newBaseManifest(options.Expected)
		report.Manifest.SourceCounts = snapshot.Counts
		report.Manifest = manifestFromSourceHashes(report.Manifest, snapshot.SourceHashes)
		return report, err
	}
	for _, event := range plan.Events {
		if _, exists := snapshot.ExistingEventIDs[string(event.EventID)]; exists {
			report.Manifest = manifestFromSnapshot(snapshot, options.Expected, plan.actual, plan.mappingLines, plan.Events, plan.eventPlanSHA256, options.AgentIDs)
			return report, newCodedError("event_collision", "planned canonical EventID already exists")
		}
	}
	if err := compareExpected(plan.actual, options.Expected); err != nil {
		report.Manifest = manifestFromSnapshot(snapshot, options.Expected, plan.actual, plan.mappingLines, plan.Events, plan.eventPlanSHA256, options.AgentIDs)
		return report, err
	}
	report.Manifest = manifestFromSnapshot(snapshot, options.Expected, plan.actual, plan.mappingLines, plan.Events, plan.eventPlanSHA256, options.AgentIDs)
	report.Manifest.Status = StatusReady
	return report, nil
}

func mergeSearches(destination map[string]legacySearch, incoming map[string]legacySearch) error {
	for id, search := range incoming {
		if prior, exists := destination[id]; exists {
			merged, err := mergeLegacySearch(prior, search)
			if err != nil {
				return newCodedError("conflicting_search", "legacy search duplicate conflicts")
			}
			destination[id] = merged
			continue
		}
		destination[id] = search
	}
	return nil
}

func mergeEvidenceMap(destination map[string]legacyEvidence, incoming map[string]legacyEvidence) error {
	for id, evidence := range incoming {
		if prior, exists := destination[id]; exists {
			merged, err := mergeLegacyEvidenceAcrossSources(prior, evidence)
			if err != nil {
				return newCodedError("conflicting_duplicate_evidence", "legacy evidence duplicate conflicts")
			}
			destination[id] = merged
			continue
		}
		destination[id] = evidence
	}
	return nil
}

func mergeEvidenceList(destination map[string]legacyEvidence, incoming []legacyEvidence) error {
	for _, evidence := range incoming {
		if prior, exists := destination[evidence.ID]; exists {
			merged, err := mergeLegacyEvidenceAcrossSources(prior, evidence)
			if err != nil {
				return newCodedError("conflicting_duplicate_evidence", "staging evidence duplicate conflicts")
			}
			destination[evidence.ID] = merged
			continue
		}
		destination[evidence.ID] = evidence
	}
	return nil
}

func mergeLegacyEvidenceAcrossSources(left, right legacyEvidence) (legacyEvidence, error) {
	leftContent := left
	rightContent := right
	leftContent.SourceID = ""
	rightContent.SourceID = ""
	if !equalLegacyEvidence(leftContent, rightContent) {
		return legacyEvidence{}, fmt.Errorf("evidence content differs")
	}
	if left.SourceID != "" && right.SourceID != "" && left.SourceID != right.SourceID {
		return legacyEvidence{}, fmt.Errorf("evidence provenance differs")
	}
	if left.SourceID == "" {
		left.SourceID = right.SourceID
	}
	return left, nil
}

func mergeRegistryRefs(snapshot *sourceSnapshot, incoming []legacyRegistryRef) {
	snapshot.RegistryRefs = append(snapshot.RegistryRefs, incoming...)
}

func validateMergedSnapshot(snapshot sourceSnapshot) error {
	projectionIdentities, err := indexL1ProjectionIdentities(snapshot.currentL1, snapshot.archiveL1)
	if err != nil {
		return err
	}
	actorLabels := make(map[string]struct{})
	for _, search := range snapshot.Searches {
		if err := validateLegacySearch(search); err != nil {
			return newCodedError("malformed_source", "merged DCI search is invalid")
		}
		actorLabels[search.Actor] = struct{}{}
		if len(actorLabels) > maxActorLabels {
			return newCodedError("bounded_source", "legacy actor label count exceeds the bound")
		}
		readPaths := make(map[string]struct{})
		for _, step := range search.Steps {
			if step.Tool == "limit" {
				continue
			}
			if _, exists := readPaths[step.FilePath]; exists {
				return newCodedError("ambiguous_evidence_attribution", "legacy DCI search contains duplicate read_file paths")
			}
			readPaths[step.FilePath] = struct{}{}
			if _, ok := snapshot.Searches[step.SearchID]; !ok {
				return newCodedError("missing_parent", "read step references missing search")
			}
		}
	}
	for _, evidence := range snapshot.Evidence {
		if _, exists := snapshot.Searches[evidence.SearchID]; !exists {
			return newCodedError("missing_parent", "evidence references missing search")
		}
	}
	for _, ref := range snapshot.RegistryRefs {
		search, searchOK := snapshot.Searches[ref.SearchID]
		evidence, evidenceOK := snapshot.Evidence[ref.EvidenceID]
		if !searchOK || !evidenceOK {
			return newCodedError("missing_parent", "source registry DCI metadata references a missing search or evidence")
		}
		if evidence.SearchID != ref.SearchID {
			return newCodedError("conflicting_duplicate_evidence", "source registry DCI metadata resolves to another search")
		}
		projectionIdentity, exists := projectionIdentities[ref.EvidenceID]
		if !exists || projectionIdentity.SearchID != ref.SearchID || projectionIdentity.SourceID != ref.SourceID {
			return newCodedError("conflicting_duplicate_registry", "source registry DCI metadata is not bound to its L1 projection identity")
		}
		_ = search
	}
	return nil
}

func buildEventPlan(ctx context.Context, snapshot sourceSnapshot, agentIDs []string) (ActualCounts, []modulecore.EventEnvelope, []string, string, error) {
	plan, err := planMigration(ctx, snapshot, agentIDs)
	if err != nil {
		return ActualCounts{}, nil, nil, "", err
	}
	return plan.actual, plan.Events, plan.mappingLines, plan.eventPlanSHA256, nil
}

func planMigration(ctx context.Context, snapshot sourceSnapshot, agentIDs []string) (migrationPlan, error) {
	if ctx == nil {
		return migrationPlan{}, fmt.Errorf("migration plan context is required")
	}
	if err := ctx.Err(); err != nil {
		return migrationPlan{}, err
	}
	agents := canonicalAgentSet(agentIDs)
	searchIDs := sortedSearchIDs(snapshot.Searches)
	evidenceIDs := sortedEvidenceIDs(snapshot.Evidence)
	events := make([]modulecore.EventEnvelope, 0, len(searchIDs)*2)
	mappingLines := make([]string, 0, len(searchIDs)*3+len(evidenceIDs)*2)
	readCount := 0
	limitCount := 0
	searchMappings := make(map[string]searchMigrationIDs, len(searchIDs))
	readMappings := make(map[readEventKey]modulecore.EventID)
	evidenceMappings := make(map[string]evidenceMigrationIDs, len(evidenceIDs))
	readCauseIDs := make(map[string]map[string]modulecore.EventID, len(searchIDs))

	for _, searchID := range searchIDs {
		if err := ctx.Err(); err != nil {
			return migrationPlan{}, err
		}
		search := snapshot.Searches[searchID]
		actionRaw, err := modulecore.NewMigrationID(modulecore.CanonicalActionID, "dci_search_trace", "event_id", searchID)
		if err != nil {
			return migrationPlan{}, err
		}
		traceRaw, err := modulecore.NewMigrationID(modulecore.CanonicalTraceID, "dci_search_trace", "event_id", searchID)
		if err != nil {
			return migrationPlan{}, err
		}
		actionID := modulecore.ActionID(actionRaw)
		traceID := modulecore.TraceID(traceRaw)
		mappingLines = append(mappingLines,
			mappingLine(modulecore.CanonicalActionID, "dci_search_trace", "event_id", searchID, actionRaw),
			mappingLine(modulecore.CanonicalTraceID, "dci_search_trace", "event_id", searchID, traceRaw),
		)
		actorKind, actorID := classifyActor(search.Actor, agents)
		startedRaw, err := modulecore.NewMigrationID(modulecore.CanonicalEventID, "dci_search_trace", "started_event_id", searchID)
		if err != nil {
			return migrationPlan{}, err
		}
		startedID := modulecore.EventID(startedRaw)
		attribution := domaindci.ActorAttributionLegacyUnattributed
		if actorKind != "" && actorID != "" {
			attribution = domaindci.ActorAttributionAuthenticated
		}
		searchMappings[searchID] = searchMigrationIDs{
			actionID: actionID, traceID: traceID, startedEventID: startedID,
			actorAttribution: attribution, actorKind: actorKind, actorID: actorID,
		}
		mappingLines = append(mappingLines, mappingLine(modulecore.CanonicalEventID, "dci_search_trace", "started_event_id", searchID, startedRaw))
		events = append(events, newMigrationEvent(traceID, actionID, startedID, "dci.search.started", search.StartedAt, actorKind, actorID, "", "", map[string]any{
			"legacy_actor_label": search.Actor,
			"status":             "started",
		}))
		readCauseIDs[searchID] = make(map[string]modulecore.EventID)
		stepNos := sortedStepNos(search.Steps)
		lastEvent := startedID
		searchEvidenceEventIDs := make([]modulecore.EventID, 0)
		for _, stepNo := range stepNos {
			step := search.Steps[stepNo]
			if step.Tool == "limit" {
				limitCount++
				continue
			}
			if step.Tool != "read_file" {
				return migrationPlan{}, newCodedError("unexpected_tool", "unexpected legacy tool")
			}
			stepKey := searchID + "\x00" + strconv.Itoa(stepNo)
			eventRaw, err := modulecore.NewMigrationID(modulecore.CanonicalEventID, "dci_search_step", "search_id_step_no", stepKey)
			if err != nil {
				return migrationPlan{}, err
			}
			eventID := modulecore.EventID(eventRaw)
			readMappings[readEventKey{searchID: searchID, stepNo: stepNo}] = eventID
			readCauseIDs[searchID][step.FilePath] = eventID
			mappingLines = append(mappingLines, mappingLine(modulecore.CanonicalEventID, "dci_search_step", "search_id_step_no", stepKey, eventRaw))
			events = append(events, newMigrationEvent(traceID, actionID, eventID, "dci.file.read", step.CreatedAt, actorKind, actorID, lastEvent, "", map[string]any{
				"legacy_actor_label": search.Actor, "step_no": step.StepNo, "tool": step.Tool,
				"command_text": step.CommandText, "file_path": step.FilePath, "result_count": step.ResultCount,
				"status": step.Status, "error_message": step.ErrorMessage,
			}))
			lastEvent = eventID
			readCount++
		}
		for _, evidenceID := range evidenceIDs {
			if err := ctx.Err(); err != nil {
				return migrationPlan{}, err
			}
			evidence := snapshot.Evidence[evidenceID]
			if evidence.SearchID != searchID {
				continue
			}
			canonicalEvidenceRaw, err := modulecore.NewMigrationID(modulecore.CanonicalEvidenceID, "dci_evidence", "evidence_id", evidence.ID)
			if err != nil {
				return migrationPlan{}, err
			}
			createdEventRaw, err := modulecore.NewMigrationID(modulecore.CanonicalEventID, "dci_evidence", "evidence_id", evidence.ID)
			if err != nil {
				return migrationPlan{}, err
			}
			evidenceCanonicalID := modulecore.EvidenceID(canonicalEvidenceRaw)
			evidenceEventID := modulecore.EventID(createdEventRaw)
			evidenceMappings[evidenceID] = evidenceMigrationIDs{evidenceID: evidenceCanonicalID, createdEventID: evidenceEventID}
			mappingLines = append(mappingLines,
				mappingLine(modulecore.CanonicalEvidenceID, "dci_evidence", "evidence_id", evidence.ID, canonicalEvidenceRaw),
				mappingLine(modulecore.CanonicalEventID, "dci_evidence", "evidence_id", evidence.ID, createdEventRaw),
			)
			cause := startedID
			if readID, ok := readCauseIDs[searchID][evidence.FilePath]; ok {
				cause = readID
			}
			events = append(events, newMigrationEvent(traceID, actionID, evidenceEventID, "dci.evidence.created", evidence.CreatedAt, actorKind, actorID, cause, evidenceCanonicalID, map[string]any{
				"legacy_actor_label": search.Actor, "file_path": evidence.FilePath,
				"line_start": evidence.LineStart, "line_end": evidence.LineEnd,
				"snippet": evidence.Snippet, "reason": evidence.Reason, "confidence": evidence.Confidence,
			}))
			lastEvent = evidenceEventID
			searchEvidenceEventIDs = append(searchEvidenceEventIDs, evidenceEventID)
		}
		terminalRaw, err := modulecore.NewMigrationID(modulecore.CanonicalEventID, "dci_search_trace", "terminal_event_id", searchID)
		if err != nil {
			return migrationPlan{}, err
		}
		terminalType := "dci.search.completed"
		if search.Status == "failed" {
			terminalType = "dci.search.failed"
		}
		terminalPayload := map[string]any{
			"legacy_actor_label": search.Actor,
			"status":             search.Status,
			"evidence_count":     0, "legacy_limit_steps": 0, "error_message": search.ErrorMessage,
		}
		if count := countEvidenceForSearch(snapshot.Evidence, searchID); count > 0 {
			terminalPayload["evidence_count"] = count
		}
		for stepNo := range search.Steps {
			if search.Steps[stepNo].Tool == "limit" {
				terminalPayload["legacy_limit_steps"] = terminalPayload["legacy_limit_steps"].(int) + 1
			}
		}
		terminalPayload["limitations"] = []string{}
		if terminalPayload["legacy_limit_steps"].(int) > 0 {
			terminalPayload["limitations"] = []string{"legacy_limit_projection"}
		}
		terminalID := modulecore.EventID(terminalRaw)
		searchIDsForPlan := searchMappings[searchID]
		searchIDsForPlan.terminalEventID = terminalID
		searchMappings[searchID] = searchIDsForPlan
		mappingLines = append(mappingLines, mappingLine(modulecore.CanonicalEventID, "dci_search_trace", "terminal_event_id", searchID, terminalRaw))
		terminalCause := lastEvent
		var terminalDependencies []modulecore.EventID
		if len(searchEvidenceEventIDs) > 0 {
			terminalCause = searchEvidenceEventIDs[len(searchEvidenceEventIDs)-1]
			if len(searchEvidenceEventIDs) > 1 {
				terminalDependencies = append([]modulecore.EventID(nil), searchEvidenceEventIDs[:len(searchEvidenceEventIDs)-1]...)
				sort.Slice(terminalDependencies, func(left, right int) bool {
					return string(terminalDependencies[left]) < string(terminalDependencies[right])
				})
			}
		}
		terminalEvent := newMigrationEvent(traceID, actionID, terminalID, terminalType, search.EndedAt, actorKind, actorID, terminalCause, "", terminalPayload)
		terminalEvent.DependencyEventIDs = terminalDependencies
		events = append(events, terminalEvent)
	}
	if err := validatePlannedPayloads(events, snapshot); err != nil {
		return migrationPlan{}, err
	}
	if err := modulecore.ValidateEventEnvelopeGraph(events); err != nil {
		return migrationPlan{}, newCodedError("event_graph_invalid", "planned DCI event graph: %v", err)
	}
	eventPlanSHA256, err := hashEventPlan(events)
	if err != nil {
		return migrationPlan{}, err
	}
	actual := ActualCounts{
		Searches:             len(searchIDs),
		ReadEvents:           readCount,
		EvidenceEvents:       len(evidenceIDs),
		TotalEvents:          len(events),
		LegacyLimitSteps:     limitCount,
		NormalizedTextValues: snapshot.normalization.NormalizedTextValues,
		InvalidUTF8Bytes:     snapshot.normalization.InvalidUTF8Bytes,
	}
	return migrationPlan{
		actual: actual, Events: events, mappingLines: mappingLines, eventPlanSHA256: eventPlanSHA256,
		searches: searchMappings, readEvents: readMappings, evidence: evidenceMappings,
	}, nil
}

func newMigrationEvent(traceID modulecore.TraceID, actionID modulecore.ActionID, eventID modulecore.EventID, eventType string, occurredAt time.Time, actorKind, actorID string, cause modulecore.EventID, evidenceID modulecore.EvidenceID, payload map[string]any) modulecore.EventEnvelope {
	event := modulecore.EventEnvelope{
		SchemaVersion: modulecore.EventEnvelopeSchemaVersion,
		EventID:       eventID, TraceID: traceID, EventType: eventType, ComponentID: "dci",
		OccurredAt: occurredAt.UTC(), ActionID: actionID, ActorKind: actorKind, ActorID: actorID,
		EvidenceID: evidenceID, Payload: payload,
	}
	if cause != "" {
		event.CausationEventID = cause
	}
	return event
}

func canonicalAgentSet(agentIDs []string) map[string]struct{} {
	agents := make(map[string]struct{}, len(agentIDs))
	for _, id := range agentIDs {
		agents[id] = struct{}{}
	}
	return agents
}

func classifyActor(label string, agents map[string]struct{}) (string, string) {
	if _, ok := agents[label]; ok {
		return "agent", label
	}
	return "", ""
}

var forbiddenLegacyPayloadKeys = map[string]struct{}{
	"legacy_search_id":            {},
	"legacy_evidence_id":          {},
	"legacy_step_no":              {},
	"legacy_final_evidence_count": {},
	"search_event_id":             {},
}

type plannedPayloadAudit struct {
	LegacyKeyCount   int
	RawLegacyIDCount int
}

func auditPlannedPayload(events []modulecore.EventEnvelope, snapshot sourceSnapshot) plannedPayloadAudit {
	legacyIDs := make(map[string]struct{}, len(snapshot.Searches)+len(snapshot.Evidence))
	for key, search := range snapshot.Searches {
		addLegacyPayloadID(legacyIDs, key)
		addLegacyPayloadID(legacyIDs, search.ID)
	}
	for key, evidence := range snapshot.Evidence {
		addLegacyPayloadID(legacyIDs, key)
		addLegacyPayloadID(legacyIDs, evidence.ID)
		addLegacyPayloadID(legacyIDs, evidence.SearchID)
	}
	audit := plannedPayloadAudit{}
	for _, event := range events {
		auditPayloadValue(event.Payload, legacyIDs, &audit)
	}
	return audit
}

func addLegacyPayloadID(ids map[string]struct{}, id string) {
	if id != "" {
		ids[id] = struct{}{}
	}
}

func auditPayloadValue(value any, legacyIDs map[string]struct{}, audit *plannedPayloadAudit) {
	auditPayloadReflect(reflect.ValueOf(value), legacyIDs, audit)
}

func auditPayloadReflect(value reflect.Value, legacyIDs map[string]struct{}, audit *plannedPayloadAudit) {
	if !value.IsValid() {
		return
	}
	for value.Kind() == reflect.Interface {
		if value.IsNil() {
			return
		}
		value = value.Elem()
	}
	switch value.Kind() {
	case reflect.String:
		if _, legacy := legacyIDs[value.String()]; legacy {
			audit.RawLegacyIDCount++
		}
	case reflect.Map:
		if value.IsNil() || value.Type().Key().Kind() != reflect.String {
			return
		}
		iterator := value.MapRange()
		for iterator.Next() {
			key := iterator.Key().String()
			if _, forbidden := forbiddenLegacyPayloadKeys[key]; forbidden {
				audit.LegacyKeyCount++
			}
			if _, legacy := legacyIDs[key]; legacy {
				audit.RawLegacyIDCount++
			}
			auditPayloadReflect(iterator.Value(), legacyIDs, audit)
		}
	case reflect.Slice, reflect.Array:
		if value.Kind() == reflect.Slice && value.IsNil() {
			return
		}
		for index := 0; index < value.Len(); index++ {
			auditPayloadReflect(value.Index(index), legacyIDs, audit)
		}
	case reflect.Pointer:
		if !value.IsNil() {
			auditPayloadReflect(value.Elem(), legacyIDs, audit)
		}
	}
}

func validatePlannedPayloads(events []modulecore.EventEnvelope, snapshot sourceSnapshot) error {
	audit := auditPlannedPayload(events, snapshot)
	if audit.LegacyKeyCount == 0 && audit.RawLegacyIDCount == 0 {
		return nil
	}
	return newCodedError("legacy_payload_present", "planned DCI payload contains %d forbidden legacy keys and %d raw legacy IDs", audit.LegacyKeyCount, audit.RawLegacyIDCount)
}

func countEvidenceForSearch(evidence map[string]legacyEvidence, searchID string) int {
	count := 0
	for _, item := range evidence {
		if item.SearchID == searchID {
			count++
		}
	}
	return count
}

func mappingLine(targetType modulecore.CanonicalIDType, table, field, value, canonical string) string {
	encoded, _ := json.Marshal([]string{string(targetType), table, field, value, canonical})
	return string(encoded)
}

func hashEventPlan(events []modulecore.EventEnvelope) (string, error) {
	lines := make([]string, 0, len(events))
	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			return "", newCodedError("event_plan_hash", "encode planned event %q: %v", event.EventID, err)
		}
		lines = append(lines, string(encoded))
	}
	return hashCanonicalLines(lines), nil
}

func manifestFromSnapshot(snapshot sourceSnapshot, expected ExpectedCounts, actual ActualCounts, mappingLines []string, events []modulecore.EventEnvelope, eventPlanSHA256 string, agentIDs []string) Manifest {
	manifest := newBaseManifest(expected)
	manifest.SourceCounts = snapshot.Counts
	manifest.ActualCounts = actual
	manifest = manifestFromSourceHashes(manifest, snapshot.SourceHashes)
	manifest.ExclusionReasonCounts = make(map[string]int)
	manifest.LegacyActorLabelCounts = make(map[string]int)
	agents := canonicalAgentSet(agentIDs)
	for _, search := range snapshot.Searches {
		label := search.Actor
		if label == "" {
			label = "<empty>"
		}
		manifest.LegacyActorLabelCounts[label]++
		actorKind, actorID := classifyActor(search.Actor, agents)
		if actorKind == "agent" && actorID != "" {
			manifest.ActorClassification.AuthenticatedAgent++
		} else {
			manifest.ActorClassification.LegacyUnattributed++
		}
		for _, step := range search.Steps {
			if step.Tool == "limit" {
				manifest.ExclusionReasonCounts["legacy_limit_projection"]++
			}
		}
	}
	manifest.DedupeCounts = deriveDedupeCounts(snapshot, actual)
	manifest.MappingSHA256 = hashCanonicalLines(mappingLines)
	manifest.ActionSetSHA256 = hashCanonicalIDSet(eventActionIDs(events))
	manifest.TraceSetSHA256 = hashCanonicalIDSet(eventTraceIDs(events))
	manifest.EvidenceSetSHA256 = hashCanonicalIDSet(eventEvidenceIDs(events))
	manifest.EventSetSHA256 = hashCanonicalIDSet(eventIDs(events))
	manifest.EventPlanSHA256 = eventPlanSHA256
	audit := auditPlannedPayload(events, snapshot)
	manifest.PlannedZeroCounters = ZeroCounters{LegacyKeyZero: audit.LegacyKeyCount + audit.RawLegacyIDCount, OrphanZero: 0}
	manifest.Mode = ModeDryRun
	manifest.Status = StatusReady
	return manifest
}

func manifestFromSourceHashes(manifest Manifest, hashes map[string]sourceHashes) Manifest {
	for source, sourceHash := range hashes {
		if sourceHash.DatabaseLogical != "" {
			manifest.SourceDatabaseLogicalSHA256[source] = sourceHash.DatabaseLogical
		}
		if sourceHash.Schema != "" {
			manifest.SourceSchemaSHA256[source] = sourceHash.Schema
		}
		if sourceHash.Classification != "" {
			manifest.SourceDCIClassificationSHA256[source] = sourceHash.Classification
		}
		if sourceHash.File != "" {
			manifest.SourceFileSHA256[source] = sourceHash.File
		}
		if sourceHash.NonDCI != "" {
			manifest.SourceNonDCILogicalSHA256[source] = sourceHash.NonDCI
		}
	}
	return manifest
}

func deriveDedupeCounts(snapshot sourceSnapshot, actual ActualCounts) DedupeCounts {
	return DedupeCounts{
		SearchesRemoved:   snapshot.Counts.DCITraces + snapshot.Counts.JSONLTraces - len(snapshot.Searches),
		StepsRemoved:      snapshot.Counts.DCISteps + snapshot.Counts.JSONLSteps - actual.ReadEvents - actual.LegacyLimitSteps,
		EvidenceRemoved:   snapshot.Counts.DCIEvidence + snapshot.Counts.CurrentDCIStaging + snapshot.Counts.ArchiveDCIStaging - actual.EvidenceEvents,
		StagingDuplicates: snapshot.Counts.CurrentDCIStaging + snapshot.Counts.ArchiveDCIStaging - len(snapshot.StagingEvidenceIDs),
	}
}

func validateDerivedDedupeCounts(snapshot sourceSnapshot, actual ActualCounts) error {
	dedupe := deriveDedupeCounts(snapshot, actual)
	if dedupe.SearchesRemoved < 0 || dedupe.StepsRemoved < 0 || dedupe.EvidenceRemoved < 0 || dedupe.StagingDuplicates < 0 {
		return newCodedError("negative_dedupe_count", "derived DCI dedupe counts cannot be negative")
	}
	return nil
}

func sortedSearchIDs(searches map[string]legacySearch) []string {
	ids := make([]string, 0, len(searches))
	for id := range searches {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func sortedEvidenceIDs(evidence map[string]legacyEvidence) []string {
	ids := make([]string, 0, len(evidence))
	for id := range evidence {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func sortedStepNos(steps map[int]legacyStep) []int {
	nos := make([]int, 0, len(steps))
	for no := range steps {
		nos = append(nos, no)
	}
	sort.Ints(nos)
	return nos
}

func eventIDs(events []modulecore.EventEnvelope) []string {
	ids := make([]string, 0, len(events))
	for _, event := range events {
		ids = append(ids, string(event.EventID))
	}
	return ids
}

func eventActionIDs(events []modulecore.EventEnvelope) []string {
	ids := make([]string, 0, len(events))
	for _, event := range events {
		if event.ActionID != "" {
			ids = append(ids, string(event.ActionID))
		}
	}
	return uniqueStrings(ids)
}

func eventTraceIDs(events []modulecore.EventEnvelope) []string {
	ids := make([]string, 0, len(events))
	for _, event := range events {
		ids = append(ids, string(event.TraceID))
	}
	return uniqueStrings(ids)
}

func eventEvidenceIDs(events []modulecore.EventEnvelope) []string {
	ids := make([]string, 0, len(events))
	for _, event := range events {
		if event.EvidenceID != "" {
			ids = append(ids, string(event.EvidenceID))
		}
	}
	return uniqueStrings(ids)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func hashCanonicalIDSet(ids []string) string {
	return hashCanonicalLines(uniqueStrings(ids))
}

func compareExpected(actual ActualCounts, expected ExpectedCounts) error {
	if actual.Searches != expected.Searches || actual.ReadEvents != expected.ReadEvents || actual.EvidenceEvents != expected.EvidenceEvents || actual.TotalEvents != expected.TotalEvents || actual.LegacyLimitSteps != expected.LegacyLimitSteps || actual.NormalizedTextValues != expected.NormalizedTextValues || actual.InvalidUTF8Bytes != expected.InvalidUTF8Bytes {
		return newCodedError("expected_count_mismatch", "actual source counts do not match the expected counts")
	}
	return nil
}
