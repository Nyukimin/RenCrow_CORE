package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	personrelatedcatalogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/personrelatedcatalog"
)

// cmdPersonRelated is the deterministic CLI entry for bounded person-related
// catalog collection. It reuses the exact eligibility, plan, attempt, and
// import contracts that the runtime D1 sweep uses; it never bypasses the
// provider or writes hobby-graph rows through a private path.
func cmdPersonRelated() {
	args := os.Args[2:]
	sub := ""
	if len(args) > 0 {
		sub = strings.ToLower(strings.TrimSpace(args[0]))
	}
	switch sub {
	case "collect-batch":
		code := runPersonRelatedCollectBatch(args[1:], os.Stdout, os.Stderr)
		if code != 0 {
			os.Exit(code)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: rencrow person-related collect-batch [--familiarity known|all] [--categories csv] [--interval DURATION] [--max-steps N] [--max-duration DURATION] [--limit-persons N] [--json]")
		os.Exit(1)
	}
}

type personRelatedCollectBatchOptions struct {
	Familiarity  string
	Categories   []string
	Interval     time.Duration
	MaxSteps     int
	MaxDuration  time.Duration
	LimitPersons int
	JSON         bool
}

func parsePersonRelatedCollectBatchArgs(args []string) (personRelatedCollectBatchOptions, error) {
	opts := personRelatedCollectBatchOptions{
		Familiarity: "known",
		Categories:  append([]string(nil), personrelatedcatalogapp.CollectionSweepCategories...),
		Interval:    500 * time.Millisecond,
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			opts.JSON = true
		case "--familiarity":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--familiarity requires a value")
			}
			i++
			value := strings.ToLower(strings.TrimSpace(args[i]))
			if value != "known" && value != "all" {
				return opts, fmt.Errorf("--familiarity must be 'known' or 'all'")
			}
			opts.Familiarity = value
		case "--categories":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--categories requires a value")
			}
			i++
			var categories []string
			for _, raw := range strings.Split(args[i], ",") {
				category := strings.ToLower(strings.TrimSpace(raw))
				if category == "" {
					continue
				}
				valid := false
				for _, known := range personrelatedcatalogapp.CollectionSweepCategories {
					if category == known {
						valid = true
						break
					}
				}
				if !valid {
					return opts, fmt.Errorf("invalid category %q (valid: %s)", category, strings.Join(personrelatedcatalogapp.CollectionSweepCategories, ","))
				}
				categories = append(categories, category)
			}
			if len(categories) == 0 {
				return opts, fmt.Errorf("--categories requires at least one valid category")
			}
			opts.Categories = categories
		case "--interval":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--interval requires a value")
			}
			i++
			interval, err := time.ParseDuration(args[i])
			if err != nil || interval < 0 {
				return opts, fmt.Errorf("invalid --interval %q", args[i])
			}
			opts.Interval = interval
		case "--max-steps":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--max-steps requires a value")
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 0 {
				return opts, fmt.Errorf("invalid --max-steps %q", args[i])
			}
			opts.MaxSteps = n
		case "--max-duration":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--max-duration requires a value")
			}
			i++
			duration, err := time.ParseDuration(args[i])
			if err != nil || duration < 0 {
				return opts, fmt.Errorf("invalid --max-duration %q", args[i])
			}
			opts.MaxDuration = duration
		case "--limit-persons":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--limit-persons requires a value")
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 0 {
				return opts, fmt.Errorf("invalid --limit-persons %q", args[i])
			}
			opts.LimitPersons = n
		default:
			return opts, fmt.Errorf("unknown flag %q", args[i])
		}
	}
	return opts, nil
}

type personRelatedCollectStepReport struct {
	PersonID     string `json:"person_id"`
	PersonName   string `json:"person_name"`
	Category     string `json:"category"`
	Status       string `json:"status"`
	ReasonCode   string `json:"reason_code,omitempty"`
	StopReason   string `json:"stop_reason,omitempty"`
	ItemCount    int    `json:"item_count"`
	ProviderCall bool   `json:"provider_call"`
	Error        string `json:"error,omitempty"`
}

