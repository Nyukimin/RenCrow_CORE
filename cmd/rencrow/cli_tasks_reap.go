package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
)

// `rencrow tasks reap-queued` cancels, once, the Run-less queued Tasks that old
// creation paths left in the Task store (see the taskmanager reaper for the
// selection rules and the guarantees).
//
//	rencrow tasks reap-queued --created-before <RFC3339> [--min-age 24h]
//	                          [--batch-size 2000] [--apply] [--compact]
//
// Without --apply it only reports what it would cancel, through a read-only
// handle: it writes nothing. With --apply it opens the store as the writer, so
// the running CORE must be stopped first (ErrTaskWriterBusy otherwise), and
// cancels the candidates in batches of one owner transaction each.
//
// --created-before is required and has no default: a Task created at or after it
// is never selected. The output is one JSON document of counts, digests and
// operation IDs; no title, context or Task ID is printed. Exit status:
//
//	0  candidates were found (dry-run) or handled (apply); also --help / -h,
//	   which prints the usage and reads and writes nothing
//	1  failure (the JSON "error" is a class; the reason is on stderr)
//	2  invalid command line
//	3  no candidates
//
// The store is read and written through the in-memory ID index even when
// task_store.index is off in the configuration: without it every SaveTask of a
// batch would fold the whole history. The index is derived from the log and is
// never persisted by this command, so the sidecar under index/ is left as it
// is; the next CORE start restores it and replays the tail.

const (
	reapExitOK           = 0
	reapExitFailure      = 1
	reapExitUsage        = 2
	reapExitNoCandidates = 3
)

func isTasksReapSubcommand(args []string) bool {
	clean := removeTaskFlag(args, "--compact")
	return len(clean) > 0 && strings.EqualFold(strings.TrimSpace(clean[0]), "reap-queued")
}

func cmdTasksReap() {
	cfg, err := config.LoadConfig(getConfigPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(reapExitFailure)
	}
	if code := runTasksReapCommand(os.Args[2:], defaultTaskStorePath(cfg.WorkspaceDir), os.Stdout, os.Stderr); code != reapExitOK {
		os.Exit(code)
	}
}

type tasksReapStoreInfo struct {
	Writer      bool   `json:"writer"`
	Index       bool   `json:"index"`
	IndexSource string `json:"index_source,omitempty"`
	IndexTasks  int    `json:"index_tasks,omitempty"`
}

type tasksReapOutput struct {
	// Status is "ok", "no_candidates" or "failed".
	Status string `json:"status"`
	// Error is a class (writer_busy, store_open_failed, plan_failed,
	// batch_failed, commit_uncertain, unsupported_store), never free text.
	Error  string                  `json:"error,omitempty"`
	Store  *tasksReapStoreInfo     `json:"store,omitempty"`
	Report *taskmanager.ReapReport `json:"report,omitempty"`
}

type tasksReapOptions struct {
	request taskmanager.ReapRequest
	pretty  bool
}

