package dcimigration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	_ "modernc.org/sqlite"
)

// l1ProjectionEvidence is measured verification evidence for one projected
// current or archive database. It intentionally contains counts, hashes, and
// zero/healthy counters only; paths, payload text, and legacy/canonical IDs
// remain private to the projection operation.
type l1ProjectionEvidence struct {
	DCIStagingRows          int
	RegistryRows            int
	CanonicalStagingRows    int
	CanonicalRegistryRows   int
	OldStagingRowsRemaining int
	RawTextHashMismatches   int
	RawHashMismatches       int
	PromotedReferences      int
	OrphanRows              int
	ForeignKeyViolations    int
	QuickCheckOK            int
	SidecarZero             int
	SourceSchemaSHA256      string
	OutputSchemaSHA256      string
	SourceNonDCISHA256      string
	OutputNonDCISHA256      string
}

type projectedStagingRow struct {
	ref            l1StagingRef
	canonicalID    modulecore.EvidenceID
	createdEventID modulecore.EventID
	newID          string
	newEventID     string
	newMetaJSON    string
	newMetadata    map[string]any
}

type projectedRegistryRow struct {
	ref         legacyRegistryRef
	newMetaJSON string
	newMetadata map[string]any
}

type l1ProjectionPlan struct {
	staging  []projectedStagingRow
	registry []projectedRegistryRow
}

// createProjectedL1Snapshot clones one captured L1 source and changes only
// the classified DCI rows in the selected origin. archive selects the
// archive staging table; registry projection is current-only.
func createProjectedL1Snapshot(ctx context.Context, source, target string, archive bool, snapshot sourceSnapshot, plan migrationPlan) (evidence l1ProjectionEvidence, err error) {
	if ctx == nil {
		return l1ProjectionEvidence{}, fmt.Errorf("L1 projection context is required")
	}
	if err := ctx.Err(); err != nil {
		return l1ProjectionEvidence{}, err
	}
	if err := validateMaterializationPlanBeforeProjection(snapshot, plan); err != nil {
		return l1ProjectionEvidence{}, err
	}
	sourcePath, err := absolutePath(source)
	if err != nil {
		return l1ProjectionEvidence{}, fmt.Errorf("resolve L1 projection source: %w", err)
	}
	targetPath, err := validateFreshProjectionTarget(target)
	if err != nil {
		return l1ProjectionEvidence{}, err
	}
	if err := validateProjectionSource(sourcePath); err != nil {
		return l1ProjectionEvidence{}, err
	}
	projectionPlan, sourceData, sourceHash, sourceHashKey, err := prepareL1Projection(ctx, snapshot, plan, archive)
	if err != nil {
		return l1ProjectionEvidence{}, err
	}

	created := false
	defer func() {
		if err != nil && created {
			cleanupProjectedL1Target(targetPath)
		}
	}()
	placeholder, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return l1ProjectionEvidence{}, fmt.Errorf("reserve fresh L1 projection target: %w", err)
	}
	created = true
	if closeErr := placeholder.Close(); closeErr != nil {
		return l1ProjectionEvidence{}, fmt.Errorf("close fresh L1 projection target placeholder: %w", closeErr)
	}
	if _, err := captureSQLiteBackup(ctx, sourcePath, targetPath); err != nil {
		return l1ProjectionEvidence{}, err
	}
	db, err := openSQLiteWritable(ctx, targetPath)
	if err != nil {
		return l1ProjectionEvidence{}, err
	}
	defer func() {
		if db != nil {
			_ = db.Close()
		}
	}()
	if err := preflightProjectedStagingCollisions(ctx, db, projectionPlan.staging, archive); err != nil {
		_ = db.Close()
		return l1ProjectionEvidence{}, err
	}
	if err := preflightProjectedRegistryRows(ctx, db, projectionPlan.registry); err != nil {
		_ = db.Close()
		return l1ProjectionEvidence{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		_ = db.Close()
		return l1ProjectionEvidence{}, fmt.Errorf("begin L1 projection transaction: %w", err)
	}
	if err := applyProjectedStaging(ctx, tx, projectionPlan.staging, archive); err != nil {
		_ = tx.Rollback()
		_ = db.Close()
		return l1ProjectionEvidence{}, err
	}
	if err := applyProjectedRegistry(ctx, tx, projectionPlan.registry); err != nil {
		_ = tx.Rollback()
		_ = db.Close()
		return l1ProjectionEvidence{}, err
	}
	if err := tx.Commit(); err != nil {
		_ = db.Close()
		return l1ProjectionEvidence{}, fmt.Errorf("commit L1 projection: %w", err)
	}
	if err := ctx.Err(); err != nil {
		_ = db.Close()
		return l1ProjectionEvidence{}, err
	}
	if closeErr := db.Close(); closeErr != nil {
		return l1ProjectionEvidence{}, fmt.Errorf("close projected L1 database: %w", closeErr)
	}
	db = nil

	verificationDB, err := openSQLiteReadOnly(ctx, targetPath)
	if err != nil {
		return l1ProjectionEvidence{}, err
	}
	evidence, err = verifyProjectedL1Snapshot(ctx, verificationDB, sourceData, sourceHash, sourceHashKey, projectionPlan, archive)
	closeErr := verificationDB.Close()
	if err != nil {
		return l1ProjectionEvidence{}, err
	}
	if closeErr != nil {
		return l1ProjectionEvidence{}, fmt.Errorf("close verified projected L1 database: %w", closeErr)
	}
	if err := syncCaptureFile(targetPath); err != nil {
		return l1ProjectionEvidence{}, fmt.Errorf("sync projected L1 database: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return l1ProjectionEvidence{}, err
	}
	if err := rejectCapturedSQLiteSidecars(targetPath); err != nil {
		return l1ProjectionEvidence{}, err
	}
	evidence.SidecarZero = 1
	created = false
	return evidence, nil
}

func validateMaterializationPlanBeforeProjection(snapshot sourceSnapshot, plan migrationPlan) error {
	_, err := validateMaterializationPlan(snapshot, plan)
	return err
}

func validateProjectionSource(path string) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("L1 projection source is missing or unsafe")
	}
	if err := rejectSQLiteSidecars(path); err != nil {
		return err
	}
	return nil
}

