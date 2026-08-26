package idlechat

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

type canceledTopicGenerationProvider struct {
	calls atomic.Int32
}

func (p *canceledTopicGenerationProvider) Generate(ctx context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.calls.Add(1)
	return llm.GenerateResponse{}, ctx.Err()
}

func (p *canceledTopicGenerationProvider) Name() string { return "canceled-topic-provider" }

func TestGenerateInterestingTopicStopsAfterContextCancellation(t *testing.T) {
	provider := &canceledTopicGenerationProvider{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := NewTopicGenerator(provider, TopicGenerationConfig{
		CandidatesPerAttempt: 1,
		MaxAttempts:          3,
		JudgeEnabled:         false,
	}).GenerateInterestingTopic(ctx, TopicCategorySingle, TopicSeed{
		Category: TopicCategorySingle,
		Genre1:   "郵便",
	}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("GenerateInterestingTopic() error = %v, want context canceled", err)
	}
	if got := provider.calls.Load(); got != 0 {
		t.Fatalf("canceled context must not retry or start generation: calls=%d", got)
	}
}

func TestGenerateInterestingTopicStopsRetriesWhenProviderCancelsContext(t *testing.T) {
	provider := &canceledTopicGenerationProvider{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The provider cancels the request's context before returning. This
	// reproduces a cancellation observed after queue admission.
	cancelingProvider := &providerCancelsTopicContext{provider: provider, cancel: cancel}
	_, err := NewTopicGenerator(cancelingProvider, TopicGenerationConfig{
		CandidatesPerAttempt: 1,
		MaxAttempts:          3,
		JudgeEnabled:         false,
	}).GenerateInterestingTopic(ctx, TopicCategorySingle, TopicSeed{
		Category: TopicCategorySingle,
		Genre1:   "郵便",
	}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("GenerateInterestingTopic() error = %v, want context canceled", err)
	}
	if got := provider.calls.Load(); got != 1 {
		t.Fatalf("provider cancellation must stop retries after first attempt: calls=%d", got)
	}
}

type providerCancelsTopicContext struct {
	provider *canceledTopicGenerationProvider
	cancel   context.CancelFunc
}

func (p *providerCancelsTopicContext) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.cancel()
	return p.provider.Generate(ctx, req)
}

func (p *providerCancelsTopicContext) Name() string { return p.provider.Name() }
