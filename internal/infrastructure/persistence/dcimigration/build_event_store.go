package dcimigration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// buildEventStoreEvidence is the private, path-free proof for one offline
// Event Store build.  It contains no event IDs, payloads, or source paths.
type buildEventStoreEvidence struct {
	SourceSchemaSHA256     string
	OutputSchemaSHA256     string
	SourceNonDCISHA256     string
	OutputNonDCISHA256     string
	OutputLogicalSHA256    string
	SourceEnvelopeCount    int
	PlannedEnvelopeCount   int
	OutputEnvelopeCount    int
	SourceDependencyCount  int
	PlannedDependencyCount int
	OutputDependencyCount  int
	PlannedDCIEventCount   int
	OutputDCIEventCount    int
	ForeignKeyViolations   int
	QuickCheckOK           int
	SidecarZero            int
}

type buildEventStoreEnvelope struct {
	event   modulecore.EventEnvelope
	rawJSON string
}

type buildEventStoreRows struct {
	envelopes       map[modulecore.EventID]buildEventStoreEnvelope
	dependencies    map[modulecore.EventID]map[modulecore.EventID]string
	envelopeCount   int
	dependencyCount int
	dciCount        int
}

// buildEventStoreAfterAppend is a package-local failure seam.  Production uses
// the no-op default; tests use it only to inject deterministic post-append
// tampering and prove that the read-only verifier and cleanup are effective.
var buildEventStoreAfterAppend = func(string) error { return nil }

