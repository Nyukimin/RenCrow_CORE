package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	personrelatedcatalog "github.com/Nyukimin/RenCrow_CORE/internal/application/personrelatedcatalog"
)

type runtimePersonRelatedSummaryWorker struct {
	hobbyGraphPath string
	collector      personrelatedcatalog.SummaryCollector
	translator     personRelatedSummaryTranslator
	interval       time.Duration
	batchSize      int
	lease          time.Duration
	maxAttempts    int
	workerID       string
	now            func() time.Time
}

type runtimePersonRelatedSummaryRunResult struct {
	Claimed     int
	Ready       int
	Unavailable int
	Dead        int
}

type personRelatedSummaryRunner interface {
	RunOnce(context.Context) (runtimePersonRelatedSummaryRunResult, error)
	Interval() time.Duration
}

func (w *runtimePersonRelatedSummaryWorker) Interval() time.Duration {
	if w == nil || w.interval <= 0 {
		return 5 * time.Minute
	}
	return w.interval
}

func startRuntimePersonRelatedSummaryWorker(runner personRelatedSummaryRunner, reporter backgroundJobFailureReporter) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	if runner == nil {
		return cancel
	}
	go func() {
		run := func() {
			if _, err := runner.RunOnce(ctx); err != nil && ctx.Err() == nil {
				reporter.Failed("person_related_summary", err, "fixed-id summary enrichment failed")
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

func prepareRuntimePersonRelatedSummaryWorker(hobbyGraphPath string, collector personrelatedcatalog.SummaryCollector, cfg config.PersonRelatedCatalogSummaryWorkerConfig, now func() time.Time) (*runtimePersonRelatedSummaryWorker, error) {
	if collector == nil {
		return nil, fmt.Errorf("person related summary collector is unavailable")
	}
	path, err := resolveRuntimePersonRelatedCatalogWritableDatabasePath(hobbyGraphPath, "hobby graph")
	if err != nil {
		return nil, err
	}
	interval, err := time.ParseDuration(cfg.Interval)
	if err != nil || interval <= 0 {
		return nil, fmt.Errorf("person related summary interval is invalid")
	}
	lease, err := time.ParseDuration(cfg.Lease)
	if err != nil || lease < 30*time.Second || lease > 10*time.Minute {
		return nil, fmt.Errorf("person related summary lease is invalid")
	}
	if cfg.BatchSize < 1 || cfg.BatchSize > 20 || cfg.MaxAttempts < 1 || cfg.MaxAttempts > 3 {
		return nil, fmt.Errorf("person related summary worker bounds are invalid")
	}
	if now == nil {
		now = time.Now
	}
	worker := &runtimePersonRelatedSummaryWorker{
		hobbyGraphPath: path, collector: collector, interval: interval, batchSize: cfg.BatchSize,
		lease: lease, maxAttempts: cfg.MaxAttempts, workerID: fmt.Sprintf("rencrow-core-summary-%d", now().UTC().UnixNano()), now: now,
	}
	db, err := openRuntimePersonRelatedCatalogReadWrite(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := personrelatedcatalog.EnsureHobbySchema(context.Background(), db); err != nil {
		return nil, fmt.Errorf("prepare person related summary schema: %w", err)
	}
	return worker, nil
}

func (w *runtimePersonRelatedSummaryWorker) RunOnce(ctx context.Context) (runtimePersonRelatedSummaryRunResult, error) {
	var result runtimePersonRelatedSummaryRunResult
	if w == nil || w.collector == nil {
		return result, fmt.Errorf("person related summary worker is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := w.now().UTC()
	db, err := openRuntimePersonRelatedCatalogReadWrite(w.hobbyGraphPath)
	if err != nil {
		return result, fmt.Errorf("open hobby graph for summary worker: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	jobs, err := personrelatedcatalog.ClaimDueSummaryJobs(ctx, db, w.workerID, now, w.batchSize, w.lease)
	if err != nil {
		return result, err
	}
	result.Claimed = len(jobs)
	if len(jobs) == 0 {
		return result, nil
	}
	targets := make([]personrelatedcatalog.SummaryTarget, 0, len(jobs))
	for _, job := range jobs {
		targets = append(targets, personrelatedcatalog.SummaryTarget{
			Category: job.Category, ItemID: job.ItemID, Source: job.Source,
			SourceRecordID: job.SourceRecordID, CanonicalURL: job.CanonicalURL,
		})
	}
	requestID := fmt.Sprintf("summary-%d", now.UnixNano())
	collected, collectErr := w.collector.CollectSummaries(ctx, personrelatedcatalog.SummaryCollectionRequest{RequestID: requestID, Targets: targets})
	if collectErr != nil {
		for _, job := range jobs {
			if job.AttemptCount >= w.maxAttempts {
				if _, completeErr := personrelatedcatalog.CompleteSummaryJob(ctx, db, job.Category, job.ItemID, job.LeaseToken, personrelatedcatalog.SummaryJobDead, "attempts_exhausted", now.Add(personrelatedcatalog.SummaryUnavailableTTL)); completeErr == nil {
					result.Dead++
				}
				continue
			}
			delay := time.Duration(job.AttemptCount) * time.Minute
			if delay < time.Minute {
				delay = time.Minute
			}
			_, _ = personrelatedcatalog.RetrySummaryJob(ctx, db, job.Category, job.ItemID, job.LeaseToken, now.Add(delay), compactSummaryWorkerReason(collectErr))
		}
		return result, fmt.Errorf("collect person related summaries: %w", collectErr)
	}
	for index := range collected.Patches {
		patch := &collected.Patches[index]
		if patch.SourceStatus != personrelatedcatalog.SummarySourceReady || strings.EqualFold(strings.TrimSpace(patch.DescriptionLanguage), "ja") || strings.TrimSpace(patch.DescriptionOriginal) == "" {
			continue
		}
		if w.translator == nil {
			patch.TranslationStatus = personrelatedcatalog.SummaryTranslationFailed
			patch.DescriptionJA = ""
			continue
		}
		translated, translateErr := w.translator.TranslateDescription(ctx, patch.DescriptionOriginal, patch.DescriptionLanguage)
		if translateErr != nil {
			patch.TranslationStatus = personrelatedcatalog.SummaryTranslationFailed
			patch.DescriptionJA = ""
			continue
		}
		patch.DescriptionJA = translated
		patch.TranslationStatus = personrelatedcatalog.SummaryTranslationReady
	}
	leaseByTarget := make(map[string]string, len(jobs))
	for _, job := range jobs {
		leaseByTarget[job.Category+"\x00"+job.ItemID] = job.LeaseToken
	}
	leasedPatches := make([]personrelatedcatalog.LeasedSummaryPatch, 0, len(collected.Patches))
	for _, patch := range collected.Patches {
		leasedPatches = append(leasedPatches, personrelatedcatalog.LeasedSummaryPatch{
			Patch: patch, LeaseToken: leaseByTarget[patch.Category+"\x00"+patch.ItemID],
		})
	}
	if err := personrelatedcatalog.ApplyLeasedSummaryPatches(ctx, db, leasedPatches, now); err != nil {
		for _, job := range jobs {
			_, _ = personrelatedcatalog.RetrySummaryJob(ctx, db, job.Category, job.ItemID, job.LeaseToken, now.Add(time.Minute), "summary_patch_rejected")
		}
		return result, fmt.Errorf("apply person related summaries: %w", err)
	}
	for _, patch := range collected.Patches {
		if patch.SourceStatus == personrelatedcatalog.SummarySourceReady {
			result.Ready++
		} else {
			result.Unavailable++
		}
	}
	return result, nil
}

func compactSummaryWorkerReason(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	if len(text) > 160 {
		text = text[:160]
	}
	return text
}
