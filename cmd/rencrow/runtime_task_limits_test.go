package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestTaskManagerCLIKeepsDefaultCodingAdmissionWhenLimitsAreOmitted(t *testing.T) {
	workspace := t.TempDir()
	manager := loadTaskManagerWithLimits(t, workspace, nil)
	for _, moduleID := range []string{"module-a", "module-b"} {
		startCodingTask(t, manager, moduleID, filepath.Join(workspace, moduleID))
	}
	_, _, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{
		Title: "default third coding task", Route: domaintask.RouteCode,
		ModuleID: "module-c", ModuleRoot: filepath.Join(workspace, "module-c"),
	}, domaintask.SharedRoleContext{})
	if !errors.Is(err, taskmanager.ErrParallelLimit) {
		t.Fatalf("third coding task error = %v, want default coding limit refusal", err)
	}
	assertRunningCodingTaskCount(t, manager, 2)
}

func TestTaskManagerCLIUsesLoadedQuadLimitsAndPreservesModuleWriteExclusion(t *testing.T) {
	workspace := t.TempDir()
	manager := loadTaskManagerWithLimits(t, workspace, map[string]any{
		"global": 4, "per_module": 1, "coding_tasks": 4, "long_research_tasks": 1, "destructive_tasks": 1,
	})
	started := make(map[string]modulecore.TaskID, 4)
	for _, moduleID := range []string{"module-a", "module-b", "module-c", "module-d"} {
		started[moduleID] = startCodingTask(t, manager, moduleID, filepath.Join(workspace, moduleID))
	}
	assertRunningCodingTaskCount(t, manager, 4)

	_, _, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{
		Title: "fifth coding task", Route: domaintask.RouteCode,
		ModuleID: "module-e", ModuleRoot: filepath.Join(workspace, "module-e"),
	}, domaintask.SharedRoleContext{})
	if !errors.Is(err, taskmanager.ErrParallelLimit) {
		t.Fatalf("fifth coding task error = %v, want global limit refusal", err)
	}
	assertRunningCodingTaskCount(t, manager, 4)

	if _, err := manager.Succeed(context.Background(), started["module-a"], "test release"); err != nil {
		t.Fatalf("release one module slot: %v", err)
	}
	_, _, err = manager.CreateAndStartRun(context.Background(), domaintask.Task{
		Title: "nested root write", Route: domaintask.RouteCode,
		ModuleID: "module-b-child", ModuleRoot: filepath.Join(workspace, "module-b", "nested"),
	}, domaintask.SharedRoleContext{})
	if !errors.Is(err, taskmanager.ErrParallelLimit) {
		t.Fatalf("nested same-module write error = %v, want PerModule refusal", err)
	}
	assertRunningCodingTaskCount(t, manager, 3)
}

func TestRuntimeTaskOwnerUsesLoadedLimitsWithBothConstructors(t *testing.T) {
	for _, tc := range []struct {
		name             string
		expectedCriteria string
	}{
		{"ordinary", ""},
		{"criteria-pinned", strings.Repeat("a", 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			configPath := writeTaskManagerConfig(t, workspace, map[string]any{
				"global": 4, "per_module": 1, "coding_tasks": 4, "long_research_tasks": 1, "destructive_tasks": 1,
			})
			cfg, err := config.LoadConfig(configPath)
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			limits := taskParallelLimitsFromConfig(cfg)
			deps := &Dependencies{taskParallelLimits: &limits}
			if tc.expectedCriteria == "" {
				err = initializeRuntimeTaskOwner(deps, cfg.WorkspaceDir)
			} else {
				err = initializeRuntimeTaskOwnerWithCriteriaRevision(deps, cfg.WorkspaceDir, tc.expectedCriteria)
			}
			if err != nil {
				t.Fatalf("initialize runtime Task owner: %v", err)
			}
			t.Cleanup(func() {
				if err := deps.taskManager.Close(); err != nil {
					t.Errorf("close runtime Task manager: %v", err)
				}
			})
			for _, moduleID := range []string{"module-a", "module-b", "module-c", "module-d"} {
				startCodingTask(t, deps.taskManager, moduleID, filepath.Join(workspace, moduleID))
			}
			_, _, err = deps.taskManager.CreateAndStartRun(context.Background(), domaintask.Task{
				Title: "fifth runtime coding task", Route: domaintask.RouteCode,
				ModuleID: "module-e", ModuleRoot: filepath.Join(workspace, "module-e"),
			}, domaintask.SharedRoleContext{})
			if !errors.Is(err, taskmanager.ErrParallelLimit) {
				t.Fatalf("fifth runtime coding task error = %v, want ErrParallelLimit", err)
			}
			assertRunningCodingTaskCount(t, deps.taskManager, 4)
		})
	}
}

func loadTaskManagerWithLimits(t *testing.T, workspace string, limits map[string]any) *taskmanager.Manager {
	t.Helper()
	configPath := writeTaskManagerConfig(t, workspace, limits)
	manager, err := loadTaskManager(configPath, []string{"create"})
	if err != nil {
		t.Fatalf("load Task manager from config: %v", err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close Task manager: %v", err)
		}
	})
	return manager
}

func writeTaskManagerConfig(t *testing.T, workspace string, limits map[string]any) string {
	t.Helper()
	root := map[string]any{
		"workspace_dir": workspace,
		"server":        map[string]any{"port": 18790},
	}
	if limits != nil {
		root["task_parallel_limits"] = limits
	}
	data, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func startCodingTask(t *testing.T, manager *taskmanager.Manager, moduleID, moduleRoot string) modulecore.TaskID {
	t.Helper()
	task, _, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{
		Title: "coding task " + moduleID, Route: domaintask.RouteCode, ModuleID: moduleID, ModuleRoot: moduleRoot,
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("start coding task for %s: %v", moduleID, err)
	}
	return task.TaskID
}

func assertRunningCodingTaskCount(t *testing.T, manager *taskmanager.Manager, want int) {
	t.Helper()
	tasks, err := manager.List(context.Background(), domaintask.Filter{Status: domaintask.StatusRunning, Route: domaintask.RouteCode, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != want {
		t.Fatalf("running coding Task count = %d, want %d", len(tasks), want)
	}
}