// createBuiltEventStore clones one captured Event Store and appends exactly the
// retained migration graph through the owner API.  It does not create any
// production output or mutate the source database.
func createBuiltEventStore(ctx context.Context, source, target string, snapshot sourceSnapshot, plan migrationPlan) (evidence buildEventStoreEvidence, err error) {
	if ctx == nil {
		return buildEventStoreEvidence{}, buildEventStoreError("invalid_options")
	}
	if err := ctx.Err(); err != nil {
		return buildEventStoreEvidence{}, err
	}

	planned, err := validateBuildEventStorePlan(snapshot, plan)
	if err != nil {
		return buildEventStoreEvidence{}, buildEventStoreCauseError(err, "plan_invalid")
	}
	if err := ctx.Err(); err != nil {
		return buildEventStoreEvidence{}, err
	}

	sourcePath, sourceInfo, err := resolveBuildEventStoreSource(source)
	if err != nil {
		return buildEventStoreEvidence{}, buildEventStoreCauseError(err, "unsafe_path")
	}
	targetPath, err := resolveBuildEventStoreTarget(target, sourcePath)
	if err != nil {
		return buildEventStoreEvidence{}, buildEventStoreCauseError(err, "unsafe_path")
	}

	sourceFileSHA256, err := fileSHA256(sourcePath)
	if err != nil {
		return buildEventStoreEvidence{}, buildEventStoreError("source_read")
	}
	sourceIDs, sourceCounts, sourceHashes, err := loadEventStore(ctx, sourcePath)
	if err != nil {
		return buildEventStoreCauseErrorResult(err, "source_read")
	}
	if err := ctx.Err(); err != nil {
		return buildEventStoreEvidence{}, err
	}
	sourceRows, err := readBuildEventStorePath(ctx, sourcePath, true)
	if err != nil {
		return buildEventStoreCauseErrorResult(err, "source_read")
	}
	if err := validateBuildEventStoreSourceBinding(snapshot, sourceIDs, sourceCounts, sourceHashes, sourceRows); err != nil {
		return buildEventStoreEvidence{}, buildEventStoreCauseError(err, "source_binding")
	}
	storedPlanned, err := assignBuildEventStoreSequences(sourceRows, plan.Events)
	if err != nil {
		return buildEventStoreEvidence{}, buildEventStoreCauseError(err, "plan_invalid")
	}
	for eventID := range planned {
		if _, exists := sourceIDs[string(eventID)]; exists {
			return buildEventStoreEvidence{}, buildEventStoreError("event_collision")
		}
	}
	if currentSHA256, hashErr := fileSHA256(sourcePath); hashErr != nil || currentSHA256 != sourceFileSHA256 {
		return buildEventStoreEvidence{}, buildEventStoreError("source_changed")
	}
	if err := ctx.Err(); err != nil {
		return buildEventStoreEvidence{}, err
	}

	created := false
	defer func() {
		if err != nil && created {
			cleanupBuiltEventStoreTarget(targetPath)
		}
	}()
	placeholder, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return buildEventStoreEvidence{}, buildEventStoreError("target_store")
	}
	created = true
	if closeErr := placeholder.Close(); closeErr != nil {
		return buildEventStoreEvidence{}, buildEventStoreError("target_store")
	}
	targetInfo, err := os.Lstat(targetPath)
	if err != nil || targetInfo.Mode()&os.ModeSymlink != 0 || !targetInfo.Mode().IsRegular() || os.SameFile(sourceInfo, targetInfo) {
		return buildEventStoreEvidence{}, buildEventStoreError("unsafe_path")
	}
	if err := ctx.Err(); err != nil {
		return buildEventStoreEvidence{}, err
	}
	if _, err := captureSQLiteBackup(ctx, sourcePath, targetPath); err != nil {
		return buildEventStoreCauseErrorResult(err, "target_store")
	}
	if err := ctx.Err(); err != nil {
		return buildEventStoreEvidence{}, err
	}

	canonicalBaseline, err := canonicalizeBuiltEventStore(ctx, targetPath)
	if err != nil {
		return buildEventStoreCauseErrorResult(err, "target_store")
	}
	if err := ctx.Err(); err != nil {
		return buildEventStoreEvidence{}, err
	}

	store, err := eventstore.NewSQLiteStore(targetPath)
	if err != nil {
		return buildEventStoreEvidence{}, buildEventStoreError("target_store")
	}
	appendErr := store.AppendBatch(ctx, plan.Events)
	closeErr := store.Close()
	if appendErr != nil {
		return buildEventStoreCauseErrorResult(appendErr, "target_store")
	}
	if closeErr != nil {
		return buildEventStoreEvidence{}, buildEventStoreError("target_store")
	}
	if err := ctx.Err(); err != nil {
		return buildEventStoreEvidence{}, err
	}
	if err := buildEventStoreAfterAppend(targetPath); err != nil {
		return buildEventStoreCauseErrorResult(err, "target_verify")
	}
	if err := syncCaptureFile(targetPath); err != nil {
		return buildEventStoreEvidence{}, buildEventStoreError("target_sync")
	}
	if err := rejectCapturedSQLiteSidecars(targetPath); err != nil {
		return buildEventStoreEvidence{}, buildEventStoreError("target_sidecar")
	}

	verificationDB, err := openSQLiteReadOnly(ctx, targetPath)
	if err != nil {
		return buildEventStoreEvidence{}, buildEventStoreError("target_verify")
	}
	outputRows, verifyErr := readBuildEventStore(ctx, verificationDB, false)
	logical, logicalErr := hashSQLiteLogical(ctx, verificationDB, buildEventStoreNonDCIExcluder(planned))
	quickErr := captureSQLiteQuickCheck(ctx, verificationDB)
	foreignKeyViolations, foreignKeyErr := countForeignKeyViolations(ctx, verificationDB)
	closeVerificationErr := verificationDB.Close()
	if verifyErr != nil {
		return buildEventStoreCauseErrorResult(verifyErr, "target_verify")
	}
	if logicalErr != nil {
		return buildEventStoreCauseErrorResult(logicalErr, "target_verify")
	}
	if quickErr != nil {
		return buildEventStoreCauseErrorResult(quickErr, "target_verify")
	}
	if foreignKeyErr != nil {
		return buildEventStoreCauseErrorResult(foreignKeyErr, "target_verify")
	}
	if foreignKeyViolations != 0 {
		return buildEventStoreEvidence{}, buildEventStoreError("target_verify")
	}
	if closeVerificationErr != nil {
		return buildEventStoreEvidence{}, buildEventStoreError("target_verify")
	}
	if err := verifyBuildEventStoreOutput(sourceRows, outputRows, storedPlanned); err != nil {
		return buildEventStoreCauseErrorResult(err, "target_verify")
	}
	if logical.Schema != canonicalBaseline.Schema || logical.NonDCI != canonicalBaseline.Full || logical.Full == "" {
		return buildEventStoreEvidence{}, buildEventStoreError("target_hash_mismatch")
	}
	if runtime.GOOS != "windows" {
		info, statErr := os.Lstat(targetPath)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return buildEventStoreEvidence{}, buildEventStoreError("target_permissions")
		}
	}
	if err := rejectCapturedSQLiteSidecars(targetPath); err != nil {
		return buildEventStoreEvidence{}, buildEventStoreError("target_sidecar")
	}
	if afterSHA256, hashErr := fileSHA256(sourcePath); hashErr != nil || afterSHA256 != sourceFileSHA256 {
		return buildEventStoreEvidence{}, buildEventStoreError("source_changed")
	}
	if err := ctx.Err(); err != nil {
		return buildEventStoreEvidence{}, err
	}

	evidence = buildEventStoreEvidence{
		SourceSchemaSHA256:     sourceHashes.Schema,
		OutputSchemaSHA256:     logical.Schema,
		SourceNonDCISHA256:     sourceHashes.NonDCI,
		OutputNonDCISHA256:     logical.NonDCI,
		OutputLogicalSHA256:    logical.Full,
		SourceEnvelopeCount:    sourceRows.envelopeCount,
		PlannedEnvelopeCount:   len(plan.Events),
		OutputEnvelopeCount:    outputRows.envelopeCount,
		SourceDependencyCount:  sourceRows.dependencyCount,
		PlannedDependencyCount: countBuildEventStoreDependencies(plannedDependencies(planned)),
		OutputDependencyCount:  outputRows.dependencyCount,
		PlannedDCIEventCount:   len(planned),
		OutputDCIEventCount:    outputRows.dciCount,
		ForeignKeyViolations:   foreignKeyViolations,
		QuickCheckOK:           1,
		SidecarZero:            1,
	}
	created = false
	return evidence, nil
}

