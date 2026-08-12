package personrelatedcatalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// IdentityJobState is the durable lifecycle of one authority-resolution
// target. The terminal-looking states are intentionally due again after their
// bounded TTL so a later request can re-evaluate new public evidence.
type IdentityJobState string

const (
	IdentityJobPending    IdentityJobState = "pending"
	IdentityJobLeased     IdentityJobState = "leased"
	IdentityJobConfirmed  IdentityJobState = "confirmed"
	IdentityJobAmbiguous  IdentityJobState = "ambiguous"
	IdentityJobUnresolved IdentityJobState = "unresolved"
	IdentityJobDead       IdentityJobState = "dead"
)

var (
	IdentityConfirmedTTL  = 90 * 24 * time.Hour
	IdentityUnresolvedTTL = 7 * 24 * time.Hour
)

var ErrIdentityJobLeaseLost = errors.New("identity job lease is no longer owned")

// IdentityJob is the durable queue projection. It is keyed by the immutable
// movie catalog person ID and never by a fuzzy name.
type IdentityJob struct {
	MovieCatalogPersonID string
	PersonName           string
	PersonURL            string
	State                IdentityJobState
	NextAttemptAt        time.Time
	LeaseUntil           time.Time
	LeaseToken           string
	AttemptCount         int
	LastReason           string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type IdentityMigrationResult struct {
	Cursor string
	Queued int
	Done   bool
}

func validIdentityJobState(state IdentityJobState) bool {
	switch state {
	case IdentityJobPending, IdentityJobLeased, IdentityJobConfirmed, IdentityJobAmbiguous, IdentityJobUnresolved, IdentityJobDead:
		return true
	default:
		return false
	}
}

func normalizeIdentityJob(job IdentityJob) (IdentityJob, error) {
	job.MovieCatalogPersonID = strings.TrimSpace(job.MovieCatalogPersonID)
	job.PersonName = strings.TrimSpace(job.PersonName)
	job.PersonURL = strings.TrimSpace(job.PersonURL)
	job.LastReason = strings.TrimSpace(job.LastReason)
	if job.MovieCatalogPersonID == "" || job.PersonName == "" {
		return IdentityJob{}, fmt.Errorf("%w: identity job person fields are required", ErrInvalidArtifact)
	}
	if job.PersonURL != "" && !validHTTPURL(job.PersonURL) {
		return IdentityJob{}, fmt.Errorf("%w: identity job person_url is invalid", ErrInvalidArtifact)
	}
	if job.State == "" {
		job.State = IdentityJobPending
	}
	if !validIdentityJobState(job.State) {
		return IdentityJob{}, fmt.Errorf("%w: identity job state %q is invalid", ErrInvalidArtifact, job.State)
	}
	if job.NextAttemptAt.IsZero() {
		job.NextAttemptAt = time.Now().UTC()
	} else {
		job.NextAttemptAt = job.NextAttemptAt.UTC()
	}
	if !job.LeaseUntil.IsZero() {
		job.LeaseUntil = job.LeaseUntil.UTC()
	}
	return job, nil
}

// IdentityJobNextAttempt maps a resolved state to its bounded re-evaluation
// TTL. It does not introduce a human-gated waiting state.
func IdentityJobNextAttempt(state IdentityJobState, now time.Time) time.Time {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	if state == IdentityJobConfirmed {
		return now.Add(IdentityConfirmedTTL)
	}
	return now.Add(IdentityUnresolvedTTL)
}

// EnqueueIdentityJob creates or refreshes immutable person metadata without
// resetting an existing state or active lease. Repeated assessment events are
// therefore safe and idempotent.
func EnqueueIdentityJob(ctx context.Context, db *sql.DB, job IdentityJob) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := requireHobbySchema(ctx, db); err != nil {
		return err
	}
	job, err := normalizeIdentityJob(job)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
INSERT INTO hobby_person_identity_jobs(
  movie_catalog_person_id,person_name,person_url,state,next_attempt_at,
  lease_token,lease_until,attempt_count,last_reason,created_at,updated_at
)
VALUES(?,?,?,'pending',?,'','',0,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)
ON CONFLICT(movie_catalog_person_id) DO UPDATE SET
  person_name=excluded.person_name,
  person_url=excluded.person_url,
  updated_at=CURRENT_TIMESTAMP`,
		job.MovieCatalogPersonID, job.PersonName, job.PersonURL,
		job.NextAttemptAt.Format(time.RFC3339), job.LastReason)
	if err != nil {
		return fmt.Errorf("enqueue identity job: %w", err)
	}
	return nil
}

// GetIdentityJob returns one exact queue row.
func GetIdentityJob(ctx context.Context, db *sql.DB, movieCatalogPersonID string) (IdentityJob, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := requireHobbySchema(ctx, db); err != nil {
		return IdentityJob{}, err
	}
	personID := strings.TrimSpace(movieCatalogPersonID)
	if personID == "" {
		return IdentityJob{}, fmt.Errorf("%w: identity job person id is required", ErrInvalidArtifact)
	}
	var job IdentityJob
	var state, nextAttempt, leaseUntil, createdAt, updatedAt string
	err := db.QueryRowContext(ctx, `
SELECT movie_catalog_person_id,person_name,person_url,state,next_attempt_at,lease_token,lease_until,attempt_count,last_reason,created_at,updated_at
FROM hobby_person_identity_jobs
WHERE movie_catalog_person_id=?`, personID).Scan(
		&job.MovieCatalogPersonID, &job.PersonName, &job.PersonURL, &state, &nextAttempt,
		&job.LeaseToken, &leaseUntil, &job.AttemptCount, &job.LastReason, &createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return IdentityJob{}, fmt.Errorf("%w: identity job %s does not exist", ErrUnavailable, personID)
		}
		return IdentityJob{}, fmt.Errorf("get identity job: %w", err)
	}
	job.State = IdentityJobState(state)
	var parseErr error
	job.NextAttemptAt, parseErr = parseIdentityJobTime(nextAttempt)
	if parseErr != nil {
		return IdentityJob{}, parseErr
	}
	job.LeaseUntil, parseErr = parseOptionalIdentityJobTime(leaseUntil)
	if parseErr != nil {
		return IdentityJob{}, parseErr
	}
	job.CreatedAt, parseErr = parseOptionalIdentityJobTime(createdAt)
	if parseErr != nil {
		return IdentityJob{}, parseErr
	}
	job.UpdatedAt, parseErr = parseOptionalIdentityJobTime(updatedAt)
	if parseErr != nil {
		return IdentityJob{}, parseErr
	}
	return job, nil
}

// ClaimDueIdentityJobs atomically leases at most twenty due targets. Expired
// leases are reclaimed by the same indexed statement after a worker crash.
func ClaimDueIdentityJobs(ctx context.Context, db *sql.DB, workerID string, now time.Time, limit int, lease time.Duration) ([]IdentityJob, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	workerID = strings.TrimSpace(workerID)
	if workerID == "" {
		return nil, fmt.Errorf("%w: identity worker id is required", ErrInvalidArtifact)
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
		return nil, fmt.Errorf("begin identity job claim: %w", err)
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
UPDATE hobby_person_identity_jobs
SET state='leased',lease_token=?,lease_until=?,attempt_count=attempt_count+1,updated_at=CURRENT_TIMESTAMP
WHERE rowid IN (
  SELECT rowid FROM hobby_person_identity_jobs INDEXED BY idx_hobby_person_identity_jobs_due
  WHERE ((state IN ('pending','confirmed','ambiguous','unresolved') AND next_attempt_at<=?)
      OR (state='leased' AND lease_until<=? AND next_attempt_at<=?))
  ORDER BY next_attempt_at,movie_catalog_person_id
  LIMIT ?
)
RETURNING movie_catalog_person_id,person_name,person_url,state,next_attempt_at,lease_token,lease_until,attempt_count,last_reason,created_at,updated_at`,
		leaseToken, leaseUntil.Format(time.RFC3339), now.Format(time.RFC3339), now.Format(time.RFC3339), now.Format(time.RFC3339), limit)
	if err != nil {
		return nil, fmt.Errorf("claim due identity jobs: %w", err)
	}
	claimed := make([]IdentityJob, 0, limit)
	for rows.Next() {
		job, scanErr := scanIdentityJob(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, scanErr
		}
		claimed = append(claimed, job)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("read claimed identity jobs: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close claimed identity jobs: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit identity job claim: %w", err)
	}
	rollback = false
	return claimed, nil
}

