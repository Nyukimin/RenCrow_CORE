package tts

import (
	"fmt"
	"strings"
	"time"
)

const (
	IdleChatTTSEventConversationMode = "chat"
	IdleChatTTSEventName             = "conversation"
	IdleChatTTSSpeechMode            = "conversational"
	IdleChatTTSUrgencyNormal         = "normal"
)

type IdleChatTTSPlanInput struct {
	PublicSessionID string
	ResponseID      string
	MessageID       string
	TurnIndex       int
	Speaker         string
	SpeechText      string
	DisplayText     string
	TimeOfDay       string
	Now             time.Time
}

type IdleChatTTSPlan struct {
	SessionID        string
	PublicSessionID  string
	ResponseID       string
	MessageID        string
	TurnIndex        int
	CharacterID      string
	VoiceID          string
	VoiceProfile     string
	SpeechMode       string
	Event            string
	ConversationMode string
	TimeOfDay        string
	Urgency          string
	SpeechText       string
	DisplayText      string
}

func BuildIdleChatTTSPlan(input IdleChatTTSPlanInput) (IdleChatTTSPlan, bool) {
	publicSessionID := strings.TrimSpace(input.PublicSessionID)
	responseID := strings.TrimSpace(input.ResponseID)
	speechText := strings.TrimSpace(input.SpeechText)
	if publicSessionID == "" || responseID == "" || speechText == "" {
		return IdleChatTTSPlan{}, false
	}
	now := input.Now
	if now.IsZero() {
		now = time.Now()
	}
	displayText := strings.TrimSpace(input.DisplayText)
	if displayText == "" {
		displayText = speechText
	}
	voiceID, voiceProfile := IdleChatVoiceForSpeaker(input.Speaker)
	timeOfDay := strings.TrimSpace(input.TimeOfDay)
	if timeOfDay == "" {
		timeOfDay = IdleChatTimeOfDayAt(now)
	}
	return IdleChatTTSPlan{
		SessionID:        fmt.Sprintf("%s-tts-%d-%s", publicSessionID, now.UnixNano(), responseID),
		PublicSessionID:  publicSessionID,
		ResponseID:       responseID,
		MessageID:        strings.TrimSpace(input.MessageID),
		TurnIndex:        input.TurnIndex,
		CharacterID:      NormalizeIdleChatCharacterID(input.Speaker),
		VoiceID:          voiceID,
		VoiceProfile:     voiceProfile,
		SpeechMode:       IdleChatTTSSpeechMode,
		Event:            IdleChatTTSEventName,
		ConversationMode: IdleChatTTSEventConversationMode,
		TimeOfDay:        timeOfDay,
		Urgency:          IdleChatTTSUrgencyNormal,
		SpeechText:       speechText,
		DisplayText:      displayText,
	}, true
}
