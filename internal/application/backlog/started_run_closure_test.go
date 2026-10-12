package backlog

import (
	"context"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager/taskmanagertest"
)

// Failure knowledge: docs/調査/*Run開始後の早期失敗でRunを閉じない_FailureKnowledge.md.
func TestBeginActionClosesTheRunItStarted(t *testing.T) {
	taskmanagertest.VerifyStartedRunClosure(t, taskmanagertest.StartedRunPath{
		WorkFailure: taskmanagertest.WorkFailureReturnsInjected,
		Start: func(t *testing.T, c *taskmanagertest.Closure, workErr error) func(context.Context) error {
			service := NewService(nil, nil).WithExecutionOwners(c.Actions, c.Tasks, "shiro")
			return func(ctx context.Context) error {
				_, finish, err := service.beginAction(ctx, "atlas_recover")
				if err != nil {
					return err
				}
				return finishAction(workErr, finish)
			}
		},
	})
}
