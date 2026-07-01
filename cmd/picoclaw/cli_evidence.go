package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	domainexecution "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/execution"
	executionpersistence "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/execution"
)

func cmdEvidence() {
	configPath := getConfigPath()
	store, err := loadEvidenceStore(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize evidence store: %v\n", err)
		os.Exit(1)
	}
	code := runEvidenceCommand(os.Args[2:], store, os.Stdout, os.Stderr)
	if code != 0 {
		os.Exit(code)
	}
}

type evidenceStore interface {
	ListRecent(ctx context.Context, limit int) ([]domainexecution.ExecutionReport, error)
	GetByJobID(ctx context.Context, jobID string) (domainexecution.ExecutionReport, error)
	Summary(ctx context.Context) (map[string]map[string]int, error)
}

func runEvidenceCommand(args []string, store evidenceStore, out io.Writer, errOut io.Writer) int {
	subcmd := "list"
	if len(args) > 0 {
		subcmd = strings.ToLower(strings.TrimSpace(args[0]))
	}
	compact := hasFlag(args, "--compact")
	pretty := !compact

	switch subcmd {
	case "list":
		limit, jsonOut, statusFilter, errorKindFilter, sinceHours, parseErr := parseEvidenceListArgs(args[1:])
		if parseErr != nil {
			fmt.Fprintf(errOut, "%v\n", parseErr)
			return 1
		}
		items, err := store.ListRecent(context.Background(), limit)
		if err != nil {
			fmt.Fprintf(errOut, "failed to list evidence: %v\n", err)
			return 1
		}
		items = filterEvidence(items, statusFilter, errorKindFilter, sinceHours)
		if jsonOut {
			writeJSONCLI(out, map[string]any{"items": items}, pretty)
			return 0
		}
		if len(items) == 0 {
			fmt.Fprintln(out, "No evidence records")
			return 0
		}
		for _, it := range items {
			fmt.Fprintf(out, "%s | %s | %s | %s\n", it.JobID, it.Status, it.ErrorKind, it.Goal)
		}
		return 0
	case "show":
		if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
			fmt.Fprintln(errOut, "usage: picoclaw evidence show <job_id>")
			return 1
		}
		jobID := strings.TrimSpace(args[1])
		item, err := store.GetByJobID(context.Background(), jobID)
		if err != nil {
			fmt.Fprintf(errOut, "failed to get evidence: %v\n", err)
			return 1
		}
		writeJSONCLI(out, item, pretty)
		return 0
	case "summary":
		_, _, statusFilter, errorKindFilter, sinceHours, parseErr := parseEvidenceListArgs(args[1:])
		if parseErr != nil {
			fmt.Fprintf(errOut, "%v\n", parseErr)
			return 1
		}
		var summary map[string]map[string]int
		if statusFilter == "" && errorKindFilter == "" && sinceHours <= 0 {
			s, err := store.Summary(context.Background())
			if err != nil {
				fmt.Fprintf(errOut, "failed to summarize evidence: %v\n", err)
				return 1
			}
			summary = s
		} else {
			items, err := store.ListRecent(context.Background(), 10000)
			if err != nil {
				fmt.Fprintf(errOut, "failed to summarize evidence: %v\n", err)
				return 1
			}
			items = filterEvidence(items, statusFilter, errorKindFilter, sinceHours)
			summary = summarizeEvidence(items)
		}
		writeJSONCLI(out, map[string]any{"summary": summary}, pretty)
		return 0
	default:
		fmt.Fprintf(errOut, "unknown evidence subcommand: %s\n", subcmd)
		fmt.Fprintln(errOut, "usage: picoclaw evidence [list|show|summary]")
		return 1
	}
}

