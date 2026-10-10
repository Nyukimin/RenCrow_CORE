package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
)

// The storage-level subcommands of `rencrow tasks`. They work on the Task store
// files and the index sidecar, not through the Task Manager, so none of them
// opens the store as a writer:
//
//	history <task_id>   every stored line of one Task (all versions, its Runs,
//	                    context and notifications), verbatim. The records are
//	                    printed as they are stored: treat the output as record content.
//	verify              check the index and the log against each other (read only).
//	                    It holds the store's read lock for the whole check; on a
//	                    running production store run it on a copy.
//	rebuild-index       rebuild the index sidecar from the log while CORE is stopped
//	                    (ErrTaskWriterBusy otherwise). The log is not modified.

func isTaskStorageSubcommand(args []string) bool {
	clean := removeTaskFlag(args, "--compact")
	if len(clean) == 0 {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(clean[0])) {
	case "history", "verify", "rebuild-index":
		return true
	}
	return false
}

func cmdTasksStorage() {
	cfg, err := config.LoadConfig(getConfigPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}
	if code := runTaskStorageCommand(os.Args[2:], defaultTaskStorePath(cfg.WorkspaceDir), os.Stdout, os.Stderr); code != 0 {
		os.Exit(code)
	}
}

// runTaskStorageCommand runs one storage-level subcommand against the store at
// root and returns the exit status. Output is JSON, bounded, on out; the reason
// for a failure goes to errOut.
func runTaskStorageCommand(args []string, root string, out io.Writer, errOut io.Writer) int {
	pretty := !hasFlag(args, "--compact")
	args = removeTaskFlag(args, "--compact")
	if len(args) == 0 {
		fmt.Fprintln(errOut, "usage: rencrow tasks [history <task_id>|verify|rebuild-index]")
		return 1
	}
	if strings.TrimSpace(root) == "" {
		fmt.Fprintln(errOut, "the task store location is unknown: workspace_dir is not configured")
		return 1
	}
	ctx := context.Background()
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "verify":
		report, err := taskpersistence.VerifyTaskStore(ctx, root)
		if err != nil {
			fmt.Fprintf(errOut, "failed to verify the task store: %v\n", err)
			return 1
		}
		writeJSONCLI(out, report, pretty)
		if !report.OK {
			fmt.Fprintf(errOut, "task store verification failed: %d mismatches\n", report.Mismatches)
			return 1
		}
		return 0
	case "rebuild-index":
		report, err := taskpersistence.RebuildTaskIndex(ctx, root)
		if err != nil {
			if errors.Is(err, taskpersistence.ErrTaskWriterBusy) {
				fmt.Fprintf(errOut, "failed to rebuild the index: %v (stop the running CORE first)\n", err)
			} else {
				fmt.Fprintf(errOut, "failed to rebuild the index: %v\n", err)
			}
			return 1
		}
		writeJSONCLI(out, report, pretty)
		return 0
	case "history":
		taskID, err := parseTaskIDArg(args, 1, "usage: rencrow tasks history <task_id>")
		if err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		reader, err := taskpersistence.NewIndexedJSONLReader(root)
		if err != nil {
			fmt.Fprintf(errOut, "failed to open the task store: %v\n", err)
			return 1
		}
		defer reader.Close()
		lines, err := reader.TaskHistory(ctx, taskID)
		if err != nil {
			if errors.Is(err, domaintask.ErrNotFound) {
				fmt.Fprintf(errOut, "task %s was not found\n", taskID)
			} else {
				fmt.Fprintf(errOut, "failed to read the task history: %v\n", err)
			}
			return 1
		}
		writeJSONCLI(out, map[string]any{"task_id": taskID, "lines": lines}, pretty)
		return 0
	}
	fmt.Fprintf(errOut, "unknown tasks subcommand: %s\n", args[0])
	return 1
}
