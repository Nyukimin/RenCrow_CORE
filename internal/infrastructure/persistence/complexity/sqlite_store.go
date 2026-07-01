package complexity

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	domaincomplexity "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/complexity"
	_ "github.com/mattn/go-sqlite3"
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
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}
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
	return s.save(ctx, "complexity_scan_event", "scan_id", item.ScanID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListScanEvents(ctx context.Context, limit int) ([]domaincomplexity.ScanEvent, error) {
	return listSQLiteItems[domaincomplexity.ScanEvent](ctx, s, "complexity_scan_event", limit)
}

func (s *SQLiteStore) SaveHotspot(ctx context.Context, item domaincomplexity.Hotspot) error {
	if err := domaincomplexity.ValidateHotspot(item); err != nil {
		return err
	}
	return s.save(ctx, "complexity_hotspot", "hotspot_id", item.HotspotID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListHotspots(ctx context.Context, limit int) ([]domaincomplexity.Hotspot, error) {
	return listSQLiteItems[domaincomplexity.Hotspot](ctx, s, "complexity_hotspot", limit)
}

func (s *SQLiteStore) SaveHotspotEvidence(ctx context.Context, item domaincomplexity.HotspotEvidence) error {
	if err := domaincomplexity.ValidateHotspotEvidence(item); err != nil {
		return err
	}
	return s.save(ctx, "complexity_hotspot_evidence", "evidence_id", item.EvidenceID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListHotspotEvidence(ctx context.Context, limit int) ([]domaincomplexity.HotspotEvidence, error) {
	return listSQLiteItems[domaincomplexity.HotspotEvidence](ctx, s, "complexity_hotspot_evidence", limit)
}

func (s *SQLiteStore) SaveReportArtifact(ctx context.Context, item domaincomplexity.ReportArtifact) error {
	if err := domaincomplexity.ValidateReportArtifact(item); err != nil {
		return err
	}
	return s.save(ctx, "complexity_report_artifact", "artifact_id", item.ArtifactID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListReportArtifacts(ctx context.Context, limit int) ([]domaincomplexity.ReportArtifact, error) {
	return listSQLiteItems[domaincomplexity.ReportArtifact](ctx, s, "complexity_report_artifact", limit)
}

func (s *SQLiteStore) save(ctx context.Context, table string, idColumn string, id string, createdAt string, item any) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("complexity sqlite store is closed")
	}
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	query := fmt.Sprintf(`INSERT OR REPLACE INTO %s (%s, created_at, payload) VALUES (?, ?, ?)`, table, idColumn)
	_, err = s.db.ExecContext(ctx, query, id, createdAt, string(payload))
	return err
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
