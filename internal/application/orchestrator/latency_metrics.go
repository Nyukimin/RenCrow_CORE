package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

type latencyTraceContextKey struct{}

type latencyTrace struct {
	startedAt time.Time

	mu               sync.Mutex
	firstTokenMarked bool
}

type latencyMetricPayload struct {
	Kind             string  `json:"kind"`
	Point            string  `json:"point"`
	ElapsedMS        float64 `json:"elapsed_ms,omitempty"`
	SinceMS          float64 `json:"since_ms,omitempty"`
	AtUnixMS         int64   `json:"at_unix_ms"`
	Detail           string  `json:"detail,omitempty"`
	CompletionTokens int     `json:"completion_tokens,omitempty"`
	TokensPerSecond  float64 `json:"tokens_per_second,omitempty"`
}

func contextWithLatencyTrace(ctx context.Context, startedAt time.Time) context.Context {
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	return context.WithValue(ctx, latencyTraceContextKey{}, &latencyTrace{startedAt: startedAt})
}

func latencyTraceFromContext(ctx context.Context) *latencyTrace {
	trace, _ := ctx.Value(latencyTraceContextKey{}).(*latencyTrace)
	return trace
}

func (t *latencyTrace) markFirstToken() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.firstTokenMarked {
		return false
	}
	t.firstTokenMarked = true
	return true
}

func emitLatencyMetric(
	emit messageEventEmitter,
	kind, point string,
	startedAt time.Time,
	route, jobID, sessionID, channel, chatID, detail string,
) {
	if emit == nil {
		return
	}
	now := time.Now()
	payload := latencyMetricPayload{
		Kind:     kind,
		Point:    point,
		AtUnixMS: now.UnixMilli(),
		Detail:   detail,
	}
	if !startedAt.IsZero() {
		payload.ElapsedMS = float64(now.Sub(startedAt).Microseconds()) / 1000.0
	}
	content, err := json.Marshal(payload)
	if err != nil {
		content = []byte(fmt.Sprintf(`{"kind":%q,"point":%q,"at_unix_ms":%d,"detail":"marshal_failed"}`, kind, point, now.UnixMilli()))
	}
	emit("metrics.latency", "metrics", "viewer", string(content), route, jobID, sessionID, channel, chatID)
}

func emitLLMThroughputMetric(
	emit messageEventEmitter,
	metrics llm.GenerationMetrics,
	route, jobID, sessionID, channel, chatID string,
) {
	if emit == nil || (metrics.CompletionTokens <= 0 && metrics.TokensPerSecond <= 0) {
		return
	}
	payload := latencyMetricPayload{
		Kind:             "llm",
		Point:            "throughput",
		AtUnixMS:         time.Now().UnixMilli(),
		CompletionTokens: metrics.CompletionTokens,
		TokensPerSecond:  metrics.TokensPerSecond,
	}
	content, err := json.Marshal(payload)
	if err != nil {
		return
	}
	emit("metrics.latency", "metrics", "viewer", string(content), route, jobID, sessionID, channel, chatID)
}
