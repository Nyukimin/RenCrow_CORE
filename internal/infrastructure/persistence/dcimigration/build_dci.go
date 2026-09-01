package dcimigration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"runtime"
	"strings"
	"time"

	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/dci"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// buildDCIEvidence is the private, path-free proof for one offline canonical
// DCI database. It contains no identity values, paths, or payloads.
type buildDCIEvidence struct {
	OutputSchemaSHA256  string
	OutputLogicalSHA256 string

	TraceRows                int
	StepRows                 int
	EvidenceRows             int
	QueryTermRows            int
	AuthenticatedTraces      int
	LegacyUnattributedTraces int
	DistinctActionIDs        int
	DistinctTraceIDs         int
	DistinctStepEventIDs     int
	DistinctEvidenceIDs      int
	DistinctCreatedEventIDs  int
	LegacyKeyMarkers         int
	OrphanActionRefs         int
	ForeignKeyViolations     int
	QuickCheckOK             int
	SidecarZero              int
}

type buildDCIOutputRows struct {
	traceRows                int
	stepRows                 int
	evidenceRows             int
	queryTermRows            int
	authenticatedTraces      int
	legacyUnattributedTraces int
	distinctActionIDs        map[string]struct{}
	distinctTraceIDs         map[string]struct{}
	distinctStepEventIDs     map[string]struct{}
	distinctEvidenceIDs      map[string]struct{}
	distinctCreatedEventIDs  map[string]struct{}
	legacyKeyMarkers         int
	orphanActionRefs         int
}

// buildDCIAfterCreate is a package-local seam for deterministic post-create
// failure and tamper tests. Production uses the no-op default.
var buildDCIAfterCreate = func(string) error { return nil }

