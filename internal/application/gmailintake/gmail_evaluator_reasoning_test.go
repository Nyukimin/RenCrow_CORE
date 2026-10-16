package gmailintake

import (
	"context"
	"testing"

	domainllm "github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

type gmailEvaluatorCapturingStub struct {
	responses []domainllm.GenerateResponse
	requests  []domainllm.GenerateRequest
	contexts  []context.Context
}

func (s *gmailEvaluatorCapturingStub) Name() string { return "gmail-evaluator-capture" }

func (s *gmailEvaluatorCapturingStub) Generate(ctx context.Context, request domainllm.GenerateRequest) (domainllm.GenerateResponse, error) {
	s.requests = append(s.requests, request)
	s.contexts = append(s.contexts, ctx)
	return s.responses[len(s.requests)-1], nil
}

// Gmailの話題抽出と検証は、判断を伴う意味評価である。thinkingを止めずにlowにする
// （実測: 既定と同じ判断で2〜3倍速く、既定では出力予算4096に近づく）。ProviderOptionsは
// 引き続き空にする（このpassがモデル出力から外部効果を作れないようにする既存の制約）。
// 呼び出し元を記録しないとGatewayではcore.unattributedになり、遅さの原因を特定できない。
func TestGmailBriefEvaluatorRequestsUseLowReasoningAndAreAttributed(t *testing.T) {
	const sourceURL = "https://example.com/ai"
	const quote = "Official AI source says the route is useful for RenCrow."
	provider := &gmailEvaluatorCapturingStub{responses: []domainllm.GenerateResponse{
		{Content: gmailEvaluatorExtraction("ai", sourceURL)},
		{Content: gmailEvaluatorAssessment("include", quote)},
	}}
	fetcher := gmailEvaluatorFetcher(sourceURL, " Intro. "+quote+" More details. \n")

	_, err := NewGmailBriefEvaluator(provider, fetcher).EvaluateBrief(gmailEvaluatorContext(t), gmailEvaluatorMessage("Read this source: "+sourceURL+"."), []string{"title: CORE LLM route\npurpose: preserve the canonical owner"})
	if err != nil {
		t.Fatal(err)
	}

	wantPurposes := []string{"extract_topics", "assess_topic"}
	if len(provider.requests) != len(wantPurposes) {
		t.Fatalf("LLM calls = %d, want %d", len(provider.requests), len(wantPurposes))
	}
	for index, request := range provider.requests {
		if request.ReasoningEffort != domainllm.ReasoningEffortLow {
			t.Errorf("request %d reasoning effort = %q, want %q", index, request.ReasoningEffort, domainllm.ReasoningEffortLow)
		}
		if request.ProviderOptions != nil || request.OnToken != nil || request.OnMetrics != nil {
			t.Errorf("request %d must keep provider options and callbacks empty: %+v", index, request)
		}
		observation, ok := domainllm.ExecutionObservationFromContext(provider.contexts[index])
		if !ok || observation.Initiator != "shiro" || observation.Caller != "gmail.daily_brief" || observation.Purpose != wantPurposes[index] {
			t.Errorf("request %d observation = %+v ok=%v, want shiro / gmail.daily_brief / %s", index, observation, ok, wantPurposes[index])
		}
	}
}
