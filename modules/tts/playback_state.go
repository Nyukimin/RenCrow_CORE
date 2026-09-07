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

func BuildPendingPlaybackSnapshot(sessionIDs []string, publicPlaybackRefs []string, topicGateCount int, topicRouteCount int) PendingPlaybackSnapshot {
	sessionIDs = append([]string(nil), sessionIDs...)
	publicPlaybackRefs = append([]string(nil), publicPlaybackRefs...)
	sort.Strings(sessionIDs)
	sort.Strings(publicPlaybackRefs)
	return PendingPlaybackSnapshot{
		PendingSessionCount:        len(sessionIDs),
		PendingPublicPlaybackCount: len(publicPlaybackRefs),
		PendingSessionIDs:          sessionIDs,
		PendingPublicPlaybackRefs:  publicPlaybackRefs,
		TopicGateCount:             topicGateCount,
		TopicRouteCount:            topicRouteCount,
	}
}

func BuildPublicPlaybackSnapshot(routeCount int, staleRouteCount int, nextChunkSessionCount int, nextPublicPlaybackRefSessionCount int) PublicPlaybackSnapshot {
	return PublicPlaybackSnapshot{
		RouteCount:                     nonNegative(routeCount),
		StaleRouteCount:                nonNegative(staleRouteCount),
		NextChunkSessionCount:          nonNegative(nextChunkSessionCount),
		NextPublicPlaybackSessionCount: nonNegative(nextPublicPlaybackRefSessionCount),
	}
}

func BuildPlaybackStateSnapshot(pending PendingPlaybackSnapshot, public PublicPlaybackSnapshot) PlaybackStateSnapshot {
	return PlaybackStateSnapshot{
		PendingSessionCount:            pending.PendingSessionCount,
		PendingPublicPlaybackCount:     pending.PendingPublicPlaybackCount,
		PendingSessionIDs:              append([]string(nil), pending.PendingSessionIDs...),
		PendingPublicPlaybackRefs:      append([]string(nil), pending.PendingPublicPlaybackRefs...),
		TopicGateCount:                 pending.TopicGateCount,
		TopicRouteCount:                pending.TopicRouteCount,
		PublicRouteCount:               public.RouteCount,
		PublicStaleRouteCount:          public.StaleRouteCount,
		NextChunkSessionCount:          public.NextChunkSessionCount,
		NextPublicPlaybackSessionCount: public.NextPublicPlaybackSessionCount,
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
	if snapshot.PendingSessionCount > 0 || snapshot.PendingPublicPlaybackCount > 0 || snapshot.TopicGateCount > 0 {
		status = core.HealthLive
		detail = "playback pending state active"
	}
	return core.HealthReport{
		Module: "tts.playback",
		Status: status,
		Ready:  true,
		Detail: detail,
		Metadata: map[string]any{
			"pending_session_count":         snapshot.PendingSessionCount,
			"pending_public_playback_count": snapshot.PendingPublicPlaybackCount,
			"public_route_count":            snapshot.PublicRouteCount,
		},
	}
}
