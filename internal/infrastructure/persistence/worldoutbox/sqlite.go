// Package worldoutbox owns CORE's durable WORLD projection delivery record.
// It stores the exact envelope and source ordering so a retry after a crash
// replays the same bytes rather than rebuilding a newer projection.
package worldoutbox

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	appprojection "github.com/Nyukimin/RenCrow_CORE/internal/application/worldprojection"
	domainprojection "github.com/Nyukimin/RenCrow_CORE/internal/domain/worldprojection"

	_ "modernc.org/sqlite"
)

const (
	schemaVersion = 2
	timeFormat    = "2006-01-02T15:04:05.000000000Z07:00"
	claimLease    = 5 * time.Minute
)

// SQLiteStore is the CORE-owned durable projection outbox. It is not a second
// source of Task/Run truth; it records delivery of a CORE projection snapshot.
type SQLiteStore struct {
	db *sql.DB
}

var _ appprojection.Outbox = (*SQLiteStore)(nil)

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("world projection outbox: sqlite path is required")
	}
	if !filepath.IsAbs(path) {
		return nil, errors.New("world projection outbox: sqlite path must be absolute")
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("world projection outbox: parent directory: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("world projection outbox: parent is not a directory")
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout%3d5000")
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
	if s == nil || s.db == nil {
		return errors.New("world projection outbox: store is closed")
	}
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version == schemaVersion {
		return nil
	}
	if version != 0 && version != 1 {
		return fmt.Errorf("world projection outbox: unsupported schema version %d", version)
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS world_projection_outbox (
		id TEXT PRIMARY KEY NOT NULL,
		world_id TEXT NOT NULL,
		timeline_id TEXT NOT NULL,
		timeline_epoch TEXT NOT NULL,
		actor_id TEXT NOT NULL,
		actor_instance_id TEXT NOT NULL,
		actor_transport_id TEXT NOT NULL,
		kind TEXT NOT NULL,
		source_epoch TEXT NOT NULL,
		source_revision INTEGER NOT NULL,
		digest TEXT NOT NULL,
		envelope BLOB NOT NULL,
		reconcile INTEGER NOT NULL,
		status TEXT NOT NULL,
		attempts INTEGER NOT NULL DEFAULT 0,
		next_attempt_at TEXT NOT NULL DEFAULT '',
		last_error TEXT NOT NULL DEFAULT '',
		ack_status TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		sent_at TEXT NOT NULL DEFAULT '',
		UNIQUE(world_id, timeline_id, timeline_epoch, actor_id, kind, source_epoch, source_revision)
	)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_world_projection_outbox_due
		ON world_projection_outbox(status, next_attempt_at)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS world_projection_source_revision (
		world_id TEXT NOT NULL,
		timeline_id TEXT NOT NULL,
		timeline_epoch TEXT NOT NULL,
		actor_id TEXT NOT NULL,
		kind TEXT NOT NULL,
		source_epoch TEXT NOT NULL,
		source_revision INTEGER NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY(world_id, timeline_id, timeline_epoch, actor_id, kind)
	)`); err != nil {
		return err
	}
	if version == 1 {
		if err := seedSourceRevisions(tx); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("PRAGMA user_version = 2"); err != nil {
		return err
	}
	return tx.Commit()
}

// seedSourceRevisions carries the durable source ordering forward when a v1
// outbox is opened by a v2 store. Every destination continues from its highest
// existing revision, so an upgrade cannot reuse or regress a revision.
func seedSourceRevisions(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT world_id, timeline_id, timeline_epoch, actor_id, kind,
		source_epoch, source_revision, updated_at
		FROM world_projection_outbox o
		WHERE rowid = (
			SELECT o2.rowid FROM world_projection_outbox o2
			WHERE o2.world_id = o.world_id AND o2.timeline_id = o.timeline_id
			  AND o2.timeline_epoch = o.timeline_epoch AND o2.actor_id = o.actor_id
			  AND o2.kind = o.kind
			ORDER BY o2.source_revision DESC, o2.rowid DESC LIMIT 1
		)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type seed struct {
		worldID, timelineID, timelineEpoch, actorID, kind, sourceEpoch, updatedAt string
		revision                                                                  uint64
	}
	var seeds []seed
	for rows.Next() {
		var value seed
		if err := rows.Scan(&value.worldID, &value.timelineID, &value.timelineEpoch,
			&value.actorID, &value.kind, &value.sourceEpoch, &value.revision, &value.updatedAt); err != nil {
			return err
		}
		seeds = append(seeds, value)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, value := range seeds {
		if _, err := tx.Exec(`INSERT INTO world_projection_source_revision
			(world_id, timeline_id, timeline_epoch, actor_id, kind, source_epoch, source_revision, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			value.worldID, value.timelineID, value.timelineEpoch, value.actorID, value.kind,
			value.sourceEpoch, value.revision, value.updatedAt); err != nil {
			return err
		}
	}
	return nil
}

// Enqueue records one projection with CORE-side ordering. The same
// destination/kind/epoch/revision and digest is an idempotent retry; a
// different digest is a conflict; an older revision is stale. A new epoch is
// accepted only as an explicit reconcile and supersedes unfinished old-epoch
// records.
func (s *SQLiteStore) Enqueue(ctx context.Context, record appprojection.OutboxRecord, now time.Time) error {
	if s == nil || s.db == nil {
		return errors.New("world projection outbox: store is closed")
	}
	if err := validateRecord(record); err != nil {
		return err
	}
	ctx = defaultContext(ctx)
	now = utcNow(now)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var (
		currentRevision uint64
		currentDigest   string
	)
	haveRecord := true
	err = tx.QueryRowContext(ctx, `SELECT source_revision, digest
		FROM world_projection_outbox
		WHERE world_id = ? AND timeline_id = ? AND timeline_epoch = ? AND actor_id = ? AND kind = ?
		ORDER BY rowid DESC LIMIT 1`,
		record.Target.WorldID, record.Target.TimelineID, record.Target.TimelineEpoch,
		record.Target.ActorID, record.Kind).Scan(&currentRevision, &currentDigest)
	if errors.Is(err, sql.ErrNoRows) {
		haveRecord = false
		err = nil
	}
	if err != nil {
		return err
	}
	// The durable source counter owns which epoch is current. The newest outbox
	// row is only the fallback for a missing counter row, so an epoch change is
	// never accepted implicitly just because a destination has no row yet.
	epoch, _, err := readSourceRevision(ctx, tx, record.Target, record.Kind, "")
	if err != nil {
		return err
	}
	switch {
	case epoch != "" && epoch != record.SourceEpoch:
		if !record.Reconcile {
			return appprojection.ErrEpochChangeRequiresReconcile
		}
		if _, err := tx.ExecContext(ctx, `UPDATE world_projection_outbox
			SET status = ?, updated_at = ?
			WHERE world_id = ? AND timeline_id = ? AND timeline_epoch = ? AND actor_id = ? AND kind = ?
			  AND source_epoch <> ? AND status IN (?, ?, ?)`,
			appprojection.OutboxSuperseded, formatTime(now),
			record.Target.WorldID, record.Target.TimelineID, record.Target.TimelineEpoch,
			record.Target.ActorID, record.Kind, record.SourceEpoch,
			appprojection.OutboxPending, appprojection.OutboxInFlight, appprojection.OutboxFailed); err != nil {
			return err
		}
		// An authenticated reconcile is the only authorized epoch switch, so it
		// moves the durable source counter in the same transaction. Later
		// allocations continue from the new epoch instead of rejecting forever.
		if err := advanceSourceRevision(ctx, tx, record.Target, record.Kind,
			record.SourceEpoch, record.SourceRevision, now); err != nil {
			return err
		}
	case haveRecord && record.SourceRevision < currentRevision:
		return appprojection.ErrStaleRevision
	case haveRecord && record.SourceRevision == currentRevision:
		if record.Digest != currentDigest {
			return appprojection.ErrRevisionConflict
		}
		return tx.Commit()
	}

	_, err = tx.ExecContext(ctx, `INSERT INTO world_projection_outbox
		(id, world_id, timeline_id, timeline_epoch, actor_id, actor_instance_id, actor_transport_id,
		 kind, source_epoch, source_revision, digest, envelope, reconcile, status, attempts,
		 next_attempt_at, last_error, ack_status, created_at, updated_at, sent_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, '', '', ?, ?, '')`,
		record.ID, record.Target.WorldID, record.Target.TimelineID, record.Target.TimelineEpoch,
		record.Target.ActorID, record.Target.ActorInstanceID, record.Target.ActorTransportID,
		record.Kind, record.SourceEpoch, record.SourceRevision, record.Digest, record.Envelope,
		boolInt(record.Reconcile), appprojection.OutboxPending, formatTime(now),
		formatTime(now), formatTime(now))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return appprojection.ErrRevisionConflict
		}
		return err
	}
	return tx.Commit()
}

