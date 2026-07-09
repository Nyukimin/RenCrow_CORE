package tts

import (
	"context"
	"sort"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type PlaybackStateReport struct {
	UpdatedAt string                `json:"updated_at"`
	Health    core.HealthReport     `json:"health"`
	Snapshot  PlaybackStateSnapshot `json:"snapshot"`
}

const (
	PlaybackStateObserverUnavailableMessage = "tts playback observer unavailable"
	PlaybackStateSnapshotFailedPrefix       = "tts playback snapshot failed: "
)

func BuildPendingPlaybackSnapshot(sessionIDs []string, responseIDs []string, topicGateCount int, topicRouteCount int) PendingPlaybackSnapshot {
	sessionIDs = append([]string(nil), sessionIDs...)
	responseIDs = append([]string(nil), responseIDs...)
	sort.Strings(sessionIDs)
	sort.Strings(responseIDs)
	return PendingPlaybackSnapshot{
		PendingSessionCount:  len(sessionIDs),
		PendingResponseCount: len(responseIDs),
		PendingSessionIDs:    sessionIDs,
		PendingResponseIDs:   responseIDs,
		TopicGateCount:       topicGateCount,
		TopicRouteCount:      topicRouteCount,
	}
}

func BuildPublicPlaybackSnapshot(routeCount int, staleRouteCount int, nextChunkSessionCount int, nextResponseSessionCount int) PublicPlaybackSnapshot {
	return PublicPlaybackSnapshot{
		RouteCount:               nonNegative(routeCount),
		StaleRouteCount:          nonNegative(staleRouteCount),
		NextChunkSessionCount:    nonNegative(nextChunkSessionCount),
		NextResponseSessionCount: nonNegative(nextResponseSessionCount),
	}
}

func BuildPlaybackStateSnapshot(pending PendingPlaybackSnapshot, public PublicPlaybackSnapshot) PlaybackStateSnapshot {
	return PlaybackStateSnapshot{
		PendingSessionCount:      pending.PendingSessionCount,
		PendingResponseCount:     pending.PendingResponseCount,
		PendingSessionIDs:        append([]string(nil), pending.PendingSessionIDs...),
		PendingResponseIDs:       append([]string(nil), pending.PendingResponseIDs...),
		TopicGateCount:           pending.TopicGateCount,
		TopicRouteCount:          pending.TopicRouteCount,
		PublicRouteCount:         public.RouteCount,
		PublicStaleRouteCount:    public.StaleRouteCount,
		NextChunkSessionCount:    public.NextChunkSessionCount,
		NextResponseSessionCount: public.NextResponseSessionCount,
	}
}

func nonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func BuildPlaybackStateReport(ctx context.Context, observer PlaybackStateObserver, snapshot PlaybackStateSnapshot, updatedAt time.Time) PlaybackStateReport {
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	health := core.ProviderHealth(ctx, "tts.playback", observer, updatedAt)
	return PlaybackStateReport{
		UpdatedAt: updatedAt.UTC().Format(time.RFC3339),
		Health:    health,
		Snapshot:  snapshot,
	}
}

func BuildPlaybackStateHealthReport(snapshot PlaybackStateSnapshot) core.HealthReport {
	status := core.HealthReady
	detail := "playback state clear"
	if snapshot.PendingSessionCount > 0 || snapshot.PendingResponseCount > 0 || snapshot.TopicGateCount > 0 {
		status = core.HealthLive
		detail = "playback pending state active"
	}
	return core.HealthReport{
		Module: "tts.playback",
		Status: status,
		Ready:  true,
		Detail: detail,
		Metadata: map[string]any{
			"pending_session_count":  snapshot.PendingSessionCount,
			"pending_response_count": snapshot.PendingResponseCount,
			"public_route_count":     snapshot.PublicRouteCount,
		},
	}
}
