package tts

import "strings"

type PublicChunkResolution struct {
	SessionID       string
	ChunkIndex      int
	NextChunkNumber int
	Assigned        bool
}

type PublicResponseResolution struct {
	ResponseID         string
	NextResponseNumber int
	Advance            bool
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

func ResolveNextPublicResponseID(publicSessionID string, nextResponseNumber int) PublicResponseResolution {
	publicSessionID = strings.TrimSpace(publicSessionID)
	if publicSessionID == "" {
		return PublicResponseResolution{}
	}
	if nextResponseNumber < 0 {
		nextResponseNumber = 0
	}
	return PublicResponseResolution{
		ResponseID:         publicSessionID + ":" + FormatFixed4(nextResponseNumber),
		NextResponseNumber: nextResponseNumber + 1,
		Advance:            true,
	}
}

func ResolvePublicResponseIDForMessage(publicSessionID string, messageID string, nextResponseNumber int) PublicResponseResolution {
	publicSessionID = strings.TrimSpace(publicSessionID)
	messageID = strings.TrimSpace(messageID)
	if publicSessionID == "" {
		return PublicResponseResolution{}
	}
	prefix := publicSessionID + ":"
	if strings.HasPrefix(messageID, prefix) {
		suffix := strings.TrimPrefix(messageID, prefix)
		if strings.HasPrefix(suffix, "msg:") {
			if n, ok := ParseFixed4(strings.TrimPrefix(suffix, "msg:")); ok {
				return PublicResponseResolution{
					ResponseID:         publicSessionID + ":" + FormatFixed4(n),
					NextResponseNumber: n + 1,
					Advance:            true,
				}
			}
		}
		if _, ok := ParseTrailingResponseNumber(suffix); ok {
			return PublicResponseResolution{ResponseID: messageID, NextResponseNumber: nextResponseNumber}
		}
	}
	return ResolveNextPublicResponseID(publicSessionID, nextResponseNumber)
}
