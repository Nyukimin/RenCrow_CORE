package personrelatedcatalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	SummaryJobPending     SummaryJobState = "pending"
	SummaryJobLeased      SummaryJobState = "leased"
	SummaryJobReady       SummaryJobState = "ready"
	SummaryJobUnavailable SummaryJobState = "unavailable"
	SummaryJobDead        SummaryJobState = "dead"
)

// SummaryJobState is the durable state of one exact category/item summary
// target. A job is intentionally keyed by the immutable item identity rather
// than by a fuzzy title or a provider request.
type SummaryJobState string

// State aliases keep the wire/storage names readable for callers that prefer
// the explicit type prefix.
const (
	SummaryJobStatePending     = SummaryJobPending
	SummaryJobStateLeased      = SummaryJobLeased
	SummaryJobStateReady       = SummaryJobReady
	SummaryJobStateUnavailable = SummaryJobUnavailable
	SummaryJobStateDead        = SummaryJobDead
)

var ErrSummaryJobLeaseLost = errors.New("summary job lease is no longer owned")

// LeasedSummaryPatch binds an enrichment result to the exact queue lease that
// produced it. The token is never accepted from the external provider.
type LeasedSummaryPatch struct {
	Patch      SummaryPatch
	LeaseToken string
}

// ApplyLeasedSummaryPatches verifies all current leases and applies all
// summaries in one transaction. A stale worker therefore cannot overwrite a
// result after another worker has reclaimed the target.
func ApplyLeasedSummaryPatches(ctx context.Context, db *sql.DB, leased []LeasedSummaryPatch, now time.Time) error {
	if len(leased) == 0 {
		return fmt.Errorf("%w: leased summary patch is empty", ErrInvalidArtifact)
	}
	patches := make([]SummaryPatch, 0, len(leased))
	leases := make(map[string]string, len(leased))
	for _, item := range leased {
		patch := item.Patch
		normalizeSummaryPatch(&patch)
		key := patch.Category + "\x00" + patch.ItemID
		token := strings.TrimSpace(item.LeaseToken)
		if token == "" {
			return ErrSummaryJobLeaseLost
		}
		if existing := leases[key]; existing != "" && existing != token {
			return ErrSummaryJobLeaseLost
		}
		leases[key] = token
		patches = append(patches, patch)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return applySummaryPatches(ctx, db, patches, leases, now.UTC())
}

// SummaryJob is both the enqueue input and the durable queue projection.
// AvailableAt is the due time for pending/unavailable jobs; LeaseUntil and
// LeaseToken are populated only while a worker owns a lease.
type SummaryJob struct {
	Category       string
	ItemID         string
	Source         string
	SourceCursor   string
	SourceRecordID string
	CanonicalURL   string
	State          SummaryJobState
	Reason         string
	LastReason     string
	AvailableAt    time.Time
	NextAttemptAt  time.Time
	LeaseUntil     time.Time
	LeaseToken     string
	AttemptCount   int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// SummaryReadyTTL is the positive summary freshness period required by the
// person-related catalog contract. The variables are replaceable in tests and
// can be projected into a future bounded runtime configuration.
var (
	SummaryReadyTTL          = 30 * 24 * time.Hour
	SummaryUnavailableTTL    = 7 * 24 * time.Hour
	SummaryRightsRejectedTTL = 30 * 24 * time.Hour
)

func validSummaryJobState(state SummaryJobState) bool {
	switch state {
	case SummaryJobPending, SummaryJobLeased, SummaryJobReady, SummaryJobUnavailable, SummaryJobDead:
		return true
	default:
		return false
	}
}

func normalizeSummaryJob(job SummaryJob) (SummaryJob, error) {
	job.Category = strings.ToLower(strings.TrimSpace(job.Category))
	job.ItemID = strings.TrimSpace(job.ItemID)
	job.Source = strings.ToLower(strings.TrimSpace(job.Source))
	job.SourceCursor = strings.ToLower(strings.TrimSpace(job.SourceCursor))
	if job.Source == "" {
		job.Source = job.SourceCursor
	}
	if job.SourceCursor == "" {
		job.SourceCursor = job.Source
	}
	job.SourceRecordID = strings.TrimSpace(job.SourceRecordID)
	job.CanonicalURL = strings.TrimSpace(job.CanonicalURL)
	job.Reason = strings.TrimSpace(job.Reason)
	job.LastReason = strings.TrimSpace(job.LastReason)
	if job.Reason == "" {
		job.Reason = job.LastReason
	}
	if job.LastReason == "" {
		job.LastReason = job.Reason
	}
	if !validHobbyCategory(job.Category) || job.ItemID == "" {
		return SummaryJob{}, fmt.Errorf("%w: summary job category/item is invalid", ErrInvalidArtifact)
	}
	if job.Source != "" && !contractFreeSourceAllowed(job.Category, job.Source) {
		return SummaryJob{}, fmt.Errorf("%w: summary job source is invalid", ErrInvalidArtifact)
	}
	if job.State != "" && !validSummaryJobState(job.State) {
		return SummaryJob{}, fmt.Errorf("%w: summary job state %q is invalid", ErrInvalidArtifact, job.State)
	}
	if job.AvailableAt.IsZero() {
		job.AvailableAt = job.NextAttemptAt
	}
	if job.AvailableAt.IsZero() {
		job.AvailableAt = time.Now().UTC()
	} else {
		job.AvailableAt = job.AvailableAt.UTC()
	}
	if job.NextAttemptAt.IsZero() {
		job.NextAttemptAt = job.AvailableAt
	} else {
		job.NextAttemptAt = job.NextAttemptAt.UTC()
	}
	if !job.LeaseUntil.IsZero() {
		job.LeaseUntil = job.LeaseUntil.UTC()
	}
	return job, nil
}

// EnqueueSummaryJob inserts an exact missing-summary target. Repeated calls
// update provenance metadata but do not reset a ready, leased, unavailable, or
// dead state, making import retries idempotent.
func EnqueueSummaryJob(ctx context.Context, db *sql.DB, job SummaryJob) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := requireHobbySchema(ctx, db); err != nil {
		return err
	}
	job, err := normalizeSummaryJob(job)
	if err != nil {
		return err
	}
	if err := enqueueSummaryJob(ctx, db, job); err != nil {
		return fmt.Errorf("enqueue summary job: %w", err)
	}
	return nil
}

func enqueueSummaryJob(ctx context.Context, db *sql.DB, job SummaryJob) error {
	_, err := db.ExecContext(ctx, `
INSERT INTO hobby_summary_jobs(
  category,item_id,source_cursor,source_record_id,canonical_url,state,next_attempt_at,
  last_reason,
  lease_token,lease_until,attempt_count,created_at,updated_at
)
VALUES(?,?,?,?,?,'pending',?,?, '', '',0,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)
ON CONFLICT(category,item_id) DO UPDATE SET
  source_cursor=excluded.source_cursor,
  source_record_id=excluded.source_record_id,
  canonical_url=excluded.canonical_url,
  updated_at=CURRENT_TIMESTAMP`,
		job.Category, job.ItemID, job.SourceCursor, job.SourceRecordID, job.CanonicalURL,
		job.NextAttemptAt.Format(time.RFC3339), job.LastReason)
	return err
}

func enqueueSummaryJobTx(ctx context.Context, tx *sql.Tx, job SummaryJob) error {
	job, err := normalizeSummaryJob(job)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO hobby_summary_jobs(
  category,item_id,source_cursor,source_record_id,canonical_url,state,next_attempt_at,
  last_reason,
  lease_token,lease_until,attempt_count,created_at,updated_at
)
VALUES(?,?,?,?,?,'pending',?,?, '', '',0,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)
ON CONFLICT(category,item_id) DO UPDATE SET
  source_cursor=excluded.source_cursor,
  source_record_id=excluded.source_record_id,
  canonical_url=excluded.canonical_url,
  updated_at=CURRENT_TIMESTAMP`,
		job.Category, job.ItemID, job.SourceCursor, job.SourceRecordID, job.CanonicalURL,
		job.NextAttemptAt.Format(time.RFC3339), job.LastReason)
	return err
}

// ClaimDueSummaryJobs atomically leases at most twenty due jobs
// jobs. Expired leases are reclaimed by the same statement, so a crashed
// worker cannot permanently hide a target.
func ClaimDueSummaryJobs(ctx context.Context, db *sql.DB, workerID string, now time.Time, limit int, lease time.Duration) ([]SummaryJob, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	workerID = strings.TrimSpace(workerID)
	if workerID == "" {
		return nil, fmt.Errorf("%w: summary worker id is required", ErrInvalidArtifact)
	}
	if limit < 1 || limit > 20 {
		return nil, ErrInvalidLimit
	}
	if lease <= 0 {
		lease = time.Minute
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	if err := requireHobbySchema(ctx, db); err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin summary job claim: %w", err)
	}
	rollback := true
	defer func() {
		if rollback {
			_ = tx.Rollback()
		}
	}()
	leaseToken := fmt.Sprintf("%s-%d", workerID, now.UnixNano())
	leaseUntil := now.Add(lease).UTC()
	rows, err := tx.QueryContext(ctx, `
UPDATE hobby_summary_jobs
SET state='leased', lease_token=?, lease_until=?, attempt_count=attempt_count+1, updated_at=CURRENT_TIMESTAMP
WHERE rowid IN (
  SELECT rowid
  FROM hobby_summary_jobs INDEXED BY idx_hobby_summary_jobs_due
	WHERE ((state IN ('pending','ready','unavailable','dead') AND next_attempt_at<=?)
	      OR (state='leased' AND lease_until<=? AND next_attempt_at<=?))
  ORDER BY next_attempt_at, category, item_id
  LIMIT ?
)
	RETURNING category,item_id,source_cursor,source_record_id,canonical_url,state,next_attempt_at,lease_until,lease_token,attempt_count,last_reason,created_at,updated_at`,
		leaseToken, leaseUntil.Format(time.RFC3339), now.Format(time.RFC3339),
		now.Format(time.RFC3339), now.Format(time.RFC3339), limit)
	if err != nil {
		return nil, fmt.Errorf("claim due summary jobs: %w", err)
	}
	claimed := make([]SummaryJob, 0, limit)
	for rows.Next() {
		job, scanErr := scanSummaryJob(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, scanErr
		}
		claimed = append(claimed, job)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("read claimed summary jobs: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close claimed summary jobs: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit summary job claim: %w", err)
	}
	rollback = false
	return claimed, nil
}

// GetSummaryJob retrieves one exact queue target by its primary key.
func GetSummaryJob(ctx context.Context, db *sql.DB, category, itemID string) (SummaryJob, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := requireHobbySchema(ctx, db); err != nil {
		return SummaryJob{}, err
	}
	category = strings.ToLower(strings.TrimSpace(category))
	itemID = strings.TrimSpace(itemID)
	var row summaryJobRow
	err := db.QueryRowContext(ctx, `
SELECT category,item_id,source_cursor,source_record_id,canonical_url,state,next_attempt_at,
       lease_until,lease_token,attempt_count,last_reason,created_at,updated_at
FROM hobby_summary_jobs WHERE category=? AND item_id=?`, category, itemID).Scan(row.args()...)
	if errors.Is(err, sql.ErrNoRows) {
		return SummaryJob{}, fmt.Errorf("%w: summary job %s/%s does not exist", ErrUnavailable, category, itemID)
	}
	if err != nil {
		return SummaryJob{}, fmt.Errorf("get summary job: %w", err)
	}
	return row.value()
}

// CompleteSummaryJob transitions a leased exact target to ready, unavailable,
// or dead. Repeating an already-completed transition is idempotent.
func CompleteSummaryJob(ctx context.Context, db *sql.DB, category, itemID, leaseToken string, state SummaryJobState, reason string, now time.Time) (SummaryJob, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !validSummaryJobState(state) || state == SummaryJobPending || state == SummaryJobLeased {
		return SummaryJob{}, fmt.Errorf("%w: summary completion state %q is invalid", ErrInvalidArtifact, state)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	if err := requireHobbySchema(ctx, db); err != nil {
		return SummaryJob{}, err
	}
	category = strings.ToLower(strings.TrimSpace(category))
	itemID = strings.TrimSpace(itemID)
	leaseToken = strings.TrimSpace(leaseToken)
	reason = strings.TrimSpace(reason)
	nextAttemptAt := now.Add(summaryJobStateTTL(state, reason)).UTC()
	whereLease := ""
	args := []any{string(state), reason, nextAttemptAt.Format(time.RFC3339), now.Format(time.RFC3339), category, itemID}
	if leaseToken != "" {
		whereLease = " AND state='leased' AND lease_token=?"
		args = []any{string(state), reason, nextAttemptAt.Format(time.RFC3339), now.Format(time.RFC3339), category, itemID, leaseToken}
	}
	result, err := db.ExecContext(ctx, `
UPDATE hobby_summary_jobs
SET state=?, last_reason=?, next_attempt_at=?, lease_token='', lease_until='', updated_at=?
WHERE category=? AND item_id=?`+whereLease, args...)
	if err != nil {
		return SummaryJob{}, fmt.Errorf("complete summary job: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		current, getErr := GetSummaryJob(ctx, db, category, itemID)
		if getErr == nil && current.State == state && current.LastReason == reason && (leaseToken == "" || current.LeaseToken == "") {
			return current, nil
		}
		if getErr != nil {
			return SummaryJob{}, getErr
		}
		return SummaryJob{}, ErrSummaryJobLeaseLost
	}
	return GetSummaryJob(ctx, db, category, itemID)
}

func summaryJobStateTTL(state SummaryJobState, reason string) time.Duration {
	if state == SummaryJobReady {
		return SummaryReadyTTL
	}
	if state == SummaryJobDead || summaryReasonIsRightsRejected(reason) {
		return SummaryRightsRejectedTTL
	}
	return SummaryUnavailableTTL
}

// RetrySummaryJob returns a leased target to pending at the supplied due time.
// A repeated call after the first successful transition is idempotent when the
// same target already has the requested pending reason and due time.
func RetrySummaryJob(ctx context.Context, db *sql.DB, category, itemID, leaseToken string, availableAt time.Time, reason string) (SummaryJob, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if availableAt.IsZero() {
		availableAt = time.Now().UTC()
	} else {
		availableAt = availableAt.UTC()
	}
	if err := requireHobbySchema(ctx, db); err != nil {
		return SummaryJob{}, err
	}
	category = strings.ToLower(strings.TrimSpace(category))
	itemID = strings.TrimSpace(itemID)
	leaseToken = strings.TrimSpace(leaseToken)
	reason = strings.TrimSpace(reason)
	result, err := db.ExecContext(ctx, `
UPDATE hobby_summary_jobs
SET state='pending', last_reason=?, next_attempt_at=?, lease_token='', lease_until='', updated_at=CURRENT_TIMESTAMP
WHERE category=? AND item_id=? AND state='leased' AND lease_token=?`,
		reason, availableAt.Format(time.RFC3339), category, itemID, leaseToken)
	if err != nil {
		return SummaryJob{}, fmt.Errorf("retry summary job: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		current, getErr := GetSummaryJob(ctx, db, category, itemID)
		if getErr == nil && current.State == SummaryJobPending && current.LastReason == reason && current.NextAttemptAt.Equal(availableAt) {
			return current, nil
		}
		if getErr != nil {
			return SummaryJob{}, getErr
		}
		return SummaryJob{}, ErrSummaryJobLeaseLost
	}
	return GetSummaryJob(ctx, db, category, itemID)
}

func syncSummaryJobTx(ctx context.Context, tx *sql.Tx, patch SummaryPatch) error {
	state := SummaryJobUnavailable
	if patch.SourceStatus == SummarySourceReady {
		state = SummaryJobReady
	} else if summaryReasonIsRightsRejected(patch.Reason) {
		state = SummaryJobDead
	}
	expiresAt, err := summaryPatchExpiry(patch)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO hobby_summary_jobs(
  category,item_id,source_cursor,source_record_id,canonical_url,state,next_attempt_at,
  last_reason,lease_token,lease_until,attempt_count,created_at,updated_at
)
VALUES(?,?,?,?,?,?,?,?,'','',0,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)
ON CONFLICT(category,item_id) DO NOTHING`,
		patch.Category, patch.ItemID, patch.Source, patch.SourceRecordID, patch.CanonicalURL,
		string(state), expiresAt.Format(time.RFC3339), patch.Reason); err != nil {
		return fmt.Errorf("ensure summary job: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
UPDATE hobby_summary_jobs
SET state=?, source_cursor=?, source_record_id=?, canonical_url=?, last_reason=?, next_attempt_at=?,
    lease_token='', lease_until='', updated_at=CURRENT_TIMESTAMP
WHERE category=? AND item_id=?`,
		string(state), patch.Source, patch.SourceRecordID, patch.CanonicalURL, patch.Reason,
		expiresAt.Format(time.RFC3339), patch.Category, patch.ItemID)
	if err != nil {
		return fmt.Errorf("sync summary job: %w", err)
	}
	return nil
}

func summaryReasonIsRightsRejected(reason string) bool {
	reason = strings.ToLower(strings.TrimSpace(reason))
	return strings.Contains(reason, "rights")
}

func summaryPatchExpiry(patch SummaryPatch) (time.Time, error) {
	if strings.TrimSpace(patch.ExpiresAt) != "" {
		expiresAt, err := time.Parse(time.RFC3339, patch.ExpiresAt)
		if err != nil {
			return time.Time{}, fmt.Errorf("%w: summary expires_at must be RFC3339", ErrInvalidArtifact)
		}
		return expiresAt.UTC(), nil
	}
	retrievedAt, err := time.Parse(time.RFC3339, patch.RetrievedAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: summary retrieved_at must be RFC3339", ErrInvalidArtifact)
	}
	return retrievedAt.Add(summaryPatchTTL(patch)).UTC(), nil
}

func summaryPatchTTL(patch SummaryPatch) time.Duration {
	if summaryReasonIsRightsRejected(patch.Reason) {
		return SummaryRightsRejectedTTL
	}
	if patch.SourceStatus == SummarySourceReady {
		return SummaryReadyTTL
	}
	return SummaryUnavailableTTL
}

type summaryJobRow struct {
	category, itemID, sourceCursor, sourceRecordID, canonicalURL string
	state, nextAttemptAt, leaseUntil, leaseToken                 string
	attemptCount                                                 int
	lastReason, createdAt, updatedAt                             string
}

func (row *summaryJobRow) args() []any {
	return []any{
		&row.category, &row.itemID, &row.sourceCursor, &row.sourceRecordID, &row.canonicalURL,
		&row.state, &row.nextAttemptAt, &row.leaseUntil, &row.leaseToken,
		&row.attemptCount, &row.lastReason, &row.createdAt, &row.updatedAt,
	}
}

func (row summaryJobRow) value() (SummaryJob, error) {
	nextAttemptAt, err := parseSummaryJobTime(row.nextAttemptAt)
	if err != nil {
		return SummaryJob{}, err
	}
	leaseUntil, err := parseSummaryJobTime(row.leaseUntil)
	if err != nil {
		return SummaryJob{}, err
	}
	createdAt, err := parseSummaryJobTime(row.createdAt)
	if err != nil {
		return SummaryJob{}, err
	}
	updatedAt, err := parseSummaryJobTime(row.updatedAt)
	if err != nil {
		return SummaryJob{}, err
	}
	return SummaryJob{
		Category: row.category, ItemID: row.itemID, Source: row.sourceCursor, SourceCursor: row.sourceCursor,
		SourceRecordID: row.sourceRecordID, CanonicalURL: row.canonicalURL,
		State: SummaryJobState(row.state), Reason: row.lastReason, LastReason: row.lastReason,
		AvailableAt: nextAttemptAt, NextAttemptAt: nextAttemptAt,
		LeaseUntil: leaseUntil, LeaseToken: row.leaseToken,
		AttemptCount: row.attemptCount, CreatedAt: createdAt, UpdatedAt: updatedAt,
	}, nil
}

func scanSummaryJob(rows *sql.Rows) (SummaryJob, error) {
	var row summaryJobRow
	if err := rows.Scan(row.args()...); err != nil {
		return SummaryJob{}, fmt.Errorf("scan summary job: %w", err)
	}
	return row.value()
}

func parseSummaryJobTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02 15:04:05.999999999"} {
		if parsed, err := time.ParseInLocation(layout, value, time.UTC); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("%w: summary job timestamp %q is invalid", ErrInvalidArtifact, value)
}
