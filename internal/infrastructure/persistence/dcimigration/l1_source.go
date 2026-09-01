package dcimigration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

var l1CurrentSpecs = map[string]tableSpec{
	"l1_staging_item": {Columns: []tableColumnSpec{
		{Name: "id", Type: "TEXT", Primary: true}, {Name: "kind", Type: "TEXT", NotNull: true},
		{Name: "namespace", Type: "TEXT", NotNull: true}, {Name: "event_id", Type: "TEXT", NotNull: true},
		{Name: "source_id", Type: "TEXT", NotNull: true}, {Name: "source_url", Type: "TEXT", NotNull: true},
		{Name: "fetched_at", Type: "TIMESTAMP", NotNull: true}, {Name: "published_at", Type: "TIMESTAMP"},
		{Name: "raw_text", Type: "TEXT", NotNull: true}, {Name: "raw_hash", Type: "TEXT", NotNull: true},
		{Name: "summary_draft", Type: "TEXT", NotNull: true}, {Name: "keywords_json", Type: "TEXT", NotNull: true},
		{Name: "license_note", Type: "TEXT", NotNull: true}, {Name: "validation_status", Type: "TEXT", NotNull: true},
		{Name: "meta_json", Type: "TEXT", NotNull: true}, {Name: "created_at", Type: "TIMESTAMP", NotNull: true},
		{Name: "updated_at", Type: "TIMESTAMP", NotNull: true},
	}},
	"l1_source_registry": {Columns: []tableColumnSpec{
		{Name: "source_id", Type: "TEXT", Primary: true}, {Name: "url", Type: "TEXT", NotNull: true},
		{Name: "kind", Type: "TEXT", NotNull: true}, {Name: "trust_score", Type: "REAL", NotNull: true},
		{Name: "fetch_interval_sec", Type: "INTEGER", NotNull: true}, {Name: "license_note", Type: "TEXT", NotNull: true},
		{Name: "enabled", Type: "INTEGER", NotNull: true}, {Name: "meta_json", Type: "TEXT", NotNull: true},
		{Name: "last_fetched_at", Type: "TIMESTAMP"}, {Name: "last_status", Type: "TEXT", NotNull: true},
		{Name: "last_error", Type: "TEXT", NotNull: true}, {Name: "created_at", Type: "TIMESTAMP", NotNull: true},
		{Name: "updated_at", Type: "TIMESTAMP", NotNull: true},
	}},
}

var l1ArchiveSpecs = map[string]tableSpec{
	"l1_staging_item_archive": {Columns: []tableColumnSpec{
		{Name: "id", Type: "VARCHAR", Primary: true}, {Name: "kind", Type: "VARCHAR", NotNull: true},
		{Name: "namespace", Type: "VARCHAR", NotNull: true}, {Name: "event_id", Type: "VARCHAR", NotNull: true},
		{Name: "source_id", Type: "VARCHAR", NotNull: true}, {Name: "source_url", Type: "TEXT", NotNull: true},
		{Name: "fetched_at", Type: "TIMESTAMP", NotNull: true}, {Name: "published_at", Type: "TIMESTAMP"},
		{Name: "raw_text", Type: "TEXT", NotNull: true}, {Name: "raw_hash", Type: "VARCHAR", NotNull: true},
		{Name: "summary_draft", Type: "TEXT", NotNull: true}, {Name: "keywords_json", Type: "TEXT", NotNull: true},
		{Name: "license_note", Type: "TEXT", NotNull: true}, {Name: "validation_status", Type: "VARCHAR", NotNull: true},
		{Name: "meta_json", Type: "TEXT", NotNull: true}, {Name: "created_at", Type: "TIMESTAMP", NotNull: true},
		{Name: "updated_at", Type: "TIMESTAMP", NotNull: true},
	}},
}

type l1SourceData struct {
	Counts             SourceCounts
	DCIStaging         int
	Evidence           []legacyEvidence
	RegistryRefs       []legacyRegistryRef
	StagingRefs        map[string]l1StagingRef
	RegistryRefsByID   map[string]legacyRegistryRef
	StagingIDs         map[string]struct{}
	StagingEvidenceIDs map[string]struct{}
	RegistryIDs        map[string]struct{}
	Lines              []string
}

func loadL1Current(ctx context.Context, path string) (l1SourceData, sourceHashes, error) {
	return loadL1SQLite(ctx, path, false)
}

func loadL1Archive(ctx context.Context, path string) (l1SourceData, sourceHashes, error) {
	return loadL1SQLite(ctx, path, true)
}