// createBuiltDCI materializes the retained migration plan through the DCI
// owner's canonical snapshot API, then verifies the resulting v2 database
// read-only. It creates no production output and never re-plans identities.
func createBuiltDCI(ctx context.Context, target string, snapshot sourceSnapshot, plan migrationPlan) (evidence buildDCIEvidence, err error) {
	if ctx == nil {
		return buildDCIEvidence{}, buildDCIError("invalid_context")
	}
	if err := ctx.Err(); err != nil {
		return buildDCIEvidence{}, err
	}

	records, err := materializeMigrationRecords(ctx, snapshot, plan)
	if err != nil {
		return buildDCIEvidence{}, buildDCICauseError(err, "plan_invalid")
	}
	if err := validateBuildDCIRecords(snapshot, plan, records); err != nil {
		return buildDCIEvidence{}, buildDCICauseError(err, "plan_invalid")
	}
	if err := ctx.Err(); err != nil {
		return buildDCIEvidence{}, err
	}

	targetPath, err := validateFreshProjectionTarget(target)
	if err != nil {
		return buildDCIEvidence{}, buildDCICauseError(err, "unsafe_path")
	}
	if err := ctx.Err(); err != nil {
		return buildDCIEvidence{}, err
	}

	created := false
	defer func() {
		if err != nil && created {
			cleanupBuiltDCITarget(targetPath)
		}
	}()

	if err := dci.CreateMigrationSnapshot(ctx, targetPath, records); err != nil {
		return buildDCIEvidence{}, buildDCICauseError(err, "create_output")
	}
	created = true
	if err := ctx.Err(); err != nil {
		return buildDCIEvidence{}, err
	}
	if err := buildDCIAfterCreate(targetPath); err != nil {
		return buildDCIEvidence{}, buildDCICauseError(err, "post_create")
	}
	if err := rejectCapturedSQLiteSidecars(targetPath); err != nil {
		return buildDCIEvidence{}, buildDCIError("output_sidecar")
	}

	if err := verifyBuildDCIOwnerResults(ctx, targetPath, records); err != nil {
		return buildDCIEvidence{}, buildDCICauseError(err, "owner_verify")
	}
	if err := ctx.Err(); err != nil {
		return buildDCIEvidence{}, err
	}

	db, err := openSQLiteReadOnly(ctx, targetPath)
	if err != nil {
		return buildDCIEvidence{}, buildDCIError("readonly_verify")
	}
	rows, readErr := readBuildDCIOutput(ctx, db, records)
	logical, logicalErr := hashSQLiteLogical(ctx, db, nil)
	quickErr := captureSQLiteQuickCheck(ctx, db)
	foreignKeyViolations, foreignKeyErr := countForeignKeyViolations(ctx, db)
	closeErr := db.Close()
	if readErr != nil {
		return buildDCIEvidence{}, buildDCICauseError(readErr, "readonly_verify")
	}
	if logicalErr != nil {
		return buildDCIEvidence{}, buildDCICauseError(logicalErr, "logical_hash")
	}
	if quickErr != nil {
		return buildDCIEvidence{}, buildDCICauseError(quickErr, "quick_check")
	}
	if foreignKeyErr != nil {
		return buildDCIEvidence{}, buildDCICauseError(foreignKeyErr, "foreign_key_check")
	}
	if closeErr != nil {
		return buildDCIEvidence{}, buildDCIError("readonly_verify")
	}
	if foreignKeyViolations != 0 {
		return buildDCIEvidence{}, buildDCIError("foreign_key_check")
	}
	if err := verifyBuildDCIOutput(rows, records); err != nil {
		return buildDCIEvidence{}, buildDCICauseError(err, "readonly_verify")
	}
	if logical.Schema == "" || logical.Full == "" {
		return buildDCIEvidence{}, buildDCIError("logical_hash")
	}
	if runtime.GOOS != "windows" {
		info, statErr := os.Lstat(targetPath)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return buildDCIEvidence{}, buildDCIError("output_permissions")
		}
	}
	if err := syncCaptureFile(targetPath); err != nil {
		return buildDCIEvidence{}, buildDCIError("output_sync")
	}
	if err := rejectCapturedSQLiteSidecars(targetPath); err != nil {
		return buildDCIEvidence{}, buildDCIError("output_sidecar")
	}
	if err := ctx.Err(); err != nil {
		return buildDCIEvidence{}, err
	}

	evidence = buildDCIEvidence{
		OutputSchemaSHA256:       logical.Schema,
		OutputLogicalSHA256:      logical.Full,
		TraceRows:                rows.traceRows,
		StepRows:                 rows.stepRows,
		EvidenceRows:             rows.evidenceRows,
		QueryTermRows:            rows.queryTermRows,
		AuthenticatedTraces:      rows.authenticatedTraces,
		LegacyUnattributedTraces: rows.legacyUnattributedTraces,
		DistinctActionIDs:        len(rows.distinctActionIDs),
		DistinctTraceIDs:         len(rows.distinctTraceIDs),
		DistinctStepEventIDs:     len(rows.distinctStepEventIDs),
		DistinctEvidenceIDs:      len(rows.distinctEvidenceIDs),
		DistinctCreatedEventIDs:  len(rows.distinctCreatedEventIDs),
		LegacyKeyMarkers:         rows.legacyKeyMarkers,
		OrphanActionRefs:         rows.orphanActionRefs,
		ForeignKeyViolations:     foreignKeyViolations,
		QuickCheckOK:             1,
		SidecarZero:              1,
	}
	created = false
	return evidence, nil
}

