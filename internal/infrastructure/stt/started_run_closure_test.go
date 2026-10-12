package stt

import (
	"context"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager/taskmanagertest"
)

// Failure knowledge: docs/調査/*Run開始後の早期失敗でRunを閉じない_FailureKnowledge.md.
func TestActionProviderClosesTheRunItStarted(t *testing.T) {
	taskmanagertest.VerifyStartedRunClosure(t, taskmanagertest.StartedRunPath{
		BindsReceiptOwner: true,
		WorkFailure:       taskmanagertest.WorkFailureReturnsInjected,
		Start: func(t *testing.T, c *taskmanagertest.Closure, workErr error) func(context.Context) error {
			provider, err := NewActionProvider(MockProvider{Text: "text", Err: workErr}, c.Actions, c.Tasks, c.NewTransport(t), "mio")
			if err != nil {
				t.Fatal(err)
			}
			wav := make([]byte, 44)
			copy(wav[0:4], "RIFF")
			copy(wav[8:12], "WAVE")
			return func(ctx context.Context) error {
				_, err := provider.Transcribe(ctx, wav)
				return err
			}
		},
	})
}
