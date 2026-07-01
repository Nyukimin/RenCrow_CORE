package main

import moduletts "github.com/Nyukimin/picoclaw_multiLLM/modules/tts"

var ttsPublicSessions = moduletts.NewPublicSessionStore()

func registerTTSPublicSession(internalSessionID, publicSessionID, responseID string) {
	registerTTSPublicSessionWithMessage(internalSessionID, publicSessionID, responseID, "", 0)
}

func registerTTSPublicSessionWithMessage(internalSessionID, publicSessionID, responseID, messageID string, turnIndex int) {
	ttsPublicSessions.Register(moduletts.PublicSessionRouteRegistration{
		InternalSessionID: internalSessionID,
		PublicSessionID:   publicSessionID,
		ResponseID:        responseID,
		MessageID:         messageID,
		TurnIndex:         turnIndex,
	})
}

func resetTTSPublicSessionRoutesForIdleChat() {
	ttsPublicSessions.ResetForIdleChat()
}

func isStaleTTSPublicSession(internalSessionID string) bool {
	return ttsPublicSessions.IsStale(internalSessionID)
}

func markTTSPublicSessionTimedOut(publicSessionID, messageID string, turnIndex int, allForSession bool) []string {
	return ttsPublicSessions.MarkTimedOut(publicSessionID, messageID, turnIndex, allForSession)
}

func resolveTTSPublicChunk(internalSessionID string, internalChunkIndex int) (string, int) {
	resolved := ttsPublicSessions.ResolveChunk(internalSessionID, internalChunkIndex)
	return resolved.SessionID, resolved.ChunkIndex
}

func resolveTTSPublicSession(internalSessionID string) string {
	return ttsPublicSessions.ResolveSession(internalSessionID)
}

func resolveTTSPublicResponse(internalSessionID string) string {
	return ttsPublicSessions.ResolveResponse(internalSessionID)
}

func resolveTTSPublicMessage(internalSessionID string) (string, int, string) {
	return ttsPublicSessions.ResolveMessage(internalSessionID)
}

func clearTTSPublicSession(internalSessionID string) {
	ttsPublicSessions.Clear(internalSessionID)
}

func clearTTSPublicSessionByResponse(responseID string) {
	ttsPublicSessions.ClearByResponse(responseID)
}

func clearTTSPublicSequenceStateIfNoRoutes() {
	ttsPublicSessions.ClearSequencesIfNoRoutes()
}

func nextTTSPublicResponseID(publicSessionID string) string {
	return ttsPublicSessions.NextResponseID(publicSessionID)
}

func nextTTSPublicResponseIDForMessage(publicSessionID, messageID string) string {
	return ttsPublicSessions.NextResponseIDForMessage(publicSessionID, messageID)
}

func isIdleChatPublicSession(sessionID string) bool {
	return moduletts.IsIdleChatPublicSession(sessionID)
}

func snapshotTTSPublicSessions() moduletts.PublicPlaybackSnapshot {
	return ttsPublicSessions.Snapshot()
}

func resetTTSPublicSessionStateForTest() {
	ttsPublicSessions.ResetAll()
}
