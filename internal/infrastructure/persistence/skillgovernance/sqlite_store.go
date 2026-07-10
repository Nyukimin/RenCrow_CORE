package skillgovernance

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	domainskill "github.com/Nyukimin/RenCrow_CORE/internal/domain/skillgovernance"
	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	if path == "" {
		path = "workspace/logs/skill_governance.db"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_time_format=sqlite")
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
		`CREATE TABLE IF NOT EXISTS skill_registry (
			skill_id TEXT PRIMARY KEY,
			updated_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS skill_trigger_log (
			event_id TEXT PRIMARY KEY,
			skill_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS skill_change_log (
			change_id TEXT PRIMARY KEY,
			skill_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS contribution_gate_log (
			event_id TEXT PRIMARY KEY,
			repo TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS external_pr_submit_log (
			submit_id TEXT PRIMARY KEY,
			repo TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS coder_transcript_log (
			event_id TEXT PRIMARY KEY,
			job_id TEXT,
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

func (s *SQLiteStore) SaveSkillManifest(ctx context.Context, item domainskill.SkillManifest) error {
	if err := domainskill.ValidateSkillManifest(item); err != nil {
		return err
	}
	return s.save(ctx, "skill_registry", "skill_id", item.SkillID, "", "", "updated_at", item.UpdatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListSkillManifests(ctx context.Context, limit int) ([]domainskill.SkillManifest, error) {
	return listSQLiteItems[domainskill.SkillManifest](ctx, s, "skill_registry", limit)
}

func (s *SQLiteStore) SaveSkillTriggerLog(ctx context.Context, item domainskill.SkillTriggerLog) error {
	if err := domainskill.ValidateSkillTriggerLog(item); err != nil {
		return err
	}
	return s.save(ctx, "skill_trigger_log", "event_id", item.EventID, "skill_id", item.SkillID, "created_at", item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListSkillTriggerLogs(ctx context.Context, limit int) ([]domainskill.SkillTriggerLog, error) {
	return listSQLiteItems[domainskill.SkillTriggerLog](ctx, s, "skill_trigger_log", limit)
}

func (s *SQLiteStore) SaveSkillChangeLog(ctx context.Context, item domainskill.SkillChangeLog) error {
	if err := domainskill.ValidateSkillChangeLog(item); err != nil {
		return err
	}
	return s.save(ctx, "skill_change_log", "change_id", item.ChangeID, "skill_id", item.SkillID, "created_at", item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListSkillChangeLogs(ctx context.Context, limit int) ([]domainskill.SkillChangeLog, error) {
	return listSQLiteItems[domainskill.SkillChangeLog](ctx, s, "skill_change_log", limit)
}

func (s *SQLiteStore) SaveContributionGateLog(ctx context.Context, item domainskill.ContributionGateLog) error {
	if err := domainskill.ValidateContributionGateLog(item); err != nil {
		return err
	}
	return s.save(ctx, "contribution_gate_log", "event_id", item.EventID, "repo", item.Repo, "created_at", item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListContributionGateLogs(ctx context.Context, limit int) ([]domainskill.ContributionGateLog, error) {
	return listSQLiteItems[domainskill.ContributionGateLog](ctx, s, "contribution_gate_log", limit)
}

func (s *SQLiteStore) SaveExternalPRSubmitRecord(ctx context.Context, item domainskill.ExternalPRSubmitRecord) error {
	if err := domainskill.ValidateExternalPRSubmitRecord(item); err != nil {
		return err
	}
	return s.save(ctx, "external_pr_submit_log", "submit_id", item.SubmitID, "repo", item.Repo, "created_at", item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListExternalPRSubmitRecords(ctx context.Context, limit int) ([]domainskill.ExternalPRSubmitRecord, error) {
	return listSQLiteItems[domainskill.ExternalPRSubmitRecord](ctx, s, "external_pr_submit_log", limit)
}

func (s *SQLiteStore) SaveCoderTranscriptEntry(ctx context.Context, item domainskill.CoderTranscriptEntry) error {
	if err := domainskill.ValidateCoderTranscriptEntry(item); err != nil {
		return err
	}
	return s.save(ctx, "coder_transcript_log", "event_id", item.EventID, "job_id", item.JobID, "created_at", item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListCoderTranscriptEntries(ctx context.Context, limit int) ([]domainskill.CoderTranscriptEntry, error) {
	return listSQLiteItems[domainskill.CoderTranscriptEntry](ctx, s, "coder_transcript_log", limit)
}

func (s *SQLiteStore) save(ctx context.Context, table string, idColumn string, id string, secondaryColumn string, secondaryValue string, timeColumn string, timestamp string, item any) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("skill governance sqlite store is closed")
	}
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	if secondaryColumn == "" {
		query := fmt.Sprintf(`INSERT OR REPLACE INTO %s (%s, %s, payload) VALUES (?, ?, ?)`, table, idColumn, timeColumn)
		_, err = s.db.ExecContext(ctx, query, id, timestamp, string(payload))
		return err
	}
	query := fmt.Sprintf(`INSERT OR REPLACE INTO %s (%s, %s, %s, payload) VALUES (?, ?, ?, ?)`, table, idColumn, secondaryColumn, timeColumn)
	_, err = s.db.ExecContext(ctx, query, id, secondaryValue, timestamp, string(payload))
	return err
}

func listSQLiteItems[T any](ctx context.Context, s *SQLiteStore, table string, limit int) ([]T, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("skill governance sqlite store is closed")
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
