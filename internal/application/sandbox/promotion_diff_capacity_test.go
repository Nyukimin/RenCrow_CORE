package sandbox

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager/taskmanagertest"
	domainsandbox "github.com/Nyukimin/RenCrow_CORE/internal/domain/sandbox"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
)

// A refused patch-apply admission must not leave a Run-less queued Task behind.
func TestPromotionDiffApplierLeavesNothingWhenExecutionCapacityIsUnavailable(t *testing.T) {
	saturated := taskmanagertest.NewSaturated(t)
	actionStore, err := actionpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatal(err)
	}
	applier := NewPromotionDiffApplier(t.TempDir(), t.TempDir()).
		WithExecutionOwners(actionmanager.New(actionStore), saturated.Manager, "shiro")

	_, err = applier.ApplyPromotionDiff(context.Background(), domainsandbox.PromotionApplyRequest{
		Promotion: domainsandbox.PromotionRequest{DiffPath: "diff.patch"},
	})
	if !errors.Is(err, taskmanager.ErrParallelLimit) {
		t.Fatalf("ApplyPromotionDiff error=%v, want ErrParallelLimit", err)
	}
	saturated.AssertNothingPersisted(t)
}
