package middleware

import (
	"context"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager/taskmanagertest"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

// Failure knowledge: docs/調査/*Run開始後の早期失敗でRunを閉じない_FailureKnowledge.md.
func TestActionProviderClosesTheRunItStarted(t *testing.T) {
	taskmanagertest.VerifyStartedRunClosure(t, taskmanagertest.StartedRunPath{
		BindsReceiptOwner: true,
		WorkFailure:       taskmanagertest.WorkFailureReturnsInjected,
		Start: func(t *testing.T, c *taskmanagertest.Closure, workErr error) func(context.Context) error {
			provider, err := WithActionExecutionOwners(&actionTestProvider{err: workErr}, c.Actions, c.Tasks, c.NewTransport(t), "mio")
			if err != nil {
				t.Fatal(err)
			}
			return func(ctx context.Context) error {
				_, err := provider.Generate(ctx, llm.GenerateRequest{})
				return err
			}
		},
	})
}
