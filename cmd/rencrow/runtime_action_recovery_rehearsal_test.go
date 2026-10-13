package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
)

// TestRehearseRestartRecoveryOnACopyOfTheWorkspace times what a start does with
// the Action store: opening the Task store and the Action store (without and with
// the snapshot) and the two restart recoveries, on a copy of a real workspace.
//
// It is skipped unless ACTIDX_REHEARSE_WORKSPACE names a directory that holds
// tasks/ and state/actions/ (a copy of a workspace, never the live one). It copies
// both into a temporary directory first, opens the Task store the way the runtime
// does with the index, the sidecar and the rotation on, and runs the recoveries on
// that copy, so the copy is changed the way a restart would change it: the Runs
// the copied writer left running are recovered. Run it with -v; one line per mode
// is logged.
func TestRehearseRestartRecoveryOnACopyOfTheWorkspace(t *testing.T) {
	source := os.Getenv("ACTIDX_REHEARSE_WORKSPACE")
	if source == "" {
		t.Skip("set ACTIDX_REHEARSE_WORKSPACE to a copy of a workspace (tasks/ and state/actions/) to rehearse a restart on it")
	}
	for _, mode := range actionStoreModes {
		t.Run(mode.name, func(t *testing.T) {
			ctx := context.Background()
			workspace := filepath.Join(t.TempDir(), "workspace")
			copyTreeForRehearsal(t, filepath.Join(source, "tasks"), filepath.Join(workspace, "tasks"))
			copyTreeForRehearsal(t, filepath.Join(source, "state", "actions"), filepath.Join(workspace, "state", "actions"))

			deps := &Dependencies{taskStoreIndex: true, taskStorePersist: true, taskStoreRotation: true}
			started := time.Now()
			if err := initializeRuntimeTaskOwnerWithCriteriaRevision(deps, workspace, ""); err != nil {
				t.Fatalf("open the Task store: %v", err)
			}
			taskOpen := time.Since(started)
			t.Cleanup(func() { _ = deps.taskStore.(interface{ Close() error }).Close() })

			started = time.Now()
			actions, err := newRuntimeActionManager(workspace, actionpersistence.OpenOptions{Index: mode.index})
			if err != nil {
				t.Fatalf("open the Action store: %v", err)
			}
			actionOpen := time.Since(started)

			started = time.Now()
			recoveredActionRuns, err := recoverActionRunsAfterRestart(ctx, actions, deps.taskManager)
			if err != nil {
				t.Fatalf("recoverActionRunsAfterRestart: %v", err)
			}
			actionRecovery := time.Since(started)

			started = time.Now()
			recoveredOrphans, err := recoverOrphanTaskRunsAfterRestart(ctx, actions, deps.taskManager)
			if err != nil {
				t.Fatalf("recoverOrphanTaskRunsAfterRestart: %v", err)
			}
			orphanRecovery := time.Since(started)

			t.Logf("REHEARSAL mode=%s task_store_open=%v action_store_open=%v action_run_recovery=%v (recovered %d) orphan_run_recovery=%v (recovered %d) action_total=%v",
				mode.name, taskOpen.Round(time.Millisecond), actionOpen.Round(time.Millisecond),
				actionRecovery.Round(time.Millisecond), recoveredActionRuns, orphanRecovery.Round(time.Millisecond), recoveredOrphans,
				(actionOpen + actionRecovery + orphanRecovery).Round(time.Millisecond))
		})
	}
}

// copyTreeForRehearsal copies a directory tree with the files' modes.
func copyTreeForRehearsal(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		target := filepath.Join(to, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			_ = out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
		return os.Chmod(target, info.Mode().Perm())
	})
	if err != nil {
		t.Fatal(err)
	}
}