// Latest returns the most recently enqueued record for a destination/kind.
// It includes failed and superseded records because the projector must not
// mistake an old accepted snapshot for a current one.
func (s *SQLiteStore) Latest(ctx context.Context, target domainprojection.Target, kind string) (appprojection.OutboxRecord, bool, error) {
	if s == nil || s.db == nil {
		return appprojection.OutboxRecord{}, false, errors.New("world projection outbox: store is closed")
	}
	if err := target.Validate(); err != nil {
		return appprojection.OutboxRecord{}, false, err
	}
	if kind != domainprojection.KindWorkSet && kind != domainprojection.KindLifePolicy {
		return appprojection.OutboxRecord{}, false, errors.New("world projection outbox: unsupported projection kind")
	}
	ctx = defaultContext(ctx)
	record, err := scanRecord(s.db.QueryRowContext(ctx, `SELECT `+columns+`
		FROM world_projection_outbox
		WHERE world_id = ? AND timeline_id = ? AND timeline_epoch = ? AND actor_id = ? AND kind = ?
		ORDER BY rowid DESC LIMIT 1`,
		target.WorldID, target.TimelineID, target.TimelineEpoch, target.ActorID, kind))
	if errors.Is(err, sql.ErrNoRows) {
		return appprojection.OutboxRecord{}, false, nil
	}
	if err != nil {
		return appprojection.OutboxRecord{}, false, err
	}
	return record, true, nil
}

