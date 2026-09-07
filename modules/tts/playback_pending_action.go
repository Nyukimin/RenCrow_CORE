package tts

import "strings"

type PendingPlaybackCompletionAction struct {
	Matched            bool   `json:"matched"`
	PublicPlaybackRef  string `json:"public_playback_ref,omitempty"`
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

func BuildPendingPlaybackCompletionAction(publicPlaybackRef string, ttsSessionID string, topicIdleSessionID string, matched bool) PendingPlaybackCompletionAction {
	publicPlaybackRef = strings.TrimSpace(publicPlaybackRef)
	ttsSessionID = strings.TrimSpace(ttsSessionID)
	topicIdleSessionID = strings.TrimSpace(topicIdleSessionID)
	if !matched {
		return PendingPlaybackCompletionAction{PublicPlaybackRef: publicPlaybackRef}
	}
	return PendingPlaybackCompletionAction{
		Matched:            true,
		PublicPlaybackRef:  publicPlaybackRef,
		TTSSessionID:       ttsSessionID,
		TopicIdleSessionID: topicIdleSessionID,
		ClosePendingWait:   true,
		CloseTopicGate:     topicIdleSessionID != "",
		ClearPublicBy:      publicPlaybackRef,
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
