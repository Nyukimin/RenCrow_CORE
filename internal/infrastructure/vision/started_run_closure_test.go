package vision

import (
	"context"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager/taskmanagertest"
	domainvision "github.com/Nyukimin/RenCrow_CORE/internal/domain/vision"
)

// closureAnalyzer is an Analyzer that ends with a fixed error.
type closureAnalyzer struct{ err error }

func (a closureAnalyzer) Analyze(context.Context, domainvision.AnalyzeRequest) (domainvision.AnalyzeResult, error) {
	return domainvision.AnalyzeResult{}, a.err
}

func (closureAnalyzer) Health(context.Context) (domainvision.HealthReport, error) {
	return domainvision.HealthReport{}, nil
}

// Failure knowledge: docs/調査/*Run開始後の早期失敗でRunを閉じない_FailureKnowledge.md.
func TestActionAnalyzerClosesTheRunItStarted(t *testing.T) {
	taskmanagertest.VerifyStartedRunClosure(t, taskmanagertest.StartedRunPath{
		BindsReceiptOwner: true,
		WorkFailure:       taskmanagertest.WorkFailureReturnsInjected,
		Start: func(t *testing.T, c *taskmanagertest.Closure, workErr error) func(context.Context) error {
			analyzer, err := NewActionAnalyzer(closureAnalyzer{err: workErr}, c.Actions, c.Tasks, c.NewTransport(t), "mio")
			if err != nil {
				t.Fatal(err)
			}
			return func(ctx context.Context) error {
				_, err := analyzer.Analyze(ctx, domainvision.AnalyzeRequest{
					RequestID: "request", SessionID: "session", Kind: "image",
					Filename: "image.png", ContentType: "image/png", Data: []byte("image"),
				})
				return err
			}
		},
	})
}