// NextSourceRevision allocates the next monotonic source revision for one
// destination/kind/epoch. The counter is durable in the same store as the
// outbox so a restart cannot reuse a revision. An epoch change is rejected
// here; it requires the explicit WORLD reconcile path.
func (s *SQLiteStore) NextSourceRevision(ctx context.Context, target domainprojection.Target, kind, sourceEpoch string, now time.Time) (uint64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("world projection outbox: store is closed")
	}
	if err := target.Validate(); err != nil {
		return 0, err
	}
	if kind != domainprojection.KindWorkSet && kind != domainprojection.KindLifePolicy {
		return 0, errors.New("world projection outbox: unsupported projection kind")
	}
	if !validText(sourceEpoch) {
		return 0, errors.New("world projection outbox: source epoch is required")
	}
	ctx = defaultContext(ctx)
	now = utcNow(now)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	currentEpoch, currentRevision, err := readSourceRevision(ctx, tx, target, kind, sourceEpoch)
	if err != nil {
		return 0, err
	}
	if currentEpoch != sourceEpoch {
		return 0, appprojection.ErrEpochChangeRequiresReconcile
	}
	if currentRevision >= domainprojection.MaxSourceRevision {
		return 0, errors.New("world projection outbox: source revision exhausted")
	}
	next := currentRevision + 1
	if err := advanceSourceRevision(ctx, tx, target, kind, sourceEpoch, next, now); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return next, nil
}

// CurrentSourceRevision reports the durable source epoch and the highest
// allocated revision for one destination/kind. The projector uses it to detect
// that CORE's configured source epoch moved, which is the D-06 source epoch
// switch and is only ever performed through the authenticated reconcile path.
func (s *SQLiteStore) CurrentSourceRevision(ctx context.Context, target domainprojection.Target, kind string) (string, uint64, bool, error) {
	if s == nil || s.db == nil {
		return "", 0, false, errors.New("world projection outbox: store is closed")
	}
	if err := target.Validate(); err != nil {
		return "", 0, false, err
	}
	if kind != domainprojection.KindWorkSet && kind != domainprojection.KindLifePolicy {
		return "", 0, false, errors.New("world projection outbox: unsupported projection kind")
	}
	ctx = defaultContext(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", 0, false, err
	}
	defer tx.Rollback()
	epoch, revision, err := readSourceRevision(ctx, tx, target, kind, "")
	if err != nil {
		return "", 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return "", 0, false, err
	}
	if epoch == "" {
		return "", 0, false, nil
	}
	return epoch, revision, true, nil
}