func validateFreshProjectionTarget(raw string) (string, error) {
	path, err := absolutePath(raw)
	if err != nil {
		return "", fmt.Errorf("resolve L1 projection target: %w", err)
	}
	if _, err := os.Lstat(path); err == nil {
		return "", fmt.Errorf("L1 projection target already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect L1 projection target: %w", err)
	}
	if err := rejectSQLiteSidecars(path); err != nil {
		return "", err
	}
	parent := filepath.Dir(path)
	parentInfo, err := os.Lstat(parent)
	if err != nil || parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
		return "", fmt.Errorf("L1 projection target parent is missing or unsafe")
	}
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil || !samePath(parent, filepath.Clean(realParent)) {
		return "", fmt.Errorf("L1 projection target parent is not canonical")
	}
	return path, nil
}

func cleanupProjectedL1Target(path string) {
	_ = os.Remove(path)
	for _, suffix := range sqliteSidecarSuffixes {
		_ = os.Remove(path + suffix)
	}
}

func openSQLiteWritable(ctx context.Context, path string) (*sql.DB, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "_pragma=busy_timeout%3d5000&_time_format=sqlite"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open writable L1 projection database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping writable L1 projection database: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable L1 projection foreign keys: %w", err)
	}
	var foreignKeys int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("verify L1 projection foreign keys: %w", err)
	}
	if foreignKeys != 1 {
		_ = db.Close()
		return nil, fmt.Errorf("L1 projection foreign keys are not enabled")
	}
	return db, nil
}

