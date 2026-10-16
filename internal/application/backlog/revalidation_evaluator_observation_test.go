package backlog

import (
	"context"
	"testing"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainllm "github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type revalidationContextStub struct {
	ctx     context.Context
	content string
}

func (s *revalidationContextStub) Name() string { return "revalidation-context-test" }
func (s *revalidationContextStub) Generate(ctx context.Context, _ domainllm.GenerateRequest) (domainllm.GenerateResponse, error) {
	s.ctx = ctx
	return domainllm.GenerateResponse{Content: s.content}, nil
}

// 呼び出し元を記録しないとGatewayではcore.unattributedになる。CORE起動のたびに約35秒
// Workerを使うこの再検証が、どの機能の呼び出しかを後から特定できるようにする。
// 上流が付けたtrace・task・sessionは上書きしない。
func TestLLMRevalidationEvaluatorAttributesItsLLMCall(t *testing.T) {
	provider := &revalidationContextStub{content: validRevalidationJSON()}
	upstream := domainllm.WithExecutionObservation(context.Background(), domainllm.ExecutionObservation{TraceID: "trace-upstream", SessionID: "session-upstream"})

	_, err := NewLLMRevalidationEvaluator(provider, "shiro").Evaluate(upstream, RevalidationEvaluationInput{
		Item: domainbacklog.Item{BacklogItemID: modulecore.BacklogItemID("subject"), Title: "subject"},
	})
	if err != nil {
		t.Fatal(err)
	}

	observation, ok := domainllm.ExecutionObservationFromContext(provider.ctx)
	if !ok || observation.Initiator != "shiro" || observation.Caller != "atlas.revalidation" || observation.Purpose != "evaluate_maturation" {
		t.Fatalf("observation = %+v ok=%v, want shiro / atlas.revalidation / evaluate_maturation", observation, ok)
	}
	if observation.TraceID != "trace-upstream" || observation.SessionID != "session-upstream" {
		t.Fatalf("upstream identity was overwritten: %+v", observation)
	}
}
