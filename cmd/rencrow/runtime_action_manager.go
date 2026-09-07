package main

import (
	"fmt"
	"path/filepath"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
)

func newRuntimeActionManager(workspaceDir string) (*actionmanager.Manager, error) {
	if workspaceDir == "" {
		return nil, fmt.Errorf("workspace dir is required")
	}
	store, err := actionpersistence.NewJSONLStore(filepath.Join(workspaceDir, "state", "actions"))
	if err != nil {
		return nil, err
	}
	return actionmanager.New(store), nil
}
