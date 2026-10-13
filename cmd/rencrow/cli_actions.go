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
// store files, not through the Action Manager, and none of them writes:
//
//	verify [--check-runs] [--compact]
//	        check the in-memory snapshot against the full read of the logs (read
//	        only): the snapshot is built the way a store opened with
//	        action_store.index.enabled builds it, the logs are read in full the way
//	        a store without it reads them for every transaction, and the two must
//	        hold the same records, field by field. The report has counts and
//	        sizes, never a record or an ID. It holds the store's OS lock for the
//	        whole check; on a running production store run it on a copy.
//	        --check-runs also asks the Task store (read only) whether a Task owns
//	        every Run the Actions name, which a start no longer checks for the
//	        Actions it has nothing to settle for.

func cmdActions() {
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
	const usage = "usage: rencrow actions verify [--check-runs] [--compact]"
	if len(args) == 0 {
		fmt.Fprintln(errOut, usage)
		return 1
	}
	if strings.ToLower(strings.TrimSpace(args[0])) != "verify" {
		fmt.Fprintf(errOut, "unknown actions subcommand: %s\n%s\n", args[0], usage)
		return 1
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
