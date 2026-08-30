package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	knowledgememoryapp "github.com/Nyukimin/RenCrow_CORE/internal/application/knowledgememory"
	moviecatalogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/moviecatalog"
	newsbriefapp "github.com/Nyukimin/RenCrow_CORE/internal/application/newsbrief"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/sourcefetcher"
	superagentapp "github.com/Nyukimin/RenCrow_CORE/internal/application/superagent"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	webgatherinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/webgather"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type backgroundJobFailureReporter struct {
	listener orchestrator.EventListener
}

func newBackgroundJobFailureReporter(listener orchestrator.EventListener) backgroundJobFailureReporter {
	if listener != nil {
		value := reflect.ValueOf(listener)
		if value.Kind() == reflect.Pointer && value.IsNil() {
			listener = nil
		}
	}
	return backgroundJobFailureReporter{listener: listener}
}

func (r backgroundJobFailureReporter) Failed(job string, err error, detail string) {
	r.failedWithTrace("", job, err, detail)
}

func (r backgroundJobFailureReporter) FailedWithTrace(traceID modulecore.TraceID, job string, err error, detail string) {
	r.failedWithTrace(traceID, job, err, detail)
}

func (r backgroundJobFailureReporter) failedWithTrace(traceID modulecore.TraceID, job string, err error, detail string) {
	if r.listener == nil || err == nil {
		return
	}
	if traceID.Validate() != nil {
		traceID = modulecore.NewTraceID()
	}
	job = normalizeBackgroundJobName(job)
	errorText := compactBackgroundJobText(err.Error(), 600)
	detail = compactBackgroundJobText(detail, 600)
	jobKey := sanitizeBackgroundJobKey(job)
	jobID := fmt.Sprintf("background-%s-%d", jobKey, time.Now().UnixNano())
	sessionID := "background:" + jobKey
	payload := map[string]string{
		"job_id":         jobID,
		"job":            job,
		"status":         "failed",
		"error":          errorText,
		"llm_policy":     "no_llm_until_failure",
		"shiro_action":   "investigate",
		"mio_action":     "report_if_user_visible",
		"background_job": "true",
	}
	if detail != "" {
		payload["detail"] = detail
	}
	payloadJSON, _ := json.Marshal(payload)
	r.listener.OnEvent(orchestrator.NewEventWithTraceID(traceID, "background_job.failed", "background_job", "shiro", string(payloadJSON), "OPS", jobID, sessionID, "background", job))
	r.listener.OnEvent(orchestrator.NewEventWithTraceID(traceID, "job.notification", "shiro", "mio", backgroundJobFailureNotification(job, errorText, detail), "OPS", jobID, sessionID, "background", job))
}

func normalizeBackgroundJobName(job string) string {
	job = strings.TrimSpace(job)
	if job == "" {
		return "background_job"
	}
	return job
}

