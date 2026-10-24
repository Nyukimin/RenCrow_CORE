package main

import moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"

var idleChatTTSPendingStore = moduletts.NewPendingPlaybackStore()

func registerIdleChatTTSPending(sessionID, publicPlaybackRef string) <-chan struct{} {
	return idleChatTTSPendingStore.Register(sessionID, publicPlaybackRef)
}

func registerIdleChatTTSPendingForPlayback(sessionID, publicPlaybackRef, runtimeEpoch string, playbackGeneration uint64) <-chan struct{} {
	return idleChatTTSPendingStore.RegisterForPlaybackGeneration(sessionID, publicPlaybackRef, moduletts.PlaybackGenerationIdentity{
		RuntimeEpoch: runtimeEpoch, PlaybackGeneration: playbackGeneration,
	})
}

func registerIdleChatTopicGate(idleSessionID, ttsSessionID string) {
	idleChatTTSPendingStore.RegisterTopicGate(idleSessionID, ttsSessionID)
}

func registerIdleChatTopicGateForPlayback(idleSessionID, ttsSessionID, runtimeEpoch string, playbackGeneration uint64) {
	idleChatTTSPendingStore.RegisterTopicGateForPlayback(idleSessionID, ttsSessionID, moduletts.PlaybackGenerationIdentity{
		RuntimeEpoch: runtimeEpoch, PlaybackGeneration: playbackGeneration,
	})
}

func notifyIdleChatTTSPlaybackCompleted(publicPlaybackRef string) bool {
	action := idleChatTTSPendingStore.CompleteByPublicPlaybackRef(publicPlaybackRef)
	if action.ClearPublicBy != "" {
		clearTTSPublicSessionByPlaybackRef(action.ClearPublicBy)
	}
	return action.Matched
}

func clearIdleChatTTSPending(sessionID string) {
	action := idleChatTTSPendingStore.Clear(sessionID)
	if action.ClearPublicSession != "" {
		clearTTSPublicSession(action.ClearPublicSession)
	}
}

func clearIdleChatTTSPendingStale(sessionID string) {
	action := idleChatTTSPendingStore.Clear(sessionID)
	if action.ClearPublicSession != "" {
		retireTTSPublicSession(action.ClearPublicSession)
	}
}

func clearIdleChatTTSPendingByChan(target <-chan struct{}) {
	action := idleChatTTSPendingStore.ClearByWait(target)
	if action.ClearPublicSession != "" {
		clearTTSPublicSession(action.ClearPublicSession)
	}
}

func clearAllIdleChatTTSPending() {
	for _, sessionID := range idleChatTTSPendingStore.ClearAll() {
		clearTTSPublicSession(sessionID)
	}
}

func clearAllIdleChatTTSPendingStale() {
	for _, sessionID := range idleChatTTSPendingStore.ClearAll() {
		retireTTSPublicSession(sessionID)
	}
}

func clearIdleChatTTSPendingGeneration(runtimeEpoch string, playbackGeneration uint64) {
	for _, sessionID := range idleChatTTSPendingStore.CancelPlaybackGeneration(runtimeEpoch, playbackGeneration) {
		retireTTSPublicSession(sessionID)
	}
}

func waitIdleChatTopicGate(idleSessionID string) {
	idleChatTTSPendingStore.WaitTopicGate(idleSessionID)
}

func snapshotIdleChatTTSPending() moduletts.PendingPlaybackSnapshot {
	return idleChatTTSPendingStore.Snapshot()
}
