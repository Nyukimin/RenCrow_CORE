package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	personrelatedcatalog "github.com/Nyukimin/RenCrow_CORE/internal/application/personrelatedcatalog"
)

const (
	runtimeIdentityWorkerInterval = 5 * time.Minute
	runtimeIdentityWorkerLease    = 2 * time.Minute
	runtimeIdentityWorkerBatch    = 20
	runtimeIdentityWorkerAttempts = 3
)

type runtimePersonRelatedIdentityWorker struct {
	movieCatalogPath string
	hobbyGraphPath   string
	resolver         personrelatedcatalog.IdentityResolver
	interval         time.Duration
	lease            time.Duration
	batchSize        int
	maxAttempts      int
	workerID         string
	now              func() time.Time
}

type runtimePersonRelatedIdentityRunResult struct {
	MigrationQueued int
	Claimed         int
	Confirmed       int
	Ambiguous       int
	Unresolved      int
	Retried         int
	Dead            int
}

type personRelatedIdentityRunner interface {
	RunOnce(context.Context) (runtimePersonRelatedIdentityRunResult, error)
	Interval() time.Duration
}

func (w *runtimePersonRelatedIdentityWorker) Interval() time.Duration {
	if w == nil || w.interval <= 0 {
		return runtimeIdentityWorkerInterval
	}
	return w.interval
}

func startRuntimePersonRelatedIdentityWorker(runner personRelatedIdentityRunner, reporter backgroundJobFailureReporter) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	if runner == nil {
		return cancel
	}
	go func() {
		run := func() {
			if _, err := runner.RunOnce(ctx); err != nil && ctx.Err() == nil {
				reporter.Failed("person_related_identity", err, "authority identity resolution failed")
			}
		}
		run()
		ticker := time.NewTicker(runner.Interval())
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
	return cancel
}

func prepareRuntimePersonRelatedIdentityWorker(movieCatalogPath, hobbyGraphPath string, resolver personrelatedcatalog.IdentityResolver, cfg config.PersonRelatedCatalogIdentityMappingConfig, now func() time.Time) (*runtimePersonRelatedIdentityWorker, error) {
	if resolver == nil {
		return nil, fmt.Errorf("person related identity resolver is unavailable")
	}
	moviePath, err := resolveRuntimePersonRelatedCatalogDatabasePath(movieCatalogPath, "movie catalog")
	if err != nil {
		return nil, err
	}
	hobbyPath, err := resolveRuntimePersonRelatedCatalogWritableDatabasePath(hobbyGraphPath, "hobby graph")
	if err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	intervalText := strings.TrimSpace(cfg.Interval)
	if intervalText == "" {
		intervalText = runtimeIdentityWorkerInterval.String()
	}
	interval, err := time.ParseDuration(intervalText)
	if err != nil || interval <= 0 {
		return nil, fmt.Errorf("person related identity interval is invalid")
	}
	batchSize := cfg.BatchSize
	if batchSize == 0 {
		batchSize = runtimeIdentityWorkerBatch
	}
	if batchSize < 1 || batchSize > 20 {
		return nil, fmt.Errorf("person related identity batch size is invalid")
	}
	leaseText := strings.TrimSpace(cfg.Lease)
	if leaseText == "" {
		leaseText = runtimeIdentityWorkerLease.String()
	}
	lease, err := time.ParseDuration(leaseText)
	if err != nil || lease < 30*time.Second || lease > 10*time.Minute {
		return nil, fmt.Errorf("person related identity lease is invalid")
	}
	maxAttempts := cfg.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = runtimeIdentityWorkerAttempts
	}
	if maxAttempts < 1 || maxAttempts > 3 {
		return nil, fmt.Errorf("person related identity max attempts is invalid")
	}
	batchCategories := cfg.BatchCategories
	if batchCategories == 0 {
		batchCategories = 7
	}
	if batchCategories < 1 || batchCategories > 7 {
		return nil, fmt.Errorf("person related identity batch categories are invalid")
	}
	worker := &runtimePersonRelatedIdentityWorker{
		movieCatalogPath: moviePath, hobbyGraphPath: hobbyPath, resolver: resolver,
		interval: interval, lease: lease,
		batchSize: batchSize, maxAttempts: maxAttempts,
		workerID: fmt.Sprintf("rencrow-core-identity-%d", now().UTC().UnixNano()), now: now,
	}
	db, err := openRuntimePersonRelatedCatalogReadWrite(hobbyPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := personrelatedcatalog.EnsureHobbySchema(context.Background(), db); err != nil {
		return nil, fmt.Errorf("prepare person related identity schema: %w", err)
	}
	movieDB, err := openRuntimePersonRelatedCatalogReadWrite(moviePath)
	if err != nil {
		return nil, fmt.Errorf("open movie catalog for identity schema: %w", err)
	}
	defer movieDB.Close()
	if err := personrelatedcatalog.EnsureSchema(context.Background(), movieDB, db); err != nil {
		return nil, fmt.Errorf("prepare person related identity assessment index: %w", err)
	}
	return worker, nil
}

