package main

import (
	"fmt"
	"path/filepath"
	"sync"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
)

// runtimeActionOwner is the Action owner of the workspace and the store it
// writes through. The store is kept so that /health can report it; everything
// that changes Actions goes through the manager.
type runtimeActionOwner struct {
	manager          *actionmanager.Manager
	store            *actionpersistence.JSONLStore
	persistenceQueue *persistencequeue.Queue[persistencequeue.PersistenceCommand]
}

// sharedActionManagers holds, per store, the Action owner of a process that runs
// with the snapshot on.
var sharedActionManagers = struct {
	sync.Mutex
	byRoot map[string]runtimeActionOwner
}{byRoot: map[string]runtimeActionOwner{}}

// actionStoreOptions is how the configuration opens the Action store.
func actionStoreOptions(cfg config.ActionStoreConfig) actionpersistence.OpenOptions {
	return actionpersistence.OpenOptions{
		Index:             cfg.Index.Enabled,
		Rotation:          cfg.Rotation.Enabled,
		RotationSizeValve: cfg.Rotation.SizeValveBytes,
	}
}

// newRuntimeActionManager opens the Action owner of the workspace and returns its
// manager. See newRuntimeActionOwner.
func newRuntimeActionManager(workspaceDir string, opts actionpersistence.OpenOptions, queues ...*persistencequeue.Queue[persistencequeue.PersistenceCommand]) (*actionmanager.Manager, error) {
	owner, err := newRuntimeActionOwner(workspaceDir, opts, queues...)
	if err != nil {
		return nil, err
	}
	return owner.manager, nil
}

// newRuntimeActionOwner opens the Action owner of the workspace.
//
// Without the snapshot (opts.Index false) it opens the store for every request, as
// it always did. With it (action_store.index.enabled) a process opens a given
// store once and every request gets the same owner: the store keeps a decoded
// copy of its logs in memory, so a second handle on the same logs would hold a
// second copy and each commit of one would make the other rebuild its copy from
// the files. The runtime asks for the Action owner in two places (the lifecycle
// owner and the Tool runtime's policy runner), so this is the case that happens,
// not a precaution. Delete the flag-off branch together with the full-read path.
func newRuntimeActionOwner(workspaceDir string, opts actionpersistence.OpenOptions, queues ...*persistencequeue.Queue[persistencequeue.PersistenceCommand]) (runtimeActionOwner, error) {
	if len(queues) > 1 {
		return runtimeActionOwner{}, fmt.Errorf("Action owner accepts at most one persistence queue")
	}
	var queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]
	if len(queues) == 1 {
		queue = queues[0]
		if queue == nil {
			return runtimeActionOwner{}, persistencequeue.ErrNilPersistenceQueue
		}
	}
	if workspaceDir == "" {
		return runtimeActionOwner{}, fmt.Errorf("workspace dir is required")
	}
	root := filepath.Join(workspaceDir, "state", "actions")
	if !opts.Index {
		return openRuntimeActionOwner(root, opts)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return runtimeActionOwner{}, fmt.Errorf("resolve action store root: %w", err)
	}
	sharedActionManagers.Lock()
	defer sharedActionManagers.Unlock()
	if owner, ok := sharedActionManagers.byRoot[abs]; ok {
		if queue != nil && owner.persistenceQueue != queue {
			return runtimeActionOwner{}, fmt.Errorf("Action owner for %s is already bound to a different persistence queue", abs)
		}
		return owner, nil
	}
	var owner runtimeActionOwner
	if queue == nil {
		owner, err = openRuntimeActionOwner(abs, opts)
	} else {
		owner, err = openRuntimeActionOwner(abs, opts, queue)
	}
	if err != nil {
		return runtimeActionOwner{}, err
	}
	sharedActionManagers.byRoot[abs] = owner
	return owner, nil
}

func openRuntimeActionOwner(root string, opts actionpersistence.OpenOptions, queues ...*persistencequeue.Queue[persistencequeue.PersistenceCommand]) (runtimeActionOwner, error) {
	if len(queues) > 1 {
		return runtimeActionOwner{}, fmt.Errorf("Action owner accepts at most one persistence queue")
	}
	var queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]
	if len(queues) == 1 {
		queue = queues[0]
		if queue == nil {
			return runtimeActionOwner{}, persistencequeue.ErrNilPersistenceQueue
		}
	}
	store, err := actionpersistence.NewJSONLStoreWithOptions(root, opts)
	if err != nil {
		return runtimeActionOwner{}, err
	}
	if queue != nil && opts.Index {
		if err := store.SetPersistenceQueue(queue); err != nil {
			return runtimeActionOwner{}, fmt.Errorf("bind Action owner to shared persistence queue: %w", err)
		}
	}
	return runtimeActionOwner{manager: actionmanager.New(store), store: store, persistenceQueue: queue}, nil
}