func loadL1SQLite(ctx context.Context, path string, archive bool) (l1SourceData, sourceHashes, error) {
	before, err := fileSHA256(path)
	if err != nil {
		return l1SourceData{}, sourceHashes{}, newCodedError("source_read", "hash L1 SQLite: %v", err)
	}
	db, err := openSQLiteReadOnly(ctx, path)
	if err != nil {
		return l1SourceData{}, sourceHashes{}, newCodedError("source_read", "open L1 SQLite: %v", err)
	}
	data, readErr := queryL1SQLite(ctx, db, archive)
	var logical logicalHashes
	if readErr == nil {
		logical, readErr = hashSQLiteLogical(ctx, db, l1NonDCIExcluder(data))
	}
	closeErr := db.Close()
	if readErr != nil {
		return l1SourceData{}, sourceHashes{}, readErr
	}
	if closeErr != nil {
		return l1SourceData{}, sourceHashes{}, newCodedError("source_read", "close L1 SQLite: %v", closeErr)
	}
	after, err := fileSHA256(path)
	if err != nil || after != before {
		return l1SourceData{}, sourceHashes{}, newCodedError("source_changed", "L1 SQLite changed during read")
	}
	return data, sourceHashes{
		DatabaseLogical: logical.Full,
		Schema:          logical.Schema,
		Classification:  hashCanonicalLines(data.Lines),
		NonDCI:          logical.NonDCI,
	}, nil
}

func queryL1SQLite(ctx context.Context, db *sql.DB, archive bool) (l1SourceData, error) {
	data := l1SourceData{
		StagingRefs:        make(map[string]l1StagingRef),
		RegistryRefsByID:   make(map[string]legacyRegistryRef),
		StagingIDs:         make(map[string]struct{}),
		StagingEvidenceIDs: make(map[string]struct{}),
		RegistryIDs:        make(map[string]struct{}),
	}
	if err := checkUserVersion(ctx, db, 0); err != nil {
		return l1SourceData{}, newCodedError("unknown_schema", "L1 schema version: %v", err)
	}
	tables, err := schemaUserTables(ctx, db)
	if err != nil {
		return l1SourceData{}, newCodedError("unknown_schema", "inspect L1 tables: %v", err)
	}
	if archive {
		if err := requireTableSet(tables, []string{"l1_staging_item_archive"}, false); err != nil {
			return l1SourceData{}, newCodedError("unknown_schema", "%v", err)
		}
		if err := inspectTable(ctx, db, "l1_staging_item_archive", l1ArchiveSpecs["l1_staging_item_archive"]); err != nil {
			return l1SourceData{}, newCodedError("unknown_schema", "archive staging schema: %v", err)
		}
		if err := readArchiveStaging(ctx, db, &data); err != nil {
			return l1SourceData{}, err
		}
		if err := checkPromotedStagingReferences(ctx, db, data.StagingIDs); err != nil {
			return l1SourceData{}, err
		}
	} else {
		if err := requireTableSet(tables, []string{"l1_staging_item", "l1_source_registry"}, false); err != nil {
			return l1SourceData{}, newCodedError("unknown_schema", "%v", err)
		}
		for table, spec := range l1CurrentSpecs {
			if err := inspectTable(ctx, db, table, spec); err != nil {
				return l1SourceData{}, newCodedError("unknown_schema", "current L1 table %s: %v", table, err)
			}
		}
		if err := readCurrentStaging(ctx, db, &data); err != nil {
			return l1SourceData{}, err
		}
		if err := readCurrentRegistry(ctx, db, &data); err != nil {
			return l1SourceData{}, err
		}
		if err := checkPromotedStagingReferences(ctx, db, data.StagingIDs); err != nil {
			return l1SourceData{}, err
		}
	}
	return data, nil
}

func readCurrentStaging(ctx context.Context, db *sql.DB, data *l1SourceData) error {
	rows, err := db.QueryContext(ctx, `SELECT id, kind, namespace, event_id, source_id, fetched_at, raw_text, raw_hash, meta_json, created_at FROM l1_staging_item ORDER BY rowid`)
	if err != nil {
		return newCodedError("source_read", "read current L1 staging: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		values := make([]any, 10)
		destinations := make([]any, len(values))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return newCodedError("source_read", "scan current L1 staging: %v", err)
		}
		data.Counts.CurrentStaging++
		if err := classifyStagingRow(values, data, "staging"); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return newCodedError("source_read", "iterate current L1 staging: %v", err)
	}
	return rows.Close()
}