func sanitizeBackgroundJobKey(job string) string {
	job = strings.ToLower(normalizeBackgroundJobName(job))
	var b strings.Builder
	lastDash := false
	for _, r := range job {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	key := strings.Trim(b.String(), "-")
	if key == "" {
		return "background-job"
	}
	return key
}

func compactBackgroundJobText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || text == "" {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "..."
}

func backgroundJobFailureNotification(job string, errorText string, detail string) string {
	content := fmt.Sprintf("background job failed: job=%s error=%s. Shiro investigation requested; Mio should report only if this affects the user.", job, errorText)
	if detail != "" {
		content += " detail=" + detail
	}
	return content
}

func startConversationBackgroundJobs(cfg *config.Config, runtime conversationRuntime, listener orchestrator.EventListener) {
	reporter := newBackgroundJobFailureReporter(listener)
	if runtime.L1Store != nil {
		bootstrapDefaultNewsSources(runtime.L1Store, reporter)
		startSourceRegistrySweeper(cfg, runtime.L1Store, reporter)
		startMemoryLifecycleJob(runtime.L1Store, reporter)
	}
}

func bootstrapDefaultNewsSources(store newsbriefapp.DefaultSourceRegistry, reporter backgroundJobFailureReporter) {
	result, err := newsbriefapp.BootstrapDefaultSources(context.Background(), store)
	if err != nil {
		log.Printf("WARN: default news source bootstrap partially failed: added=%d existing=%d failed=%d error=%v", result.Added, result.Existing, result.Failed, err)
		reporter.Failed("default_news_source_bootstrap", err, fmt.Sprintf("added=%d existing=%d failed=%d", result.Added, result.Existing, result.Failed))
		return
	}
	log.Printf("Default news sources ready: added=%d existing=%d", result.Added, result.Existing)
}

func startSourceRegistrySweeper(cfg *config.Config, store *l1sqlite.L1SQLiteStore, reporter backgroundJobFailureReporter) {
	sweep := func() {
		now := time.Now().UTC()
		opts := sourceRegistrySweepOptions(cfg)
		result, err := sourcefetcher.SweepAllFeedSources(context.Background(), store, now, opts)
		if err != nil {
			log.Printf("WARN: all-feed source registry sweep failed: %v", err)
			reporter.Failed("source_registry_all_feed_sweep", err, "limit_per_source=10 minimum_trust_score=0.5")
		} else if result.Sources > 0 || result.Staged > 0 || result.Failed > 0 {
			log.Printf("All-feed source registry sweep complete: sources=%d staged=%d validated=%d promoted_news=%d article_fetched=%d article_reused=%d article_deferred=%d skipped_existing=%d failed=%d",
				result.Sources, result.Staged, result.Validated, result.PromotedNews, result.ArticleFetched, result.ArticleReused, result.ArticleDeferred, result.SkippedExisting, result.Failed)
		}

		dueResult, dueErr := sourcefetcher.SweepDueSources(context.Background(), store, now, opts)
		if dueErr != nil {
			log.Printf("WARN: due source registry sweep failed: %v", dueErr)
			reporter.Failed("source_registry_due_sweep", dueErr, "limit_per_source=10 minimum_trust_score=0.5")
		} else if dueResult.Sources > 0 || dueResult.Staged > 0 || dueResult.Failed > 0 {
			log.Printf("Due source registry sweep complete: sources=%d staged=%d validated=%d promoted_news=%d failed=%d",
				dueResult.Sources, dueResult.Staged, dueResult.Validated, dueResult.PromotedNews, dueResult.Failed)
		}
	}
	go func() {
		sweep()
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			sweep()
		}
	}()
}

func sourceRegistrySweepOptions(cfg *config.Config) sourcefetcher.SweepOptions {
	opts := sourcefetcher.SweepOptions{
		LimitPerSource:    10,
		MinimumTrustScore: 0.5,
	}
	if cfg != nil && cfg.WebGather.ArticleReader.Enabled {
		reader := cfg.WebGather.ArticleReader
		opts.ArticleFallback = webgatherinfra.NewArticleReaderFetcher(webgatherinfra.ArticleReaderFetcherConfig{
			Enabled: reader.Enabled, EndpointPrefix: reader.EndpointPrefix,
			AllowedSourceHosts: append([]string(nil), reader.AllowedSourceHosts...), TimeoutMS: reader.TimeoutMS,
		})
	}
	return opts
}

func startMemoryLifecycleJob(store *l1sqlite.L1SQLiteStore, reporter backgroundJobFailureReporter) {
	startMemoryLifecycleJobWithConfig(store, memoryLifecycleJobConfigFromEnv(time.Now), reporter)
}

type memoryLifecycleJobConfig struct {
	Interval time.Duration
	Now      func() time.Time
	Label    string
}

const minimumAcceleratedMemoryLifecycleInterval = 100 * time.Millisecond

