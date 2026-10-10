package backlog

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager/taskmanagertest"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
)

// A refused Atlas admission must not leave a Run-less queued Task behind
// (failure knowledge: 20261011 Run無しqueued Task).
func TestBeginActionLeavesNothingWhenExecutionCapacityIsUnavailable(t *testing.T) {
	saturated := taskmanagertest.NewSaturated(t)
	actionStore, err := actionpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatalf("create Action store: %v", err)
	}
	service := NewService(nil, nil).WithExecutionOwners(actionmanager.New(actionStore), saturated.Manager, "shiro")

	for _, name := range []string{"atlas_acquire_runnable", "atlas_recover", "atlas_revise"} {
		if _, _, err := service.beginAction(context.Background(), name); !errors.Is(err, taskmanager.ErrParallelLimit) {
			t.Fatalf("beginAction(%s) error=%v, want ErrParallelLimit", name, err)
		}
	}
	saturated.AssertNothingPersisted(t)
}
