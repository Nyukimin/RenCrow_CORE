package main

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

type llmBusySnapshot struct {
	Active          bool           `json:"active"`
	ActiveCount     int            `json:"active_count"`
	Sources         map[string]int `json:"sources,omitempty"`
	External        bool           `json:"external"`
	ExternalCount   int            `json:"external_count"`
	ExternalSources map[string]int `json:"external_sources,omitempty"`
}

var errTrackedProviderNoToolCalling = errors.New("tracked provider inner does not support tool calling")

type llmBusyTracker struct {
	mu      sync.Mutex
	sources map[string]int
}

func newLLMBusyTracker() *llmBusyTracker {
	return &llmBusyTracker{sources: map[string]int{}}
}

func (t *llmBusyTracker) Begin(ctx context.Context, fallbackSource string) func() {
	if t == nil {
		return func() {}
	}
	source := strings.TrimSpace(llm.BusySourceFromContext(ctx))
	if source == "" {
		source = strings.TrimSpace(fallbackSource)
	}
	if source == "" {
		source = "unknown"
	}
	t.mu.Lock()
	if t.sources == nil {
		t.sources = map[string]int{}
	}
	t.sources[source]++
	t.mu.Unlock()
	return func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.sources[source] <= 1 {
			delete(t.sources, source)
			return
		}
		t.sources[source]--
	}
}

func (t *llmBusyTracker) Snapshot() llmBusySnapshot {
	if t == nil {
		return llmBusySnapshot{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	all := copyBusySources(t.sources)
	external := map[string]int{}
	activeCount := 0
	externalCount := 0
	for source, count := range all {
		if count <= 0 {
			continue
		}
		activeCount += count
		if source == "idlechat" {
			continue
		}
		external[source] = count
		externalCount += count
	}
	return llmBusySnapshot{
		Active:          activeCount > 0,
		ActiveCount:     activeCount,
		Sources:         sortedBusySources(all),
		External:        externalCount > 0,
		ExternalCount:   externalCount,
		ExternalSources: sortedBusySources(external),
	}
}

func (t *llmBusyTracker) ExternalBusy() bool {
	return t.Snapshot().External
}

func copyBusySources(in map[string]int) map[string]int {
	out := map[string]int{}
	for k, v := range in {
		if strings.TrimSpace(k) == "" || v <= 0 {
			continue
		}
		out[k] = v
	}
	return out
}

func sortedBusySources(in map[string]int) map[string]int {
	if len(in) == 0 {
		return nil
	}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]int, len(keys))
	for _, k := range keys {
		out[k] = in[k]
	}
	return out
}

type trackedLLMProvider struct {
	source  string
	inner   llm.LLMProvider
	tracker *llmBusyTracker
}

func trackLLMProvider(source string, inner llm.LLMProvider, tracker *llmBusyTracker) llm.LLMProvider {
	if inner == nil || tracker == nil {
		return inner
	}
	return trackedLLMProvider{source: source, inner: inner, tracker: tracker}
}

func (p trackedLLMProvider) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	done := p.tracker.Begin(ctx, p.source)
	defer done()
	return p.inner.Generate(ctx, req)
}

func (p trackedLLMProvider) Name() string {
	return p.inner.Name()
}

func (p trackedLLMProvider) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	toolProvider, ok := p.inner.(llm.ToolCallingProvider)
	if !ok {
		return llm.ChatResponse{}, errTrackedProviderNoToolCalling
	}
	done := p.tracker.Begin(ctx, p.source)
	defer done()
	return toolProvider.Chat(ctx, req)
}