// sourceRevisionQuerier is satisfied by both *sql.DB and *sql.Tx so the counter
// read has one definition.
type sourceRevisionQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// readSourceRevision reads the durable counter for one destination/kind. When
// the counter row is missing (a partially applied migration or a repaired
// database) it recovers from the committed outbox history, which can only move
// forward. When there is no history at all it reports no epoch; defaultEpoch is
// used only by the allocator, which treats an unused destination as revision 0.
func readSourceRevision(ctx context.Context, q sourceRevisionQuerier, target domainprojection.Target, kind, defaultEpoch string) (string, uint64, error) {
	var epoch string
	var revision uint64
	err := q.QueryRowContext(ctx, `SELECT source_epoch, source_revision
		FROM world_projection_source_revision
		WHERE world_id = ? AND timeline_id = ? AND timeline_epoch = ? AND actor_id = ? AND kind = ?`,
		target.WorldID, target.TimelineID, target.TimelineEpoch, target.ActorID, kind).
		Scan(&epoch, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		err = q.QueryRowContext(ctx, `SELECT source_epoch, source_revision
			FROM world_projection_outbox
			WHERE world_id = ? AND timeline_id = ? AND timeline_epoch = ? AND actor_id = ? AND kind = ?
			ORDER BY source_revision DESC, rowid DESC LIMIT 1`,
			target.WorldID, target.TimelineID, target.TimelineEpoch, target.ActorID, kind).
			Scan(&epoch, &revision)
		if errors.Is(err, sql.ErrNoRows) {
			return defaultEpoch, 0, nil
		}
	}
	if err != nil {
		return "", 0, err
	}
	return epoch, revision, nil
}

// advanceSourceRevision is the single write path for the durable counter, so
// allocation and an authenticated epoch switch cannot drift apart.
func advanceSourceRevision(ctx context.Context, tx *sql.Tx, target domainprojection.Target, kind, sourceEpoch string, revision uint64, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO world_projection_source_revision
		(world_id, timeline_id, timeline_epoch, actor_id, kind, source_epoch, source_revision, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(world_id, timeline_id, timeline_epoch, actor_id, kind)
		DO UPDATE SET source_epoch = excluded.source_epoch, source_revision = excluded.source_revision,
			updated_at = excluded.updated_at`,
		target.WorldID, target.TimelineID, target.TimelineEpoch, target.ActorID, kind,
		sourceEpoch, revision, formatTime(now))
	return err
}

func (s *SQLiteStore) Claim(ctx context.Context, now time.Time, limit int) ([]appprojection.OutboxRecord, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("world projection outbox: store is closed")
	}
	if limit <= 0 {
		return nil, errors.New("world projection outbox: claim limit must be positive")
	}
	ctx = defaultContext(ctx)
	now = utcNow(now)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	staleBefore := now.Add(-claimLease)
	rows, err := tx.QueryContext(ctx, `SELECT `+columns+`
		FROM world_projection_outbox
		WHERE (status = ? AND next_attempt_at <= ?)
		   OR (status = ? AND updated_at <= ?)
		ORDER BY source_revision ASC, created_at ASC, id ASC
		LIMIT ?`, appprojection.OutboxPending, formatTime(now),
		appprojection.OutboxInFlight, formatTime(staleBefore), limit)
	if err != nil {
		return nil, err
	}
	var records []appprojection.OutboxRecord
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	claimed := records[:0]
	for _, record := range records {
		result, err := tx.ExecContext(ctx, `UPDATE world_projection_outbox
			SET status = ?, updated_at = ?
			WHERE id = ? AND (status = ? OR (status = ? AND updated_at <= ?))`,
			appprojection.OutboxInFlight, formatTime(now), record.ID,
			appprojection.OutboxPending, appprojection.OutboxInFlight, formatTime(staleBefore))
		if err != nil {
			return nil, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if affected == 1 {
			record.Status = appprojection.OutboxInFlight
			record.UpdatedAt = now
			claimed = append(claimed, record)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claimed, nil
}

func (s *SQLiteStore) Complete(ctx context.Context, id string, ack appprojection.PublishResult, now time.Time) error {
	if s == nil || s.db == nil {
		return errors.New("world projection outbox: store is closed")
	}
	ctx = defaultContext(ctx)
	now = utcNow(now)
	result, err := s.db.ExecContext(ctx, `UPDATE world_projection_outbox
		SET status = ?, ack_status = ?, updated_at = ?, sent_at = ?
		WHERE id = ? AND status IN (?, ?)`,
		appprojection.OutboxSent, ack.Status, formatTime(now), formatTime(now), id,
		appprojection.OutboxInFlight, appprojection.OutboxPending)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	exists, err := s.exists(ctx, id)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return appprojection.ErrOutboxRecordNotFound
}

func (s *SQLiteStore) Fail(ctx context.Context, id string, retryAt time.Time, terminal bool, message string, now time.Time) error {
	if s == nil || s.db == nil {
		return errors.New("world projection outbox: store is closed")
	}
	ctx = defaultContext(ctx)
	now = utcNow(now)
	status := appprojection.OutboxPending
	next := formatTime(retryAt.UTC())
	if terminal {
		status = appprojection.OutboxFailed
		next = ""
	}
	result, err := s.db.ExecContext(ctx, `UPDATE world_projection_outbox
		SET status = ?, attempts = attempts + 1, next_attempt_at = ?, last_error = ?, updated_at = ?
		WHERE id = ? AND status IN (?, ?)`,
		status, next, message, formatTime(now), id, appprojection.OutboxInFlight, appprojection.OutboxPending)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	exists, err := s.exists(ctx, id)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return appprojection.ErrOutboxRecordNotFound
}

func (s *SQLiteStore) Get(ctx context.Context, id string) (appprojection.OutboxRecord, error) {
	if s == nil || s.db == nil {
		return appprojection.OutboxRecord{}, errors.New("world projection outbox: store is closed")
	}
	ctx = defaultContext(ctx)
	record, err := scanRecord(s.db.QueryRowContext(ctx, `SELECT `+columns+`
		FROM world_projection_outbox WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return appprojection.OutboxRecord{}, appprojection.ErrOutboxRecordNotFound
	}
	return record, err
}