// CompleteIdentityJob transitions an owned lease to one of the durable
// resolution states and clears its lease in the same update.
func CompleteIdentityJob(ctx context.Context, db *sql.DB, personID, leaseToken string, state IdentityJobState, reason string, nextAttemptAt time.Time) (IdentityJob, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if state != IdentityJobConfirmed && state != IdentityJobAmbiguous && state != IdentityJobUnresolved && state != IdentityJobDead {
		return IdentityJob{}, fmt.Errorf("%w: identity completion state %q is invalid", ErrInvalidArtifact, state)
	}
	personID = strings.TrimSpace(personID)
	leaseToken = strings.TrimSpace(leaseToken)
	if personID == "" || leaseToken == "" {
		return IdentityJob{}, ErrIdentityJobLeaseLost
	}
	if nextAttemptAt.IsZero() {
		nextAttemptAt = IdentityJobNextAttempt(state, time.Now().UTC())
	}
	if err := requireHobbySchema(ctx, db); err != nil {
		return IdentityJob{}, err
	}
	result, err := db.ExecContext(ctx, `
UPDATE hobby_person_identity_jobs
SET state=?,next_attempt_at=?,lease_token='',lease_until='',last_reason=?,updated_at=CURRENT_TIMESTAMP
WHERE movie_catalog_person_id=? AND state='leased' AND lease_token=?`,
		state, nextAttemptAt.UTC().Format(time.RFC3339), strings.TrimSpace(reason), personID, leaseToken)
	if err != nil {
		return IdentityJob{}, fmt.Errorf("complete identity job: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return IdentityJob{}, ErrIdentityJobLeaseLost
	}
	return GetIdentityJob(ctx, db, personID)
}

func RetryIdentityJob(ctx context.Context, db *sql.DB, job IdentityJob, nextAttemptAt time.Time, reason string) error {
	if job.AttemptCount < 1 || strings.TrimSpace(job.LeaseToken) == "" {
		return ErrIdentityJobLeaseLost
	}
	if nextAttemptAt.IsZero() {
		nextAttemptAt = time.Now().UTC().Add(time.Minute)
	}
	_, err := db.ExecContext(ctx, `
UPDATE hobby_person_identity_jobs
SET state='pending',next_attempt_at=?,lease_token='',lease_until='',last_reason=?,updated_at=CURRENT_TIMESTAMP
WHERE movie_catalog_person_id=? AND state='leased' AND lease_token=?`,
		nextAttemptAt.UTC().Format(time.RFC3339), strings.TrimSpace(reason), job.MovieCatalogPersonID, job.LeaseToken)
	if err != nil {
		return fmt.Errorf("retry identity job: %w", err)
	}
	return nil
}

// ApplyIdentityJobResolution persists all provider evidence and the queue
// state in one hobby-graph transaction. A provider's status is advisory: the
// existing CORE identity state machine is re-run before the job is completed.
func ApplyIdentityJobResolution(ctx context.Context, db *sql.DB, job IdentityJob, result IdentityResolveResult, now time.Time) (IdentityJob, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if job.MovieCatalogPersonID == "" || job.LeaseToken == "" {
		return IdentityJob{}, ErrIdentityJobLeaseLost
	}
	if result.Status != IdentityStatusConfirmed && result.Status != IdentityStatusAmbiguous && result.Status != IdentityStatusUnresolved {
		return IdentityJob{}, fmt.Errorf("%w: identity provider status %q is invalid", ErrCollectorProtocol, result.Status)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	if err := requireHobbySchema(ctx, db); err != nil {
		return IdentityJob{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return IdentityJob{}, fmt.Errorf("begin identity resolution apply: %w", err)
	}
	rollback := true
	defer func() {
		if rollback {
			_ = tx.Rollback()
		}
	}()
	for _, candidate := range result.Candidates {
		candidate.PersonID = strings.TrimSpace(candidate.PersonID)
		if candidate.PersonID == "" {
			candidate.PersonID = job.MovieCatalogPersonID
		}
		if candidate.RetrievedAt == "" {
			candidate.RetrievedAt = result.RetrievedAt
		}
		if candidate.RetrievedAt == "" {
			candidate.RetrievedAt = now.Format(time.RFC3339)
		}
		candidate.State = result.Status
		normalized, normalizeErr := normalizeIdentityEvidence(candidate)
		if normalizeErr != nil {
			return IdentityJob{}, normalizeErr
		}
		if _, applyErr := upsertIdentityEvidenceTx(ctx, tx, normalized); applyErr != nil {
			return IdentityJob{}, applyErr
		}
	}
	resolution, err := resolvePersonIdentityTx(ctx, tx, job.MovieCatalogPersonID)
	if err != nil {
		return IdentityJob{}, err
	}
	jobState := IdentityJobUnresolved
	switch result.Status {
	case IdentityStatusAmbiguous:
		jobState = IdentityJobAmbiguous
	case IdentityStatusConfirmed:
		if resolution.Status == IdentityStatusConfirmed {
			jobState = IdentityJobConfirmed
		} else if resolution.Status == IdentityStatusAmbiguous {
			jobState = IdentityJobAmbiguous
		}
	}
	if jobState == IdentityJobConfirmed && len(resolution.Mappings) == 0 {
		jobState = IdentityJobUnresolved
	}
	nextAttemptAt := IdentityJobNextAttempt(jobState, now)
	if jobState == IdentityJobDead {
		nextAttemptAt = now
	}
	update, err := tx.ExecContext(ctx, `
UPDATE hobby_person_identity_jobs
SET state=?,next_attempt_at=?,lease_token='',lease_until='',last_reason=?,updated_at=CURRENT_TIMESTAMP
WHERE movie_catalog_person_id=? AND state='leased' AND lease_token=?`,
		jobState, nextAttemptAt.Format(time.RFC3339), strings.TrimSpace(result.ReasonCode), job.MovieCatalogPersonID, job.LeaseToken)
	if err != nil {
		return IdentityJob{}, fmt.Errorf("complete identity resolution job: %w", err)
	}
	if affected, _ := update.RowsAffected(); affected != 1 {
		return IdentityJob{}, ErrIdentityJobLeaseLost
	}
	if err := tx.Commit(); err != nil {
		return IdentityJob{}, fmt.Errorf("commit identity resolution apply: %w", err)
	}
	rollback = false
	return GetIdentityJob(ctx, db, job.MovieCatalogPersonID)
}

// EnsureIdentityJobForPerson is the event-side enqueue boundary. It performs
// one exact assessment lookup and only queues a person when no recent identity
// decision exists. It never enumerates the eligible population.
func EnsureIdentityJobForPerson(ctx context.Context, movieDB, hobbyDB *sql.DB, movieCatalogPersonID string, now time.Time) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	person, found, err := EligiblePersonByID(ctx, movieDB, movieCatalogPersonID)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	resolution, err := ResolvePersonIdentity(ctx, hobbyDB, person.MovieCatalogPersonID)
	if err != nil {
		return false, err
	}
	if identityResolutionIsFresh(resolution, now) {
		return false, nil
	}
	reason := "identity_missing"
	if resolution.Status == IdentityStatusConfirmed {
		reason = "identity_ttl_expired"
	}
	if err := EnqueueIdentityJob(ctx, hobbyDB, IdentityJob{
		MovieCatalogPersonID: person.MovieCatalogPersonID,
		PersonName:           person.Name,
		PersonURL:            person.URL,
		NextAttemptAt:        now,
		LastReason:           reason,
	}); err != nil {
		return false, err
	}
	return true, nil
}

func identityResolutionIsFresh(resolution IdentityResolution, now time.Time) bool {
	if resolution.Status != IdentityStatusConfirmed && resolution.Status != IdentityStatusAmbiguous && resolution.Status != IdentityStatusUnresolved {
		return false
	}
	cutoff := now.Add(-IdentityUnresolvedTTL)
	if resolution.Status == IdentityStatusConfirmed {
		cutoff = now.Add(-IdentityConfirmedTTL)
	}
	fresh := false
	for _, mapping := range resolution.Mappings {
		if parsed, err := time.Parse(time.RFC3339, mapping.RetrievedAt); err == nil && !parsed.Before(cutoff) {
			fresh = true
		}
	}
	for _, evidence := range resolution.Candidates {
		if parsed, err := time.Parse(time.RFC3339, evidence.RetrievedAt); err == nil && !parsed.Before(cutoff) {
			fresh = true
		}
	}
	return fresh
}

// EnqueueInitialIdentityJobs imports at most limit assessment rows after the
// supplied cursor. The caller persists the returned cursor; duplicate work
// after a crash is harmless because queue insertion is idempotent.
func EnqueueInitialIdentityJobs(ctx context.Context, movieDB, hobbyDB *sql.DB, cursor string, limit int, now time.Time) (IdentityMigrationResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if limit < 1 || limit > 200 {
		return IdentityMigrationResult{}, ErrInvalidLimit
	}
	if err := requireMovieSelectionSchema(ctx, movieDB); err != nil {
		return IdentityMigrationResult{}, err
	}
	if err := requireHobbySchema(ctx, hobbyDB); err != nil {
		return IdentityMigrationResult{}, err
	}
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		storedCursor, completed, stateErr := GetIdentityMigrationState(ctx, hobbyDB)
		if stateErr != nil {
			return IdentityMigrationResult{}, stateErr
		}
		if completed {
			return IdentityMigrationResult{Cursor: storedCursor, Done: true}, nil
		}
		cursor = storedCursor
	}
	rows, err := movieDB.QueryContext(ctx, `
SELECT p.person_id,p.name,p.url
FROM movie_catalog_assessments AS a INDEXED BY idx_movie_catalog_assessments_eligible_target
JOIN people AS p ON p.person_id=a.target_id
WHERE a.kind='person' AND (a.familiarity='known' OR a.sentiment='like') AND p.person_id>?
ORDER BY p.person_id
LIMIT ?`, cursor, limit)
	if err != nil {
		return IdentityMigrationResult{}, fmt.Errorf("select identity migration people: %w", err)
	}
	people := []EligiblePerson{}
	for rows.Next() {
		var person EligiblePerson
		if err := rows.Scan(&person.MovieCatalogPersonID, &person.Name, &person.URL); err != nil {
			_ = rows.Close()
			return IdentityMigrationResult{}, fmt.Errorf("scan identity migration person: %w", err)
		}
		people = append(people, person)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return IdentityMigrationResult{}, fmt.Errorf("read identity migration people: %w", err)
	}
	if err := rows.Close(); err != nil {
		return IdentityMigrationResult{}, fmt.Errorf("close identity migration people: %w", err)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	result := IdentityMigrationResult{Cursor: cursor, Done: len(people) < limit}
	for _, person := range people {
		if err := EnqueueIdentityJob(ctx, hobbyDB, IdentityJob{MovieCatalogPersonID: person.MovieCatalogPersonID, PersonName: person.Name, PersonURL: person.URL, NextAttemptAt: now, LastReason: "initial_identity_migration"}); err != nil {
			return IdentityMigrationResult{}, err
		}
		result.Queued++
		result.Cursor = person.MovieCatalogPersonID
	}
	if err := saveIdentityMigrationState(ctx, hobbyDB, result.Cursor, result.Done); err != nil {
		return IdentityMigrationResult{}, err
	}
	return result, nil
}

// GetIdentityMigrationState returns the durable cursor for the one bounded
// initial migration. A missing row means the migration has not started.
func GetIdentityMigrationState(ctx context.Context, db *sql.DB) (cursor string, completed bool, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := requireHobbySchema(ctx, db); err != nil {
		return "", false, err
	}
	var done int
	err = db.QueryRowContext(ctx, `SELECT cursor_person_id,completed FROM hobby_person_identity_migrations WHERE migration_name='eligible_people_v1'`).Scan(&cursor, &done)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read identity migration state: %w", err)
	}
	return strings.TrimSpace(cursor), done == 1, nil
}

func saveIdentityMigrationState(ctx context.Context, db *sql.DB, cursor string, done bool) error {
	completed := 0
	if done {
		completed = 1
	}
	_, err := db.ExecContext(ctx, `
INSERT INTO hobby_person_identity_migrations(migration_name,cursor_person_id,completed,updated_at)
VALUES('eligible_people_v1',?,?,CURRENT_TIMESTAMP)
ON CONFLICT(migration_name) DO UPDATE SET cursor_person_id=excluded.cursor_person_id,completed=excluded.completed,updated_at=CURRENT_TIMESTAMP`, cursor, completed)
	if err != nil {
		return fmt.Errorf("save identity migration state: %w", err)
	}
	return nil
}

func scanIdentityJob(scanner interface{ Scan(...any) error }) (IdentityJob, error) {
	var job IdentityJob
	var state, nextAttempt, leaseUntil, createdAt, updatedAt string
	if err := scanner.Scan(&job.MovieCatalogPersonID, &job.PersonName, &job.PersonURL, &state, &nextAttempt, &job.LeaseToken, &leaseUntil, &job.AttemptCount, &job.LastReason, &createdAt, &updatedAt); err != nil {
		return IdentityJob{}, fmt.Errorf("scan identity job: %w", err)
	}
	job.State = IdentityJobState(state)
	var err error
	if job.NextAttemptAt, err = parseIdentityJobTime(nextAttempt); err != nil {
		return IdentityJob{}, err
	}
	if job.LeaseUntil, err = parseOptionalIdentityJobTime(leaseUntil); err != nil {
		return IdentityJob{}, err
	}
	if job.CreatedAt, err = parseOptionalIdentityJobTime(createdAt); err != nil {
		return IdentityJob{}, err
	}
	if job.UpdatedAt, err = parseOptionalIdentityJobTime(updatedAt); err != nil {
		return IdentityJob{}, err
	}
	return job, nil
}

func parseIdentityJobTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02 15:04:05.999999999"} {
		if parsed, err := time.ParseInLocation(layout, value, time.UTC); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("%w: identity job timestamp %q is invalid", ErrInvalidArtifact, value)
}

func parseOptionalIdentityJobTime(value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, nil
	}
	return parseIdentityJobTime(value)
}
