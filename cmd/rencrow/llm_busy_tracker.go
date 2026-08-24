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
	mu              sync.Mutex
	sources         map[string]int
	idleLease       *llmIdleLease
	idleLeaseCancel context.CancelFunc
	idleReservation *llmIdleReservation
}

type llmIdleLease struct {
	source string
}

type llmIdleReservation struct {
	source string
}

type llmIdleLeaseContextKey struct{}

func newLLMBusyTracker() *llmBusyTracker {
	return &llmBusyTracker{sources: map[string]int{}}
}

func (t *llmBusyTracker) Begin(ctx context.Context, fallbackSource string) func() {
	done, _ := t.TryBegin(ctx, fallbackSource)
	return done
}

func (t *llmBusyTracker) TryBegin(ctx context.Context, fallbackSource string) (func(), bool) {
	if t == nil {
		return func() {}, true
	}
	source := strings.TrimSpace(llm.BusySourceFromContext(ctx))
	if source == "" {
		source = strings.TrimSpace(fallbackSource)
	}
	if source == "" {
		source = "unknown"
	}
	t.mu.Lock()
	lease, _ := ctx.Value(llmIdleLeaseContextKey{}).(*llmIdleLease)
	if t.idleReservation != nil && source == "idlechat" {
		t.mu.Unlock()
		return func() {}, false
	}
	if t.idleReservation != nil && source != "idlechat" {
		t.idleReservation = nil
	}
	if t.idleLease != nil && lease != t.idleLease {
		if source == "idlechat" {
			t.mu.Unlock()
			return func() {}, false
		}
		cancel := t.idleLeaseCancel
		t.idleLease = nil
		t.idleLeaseCancel = nil
		if cancel != nil {
			cancel()
		}
	}
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
	}, true
}

func (t *llmBusyTracker) TryReserveIdleReservation(source string) (func(), bool) {
	if t == nil {
		return func() {}, false
	}
	source = strings.TrimSpace(source)
	if source == "" {
		source = "idle_reservation"
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.idleLease != nil {
		return func() {}, false
	}
	if t.idleReservation != nil {
		if t.idleReservation.source != source {
			return func() {}, false
		}
		reservation := t.idleReservation
		return func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			if t.idleReservation == reservation {
				t.idleReservation = nil
			}
		}, true
	}
	for busySource, count := range t.sources {
		if count > 0 && busySource != "idlechat" {
			return func() {}, false
		}
	}
	reservation := &llmIdleReservation{source: source}
	t.idleReservation = reservation
	return func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.idleReservation == reservation {
			t.idleReservation = nil
		}
	}, true
}

func (t *llmBusyTracker) TryAcquireIdleLease(parent context.Context, source string) (context.Context, func(), bool) {
	return t.tryAcquireIdleLease(parent, source, false)
}

func (t *llmBusyTracker) TryAcquireReservedIdleLease(parent context.Context, source string) (context.Context, func(), bool) {
	return t.tryAcquireIdleLease(parent, source, true)
}

func (t *llmBusyTracker) IdleReservationHeld(source string) bool {
	if t == nil {
		return false
	}
	source = strings.TrimSpace(source)
	if source == "" {
		source = "idle_reservation"
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.idleReservation != nil && t.idleReservation.source == source
}

func (t *llmBusyTracker) tryAcquireIdleLease(parent context.Context, source string, requireReservation bool) (context.Context, func(), bool) {
	if t == nil {
		return parent, func() {}, false
	}
	source = strings.TrimSpace(source)
	if source == "" {
		source = "idle_lease"
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.idleLease != nil {
		return parent, func() {}, false
	}
	busySources := copyBusySources(t.sources)
	if t.idleReservation != nil {
		if t.idleReservation.source != source || len(busySources) > 0 {
			return parent, func() {}, false
		}
		t.idleReservation = nil
	} else if requireReservation || len(busySources) > 0 {
		return parent, func() {}, false
	}
	lease := &llmIdleLease{source: source}
	leaseCtx, cancel := context.WithCancel(parent)
	leaseCtx = context.WithValue(leaseCtx, llmIdleLeaseContextKey{}, lease)
	t.idleLease = lease
	t.idleLeaseCancel = cancel
	return leaseCtx, func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.idleLease != lease {
			return
		}
		t.idleLease = nil
		t.idleLeaseCancel = nil
		cancel()
	}, true
}

func (t *llmBusyTracker) Snapshot() llmBusySnapshot {
	if t == nil {
		return llmBusySnapshot{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	all := copyBusySources(t.sources)
	if t.idleLease != nil {
		all[t.idleLease.source]++
	}
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
	done, ok := p.tracker.TryBegin(ctx, p.source)
	if !ok {
		return llm.GenerateResponse{}, context.Canceled
	}
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
	done, ok := p.tracker.TryBegin(ctx, p.source)
	if !ok {
		return llm.ChatResponse{}, context.Canceled
	}
	defer done()
	return toolProvider.Chat(ctx, req)
}
