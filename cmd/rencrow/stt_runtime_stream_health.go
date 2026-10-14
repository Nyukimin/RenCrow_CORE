package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	sttinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/stt"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	modulestt "github.com/Nyukimin/RenCrow_CORE/modules/stt"
)

const (
	defaultSTTStreamHealthTTL     = 10 * time.Second
	defaultSTTStreamHealthTimeout = 2 * time.Second
	// STT's health answer is a few hundred bytes.
	maxSTTStreamHealthBody = 64 * 1024
)

// sttStreamHealthConfig configures the report of the STT audio stream.
type sttStreamHealthConfig struct {
	// BaseURL is the RenCrow_STT Gateway's HTTP base URL; /health is read from it.
	BaseURL string
	// TTL is how long an answer is reused (default 10s).
	TTL time.Duration
	// Timeout bounds one request to the Gateway (default 2s).
	Timeout time.Duration
	// Now replaces the clock of the cache.
	Now func() time.Time
	// RecorderStats adds the counters of the utterance recorder to the report, so
	// a lost record is visible next to the stream it belongs to.
	RecorderStats func() sttinfra.RecorderStats
}

// sttStreamHealthProvider reports "stt.stream" in /viewer/modules/health: whether
// a /stt connection can be relayed right now. It reads the Gateway's /health,
// reuses the answer for a while, and shares one request among concurrent
// callers. The request runs under its own context, so a caller that gives up
// (the health handler has a 3 second budget) neither cancels it nor wastes its
// answer. /stt itself never consults it: a connection simply tries to open its
// stream and reports the failure.
type sttStreamHealthProvider struct {
	cfg    sttStreamHealthConfig
	client *http.Client

	mu       sync.Mutex
	cached   *modulestt.StreamProbeResult
	cachedAt time.Time
	inflight chan struct{}
}

func newSTTStreamHealthProvider(cfg sttStreamHealthConfig) *sttStreamHealthProvider {
	if cfg.TTL <= 0 {
		cfg.TTL = defaultSTTStreamHealthTTL
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultSTTStreamHealthTimeout
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &sttStreamHealthProvider{cfg: cfg, client: &http.Client{}}
}

// Health returns the report. It never fails the caller: whatever cannot be
// confirmed is reported as unreachable.
func (p *sttStreamHealthProvider) Health(ctx context.Context) modulecore.HealthReport {
	result := p.probe(ctx)
	report := modulestt.BuildStreamHealthReport(result)
	if p.cfg.RecorderStats != nil {
		stats := p.cfg.RecorderStats()
		if report.Metadata == nil {
			report.Metadata = map[string]any{}
		}
		report.Metadata["recorder_recorded"] = stats.Recorded
		report.Metadata["recorder_dropped"] = stats.Dropped
		report.Metadata["recorder_failed"] = stats.Failed
		report.Metadata["recorder_missing_target_called"] = stats.MissingTargetCalled
	}
	return report
}

func (p *sttStreamHealthProvider) probe(ctx context.Context) modulestt.StreamProbeResult {
	p.mu.Lock()
	if p.cached != nil && p.cfg.Now().Sub(p.cachedAt) < p.cfg.TTL {
		result := *p.cached
		p.mu.Unlock()
		return result
	}
	wait := p.inflight
	if wait == nil {
		wait = make(chan struct{})
		p.inflight = wait
		go p.fetch(wait)
	}
	p.mu.Unlock()

	select {
	case <-wait:
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.cached != nil {
			return *p.cached
		}
		return modulestt.StreamProbeResult{Err: context.Canceled}
	case <-ctx.Done():
		return modulestt.StreamProbeResult{Err: ctx.Err()}
	}
}

func (p *sttStreamHealthProvider) fetch(done chan struct{}) {
	result := p.request()
	p.mu.Lock()
	p.cached, p.cachedAt = &result, p.cfg.Now()
	p.inflight = nil
	p.mu.Unlock()
	close(done)
}

func (p *sttStreamHealthProvider) request() modulestt.StreamProbeResult {
	ctx, cancel := context.WithTimeout(context.Background(), p.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.cfg.BaseURL, "/")+"/health", nil)
	if err != nil {
		return modulestt.StreamProbeResult{Err: err}
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return modulestt.StreamProbeResult{Err: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSTTStreamHealthBody))
	if err != nil {
		return modulestt.StreamProbeResult{Err: err}
	}
	health, err := modulestt.ParseStreamHealth(body)
	return modulestt.StreamProbeResult{Health: health, Err: err}
}

// sttStreamUnavailableHealth is the report when stt.enabled is off.
type sttStreamUnavailableHealth struct{}

func (sttStreamUnavailableHealth) Health(context.Context) modulecore.HealthReport {
	return modulestt.BuildUnavailableProviderHealth()
}
