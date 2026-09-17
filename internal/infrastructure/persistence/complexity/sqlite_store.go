package complexity

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	domaincomplexity "github.com/Nyukimin/RenCrow_CORE/internal/domain/complexity"
	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	if path == "" {
		path = "workspace/logs/complexity_hotspot.sqlite"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout%3d5000&_time_format=sqlite")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &SQLiteStore{db: db}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *SQLiteStore) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS complexity_scan_event (
			scan_id TEXT PRIMARY KEY,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS complexity_hotspot (
			hotspot_id TEXT PRIMARY KEY,
			scan_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS complexity_hotspot_evidence (
			evidence_id TEXT PRIMARY KEY,
			hotspot_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS complexity_report_artifact (
			artifact_id TEXT PRIMARY KEY,
			scan_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) SaveScanEvent(ctx context.Context, item domaincomplexity.ScanEvent) error {
	if err := domaincomplexity.ValidateScanEvent(item); err != nil {
		return err
	}
	return s.save(ctx, "complexity_scan_event", "scan_id", item.ScanID, "", "", item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListScanEvents(ctx context.Context, limit int) ([]domaincomplexity.ScanEvent, error) {
	return listSQLiteItems[domaincomplexity.ScanEvent](ctx, s, "complexity_scan_event", limit)
}

func (s *SQLiteStore) SaveHotspot(ctx context.Context, item domaincomplexity.Hotspot) error {
	if err := domaincomplexity.ValidateHotspot(item); err != nil {
		return err
	}
	return s.save(ctx, "complexity_hotspot", "hotspot_id", item.HotspotID, "scan_id", item.ScanID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListHotspots(ctx context.Context, limit int) ([]domaincomplexity.Hotspot, error) {
	return listSQLiteItems[domaincomplexity.Hotspot](ctx, s, "complexity_hotspot", limit)
}

// FindHotspotByID returns the row with the exact primary ID without scanning a list.
func (s *SQLiteStore) FindHotspotByID(ctx context.Context, hotspotID string) (domaincomplexity.Hotspot, bool, error) {
	if s == nil || s.db == nil {
		return domaincomplexity.Hotspot{}, false, fmt.Errorf("complexity sqlite store is closed")
	}
	var payload string
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM complexity_hotspot WHERE hotspot_id = ?`, hotspotID).Scan(&payload)
	if err != nil {
		if err == sql.ErrNoRows {
			return domaincomplexity.Hotspot{}, false, nil
		}
		return domaincomplexity.Hotspot{}, false, err
	}
	var item domaincomplexity.Hotspot
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		return domaincomplexity.Hotspot{}, false, err
	}
	if err := domaincomplexity.ValidateHotspot(item); err != nil {
		return domaincomplexity.Hotspot{}, false, err
	}
	if item.HotspotID != hotspotID {
		return domaincomplexity.Hotspot{}, false, fmt.Errorf("hotspot payload id %q does not match requested id %q", item.HotspotID, hotspotID)
	}
	return item, true, nil
}

func (s *SQLiteStore) SaveHotspotEvidence(ctx context.Context, item domaincomplexity.HotspotEvidence) error {
	if err := domaincomplexity.ValidateHotspotEvidence(item); err != nil {
		return err
	}
	return s.save(ctx, "complexity_hotspot_evidence", "evidence_id", string(item.EvidenceID), "hotspot_id", item.HotspotID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListHotspotEvidence(ctx context.Context, limit int) ([]domaincomplexity.HotspotEvidence, error) {
	return listSQLiteItems[domaincomplexity.HotspotEvidence](ctx, s, "complexity_hotspot_evidence", limit)
}

func (s *SQLiteStore) SaveReportArtifact(ctx context.Context, item domaincomplexity.ReportArtifact) error {
	if err := domaincomplexity.ValidateReportArtifact(item); err != nil {
		return err
	}
	return s.save(ctx, "complexity_report_artifact", "artifact_id", item.ArtifactID, "scan_id", item.ScanID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListReportArtifacts(ctx context.Context, limit int) ([]domaincomplexity.ReportArtifact, error) {
	return listSQLiteItems[domaincomplexity.ReportArtifact](ctx, s, "complexity_report_artifact", limit)
}

// FindReportArtifactByID returns the row with the exact primary ID without scanning a list.
func (s *SQLiteStore) FindReportArtifactByID(ctx context.Context, artifactID string) (domaincomplexity.ReportArtifact, bool, error) {
	if s == nil || s.db == nil {
		return domaincomplexity.ReportArtifact{}, false, fmt.Errorf("complexity sqlite store is closed")
	}
	var payload string
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM complexity_report_artifact WHERE artifact_id = ?`, artifactID).Scan(&payload)
	if err != nil {
		if err == sql.ErrNoRows {
			return domaincomplexity.ReportArtifact{}, false, nil
		}
		return domaincomplexity.ReportArtifact{}, false, err
	}
	var item domaincomplexity.ReportArtifact
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		return domaincomplexity.ReportArtifact{}, false, err
	}
	if err := domaincomplexity.ValidateReportArtifact(item); err != nil {
		return domaincomplexity.ReportArtifact{}, false, err
	}
	if item.ArtifactID != artifactID {
		return domaincomplexity.ReportArtifact{}, false, fmt.Errorf("report artifact payload id %q does not match requested id %q", item.ArtifactID, artifactID)
	}
	return item, true, nil
}

// save writes one payload row together with the identity index column that the
// payload is authoritative for. Passing an empty indexColumn keeps a table that
// has no parent reference. The index value must come from the same record the
// payload carries, otherwise the column cannot prove that the row is not an
// orphan, which is the Step 14 completion rule "Evidence orphan zero".
func (s *SQLiteStore) save(ctx context.Context, table string, idColumn string, id string, indexColumn string, indexValue string, createdAt string, item any) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("complexity sqlite store is closed")
	}
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	if indexColumn == "" {
		query := fmt.Sprintf(`INSERT OR REPLACE INTO %s (%s, created_at, payload) VALUES (?, ?, ?)`, table, idColumn)
		_, err = s.db.ExecContext(ctx, query, id, createdAt, string(payload))
		return err
	}
	query := fmt.Sprintf(`INSERT OR REPLACE INTO %s (%s, %s, created_at, payload) VALUES (?, ?, ?, ?)`,
		table, idColumn, indexColumn)
	_, err = s.db.ExecContext(ctx, query, id, indexValue, createdAt, string(payload))
	return err
}

// ComplexityIdentityOrphanCounts reports, per Step 14 complexity owner, how many
// persisted rows fail the identity reference rules. A row whose index column is
// empty or disagrees with its payload counts as InvalidIndex, and a row that
// references a parent record that does not exist counts as Missing*.
type ComplexityIdentityOrphanCounts struct {
	ScanEvents             int `json:"scan_events"`
	Hotspots               int `json:"hotspots"`
	Evidence               int `json:"evidence"`
	Artifacts              int `json:"artifacts"`
	HotspotsMissingScan    int `json:"hotspots_missing_scan"`
	HotspotsInvalidIndex   int `json:"hotspots_invalid_index"`
	EvidenceMissingHotspot int `json:"evidence_missing_hotspot"`
	EvidenceInvalidIndex   int `json:"evidence_invalid_index"`
	ArtifactsMissingScan   int `json:"artifacts_missing_scan"`
	ArtifactsInvalidIndex  int `json:"artifacts_invalid_index"`
}

const complexityOrphanCountQuery = `
WITH hotspot_orphans AS (
	SELECT
		CASE WHEN COALESCE(hotspot.scan_id, '') = ''
			OR hotspot.scan_id <> COALESCE(json_extract(hotspot.payload, '$.scan_id'), '')
			THEN 1 ELSE 0 END AS invalid_index,
		CASE WHEN COALESCE(hotspot.scan_id, '') <> ''
			AND scan.scan_id IS NULL
			THEN 1 ELSE 0 END AS missing_parent
	FROM complexity_hotspot hotspot
	LEFT JOIN complexity_scan_event scan ON scan.scan_id = hotspot.scan_id
),
evidence_orphans AS (
	SELECT
		CASE WHEN COALESCE(evidence.hotspot_id, '') = ''
			OR evidence.hotspot_id <> COALESCE(json_extract(evidence.payload, '$.hotspot_id'), '')
			THEN 1 ELSE 0 END AS invalid_index,
		CASE WHEN COALESCE(evidence.hotspot_id, '') <> ''
			AND hotspot.hotspot_id IS NULL
			THEN 1 ELSE 0 END AS missing_parent
	FROM complexity_hotspot_evidence evidence
	LEFT JOIN complexity_hotspot hotspot ON hotspot.hotspot_id = evidence.hotspot_id
),
artifact_orphans AS (
	SELECT
		CASE WHEN COALESCE(artifact.scan_id, '') = ''
			OR artifact.scan_id <> COALESCE(json_extract(artifact.payload, '$.scan_id'), '')
			THEN 1 ELSE 0 END AS invalid_index,
		CASE WHEN COALESCE(artifact.scan_id, '') <> ''
			AND scan.scan_id IS NULL
			THEN 1 ELSE 0 END AS missing_parent
	FROM complexity_report_artifact artifact
	LEFT JOIN complexity_scan_event scan ON scan.scan_id = artifact.scan_id
)
SELECT
	(SELECT count(*) FROM complexity_scan_event),
	(SELECT count(*) FROM complexity_hotspot),
	(SELECT count(*) FROM complexity_hotspot_evidence),
	(SELECT count(*) FROM complexity_report_artifact),
	(SELECT COALESCE(sum(missing_parent), 0) FROM hotspot_orphans),
	(SELECT COALESCE(sum(invalid_index), 0) FROM hotspot_orphans),
	(SELECT COALESCE(sum(missing_parent), 0) FROM evidence_orphans),
	(SELECT COALESCE(sum(invalid_index), 0) FROM evidence_orphans),
	(SELECT COALESCE(sum(missing_parent), 0) FROM artifact_orphans),
	(SELECT COALESCE(sum(invalid_index), 0) FROM artifact_orphans)`

// CountComplexityIdentityOrphans counts the rows that violate the Step 14
// identity reference rules. A total greater than one means an orphan that no
// owner test can see, so callers treat every non-zero field as a Step 14 failure.
func (s *SQLiteStore) CountComplexityIdentityOrphans(ctx context.Context) (ComplexityIdentityOrphanCounts, error) {
	var counts ComplexityIdentityOrphanCounts
	if s == nil || s.db == nil {
		return counts, fmt.Errorf("complexity sqlite store is closed")
	}
	err := s.db.QueryRowContext(ctx, complexityOrphanCountQuery).Scan(
		&counts.ScanEvents, &counts.Hotspots, &counts.Evidence, &counts.Artifacts,
		&counts.HotspotsMissingScan, &counts.HotspotsInvalidIndex,
		&counts.EvidenceMissingHotspot, &counts.EvidenceInvalidIndex,
		&counts.ArtifactsMissingScan, &counts.ArtifactsInvalidIndex,
	)
	if err != nil {
		return ComplexityIdentityOrphanCounts{}, err
	}
	return counts, nil
}

func listSQLiteItems[T any](ctx context.Context, s *SQLiteStore, table string, limit int) ([]T, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("complexity sqlite store is closed")
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT payload FROM %s ORDER BY rowid DESC LIMIT ?`, table), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []T{}
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var item T
		if err := json.Unmarshal([]byte(payload), &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

const timeFormatRFC3339Nano = "2006-01-02T15:04:05.999999999Z07:00"