func validateBuildDCIRecords(snapshot sourceSnapshot, plan migrationPlan, records []dci.MigrationRecord) error {
	if len(plan.Events) != plan.actual.TotalEvents || plan.actual.TotalEvents < 0 {
		return errors.New("migration plan event count is inconsistent")
	}
	if len(records) != plan.actual.Searches || len(records) != len(snapshot.Searches) {
		return errors.New("materialized DCI trace count is inconsistent")
	}
	steps, evidence, terms := 0, 0, 0
	seenActions := make(map[modulecore.ActionID]struct{}, len(records))
	seenTraces := make(map[modulecore.TraceID]struct{}, len(records))
	seenStepEvents := make(map[modulecore.EventID]struct{})
	seenEvidence := make(map[modulecore.EvidenceID]struct{})
	seenCreated := make(map[modulecore.EventID]struct{})
	for _, record := range records {
		if err := domaindci.ValidateStoredSearchResult(record.Result); err != nil {
			return errors.New("materialized DCI result is invalid")
		}
		if record.Result.Trace.IdempotencyKey != "" {
			return errors.New("materialized DCI idempotency key is not empty")
		}
		if _, exists := seenActions[record.Result.Trace.ActionID]; exists {
			return errors.New("materialized DCI action IDs are duplicated")
		}
		if _, exists := seenTraces[record.Result.Trace.TraceID]; exists {
			return errors.New("materialized DCI trace IDs are duplicated")
		}
		seenActions[record.Result.Trace.ActionID] = struct{}{}
		seenTraces[record.Result.Trace.TraceID] = struct{}{}
		steps += len(record.Result.Trace.Steps)
		evidence += len(record.Result.Pack.Evidence)
		terms += len(record.Result.Pack.DerivedTerms)
		if len(record.EvidenceCreatedAt) != len(record.Result.Pack.Evidence) {
			return errors.New("materialized DCI evidence timestamps are inconsistent")
		}
		for _, step := range record.Result.Trace.Steps {
			if _, exists := seenStepEvents[step.EventID]; exists {
				return errors.New("materialized DCI step event IDs are duplicated")
			}
			seenStepEvents[step.EventID] = struct{}{}
		}
		for _, item := range record.Result.Pack.Evidence {
			if _, exists := seenEvidence[item.EvidenceID]; exists {
				return errors.New("materialized DCI evidence IDs are duplicated")
			}
			if _, exists := seenCreated[item.CreatedByEventID]; exists {
				return errors.New("materialized DCI created event IDs are duplicated")
			}
			if timestamp, ok := record.EvidenceCreatedAt[item.EvidenceID]; !ok || timestamp.IsZero() {
				return errors.New("materialized DCI evidence timestamp is missing")
			}
			seenEvidence[item.EvidenceID] = struct{}{}
			seenCreated[item.CreatedByEventID] = struct{}{}
		}
	}
	if steps != plan.actual.ReadEvents || evidence != plan.actual.EvidenceEvents {
		return errors.New("materialized DCI row counts do not match the retained plan")
	}
	if terms < 0 {
		return errors.New("materialized DCI term count is invalid")
	}
	return nil
}

func verifyBuildDCIOwnerResults(ctx context.Context, target string, records []dci.MigrationRecord) error {
	store, err := dci.NewSQLiteStore(target)
	if err != nil {
		return err
	}
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			_ = store.Close()
			return err
		}
		got, found, err := store.FindSearchResultByActionID(ctx, record.Result.Trace.ActionID)
		if err != nil {
			_ = store.Close()
			return err
		}
		if !found || got.Trace.IdempotencyKey != "" || !equalBuildDCIResults(record.Result, got) {
			_ = store.Close()
			return errors.New("owner DCI result does not match materialized result")
		}
	}
	return store.Close()
}

func equalBuildDCIResults(want, got domaindci.SearchResult) bool {
	if want.Trace.IdempotencyKey != "" || got.Trace.IdempotencyKey != "" {
		return false
	}
	want = normalizeBuildDCIResult(want)
	got = normalizeBuildDCIResult(got)
	if !reflect.DeepEqual(want, got) {
		return false
	}
	wantJSON, wantErr := json.Marshal(want)
	gotJSON, gotErr := json.Marshal(got)
	return wantErr == nil && gotErr == nil && bytes.Equal(wantJSON, gotJSON)
}