func memoryLifecycleJobConfigFromEnv(wallNow func() time.Time) memoryLifecycleJobConfig {
	if wallNow == nil {
		wallNow = time.Now
	}
	cfg := memoryLifecycleJobConfig{
		Interval: 24 * time.Hour,
		Now: func() time.Time {
			return wallNow().UTC()
		},
		Label: "normal",
	}
	explicitInterval, hasExplicitInterval := memoryLifecycleDurationFromEnv(
		"RENCROW_MEMORY_LIFECYCLE_INTERVAL_MS",
		"RENCROW_MEMORY_LIFECYCLE_INTERVAL_SEC",
	)
	if hasExplicitInterval {
		cfg.Interval = explicitInterval
	}
	monthDuration, hasAcceleration := memoryLifecycleDurationFromEnv(
		"RENCROW_MEMORY_LIFECYCLE_ACCEL_MONTH_MS",
		"RENCROW_MEMORY_LIFECYCLE_ACCEL_MONTH_SEC",
	)
	if !hasAcceleration {
		return cfg
	}
	startWall := wallNow().UTC()
	startSim := startWall
	scale := (30 * 24 * time.Hour).Seconds() / monthDuration.Seconds()
	cfg.Now = func() time.Time {
		elapsed := wallNow().UTC().Sub(startWall)
		if elapsed < 0 {
			elapsed = 0
		}
		return startSim.Add(time.Duration(float64(elapsed) * scale)).UTC()
	}
	if !hasExplicitInterval {
		tick := monthDuration / 120
		if tick < minimumAcceleratedMemoryLifecycleInterval {
			tick = minimumAcceleratedMemoryLifecycleInterval
		}
		if tick > 60*time.Second {
			tick = 60 * time.Second
		}
		cfg.Interval = tick
	}
	cfg.Label = fmt.Sprintf("accelerated:30d/%s interval=%s", monthDuration, cfg.Interval)
	return cfg
}

func memoryLifecycleDurationFromEnv(msKey string, secKey string) (time.Duration, bool) {
	if raw := strings.TrimSpace(os.Getenv(msKey)); raw != "" {
		ms, err := strconv.Atoi(raw)
		if err != nil || ms <= 0 {
			log.Printf("WARN: invalid %s=%q", msKey, raw)
			return 0, false
		}
		return time.Duration(ms) * time.Millisecond, true
	}
	if raw := strings.TrimSpace(os.Getenv(secKey)); raw != "" {
		sec, err := strconv.Atoi(raw)
		if err != nil || sec <= 0 {
			log.Printf("WARN: invalid %s=%q", secKey, raw)
			return 0, false
		}
		return time.Duration(sec) * time.Second, true
	}
	return 0, false
}

func startMemoryLifecycleJobWithConfig(store *l1sqlite.L1SQLiteStore, cfg memoryLifecycleJobConfig, reporter backgroundJobFailureReporter) {
	startMemoryLifecycleJobRunner(store, cfg, nil, reporter)
}

func startMemoryLifecycleJobWithStop(store *l1sqlite.L1SQLiteStore, cfg memoryLifecycleJobConfig, stop <-chan struct{}, reporter backgroundJobFailureReporter) {
	startMemoryLifecycleJobRunner(store, cfg, stop, reporter)
}

type memoryLifecycleMaintenanceRunner interface {
	RunMemoryLifecycleMaintenance(ctx context.Context, opts l1sqlite.MemoryLifecycleOptions) (*l1sqlite.MemoryLifecycleResult, error)
}

