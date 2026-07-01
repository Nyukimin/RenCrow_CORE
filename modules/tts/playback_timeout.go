package tts

import "strings"

const PlaybackTimeoutKindSessionAudio = "session_audio_timeout"

type PlaybackTimeoutInput struct {
	Kind           string
	SessionID      string
	MessageID      string
	TurnIndex      int
	RemainingIndex int
	RemainingCount int
}

type PlaybackTimeoutConsumption struct {
	Kind                      string   `json:"kind"`
	SessionID                 string   `json:"session_id"`
	MessageID                 string   `json:"message_id,omitempty"`
	TurnIndex                 int      `json:"turn_index,omitempty"`
	RemainingIndex            int      `json:"remaining_index,omitempty"`
	RemainingCount            int      `json:"remaining_count,omitempty"`
	AllForSession             bool     `json:"all_for_session"`
	MatchedInternalSessionIDs []string `json:"matched_internal_session_ids,omitempty"`
	MatchedCount              int      `json:"matched_count"`
}

func BuildPlaybackTimeoutConsumption(input PlaybackTimeoutInput, matchedInternalSessionIDs []string) PlaybackTimeoutConsumption {
	matched := make([]string, 0, len(matchedInternalSessionIDs))
	for _, id := range matchedInternalSessionIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		matched = append(matched, id)
	}
	kind := strings.TrimSpace(input.Kind)
	allForSession := kind == PlaybackTimeoutKindSessionAudio
	return PlaybackTimeoutConsumption{
		Kind:                      kind,
		SessionID:                 strings.TrimSpace(input.SessionID),
		MessageID:                 strings.TrimSpace(input.MessageID),
		TurnIndex:                 input.TurnIndex,
		RemainingIndex:            input.RemainingIndex,
		RemainingCount:            input.RemainingCount,
		AllForSession:             allForSession,
		MatchedInternalSessionIDs: matched,
		MatchedCount:              len(matched),
	}
}