// canonicalizeBuiltEventStore lets the current Event Store owner apply its
// schema migration to the captured clone before DCI events are appended. The
// resulting logical state is the only valid output baseline: captured source
// hashes still bind the input, while owner-added indexes may intentionally
// make the output schema-aware hash differ from the previous generation.
func canonicalizeBuiltEventStore(ctx context.Context, target string) (logicalHashes, error) {
	store, err := eventstore.NewSQLiteStore(target)
	if err != nil {
		return logicalHashes{}, err
	}
	if err := store.Close(); err != nil {
		return logicalHashes{}, err
	}
	db, err := openSQLiteReadOnly(ctx, target)
	if err != nil {
		return logicalHashes{}, err
	}
	logical, hashErr := hashSQLiteLogical(ctx, db, nil)
	closeErr := db.Close()
	if hashErr != nil {
		return logicalHashes{}, hashErr
	}
	if closeErr != nil {
		return logicalHashes{}, closeErr
	}
	return logical, nil
}

func validateBuildEventStorePlan(snapshot sourceSnapshot, plan migrationPlan) (map[modulecore.EventID]modulecore.EventEnvelope, error) {
	planned, err := validateMaterializationPlan(snapshot, plan)
	if err != nil {
		return nil, err
	}
	if err := modulecore.ValidateEventEnvelopeGraph(plan.Events); err != nil {
		return nil, err
	}
	if len(planned) != len(plan.Events) {
		return nil, errors.New("migration plan event coverage mismatch")
	}
	for _, event := range plan.Events {
		if event.EventSeq != 0 {
			return nil, errors.New("migration plan event_seq must be unassigned")
		}
		if !strings.HasPrefix(event.EventType, "dci.") {
			return nil, errors.New("migration plan contains a non-DCI event")
		}
	}
	return planned, nil
}

