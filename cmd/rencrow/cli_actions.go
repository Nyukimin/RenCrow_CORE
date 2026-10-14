package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// The storage-level subcommands of `rencrow actions`. They work on the Action
// store files, not through the Action Manager, and none of them writes except
// `layout concat --apply`:
//
//	verify [--check-runs] [--compact]
//	        check the in-memory snapshot against the full read of the logs (read
//	        only): the snapshot is built the way a store opened with
//	        action_store.index.enabled builds it, the logs are read in full the way
//	        a store without it reads them for every transaction, and the two must
//	        hold the same records, field by field. On a store with segments it reads
//	        every segment and holds each closed one to the size recorded when it
//	        was closed. The report has counts and sizes, never a record or an ID.
//	        It holds the store's OS lock for the whole check; on a running
//	        production store run it on a copy. --check-runs also asks the Task
//	        store (read only) whether a Task owns every Run the Actions name,
//	        which a start no longer checks for the Actions it has nothing to settle
//	        for.
//	layout show [--compact]
//	        what layout the store is on (plain files, or segments) and what it is
//	        made of. Takes no lock and writes nothing.
//	layout concat [--apply] [--compact]
//	        return a store with segments to the plain layout, while CORE is
//	        stopped. Without --apply it only says what it would do.

func cmdActions() {
	// A request for help is answered before the config file or any store is opened.
	if status, done := actionsPreflight(os.Args[2:], os.Stdout); done {
		if status != 0 {
			os.Exit(status)
		}
		return
	}
	cfg, err := config.LoadConfig(getConfigPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}
	if code := runActionsCommand(os.Args[2:], defaultActionStorePath(cfg.WorkspaceDir), defaultTaskStorePath(cfg.WorkspaceDir), os.Stdout, os.Stderr); code != 0 {
		os.Exit(code)
	}
}

// defaultActionStorePath is where the runtime keeps the Action store.
func defaultActionStorePath(workspaceDir string) string {
	if strings.TrimSpace(workspaceDir) == "" {
		return ""
	}
	return filepath.Join(workspaceDir, "state", "actions")
}

// runActionsCommand runs one subcommand against the Action store at actionRoot
// (and, for --check-runs, the Task store at taskRoot) and returns the exit
// status. Output is JSON on out; the reason for a failure goes to errOut.
func runActionsCommand(args []string, actionRoot, taskRoot string, out io.Writer, errOut io.Writer) int {
	const usage = actionsUsageSummary
	if status, done := actionsPreflight(args, out); done {
		return status
	}
	if len(args) == 0 {
		fmt.Fprintln(errOut, usage)
		return 1
	}
	sub := strings.ToLower(strings.TrimSpace(args[0]))
	if sub != "verify" && sub != "layout" {
		fmt.Fprintf(errOut, "unknown actions subcommand: %s\n%s\n", args[0], usage)
		return 1
	}
	if sub == "layout" {
		if strings.TrimSpace(actionRoot) == "" {
			fmt.Fprintln(errOut, "the action store location is unknown: workspace_dir is not configured")
			return 1
		}
		return runActionLayoutCommand(context.Background(), args[1:], actionRoot, out, errOut)
	}
	pretty, checkRuns := true, false
	for _, arg := range args[1:] {
		switch strings.ToLower(strings.TrimSpace(arg)) {
		case "--compact":
			pretty = false
		case "--check-runs":
			checkRuns = true
		default:
			fmt.Fprintf(errOut, "unexpected argument %q\n%s\n", arg, usage)
			return 1
		}
	}
	if strings.TrimSpace(actionRoot) == "" {
		fmt.Fprintln(errOut, "the action store location is unknown: workspace_dir is not configured")
		return 1
	}
	options := actionpersistence.VerifyOptions{}
	if checkRuns {
		if strings.TrimSpace(taskRoot) == "" {
			fmt.Fprintln(errOut, "--check-runs needs the task store location: workspace_dir is not configured")
			return 1
		}
		reader, err := taskpersistence.NewIndexedJSONLReader(taskRoot)
		if err != nil {
			fmt.Fprintf(errOut, "failed to open the task store: %v\n", err)
			return 1
		}
		defer reader.Close()
		options.CheckRuns = func(ctx context.Context, runs []modulecore.RunID) (int, error) {
			missing := 0
			for _, run := range runs {
				if _, err := reader.GetRun(ctx, run); err != nil {
					if !errors.Is(err, domaintask.ErrNotFound) {
						return 0, err
					}
					missing++
				}
			}
			return missing, nil
		}
	}
	report, err := actionpersistence.VerifyActionStoreWithOptions(context.Background(), actionRoot, options)
	if err != nil {
		fmt.Fprintf(errOut, "failed to verify the action store: %v\n", err)
		return 1
	}
	writeJSONCLI(out, report, pretty)
	if !report.OK {
		fmt.Fprintf(errOut, "action store verification failed: %d mismatches\n", report.Mismatches)
		return 1
	}
	return 0
}

// runActionLayoutCommand runs `rencrow actions layout <show|concat>`.
func runActionLayoutCommand(ctx context.Context, args []string, root string, out io.Writer, errOut io.Writer) int {
	const usage = actionsUsageLayout
	if len(args) == 0 {
		fmt.Fprintln(errOut, usage)
		return 1
	}
	sub := strings.ToLower(strings.TrimSpace(args[0]))
	pretty, apply := true, false
	for _, arg := range args[1:] {
		switch {
		case arg == "--compact":
			pretty = false
		case arg == "--apply" && sub == "concat":
			apply = true
		default:
			fmt.Fprintf(errOut, "unexpected argument %q\n%s\n", arg, usage)
			return 1
		}
	}
	switch sub {
	case "show":
		report, err := actionpersistence.InspectActionLayout(root)
		if err != nil {
			fmt.Fprintf(errOut, "failed to inspect the action store layout: %v\n", err)
			return 1
		}
		writeJSONCLI(out, report, pretty)
		return 0
	case "concat":
		report, err := actionpersistence.ConcatActionLayout(ctx, root, apply)
		if err != nil {
			if errors.Is(err, actionpersistence.ErrStoreBusy) {
				fmt.Fprintf(errOut, "failed to concatenate the action store layout: %v (stop the running CORE first)\n", err)
			} else {
				fmt.Fprintf(errOut, "failed to concatenate the action store layout: %v\n", err)
			}
			writeJSONCLI(out, report, pretty)
			return 1
		}
		writeJSONCLI(out, report, pretty)
		return 0
	}
	fmt.Fprintf(errOut, "unknown layout subcommand: %s\n%s\n", args[0], usage)
	return 1
}
