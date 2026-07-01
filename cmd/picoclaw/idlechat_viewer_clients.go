package main

import "log"

var idleChatViewerClientCount func() int

func setIdleChatViewerClientCount(fn func() int) {
	idleChatViewerClientCount = fn
}

func hasIdleChatViewerClients() bool {
	if idleChatViewerClientCount == nil {
		return true
	}
	return idleChatViewerClientCount() > 0
}

func handleIdleChatViewerClientCountChanged(count int) {
	if count != 0 {
		return
	}
	pending := snapshotIdleChatTTSPending()
	if pending.PendingSessionCount == 0 && pending.PendingResponseCount == 0 {
		return
	}
	clearAllIdleChatTTSPending()
	log.Printf("[IdleChat] cleared pending TTS playback waits because no Viewer SSE clients remain: pending_sessions=%d pending_responses=%d", pending.PendingSessionCount, pending.PendingResponseCount)
}
