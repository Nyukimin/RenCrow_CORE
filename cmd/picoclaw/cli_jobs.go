package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/jobmanager"
	domainjob "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/job"
	jobpersistence "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/job"
)

func cmdJobs() {
	manager, err := loadJobManager(getConfigPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize job manager: %v\n", err)
		os.Exit(1)
	}
	code := runJobsCommand(os.Args[2:], manager, os.Stdout, os.Stderr)
	if code != 0 {
		os.Exit(code)
	}
}

type jobCommandStore interface {
	CreateJob(ctx context.Context, draft domainjob.Job, shared domainjob.SharedRoleContext) (domainjob.Job, error)
	StartJob(ctx context.Context, jobID string) (domainjob.Job, error)
	CancelJob(ctx context.Context, jobID string, summary string) (domainjob.Job, error)
	UpdateStatus(ctx context.Context, jobID string, status domainjob.Status, summary string, nextActions []string) (domainjob.Job, error)
	ListJobs(ctx context.Context, filter domainjob.Filter) ([]domainjob.Job, error)
	GetJob(ctx context.Context, jobID string) (domainjob.Job, error)
	GetContext(ctx context.Context, jobID string) (domainjob.SharedRoleContext, error)
	ListNotifications(ctx context.Context, limit int, interruptOnly bool) ([]domainjob.Notification, error)
}

func runJobsCommand(args []string, manager jobCommandStore, out io.Writer, errOut io.Writer) int {
	subcmd := "list"
	if len(args) > 0 {
		subcmd = strings.ToLower(strings.TrimSpace(args[0]))
	}
	compact := hasFlag(args, "--compact")
	pretty := !compact
	switch subcmd {
	case "list":
		filter, jsonOut, err := parseJobsListArgs(args[1:])
		if err != nil {
			fmt.Fprintf(errOut, "%v\n", err)
			return 1
		}
		items, err := manager.ListJobs(context.Background(), filter)
		if err != nil {
			fmt.Fprintf(errOut, "failed to list jobs: %v\n", err)
			return 1
		}
		if jsonOut {
			writeJSONCLI(out, map[string]any{"items": items}, pretty)
			return 0
		}
		if len(items) == 0 {
			fmt.Fprintln(out, "No jobs")
			return 0
		}
		for _, item := range items {
			fmt.Fprintf(out, "%s | %s | %s | %s | %s\n", item.JobID, item.Status, item.Route, item.Assignee, item.Title)
		}
		return 0
	case "show":
		if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
			fmt.Fprintln(errOut, "usage: picoclaw jobs show <job_id>")
			return 1
		}
		jobID := strings.TrimSpace(args[1])
		j, err := manager.GetJob(context.Background(), jobID)
		if err != nil {
			fmt.Fprintf(errOut, "failed to get job: %v\n", err)
			return 1
		}
		shared, err := manager.GetContext(context.Background(), jobID)
		if err != nil && !errors.Is(err, jobmanager.ErrNotFound) {
			fmt.Fprintf(errOut, "failed to get job context: %v\n", err)
			return 1
		}
		writeJSONCLI(out, map[string]any{"job": j, "context": shared}, pretty)
		return 0
	case "create":
		draft, shared, jsonOut, err := parseJobsCreateArgs(args[1:])
		if err != nil {
			fmt.Fprintf(errOut, "%v\n", err)
			return 1
		}
		j, err := manager.CreateJob(context.Background(), draft, shared)
		if err != nil {
			fmt.Fprintf(errOut, "failed to create job: %v\n", err)
			return 1
		}
		if jsonOut {
			writeJSONCLI(out, map[string]any{"job": j}, pretty)
			return 0
		}
		fmt.Fprintf(out, "created %s\n", j.JobID)
		return 0
	case "start":
		if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
			fmt.Fprintln(errOut, "usage: picoclaw jobs start <job_id>")
			return 1
		}
		j, err := manager.StartJob(context.Background(), strings.TrimSpace(args[1]))
		if err != nil {
			fmt.Fprintf(errOut, "failed to start job: %v\n", err)
			return 1
		}
		writeJSONCLI(out, map[string]any{"job": j}, pretty)
		return 0
	case "status":
		if len(args) < 3 || strings.TrimSpace(args[1]) == "" || strings.TrimSpace(args[2]) == "" {
			fmt.Fprintln(errOut, "usage: picoclaw jobs status <job_id> <status> [--summary text]")
			return 1
		}
		summary := stringFlag(args[3:], "--summary")
		j, err := manager.UpdateStatus(context.Background(), strings.TrimSpace(args[1]), domainjob.Status(strings.TrimSpace(args[2])), summary, nil)
		if err != nil {
			fmt.Fprintf(errOut, "failed to update job: %v\n", err)
			return 1
		}
		writeJSONCLI(out, map[string]any{"job": j}, pretty)
		return 0
	case "cancel":
		if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
			fmt.Fprintln(errOut, "usage: picoclaw jobs cancel <job_id> [--summary text]")
			return 1
		}
		j, err := manager.CancelJob(context.Background(), strings.TrimSpace(args[1]), stringFlag(args[2:], "--summary"))
		if err != nil {
			fmt.Fprintf(errOut, "failed to cancel job: %v\n", err)
			return 1
		}
		writeJSONCLI(out, map[string]any{"job": j}, pretty)
		return 0
	case "notifications":
		limit, jsonOut := parseNotificationsArgs(args[1:])
		items, err := manager.ListNotifications(context.Background(), limit, true)
		if err != nil {
			fmt.Fprintf(errOut, "failed to list notifications: %v\n", err)
			return 1
		}
		if jsonOut {
			writeJSONCLI(out, map[string]any{"items": items}, pretty)
			return 0
		}
		if len(items) == 0 {
			fmt.Fprintln(out, "No job notifications")
			return 0
		}
		for _, item := range items {
			fmt.Fprintf(out, "%s | %s | %s | %s\n", item.JobID, item.Level, item.Status, item.Title)
		}
		return 0
	default:
		fmt.Fprintf(errOut, "unknown jobs subcommand: %s\n", subcmd)
		fmt.Fprintln(errOut, "usage: picoclaw jobs [list|show|create|start|status|cancel|notifications]")
		return 1
	}
}

