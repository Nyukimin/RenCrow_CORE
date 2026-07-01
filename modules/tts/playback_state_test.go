package tts

import (
	"context"
	"testing"
	"time"
)

func TestBuildPlaybackStateReport(t *testing.T) {
	snapshot := PlaybackStateSnapshot{PendingSessionCount: 1, PublicRouteCount: 2}
	report := BuildPlaybackStateReport(context.Background(), fakePlaybackStateObserver{}, snapshot, time.Date(2026, 5, 30, 1, 2, 3, 0, time.UTC))

	if report.UpdatedAt != "2026-05-30T01:02:03Z" {
		t.Fatalf("unexpected updated_at: %+v", report)
	}
	if report.Health.Module != "tts.playback" || report.Health.CheckedAt.IsZero() {
		t.Fatalf("health was not normalized: %+v", report.Health)
	}
	if report.Snapshot.PendingSessionCount != 1 || report.Snapshot.PublicRouteCount != 2 {
		t.Fatalf("snapshot was not preserved: %+v", report.Snapshot)
	}
}

func TestBuildPlaybackStateHealthReport(t *testing.T) {
	clear := BuildPlaybackStateHealthReport(PlaybackStateSnapshot{PublicRouteCount: 2})
	if clear.Module != "tts.playback" || clear.Status != "ready" || !clear.Ready || clear.Detail != "playback state clear" {
		t.Fatalf("clear health = %+v", clear)
	}
	if clear.Metadata["public_route_count"] != 2 {
		t.Fatalf("clear metadata = %+v", clear.Metadata)
	}

	live := BuildPlaybackStateHealthReport(PlaybackStateSnapshot{
		PendingSessionCount:  1,
		PendingResponseCount: 2,
		TopicGateCount:       3,
		PublicRouteCount:     4,
	})
	if live.Status != "live" || !live.Ready || live.Detail != "playback pending state active" {
		t.Fatalf("live health = %+v", live)
	}
	if live.Metadata["pending_session_count"] != 1 || live.Metadata["pending_response_count"] != 2 {
		t.Fatalf("live metadata = %+v", live.Metadata)
	}
}

func TestPlaybackStateEndpointMessages(t *testing.T) {
	if PlaybackStateObserverUnavailableMessage != "tts playback observer unavailable" {
		t.Fatalf("unexpected unavailable message: %q", PlaybackStateObserverUnavailableMessage)
	}
	if PlaybackStateSnapshotFailedPrefix != "tts playback snapshot failed: " {
		t.Fatalf("unexpected snapshot failed prefix: %q", PlaybackStateSnapshotFailedPrefix)
	}
}

func TestBuildPendingPlaybackSnapshotSortsAndCopiesIDs(t *testing.T) {
	sessions := []string{"session-b", "session-a"}
	responses := []string{"response-b", "response-a"}

	got := BuildPendingPlaybackSnapshot(sessions, responses, 3, 4)
	if got.PendingSessionCount != 2 || got.PendingResponseCount != 2 || got.TopicGateCount != 3 || got.TopicRouteCount != 4 {
		t.Fatalf("unexpected counts: %+v", got)
	}
	if got.PendingSessionIDs[0] != "session-a" || got.PendingResponseIDs[0] != "response-a" {
		t.Fatalf("ids were not sorted: %+v", got)
	}

	sessions[0] = "mutated"
	responses[0] = "mutated"
	if got.PendingSessionIDs[1] != "session-b" || got.PendingResponseIDs[1] != "response-b" {
		t.Fatalf("snapshot should not alias input slices: %+v", got)
	}
}

func TestBuildPublicPlaybackSnapshotClampsNegativeCounts(t *testing.T) {
	got := BuildPublicPlaybackSnapshot(-1, 2, -3, 4)
	if got.RouteCount != 0 || got.StaleRouteCount != 2 || got.NextChunkSessionCount != 0 || got.NextResponseSessionCount != 4 {
		t.Fatalf("unexpected public snapshot: %+v", got)
	}
}

func TestBuildPlaybackStateSnapshotCombinesPendingAndPublicState(t *testing.T) {
	pending := PendingPlaybackSnapshot{
		PendingSessionCount:  1,
		PendingResponseCount: 2,
		PendingSessionIDs:    []string{"s1"},
		PendingResponseIDs:   []string{"r1", "r2"},
		TopicGateCount:       3,
		TopicRouteCount:      4,
	}
	public := PublicPlaybackSnapshot{
		RouteCount:               5,
		StaleRouteCount:          6,
		NextChunkSessionCount:    7,
		NextResponseSessionCount: 8,
	}

	got := BuildPlaybackStateSnapshot(pending, public)
	if got.PendingSessionCount != 1 || got.PendingResponseCount != 2 || got.TopicGateCount != 3 || got.TopicRouteCount != 4 {
		t.Fatalf("pending state was not mapped: %+v", got)
	}
	if got.PublicRouteCount != 5 || got.PublicStaleRouteCount != 6 || got.NextChunkSessionCount != 7 || got.NextResponseSessionCount != 8 {
		t.Fatalf("public state was not mapped: %+v", got)
	}
	pending.PendingSessionIDs[0] = "mutated"
	public.RouteCount = 99
	if got.PendingSessionIDs[0] != "s1" || got.PublicRouteCount != 5 {
		t.Fatalf("snapshot did not isolate source data: %+v", got)
	}
}