func readArchiveStaging(ctx context.Context, db *sql.DB, data *l1SourceData) error {
	rows, err := db.QueryContext(ctx, `SELECT id, kind, namespace, event_id, source_id, fetched_at, raw_text, raw_hash, meta_json, created_at FROM l1_staging_item_archive ORDER BY rowid`)
	if err != nil {
		return newCodedError("source_read", "read archive L1 staging: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		values := make([]any, 10)
		destinations := make([]any, len(values))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return newCodedError("source_read", "scan archive L1 staging: %v", err)
		}
		data.Counts.ArchiveStaging++
		if err := classifyStagingRow(values, data, "archive_staging"); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return newCodedError("source_read", "iterate archive L1 staging: %v", err)
	}
	return rows.Close()
}

func classifyStagingRow(values []any, data *l1SourceData, lineTag string) error {
	stagingID := readText(values[0])
	kind := strings.TrimSpace(readText(values[1]))
	namespace := strings.TrimSpace(readText(values[2]))
	eventID := readText(values[3])
	rawText := readText(values[6])
	rawHash := readText(values[7])
	rawMetaJSON := readText(values[8])
	meta, err := decodeMetadata(rawMetaJSON)
	if err != nil {
		return newCodedError("malformed_source", "L1 staging metadata: %v", err)
	}
	sourceKind := metadataString(meta, "source_kind")
	searchID := metadataString(meta, "search_event_id")
	evidenceID := metadataString(meta, "evidence_id")
	if !hasDCIMarker(namespace, sourceKind, searchID, evidenceID, meta) {
		return nil
	}
	if hasCanonicalDCIMarker(meta) {
		return newCodedError("legacy_marker_mismatch", "canonical DCI metadata is not a legacy migration input")
	}
	if kind != "search_result" || namespace != "kb:dci" || sourceKind != "dci" || stagingID == "" || searchID == "" || evidenceID == "" || eventID != searchID+":"+evidenceID {
		return newCodedError("legacy_marker_mismatch", "DCI staging row does not contain one consistent legacy marker tuple")
	}
	if strings.TrimSpace(stagingID) != stagingID || strings.TrimSpace(searchID) != searchID || strings.TrimSpace(evidenceID) != evidenceID || strings.TrimSpace(eventID) != eventID {
		return newCodedError("legacy_marker_mismatch", "DCI staging identifiers must not have surrounding whitespace")
	}
	if err := validateRawTextHash(rawText, rawHash); err != nil {
		return err
	}
	if _, exists := data.StagingRefs[stagingID]; exists {
		return newCodedError("conflicting_duplicate_staging", "DCI staging primary key is duplicated")
	}
	if _, exists := data.StagingEvidenceIDs[evidenceID]; exists {
		return newCodedError("conflicting_duplicate_evidence", "DCI staging evidence ID is duplicated in one source")
	}
	data.StagingIDs[stagingID] = struct{}{}
	data.DCIStaging++
	lineStart, err := metadataInt(meta, "line_start")
	if err != nil {
		return newCodedError("malformed_source", "DCI staging line_start: %v", err)
	}
	lineEnd, err := metadataInt(meta, "line_end")
	if err != nil {
		return newCodedError("malformed_source", "DCI staging line_end: %v", err)
	}
	confidence, err := metadataFloat(meta, "confidence")
	if err != nil {
		return newCodedError("malformed_source", "DCI staging confidence: %v", err)
	}
	created, err := parseLegacyTime(values[9])
	if err != nil {
		return newCodedError("malformed_source", "DCI staging created_at: %v", err)
	}
	item := legacyEvidence{
		// L1 staging source_id identifies the projection/source-registry row.
		// The staging schema does not retain the original Evidence provenance,
		// so do not infer Evidence.SourceID from that projection identity.
		ID: evidenceID, SearchID: searchID, SourceID: "",
		FilePath: metadataString(meta, "file_path"), Heading: metadataString(meta, "heading"),
		LineStart: int(lineStart), LineEnd: int(lineEnd), Snippet: rawText,
		Reason: metadataString(meta, "reason"), Confidence: confidence, CreatedAt: created,
	}
	if err := validateLegacyEvidence(item); err != nil {
		return newCodedError("malformed_source", "DCI staging evidence: %v", err)
	}
	data.StagingEvidenceIDs[evidenceID] = struct{}{}
	data.Evidence = append(data.Evidence, item)
	data.StagingRefs[stagingID] = l1StagingRef{
		ID: stagingID, EventID: eventID, SourceID: readText(values[4]), RawHash: rawHash,
		RawTextSHA256: rawTextSHA256(rawText), RawMetaJSON: rawMetaJSON,
		SearchID: searchID, EvidenceID: evidenceID, OriginTable: lineTag,
	}
	line, _ := json.Marshal([]any{lineTag, stagingID, kind, namespace, eventID, readText(values[4]), readText(values[5]), rawText, rawHash, rawMetaJSON, readText(values[9])})
	data.Lines = append(data.Lines, string(line))
	return nil
}