func parseJobsListArgs(args []string) (domainjob.Filter, bool, error) {
	filter := domainjob.Filter{Limit: 20}
	jsonOut := false
	for i := 0; i < len(args); i++ {
		v := strings.TrimSpace(args[i])
		switch strings.ToLower(v) {
		case "--json":
			jsonOut = true
		case "--status":
			if i+1 >= len(args) {
				return filter, false, fmt.Errorf("--status requires a value")
			}
			filter.Status = domainjob.Status(strings.TrimSpace(args[i+1]))
			i++
		case "--module", "--module-id":
			if i+1 >= len(args) {
				return filter, false, fmt.Errorf("%s requires a value", v)
			}
			filter.ModuleID = strings.TrimSpace(args[i+1])
			i++
		case "--assignee":
			if i+1 >= len(args) {
				return filter, false, fmt.Errorf("--assignee requires a value")
			}
			filter.Assignee = strings.TrimSpace(args[i+1])
			i++
		case "--route":
			if i+1 >= len(args) {
				return filter, false, fmt.Errorf("--route requires a value")
			}
			filter.Route = domainjob.Route(strings.ToUpper(strings.TrimSpace(args[i+1])))
			i++
		default:
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				filter.Limit = n
			}
		}
	}
	return filter, jsonOut, nil
}

func parseJobsCreateArgs(args []string) (domainjob.Job, domainjob.SharedRoleContext, bool, error) {
	var draft domainjob.Job
	var shared domainjob.SharedRoleContext
	jsonOut := false
	for i := 0; i < len(args); i++ {
		v := strings.TrimSpace(args[i])
		switch strings.ToLower(v) {
		case "--json":
			jsonOut = true
		case "--title":
			if i+1 >= len(args) {
				return draft, shared, false, fmt.Errorf("--title requires a value")
			}
			draft.Title = strings.TrimSpace(args[i+1])
			shared.UserIntent = draft.Title
			i++
		case "--module", "--module-id":
			if i+1 >= len(args) {
				return draft, shared, false, fmt.Errorf("%s requires a value", v)
			}
			draft.ModuleID = strings.TrimSpace(args[i+1])
			shared.ModuleID = draft.ModuleID
			i++
		case "--module-root":
			if i+1 >= len(args) {
				return draft, shared, false, fmt.Errorf("--module-root requires a value")
			}
			draft.ModuleRoot = strings.TrimSpace(args[i+1])
			shared.ModuleRoot = draft.ModuleRoot
			i++
		case "--route":
			if i+1 >= len(args) {
				return draft, shared, false, fmt.Errorf("--route requires a value")
			}
			draft.Route = domainjob.Route(strings.ToUpper(strings.TrimSpace(args[i+1])))
			i++
		case "--assignee":
			if i+1 >= len(args) {
				return draft, shared, false, fmt.Errorf("--assignee requires a value")
			}
			draft.Assignee = strings.TrimSpace(args[i+1])
			i++
		case "--created-by":
			if i+1 >= len(args) {
				return draft, shared, false, fmt.Errorf("--created-by requires a value")
			}
			draft.CreatedBy = strings.TrimSpace(args[i+1])
			i++
		case "--priority":
			if i+1 >= len(args) {
				return draft, shared, false, fmt.Errorf("--priority requires a value")
			}
			draft.Priority = domainjob.Priority(strings.ToLower(strings.TrimSpace(args[i+1])))
			i++
		case "--read-only":
			draft.ReadOnly = true
		case "--plan":
			if i+1 >= len(args) {
				return draft, shared, false, fmt.Errorf("--plan requires a value")
			}
			shared.CurrentPlan = strings.TrimSpace(args[i+1])
			i++
		}
	}
	if strings.TrimSpace(draft.Title) == "" {
		return draft, shared, false, fmt.Errorf("--title is required")
	}
	return draft, shared, jsonOut, nil
}

func parseNotificationsArgs(args []string) (int, bool) {
	limit := 20
	jsonOut := false
	for _, a := range args {
		v := strings.TrimSpace(a)
		if strings.EqualFold(v, "--json") {
			jsonOut = true
			continue
		}
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	return limit, jsonOut
}

func stringFlag(args []string, name string) string {
	for i := 0; i < len(args)-1; i++ {
		if strings.EqualFold(strings.TrimSpace(args[i]), name) {
			return strings.TrimSpace(args[i+1])
		}
	}
	return ""
}

func loadJobManager(configPath string) (*jobmanager.Manager, error) {
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return nil, err
	}
	store, err := jobpersistence.NewJSONLStore(defaultParallelJobStorePath(cfg.WorkspaceDir))
	if err != nil {
		return nil, err
	}
	return jobmanager.New(store, jobmanager.DefaultParallelLimits()), nil
}

func defaultParallelJobStorePath(workspaceDir string) string {
	if strings.TrimSpace(workspaceDir) == "" {
		return ""
	}
	return filepath.Join(workspaceDir, "jobs")
}
