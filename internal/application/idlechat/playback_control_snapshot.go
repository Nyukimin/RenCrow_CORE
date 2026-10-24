package idlechat

import "strings"

// PlaybackControlSnapshot is the IdleChat owner's short RAM projection for
// immediate playback control. Surface presence combines it with its own policy
// revision and admission state before publishing it to Viewer clients.
type PlaybackControlSnapshot struct {
	RuntimeEpoch         string
	PlaybackGeneration   uint64
	PlaybackActive       bool
	CleanupPending       bool
	GenerationState      string
	GenerationQuiesced   bool
	BackendResourceState string
}

// SetRuntimeEpoch attaches the current surface-presence epoch to IdleChat
// events and control snapshots. Surface presence owns the epoch value.
func (o *IdleChatOrchestrator) SetRuntimeEpoch(runtimeEpoch string) {
	if o == nil {
		return
	}
	runtimeEpoch = strings.TrimSpace(runtimeEpoch)
	o.mu.Lock()
	changed := o.runtimeEpoch != runtimeEpoch
	o.runtimeEpoch = runtimeEpoch
	o.mu.Unlock()
	if changed {
		o.notifyPlaybackControlSnapshotChanged()
	}
}

// SetPlaybackControlSnapshotChangedHandler installs a nonblocking notification
// hook. The handler is always invoked after releasing IdleChat owner locks.
func (o *IdleChatOrchestrator) SetPlaybackControlSnapshotChangedHandler(handler func()) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.playbackControlChanged = handler
	o.mu.Unlock()
}

// PlaybackControlSnapshot reads only IdleChat RAM state. It does not call a
// provider, persistence owner, callback, or backend resource API.
func (o *IdleChatOrchestrator) PlaybackControlSnapshot() PlaybackControlSnapshot {
	if o == nil {
		return PlaybackControlSnapshot{
			GenerationState:      "unknown",
			BackendResourceState: "unknown",
		}
	}
	o.mu.Lock()
	runtimeEpoch := o.runtimeEpoch
	generation := o.activeGeneration
	playbackActive := o.chatActive || o.manualMode
	// A run being admitted is not cleanup. Keep this bit limited to work that
	// has been stopped and still needs its owner finalizer to finish.
	cleanupPending := o.pendingConversationRun != nil
	o.mu.Unlock()

	o.generationWorkMu.Lock()
	managedWorkActive := o.generationWorkActive
	o.generationWorkMu.Unlock()

	generationQuiesced := !playbackActive && managedWorkActive == 0
	generationState := "quiesced"
	if playbackActive || managedWorkActive > 0 {
		generationState = "running"
	}
	return PlaybackControlSnapshot{
		RuntimeEpoch:         runtimeEpoch,
		PlaybackGeneration:   generation,
		PlaybackActive:       playbackActive,
		CleanupPending:       cleanupPending,
		GenerationState:      generationState,
		GenerationQuiesced:   generationQuiesced,
		BackendResourceState: "unknown",
	}
}

func (o *IdleChatOrchestrator) notifyPlaybackControlSnapshotChanged() {
	if o == nil {
		return
	}
	o.mu.Lock()
	handler := o.playbackControlChanged
	o.mu.Unlock()
	if handler != nil {
		handler()
	}
}