func parseEvidenceListArgs(args []string) (limit int, jsonOut bool, statusFilter string, errorKindFilter string, sinceHours int, parseErr error) {
	limit = 20
	validStatus := map[string]struct{}{
		"passed": {},
		"failed": {},
		"other":  {},
	}
	validErrorKind := map[string]struct{}{
		"apply":  {},
		"verify": {},
		"repair": {},
		"none":   {},
		"other":  {},
	}
	for i := 0; i < len(args); i++ {
		v := strings.TrimSpace(strings.ToLower(args[i]))
		if v == "--json" {
			jsonOut = true
			continue
		}
		if v == "--status" && i+1 < len(args) {
			statusFilter = strings.TrimSpace(strings.ToLower(args[i+1]))
			if _, ok := validStatus[statusFilter]; !ok {
				parseErr = fmt.Errorf("invalid --status: %s", strings.TrimSpace(args[i+1]))
				return
			}
			i++
			continue
		}
		if v == "--error-kind" && i+1 < len(args) {
			errorKindFilter = strings.TrimSpace(strings.ToLower(args[i+1]))
			if _, ok := validErrorKind[errorKindFilter]; !ok {
				parseErr = fmt.Errorf("invalid --error-kind: %s", strings.TrimSpace(args[i+1]))
				return
			}
			i++
			continue
		}
		if v == "--since-hours" && i+1 < len(args) {
			n, err := strconv.Atoi(strings.TrimSpace(args[i+1]))
			if err != nil || n <= 0 {
				parseErr = fmt.Errorf("invalid --since-hours: %s", strings.TrimSpace(args[i+1]))
				return
			}
			sinceHours = n
			i++
			continue
		}
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	return
}

func filterEvidence(items []domainexecution.ExecutionReport, statusFilter, errorKindFilter string, sinceHours int) []domainexecution.ExecutionReport {
	if statusFilter == "" && errorKindFilter == "" && sinceHours <= 0 {
		return items
	}
	var cutoff time.Time
	if sinceHours > 0 {
		cutoff = time.Now().UTC().Add(-time.Duration(sinceHours) * time.Hour)
	}
	filtered := make([]domainexecution.ExecutionReport, 0, len(items))
	for _, it := range items {
		if statusFilter != "" && strings.ToLower(strings.TrimSpace(it.Status)) != statusFilter {
			continue
		}
		if errorKindFilter != "" && strings.ToLower(strings.TrimSpace(it.ErrorKind)) != errorKindFilter {
			continue
		}
		if !cutoff.IsZero() {
			ref := it.FinishedAt
			if ref.IsZero() {
				ref = it.CreatedAt
			}
			if ref.IsZero() || ref.Before(cutoff) {
				continue
			}
		}
		filtered = append(filtered, it)
	}
	return filtered
}

func summarizeEvidence(items []domainexecution.ExecutionReport) map[string]map[string]int {
	summary := map[string]map[string]int{
		"status": {
			"passed": 0,
			"failed": 0,
			"other":  0,
		},
		"error_kind": {
			"apply":  0,
			"verify": 0,
			"repair": 0,
			"none":   0,
			"other":  0,
		},
	}
	for _, it := range items {
		switch strings.ToLower(strings.TrimSpace(it.Status)) {
		case "passed":
			summary["status"]["passed"]++
		case "failed":
			summary["status"]["failed"]++
		default:
			summary["status"]["other"]++
		}
		k := strings.ToLower(strings.TrimSpace(it.ErrorKind))
		switch k {
		case "apply":
			summary["error_kind"]["apply"]++
		case "verify":
			summary["error_kind"]["verify"]++
		case "repair":
			summary["error_kind"]["repair"]++
		case "":
			summary["error_kind"]["none"]++
		default:
			summary["error_kind"]["other"]++
		}
	}
	return summary
}

func loadEvidenceStore(configPath string) (*executionpersistence.JSONLReportStore, error) {
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return nil, err
	}
	p := strings.TrimSpace(cfg.Security.Audit.Path)
	if p == "" {
		p = defaultExecutionReportPath(cfg.WorkspaceDir)
	}
	return executionpersistence.NewJSONLReportStore(p)
}
