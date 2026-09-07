package main

import moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"

var ttsPublicSessions = moduletts.NewPublicSessionStore()

func registerTTSPublicSession(internalSessionID, publicSessionID, publicPlaybackRef string) {
	registerTTSPublicSessionWithMessage(internalSessionID, publicSessionID, publicPlaybackRef, "", 0)
}

func registerTTSPublicSessionWithMessage(internalSessionID, publicSessionID, publicPlaybackRef, messageID string, turnIndex int) {
	ttsPublicSessions.Register(moduletts.PublicSessionRouteRegistration{
		InternalSessionID: internalSessionID,
		PublicSessionID:   publicSessionID,
		PublicPlaybackRef: publicPlaybackRef,
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

func resolveTTSPublicPlaybackRef(internalSessionID string) string {
	return ttsPublicSessions.ResolvePublicPlaybackRef(internalSessionID)
}

func resolveTTSPublicMessage(internalSessionID string) (string, int, string) {
	return ttsPublicSessions.ResolveMessage(internalSessionID)
}

func clearTTSPublicSession(internalSessionID string) {
	ttsPublicSessions.Clear(internalSessionID)
}

func retireTTSPublicSession(internalSessionID string) {
	ttsPublicSessions.Retire(internalSessionID)
}

func clearTTSPublicSessionByPlaybackRef(publicPlaybackRef string) {
	ttsPublicSessions.ClearByPublicPlaybackRef(publicPlaybackRef)
}

func retireTTSPublicSessionByPlaybackRef(publicPlaybackRef string) {
	ttsPublicSessions.RetireByPublicPlaybackRef(publicPlaybackRef)
}

func clearTTSPublicSequenceStateIfNoRoutes() {
	ttsPublicSessions.ClearSequencesIfNoRoutes()
}

func nextTTSPublicPlaybackRef(publicSessionID string) string {
	return ttsPublicSessions.NextPublicPlaybackRef(publicSessionID)
}

func nextTTSPublicPlaybackRefForMessage(publicSessionID, messageID string) string {
	return ttsPublicSessions.NextPublicPlaybackRefForMessage(publicSessionID, messageID)
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