func startMemoryLifecycleJobRunner(store memoryLifecycleMaintenanceRunner, cfg memoryLifecycleJobConfig, stop <-chan struct{}, reporter backgroundJobFailureReporter) {
	if store == nil {
		return
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 24 * time.Hour
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.Label != "" && cfg.Label != "normal" {
		log.Printf("Memory lifecycle job enabled: %s", cfg.Label)
	}
	run := func() {
		opts := l1sqlite.DefaultMemoryLifecycleOptions()
		opts.Now = cfg.Now()
		result, err := store.RunMemoryLifecycleMaintenance(context.Background(), opts)
		if err != nil {
			log.Printf("WARN: memory lifecycle maintenance failed: %v", err)
			reporter.Failed("memory_lifecycle", err, "label="+cfg.Label)
			return
		}
		if result.RawCompacted > 0 || result.CandidatesQueued > 0 || result.MonthlyHighlightsBuilt > 0 || result.ThreadSummarySeedsQueued > 0 || result.Decayed > 0 || result.VectorCleanupQueued > 0 || result.VectorCleanupExecuted > 0 {
			log.Printf("Memory lifecycle maintenance complete: raw_compacted=%d candidates_queued=%d monthly_highlights_built=%d thread_summary_seeds_queued=%d decayed=%d vector_cleanup_queued=%d vector_cleanup_executed=%d",
				result.RawCompacted, result.CandidatesQueued, result.MonthlyHighlightsBuilt, result.ThreadSummarySeedsQueued, result.Decayed, result.VectorCleanupQueued, result.VectorCleanupExecuted)
		}
	}
	go func() {
		run()
		ticker := time.NewTicker(cfg.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				run()
			case <-stop:
				return
			}
		}
	}()
}

func startDailyIntakeSweeper(rules knowledgememoryapp.DailyIntakeRuleStore, registry knowledgememoryapp.DailyIntakeRegistryStore, reporter backgroundJobFailureReporter) {
	if rules == nil || registry == nil {
		return
	}
	sweep := func() {
		result, err := knowledgememoryapp.RunDailyIntakeSweep(context.Background(), rules, registry, knowledgememoryapp.DailyIntakeSweepOptions{
			RuleLimit:         100,
			SourceLimit:       10,
			MinimumTrustScore: 0.5,
		})
		if err != nil {
			log.Printf("WARN: daily intake sweep failed: %v", err)
			reporter.Failed("daily_intake_sweep", err, "rule_limit=100 source_limit=10 minimum_trust_score=0.5")
			return
		}
		if result.SourcesEnabled > 0 || result.RegistrySweep.Staged > 0 || result.RegistrySweep.Failed > 0 {
			log.Printf("Daily intake sweep complete: rules=%d enabled=%d skipped=%d staged=%d promoted_knowledge=%d failed=%d",
				result.RulesScanned, result.SourcesEnabled, result.SourcesSkipped, result.RegistrySweep.Staged, result.RegistrySweep.PromotedKnowledge, result.RegistrySweep.Failed)
		}
	}
	go func() {
		sweep()
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			sweep()
		}
	}()
}

func startMovieCatalogBackfillJob(cfg *config.Config, reporter backgroundJobFailureReporter) {
	if cfg == nil {
		return
	}
	if movieCatalogBackfillDisabled() {
		log.Printf("[MovieCatalogBackfill] disabled by environment")
		return
	}
	dbPath := resolveMovieCatalogBackfillDBPath(cfg.Storage.Databases.MovieCatalog)
	if dbPath == "" {
		log.Printf("[MovieCatalogBackfill] skipped: movie catalog DB not found")
		return
	}
	crawlerURL := resolveMovieCatalogCrawlerBaseURL(cfg)
	if crawlerURL == "" {
		log.Printf("[MovieCatalogBackfill] skipped: movie_catalog.crawler_url is not configured")
		return
	}
	interval := movieCatalogBackfillDurationEnv("RENCROW_MOVIE_CATALOG_BACKFILL_INTERVAL_SEC", 5*time.Minute, time.Minute)
	initialDelay := movieCatalogBackfillDurationEnv("RENCROW_MOVIE_CATALOG_BACKFILL_INITIAL_DELAY_SEC", 10*time.Second, 0)
	timeout := movieCatalogBackfillDurationEnv("RENCROW_MOVIE_CATALOG_BACKFILL_TIMEOUT_SEC", 90*time.Second, 10*time.Second)
	maxPages := movieCatalogBackfillIntEnv("RENCROW_MOVIE_CATALOG_BACKFILL_MAX_PAGES", 1, 1, 3)
	crawlerDelay := movieCatalogBackfillDurationEnv("RENCROW_MOVIE_CATALOG_BACKFILL_CRAWLER_DELAY_SEC", 2*time.Second, time.Second)

	job := moviecatalogapp.NewBackfillService(moviecatalogapp.BackfillOptions{
		DBPath:       dbPath,
		Interval:     interval,
		InitialDelay: initialDelay,
		Timeout:      timeout,
		MaxPages:     maxPages,
		CrawlerDelay: crawlerDelay,
		Crawler:      moviecatalogapp.NewHTTPCrawler(crawlerURL, timeout),
	})
	go func() {
		for result := range job.Start(context.Background()) {
			if result.Status == "idle" {
				continue
			}
			moviecatalogapp.LogBackfillResult("[MovieCatalogBackfill]", result)
			if result.Status == "error" {
				detail := fmt.Sprintf("kind=%s id=%s title=%q url=%s", result.Target.Kind, result.Target.ID, result.Target.Title, result.Target.URL)
				errText := strings.TrimSpace(result.Output)
				if errText == "" {
					errText = "movie catalog backfill failed"
				}
				reporter.Failed("movie_catalog_backfill", fmt.Errorf("%s", errText), detail)
			}
		}
	}()
	log.Printf("[MovieCatalogBackfill] enabled: db=%s interval=%s initial_delay=%s timeout=%s max_pages=%d crawler_delay=%s",
		dbPath, interval, initialDelay, timeout, maxPages, crawlerDelay)
}

