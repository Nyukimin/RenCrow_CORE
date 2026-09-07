package tts

import "strings"

type PublicChunkResolution struct {
	SessionID       string
	ChunkIndex      int
	NextChunkNumber int
	Assigned        bool
}

type PublicPlaybackRefResolution struct {
	PublicPlaybackRef           string
	NextPublicPlaybackRefNumber int
	Advance                     bool
}

func ResolvePublicChunk(route *PublicSessionRoute, internalSessionID string, internalChunkIndex int, nextChunkNumber int) PublicChunkResolution {
	internalSessionID = strings.TrimSpace(internalSessionID)
	if route == nil || strings.TrimSpace(route.PublicSessionID) == "" {
		return PublicChunkResolution{SessionID: internalSessionID, ChunkIndex: internalChunkIndex, NextChunkNumber: nextChunkNumber}
	}
	if route.ChunkIndexes == nil {
		route.ChunkIndexes = map[int]int{}
	}
	if publicChunkIndex, ok := route.ChunkIndexes[internalChunkIndex]; ok {
		return PublicChunkResolution{SessionID: route.PublicSessionID, ChunkIndex: publicChunkIndex, NextChunkNumber: nextChunkNumber}
	}
	if nextChunkNumber < 0 {
		nextChunkNumber = 0
	}
	route.ChunkIndexes[internalChunkIndex] = nextChunkNumber
	return PublicChunkResolution{
		SessionID:       route.PublicSessionID,
		ChunkIndex:      nextChunkNumber,
		NextChunkNumber: nextChunkNumber + 1,
		Assigned:        true,
	}
}

func ResolveNextPublicPlaybackRef(publicSessionID string, nextPublicPlaybackRefNumber int) PublicPlaybackRefResolution {
	publicSessionID = strings.TrimSpace(publicSessionID)
	if publicSessionID == "" {
		return PublicPlaybackRefResolution{}
	}
	if nextPublicPlaybackRefNumber < 0 {
		nextPublicPlaybackRefNumber = 0
	}
	return PublicPlaybackRefResolution{
		PublicPlaybackRef:           publicSessionID + ":" + FormatFixed4(nextPublicPlaybackRefNumber),
		NextPublicPlaybackRefNumber: nextPublicPlaybackRefNumber + 1,
		Advance:                     true,
	}
}

func ResolvePublicPlaybackRefForMessage(publicSessionID string, messageID string, nextPublicPlaybackRefNumber int) PublicPlaybackRefResolution {
	publicSessionID = strings.TrimSpace(publicSessionID)
	messageID = strings.TrimSpace(messageID)
	if publicSessionID == "" {
		return PublicPlaybackRefResolution{}
	}
	prefix := publicSessionID + ":"
	if strings.HasPrefix(messageID, prefix) {
		suffix := strings.TrimPrefix(messageID, prefix)
		if strings.HasPrefix(suffix, "msg:") {
			if n, ok := ParseFixed4(strings.TrimPrefix(suffix, "msg:")); ok {
				return PublicPlaybackRefResolution{
					PublicPlaybackRef:           publicSessionID + ":" + FormatFixed4(n),
					NextPublicPlaybackRefNumber: n + 1,
					Advance:                     true,
				}
			}
		}
		if _, ok := ParseTrailingPublicPlaybackNumber(suffix); ok {
			return PublicPlaybackRefResolution{PublicPlaybackRef: messageID, NextPublicPlaybackRefNumber: nextPublicPlaybackRefNumber}
		}
	}
	return ResolveNextPublicPlaybackRef(publicSessionID, nextPublicPlaybackRefNumber)
}
