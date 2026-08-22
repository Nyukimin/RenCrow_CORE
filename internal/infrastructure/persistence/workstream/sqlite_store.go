package workstream

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	db        *sql.DB
	vaultRoot string
}

const sqliteBusyTimeoutMilliseconds = 5000

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	return NewSQLiteStoreWithVault(path, "")
}

func NewSQLiteStoreWithVault(path, vaultRoot string) (*SQLiteStore, error) {
	if path == "" {
		path = "workspace/logs/workstream.db"
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
	store := &SQLiteStore{db: db, vaultRoot: vaultRoot}
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
		`CREATE TABLE IF NOT EXISTS workstream (
			workstream_id TEXT PRIMARY KEY,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS workstream_goal (
			goal_id TEXT PRIMARY KEY,
			workstream_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS artifact (
			artifact_id TEXT PRIMARY KEY,
			workstream_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS artifact_annotation (
			annotation_id TEXT PRIMARY KEY,
			artifact_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS steering_queue (
			steering_id TEXT PRIMARY KEY,
			workstream_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS heartbeat_schedule (
			heartbeat_id TEXT PRIMARY KEY,
			workstream_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS vault_update_log (
			update_id TEXT PRIMARY KEY,
			workstream_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS implementation_lease (
			lease_name TEXT PRIMARY KEY,
			holder_unit_id TEXT NOT NULL,
			holder_workstream_id TEXT,
			stage TEXT,
			revision TEXT,
			acquired_at TEXT NOT NULL,
				heartbeat_at TEXT NOT NULL
			)`,
		`CREATE TABLE IF NOT EXISTS queue_freeze (
				freeze_id TEXT PRIMARY KEY,
				blocked_unit_id TEXT NOT NULL,
				blocked_revision INTEGER NOT NULL,
				created_at TEXT NOT NULL,
				payload TEXT NOT NULL
			)`,
		`CREATE TABLE IF NOT EXISTS stage_run_receipt (
				receipt_id TEXT PRIMARY KEY,
				idempotency_key TEXT NOT NULL UNIQUE,
				unit_id TEXT NOT NULL,
				implementation_revision INTEGER NOT NULL,
				created_at TEXT NOT NULL,
				payload TEXT NOT NULL
			)`,
		`CREATE TABLE IF NOT EXISTS closure_receipt (
				receipt_id TEXT PRIMARY KEY,
				idempotency_key TEXT NOT NULL UNIQUE,
				unit_id TEXT NOT NULL,
				implementation_revision INTEGER NOT NULL,
				created_at TEXT NOT NULL,
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

// AcquireImplementationLeaseIfUnfrozen performs the queue-freeze check and
// singleton lease write in one SQLite transaction.
func (s *SQLiteStore) AcquireImplementationLeaseIfUnfrozen(ctx context.Context, item domainworkstream.ImplementationLease) (bool, string, error) {
	if err := domainworkstream.ValidateImplementationLease(item); err != nil {
		return false, "", err
	}
	if s == nil || s.db == nil {
		return false, "", fmt.Errorf("workstream sqlite store is closed")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, "", err
	}
	defer func() { _ = tx.Rollback() }()
	frozen, err := activeQueueFreezeTx(ctx, tx)
	if err != nil {
		return false, "", err
	}
	if frozen {
		if err := tx.Commit(); err != nil {
			return false, "", err
		}
		return false, domainworkstream.ErrQueueFrozen.Error(), nil
	}
	var holder string
	err = tx.QueryRowContext(ctx, `SELECT holder_unit_id FROM implementation_lease WHERE lease_name = ?`, item.LeaseName).Scan(&holder)
	if err == nil {
		if holder != "" && holder != item.HolderUnitID {
			if commitErr := tx.Commit(); commitErr != nil {
				return false, "", commitErr
			}
			return false, domainworkstream.ErrImplementationLeaseHeld.Error(), nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE implementation_lease SET holder_unit_id=?, holder_workstream_id=?, stage=?, revision=?, acquired_at=?, heartbeat_at=? WHERE lease_name=?`, item.HolderUnitID, item.HolderWorkstreamID, item.Stage, item.Revision, item.AcquiredAt.Format(timeFormatRFC3339Nano), item.HeartbeatAt.Format(timeFormatRFC3339Nano), item.LeaseName); err != nil {
			return false, "", err
		}
	} else if err == sql.ErrNoRows {
		if _, err := tx.ExecContext(ctx, `INSERT INTO implementation_lease (lease_name, holder_unit_id, holder_workstream_id, stage, revision, acquired_at, heartbeat_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, item.LeaseName, item.HolderUnitID, item.HolderWorkstreamID, item.Stage, item.Revision, item.AcquiredAt.Format(timeFormatRFC3339Nano), item.HeartbeatAt.Format(timeFormatRFC3339Nano)); err != nil {
			return false, "", err
		}
	} else {
		return false, "", err
	}
	if err := tx.Commit(); err != nil {
		return false, "", err
	}
	return true, "", nil
}

// AcquireImplementationLease keeps the legacy result shape while routing all
// new writes through the freeze-aware atomic owner operation.
func (s *SQLiteStore) AcquireImplementationLease(ctx context.Context, item domainworkstream.ImplementationLease) (bool, error) {
	acquired, _, err := s.AcquireImplementationLeaseIfUnfrozen(ctx, item)
	return acquired, err
}

// ResolveQueueFreezeAndAcquireLease resolves one exact active freeze and
// acquires the replacement lease in one SQLite transaction.
func (s *SQLiteStore) ResolveQueueFreezeAndAcquireLease(ctx context.Context, freezeID string, resolution domainworkstream.QueueFreezeResolution, replacement domainworkstream.ImplementationLease) (domainworkstream.QueueFreeze, domainworkstream.ImplementationLease, bool, error) {
	if freezeID == "" {
		return domainworkstream.QueueFreeze{}, domainworkstream.ImplementationLease{}, false, fmt.Errorf("freeze_id is required")
	}
	if err := domainworkstream.ValidateQueueFreezeResolution(resolution, replacement); err != nil {
		return domainworkstream.QueueFreeze{}, domainworkstream.ImplementationLease{}, false, err
	}
	if s == nil || s.db == nil {
		return domainworkstream.QueueFreeze{}, domainworkstream.ImplementationLease{}, false, fmt.Errorf("workstream sqlite store is closed")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domainworkstream.QueueFreeze{}, domainworkstream.ImplementationLease{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	var payload string
	err = tx.QueryRowContext(ctx, `SELECT payload FROM queue_freeze WHERE freeze_id = ?`, freezeID).Scan(&payload)
	if err == sql.ErrNoRows {
		return domainworkstream.QueueFreeze{}, domainworkstream.ImplementationLease{}, false, domainworkstream.ErrQueueFreezeNotFound
	}
	if err != nil {
		return domainworkstream.QueueFreeze{}, domainworkstream.ImplementationLease{}, false, err
	}
	var freeze domainworkstream.QueueFreeze
	if err := json.Unmarshal([]byte(payload), &freeze); err != nil {
		return domainworkstream.QueueFreeze{}, domainworkstream.ImplementationLease{}, false, err
	}
	if freeze.Status == domainworkstream.QueueFreezeResolved {
		if !freeze.MatchesResolved(resolution) {
			return freeze, domainworkstream.ImplementationLease{}, false, domainworkstream.ErrQueueFreezeResolutionConflict
		}
		if err := tx.Commit(); err != nil {
			return freeze, domainworkstream.ImplementationLease{}, false, err
		}
		return freeze, freeze.ReplacementLease, freeze.ResolutionAcquired, nil
	}
	if freeze.Status != domainworkstream.QueueFreezeActive && freeze.Status != "" {
		return freeze, domainworkstream.ImplementationLease{}, false, domainworkstream.ErrQueueFreezeResolutionConflict
	}
	if freeze.FreezeRevision != resolution.ExpectedFreezeRevision {
		return freeze, domainworkstream.ImplementationLease{}, false, fmt.Errorf("%w: expected %d current %d", domainworkstream.ErrQueueFreezeRevisionConflict, resolution.ExpectedFreezeRevision, freeze.FreezeRevision)
	}
	var holder string
	leaseErr := tx.QueryRowContext(ctx, `SELECT holder_unit_id FROM implementation_lease WHERE lease_name = ?`, replacement.LeaseName).Scan(&holder)
	if leaseErr != nil && leaseErr != sql.ErrNoRows {
		return freeze, domainworkstream.ImplementationLease{}, false, leaseErr
	}
	if leaseErr == nil && holder != "" && holder != replacement.HolderUnitID {
		if err := tx.Commit(); err != nil {
			return freeze, domainworkstream.ImplementationLease{}, false, err
		}
		return freeze, domainworkstream.ImplementationLease{}, false, nil
	}

	now := time.Now().UTC()
	freeze.Status = domainworkstream.QueueFreezeResolved
	freeze.ResolutionRequestID = resolution.ResolutionRequestID
	freeze.ReplacementUnitID = resolution.ReplacementUnitID
	freeze.ReplacementLease = replacement
	freeze.ResolutionAcquired = true
	freeze.SupersedesUnitID = resolution.SupersedesUnitID
	freeze.BlockerResolutionRefs = append([]domainbacklog.EvidenceRef(nil), resolution.BlockerResolutionRefs...)
	freeze.ResolutionPayloadHash = resolution.ResolutionPayloadHash
	freeze.UpdatedAt = now
	freeze.ResolvedAt = now
	resolvedPayload, err := json.Marshal(freeze)
	if err != nil {
		return freeze, domainworkstream.ImplementationLease{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE queue_freeze SET blocked_unit_id=?, blocked_revision=?, created_at=?, payload=? WHERE freeze_id=?`, freeze.BlockedUnitID, freeze.BlockedRevision, freeze.CreatedAt.Format(timeFormatRFC3339Nano), string(resolvedPayload), freeze.FreezeID); err != nil {
		return freeze, domainworkstream.ImplementationLease{}, false, err
	}
	if leaseErr == sql.ErrNoRows {
		if _, err := tx.ExecContext(ctx, `INSERT INTO implementation_lease (lease_name, holder_unit_id, holder_workstream_id, stage, revision, acquired_at, heartbeat_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, replacement.LeaseName, replacement.HolderUnitID, replacement.HolderWorkstreamID, replacement.Stage, replacement.Revision, replacement.AcquiredAt.Format(timeFormatRFC3339Nano), replacement.HeartbeatAt.Format(timeFormatRFC3339Nano)); err != nil {
			return freeze, domainworkstream.ImplementationLease{}, false, err
		}
	} else if _, err := tx.ExecContext(ctx, `UPDATE implementation_lease SET holder_unit_id=?, holder_workstream_id=?, stage=?, revision=?, acquired_at=?, heartbeat_at=? WHERE lease_name=?`, replacement.HolderUnitID, replacement.HolderWorkstreamID, replacement.Stage, replacement.Revision, replacement.AcquiredAt.Format(timeFormatRFC3339Nano), replacement.HeartbeatAt.Format(timeFormatRFC3339Nano), replacement.LeaseName); err != nil {
		return freeze, domainworkstream.ImplementationLease{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return freeze, domainworkstream.ImplementationLease{}, false, err
	}
	return freeze, replacement, true, nil
}

func (s *SQLiteStore) ReleaseImplementationLease(ctx context.Context, leaseName, holderUnitID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("workstream sqlite store is closed")
	}
	if holderUnitID == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM implementation_lease WHERE lease_name = ?`, leaseName)
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM implementation_lease WHERE lease_name = ? AND holder_unit_id = ?`, leaseName, holderUnitID)
	return err
}

func (s *SQLiteStore) GetImplementationLease(ctx context.Context, leaseName string) (domainworkstream.ImplementationLease, bool, error) {
	if s == nil || s.db == nil {
		return domainworkstream.ImplementationLease{}, false, fmt.Errorf("workstream sqlite store is closed")
	}
	var item domainworkstream.ImplementationLease
	var acquired, heartbeat string
	err := s.db.QueryRowContext(ctx, `SELECT lease_name, holder_unit_id, holder_workstream_id, stage, revision, acquired_at, heartbeat_at FROM implementation_lease WHERE lease_name = ?`, leaseName).Scan(&item.LeaseName, &item.HolderUnitID, &item.HolderWorkstreamID, &item.Stage, &item.Revision, &acquired, &heartbeat)
	if err == sql.ErrNoRows {
		return domainworkstream.ImplementationLease{}, false, nil
	}
	if err != nil {
		return domainworkstream.ImplementationLease{}, false, err
	}
	item.AcquiredAt, err = time.Parse(timeFormatRFC3339Nano, acquired)
	if err != nil {
		return domainworkstream.ImplementationLease{}, false, err
	}
	item.HeartbeatAt, err = time.Parse(timeFormatRFC3339Nano, heartbeat)
	if err != nil {
		return domainworkstream.ImplementationLease{}, false, err
	}
	return item, item.HolderUnitID != "", nil
}

func (s *SQLiteStore) HeartbeatImplementationLease(ctx context.Context, item domainworkstream.ImplementationLease) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("workstream sqlite store is closed")
	}
	if item.HeartbeatAt.IsZero() {
		item.HeartbeatAt = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `UPDATE implementation_lease SET stage=?, revision=?, heartbeat_at=? WHERE lease_name=? AND holder_unit_id=?`, item.Stage, item.Revision, item.HeartbeatAt.Format(timeFormatRFC3339Nano), item.LeaseName, item.HolderUnitID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("implementation lease is not held by %s", item.HolderUnitID)
	}
	return nil
}

func (s *SQLiteStore) AcquireLease(ctx context.Context, item domainworkstream.ImplementationLease) (bool, error) {
	return s.AcquireImplementationLease(ctx, item)
}
func (s *SQLiteStore) ReleaseLease(ctx context.Context, name, holder string) error {
	return s.ReleaseImplementationLease(ctx, name, holder)
}
func (s *SQLiteStore) GetLease(ctx context.Context, name string) (domainworkstream.ImplementationLease, bool, error) {
	return s.GetImplementationLease(ctx, name)
}
func (s *SQLiteStore) HeartbeatLease(ctx context.Context, item domainworkstream.ImplementationLease) error {
	return s.HeartbeatImplementationLease(ctx, item)
}

func (s *SQLiteStore) SaveQueueFreeze(ctx context.Context, item domainworkstream.QueueFreeze) error {
	if item.FreezeRevision < 1 {
		item.FreezeRevision = 1
	}
	if item.Status == "" {
		item.Status = domainworkstream.QueueFreezeActive
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = item.CreatedAt
	}
	if err := domainworkstream.ValidateQueueFreeze(item); err != nil {
		return err
	}
	if s == nil || s.db == nil {
		return fmt.Errorf("workstream sqlite store is closed")
	}
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT OR REPLACE INTO queue_freeze (freeze_id, blocked_unit_id, blocked_revision, created_at, payload) VALUES (?, ?, ?, ?, ?)`, item.FreezeID, item.BlockedUnitID, item.BlockedRevision, item.CreatedAt.Format(timeFormatRFC3339Nano), string(payload))
	return err
}