func prepareL1Projection(ctx context.Context, snapshot sourceSnapshot, plan migrationPlan, archive bool) (l1ProjectionPlan, l1SourceData, sourceHashes, string, error) {
	projectionIdentities, err := indexL1ProjectionIdentities(snapshot.currentL1, snapshot.archiveL1)
	if err != nil {
		return l1ProjectionPlan{}, l1SourceData{}, sourceHashes{}, "", err
	}
	var sourceData l1SourceData
	var sourceHashKey string
	if archive {
		sourceData = snapshot.archiveL1
		sourceHashKey = "source_archive"
	} else {
		sourceData = snapshot.currentL1
		sourceHashKey = "source_l1"
	}
	sourceHash, ok := snapshot.SourceHashes[sourceHashKey]
	if !ok || sourceHash.Schema == "" || sourceHash.NonDCI == "" {
		return l1ProjectionPlan{}, l1SourceData{}, sourceHashes{}, "", fmt.Errorf("L1 projection source hashes are missing")
	}
	if sourceData.StagingRefs == nil {
		return l1ProjectionPlan{}, l1SourceData{}, sourceHashes{}, "", fmt.Errorf("L1 projection staging retention is missing")
	}
	if archive && len(sourceData.RegistryRefsByID) != 0 {
		return l1ProjectionPlan{}, l1SourceData{}, sourceHashes{}, "", fmt.Errorf("archive L1 projection must not contain registry refs")
	}
	projection := l1ProjectionPlan{
		staging:  make([]projectedStagingRow, 0, len(sourceData.StagingRefs)),
		registry: make([]projectedRegistryRow, 0, len(sourceData.RegistryRefsByID)),
	}
	stagingIDs := sortedStagingRefIDs(sourceData.StagingRefs)
	seenCanonicalIDs := make(map[string]struct{}, len(stagingIDs))
	seenCanonicalEvents := make(map[string]struct{}, len(stagingIDs))
	for _, stagingID := range stagingIDs {
		if err := ctx.Err(); err != nil {
			return l1ProjectionPlan{}, l1SourceData{}, sourceHashes{}, "", err
		}
		ref := sourceData.StagingRefs[stagingID]
		if ref.ID != stagingID || ref.OriginTable != expectedStagingOrigin(archive) {
			return l1ProjectionPlan{}, l1SourceData{}, sourceHashes{}, "", fmt.Errorf("retained L1 staging ref %q is inconsistent", stagingID)
		}
		searchIDs, ok := plan.searches[ref.SearchID]
		if !ok {
			return l1ProjectionPlan{}, l1SourceData{}, sourceHashes{}, "", fmt.Errorf("L1 staging ref %q has no search mapping", stagingID)
		}
		evidenceIDs, ok := plan.evidence[ref.EvidenceID]
		if !ok {
			return l1ProjectionPlan{}, l1SourceData{}, sourceHashes{}, "", fmt.Errorf("L1 staging ref %q has no evidence mapping", stagingID)
		}
		legacyEvidence, ok := snapshot.Evidence[ref.EvidenceID]
		if !ok || legacyEvidence.SearchID != ref.SearchID {
			return l1ProjectionPlan{}, l1SourceData{}, sourceHashes{}, "", fmt.Errorf("L1 staging ref %q evidence mapping is inconsistent", stagingID)
		}
		if !isLowerHexSHA256(ref.RawHash) || ref.RawTextSHA256 == "" || ref.RawHash != ref.RawTextSHA256 {
			return l1ProjectionPlan{}, l1SourceData{}, sourceHashes{}, "", fmt.Errorf("L1 staging ref %q raw hash retention is invalid", stagingID)
		}
		metadata, encoded, err := canonicalL1Metadata(ref.RawMetaJSON, searchIDs, evidenceIDs, snapshot.Searches[ref.SearchID].Query)
		if err != nil {
			return l1ProjectionPlan{}, l1SourceData{}, sourceHashes{}, "", fmt.Errorf("L1 staging ref %q metadata: %w", stagingID, err)
		}
		newID := fmt.Sprintf("kb:dci:%s:%s", evidenceIDs.createdEventID, ref.RawHash[:12])
		newEventID := string(evidenceIDs.createdEventID)
		if _, exists := seenCanonicalIDs[newID]; exists {
			return l1ProjectionPlan{}, l1SourceData{}, sourceHashes{}, "", fmt.Errorf("L1 projection canonical staging ID collision")
		}
		if _, exists := seenCanonicalEvents[newEventID]; exists {
			return l1ProjectionPlan{}, l1SourceData{}, sourceHashes{}, "", fmt.Errorf("L1 projection canonical event ID collision")
		}
		seenCanonicalIDs[newID] = struct{}{}
		seenCanonicalEvents[newEventID] = struct{}{}
		projection.staging = append(projection.staging, projectedStagingRow{
			ref: ref, canonicalID: evidenceIDs.evidenceID, createdEventID: evidenceIDs.createdEventID,
			newID: newID, newEventID: newEventID, newMetaJSON: encoded, newMetadata: metadata,
		})
	}
	if !archive {
		registryIDs := sortedRegistryRefIDs(sourceData.RegistryRefsByID)
		for _, sourceID := range registryIDs {
			if err := ctx.Err(); err != nil {
				return l1ProjectionPlan{}, l1SourceData{}, sourceHashes{}, "", err
			}
			ref := sourceData.RegistryRefsByID[sourceID]
			if ref.SourceID != sourceID || ref.OriginTable != "l1_source_registry" {
				return l1ProjectionPlan{}, l1SourceData{}, sourceHashes{}, "", fmt.Errorf("retained L1 registry ref %q is inconsistent", sourceID)
			}
			searchIDs, ok := plan.searches[ref.SearchID]
			if !ok {
				return l1ProjectionPlan{}, l1SourceData{}, sourceHashes{}, "", fmt.Errorf("L1 registry ref %q has no search mapping", sourceID)
			}
			evidenceIDs, ok := plan.evidence[ref.EvidenceID]
			if !ok {
				return l1ProjectionPlan{}, l1SourceData{}, sourceHashes{}, "", fmt.Errorf("L1 registry ref %q has no evidence mapping", sourceID)
			}
			legacyEvidence, ok := snapshot.Evidence[ref.EvidenceID]
			projectionIdentity, projectionOK := projectionIdentities[ref.EvidenceID]
			if !ok || legacyEvidence.SearchID != ref.SearchID || !projectionOK || projectionIdentity.SearchID != ref.SearchID || projectionIdentity.SourceID != ref.SourceID {
				return l1ProjectionPlan{}, l1SourceData{}, sourceHashes{}, "", fmt.Errorf("L1 registry ref %q evidence mapping is inconsistent", sourceID)
			}
			metadata, encoded, err := canonicalL1Metadata(ref.RawMetaJSON, searchIDs, evidenceIDs, snapshot.Searches[ref.SearchID].Query)
			if err != nil {
				return l1ProjectionPlan{}, l1SourceData{}, sourceHashes{}, "", fmt.Errorf("L1 registry ref %q metadata: %w", sourceID, err)
			}
			projection.registry = append(projection.registry, projectedRegistryRow{ref: ref, newMetaJSON: encoded, newMetadata: metadata})
		}
	}
	return projection, sourceData, sourceHash, sourceHashKey, nil
}