type personRelatedCollectBatchSummary struct {
	Familiarity    string         `json:"familiarity"`
	Categories     []string       `json:"categories"`
	PersonsVisited int            `json:"persons_visited"`
	PairsVisited   int            `json:"pairs_visited"`
	ProviderSteps  int            `json:"provider_steps"`
	StatusCounts   map[string]int `json:"status_counts"`
	ImportedItems  int            `json:"imported_items"`
	Errors         int            `json:"errors"`
	StoppedBy      string         `json:"stopped_by"`
	ElapsedSeconds int64          `json:"elapsed_seconds"`
}

// personRelatedAttemptAfter reports whether the plan result contains a
// collection attempt retrieved at or after the given time, i.e. whether this
// call actually reached the provider instead of replaying fresh attempts.
func personRelatedAttemptAfter(plan personrelatedcatalogapp.CollectionPlanResult, since time.Time) bool {
	for _, attempt := range plan.Attempts {
		retrievedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(attempt.RetrievedAt))
		if err != nil {
			retrievedAt, err = time.Parse(time.RFC3339, strings.TrimSpace(attempt.RetrievedAt))
		}
		if err == nil && !retrievedAt.Before(since) {
			return true
		}
	}
	return false
}

func runPersonRelatedCollectBatch(args []string, out io.Writer, errOut io.Writer) int {
	opts, err := parsePersonRelatedCollectBatchArgs(args)
	if err != nil {
		fmt.Fprintf(errOut, "%v\n", err)
		return 1
	}

	cfg, err := config.LoadConfig(getConfigPath())
	if err != nil {
		fmt.Fprintf(errOut, "failed to load config: %v\n", err)
		return 1
	}
	providerURL := strings.TrimSpace(cfg.PersonRelatedCatalog.ProviderURL)
	if strings.TrimSpace(providerURL) == "" {
		fmt.Fprintln(errOut, "person related catalog provider URL is not configured (set person_related_catalog.provider_url)")
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	prepareCtx, cancelPrepare := context.WithTimeout(ctx, 10*time.Second)
	collector, err := prepareRuntimePersonRelatedCatalogCollector(
		prepareCtx,
		cfg.Storage.Databases.MovieCatalog,
		cfg.Storage.Databases.HobbyGraph,
		providerURL,
	)
	cancelPrepare()
	if err != nil {
		fmt.Fprintf(errOut, "failed to prepare collector: %v\n", err)
		return 1
	}

	movieDB, err := openRuntimeMovieCatalogReadOnly(collector.movieCatalogPath)
	if err != nil {
		fmt.Fprintf(errOut, "failed to open movie catalog: %v\n", err)
		return 1
	}
	defer movieDB.Close()
	movieDB.SetMaxOpenConns(1)

	summary := personRelatedCollectBatchSummary{
		Familiarity:  opts.Familiarity,
		Categories:   opts.Categories,
		StatusCounts: map[string]int{},
		StoppedBy:    "complete",
	}
	started := time.Now()
	deadline := time.Time{}
	if opts.MaxDuration > 0 {
		deadline = started.Add(opts.MaxDuration)
	}

	emitStep := func(report personRelatedCollectStepReport) {
		if opts.JSON {
			line, marshalErr := json.Marshal(report)
			if marshalErr == nil {
				fmt.Fprintln(out, string(line))
			}
			return
		}
		suffix := report.Status
		if report.ReasonCode != "" {
			suffix += " (" + report.ReasonCode + ")"
		}
		if report.Error != "" {
			suffix = "error: " + report.Error
		}
		fmt.Fprintf(out, "%s %s [%s] items=%d %s\n", report.PersonID, report.PersonName, report.Category, report.ItemCount, suffix)
	}

	cursor := ""
loop:
	for {
		if ctx.Err() != nil {
			summary.StoppedBy = "signal"
			break
		}
		person, found, err := personrelatedcatalogapp.NextEligiblePersonByID(ctx, movieDB, cursor)
		for retry := 0; retry < 3 && err != nil && ctx.Err() == nil && strings.Contains(err.Error(), "database is locked"); retry++ {
			select {
			case <-ctx.Done():
			case <-time.After(time.Duration(retry+1) * 2 * time.Second):
			}
			person, found, err = personrelatedcatalogapp.NextEligiblePersonByID(ctx, movieDB, cursor)
		}
		if err != nil {
			fmt.Fprintf(errOut, "failed to select next eligible person: %v\n", err)
			return 1
		}
		if !found {
			break
		}
		cursor = person.MovieCatalogPersonID
		if opts.Familiarity == "known" && person.Familiarity != "known" {
			continue
		}
		summary.PersonsVisited++
		if opts.LimitPersons > 0 && summary.PersonsVisited > opts.LimitPersons {
			summary.PersonsVisited--
			summary.StoppedBy = "limit-persons"
			break
		}
		for _, category := range opts.Categories {
			if ctx.Err() != nil {
				summary.StoppedBy = "signal"
				break loop
			}
			if !deadline.IsZero() && time.Now().After(deadline) {
				summary.StoppedBy = "max-duration"
				break loop
			}
			if opts.MaxSteps > 0 && summary.ProviderSteps >= opts.MaxSteps {
				summary.StoppedBy = "max-steps"
				break loop
			}
			summary.PairsVisited++
			report := personRelatedCollectStepReport{
				PersonID:   person.MovieCatalogPersonID,
				PersonName: person.Name,
				Category:   category,
			}
			callStarted := time.Now().UTC().Add(-2 * time.Second)
			result, collectErr := collector.CollectByPersonID(ctx, person.MovieCatalogPersonID, category)
			// The hobby graph is shared with the runtime sweep and enrichment
			// workers; a busy write lock is transient, so retry the pair a
			// bounded number of times before recording it as an error.
			for retry := 0; retry < 2 && collectErr != nil && ctx.Err() == nil && strings.Contains(collectErr.Error(), "database is locked"); retry++ {
				select {
				case <-ctx.Done():
				case <-time.After(time.Duration(retry+1) * 2 * time.Second):
				}
				result, collectErr = collector.CollectByPersonID(ctx, person.MovieCatalogPersonID, category)
			}
			if collectErr != nil {
				if ctx.Err() != nil {
					summary.StoppedBy = "signal"
					break loop
				}
				summary.Errors++
				report.Error = collectErr.Error()
				emitStep(report)
				continue
			}
			newItems := 0
			if plan, ok := result.(personrelatedcatalogapp.CollectionPlanResult); ok {
				report.Status = plan.Status
				report.ReasonCode = plan.ReasonCode
				report.StopReason = plan.StopReason
				report.ProviderCall = personRelatedAttemptAfter(plan, callStarted)
				for _, attempt := range plan.Attempts {
					report.ItemCount += attempt.ItemCount
					retrievedAt, parseErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(attempt.RetrievedAt))
					if parseErr != nil {
						retrievedAt, parseErr = time.Parse(time.RFC3339, strings.TrimSpace(attempt.RetrievedAt))
					}
					if parseErr == nil && !retrievedAt.Before(callStarted) {
						newItems += attempt.ItemCount
					}
				}
			}
			if report.Status == "" {
				report.Status = "unknown"
			}
			summary.StatusCounts[report.Status]++
			summary.ImportedItems += newItems
			emitStep(report)
			if !report.ProviderCall {
				continue
			}
			summary.ProviderSteps++
			if opts.Interval > 0 {
				select {
				case <-ctx.Done():
					summary.StoppedBy = "signal"
					break loop
				case <-time.After(opts.Interval):
				}
			}
		}
	}

	summary.ElapsedSeconds = int64(time.Since(started).Seconds())
	if opts.JSON {
		line, marshalErr := json.Marshal(map[string]any{"summary": summary})
		if marshalErr == nil {
			fmt.Fprintln(out, string(line))
		}
	} else {
		fmt.Fprintf(out, "done: stopped_by=%s persons=%d pairs=%d provider_steps=%d imported_items=%d errors=%d elapsed=%ds status=%v\n",
			summary.StoppedBy, summary.PersonsVisited, summary.PairsVisited, summary.ProviderSteps, summary.ImportedItems, summary.Errors, summary.ElapsedSeconds, summary.StatusCounts)
	}
	return 0
}
