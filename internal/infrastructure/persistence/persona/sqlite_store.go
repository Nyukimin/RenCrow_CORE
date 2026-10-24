package persona

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	domainpersona "github.com/Nyukimin/RenCrow_CORE/internal/domain/persona"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence"
	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	db               *sql.DB
	metaRoot         string
	checkpointWorker *persistence.SQLiteWALCheckpointWorker
	admissionWrites  *personaAdmissionState
	closeMu          sync.Mutex
	closed           bool
	closeErr         error
}

const sqliteBusyTimeoutMilliseconds = 5000

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	if path == "" {
		path = "workspace/logs/persona.db"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	db, _, err := persistence.OpenSQLiteWALWriter(path, sqliteBusyTimeoutMilliseconds)
	if err != nil {
		return nil, err
	}
	store := &SQLiteStore{db: db, admissionWrites: newPersonaAdmissionState()}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	store.checkpointWorker, err = persistence.NewSQLiteWALCheckpointWorker("persona", path, sqliteBusyTimeoutMilliseconds)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.checkpointWorker.Start(); err != nil {
		_ = store.checkpointWorker.Close()
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

// SetPersistenceQueue binds this owner to the process's shared RAM FIFO. It
// warms bounded read snapshots before admissions can use the queue so CHAT
// reads never need to wait on SQLite while a queued write is in flight.
func (s *SQLiteStore) SetPersistenceQueue(queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) error {
	if s == nil || s.db == nil {
		return ErrPersonaAdmissionUnavailable
	}
	if err := s.admissionWrites.validateQueueBinding(queue); err != nil {
		return err
	}
	release, err := s.admissionWrites.beginSynchronous(context.Background())
	if err != nil {
		return err
	}
	defer release()
	snapshots, err := s.loadAdmissionSnapshots(context.Background())
	if err != nil {
		return fmt.Errorf("warm Persona admission read snapshots: %w", err)
	}
	for table, snapshot := range snapshots {
		s.admissionWrites.setSnapshot(table, snapshot.writes, personaAdmissionListSnapshotLimit, snapshot.complete)
	}
	return s.admissionWrites.setQueue(queue)
}

func (s *SQLiteStore) MatchesPersistenceQueue(queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) bool {
	return s != nil && s.admissionWrites.matchesQueue(queue)
}

type personaReadSnapshot struct {
	writes   []personaAdmissionWrite
	complete bool
}

func (s *SQLiteStore) loadAdmissionSnapshots(ctx context.Context) (map[string]personaReadSnapshot, error) {
	tables := []struct {
		name      string
		keyColumn string
	}{
		{name: "persona_discomfort_log", keyColumn: "event_id"},
		{name: "persona_trigger_log", keyColumn: "event_id"},
		{name: "canonical_response_log", keyColumn: "event_id"},
		{name: "observation_log", keyColumn: "event_id"},
		{name: "meta_profile_update", keyColumn: "update_id"},
		{name: "persona_interface_session", keyColumn: "session_id"},
	}
	snapshots := make(map[string]personaReadSnapshot, len(tables))
	for _, table := range tables {
		rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT %s, payload FROM %s ORDER BY rowid DESC LIMIT ?`, table.keyColumn, table.name), personaAdmissionListSnapshotLimit+1)
		if err != nil {
			return nil, err
		}
		writes := make([]personaAdmissionWrite, 0, personaAdmissionListSnapshotLimit+1)
		for rows.Next() {
			var key, payload string
			if err := rows.Scan(&key, &payload); err != nil {
				_ = rows.Close()
				return nil, err
			}
			writes = append(writes, personaAdmissionWrite{table: table.name, key: key, payload: payload})
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		complete := len(writes) <= personaAdmissionListSnapshotLimit
		if len(writes) > personaAdmissionListSnapshotLimit {
			writes = writes[:personaAdmissionListSnapshotLimit]
		}
		snapshots[table.name] = personaReadSnapshot{
			writes:   writes,
			complete: complete,
		}
	}
	return snapshots, nil
}

func (s *SQLiteStore) Close() error {
	if s == nil {
		return nil
	}
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closed {
		return s.closeErr
	}
	var drainErr error
	if s.admissionWrites != nil {
		drainErr = s.admissionWrites.stopAcceptingAndDrain()
	}
	var writerErr error
	if s.db == nil {
		var checkpointErr error
		if s.checkpointWorker != nil {
			checkpointErr = s.checkpointWorker.Close()
		}
		s.closeErr = errors.Join(drainErr, checkpointErr)
		s.closed = true
		return s.closeErr
	}
	// Close and drain the writer pool before the final checkpoint.
	writerErr = persistence.CloseSQLiteWALWriter(s.db)
	var checkpointErr error
	if s.checkpointWorker != nil {
		checkpointErr = s.checkpointWorker.Close()
	}
	s.closeErr = errors.Join(drainErr, writerErr, checkpointErr)
	s.closed = true
	return s.closeErr
}

// CheckpointStatus reports the latest checkpoint and its effective WAL settings.
func (s *SQLiteStore) CheckpointStatus() persistence.SQLiteWALCheckpointStatus {
	if s == nil || s.checkpointWorker == nil {
		return persistence.SQLiteWALCheckpointStatus{}
	}
	return s.checkpointWorker.CheckpointStatus()
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
	return s.save(ctx, "persona_discomfort_log", item.EventID, `INSERT OR REPLACE INTO persona_discomfort_log (
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
	return s.save(ctx, "persona_trigger_log", item.EventID, `INSERT OR REPLACE INTO persona_trigger_log (
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
	return s.save(ctx, "canonical_response_log", item.EventID, `INSERT OR REPLACE INTO canonical_response_log (
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
	return s.save(ctx, "observation_log", item.EventID, `INSERT OR REPLACE INTO observation_log (
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
	if admission, ok := persistencequeue.AdmissionFromContext(ctx); ok {
		write, found, complete, err := s.admissionWrites.observationSnapshotAndPending(admission, eventID)
		if err != nil {
			return domainpersona.ObservationLog{}, false, err
		}
		if found {
			item, err := decodePersonaAdmissionWrite[domainpersona.ObservationLog](write)
			if err != nil {
				return domainpersona.ObservationLog{}, false, err
			}
			return item, true, nil
		}
		if !complete {
			return domainpersona.ObservationLog{}, false, ErrPersonaAdmissionSnapshotUnavailable
		}
		return domainpersona.ObservationLog{}, false, nil
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
	return s.save(ctx, "meta_profile_update", item.UpdateID, `INSERT OR REPLACE INTO meta_profile_update (
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
	return s.save(ctx, "persona_interface_session", item.SessionID, `INSERT OR REPLACE INTO persona_interface_session (
		session_id, character_id, interface_type, session_key, workstream_id, created_at, payload
	) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		item.SessionID, item.CharacterID, item.InterfaceType, item.SessionKey, item.WorkstreamID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListInterfaceSessions(ctx context.Context, limit int) ([]domainpersona.InterfaceSession, error) {
	return listSQLiteItems[domainpersona.InterfaceSession](ctx, s, "persona_interface_session", limit)
}

func (s *SQLiteStore) save(ctx context.Context, table, key, query string, args ...any) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("persona sqlite store is closed")
	}
	if len(args) == 0 || strings.TrimSpace(table) == "" || strings.TrimSpace(key) == "" {
		return fmt.Errorf("persona sqlite store save requires payload")
	}
	payload, err := json.Marshal(args[len(args)-1])
	if err != nil {
		return err
	}
	immutableArgs := append([]any(nil), args...)
	immutablePayload := string(payload)
	immutableArgs[len(immutableArgs)-1] = immutablePayload
	if admission, ok := persistencequeue.AdmissionFromContext(ctx); ok {
		immutableQuery := query
		return s.admissionWrites.enqueue(admission, table, key, "sqlite."+table+".insert_or_replace", immutablePayload, func(workerCtx context.Context) error {
			if s == nil || s.db == nil {
				return errors.New("persona sqlite store is closed")
			}
			_, err := s.db.ExecContext(workerCtx, immutableQuery, immutableArgs...)
			return err
		})
	}
	release, err := s.admissionWrites.beginSynchronous(ctx)
	if err != nil {
		return err
	}
	defer release()
	_, err = s.db.ExecContext(ctx, query, immutableArgs...)
	if err == nil {
		s.admissionWrites.recordSynchronous(table, key, immutablePayload)
	}
	return err
}

func listSQLiteItems[T any](ctx context.Context, s *SQLiteStore, table string, limit int) ([]T, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("persona sqlite store is closed")
	}
	if limit <= 0 {
		limit = 50
	}
	if admission, ok := persistencequeue.AdmissionFromContext(ctx); ok {
		snapshot, admissionWrites, _, err := s.admissionWrites.snapshotAndPendingFor(admission, table, limit)
		if err != nil {
			return nil, err
		}
		items := make([]T, 0, len(snapshot))
		for _, write := range snapshot {
			item, err := decodePersonaAdmissionWrite[T](write)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return mergePersonaAdmissionWrites(items, admissionWrites, limit)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

const timeFormatRFC3339Nano = "2006-01-02T15:04:05.999999999Z07:00"
