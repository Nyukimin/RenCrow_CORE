package speech

import (
	"context"
	"time"
)

// PlaybackPhase is the viewer-owned playback state for one selected utterance.
type PlaybackPhase string

const (
	PlaybackPhaseReady     PlaybackPhase = "ready"
	PlaybackPhaseStarted   PlaybackPhase = "started"
	PlaybackPhaseEnded     PlaybackPhase = "ended"
	PlaybackPhaseError     PlaybackPhase = "error"
	PlaybackPhaseCancelled PlaybackPhase = "cancelled"
)

// PlaybackProgress coordinates synthesis with the actual playback owner.
// Implementations are turn-scoped and must reject out-of-order or unready
// started reports. Utterance indexes are bounded by MaxUtterances.
type PlaybackProgress interface {
	MarkReady(index int) error
	WaitStarted(ctx context.Context, index int) error
	WaitEnded(ctx context.Context, index int) error
	MarkStarted(index int) error
	MarkEnded(index int) error
	MarkError(index int, cause error) error
	MarkCancelled(index int) error
}

// PlaybackProgressTiming exposes turn and phase timestamps for bounded
// transport/playback observations. PlaybackProgress implementations may omit
// it when they do not retain timing data.
type PlaybackProgressTiming interface {
	TurnCreatedAt() time.Time
	PlaybackStartedAt(index int) (time.Time, bool)
	PlaybackEndedAt(index int) (time.Time, bool)
}

type playbackProgressContextKey struct{}

// WithPlaybackProgress attaches a turn-scoped playback coordinator to ctx.
func WithPlaybackProgress(ctx context.Context, progress PlaybackProgress) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if progress == nil {
		return ctx
	}
	return context.WithValue(ctx, playbackProgressContextKey{}, progress)
}

// PlaybackProgressFromContext returns the playback coordinator attached to ctx.
func PlaybackProgressFromContext(ctx context.Context) PlaybackProgress {
	if ctx == nil {
		return nil
	}
	progress, _ := ctx.Value(playbackProgressContextKey{}).(PlaybackProgress)
	return progress
}
