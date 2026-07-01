package tts

import "strings"

type PendingPlaybackCompletionAction struct {
	Matched            bool   `json:"matched"`
	ResponseID         string `json:"response_id,omitempty"`
	TTSSessionID       string `json:"tts_session_id,omitempty"`
	TopicIdleSessionID string `json:"topic_idle_session_id,omitempty"`
	ClosePendingWait   bool   `json:"close_pending_wait"`
	CloseTopicGate     bool   `json:"close_topic_gate"`
	ClearPublicBy      string `json:"clear_public_by,omitempty"`
}

type PendingPlaybackClearAction struct {
	Matched            bool   `json:"matched"`
	TTSSessionID       string `json:"tts_session_id,omitempty"`
	TopicIdleSessionID string `json:"topic_idle_session_id,omitempty"`
	ClosePendingWait   bool   `json:"close_pending_wait"`
	CloseTopicGate     bool   `json:"close_topic_gate"`
	ClearPublicSession string `json:"clear_public_session,omitempty"`
}

func BuildPendingPlaybackCompletionAction(responseID string, ttsSessionID string, topicIdleSessionID string, matched bool) PendingPlaybackCompletionAction {
	responseID = strings.TrimSpace(responseID)
	ttsSessionID = strings.TrimSpace(ttsSessionID)
	topicIdleSessionID = strings.TrimSpace(topicIdleSessionID)
	if !matched {
		return PendingPlaybackCompletionAction{ResponseID: responseID}
	}
	return PendingPlaybackCompletionAction{
		Matched:            true,
		ResponseID:         responseID,
		TTSSessionID:       ttsSessionID,
		TopicIdleSessionID: topicIdleSessionID,
		ClosePendingWait:   true,
		CloseTopicGate:     topicIdleSessionID != "",
		ClearPublicBy:      responseID,
	}
}

func BuildPendingPlaybackClearAction(ttsSessionID string, topicIdleSessionID string, matched bool) PendingPlaybackClearAction {
	ttsSessionID = strings.TrimSpace(ttsSessionID)
	topicIdleSessionID = strings.TrimSpace(topicIdleSessionID)
	if !matched {
		return PendingPlaybackClearAction{TTSSessionID: ttsSessionID}
	}
	return PendingPlaybackClearAction{
		Matched:            true,
		TTSSessionID:       ttsSessionID,
		TopicIdleSessionID: topicIdleSessionID,
		ClosePendingWait:   true,
		CloseTopicGate:     topicIdleSessionID != "",
		ClearPublicSession: ttsSessionID,
	}
}