func normalizeBuildDCIResult(result domaindci.SearchResult) domaindci.SearchResult {
	result.Trace.StartedAt = result.Trace.StartedAt.UTC()
	result.Trace.EndedAt = result.Trace.EndedAt.UTC()
	if result.Trace.CorpusScope == nil {
		result.Trace.CorpusScope = []string{}
	}
	if result.Trace.Steps == nil {
		result.Trace.Steps = []domaindci.SearchStep{}
	}
	for index := range result.Trace.Steps {
		result.Trace.Steps[index].CreatedAt = result.Trace.Steps[index].CreatedAt.UTC()
	}
	if result.Pack.CorpusScope == nil {
		result.Pack.CorpusScope = []string{}
	}
	if result.Pack.Evidence == nil {
		result.Pack.Evidence = []domaindci.Evidence{}
	}
	if result.Pack.DerivedTerms == nil {
		result.Pack.DerivedTerms = []string{}
	}
	if result.Pack.Limitations == nil {
		result.Pack.Limitations = []string{}
	}
	return result
}

func readBuildDCIOutput(ctx context.Context, db *sql.DB, records []dci.MigrationRecord) (buildDCIOutputRows, error) {
	if err := ctx.Err(); err != nil {
		return buildDCIOutputRows{}, err
	}
	if err := validateBuildDCIReadOnlySchema(ctx, db); err != nil {
		return buildDCIOutputRows{}, err
	}
	rows := buildDCIOutputRows{
		distinctActionIDs:       make(map[string]struct{}, len(records)),
		distinctTraceIDs:        make(map[string]struct{}, len(records)),
		distinctStepEventIDs:    make(map[string]struct{}),
		distinctEvidenceIDs:     make(map[string]struct{}),
		distinctCreatedEventIDs: make(map[string]struct{}),
	}
	expectedActions := make(map[string]struct{}, len(records))
	expectedEvidence := make(map[string]struct{})
	expectedStepEvents := make(map[string]struct{})
	expectedCreatedEvents := make(map[string]struct{})
	for _, record := range records {
		expectedActions[string(record.Result.Trace.ActionID)] = struct{}{}
		for _, step := range record.Result.Trace.Steps {
			expectedStepEvents[string(step.EventID)] = struct{}{}
		}
		for _, item := range record.Result.Pack.Evidence {
			expectedEvidence[string(item.EvidenceID)] = struct{}{}
			expectedCreatedEvents[string(item.CreatedByEventID)] = struct{}{}
		}
	}

	traceRows, err := db.QueryContext(ctx, `SELECT action_id, trace_id, actor_attribution, actor_kind, actor_id, idempotency_key FROM dci_search_trace`)
	if err != nil {
		return buildDCIOutputRows{}, err
	}
	seenActions := make(map[string]struct{}, len(records))
	seenTraces := make(map[string]struct{}, len(records))
	for traceRows.Next() {
		if rows.traceRows >= maxLogicalRows {
			_ = traceRows.Close()
			return buildDCIOutputRows{}, errors.New("DCI trace row bound exceeded")
		}
		var actionID, traceID, attribution, actorKind, actorID, idempotencyKey string
		if err := traceRows.Scan(&actionID, &traceID, &attribution, &actorKind, &actorID, &idempotencyKey); err != nil {
			_ = traceRows.Close()
			return buildDCIOutputRows{}, err
		}
		if actionID == "" || traceID == "" || strings.TrimSpace(actionID) != actionID || strings.TrimSpace(traceID) != traceID {
			_ = traceRows.Close()
			return buildDCIOutputRows{}, errors.New("DCI trace identity is empty or noncanonical")
		}
		if _, exists := expectedActions[actionID]; !exists {
			rows.orphanActionRefs++
			_ = traceRows.Close()
			return buildDCIOutputRows{}, errors.New("DCI output contains an unexpected action")
		}
		if _, exists := seenActions[actionID]; exists {
			_ = traceRows.Close()
			return buildDCIOutputRows{}, errors.New("DCI action IDs are duplicated")
		}
		if _, exists := seenTraces[traceID]; exists {
			_ = traceRows.Close()
			return buildDCIOutputRows{}, errors.New("DCI trace IDs are duplicated")
		}
		seenActions[actionID] = struct{}{}
		seenTraces[traceID] = struct{}{}
		if idempotencyKey != "" {
			rows.legacyKeyMarkers++
			_ = traceRows.Close()
			return buildDCIOutputRows{}, errors.New("DCI idempotency key is not empty")
		}
		switch domaindci.ActorAttribution(attribution) {
		case domaindci.ActorAttributionAuthenticated:
			if actorKind == "" || actorID == "" {
				_ = traceRows.Close()
				return buildDCIOutputRows{}, errors.New("authenticated DCI actor identity is empty")
			}
			rows.authenticatedTraces++
		case domaindci.ActorAttributionLegacyUnattributed:
			if actorKind != "" || actorID != "" {
				_ = traceRows.Close()
				return buildDCIOutputRows{}, errors.New("legacy DCI actor identity is not empty")
			}
			rows.legacyUnattributedTraces++
		default:
			_ = traceRows.Close()
			return buildDCIOutputRows{}, errors.New("DCI actor attribution is invalid")
		}
		rows.traceRows++
	}
	if err := traceRows.Err(); err != nil {
		_ = traceRows.Close()
		return buildDCIOutputRows{}, err
	}
	if err := traceRows.Close(); err != nil {
		return buildDCIOutputRows{}, err
	}
	if len(seenActions) != len(expectedActions) || len(seenTraces) != len(expectedActions) {
		return buildDCIOutputRows{}, errors.New("DCI trace rows do not match materialized actions")
	}
	rows.distinctActionIDs = seenActions
	rows.distinctTraceIDs = seenTraces

	stepRows, err := db.QueryContext(ctx, `SELECT action_id, event_id FROM dci_search_step`)
	if err != nil {
		return buildDCIOutputRows{}, err
	}
	seenStepEvents := make(map[string]struct{}, len(expectedStepEvents))
	for stepRows.Next() {
		if rows.stepRows >= maxLogicalRows {
			_ = stepRows.Close()
			return buildDCIOutputRows{}, errors.New("DCI step row bound exceeded")
		}
		var actionID, eventID string
		if err := stepRows.Scan(&actionID, &eventID); err != nil {
			_ = stepRows.Close()
			return buildDCIOutputRows{}, err
		}
		if actionID == "" || eventID == "" || strings.TrimSpace(actionID) != actionID || strings.TrimSpace(eventID) != eventID {
			_ = stepRows.Close()
			return buildDCIOutputRows{}, errors.New("DCI step identity is empty or noncanonical")
		}
		if _, exists := seenActions[actionID]; !exists {
			rows.orphanActionRefs++
			_ = stepRows.Close()
			return buildDCIOutputRows{}, errors.New("DCI step references an unknown action")
		}
		if _, expected := expectedStepEvents[eventID]; !expected {
			_ = stepRows.Close()
			return buildDCIOutputRows{}, errors.New("DCI step event is unexpected")
		}
		if _, exists := seenStepEvents[eventID]; exists {
			_ = stepRows.Close()
			return buildDCIOutputRows{}, errors.New("DCI step event IDs are duplicated")
		}
		seenStepEvents[eventID] = struct{}{}
		rows.stepRows++
	}
	if err := stepRows.Err(); err != nil {
		_ = stepRows.Close()
		return buildDCIOutputRows{}, err
	}
	if err := stepRows.Close(); err != nil {
		return buildDCIOutputRows{}, err
	}
	if len(seenStepEvents) != len(expectedStepEvents) {
		return buildDCIOutputRows{}, errors.New("DCI step rows do not match materialized steps")
	}
	rows.distinctStepEventIDs = seenStepEvents

	evidenceRows, err := db.QueryContext(ctx, `SELECT evidence_id, action_id, created_by_event_id, created_at FROM dci_evidence`)
	if err != nil {
		return buildDCIOutputRows{}, err
	}
	seenEvidence := make(map[string]struct{}, len(expectedEvidence))
	seenCreated := make(map[string]struct{}, len(expectedCreatedEvents))
	expectedTimestamps := make(map[string]string, len(expectedEvidence))
	for _, record := range records {
		for evidenceID, timestamp := range record.EvidenceCreatedAt {
			expectedTimestamps[string(evidenceID)] = formatBuildDCITime(timestamp)
		}
	}
	for evidenceRows.Next() {
		if rows.evidenceRows >= maxLogicalRows {
			_ = evidenceRows.Close()
			return buildDCIOutputRows{}, errors.New("DCI evidence row bound exceeded")
		}
		var evidenceID, actionID, createdByEventID, createdAt string
		if err := evidenceRows.Scan(&evidenceID, &actionID, &createdByEventID, &createdAt); err != nil {
			_ = evidenceRows.Close()
			return buildDCIOutputRows{}, err
		}
		if evidenceID == "" || actionID == "" || createdByEventID == "" || strings.TrimSpace(evidenceID) != evidenceID || strings.TrimSpace(actionID) != actionID || strings.TrimSpace(createdByEventID) != createdByEventID {
			_ = evidenceRows.Close()
			return buildDCIOutputRows{}, errors.New("DCI evidence identity is empty or noncanonical")
		}
		if _, exists := seenActions[actionID]; !exists {
			rows.orphanActionRefs++
			_ = evidenceRows.Close()
			return buildDCIOutputRows{}, errors.New("DCI evidence references an unknown action")
		}
		if _, expected := expectedEvidence[evidenceID]; !expected {
			_ = evidenceRows.Close()
			return buildDCIOutputRows{}, errors.New("DCI evidence is unexpected")
		}
		if _, expected := expectedCreatedEvents[createdByEventID]; !expected {
			_ = evidenceRows.Close()
			return buildDCIOutputRows{}, errors.New("DCI evidence created event is unexpected")
		}
		if _, exists := seenEvidence[evidenceID]; exists {
			_ = evidenceRows.Close()
			return buildDCIOutputRows{}, errors.New("DCI evidence IDs are duplicated")
		}
		if _, exists := seenCreated[createdByEventID]; exists {
			_ = evidenceRows.Close()
			return buildDCIOutputRows{}, errors.New("DCI created event IDs are duplicated")
		}
		if expectedTimestamps[evidenceID] != createdAt {
			_ = evidenceRows.Close()
			return buildDCIOutputRows{}, errors.New("DCI evidence timestamp does not match materialized history")
		}
		seenEvidence[evidenceID] = struct{}{}
		seenCreated[createdByEventID] = struct{}{}
		rows.evidenceRows++
	}
	if err := evidenceRows.Err(); err != nil {
		_ = evidenceRows.Close()
		return buildDCIOutputRows{}, err
	}
	if err := evidenceRows.Close(); err != nil {
		return buildDCIOutputRows{}, err
	}
	if len(seenEvidence) != len(expectedEvidence) || len(seenCreated) != len(expectedCreatedEvents) {
		return buildDCIOutputRows{}, errors.New("DCI evidence rows do not match materialized evidence")
	}
	rows.distinctEvidenceIDs = seenEvidence
	rows.distinctCreatedEventIDs = seenCreated

	termRows, err := db.QueryContext(ctx, `SELECT action_id, term FROM dci_query_terms`)
	if err != nil {
		return buildDCIOutputRows{}, err
	}
	for termRows.Next() {
		if rows.queryTermRows >= maxLogicalRows {
			_ = termRows.Close()
			return buildDCIOutputRows{}, errors.New("DCI query term row bound exceeded")
		}
		var actionID, term string
		if err := termRows.Scan(&actionID, &term); err != nil {
			_ = termRows.Close()
			return buildDCIOutputRows{}, err
		}
		if actionID == "" || term == "" || strings.TrimSpace(actionID) != actionID || strings.TrimSpace(term) != term {
			_ = termRows.Close()
			return buildDCIOutputRows{}, errors.New("DCI query term identity is empty or noncanonical")
		}
		if _, exists := seenActions[actionID]; !exists {
			rows.orphanActionRefs++
			_ = termRows.Close()
			return buildDCIOutputRows{}, errors.New("DCI query term references an unknown action")
		}
		rows.queryTermRows++
	}
	if err := termRows.Err(); err != nil {
		_ = termRows.Close()
		return buildDCIOutputRows{}, err
	}
	if err := termRows.Close(); err != nil {
		return buildDCIOutputRows{}, err
	}
	return rows, nil
}

