package tts

import "strings"

const (
	EventChannelViewer = "viewer"
	EventChannelIdle   = "idlechat"
	EventChatIDViewer  = "viewer-user"
	DefaultTrack       = "default"
)

type AudioChunkEventPayloadInput struct {
	SessionID   string
	ResponseID  string
	MessageID   string
	TurnIndex   int
	UtteranceID string
	ChunkIndex  int
	CharacterID string
	SpeechText  string
	DisplayText string
	AudioPath   string
	AudioURL    string
	Track       string
}

type AudioChunkEventPayload struct {
	SessionID   string `json:"session_id"`
	ResponseID  string `json:"response_id"`
	MessageID   string `json:"message_id,omitempty"`
	TurnIndex   int    `json:"turn_index"`
	UtteranceID string `json:"utterance_id"`
	ChunkIndex  int    `json:"chunk_index"`
	CharacterID string `json:"character_id"`
	SpeechText  string `json:"speech_text"`
	Text        string `json:"text"`
	DisplayText string `json:"display_text"`
	AudioPath   string `json:"audio_path,omitempty"`
	AudioURL    string `json:"audio_url,omitempty"`
	Track       string `json:"track"`
}

type SessionCompletedEventPayloadInput struct {
	SessionID   string
	ResponseID  string
	MessageID   string
	TurnIndex   int
	UtteranceID string
	CharacterID string
}

type SessionCompletedEventPayload struct {
	SessionID   string `json:"session_id"`
	ResponseID  string `json:"response_id,omitempty"`
	MessageID   string `json:"message_id,omitempty"`
	TurnIndex   int    `json:"turn_index"`
	UtteranceID string `json:"utterance_id,omitempty"`
	CharacterID string `json:"character_id"`
}

type PlaybackEventRoute struct {
	Channel string
	ChatID  string
}

func BuildAudioChunkEventPayload(input AudioChunkEventPayloadInput) AudioChunkEventPayload {
	displayText := strings.TrimSpace(input.DisplayText)
	if displayText == "" {
		displayText = input.SpeechText
	}
	track := strings.TrimSpace(input.Track)
	if track == "" {
		track = DefaultTrack
	}
	return AudioChunkEventPayload{
		SessionID:   strings.TrimSpace(input.SessionID),
		ResponseID:  strings.TrimSpace(input.ResponseID),
		MessageID:   strings.TrimSpace(input.MessageID),
		TurnIndex:   input.TurnIndex,
		UtteranceID: strings.TrimSpace(input.UtteranceID),
		ChunkIndex:  input.ChunkIndex,
		CharacterID: strings.TrimSpace(input.CharacterID),
		SpeechText:  input.SpeechText,
		Text:        input.SpeechText,
		DisplayText: displayText,
		AudioPath:   strings.TrimSpace(input.AudioPath),
		AudioURL:    strings.TrimSpace(input.AudioURL),
		Track:       track,
	}
}

func BuildSessionCompletedEventPayload(input SessionCompletedEventPayloadInput) SessionCompletedEventPayload {
	return SessionCompletedEventPayload{
		SessionID:   strings.TrimSpace(input.SessionID),
		ResponseID:  strings.TrimSpace(input.ResponseID),
		MessageID:   strings.TrimSpace(input.MessageID),
		TurnIndex:   input.TurnIndex,
		UtteranceID: strings.TrimSpace(input.UtteranceID),
		CharacterID: strings.TrimSpace(input.CharacterID),
	}
}

func PlaybackEventRouteForSession(sessionID string) PlaybackEventRoute {
	sessionID = strings.TrimSpace(sessionID)
	if IsIdleChatPublicSession(sessionID) {
		return PlaybackEventRoute{Channel: EventChannelIdle, ChatID: sessionID}
	}
	return PlaybackEventRoute{Channel: EventChannelViewer, ChatID: EventChatIDViewer}
}
