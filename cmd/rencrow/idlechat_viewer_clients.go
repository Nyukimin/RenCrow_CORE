package main

import (
	"log"
	"strings"
)

var idleChatViewerClientCount func() int

func setIdleChatViewerClientCount(fn func() int) {
	idleChatViewerClientCount = fn
}

func hasIdleChatViewerClients() bool {
	if idleChatViewerClientCount == nil {
		return true
	}
	return idleChatViewerClientCount() > 0 && strings.TrimSpace(activeViewerControl.Snapshot().ActiveAudioViewerID) != ""
}

func handleIdleChatViewerClientCountChanged(count int) {
	if count != 0 {
		return
	}
	cancelAllIdleChatTTS()
	if idleChatTTSPrefetch != nil {
		idleChatTTSPrefetch.CancelAll()
	}
	pending := snapshotIdleChatTTSPending()
	if pending.PendingSessionCount == 0 && pending.PendingPublicPlaybackCount == 0 {
		return
	}
	clearAllIdleChatTTSPendingStale()
	log.Printf("[IdleChat] cleared pending TTS playback waits because no Viewer SSE clients remain: pending_sessions=%d pending_public_playback=%d", pending.PendingSessionCount, pending.PendingPublicPlaybackCount)
}
