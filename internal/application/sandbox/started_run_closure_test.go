package sandbox

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager/taskmanagertest"
	domainsandbox "github.com/Nyukimin/RenCrow_CORE/internal/domain/sandbox"
)

// Failure knowledge: docs/調査/*Run開始後の早期失敗でRunを閉じない_FailureKnowledge.md.
func TestPromotionDiffApplierClosesTheRunItStarted(t *testing.T) {
	taskmanagertest.VerifyStartedRunClosure(t, taskmanagertest.StartedRunPath{
		// The patch cannot be applied when its diff is missing, which is how the
		// work fails here; the error is the applier's own, and it names the read
		// that failed (a path the sandbox guard refuses would fail earlier, with
		// another text). The applier does not take a context, so a cancellation
		// cannot reach the work.
		WorkFailure:        taskmanagertest.WorkFailureReturnsSome,
		WorkFailureText:    "read promotion diff",
		WorkIgnoresContext: true,
		Start: func(t *testing.T, c *taskmanagertest.Closure, workErr error) func(context.Context) error {
			sandboxRoot := t.TempDir()
			applyRoot := t.TempDir()
			writeFile(t, filepath.Join(applyRoot, "docs", "example.md"), "before\n")
			writeFile(t, filepath.Join(sandboxRoot, "diff.patch"), `diff --git a/docs/example.md b/docs/example.md
--- a/docs/example.md
+++ b/docs/example.md
@@ -1 +1 @@
-before
+after
`)
			diffPath := "diff.patch"
			if workErr != nil {
				diffPath = "missing.patch"
			}
			applier := NewPromotionDiffApplier(sandboxRoot, applyRoot).WithExecutionOwners(c.Actions, c.Tasks, "shiro")
			return func(ctx context.Context) error {
				_, err := applier.ApplyPromotionDiff(ctx, domainsandbox.PromotionApplyRequest{
					Promotion: domainsandbox.PromotionRequest{DiffPath: diffPath},
				})
				return err
			}
		},
	})
}