func validateBuildDCIReadOnlySchema(ctx context.Context, db *sql.DB) error {
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version != 2 {
		return errors.New("DCI output schema version is not v2")
	}
	traceColumns, err := db.QueryContext(ctx, "PRAGMA table_info('dci_search_trace')")
	if err != nil {
		return err
	}
	for traceColumns.Next() {
		var cid int
		var name, columnType string
		var notNull, primary int
		var defaultValue any
		if err := traceColumns.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primary); err != nil {
			_ = traceColumns.Close()
			return err
		}
		if name == "event_id" {
			_ = traceColumns.Close()
			return errors.New("legacy DCI event_id trace column remains")
		}
	}
	if err := traceColumns.Err(); err != nil {
		_ = traceColumns.Close()
		return err
	}
	return traceColumns.Close()
}

func formatBuildDCITime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func verifyBuildDCIOutput(observed buildDCIOutputRows, records []dci.MigrationRecord) error {
	expectedSteps, expectedEvidence, expectedTerms := 0, 0, 0
	expectedAuthenticated, expectedLegacy := 0, 0
	for _, record := range records {
		expectedSteps += len(record.Result.Trace.Steps)
		expectedEvidence += len(record.Result.Pack.Evidence)
		expectedTerms += len(record.Result.Pack.DerivedTerms)
		switch record.Result.Trace.ActorAttribution {
		case domaindci.ActorAttributionAuthenticated:
			expectedAuthenticated++
		case domaindci.ActorAttributionLegacyUnattributed:
			expectedLegacy++
		default:
			return errors.New("materialized DCI actor attribution is invalid")
		}
	}
	if observed.traceRows != len(records) || observed.stepRows != expectedSteps || observed.evidenceRows != expectedEvidence || observed.queryTermRows != expectedTerms {
		return errors.New("DCI output row counts do not match materialized records")
	}
	if observed.authenticatedTraces != expectedAuthenticated || observed.legacyUnattributedTraces != expectedLegacy {
		return errors.New("DCI output actor counts do not match materialized records")
	}
	if len(observed.distinctActionIDs) != len(records) || len(observed.distinctTraceIDs) != len(records) || len(observed.distinctStepEventIDs) != expectedSteps || len(observed.distinctEvidenceIDs) != expectedEvidence || len(observed.distinctCreatedEventIDs) != expectedEvidence {
		return errors.New("DCI output distinct identity counts do not match materialized records")
	}
	if observed.legacyKeyMarkers != 0 || observed.orphanActionRefs != 0 {
		return errors.New("DCI output contains legacy keys or orphan action references")
	}
	return nil
}

func cleanupBuiltDCITarget(path string) {
	_ = os.Remove(path)
	for _, suffix := range sqliteSidecarSuffixes {
		_ = os.Remove(path + suffix)
	}
}

func buildDCIError(code string) error {
	return newCodedError(code, "offline DCI build failed")
}

func buildDCICauseError(cause error, fallback string) error {
	if cause == nil {
		return buildDCIError(fallback)
	}
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return cause
	}
	return buildDCIError(errorCode(cause, fallback))
}
