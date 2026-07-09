package main

import (
	"context"
	"net/http"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/modules/core"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

type ttsPlaybackStateObserver struct{}

func (ttsPlaybackStateObserver) Health(context.Context) core.HealthReport {
	snapshot := collectTTSPlaybackStateSnapshot()
	return moduletts.BuildPlaybackStateHealthReport(snapshot)
}

func (ttsPlaybackStateObserver) Snapshot(context.Context) (moduletts.PlaybackStateSnapshot, error) {
	return collectTTSPlaybackStateSnapshot(), nil
}

func collectTTSPlaybackStateSnapshot() moduletts.PlaybackStateSnapshot {
	pending := snapshotIdleChatTTSPending()
	public := snapshotTTSPublicSessions()
	return moduletts.BuildPlaybackStateSnapshot(pending, public)
}

func handleModuleTTSPlaybackState(observer moduletts.PlaybackStateObserver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !core.RequireHTTPMethod(w, r, http.MethodGet) {
			return
		}
		if observer == nil {
			http.Error(w, moduletts.PlaybackStateObserverUnavailableMessage, http.StatusServiceUnavailable)
			return
		}
		snapshot, err := observer.Snapshot(r.Context())
		if err != nil {
			http.Error(w, moduletts.PlaybackStateSnapshotFailedPrefix+err.Error(), http.StatusInternalServerError)
			return
		}
		_ = core.WriteJSON(w, moduletts.BuildPlaybackStateReport(r.Context(), observer, snapshot, time.Now().UTC()))
	}
}
