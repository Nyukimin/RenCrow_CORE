package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sttinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/stt"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const readyStreamHealthBody = `{"status":"ok","target_health":{"ready":true},"stream":{"protocol":"stt.stream.v1","enabled":true,"connections":1,"max_connections":32,"pending_jobs":0,"fail_fast":false}}`

type streamHealthFixture struct {
	server   *httptest.Server
	requests atomic.Int64
	body     atomic.Value // string
	delay    atomic.Int64 // nanoseconds
}

func newStreamHealthFixture(t *testing.T, body string) *streamHealthFixture {
	t.Helper()
	f := &streamHealthFixture{}
	f.body.Store(body)
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		f.requests.Add(1)
		if d := time.Duration(f.delay.Load()); d > 0 {
			select {
			case <-time.After(d):
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(f.body.Load().(string)))
	}))
	t.Cleanup(f.server.Close)
	return f
}

func newTestStreamHealth(f *streamHealthFixture, now func() time.Time) *sttStreamHealthProvider {
	return newSTTStreamHealthProvider(sttStreamHealthConfig{BaseURL: f.server.URL, TTL: 10 * time.Second, Timeout: 2 * time.Second, Now: now})
}

func TestSTTStreamHealthReportsReadyFromTheGatewayHealth(t *testing.T) {
	f := newStreamHealthFixture(t, readyStreamHealthBody)
	report := newTestStreamHealth(f, nil).Health(context.Background())
	if report.Status != modulecore.HealthReady || !report.Ready || report.Detail != "" {
		t.Fatalf("report = %#v", report)
	}
	if report.Metadata["max_connections"] != 32 {
		t.Fatalf("metadata = %#v", report.Metadata)
	}
}

func TestSTTStreamHealthCachesForTheTTLAndThenAsksAgain(t *testing.T) {
	f := newStreamHealthFixture(t, readyStreamHealthBody)
	var mu sync.Mutex
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	provider := newTestStreamHealth(f, clock)

	provider.Health(context.Background())
	provider.Health(context.Background())
	if got := f.requests.Load(); got != 1 {
		t.Fatalf("requests within the TTL = %d, want 1", got)
	}

	mu.Lock()
	now = now.Add(11 * time.Second)
	mu.Unlock()
	f.body.Store(`{"stream":{"protocol":"stt.stream.v1","enabled":true,"fail_fast":true,"fail_fast_reason":"counted"}}`)
	report := provider.Health(context.Background())
	if f.requests.Load() != 2 {
		t.Fatalf("requests after the TTL = %d, want 2", f.requests.Load())
	}
	if report.Status != modulecore.HealthLive || report.Ready || report.Detail != "fail_fast" {
		t.Fatalf("report = %#v", report)
	}
}

func TestSTTStreamHealthAsksOnceForManyConcurrentCallers(t *testing.T) {
	f := newStreamHealthFixture(t, readyStreamHealthBody)
	f.delay.Store(int64(150 * time.Millisecond))
	provider := newTestStreamHealth(f, nil)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if report := provider.Health(context.Background()); report.Status != modulecore.HealthReady {
				t.Errorf("report = %#v", report)
			}
		}()
	}
	wg.Wait()
	if got := f.requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1 for 20 concurrent callers", got)
	}
}

func TestSTTStreamHealthTellsAnOldGatewayFromAnUnreachableOne(t *testing.T) {
	old := newStreamHealthFixture(t, `{"status":"ok","target_health":{"ready":true}}`)
	if report := newTestStreamHealth(old, nil).Health(context.Background()); report.Status != modulecore.HealthBlocked || report.Detail != "stream_not_supported" {
		t.Fatalf("an old gateway: %#v", report)
	}

	gone := newStreamHealthFixture(t, readyStreamHealthBody)
	provider := newTestStreamHealth(gone, nil)
	gone.server.Close()
	if report := provider.Health(context.Background()); report.Status != modulecore.HealthBlocked || report.Detail != "stream_unreachable" {
		t.Fatalf("an unreachable gateway: %#v", report)
	}

	html := newStreamHealthFixture(t, `<html>not a gateway</html>`)
	if report := newTestStreamHealth(html, nil).Health(context.Background()); report.Detail != "stream_unreachable" {
		t.Fatalf("an answer that is not JSON: %#v", report)
	}
}

func TestSTTStreamHealthIsBoundedAndNotPoisonedByACallerThatGaveUp(t *testing.T) {
	f := newStreamHealthFixture(t, readyStreamHealthBody)
	f.delay.Store(int64(300 * time.Millisecond))
	provider := newSTTStreamHealthProvider(sttStreamHealthConfig{BaseURL: f.server.URL, TTL: 10 * time.Second, Timeout: 2 * time.Second})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	first := provider.Health(ctx)
	if time.Since(started) > time.Second {
		t.Fatalf("Health waited %v although the caller's context ended after 50ms", time.Since(started))
	}
	if first.Detail != "stream_unreachable" {
		t.Fatalf("a caller that gave up is told the stream could not be confirmed: %#v", first)
	}

	// The request ran under its own context, so its answer is not lost.
	waitUntil(t, "the cached answer", func() bool {
		return provider.Health(context.Background()).Status == modulecore.HealthReady
	})
	if f.requests.Load() != 1 {
		t.Fatalf("requests = %d: the request of the first caller must serve the later ones", f.requests.Load())
	}
}

func TestSTTStreamHealthGivesUpOnAGatewayThatDoesNotAnswer(t *testing.T) {
	f := newStreamHealthFixture(t, readyStreamHealthBody)
	f.delay.Store(int64(5 * time.Second))
	provider := newSTTStreamHealthProvider(sttStreamHealthConfig{BaseURL: f.server.URL, TTL: 10 * time.Second, Timeout: 100 * time.Millisecond})
	started := time.Now()
	report := provider.Health(context.Background())
	if time.Since(started) > 2*time.Second || report.Detail != "stream_unreachable" {
		t.Fatalf("report = %#v after %v", report, time.Since(started))
	}
}

func TestSTTStreamHealthShowsTheRecorderCounters(t *testing.T) {
	f := newStreamHealthFixture(t, readyStreamHealthBody)
	provider := newSTTStreamHealthProvider(sttStreamHealthConfig{
		BaseURL: f.server.URL, TTL: time.Second, Timeout: time.Second,
		RecorderStats: func() sttinfra.RecorderStats {
			return sttinfra.RecorderStats{Recorded: 5, Dropped: 2, Failed: 1, MissingTargetCalled: 3}
		},
	})
	report := provider.Health(context.Background())
	if report.Metadata["recorder_recorded"] != int64(5) || report.Metadata["recorder_dropped"] != int64(2) ||
		report.Metadata["recorder_failed"] != int64(1) || report.Metadata["recorder_missing_target_called"] != int64(3) {
		t.Fatalf("metadata = %#v: a lost record must be visible to the operator", report.Metadata)
	}
}

func TestSTTStreamHealthNeverCarriesTheGatewayAddress(t *testing.T) {
	gone := newStreamHealthFixture(t, readyStreamHealthBody)
	provider := newTestStreamHealth(gone, nil)
	gone.server.Close()
	report := provider.Health(context.Background())
	for key, value := range report.Metadata {
		if text, ok := value.(string); ok && strings.Contains(text, "127.0.0.1") {
			t.Fatalf("metadata[%s] carries the gateway address: %q", key, text)
		}
	}
	if strings.Contains(report.Detail, "127.0.0.1") {
		t.Fatalf("detail carries the gateway address: %q", report.Detail)
	}
}