type l1ProjectionIdentity struct {
	SearchID string
	SourceID string
}

func indexL1ProjectionIdentities(sources ...l1SourceData) (map[string]l1ProjectionIdentity, error) {
	identities := make(map[string]l1ProjectionIdentity)
	for _, source := range sources {
		for _, ref := range source.StagingRefs {
			identity := l1ProjectionIdentity{SearchID: ref.SearchID, SourceID: ref.SourceID}
			if prior, exists := identities[ref.EvidenceID]; exists && prior != identity {
				return nil, newCodedError("conflicting_duplicate_evidence", "L1 staging evidence resolves to inconsistent projection identities")
			}
			identities[ref.EvidenceID] = identity
		}
	}
	return identities, nil
}

func readCurrentRegistry(ctx context.Context, db *sql.DB, data *l1SourceData) error {
	rows, err := db.QueryContext(ctx, `SELECT source_id, kind, meta_json FROM l1_source_registry ORDER BY rowid`)
	if err != nil {
		return newCodedError("source_read", "read L1 source registry: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sourceID, kind, rawMeta string
		if err := rows.Scan(&sourceID, &kind, &rawMeta); err != nil {
			return newCodedError("source_read", "scan L1 source registry: %v", err)
		}
		data.Counts.CurrentRegistry++
		meta, err := decodeMetadata(rawMeta)
		if err != nil {
			return newCodedError("malformed_source", "L1 source registry metadata: %v", err)
		}
		sourceKind := metadataString(meta, "source_kind")
		searchID := metadataString(meta, "search_event_id")
		evidenceID := metadataString(meta, "evidence_id")
		if !hasDCIRegistryMarker(sourceKind, searchID, evidenceID, meta) {
			continue
		}
		if hasCanonicalDCIMarker(meta) {
			return newCodedError("legacy_marker_mismatch", "canonical DCI source registry metadata is not a legacy migration input")
		}
		if kind != "search_fallback" || sourceKind != "dci" || sourceID == "" || searchID == "" || evidenceID == "" {
			return newCodedError("legacy_marker_mismatch", "DCI source registry row does not contain one consistent legacy marker tuple")
		}
		if strings.TrimSpace(sourceID) != sourceID || strings.TrimSpace(searchID) != searchID || strings.TrimSpace(evidenceID) != evidenceID {
			return newCodedError("legacy_marker_mismatch", "DCI source registry identifiers must not have surrounding whitespace")
		}
		if _, exists := data.RegistryRefsByID[sourceID]; exists {
			return newCodedError("conflicting_duplicate_registry", "DCI source registry primary key is duplicated")
		}
		ref := legacyRegistryRef{SourceID: sourceID, SearchID: searchID, EvidenceID: evidenceID, RawMetaJSON: rawMeta, OriginTable: "l1_source_registry"}
		data.RegistryRefs = append(data.RegistryRefs, ref)
		data.RegistryRefsByID[sourceID] = ref
		data.RegistryIDs[sourceID] = struct{}{}
		line, _ := json.Marshal([]any{"registry", sourceID, kind, rawMeta})
		data.Lines = append(data.Lines, string(line))
	}
	if err := rows.Err(); err != nil {
		return newCodedError("source_read", "iterate L1 source registry: %v", err)
	}
	return rows.Close()
}

func rawTextSHA256(rawText string) string {
	digest := sha256.Sum256([]byte(rawText))
	return hex.EncodeToString(digest[:])
}

func validateRawTextHash(rawText, rawHash string) error {
	if !isLowerHexSHA256(rawHash) || rawTextSHA256(rawText) != rawHash {
		return newCodedError("raw_hash_mismatch", "classified DCI staging raw_hash does not match raw_text")
	}
	return nil
}

func l1NonDCIExcluder(data l1SourceData) logicalRowExcluder {
	stagingIDs := make(map[string]struct{}, len(data.StagingIDs))
	for id := range data.StagingIDs {
		stagingIDs[id] = struct{}{}
	}
	registryIDs := make(map[string]struct{}, len(data.RegistryIDs))
	for id := range data.RegistryIDs {
		registryIDs[id] = struct{}{}
	}
	return func(table string, columns []string, values []any) (bool, error) {
		indexOf := func(name string) int {
			for index, column := range columns {
				if column == name {
					return index
				}
			}
			return -1
		}
		switch table {
		case "l1_staging_item", "l1_staging_item_archive":
			index := indexOf("id")
			if index < 0 || index >= len(values) {
				return false, newCodedError("logical_hash_schema", "L1 staging primary key is missing from table_xinfo")
			}
			_, excluded := stagingIDs[readText(values[index])]
			return excluded, nil
		case "l1_source_registry":
			index := indexOf("source_id")
			if index < 0 || index >= len(values) {
				return false, newCodedError("logical_hash_schema", "L1 source registry primary key is missing from table_xinfo")
			}
			_, excluded := registryIDs[readText(values[index])]
			return excluded, nil
		default:
			return false, nil
		}
	}
}

var canonicalDCIMetadataKeys = []string{"search_action_id", "trace_id", "evidence_created_event_id"}

func hasCanonicalDCIMarker(metadata map[string]any) bool {
	for _, key := range canonicalDCIMetadataKeys {
		if _, ok := metadata[key]; ok {
			return true
		}
	}
	return false
}

func hasDCIMarker(namespace, sourceKind, searchID, evidenceID string, metadata map[string]any) bool {
	return namespace == "kb:dci" || sourceKind == "dci" || searchID != "" || evidenceID != "" || hasCanonicalDCIMarker(metadata)
}

func hasDCIRegistryMarker(sourceKind, searchID, evidenceID string, metadata map[string]any) bool {
	return sourceKind == "dci" || searchID != "" || evidenceID != "" || hasCanonicalDCIMarker(metadata)
}

func checkPromotedStagingReferences(ctx context.Context, db *sql.DB, stagingIDs map[string]struct{}) error {
	if len(stagingIDs) == 0 {
		return nil
	}
	for _, table := range []string{"l1_news_item", "l1_knowledge_item", "domain_graph_assertion", "l1_news_item_archive", "l1_knowledge_item_archive", "domain_graph_assertion_archive"} {
		exists, err := tableExists(ctx, db, table)
		if err != nil {
			return newCodedError("unknown_schema", "inspect downstream L1 table: %v", err)
		}
		if !exists {
			continue
		}
		if err := requireColumn(ctx, db, table, "staging_id"); err != nil {
			return newCodedError("unknown_schema", "%v", err)
		}
		rows, err := db.QueryContext(ctx, "SELECT staging_id FROM \""+table+"\" WHERE staging_id IS NOT NULL")
		if err != nil {
			return newCodedError("source_read", "read promoted L1 references: %v", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return newCodedError("source_read", "scan promoted L1 reference: %v", err)
			}
			if _, ok := stagingIDs[id]; ok {
				_ = rows.Close()
				return newCodedError("promoted_staging_reference", "affected DCI staging item is already promoted")
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return newCodedError("source_read", "iterate promoted L1 references: %v", err)
		}
		if err := rows.Close(); err != nil {
			return newCodedError("source_read", "close promoted L1 references: %v", err)
		}
	}
	return nil
}

func tableExists(ctx context.Context, db *sql.DB, table string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count)
	return count > 0, err
}

func requireColumn(ctx context.Context, db *sql.DB, table, column string) error {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info('"+strings.ReplaceAll(table, "'", "''")+"')")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primary int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primary); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	return fmt.Errorf("table %s is missing column %s", table, column)
}

func decodeMetadata(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}, nil
	}
	var metadata map[string]any
	if err := decodeStrictJSON([]byte(raw), &metadata, nil); err != nil {
		return nil, err
	}
	if metadata == nil {
		return nil, fmt.Errorf("metadata must be a JSON object")
	}
	return metadata, nil
}

func metadataString(metadata map[string]any, key string) string {
	value, ok := metadata[key]
	if !ok {
		return ""
	}
	return readText(value)
}

func metadataInt(metadata map[string]any, key string) (int64, error) {
	value, ok := metadata[key]
	if !ok {
		return 0, fmt.Errorf("%s is required", key)
	}
	return readInt(value)
}

func metadataFloat(metadata map[string]any, key string) (float64, error) {
	value, ok := metadata[key]
	if !ok {
		return 0, nil
	}
	return readFloat(value)
}