func (s *SQLiteStore) exists(ctx context.Context, id string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM world_projection_outbox WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

const columns = `id, world_id, timeline_id, timeline_epoch, actor_id, actor_instance_id,
	actor_transport_id, kind, source_epoch, source_revision, digest, envelope, reconcile,
	status, attempts, next_attempt_at, last_error, ack_status, created_at, updated_at, sent_at`

type rowScanner interface {
	Scan(...any) error
}

func scanRecord(row rowScanner) (appprojection.OutboxRecord, error) {
	var (
		record                                 appprojection.OutboxRecord
		nextAttemptAt, createdAt, updatedAt    string
		sentAt, lastError, ackStatus, sourceEp string
		reconcile                              int
	)
	if err := row.Scan(
		&record.ID, &record.Target.WorldID, &record.Target.TimelineID, &record.Target.TimelineEpoch,
		&record.Target.ActorID, &record.Target.ActorInstanceID, &record.Target.ActorTransportID,
		&record.Kind, &sourceEp, &record.SourceRevision, &record.Digest, &record.Envelope, &reconcile,
		&record.Status, &record.Attempts, &nextAttemptAt, &lastError, &ackStatus,
		&createdAt, &updatedAt, &sentAt,
	); err != nil {
		return appprojection.OutboxRecord{}, err
	}
	record.SourceEpoch = sourceEp
	record.Reconcile = reconcile != 0
	record.LastError = lastError
	record.AckStatus = ackStatus
	var err error
	if record.NextAttemptAt, err = parseTime(nextAttemptAt); err != nil {
		return appprojection.OutboxRecord{}, err
	}
	if record.CreatedAt, err = parseTime(createdAt); err != nil {
		return appprojection.OutboxRecord{}, err
	}
	if record.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return appprojection.OutboxRecord{}, err
	}
	if record.SentAt, err = parseTime(sentAt); err != nil {
		return appprojection.OutboxRecord{}, err
	}
	return record, nil
}

func validateRecord(record appprojection.OutboxRecord) error {
	if !validText(record.ID) {
		return errors.New("world projection outbox: record id is required")
	}
	if err := record.Target.Validate(); err != nil {
		return err
	}
	if record.Kind != domainprojection.KindWorkSet && record.Kind != domainprojection.KindLifePolicy {
		return errors.New("world projection outbox: unsupported projection kind")
	}
	if err := (domainprojection.SourceRevision{Epoch: record.SourceEpoch, Revision: record.SourceRevision}).Validate(); err != nil {
		return err
	}
	if len(record.Digest) != 64 {
		return errors.New("world projection outbox: digest must be lowercase hex SHA-256")
	}
	if _, err := hex.DecodeString(record.Digest); err != nil || strings.ToLower(record.Digest) != record.Digest {
		return errors.New("world projection outbox: digest must be lowercase hex SHA-256")
	}
	if len(record.Envelope) == 0 || len(record.Envelope) > domainprojection.MaxEnvelopeBytes {
		return errors.New("world projection outbox: envelope size is invalid")
	}
	return nil
}

func validText(value string) bool {
	if value == "" || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func defaultContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func utcNow(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(timeFormat)
}

func parseTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	return time.Parse(timeFormat, value)
}
