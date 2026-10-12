package tts

import (
	"strings"
	"time"
)

type RouteTTSContext struct {
	ConversationMode      string
	TimeOfDay             string
	Urgency               string
	UserAttentionRequired bool
}

type RouteTTSPlanInput struct {
	Route                 string
	SessionID             string
	PublicPlaybackRef     string
	ChatCharacterID       string
	Urgency               string
	UserAttentionRequired bool
	Now                   time.Time
}

type RouteTTSPlan struct {
	SessionID             string
	PublicPlaybackRef     string
	CharacterID           string
	VoiceID               string
	VoiceProfile          string
	SpeechMode            string
	Event                 string
	Urgency               string
	ConversationMode      string
	UserAttentionRequired bool
	Context               RouteTTSContext
}

func BuildRouteTTSPlan(input RouteTTSPlanInput) (RouteTTSPlan, bool) {
	sessionID := strings.TrimSpace(input.SessionID)
	publicPlaybackRef := strings.TrimSpace(input.PublicPlaybackRef)
	if sessionID == "" || publicPlaybackRef == "" {
		return RouteTTSPlan{}, false
	}
	route := normalizeRouteName(input.Route)
	// The voice is the responding Agent's own voice. An unresolved route or
	// recipient yields no plan: no session starts instead of speaking with a
	// different Agent's voice.
	speaker, ok := RespondingAgentForRoute(route, input.ChatCharacterID)
	if !ok {
		return RouteTTSPlan{}, false
	}
	voiceID, voiceProfile, ok := VoiceForAgent(speaker)
	if !ok {
		return RouteTTSPlan{}, false
	}
	ctx := BuildRouteTTSContext(route, input.Urgency, input.UserAttentionRequired, input.Now)
	return RouteTTSPlan{
		SessionID:             sessionID,
		PublicPlaybackRef:     publicPlaybackRef,
		CharacterID:           speaker,
		VoiceID:               voiceID,
		VoiceProfile:          voiceProfile,
		SpeechMode:            SpeechModeForRoute(route),
		Event:                 EventForRoute(route),
		Urgency:               ctx.Urgency,
		ConversationMode:      ctx.ConversationMode,
		UserAttentionRequired: ctx.UserAttentionRequired,
		Context:               ctx,
	}, true
}

func BuildRouteTTSContext(route string, urgency string, attention bool, now time.Time) RouteTTSContext {
	if now.IsZero() {
		now = time.Now()
	}
	return RouteTTSContext{
		ConversationMode:      ConversationModeForRoute(route),
		TimeOfDay:             IdleChatTimeOfDayAt(now),
		Urgency:               ChooseNonEmpty(urgency, "normal"),
		UserAttentionRequired: attention,
	}
}

func EventForRoute(route string) string {
	switch normalizeRouteName(route) {
	case "PLAN", "ANALYZE", "RESEARCH", "OPS":
		return "analysis_report"
	default:
		return "conversation"
	}
}

func ConversationModeForRoute(route string) string {
	switch normalizeRouteName(route) {
	case "PLAN", "ANALYZE", "RESEARCH", "OPS":
		return "report"
	default:
		return "chat"
	}
}

func SpeechModeForRoute(route string) string {
	switch normalizeRouteName(route) {
	case "OPS", "PLAN", "ANALYZE", "RESEARCH":
		return "report"
	default:
		return "conversational"
	}
}

func ChooseNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func EqualFoldTrim(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func normalizeRouteName(route string) string {
	return strings.ToUpper(strings.TrimSpace(route))
}
