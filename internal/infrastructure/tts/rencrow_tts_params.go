package tts

import moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"

func buildRequestIDHeader(sessionID string, chunkIndex int) string {
	return moduletts.BuildRequestIDHeader(sessionID, chunkIndex)
}
