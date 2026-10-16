package knowledge

import (
	"context"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

// 要約は短い構造化JSONを返す仕事で、thinkingは不要である。Workerは役割として
// thinkingを許すため、要求側で止めないと思考が出力予算（4096 token）を使い切って
// finish_reason=length で失敗し、同じWorkerを使う他の呼び出し（会話の要約など）を
// 長く塞ぐ。Strataでは think:false だけでは足りず、chat_template_kwargs の
// enable_thinking も false にしたときだけ思考が止まる（実測: 出力 375〜1,246 → 108〜127 token）。
func TestExternalLinkSummaryRequestsDisableThinkingForEveryCall(t *testing.T) {
	provider := &summaryTestProvider{fn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
		// 1回目は存在しない根拠IDを返して、根拠の修復呼び出し（2回目）も発生させる。
		if len(req.Messages) == 1 {
			return llm.GenerateResponse{Content: `{"summary":"本文の要点","evidence_ids":[99]}`}, nil
		}
		return llm.GenerateResponse{Content: `{"summary":"本文の要点","evidence_ids":[1]}`}, nil
	}}

	if _, err := NewExternalLinkSummarizer(provider, "shiro").SummarizeBody(context.Background(), "https://example.com/article", strings.Repeat("本文SOURCE ", 30)); err != nil {
		t.Fatalf("SummarizeBody failed: %v", err)
	}

	if provider.callCount() != 2 {
		t.Fatalf("provider calls = %d, want the first call and the evidence repair call", provider.callCount())
	}
	for index, req := range provider.requests {
		if think, ok := req.ProviderOptions["think"].(bool); !ok || think {
			t.Fatalf("request %d think option = %#v, want false", index, req.ProviderOptions["think"])
		}
		kwargs, _ := req.ProviderOptions["chat_template_kwargs"].(map[string]any)
		if enabled, ok := kwargs["enable_thinking"].(bool); !ok || enabled {
			t.Fatalf("request %d chat_template_kwargs = %#v, want enable_thinking=false", index, req.ProviderOptions["chat_template_kwargs"])
		}
	}
}
