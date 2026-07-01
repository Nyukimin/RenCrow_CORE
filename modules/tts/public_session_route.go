package tts

import "strings"

type PublicSessionRoute struct {
	PublicSessionID string
	ResponseID      string
	MessageID       string
	TurnIndex       int
	UtteranceID     string
	Generation      uint64
	TimedOut        bool
	ChunkIndexes    map[int]int
}

type PublicSessionRouteRegistration struct {
	InternalSessionID string
	PublicSessionID   string
	ResponseID        string
	MessageID         string
	TurnIndex         int
	Generation        uint64
}

func NewPublicSessionRoute(reg PublicSessionRouteRegistration) (PublicSessionRoute, bool) {
	internalSessionID := strings.TrimSpace(reg.InternalSessionID)
	publicSessionID := strings.TrimSpace(reg.PublicSessionID)
	if internalSessionID == "" || publicSessionID == "" || internalSessionID == publicSessionID {
		return PublicSessionRoute{}, false
	}
	messageID := strings.TrimSpace(reg.MessageID)
	utteranceID := strings.TrimSpace(reg.ResponseID)
	if messageID != "" {
		utteranceID = messageID + ":utt:0000"
	}
	return PublicSessionRoute{
		PublicSessionID: publicSessionID,
		ResponseID:      strings.TrimSpace(reg.ResponseID),
		MessageID:       messageID,
		TurnIndex:       reg.TurnIndex,
		UtteranceID:     utteranceID,
		Generation:      reg.Generation,
		ChunkIndexes:    map[int]int{},
	}, true
}

func (r PublicSessionRoute) IsCurrent(generation uint64) bool {
	return r.Generation == generation && !r.TimedOut
}

func (r PublicSessionRoute) IsStale(generation uint64) bool {
	return r.Generation != generation || r.TimedOut
}

func (r PublicSessionRoute) MatchesTimeout(publicSessionID string, messageID string, turnIndex int, allForSession bool) bool {
	if strings.TrimSpace(r.PublicSessionID) != strings.TrimSpace(publicSessionID) || strings.TrimSpace(publicSessionID) == "" {
		return false
	}
	if allForSession {
		return true
	}
	messageID = strings.TrimSpace(messageID)
	if messageID != "" {
		return strings.TrimSpace(r.MessageID) == messageID
	}
	if turnIndex >= 0 {
		return r.TurnIndex == turnIndex
	}
	return false
}

func (r PublicSessionRoute) PublicSessionOrFallback(internalSessionID string) string {
	if strings.TrimSpace(r.PublicSessionID) == "" {
		return strings.TrimSpace(internalSessionID)
	}
	return strings.TrimSpace(r.PublicSessionID)
}

func (r PublicSessionRoute) Response() string {
	return strings.TrimSpace(r.ResponseID)
}

func (r PublicSessionRoute) Message() (string, int, string) {
	return strings.TrimSpace(r.MessageID), r.TurnIndex, strings.TrimSpace(r.UtteranceID)
}