func expectedStagingOrigin(archive bool) string {
	if archive {
		return "archive_staging"
	}
	return "staging"
}

func sortedStagingRefIDs(refs map[string]l1StagingRef) []string {
	ids := make([]string, 0, len(refs))
	for id := range refs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func sortedRegistryRefIDs(refs map[string]legacyRegistryRef) []string {
	ids := make([]string, 0, len(refs))
	for id := range refs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func canonicalL1Metadata(raw string, searchIDs searchMigrationIDs, evidenceIDs evidenceMigrationIDs, query string) (map[string]any, string, error) {
	metadata, err := decodeMetadata(raw)
	if err != nil {
		return nil, "", err
	}
	delete(metadata, "search_event_id")
	metadata["source_kind"] = "dci"
	metadata["search_action_id"] = string(searchIDs.actionID)
	metadata["trace_id"] = string(searchIDs.traceID)
	metadata["evidence_id"] = string(evidenceIDs.evidenceID)
	metadata["evidence_created_event_id"] = string(evidenceIDs.createdEventID)
	metadata["query"] = query
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, "", err
	}
	return metadata, string(encoded), nil
}

func preflightProjectedStagingCollisions(ctx context.Context, db *sql.DB, rows []projectedStagingRow, archive bool) error {
	table := projectedStagingTable(archive)
	existingIDs, existingEvents, err := existingStagingKeys(ctx, db, table)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if _, exists := existingIDs[row.newID]; exists {
			return fmt.Errorf("L1 projection canonical staging ID already exists")
		}
		if _, exists := existingEvents[row.newEventID]; exists {
			return fmt.Errorf("L1 projection canonical staging event ID already exists")
		}
	}
	return nil
}