func (w *runtimePersonRelatedIdentityWorker) RunOnce(ctx context.Context) (runtimePersonRelatedIdentityRunResult, error) {
	var result runtimePersonRelatedIdentityRunResult
	if w == nil || w.resolver == nil {
		return result, fmt.Errorf("person related identity worker is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := w.now().UTC()
	movieDB, err := openRuntimePersonRelatedCatalogReadOnly(w.movieCatalogPath)
	if err != nil {
		return result, fmt.Errorf("open movie catalog for identity worker: %w", err)
	}
	defer movieDB.Close()
	movieDB.SetMaxOpenConns(1)
	hobbyDB, err := openRuntimePersonRelatedCatalogReadWrite(w.hobbyGraphPath)
	if err != nil {
		return result, fmt.Errorf("open hobby graph for identity worker: %w", err)
	}
	defer hobbyDB.Close()
	hobbyDB.SetMaxOpenConns(1)
	if err := personrelatedcatalog.EnsureHobbySchema(ctx, hobbyDB); err != nil {
		return result, err
	}
	_, migrationDone, err := personrelatedcatalog.GetIdentityMigrationState(ctx, hobbyDB)
	if err != nil {
		return result, err
	}
	if !migrationDone {
		migration, migrationErr := personrelatedcatalog.EnqueueInitialIdentityJobs(ctx, movieDB, hobbyDB, "", 200, now)
		if migrationErr != nil {
			return result, fmt.Errorf("enqueue initial identity jobs: %w", migrationErr)
		}
		result.MigrationQueued = migration.Queued
	}
	jobs, err := personrelatedcatalog.ClaimDueIdentityJobs(ctx, hobbyDB, w.workerID, now, w.batchSize, w.lease)
	if err != nil {
		return result, err
	}
	result.Claimed = len(jobs)
	var firstErr error
	for _, job := range jobs {
		confirmedIDs, confirmedErr := personrelatedcatalog.ConfirmedIdentityIDs(ctx, hobbyDB, job.MovieCatalogPersonID, 20)
		if confirmedErr != nil {
			if err := w.handleIdentityError(ctx, hobbyDB, job, confirmedErr, now, &result); err != nil && firstErr == nil {
				firstErr = err
			}
			continue
		}
		resolved, resolveErr := w.resolver.ResolveIdentity(ctx, personrelatedcatalog.IdentityResolveRequest{
			RunID:                fmt.Sprintf("identity-%d-%s", now.UnixNano(), job.MovieCatalogPersonID),
			MovieCatalogPersonID: job.MovieCatalogPersonID, PersonName: job.PersonName,
			PublicPersonURL: job.PersonURL, ConfirmedExternalIDs: confirmedIDs,
		})
		if resolveErr != nil {
			if err := w.handleIdentityError(ctx, hobbyDB, job, resolveErr, now, &result); err != nil && firstErr == nil {
				firstErr = err
			}
			continue
		}
		completed, applyErr := personrelatedcatalog.ApplyIdentityJobResolution(ctx, hobbyDB, job, resolved, now)
		if applyErr != nil {
			if err := w.handleIdentityError(ctx, hobbyDB, job, applyErr, now, &result); err != nil && firstErr == nil {
				firstErr = err
			}
			continue
		}
		switch completed.State {
		case personrelatedcatalog.IdentityJobConfirmed:
			result.Confirmed++
		case personrelatedcatalog.IdentityJobAmbiguous:
			result.Ambiguous++
		default:
			result.Unresolved++
		}
	}
	return result, firstErr
}

func (w *runtimePersonRelatedIdentityWorker) handleIdentityError(ctx context.Context, db *sql.DB, job personrelatedcatalog.IdentityJob, err error, now time.Time, result *runtimePersonRelatedIdentityRunResult) error {
	var resolverErr *personrelatedcatalog.IdentityResolveError
	if errors.As(err, &resolverErr) && resolverErr.Retryable && job.AttemptCount < w.maxAttempts {
		delay := resolverErr.RetryAfter
		if delay <= 0 {
			delay = time.Duration(job.AttemptCount) * time.Minute
		}
		if retryErr := personrelatedcatalog.RetryIdentityJob(ctx, db, job, now.Add(delay), compactIdentityWorkerReason(err)); retryErr != nil {
			return retryErr
		}
		result.Retried++
		return err
	}
	state := personrelatedcatalog.IdentityJobDead
	if errors.As(err, &resolverErr) && resolverErr.Retryable {
		state = personrelatedcatalog.IdentityJobDead
	}
	if _, completeErr := personrelatedcatalog.CompleteIdentityJob(ctx, db, job.MovieCatalogPersonID, job.LeaseToken, state, compactIdentityWorkerReason(err), now); completeErr != nil {
		return completeErr
	}
	result.Dead++
	return err
}

func compactIdentityWorkerReason(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	if len(text) > 160 {
		text = text[:160]
	}
	return text
}