func resolveBuildEventStoreSource(raw string) (string, os.FileInfo, error) {
	path, err := absolutePath(raw)
	if err != nil {
		return "", nil, errors.New("resolve Event Store source")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", nil, errors.New("Event Store source is missing or unsafe")
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil || !samePath(path, filepath.Clean(realPath)) {
		return "", nil, errors.New("Event Store source is not canonical")
	}
	realInfo, err := os.Lstat(realPath)
	if err != nil || realInfo.Mode()&os.ModeSymlink != 0 || !realInfo.Mode().IsRegular() {
		return "", nil, errors.New("Event Store source is not a regular file")
	}
	if err := rejectSQLiteSidecars(path); err != nil {
		return "", nil, err
	}
	return path, info, nil
}

func resolveBuildEventStoreTarget(raw, source string) (string, error) {
	path, err := validateFreshProjectionTarget(raw)
	if err != nil {
		return "", err
	}
	if samePath(path, source) {
		return "", errors.New("Event Store target aliases source")
	}
	return path, nil
}

func validateBuildEventStoreSourceBinding(snapshot sourceSnapshot, sourceIDs map[string]struct{}, sourceCounts SourceCounts, sourceHashes sourceHashes, sourceRows buildEventStoreRows) error {
	expected, ok := snapshot.SourceHashes["source_event_store"]
	if !ok || expected.Schema == "" || expected.NonDCI == "" {
		return errors.New("source Event Store hashes are missing")
	}
	if sourceHashes.Schema != expected.Schema || sourceHashes.NonDCI != expected.NonDCI {
		return errors.New("source Event Store hashes differ from retained snapshot")
	}
	if expected.DatabaseLogical != "" && sourceHashes.DatabaseLogical != expected.DatabaseLogical {
		return errors.New("source Event Store logical hash differs from retained snapshot")
	}
	if sourceCounts.EventStore != snapshot.Counts.EventStore || sourceRows.envelopeCount != sourceCounts.EventStore || len(sourceIDs) != sourceCounts.EventStore {
		return errors.New("source Event Store count differs from retained snapshot")
	}
	if snapshot.ExistingEventIDs != nil && !sameBuildEventIDSet(sourceIDs, snapshot.ExistingEventIDs) {
		return errors.New("source Event Store IDs differ from retained snapshot")
	}
	if sourceRows.dciCount != 0 {
		return errors.New("source Event Store contains DCI history")
	}
	return nil
}

func sameBuildEventIDSet(left, right map[string]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for id := range left {
		if _, ok := right[id]; !ok {
			return false
		}
	}
	return true
}

func readBuildEventStorePath(ctx context.Context, path string, rejectDCI bool) (buildEventStoreRows, error) {
	db, err := openSQLiteReadOnly(ctx, path)
	if err != nil {
		return buildEventStoreRows{}, err
	}
	rows, readErr := readBuildEventStore(ctx, db, rejectDCI)
	closeErr := db.Close()
	if readErr != nil {
		return buildEventStoreRows{}, readErr
	}
	if closeErr != nil {
		return buildEventStoreRows{}, closeErr
	}
	return rows, nil
}

func readBuildEventStore(ctx context.Context, db *sql.DB, rejectDCI bool) (buildEventStoreRows, error) {
	if err := ctx.Err(); err != nil {
		return buildEventStoreRows{}, err
	}
	if err := checkUserVersion(ctx, db, 0); err != nil {
		return buildEventStoreRows{}, err
	}
	tables, err := schemaUserTables(ctx, db)
	if err != nil {
		return buildEventStoreRows{}, err
	}
	if err := requireTableSet(tables, []string{"event_envelope", "event_dependency"}, true); err != nil {
		return buildEventStoreRows{}, err
	}
	if err := inspectTable(ctx, db, "event_envelope", eventStoreSpecs["event_envelope"]); err != nil {
		return buildEventStoreRows{}, err
	}
	if err := inspectCompositePrimaryKey(ctx, db, "event_dependency", []string{"event_id", "dependency_event_id"}); err != nil {
		return buildEventStoreRows{}, err
	}
	if err := inspectTableWithoutPrimaryValidation(ctx, db, "event_dependency", eventStoreSpecs["event_dependency"]); err != nil {
		return buildEventStoreRows{}, err
	}
	if err := inspectEventDependencyForeignKeys(ctx, db); err != nil {
		return buildEventStoreRows{}, err
	}

	result := buildEventStoreRows{
		envelopes:    make(map[modulecore.EventID]buildEventStoreEnvelope),
		dependencies: make(map[modulecore.EventID]map[modulecore.EventID]string),
	}
	events := make([]modulecore.EventEnvelope, 0)
	rows, err := db.QueryContext(ctx, `SELECT event_id, event_seq, trace_id, schema_version, event_type, component_id, occurred_at, envelope_json FROM event_envelope ORDER BY event_seq`)
	if err != nil {
		return buildEventStoreRows{}, err
	}
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			_ = rows.Close()
			return buildEventStoreRows{}, err
		}
		if result.envelopeCount >= maxLogicalRows {
			_ = rows.Close()
			return buildEventStoreRows{}, errors.New("Event Store envelope count exceeds bound")
		}
		var eventID, traceID, schemaVersion, eventType, componentID, occurredAt, envelopeJSON string
		var eventSeq int64
		if err := rows.Scan(&eventID, &eventSeq, &traceID, &schemaVersion, &eventType, &componentID, &occurredAt, &envelopeJSON); err != nil {
			_ = rows.Close()
			return buildEventStoreRows{}, err
		}
		event, err := validateExistingEventIdentity(eventID, eventSeq, traceID, schemaVersion, eventType, componentID, occurredAt, envelopeJSON)
		if err != nil {
			_ = rows.Close()
			return buildEventStoreRows{}, err
		}
		if rejectDCI && strings.HasPrefix(event.EventType, "dci.") {
			_ = rows.Close()
			return buildEventStoreRows{}, errors.New("source Event Store contains DCI history")
		}
		if _, exists := result.envelopes[event.EventID]; exists {
			_ = rows.Close()
			return buildEventStoreRows{}, errors.New("Event Store has duplicate event ID")
		}
		result.envelopes[event.EventID] = buildEventStoreEnvelope{event: event, rawJSON: envelopeJSON}
		result.envelopeCount++
		if strings.HasPrefix(event.EventType, "dci.") {
			result.dciCount++
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return buildEventStoreRows{}, err
	}
	if err := rows.Close(); err != nil {
		return buildEventStoreRows{}, err
	}

	dependencyRows, err := db.QueryContext(ctx, `SELECT event_id, dependency_event_id, relation_type FROM event_dependency ORDER BY rowid`)
	if err != nil {
		return buildEventStoreRows{}, err
	}
	for dependencyRows.Next() {
		if err := ctx.Err(); err != nil {
			_ = dependencyRows.Close()
			return buildEventStoreRows{}, err
		}
		if result.dependencyCount >= maxLogicalRows {
			_ = dependencyRows.Close()
			return buildEventStoreRows{}, errors.New("Event Store dependency count exceeds bound")
		}
		var eventID, dependencyID, relation string
		if err := dependencyRows.Scan(&eventID, &dependencyID, &relation); err != nil {
			_ = dependencyRows.Close()
			return buildEventStoreRows{}, err
		}
		if relation != "causation" && relation != "dependency" {
			_ = dependencyRows.Close()
			return buildEventStoreRows{}, errors.New("Event Store dependency relation is invalid")
		}
		owner := modulecore.EventID(eventID)
		dependency := modulecore.EventID(dependencyID)
		if _, ok := result.envelopes[owner]; !ok {
			_ = dependencyRows.Close()
			return buildEventStoreRows{}, errors.New("Event Store dependency owner is missing")
		}
		if _, ok := result.envelopes[dependency]; !ok {
			_ = dependencyRows.Close()
			return buildEventStoreRows{}, errors.New("Event Store dependency target is missing")
		}
		byDependency := result.dependencies[owner]
		if byDependency == nil {
			byDependency = make(map[modulecore.EventID]string)
			result.dependencies[owner] = byDependency
		}
		if _, exists := byDependency[dependency]; exists {
			_ = dependencyRows.Close()
			return buildEventStoreRows{}, errors.New("Event Store dependency is duplicated")
		}
		byDependency[dependency] = relation
		result.dependencyCount++
	}
	if err := dependencyRows.Err(); err != nil {
		_ = dependencyRows.Close()
		return buildEventStoreRows{}, err
	}
	if err := dependencyRows.Close(); err != nil {
		return buildEventStoreRows{}, err
	}
	if err := modulecore.ValidateEventEnvelopeGraph(events); err != nil {
		return buildEventStoreRows{}, err
	}
	if !equalBuildEventStoreDependencies(eventDependenciesFromRows(result.envelopes), result.dependencies) {
		return buildEventStoreRows{}, errors.New("Event Store dependency graph does not match envelopes")
	}
	return result, nil
}

