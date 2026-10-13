package main

import (
	"fmt"
	"path/filepath"
	"sync"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
)

// sharedActionManagers holds, per store, the Action owner of a process that runs
// with the snapshot on.
var sharedActionManagers = struct {
	sync.Mutex
	byRoot map[string]*actionmanager.Manager
}{byRoot: map[string]*actionmanager.Manager{}}

// newRuntimeActionManager opens the Action owner of the workspace.
//
// With index false it opens the store for every request, as it always did. With
// index true (action_store.index.enabled) a process opens a given store once and
// every request gets the same owner: the store keeps a decoded copy of its logs
// in memory, so a second handle on the same logs would hold a second copy and
// each commit of one would make the other rebuild its copy from the files. The
// runtime asks for the Action owner in two places (the lifecycle owner and the
// Tool runtime's policy runner), so this is the case that happens, not a
// precaution. Delete the flag-off branch together with the full-read path.
func newRuntimeActionManager(workspaceDir string, index bool) (*actionmanager.Manager, error) {
	if workspaceDir == "" {
		return nil, fmt.Errorf("workspace dir is required")
	}
	root := filepath.Join(workspaceDir, "state", "actions")
	if !index {
		return openRuntimeActionManager(root, false)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve action store root: %w", err)
	}
	sharedActionManagers.Lock()
	defer sharedActionManagers.Unlock()
	if manager, ok := sharedActionManagers.byRoot[abs]; ok {
		return manager, nil
	}
	manager, err := openRuntimeActionManager(abs, true)
	if err != nil {
		return nil, err
	}
	sharedActionManagers.byRoot[abs] = manager
	return manager, nil
}

func openRuntimeActionManager(root string, index bool) (*actionmanager.Manager, error) {
	store, err := actionpersistence.NewJSONLStoreWithOptions(root, actionpersistence.OpenOptions{Index: index})
	if err != nil {
		return nil, err
	}
	return actionmanager.New(store), nil
}