func (s *SQLiteStore) GetQueueFreeze(ctx context.Context, freezeID string) (domainworkstream.QueueFreeze, bool, error) {
	var payload string
	if s == nil || s.db == nil {
		return domainworkstream.QueueFreeze{}, false, fmt.Errorf("workstream sqlite store is closed")
	}
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM queue_freeze WHERE freeze_id = ?`, freezeID).Scan(&payload)
	if err == sql.ErrNoRows {
		return domainworkstream.QueueFreeze{}, false, nil
	}
	if err != nil {
		return domainworkstream.QueueFreeze{}, false, err
	}
	var item domainworkstream.QueueFreeze
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		return domainworkstream.QueueFreeze{}, false, err
	}
	return item, true, nil
}

func (s *SQLiteStore) ListQueueFreezes(ctx context.Context, limit int) ([]domainworkstream.QueueFreeze, error) {
	return listSQLiteItems[domainworkstream.QueueFreeze](ctx, s, "queue_freeze", limit)
}

func (s *SQLiteStore) SaveStageRunReceipt(ctx context.Context, item domainworkstream.StageRunReceipt) error {
	if err := domainworkstream.ValidateStageRunReceipt(item); err != nil {
		return err
	}
	return s.saveLifecycleReceipt(ctx, "stage_run_receipt", item.ReceiptID, item.IdempotencyKey, item.UnitID, item.ImplementationRevision, item.CreatedAt, item)
}

func (s *SQLiteStore) FindStageRunReceipt(ctx context.Context, key string) (domainworkstream.StageRunReceipt, bool, error) {
	return findSQLiteLifecycleReceipt[domainworkstream.StageRunReceipt](ctx, s, "stage_run_receipt", key)
}

func (s *SQLiteStore) GetStageRunReceipt(ctx context.Context, key string) (domainworkstream.StageRunReceipt, bool, error) {
	return s.FindStageRunReceipt(ctx, key)
}

func (s *SQLiteStore) ListStageRunReceipts(ctx context.Context, limit int) ([]domainworkstream.StageRunReceipt, error) {
	return listSQLiteItems[domainworkstream.StageRunReceipt](ctx, s, "stage_run_receipt", limit)
}

func (s *SQLiteStore) SaveClosureReceipt(ctx context.Context, item domainworkstream.ClosureReceipt) error {
	if err := domainworkstream.ValidateClosureReceipt(item); err != nil {
		return err
	}
	return s.saveLifecycleReceipt(ctx, "closure_receipt", item.ReceiptID, item.IdempotencyKey, item.UnitID, item.ImplementationRevision, item.CreatedAt, item)
}

func (s *SQLiteStore) FindClosureReceipt(ctx context.Context, key string) (domainworkstream.ClosureReceipt, bool, error) {
	return findSQLiteLifecycleReceipt[domainworkstream.ClosureReceipt](ctx, s, "closure_receipt", key)
}

func (s *SQLiteStore) GetClosureReceipt(ctx context.Context, key string) (domainworkstream.ClosureReceipt, bool, error) {
	return s.FindClosureReceipt(ctx, key)
}

func (s *SQLiteStore) ListClosureReceipts(ctx context.Context, limit int) ([]domainworkstream.ClosureReceipt, error) {
	return listSQLiteItems[domainworkstream.ClosureReceipt](ctx, s, "closure_receipt", limit)
}

func (s *SQLiteStore) SaveWorkstream(ctx context.Context, item domainworkstream.Workstream) error {
	if err := domainworkstream.ValidateWorkstream(item); err != nil {
		return err
	}
	if s.vaultRoot != "" {
		vaultPath, err := ensureVaultFiles(s.vaultRoot, item)
		if err != nil {
			return err
		}
		if item.VaultPath == "" {
			item.VaultPath = vaultPath
		}
	}
	return s.save(ctx, "workstream", "workstream_id", item.WorkstreamID, "", "", item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListWorkstreams(ctx context.Context, limit int) ([]domainworkstream.Workstream, error) {
	return listSQLiteItems[domainworkstream.Workstream](ctx, s, "workstream", limit)
}

func (s *SQLiteStore) SaveGoal(ctx context.Context, item domainworkstream.Goal) error {
	if err := domainworkstream.ValidateGoal(item); err != nil {
		return err
	}
	return s.save(ctx, "workstream_goal", "goal_id", item.GoalID, "workstream_id", item.WorkstreamID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

// FindGoalByID returns the row with the exact primary ID without scanning a list.
func (s *SQLiteStore) FindGoalByID(ctx context.Context, goalID string) (domainworkstream.Goal, bool, error) {
	if s == nil || s.db == nil {
		return domainworkstream.Goal{}, false, fmt.Errorf("workstream sqlite store is closed")
	}
	var payload string
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM workstream_goal WHERE goal_id = ?`, goalID).Scan(&payload)
	if err != nil {
		if err == sql.ErrNoRows {
			return domainworkstream.Goal{}, false, nil
		}
		return domainworkstream.Goal{}, false, err
	}
	var item domainworkstream.Goal
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		return domainworkstream.Goal{}, false, err
	}
	return item, true, nil
}