func assignBuildEventStoreSequences(sourceRows buildEventStoreRows, events []modulecore.EventEnvelope) (map[modulecore.EventID]modulecore.EventEnvelope, error) {
	var maximum int64
	for _, item := range sourceRows.envelopes {
		sequence := int64(item.event.EventSeq)
		if sequence > maximum {
			maximum = sequence
		}
	}
	assigned := make(map[modulecore.EventID]modulecore.EventEnvelope, len(events))
	for _, event := range events {
		if event.EventSeq != 0 {
			return nil, errors.New("migration plan event_seq must be unassigned")
		}
		if maximum == int64(^uint64(0)>>1) {
			return nil, errors.New("event_seq exhausted")
		}
		maximum++
		event.EventSeq = modulecore.EventSeq(maximum)
		if _, exists := assigned[event.EventID]; exists {
			return nil, errors.New("migration plan has duplicate event ID")
		}
		assigned[event.EventID] = event
	}
	return assigned, nil
}

func eventDependenciesFromRows(envelopes map[modulecore.EventID]buildEventStoreEnvelope) map[modulecore.EventID]map[modulecore.EventID]string {
	result := make(map[modulecore.EventID]map[modulecore.EventID]string, len(envelopes))
	for eventID, item := range envelopes {
		dependencies := make(map[modulecore.EventID]string, 1+len(item.event.DependencyEventIDs))
		if item.event.CausationEventID != "" {
			dependencies[item.event.CausationEventID] = "causation"
		}
		for _, dependencyID := range item.event.DependencyEventIDs {
			dependencies[dependencyID] = "dependency"
		}
		if len(dependencies) > 0 {
			result[eventID] = dependencies
		}
	}
	return result
}

