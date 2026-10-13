package stt

import (
	"context"
	"sync"
	"testing"
	"time"
)

// Finals must never be superseded by default: three concurrent requests all
// reach the engine, and the engine (not CORE) decides the order.
func TestDefaultBusyPolicyDeliversEveryConcurrentRequest(t *testing.T) {
	base := &blockingProvider{release: make(chan struct{})}
	provider := NewBusyPolicyProvider(base, "")

	var wg sync.WaitGroup
	results := make(chan string, 3)
	errs := make(chan error, 3)
	for _, name := range []string{"first", "second", "third"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			result, err := provider.Transcribe(context.Background(), namedWAV(name))
			if err != nil {
				errs <- err
				return
			}
			results <- result.Text
		}(name)
	}
	base.waitCalls(t, 3)
	close(base.release)
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Fatalf("a request was dropped or failed under the default policy: %v", err)
	}
	got := map[string]bool{}
	for text := range results {
		got[text] = true
	}
	if len(got) != 3 || !got["first"] || !got["second"] || !got["third"] {
		t.Fatalf("results = %v, want all three requests answered", got)
	}
}

func TestExplicitQueueLatestStillSupersedes(t *testing.T) {
	plan := NewBusyPolicyProvider(&blockingProvider{release: make(chan struct{})}, BusyPolicyQueueLatest)
	if _, ok := plan.(*busyPolicyProvider); !ok {
		t.Fatalf("provider type = %T", plan)
	}
	if plan.(*busyPolicyProvider).policy != BusyPolicyQueueLatest {
		t.Fatalf("explicit queue_latest must be honoured, got %q", plan.(*busyPolicyProvider).policy)
	}
}

func TestGatewayProviderTimeoutFallbackCoversLongUtterances(t *testing.T) {
	if got := (GatewayProvider{}).requestTimeout(); got < time.Minute {
		t.Fatalf("requestTimeout() = %s, want at least a minute for a 30 s utterance", got)
	}
	if got := (GatewayProvider{Timeout: 5 * time.Second}).requestTimeout(); got != 5*time.Second {
		t.Fatalf("explicit timeout must win, got %s", got)
	}
}
