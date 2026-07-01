package tts

type EmotionVector struct {
	Warmth         float64 `json:"warmth"`
	Cheerfulness   float64 `json:"cheerfulness"`
	Seriousness    float64 `json:"seriousness"`
	Alertness      float64 `json:"alertness"`
	Calmness       float64 `json:"calmness"`
	Expressiveness float64 `json:"expressiveness"`
}

type Prosody struct {
	Speed          float64 `json:"speed"`
	Pitch          float64 `json:"pitch"`
	Pause          float64 `json:"pause"`
	Expressiveness float64 `json:"expressiveness"`
}

type ReasonTrace struct {
	Event               string   `json:"event"`
	AppliedContextRules []string `json:"applied_context_rules,omitempty"`
	AppliedTextFeatures []string `json:"applied_text_features,omitempty"`
	VoiceProfile        string   `json:"voice_profile,omitempty"`
}

type EmotionState struct {
	PrimaryEmotion string         `json:"primary_emotion"`
	VoiceProfile   string         `json:"voice_profile,omitempty"`
	EmotionVector  EmotionVector  `json:"emotion_vector"`
	Prosody        Prosody        `json:"prosody"`
	ReasonTrace    ReasonTrace    `json:"reason_trace"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

type EmotionContext struct {
	ConversationMode      string `json:"conversation_mode,omitempty"`
	UserWaitingTimeSec    int    `json:"user_waiting_time_sec,omitempty"`
	TimeOfDay             string `json:"time_of_day,omitempty"`
	PreviousEvent         string `json:"previous_event,omitempty"`
	RetryCount            int    `json:"retry_count,omitempty"`
	ConsecutiveFailures   int    `json:"consecutive_failures,omitempty"`
	Urgency               string `json:"urgency,omitempty"`
	UserAttentionRequired bool   `json:"user_attention_required,omitempty"`
}