func plannedDependencies(planned map[modulecore.EventID]modulecore.EventEnvelope) map[modulecore.EventID]map[modulecore.EventID]string {
	result := make(map[modulecore.EventID]map[modulecore.EventID]string, len(planned))
	for eventID, event := range planned {
		dependencies := make(map[modulecore.EventID]string, 1+len(event.DependencyEventIDs))
		if event.CausationEventID != "" {
			dependencies[event.CausationEventID] = "causation"
		}
		for _, dependencyID := range event.DependencyEventIDs {
			dependencies[dependencyID] = "dependency"
		}
		if len(dependencies) > 0 {
			result[eventID] = dependencies
		}
	}
	return result
}

func countBuildEventStoreDependencies(dependencies map[modulecore.EventID]map[modulecore.EventID]string) int {
	count := 0
	for _, byDependency := range dependencies {
		count += len(byDependency)
	}
	return count
}

func mergeBuildEventStoreDependencies(left, right map[modulecore.EventID]map[modulecore.EventID]string) map[modulecore.EventID]map[modulecore.EventID]string {
	merged := make(map[modulecore.EventID]map[modulecore.EventID]string, len(left)+len(right))
	for owner, dependencies := range left {
		merged[owner] = make(map[modulecore.EventID]string, len(dependencies))
		for dependency, relation := range dependencies {
			merged[owner][dependency] = relation
		}
	}
	for owner, dependencies := range right {
		if merged[owner] == nil {
			merged[owner] = make(map[modulecore.EventID]string, len(dependencies))
		}
		for dependency, relation := range dependencies {
			merged[owner][dependency] = relation
		}
	}
	return merged
}

func equalBuildEventStoreDependencies(left, right map[modulecore.EventID]map[modulecore.EventID]string) bool {
	if len(left) != len(right) {
		return false
	}
	for owner, leftDependencies := range left {
		rightDependencies, ok := right[owner]
		if !ok || len(leftDependencies) != len(rightDependencies) {
			return false
		}
		for dependency, leftRelation := range leftDependencies {
			if rightDependencies[dependency] != leftRelation {
				return false
			}
		}
	}
	return true
}