func resolveMovieCatalogCrawlerBaseURL(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.MovieCatalog.CrawlerURL)
}

func movieCatalogBackfillDisabled() bool {
	disabled := strings.ToLower(strings.TrimSpace(os.Getenv("RENCROW_MOVIE_CATALOG_BACKFILL_DISABLED")))
	switch disabled {
	case "1", "true", "yes", "on":
		return true
	}
	enabled := strings.ToLower(strings.TrimSpace(os.Getenv("RENCROW_MOVIE_CATALOG_BACKFILL")))
	switch enabled {
	case "0", "false", "no", "off", "disabled":
		return true
	}
	return false
}

func resolveMovieCatalogBackfillDBPath(configured string) string {
	if configured = strings.TrimSpace(configured); configured != "" {
		if st, err := os.Stat(configured); err == nil && !st.IsDir() {
			return configured
		}
		return ""
	}
	candidates := []string{}
	if env := strings.TrimSpace(os.Getenv("RENCROW_MOVIE_CATALOG_DB")); env != "" {
		candidates = append(candidates, env)
	}
	candidates = append(candidates,
		filepath.Join("tmp", "eiga_catalog", "eiga_catalog.sqlite"),
		filepath.Join("tmp", "eiga_catalog_smoke", "eiga_catalog.sqlite"),
	)
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func movieCatalogBackfillDurationEnv(name string, fallback time.Duration, min time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	sec, err := strconv.Atoi(raw)
	if err != nil || sec < 0 {
		log.Printf("WARN: invalid %s=%q; using %s", name, raw, fallback)
		return fallback
	}
	d := time.Duration(sec) * time.Second
	if d < min {
		log.Printf("WARN: %s=%s is too small; using minimum %s", name, d, min)
		return min
	}
	return d
}

func movieCatalogBackfillIntEnv(name string, fallback int, min int, max int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		log.Printf("WARN: invalid %s=%q; using %d", name, raw, fallback)
		return fallback
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

type superAgentRunQueueMessageProcessor interface {
	ProcessMessage(context.Context, orchestrator.ProcessMessageRequest) (orchestrator.ProcessMessageResponse, error)
}

type superAgentRunQueueStore interface {
	superagentapp.RunQueueStore
	superagentapp.InterruptedRunRecoveryStore
}

func startSuperAgentRunQueueScheduler(cfg *config.Config, store superAgentRunQueueStore, processor superAgentRunQueueMessageProcessor, reporter backgroundJobFailureReporter) {
	if cfg == nil || !cfg.SuperAgentHarness.RunQueueSchedulerEnabled {
		return
	}
	if store == nil || processor == nil {
		err := fmt.Errorf("superagent run queue scheduler requested but store or processor is unavailable")
		log.Printf("WARN: %v", err)
		reporter.Failed("superagent_run_queue", err, "")
		return
	}
	queued, blocked, err := superagentapp.RecoverInterruptedAgentRuns(context.Background(), store, time.Now().UTC())
	if err != nil {
		log.Printf("WARN: superagent interrupted run recovery failed: %v", err)
		reporter.Failed("superagent_run_recovery", err, "")
		return
	}
	log.Printf("SuperAgent interrupted run recovery: queued=%d blocked=%d", queued, blocked)
	interval := time.Duration(cfg.SuperAgentHarness.RunQueueSchedulerIntervalSec) * time.Second
	claimLimit := cfg.SuperAgentHarness.RunQueueSchedulerClaimLimit
	scheduler := superagentapp.NewRunQueueScheduler(store, newSuperAgentRunQueueProcessor(processor, reporter), superagentapp.RunQueueSchedulerOptions{
		Interval:   interval,
		ClaimLimit: claimLimit,
	})
	scheduler.Start(context.Background())
	log.Printf("SuperAgent run queue scheduler enabled: interval=%s claim_limit=%d", interval, claimLimit)
}

func newSuperAgentRunQueueProcessor(processor superAgentRunQueueMessageProcessor, reporter backgroundJobFailureReporter) superagentapp.RunQueueProcessorFunc {
	return superagentapp.RunQueueProcessorFunc(func(ctx context.Context, item domainsuperagent.RunQueueItem, traceID modulecore.TraceID) (string, error) {
		fail := func(err error) (string, error) {
			detail := fmt.Sprintf("queue_id=%s run_id=%s workstream_id=%s action=%s", strings.TrimSpace(item.QueueID), strings.TrimSpace(item.RunID), strings.TrimSpace(item.WorkstreamID), strings.TrimSpace(item.Action))
			reporter.FailedWithTrace(traceID, "superagent_run_queue", err, detail)
			return "", err
		}
		if err := traceID.Validate(); err != nil {
			return fail(fmt.Errorf("trace_id must be canonical: %w", err))
		}
		action := strings.TrimSpace(item.Action)
		if action != "resume" && action != "process_message" && action != "chat" {
			return fail(fmt.Errorf("unsupported run queue action: %s", action))
		}
		sessionID := strings.TrimSpace(item.WorkstreamID)
		if sessionID == "" {
			sessionID = strings.TrimSpace(item.RunID)
		}
		if sessionID == "" {
			sessionID = "superagent:" + strings.TrimSpace(item.QueueID)
		}
		resp, err := processor.ProcessMessage(ctx, orchestrator.ProcessMessageRequest{
			JobID:                    strings.TrimPrefix(strings.TrimSpace(item.RunID), "run_lead_"),
			TraceID:                  string(traceID),
			SessionID:                sessionID,
			Channel:                  "superagent",
			ChatID:                   strings.TrimSpace(item.QueueID),
			UserMessage:              strings.TrimSpace(item.Goal),
			ResumeCheckpointRevision: item.CheckpointRevision,
			ResumeCheckpointSummary:  strings.TrimSpace(item.CheckpointSummary),
			ResumeNextAction:         strings.TrimSpace(item.NextAction),
		})
		if err != nil {
			return fail(err)
		}
		if resp.Route == "" {
			return fail(fmt.Errorf("run queue item did not produce a route"))
		}
		if action != "chat" && resp.Route == "CHAT" {
			return fail(fmt.Errorf("run queue item fell back to CHAT route"))
		}
		if strings.TrimSpace(resp.JobID) == "" {
			return fail(fmt.Errorf("run queue item did not produce a job_id"))
		}
		return fmt.Sprintf("route=%s job_id=%s", resp.Route, resp.JobID), nil
	})
}