func (s *SQLiteStore) ListGoals(ctx context.Context, limit int) ([]domainworkstream.Goal, error) {
	return listSQLiteItems[domainworkstream.Goal](ctx, s, "workstream_goal", limit)
}

func (s *SQLiteStore) SaveArtifact(ctx context.Context, item domainworkstream.Artifact) error {
	if err := domainworkstream.ValidateArtifact(item); err != nil {
		return err
	}
	return s.save(ctx, "artifact", "artifact_id", item.ArtifactID, "workstream_id", item.WorkstreamID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListArtifacts(ctx context.Context, limit int) ([]domainworkstream.Artifact, error) {
	return listSQLiteItems[domainworkstream.Artifact](ctx, s, "artifact", limit)
}

func (s *SQLiteStore) SaveArtifactAnnotation(ctx context.Context, item domainworkstream.ArtifactAnnotation) error {
	if err := domainworkstream.ValidateArtifactAnnotation(item); err != nil {
		return err
	}
	return s.save(ctx, "artifact_annotation", "annotation_id", item.AnnotationID, "artifact_id", item.ArtifactID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListArtifactAnnotations(ctx context.Context, limit int) ([]domainworkstream.ArtifactAnnotation, error) {
	return listSQLiteItems[domainworkstream.ArtifactAnnotation](ctx, s, "artifact_annotation", limit)
}

func (s *SQLiteStore) SaveSteeringItem(ctx context.Context, item domainworkstream.SteeringItem) error {
	if err := domainworkstream.ValidateSteeringItem(item); err != nil {
		return err
	}
	return s.save(ctx, "steering_queue", "steering_id", item.SteeringID, "workstream_id", item.WorkstreamID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListSteeringItems(ctx context.Context, limit int) ([]domainworkstream.SteeringItem, error) {
	return listSQLiteItems[domainworkstream.SteeringItem](ctx, s, "steering_queue", limit)
}

func (s *SQLiteStore) SaveHeartbeatSchedule(ctx context.Context, item domainworkstream.HeartbeatSchedule) error {
	if err := domainworkstream.ValidateHeartbeatSchedule(item); err != nil {
		return err
	}
	return s.save(ctx, "heartbeat_schedule", "heartbeat_id", item.HeartbeatID, "workstream_id", item.WorkstreamID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListHeartbeatSchedules(ctx context.Context, limit int) ([]domainworkstream.HeartbeatSchedule, error) {
	return listSQLiteItems[domainworkstream.HeartbeatSchedule](ctx, s, "heartbeat_schedule", limit)
}

func (s *SQLiteStore) SaveVaultUpdateLog(ctx context.Context, item domainworkstream.VaultUpdateLog) error {
	if err := domainworkstream.ValidateVaultUpdateLog(item); err != nil {
		return err
	}
	return s.save(ctx, "vault_update_log", "update_id", item.UpdateID, "workstream_id", item.WorkstreamID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListVaultUpdateLogs(ctx context.Context, limit int) ([]domainworkstream.VaultUpdateLog, error) {
	return listSQLiteItems[domainworkstream.VaultUpdateLog](ctx, s, "vault_update_log", limit)
}

func (s *SQLiteStore) save(ctx context.Context, table string, idColumn string, id string, secondaryColumn string, secondaryValue string, createdAt string, item any) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("workstream sqlite store is closed")
	}
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	if secondaryColumn == "" {
		query := fmt.Sprintf(`INSERT OR REPLACE INTO %s (%s, created_at, payload) VALUES (?, ?, ?)`, table, idColumn)
		_, err = s.db.ExecContext(ctx, query, id, createdAt, string(payload))
		return err
	}
	query := fmt.Sprintf(`INSERT OR REPLACE INTO %s (%s, %s, created_at, payload) VALUES (?, ?, ?, ?)`, table, idColumn, secondaryColumn)
	_, err = s.db.ExecContext(ctx, query, id, secondaryValue, createdAt, string(payload))
	return err
}

func (s *SQLiteStore) saveLifecycleReceipt(ctx context.Context, table, receiptID, idempotencyKey, unitID string, revision int, createdAt time.Time, item any) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("workstream sqlite store is closed")
	}
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	query := fmt.Sprintf(`INSERT OR REPLACE INTO %s (receipt_id, idempotency_key, unit_id, implementation_revision, created_at, payload) VALUES (?, ?, ?, ?, ?, ?)`, table)
	_, err = s.db.ExecContext(ctx, query, receiptID, idempotencyKey, unitID, revision, createdAt.Format(timeFormatRFC3339Nano), string(payload))
	return err
}

func findSQLiteLifecycleReceipt[T any](ctx context.Context, s *SQLiteStore, table, key string) (T, bool, error) {
	var item T
	if s == nil || s.db == nil {
		return item, false, fmt.Errorf("workstream sqlite store is closed")
	}
	var payload string
	query := fmt.Sprintf(`SELECT payload FROM %s WHERE idempotency_key = ? OR receipt_id = ?`, table)
	err := s.db.QueryRowContext(ctx, query, key, key).Scan(&payload)
	if err == sql.ErrNoRows {
		return item, false, nil
	}
	if err != nil {
		return item, false, err
	}
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		return item, false, err
	}
	return item, true, nil
}

func listSQLiteItems[T any](ctx context.Context, s *SQLiteStore, table string, limit int) ([]T, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("workstream sqlite store is closed")
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

func activeQueueFreezeTx(ctx context.Context, tx *sql.Tx) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT payload FROM queue_freeze ORDER BY rowid DESC`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return false, err
		}
		var freeze domainworkstream.QueueFreeze
		if err := json.Unmarshal([]byte(payload), &freeze); err != nil {
			return false, err
		}
		if freeze.Status == domainworkstream.QueueFreezeActive || freeze.Status == "" {
			return true, nil
		}
	}
	return false, rows.Err()
}

const timeFormatRFC3339Nano = "2006-01-02T15:04:05.999999999Z07:00"
