package xbookmarkworkflow

import (
	"context"
	"testing"

	domainllm "github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainworkflow "github.com/Nyukimin/RenCrow_CORE/internal/domain/xbookmarkworkflow"
)

// Workerのrole policyはthinkingを許す。評価の呼び出しは判断を伴うので止めず、
// プロジェクトの慣例（Atlas再検証・idlechat日次要約・toolloopと同じ）どおり
// reasoning effortをlowにする。既定のままだと思考が出力予算を使い切って
// finish_reason=lengthで失敗する（画像prompt抽出は1024 tokenの予算に対し既定で892 token使う）。
// 完全に止めると判断が変わりうる（実測: AI Tips評価がconditionalからvaluableへ変わった）。
func TestWorkflowEvaluationsUseLowReasoningEffort(t *testing.T) {
	cases := map[string]struct {
		workflow string
		content  string
	}{
		"image prompt": {
			workflow: domainworkflow.WorkflowImagePromptDraw,
			content:  `{"decision":"not_applicable","prompt":"","negative_prompt":"","prompt_source":"post","association":"unknown","reason":"生成promptではない"}`,
		},
		"ai tip": {
			workflow: domainworkflow.WorkflowAITipRenCrowEvaluation,
			content:  `{"verdict":"not_applicable","summary":"対象外","rationale":"無関係","evidence_urls":[],"improvement":"","affected_modules":[],"prerequisites":[],"cost":"low","risks":[],"validation_plan":[],"priority":"low"}`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			llm := &workflowLLMStub{content: tc.content}
			store := &workflowResultStoreStub{values: map[string]domainworkflow.Result{}}
			service := NewService(workflowSourceStub{workflowTestSource()}, store, llm, &workflowImageStub{}, &workflowBacklogStub{})

			if _, err := service.Run(context.Background(), domainworkflow.RunRequest{Workflow: tc.workflow, SourceRecordID: workflowTestSource().ID}); err != nil {
				t.Fatalf("Run failed: %v", err)
			}
			if llm.calls != 1 {
				t.Fatalf("LLM calls = %d, want 1", llm.calls)
			}
			if llm.last.ReasoningEffort != domainllm.ReasoningEffortLow {
				t.Fatalf("reasoning effort = %q, want %q", llm.last.ReasoningEffort, domainllm.ReasoningEffortLow)
			}
		})
	}
}