func preflightProjectedRegistryRows(ctx context.Context, db *sql.DB, rows []projectedRegistryRow) error {
	for _, row := range rows {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM l1_source_registry WHERE source_id = ? AND meta_json = ?`, row.ref.SourceID, row.ref.RawMetaJSON).Scan(&count); err != nil {
			return fmt.Errorf("preflight L1 registry row: %w", err)
		}
		if count != 1 {
			return fmt.Errorf("L1 registry row source identity changed")
		}
	}
	return nil
}

func existingStagingKeys(ctx context.Context, db *sql.DB, table string) (map[string]struct{}, map[string]struct{}, error) {
	rows, err := db.QueryContext(ctx, "SELECT id, event_id FROM "+quoteSQLiteIdentifier(table))
	if err != nil {
		return nil, nil, fmt.Errorf("preflight L1 staging rows: %w", err)
	}
	defer rows.Close()
	ids := make(map[string]struct{})
	events := make(map[string]struct{})
	for rows.Next() {
		var id, eventID string
		if err := rows.Scan(&id, &eventID); err != nil {
			return nil, nil, fmt.Errorf("scan L1 staging keys: %w", err)
		}
		ids[id] = struct{}{}
		events[eventID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate L1 staging keys: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, fmt.Errorf("close L1 staging keys: %w", err)
	}
	return ids, events, nil
}

func applyProjectedStaging(ctx context.Context, tx *sql.Tx, rows []projectedStagingRow, archive bool) error {
	table := projectedStagingTable(archive)
	query := fmt.Sprintf("UPDATE %s SET id = ?, event_id = ?, meta_json = ? WHERE id = ? AND event_id = ? AND source_id = ? AND raw_hash = ? AND meta_json = ?", quoteSQLiteIdentifier(table))
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, query, row.newID, row.newEventID, row.newMetaJSON, row.ref.ID, row.ref.EventID, row.ref.SourceID, row.ref.RawHash, row.ref.RawMetaJSON)
		if err != nil {
			return fmt.Errorf("update projected L1 staging row: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("measure projected L1 staging update: %w", err)
		}
		if affected != 1 {
			return fmt.Errorf("projected L1 staging update affected %d rows", affected)
		}
	}
	return nil
}

func applyProjectedRegistry(ctx context.Context, tx *sql.Tx, rows []projectedRegistryRow) error {
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE l1_source_registry SET meta_json = ? WHERE source_id = ? AND meta_json = ?`, row.newMetaJSON, row.ref.SourceID, row.ref.RawMetaJSON)
		if err != nil {
			return fmt.Errorf("update projected L1 registry row: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("measure projected L1 registry update: %w", err)
		}
		if affected != 1 {
			return fmt.Errorf("projected L1 registry update affected %d rows", affected)
		}
	}
	return nil
}

func projectedStagingTable(archive bool) string {
	if archive {
		return "l1_staging_item_archive"
	}
	return "l1_staging_item"
}

