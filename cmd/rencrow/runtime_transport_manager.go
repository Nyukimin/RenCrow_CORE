package main

import (
	"fmt"
	"path/filepath"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/transportmanager"
	transportpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/transport"
)

func newRuntimeTransportManager(
	workspaceDir string,
	actions *actionmanager.Manager,
	queues ...*persistencequeue.Queue[persistencequeue.PersistenceCommand],
) (*transportmanager.Manager, error) {
	if len(queues) > 1 {
		return nil, fmt.Errorf("Transport owner accepts at most one persistence queue")
	}
	var queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]
	if len(queues) == 1 {
		queue = queues[0]
		if queue == nil {
			return nil, persistencequeue.ErrNilPersistenceQueue
		}
	}
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
	if queue != nil {
		if err := store.SetPersistenceQueue(queue); err != nil {
			return nil, fmt.Errorf("bind Transport owner to shared persistence queue: %w", err)
		}
	}
	return transportmanager.New(store, actions), nil
}
