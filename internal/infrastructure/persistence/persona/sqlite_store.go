package persona

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	domainpersona "github.com/Nyukimin/RenCrow_CORE/internal/domain/persona"
	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	db       *sql.DB
	metaRoot string
}

const sqliteBusyTimeoutMilliseconds = 5000

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	if path == "" {
		path = "workspace/logs/persona.db"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", fmt.Sprintf("%s?_pragma=busy_timeout%%3d%d&_time_format=sqlite", path, sqliteBusyTimeoutMilliseconds))
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
			response_key TEXT,
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
		`CREATE TABLE IF NOT EXISTS persona_storagehost_operation_receipt (
			op_id TEXT PRIMARY KEY CHECK(length(op_id) BETWEEN 1 AND 128),
			operation TEXT NOT NULL CHECK(operation IN (
				'save_discomfort_log', 'save_trigger_log', 'save_canonical_response_log',
				'save_observation_log', 'save_meta_profile_update', 'save_interface_session')),
			payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256) = 64 AND payload_sha256 = lower(payload_sha256) AND payload_sha256 NOT GLOB '*[^0-9a-f]*'),
			writer_generation INTEGER NOT NULL CHECK(writer_generation > 0),
			effect_table TEXT NOT NULL CHECK(effect_table IN (
				'persona_discomfort_log', 'persona_trigger_log', 'canonical_response_log',
				'observation_log', 'meta_profile_update', 'persona_interface_session')),
			effect_id TEXT NOT NULL CHECK(length(effect_id) BETWEEN 1 AND 512),
			result_json TEXT NOT NULL,
			result_sha256 TEXT NOT NULL CHECK(length(result_sha256) = 64 AND result_sha256 = lower(result_sha256) AND result_sha256 NOT GLOB '*[^0-9a-f]*'),
			CHECK(
				(operation = 'save_discomfort_log' AND effect_table = 'persona_discomfort_log') OR
				(operation = 'save_trigger_log' AND effect_table = 'persona_trigger_log') OR
				(operation = 'save_canonical_response_log' AND effect_table = 'canonical_response_log') OR
				(operation = 'save_observation_log' AND effect_table = 'observation_log') OR
				(operation = 'save_meta_profile_update' AND effect_table = 'meta_profile_update') OR
				(operation = 'save_interface_session' AND effect_table = 'persona_interface_session'))
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return s.validatePersonaStorageHostReceiptSchema()
}

func (s *SQLiteStore) validatePersonaStorageHostReceiptSchema() error {
	rows, err := s.db.Query(`PRAGMA table_info(` + personaStorageHostReceiptTable + `)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	primaryKeyCount := 0
	primaryKeyColumn := ""
	for rows.Next() {
		var cid, notNull, primaryKeyOrdinal int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKeyOrdinal); err != nil {
			return err
		}
		if primaryKeyOrdinal > 0 {
			primaryKeyCount++
			if primaryKeyOrdinal == 1 {
				primaryKeyColumn = name
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if primaryKeyCount != 1 || primaryKeyColumn != "op_id" {
		return fmt.Errorf("persona storage-host receipt primary key must be exactly op_id")
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
		event_id, character_id, response_key, message_id, created_at, payload
	) VALUES (?, ?, ?, ?, ?, ?)`,
		item.EventID, item.CharacterID, item.ResponseKey, item.MessageID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
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

// FindObservationLogByID returns the row with the exact primary ID without scanning a list.
func (s *SQLiteStore) FindObservationLogByID(ctx context.Context, eventID string) (domainpersona.ObservationLog, bool, error) {
	if s == nil || s.db == nil {
		return domainpersona.ObservationLog{}, false, fmt.Errorf("persona sqlite store is closed")
	}
	var payload string
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM observation_log WHERE event_id = ?`, eventID).Scan(&payload)
	if err != nil {
		if err == sql.ErrNoRows {
			return domainpersona.ObservationLog{}, false, nil
		}
		return domainpersona.ObservationLog{}, false, err
	}
	var item domainpersona.ObservationLog
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		return domainpersona.ObservationLog{}, false, err
	}
	return item, true, nil
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
