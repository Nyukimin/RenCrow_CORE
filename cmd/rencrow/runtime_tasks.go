package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
)

func initializeRuntimeTaskOwner(deps *Dependencies, workspace string, selectedStores ...domaintask.Store) error {
	return initializeRuntimeTaskOwnerWithCriteriaRevision(deps, workspace, "", selectedStores...)
}

func initializeRuntimeTaskOwnerWithCriteriaRevision(deps *Dependencies, workspace, expectedCriteriaRevision string, selectedStores ...domaintask.Store) error {
	if deps == nil {
		return errors.New("runtime dependencies are nil")
	}
	if len(selectedStores) > 1 {
		return errors.New("canonical Task owner accepts at most one selected store")
	}
	if deps.taskStore != nil || deps.taskManager != nil {
		return errors.New("canonical Task owner is already initialized")
	}
	var store domaintask.Store
	if len(selectedStores) == 1 {
		store = selectedStores[0]
	}
	if store == nil {
		path := strings.TrimSpace(defaultTaskStorePath(workspace))
		if path == "" {
			return errors.New("workspace directory is required for canonical Task owner")
		}
		var err error
		store, err = taskpersistence.NewJSONLStore(path)
		if err != nil {
			return fmt.Errorf("open canonical Task store: %w", err)
		}
	}
	deps.taskStore = store
	if expectedCriteriaRevision == "" {
		deps.taskManager = taskmanager.New(store, taskmanager.DefaultParallelLimits())
		return nil
	}
	manager, err := taskmanager.NewWithExpectedCriteriaRevision(store, taskmanager.DefaultParallelLimits(), expectedCriteriaRevision)
	if err != nil {
		return fmt.Errorf("configure Task owner criteria revision: %w", err)
	}
	deps.taskManager = manager
	return nil
}

// taskExecutionRunner admits a Tool call against the currently owned Task Run
// before delegating to the existing runner. It performs no identity minting or
// context rewriting, so downstream policy and Tool runners receive the exact
// context and arguments selected by the caller.
type taskExecutionRunner struct {
	owner *taskmanager.Manager
	inner domaintool.RunnerV2
}

func (r *taskExecutionRunner) ExecuteV2(ctx context.Context, toolName string, args map[string]any) (*domaintool.ToolResponse, error) {
	if r == nil {
		return nil, errors.New("task execution runner is nil")
	}
	if r.owner == nil {
		return nil, errors.New("task execution owner is nil")
	}
	if r.inner == nil {
		return nil, errors.New("task execution inner runner is nil")
	}

	if _, err := r.owner.ValidateExecutionContext(ctx); err != nil {
		return nil, fmt.Errorf("task execution admission denied: %w", err)
	}

	return r.inner.ExecuteV2(ctx, toolName, args)
}

func (r *taskExecutionRunner) ListTools(ctx context.Context) ([]domaintool.ToolMetadata, error) {
	if r == nil {
		return nil, errors.New("task execution runner is nil")
	}
	if r.inner == nil {
		return nil, errors.New("task execution inner runner is nil")
	}
	return r.inner.ListTools(ctx)
}
