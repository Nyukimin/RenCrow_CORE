package main

import moduletts "github.com/Nyukimin/picoclaw_multiLLM/modules/tts"

var idleChatTTSPendingStore = moduletts.NewPendingPlaybackStore()

func registerIdleChatTTSPending(sessionID, responseID string) <-chan struct{} {
	return idleChatTTSPendingStore.Register(sessionID, responseID)
}

func registerIdleChatTopicGate(idleSessionID, ttsSessionID string) {
	idleChatTTSPendingStore.RegisterTopicGate(idleSessionID, ttsSessionID)
}

func notifyIdleChatTTSPlaybackCompleted(responseID string) bool {
	action := idleChatTTSPendingStore.CompleteByResponse(responseID)
	if action.ClearPublicBy != "" {
		clearTTSPublicSessionByResponse(action.ClearPublicBy)
	}
	return action.Matched
}

func clearIdleChatTTSPending(sessionID string) {
	action := idleChatTTSPendingStore.Clear(sessionID)
	if action.ClearPublicSession != "" {
		clearTTSPublicSession(action.ClearPublicSession)
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

func waitIdleChatTopicGate(idleSessionID string) {
	idleChatTTSPendingStore.WaitTopicGate(idleSessionID)
}

func snapshotIdleChatTTSPending() moduletts.PendingPlaybackSnapshot {
	return idleChatTTSPendingStore.Snapshot()
}