func verifyProjectedL1Snapshot(ctx context.Context, db *sql.DB, sourceData l1SourceData, sourceHash sourceHashes, sourceHashKey string, projection l1ProjectionPlan, archive bool) (l1ProjectionEvidence, error) {
	if err := ctx.Err(); err != nil {
		return l1ProjectionEvidence{}, err
	}
	table := projectedStagingTable(archive)
	observed, err := readProjectedStagingRows(ctx, db, table)
	if err != nil {
		return l1ProjectionEvidence{}, err
	}
	expectedStagingRows := sourceData.Counts.CurrentStaging
	if archive {
		expectedStagingRows = sourceData.Counts.ArchiveStaging
	}
	if len(observed) != expectedStagingRows {
		return l1ProjectionEvidence{}, fmt.Errorf("projected L1 staging row count changed")
	}
	evidence := l1ProjectionEvidence{
		DCIStagingRows:     len(projection.staging),
		RegistryRows:       len(projection.registry),
		SourceSchemaSHA256: sourceHash.Schema,
		SourceNonDCISHA256: sourceHash.NonDCI,
	}
	canonicalIDs := make(map[string]struct{}, len(projection.staging))
	oldIDs := make(map[string]struct{}, len(projection.staging))
	for _, row := range projection.staging {
		canonicalIDs[row.newID] = struct{}{}
		oldIDs[row.ref.ID] = struct{}{}
		actual, ok := observed[row.newID]
		if !ok {
			return l1ProjectionEvidence{}, fmt.Errorf("projected L1 canonical staging row is missing")
		}
		evidence.CanonicalStagingRows++
		if actual.EventID != row.newEventID || actual.SourceID != row.ref.SourceID {
			return l1ProjectionEvidence{}, fmt.Errorf("projected L1 canonical staging identity is incorrect")
		}
		if rawTextSHA256(actual.RawText) != row.ref.RawTextSHA256 {
			evidence.RawTextHashMismatches++
		}
		if actual.RawHash != row.ref.RawHash || !isLowerHexSHA256(actual.RawHash) {
			evidence.RawHashMismatches++
		}
		metadata, err := decodeMetadata(actual.MetaJSON)
		if err != nil {
			return l1ProjectionEvidence{}, fmt.Errorf("decode projected L1 staging metadata: %w", err)
		}
		if _, exists := metadata["search_event_id"]; exists || !reflect.DeepEqual(metadata, row.newMetadata) {
			return l1ProjectionEvidence{}, fmt.Errorf("projected L1 staging metadata is incorrect")
		}
	}
	for _, actual := range observed {
		if _, canonical := canonicalIDs[actual.ID]; canonical {
			continue
		}
		if _, old := oldIDs[actual.ID]; old {
			evidence.OldStagingRowsRemaining++
		}
		metadata, err := decodeMetadata(actual.MetaJSON)
		if err != nil {
			return l1ProjectionEvidence{}, fmt.Errorf("decode projected L1 staging row metadata: %w", err)
		}
		if hasCanonicalDCIMarker(metadata) {
			evidence.OrphanRows++
		}
	}
	if evidence.CanonicalStagingRows != evidence.DCIStagingRows || evidence.OldStagingRowsRemaining != 0 || evidence.RawTextHashMismatches != 0 || evidence.RawHashMismatches != 0 || evidence.OrphanRows != 0 {
		return l1ProjectionEvidence{}, fmt.Errorf("projected L1 staging verification failed")
	}

	if !archive {
		registry, err := readProjectedRegistryRows(ctx, db)
		if err != nil {
			return l1ProjectionEvidence{}, err
		}
		for _, row := range projection.registry {
			actual, ok := registry[row.ref.SourceID]
			if !ok {
				return l1ProjectionEvidence{}, fmt.Errorf("projected L1 registry row is missing")
			}
			evidence.CanonicalRegistryRows++
			metadata, err := decodeMetadata(actual)
			if err != nil {
				return l1ProjectionEvidence{}, fmt.Errorf("decode projected L1 registry metadata: %w", err)
			}
			if _, exists := metadata["search_event_id"]; exists || !reflect.DeepEqual(metadata, row.newMetadata) {
				return l1ProjectionEvidence{}, fmt.Errorf("projected L1 registry metadata is incorrect")
			}
		}
		if evidence.CanonicalRegistryRows != evidence.RegistryRows {
			return l1ProjectionEvidence{}, fmt.Errorf("projected L1 registry verification failed")
		}
		if len(registry) != sourceData.Counts.CurrentRegistry {
			return l1ProjectionEvidence{}, fmt.Errorf("projected L1 registry row count changed")
		}
	}

	forbiddenStagingIDs := make(map[string]struct{}, len(projection.staging)*2)
	for _, row := range projection.staging {
		forbiddenStagingIDs[row.ref.ID] = struct{}{}
		forbiddenStagingIDs[row.newID] = struct{}{}
	}
	if err := checkPromotedStagingReferences(ctx, db, forbiddenStagingIDs); err != nil {
		return l1ProjectionEvidence{}, err
	}
	logicalData := l1SourceData{StagingIDs: make(map[string]struct{}, len(projection.staging)), RegistryIDs: make(map[string]struct{}, len(projection.registry))}
	for _, row := range projection.staging {
		logicalData.StagingIDs[row.newID] = struct{}{}
	}
	for _, row := range projection.registry {
		logicalData.RegistryIDs[row.ref.SourceID] = struct{}{}
	}
	logical, err := hashSQLiteLogical(ctx, db, l1NonDCIExcluder(logicalData))
	if err != nil {
		return l1ProjectionEvidence{}, fmt.Errorf("hash projected L1 database: %w", err)
	}
	evidence.OutputSchemaSHA256 = logical.Schema
	evidence.OutputNonDCISHA256 = logical.NonDCI
	if logical.Schema != sourceHash.Schema || logical.NonDCI != sourceHash.NonDCI {
		return l1ProjectionEvidence{}, fmt.Errorf("projected L1 schema or non-DCI logical hash differs from source")
	}
	quickErr := captureSQLiteQuickCheck(ctx, db)
	if quickErr != nil {
		return l1ProjectionEvidence{}, quickErr
	}
	foreignKeyViolations, err := countForeignKeyViolations(ctx, db)
	if err != nil {
		return l1ProjectionEvidence{}, err
	}
	evidence.ForeignKeyViolations = foreignKeyViolations
	if foreignKeyViolations != 0 {
		return l1ProjectionEvidence{}, fmt.Errorf("projected L1 foreign key check found %d violations", foreignKeyViolations)
	}
	evidence.QuickCheckOK = 1
	if evidence.OutputSchemaSHA256 == "" || evidence.OutputNonDCISHA256 == "" || sourceHashKey == "" {
		return l1ProjectionEvidence{}, fmt.Errorf("projected L1 integrity evidence is incomplete")
	}
	return evidence, nil
}

