package main

import (
	"fmt"
	"path/filepath"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/transportmanager"
	transportpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/transport"
)

func newRuntimeTransportManager(workspaceDir string, actions *actionmanager.Manager) (*transportmanager.Manager, error) {
	if workspaceDir == "" {
		return nil, fmt.Errorf("workspace dir is required")
	}
	if actions == nil {
		return nil, fmt.Errorf("action owner is required")
	}
	store, err := transportpersistence.NewJSONLStore(filepath.Join(workspaceDir, "state", "transport"))
	if err != nil {
		return nil, err
	}
	return transportmanager.New(store, actions), nil
}
