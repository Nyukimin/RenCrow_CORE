package persona

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	domainpersona "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/persona"
	_ "github.com/mattn/go-sqlite3"
)

type SQLiteStore struct {
	db       *sql.DB
	metaRoot string
}

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	if path == "" {
		path = "workspace/logs/persona.db"
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

func NewSQLiteStoreWithMetaRoot(path, metaRoot string) (*SQLiteStore, error) {
	store, err := NewSQLiteStore(path)
	if err != nil {
		return nil, err
	}
	store.metaRoot = metaRoot
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
		`CREATE TABLE IF NOT EXISTS persona_discomfort_log (
			event_id TEXT PRIMARY KEY,
			character_id TEXT,
			status TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS persona_trigger_log (
			event_id TEXT PRIMARY KEY,
			character_id TEXT,
			trigger_id TEXT,
			trigger_category TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS canonical_response_log (
			event_id TEXT PRIMARY KEY,
			character_id TEXT,
			response_id TEXT,
			message_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS observation_log (
			event_id TEXT PRIMARY KEY,
			observer_id TEXT,
			target_id TEXT,
			observation_type TEXT,
			sensitivity TEXT,
			review_status TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS meta_profile_update (
			update_id TEXT PRIMARY KEY,
			observer_id TEXT,
			target_id TEXT,
			section TEXT,
			sensitivity TEXT,
			review_status TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS persona_interface_session (
			session_id TEXT PRIMARY KEY,
			character_id TEXT,
			interface_type TEXT,
			session_key TEXT,
			workstream_id TEXT,
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

func (s *SQLiteStore) SaveDiscomfortLog(ctx context.Context, item domainpersona.DiscomfortLog) error {
	if err := domainpersona.ValidateDiscomfortLog(item); err != nil {
		return err
	}
	return s.save(ctx, `INSERT OR REPLACE INTO persona_discomfort_log (
		event_id, character_id, status, created_at, payload
	) VALUES (?, ?, ?, ?, ?)`,
		item.EventID, item.CharacterID, item.Status, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListDiscomfortLogs(ctx context.Context, limit int) ([]domainpersona.DiscomfortLog, error) {
	return listSQLiteItems[domainpersona.DiscomfortLog](ctx, s, "persona_discomfort_log", limit)
}

func (s *SQLiteStore) SaveTriggerLog(ctx context.Context, item domainpersona.TriggerLog) error {
	if err := domainpersona.ValidateTriggerLog(item); err != nil {
		return err
	}
	return s.save(ctx, `INSERT OR REPLACE INTO persona_trigger_log (
		event_id, character_id, trigger_id, trigger_category, created_at, payload
	) VALUES (?, ?, ?, ?, ?, ?)`,
		item.EventID, item.CharacterID, item.TriggerID, item.TriggerCategory, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListTriggerLogs(ctx context.Context, limit int) ([]domainpersona.TriggerLog, error) {
	return listSQLiteItems[domainpersona.TriggerLog](ctx, s, "persona_trigger_log", limit)
}

func (s *SQLiteStore) SaveCanonicalResponseLog(ctx context.Context, item domainpersona.CanonicalResponseLog) error {
	if err := domainpersona.ValidateCanonicalResponseLog(item); err != nil {
		return err
	}
	return s.save(ctx, `INSERT OR REPLACE INTO canonical_response_log (
		event_id, character_id, response_id, message_id, created_at, payload
	) VALUES (?, ?, ?, ?, ?, ?)`,
		item.EventID, item.CharacterID, item.ResponseID, item.MessageID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListCanonicalResponseLogs(ctx context.Context, limit int) ([]domainpersona.CanonicalResponseLog, error) {
	return listSQLiteItems[domainpersona.CanonicalResponseLog](ctx, s, "canonical_response_log", limit)
}

func (s *SQLiteStore) SaveObservationLog(ctx context.Context, item domainpersona.ObservationLog) error {
	if err := domainpersona.ValidateObservationLog(item); err != nil {
		return err
	}
	return s.save(ctx, `INSERT OR REPLACE INTO observation_log (
		event_id, observer_id, target_id, observation_type, sensitivity, review_status, created_at, payload
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		item.EventID, item.ObserverID, item.TargetID, item.ObservationType, item.Sensitivity, item.ReviewStatus, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListObservationLogs(ctx context.Context, limit int) ([]domainpersona.ObservationLog, error) {
	return listSQLiteItems[domainpersona.ObservationLog](ctx, s, "observation_log", limit)
}

func (s *SQLiteStore) SaveMetaProfileUpdate(ctx context.Context, item domainpersona.MetaProfileUpdate) error {
	if err := domainpersona.ValidateMetaProfileUpdate(item); err != nil {
		return err
	}
	return s.save(ctx, `INSERT OR REPLACE INTO meta_profile_update (
		update_id, observer_id, target_id, section, sensitivity, review_status, created_at, payload
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		item.UpdateID, item.ObserverID, item.TargetID, item.Section, item.Sensitivity, item.ReviewStatus, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListMetaProfileUpdates(ctx context.Context, limit int) ([]domainpersona.MetaProfileUpdate, error) {
	return listSQLiteItems[domainpersona.MetaProfileUpdate](ctx, s, "meta_profile_update", limit)
}

func (s *SQLiteStore) SaveInterfaceSession(ctx context.Context, item domainpersona.InterfaceSession) error {
	if err := domainpersona.ValidateInterfaceSession(item); err != nil {
		return err
	}
	return s.save(ctx, `INSERT OR REPLACE INTO persona_interface_session (
		session_id, character_id, interface_type, session_key, workstream_id, created_at, payload
	) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		item.SessionID, item.CharacterID, item.InterfaceType, item.SessionKey, item.WorkstreamID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListInterfaceSessions(ctx context.Context, limit int) ([]domainpersona.InterfaceSession, error) {
	return listSQLiteItems[domainpersona.InterfaceSession](ctx, s, "persona_interface_session", limit)
}

func (s *SQLiteStore) save(ctx context.Context, query string, args ...any) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("persona sqlite store is closed")
	}
	if len(args) == 0 {
		return fmt.Errorf("persona sqlite store save requires payload")
	}
	payload, err := json.Marshal(args[len(args)-1])
	if err != nil {
		return err
	}
	args[len(args)-1] = string(payload)
	_, err = s.db.ExecContext(ctx, query, args...)
	return err
}

func listSQLiteItems[T any](ctx context.Context, s *SQLiteStore, table string, limit int) ([]T, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("persona sqlite store is closed")
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