func parseTasksReapArgs(args []string) (tasksReapOptions, error) {
	options := tasksReapOptions{pretty: !hasFlag(args, "--compact")}
	args = removeTaskFlag(args, "--compact")
	flags := flag.NewFlagSet("tasks reap-queued", flag.ContinueOnError)
	flags.SetOutput(io.Discard) // the caller prints the error once, with the usage line
	createdBefore := flags.String("created-before", "", "required: only Tasks created before this RFC3339 time (with a zone) are selected")
	minAge := flags.Duration("min-age", taskmanager.DefaultReapMinAge, "a selected Task must be at least this old")
	batchSize := flags.Int("batch-size", taskmanager.DefaultReapBatchSize, fmt.Sprintf("Tasks cancelled by one transaction (1..%d)", taskmanager.MaxReapBatchSize))
	apply := flags.Bool("apply", false, "cancel the candidates (default: report only)")
	if err := flags.Parse(args[1:]); err != nil {
		return options, err
	}
	if flags.NArg() > 0 {
		return options, fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	if strings.TrimSpace(*createdBefore) == "" {
		return options, errors.New("--created-before is required (RFC3339 with a zone, for example 2026-01-02T03:04:05Z): a reap has no default cutoff")
	}
	cutoff, err := time.Parse(time.RFC3339, strings.TrimSpace(*createdBefore))
	if err != nil {
		return options, fmt.Errorf("--created-before must be RFC3339 with a zone: %w", err)
	}
	options.request = taskmanager.ReapRequest{CreatedBefore: cutoff, MinAge: *minAge, BatchSize: *batchSize, Apply: *apply}
	return options, options.request.Validate()
}

// runTasksReapCommand runs the command against the store at root and returns the
// exit status. The JSON document goes to out; reasons go to errOut.
func runTasksReapCommand(args []string, root string, out io.Writer, errOut io.Writer) int {
	options, err := parseTasksReapArgs(args)
	if errors.Is(err, flag.ErrHelp) {
		// -h, -help and --help are a request for the usage, not a bad command line.
		fmt.Fprintln(out, tasksUsageReap)
		return reapExitOK
	}
	if err != nil {
		fmt.Fprintln(errOut, err)
		fmt.Fprintln(errOut, tasksUsageReap)
		return reapExitUsage
	}
	fail := func(class string, info *tasksReapStoreInfo, report *taskmanager.ReapReport, format string, a ...any) int {
		fmt.Fprintf(errOut, format+"\n", a...)
		writeJSONCLI(out, tasksReapOutput{Status: "failed", Error: class, Store: info, Report: report}, options.pretty)
		return reapExitFailure
	}
	if strings.TrimSpace(root) == "" {
		return fail("store_open_failed", nil, nil, "the task store location is unknown: workspace_dir is not configured")
	}

	request := options.request
	var store *taskpersistence.JSONLStore
	if request.Apply {
		store, err = taskpersistence.NewJSONLStoreWithOptions(root, taskpersistence.OpenOptions{Index: true})
	} else {
		store, err = taskpersistence.NewIndexedJSONLReader(root)
	}
	if err != nil {
		if errors.Is(err, taskpersistence.ErrTaskWriterBusy) {
			return fail("writer_busy", nil, nil, "failed to open the task store: %v (stop the running CORE first)", err)
		}
		return fail("store_open_failed", nil, nil, "failed to open the task store: %v", err)
	}
	manager := taskmanager.New(store, taskmanager.DefaultParallelLimits())
	defer manager.Close()

	info := &tasksReapStoreInfo{Writer: request.Apply}
	if stats, ok := store.IndexStats(); ok {
		info.Index, info.IndexSource, info.IndexTasks = true, stats.Source, stats.Tasks
	}
	report, err := manager.ReapStaleQueued(context.Background(), request)
	if err != nil {
		class := "plan_failed"
		switch {
		case errors.Is(err, taskmanager.ErrReapUnsupportedStore):
			class = "unsupported_store"
		case errors.Is(err, jsonlbatch.ErrCommitUncertain):
			class = "commit_uncertain"
		case len(report.Batches) > 0 || report.Mode == "apply":
			class = "batch_failed"
		}
		var partial *taskmanager.ReapReport
		if report.Mode != "" {
			partial = &report
		}
		hint := ""
		if class == "commit_uncertain" {
			hint = " (the outcome of the last batch is unknown: run the command again, it selects again and cancels each Task once)"
		}
		return fail(class, info, partial, "reap-queued failed after %d batch(es): %v%s", len(report.Batches), err, hint)
	}
	status, code := "ok", reapExitOK
	if report.Candidates == 0 {
		status, code = "no_candidates", reapExitNoCandidates
	}
	writeJSONCLI(out, tasksReapOutput{Status: status, Store: info, Report: &report}, options.pretty)
	return code
}