type projectedStagingObservation struct {
	ID       string
	EventID  string
	SourceID string
	RawText  string
	RawHash  string
	MetaJSON string
}

func readProjectedStagingRows(ctx context.Context, db *sql.DB, table string) (map[string]projectedStagingObservation, error) {
	rows, err := db.QueryContext(ctx, "SELECT id, event_id, source_id, raw_text, raw_hash, meta_json FROM "+quoteSQLiteIdentifier(table))
	if err != nil {
		return nil, fmt.Errorf("read projected L1 staging rows: %w", err)
	}
	defer rows.Close()
	observed := make(map[string]projectedStagingObservation)
	for rows.Next() {
		var item projectedStagingObservation
		if err := rows.Scan(&item.ID, &item.EventID, &item.SourceID, &item.RawText, &item.RawHash, &item.MetaJSON); err != nil {
			return nil, fmt.Errorf("scan projected L1 staging row: %w", err)
		}
		if _, exists := observed[item.ID]; exists {
			return nil, fmt.Errorf("projected L1 staging primary key is duplicated")
		}
		observed[item.ID] = item
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate projected L1 staging rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close projected L1 staging rows: %w", err)
	}
	return observed, nil
}

func readProjectedRegistryRows(ctx context.Context, db *sql.DB) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT source_id, meta_json FROM l1_source_registry`)
	if err != nil {
		return nil, fmt.Errorf("read projected L1 registry rows: %w", err)
	}
	defer rows.Close()
	observed := make(map[string]string)
	for rows.Next() {
		var sourceID, metadata string
		if err := rows.Scan(&sourceID, &metadata); err != nil {
			return nil, fmt.Errorf("scan projected L1 registry row: %w", err)
		}
		if _, exists := observed[sourceID]; exists {
			return nil, fmt.Errorf("projected L1 registry primary key is duplicated")
		}
		observed[sourceID] = metadata
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate projected L1 registry rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close projected L1 registry rows: %w", err)
	}
	return observed, nil
}

func countForeignKeyViolations(ctx context.Context, db *sql.DB) (int, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return 0, fmt.Errorf("check projected L1 foreign keys: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var table string
		var rowID, parent, foreignKeyID any
		if err := rows.Scan(&table, &rowID, &parent, &foreignKeyID); err != nil {
			return 0, fmt.Errorf("scan projected L1 foreign key check: %w", err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate projected L1 foreign key check: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close projected L1 foreign key check: %w", err)
	}
	return count, nil
}
