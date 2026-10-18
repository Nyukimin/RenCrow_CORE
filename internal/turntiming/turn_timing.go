// Package turntiming records bounded, process-local timing points for one
// opt-in chat turn. It never records request content or identity values.
package turntiming

import (
	"context"
	"sync"
	"time"
)

type Point string

const (
	PointHandlerReceived            Point = "handler_received"
	PointAccepted                   Point = "accepted"
	PointAsyncStart                 Point = "async_start"
	PointRouteDecided               Point = "route_decided"
	PointDispatchStarted            Point = "dispatch_started"
	PointBeginTurnStarted           Point = "begin_turn_started"
	PointBeginTurnEnded             Point = "begin_turn_ended"
	PointUserMemoryStarted          Point = "user_memory_started"
	PointUserMemoryEnded            Point = "user_memory_ended"
	PointRecallStarted              Point = "recall_started"
	PointRecallEnded                Point = "recall_ended"
	PointCategoryStarted            Point = "category_started"
	PointCategoryEnded              Point = "category_ended"
	PointPromptPreparationStarted   Point = "prompt_preparation_started"
	PointPromptPreparationEnded     Point = "prompt_preparation_ended"
	PointGenerationProviderStart    Point = "generation_provider_started"
	PointGenerationProviderEnd      Point = "generation_provider_ended"
	PointGatewayRequestStarted      Point = "gateway_request_started"
	PointGatewayResponseHeaders     Point = "gateway_response_headers"
	PointFirstTextCallback          Point = "first_text_callback"
	PointConversationCommitStarted  Point = "conversation_commit_started"
	PointConversationCommitEnded    Point = "conversation_commit_ended"
	PointResponsePublicationStarted Point = "response_publication_started"
)

const SchemaVersion = 1

// Snapshot is the bounded public projection attached to one terminal response.
type Snapshot struct {
	SchemaVersion int                `json:"schema_version"`
	PointsMS      map[string]float64 `json:"points_ms"`
}

// Recorder stores elapsed monotonic offsets from the handler-entry origin.
type Recorder struct {
	startedAt time.Time
	mu        sync.Mutex
	pointsMS  map[string]float64
}

type contextKey struct{}
type bodyGenerationContextKey struct{}

// New creates a recorder with handler-entry already recorded at offset zero.
func New(startedAt time.Time) *Recorder {
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	return &Recorder{
		startedAt: startedAt,
		pointsMS:  map[string]float64{string(PointHandlerReceived): 0},
	}
}

func WithRecorder(ctx context.Context, recorder *Recorder) context.Context {
	if recorder == nil {
		return ctx
	}
	return context.WithValue(ctx, contextKey{}, recorder)
}

func FromContext(ctx context.Context) *Recorder {
	if ctx == nil {
		return nil
	}
	recorder, _ := ctx.Value(contextKey{}).(*Recorder)
	return recorder
}

// WithBodyGenerationContext marks provider calls that produce the user's chat
// response. Routing and other provider calls must keep their context unmarked.
func WithBodyGenerationContext(ctx context.Context) context.Context {
	if ctx == nil || FromContext(ctx) == nil {
		return ctx
	}
	return context.WithValue(ctx, bodyGenerationContextKey{}, true)
}

func IsBodyGenerationContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	marked, _ := ctx.Value(bodyGenerationContextKey{}).(bool)
	return marked
}

// Gateway boundary marks are restricted to body-generation calls so a prior
// routing request cannot claim the first HTTP timing points for the turn.
func MarkGatewayRequestStarted(ctx context.Context) {
	if IsBodyGenerationContext(ctx) {
		Mark(ctx, PointGatewayRequestStarted)
	}
}

func MarkGatewayResponseHeaders(ctx context.Context) {
	if IsBodyGenerationContext(ctx) {
		Mark(ctx, PointGatewayResponseHeaders)
	}
}

// Mark records the first observation of a known point. Unknown point names are
// ignored so the serialized snapshot remains bounded by this package's schema.
func Mark(ctx context.Context, point Point) {
	if recorder := FromContext(ctx); recorder != nil {
		recorder.Mark(point)
	}
}

func (r *Recorder) Mark(point Point) {
	if r == nil || !knownPoint(point) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := string(point)
	if _, exists := r.pointsMS[key]; exists {
		return
	}
	r.pointsMS[key] = time.Since(r.startedAt).Seconds() * 1000
}

func (r *Recorder) Snapshot() Snapshot {
	if r == nil {
		return Snapshot{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	points := make(map[string]float64, len(r.pointsMS))
	for point, offset := range r.pointsMS {
		points[point] = offset
	}
	return Snapshot{SchemaVersion: SchemaVersion, PointsMS: points}
}

func knownPoint(point Point) bool {
	switch point {
	case PointHandlerReceived, PointAccepted, PointAsyncStart, PointRouteDecided,
		PointDispatchStarted, PointBeginTurnStarted, PointBeginTurnEnded,
		PointUserMemoryStarted, PointUserMemoryEnded, PointRecallStarted,
		PointRecallEnded, PointCategoryStarted, PointCategoryEnded,
		PointPromptPreparationStarted, PointPromptPreparationEnded,
		PointGenerationProviderStart, PointGenerationProviderEnd,
		PointGatewayRequestStarted, PointGatewayResponseHeaders,
		PointFirstTextCallback, PointConversationCommitStarted, PointConversationCommitEnded,
		PointResponsePublicationStarted:
		return true
	default:
		return false
	}
}
