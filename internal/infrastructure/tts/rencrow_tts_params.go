package tts

import moduletts "github.com/Nyukimin/picoclaw_multiLLM/modules/tts"

func buildRequestIDHeader(sessionID string, chunkIndex int) string {
	return moduletts.BuildRequestIDHeader(sessionID, chunkIndex)
}
