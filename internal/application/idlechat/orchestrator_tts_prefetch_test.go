package idlechat

import (
	"context"
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/session"
)

type streamingIdleProvider struct {
	response string
	tokens   []string
	requests []llm.GenerateRequest
}

func (p *streamingIdleProvider) Generate(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.requests = append(p.requests, req)
	if req.OnToken != nil {
		for _, token := range p.tokens {
			req.OnToken(token)
		}
	}
	return llm.GenerateResponse{Content: p.response}, nil
}

func (p *streamingIdleProvider) Name() string { return "streaming-idle" }

func TestGenerateResponseWithRawStreamsPrimaryTokensToPrefetchEmitter(t *testing.T) {
	provider := &streamingIdleProvider{
		response: "古書店の棚の奥で、雨に濡れた封筒が一通だけ見つかる。誰かの秘密がまだ乾いていない感じがする。",
		tokens: []string{
			"古書店の棚の奥で、",
			"雨に濡れた封筒が一通だけ見つかる。",
			"誰かの秘密がまだ乾いていない感じがする。",
		},
	}
	o := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")

	var got []TTSPrefetchEvent
	o.SetTTSPrefetchEmitter(func(ev TTSPrefetchEvent) {
		got = append(got, ev)
	})

	_, raw, err := o.generateResponseWithRaw("shiro", "mio", "idle-prefetch", 1, 1, "郵便と古書店")
	if err != nil {
		t.Fatalf("generateResponseWithRaw() error = %v", err)
	}
	if raw != provider.response {
		t.Fatalf("raw response = %q, want %q", raw, provider.response)
	}
	if len(got) != len(provider.tokens) {
		t.Fatalf("prefetch event count = %d, want %d", len(got), len(provider.tokens))
	}
	for i, token := range provider.tokens {
		if got[i].SessionID != "idle-prefetch" {
			t.Fatalf("event[%d] session_id = %q, want idle-prefetch", i, got[i].SessionID)
		}
		if got[i].MessageID != "idle-prefetch:msg:0002" {
			t.Fatalf("event[%d] message_id = %q, want idle-prefetch:msg:0002", i, got[i].MessageID)
		}
		if got[i].Token != token {
			t.Fatalf("event[%d] token = %q, want %q", i, got[i].Token, token)
		}
	}
	if len(provider.requests) != 1 {
		t.Fatalf("LLM request count = %d, want 1", len(provider.requests))
	}
	if provider.requests[0].OnToken == nil {
		t.Fatal("expected primary request to wire OnToken")
	}
}