func sameBuildEventEnvelope(left, right modulecore.EventEnvelope) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func verifyBuildEventStoreOutput(sourceRows, outputRows buildEventStoreRows, planned map[modulecore.EventID]modulecore.EventEnvelope) error {
	if outputRows.envelopeCount != sourceRows.envelopeCount+len(planned) {
		return errors.New("Event Store envelope count differs from source and plan")
	}
	if outputRows.dciCount != len(planned) {
		return errors.New("Event Store DCI event count differs from plan")
	}
	for eventID, sourceItem := range sourceRows.envelopes {
		outputItem, ok := outputRows.envelopes[eventID]
		if !ok || outputItem.rawJSON != sourceItem.rawJSON || !sameBuildEventEnvelope(outputItem.event, sourceItem.event) {
			return errors.New("non-DCI Event Store envelope changed during build")
		}
	}
	for eventID, expected := range planned {
		outputItem, ok := outputRows.envelopes[eventID]
		if !ok || !strings.HasPrefix(outputItem.event.EventType, "dci.") {
			return errors.New("planned DCI Event Store envelope is missing")
		}
		expectedJSON, err := json.Marshal(expected)
		if err != nil || outputItem.rawJSON != string(expectedJSON) || !sameBuildEventEnvelope(outputItem.event, expected) {
			return errors.New("planned Event Store envelope differs from plan")
		}
	}
	for eventID, outputItem := range outputRows.envelopes {
		if _, ok := sourceRows.envelopes[eventID]; ok {
			continue
		}
		if _, ok := planned[eventID]; !ok || !strings.HasPrefix(outputItem.event.EventType, "dci.") {
			return errors.New("Event Store contains an unexpected envelope")
		}
	}

	expectedDependencies := mergeBuildEventStoreDependencies(sourceRows.dependencies, plannedDependencies(planned))
	if !equalBuildEventStoreDependencies(expectedDependencies, outputRows.dependencies) {
		return errors.New("Event Store dependency rows differ from source and plan")
	}
	for owner, dependencies := range outputRows.dependencies {
		if _, plannedOwner := planned[owner]; plannedOwner {
			continue
		}
		for dependency := range dependencies {
			if _, plannedDependency := planned[dependency]; plannedDependency {
				return errors.New("nonplanned Event Store row depends on planned event")
			}
		}
	}
	return nil
}

func buildEventStoreNonDCIExcluder(planned map[modulecore.EventID]modulecore.EventEnvelope) logicalRowExcluder {
	plannedIDs := make(map[string]struct{}, len(planned))
	for eventID := range planned {
		plannedIDs[string(eventID)] = struct{}{}
	}
	return func(table string, columns []string, values []any) (bool, error) {
		if table != "event_envelope" && table != "event_dependency" {
			return false, nil
		}
		index := -1
		for columnIndex, column := range columns {
			if column == "event_id" {
				index = columnIndex
				break
			}
		}
		if index < 0 || index >= len(values) {
			return false, errors.New("Event Store logical hash event_id column is missing")
		}
		_, excluded := plannedIDs[readText(values[index])]
		return excluded, nil
	}
}

func cleanupBuiltEventStoreTarget(path string) {
	_ = os.Remove(path)
	for _, suffix := range sqliteSidecarSuffixes {
		_ = os.Remove(path + suffix)
	}
}

func buildEventStoreError(code string) error {
	if code == "" {
		code = "build_event_store"
	}
	return newCodedError(code, "offline Event Store build failed")
}

func buildEventStoreCauseError(cause error, fallback string) error {
	if cause == nil {
		return buildEventStoreError(fallback)
	}
	if errors.Is(cause, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return buildEventStoreError(errorCode(cause, fallback))
}

func buildEventStoreCauseErrorResult(cause error, fallback string) (buildEventStoreEvidence, error) {
	return buildEventStoreEvidence{}, buildEventStoreCauseError(cause, fallback)
}
