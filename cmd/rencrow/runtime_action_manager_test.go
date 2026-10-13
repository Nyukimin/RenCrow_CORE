package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// With the snapshot, a process holds one copy of the Action store's content per
// store. The runtime asks for the Action owner in two places (the lifecycle owner
// and the Tool runtime's policy runner); two handles would each hold a copy and
// each would rebuild its copy from the logs after every commit of the other.
func TestRuntimeActionManagerIsSharedPerStoreWhenTheSnapshotIsOn(t *testing.T) {
	workspace := t.TempDir()
	first, err := newRuntimeActionManager(workspace, actionpersistence.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := newRuntimeActionManager(workspace, actionpersistence.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("two requests for the Action owner of one workspace returned two owners; they would hold two snapshots")
	}
	other, err := newRuntimeActionManager(t.TempDir(), actionpersistence.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	if other == first {
		t.Fatal("two workspaces share one Action owner")
	}
	// A path spelled differently is still the same store.
	respelled, err := newRuntimeActionManager(filepath.Join(workspace, ".", "state", "..", ""), actionpersistence.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	if respelled != first {
		t.Fatal("the same store reached through another spelling of its path got another owner")
	}
}

// With the snapshot off the runtime is what it has always been: every request
// opens the store.
func TestRuntimeActionManagerIsNotSharedWhenTheSnapshotIsOff(t *testing.T) {
	workspace := t.TempDir()
	first, err := newRuntimeActionManager(workspace, actionpersistence.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := newRuntimeActionManager(workspace, actionpersistence.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("with the snapshot off the runtime must open the store for every request, as it always did")
	}
}

func TestRuntimeActionManagerWorksWithTheSnapshotOn(t *testing.T) {
	ctx := context.Background()
	manager, err := newRuntimeActionManager(t.TempDir(), actionpersistence.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	action, attempt, err := manager.CreateAction(ctx, actionmanager.CreateInput{TaskID: modulecore.NewTaskID(), RunID: modulecore.NewRunID(), Kind: domainaction.KindTool, Name: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.CompleteAttempt(ctx, action.ActionID, attempt.AttemptID, domainaction.AttemptStatusSucceeded, domainaction.StatusSucceeded, "ok"); err != nil {
		t.Fatal(err)
	}
	got, err := manager.GetAction(ctx, action.ActionID)
	if err != nil || got.Status != domainaction.StatusSucceeded {
		t.Fatalf("GetAction = %+v, %v", got, err)
	}
}

// With rotation on, the first open moves the workspace's Action store to
// segments, and the owner works across the move.
func TestRuntimeActionManagerMovesTheStoreToSegmentsWhenRotationIsOn(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	root := filepath.Join(workspace, "state", "actions")
	t.Cleanup(func() { makeActionStoreRemovable(t, root) })
	cfg := config.ActionStoreConfig{Index: config.ActionStoreIndexConfig{Enabled: true}, Rotation: config.ActionStoreRotationConfig{Enabled: true}}
	manager, err := newRuntimeActionManager(workspace, actionStoreOptions(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "LAYOUT")); err != nil {
		t.Fatalf("the store was not moved to segments: %v", err)
	}
	action, attempt, err := manager.CreateAction(ctx, actionmanager.CreateInput{TaskID: modulecore.NewTaskID(), RunID: modulecore.NewRunID(), Kind: domainaction.KindTool, Name: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.CompleteAttempt(ctx, action.ActionID, attempt.AttemptID, domainaction.AttemptStatusSucceeded, domainaction.StatusSucceeded, "ok"); err != nil {
		t.Fatal(err)
	}
}

func TestActionStoreOptionsFollowTheConfiguration(t *testing.T) {
	zero := actionStoreOptions(config.ActionStoreConfig{})
	if zero.Index || zero.Rotation || zero.RotationSizeValve != 0 {
		t.Fatalf("the default configuration opens the store with %+v", zero)
	}
	on := actionStoreOptions(config.ActionStoreConfig{
		Index:    config.ActionStoreIndexConfig{Enabled: true},
		Rotation: config.ActionStoreRotationConfig{Enabled: true, SizeValveBytes: 4 << 20},
	})
	if !on.Index || !on.Rotation || on.RotationSizeValve != 4<<20 {
		t.Fatalf("options = %+v", on)
	}
}
