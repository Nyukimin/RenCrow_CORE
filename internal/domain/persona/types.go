package persona

import "time"

type DiscomfortLog struct {
	EventID       string    `json:"event_id"`
	CharacterID   string    `json:"character_id"`
	MessageID     string    `json:"message_id,omitempty"`
	Discomfort    string    `json:"discomfort"`
	Expected      string    `json:"expected,omitempty"`
	SuspectedFile string    `json:"suspected_file,omitempty"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
}

type TriggerLog struct {
	EventID         string    `json:"event_id"`
	CharacterID     string    `json:"character_id"`
	TriggerID       string    `json:"trigger_id"`
	TriggerCategory string    `json:"trigger_category,omitempty"`
	Activated       bool      `json:"activated"`
	Confidence      float64   `json:"confidence,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

type CanonicalResponseLog struct {
	EventID     string    `json:"event_id"`
	CharacterID string    `json:"character_id"`
	ResponseID  string    `json:"response_id"`
	MessageID   string    `json:"message_id,omitempty"`
	Used        bool      `json:"used"`
	Rewritten   bool      `json:"rewritten"`
	CreatedAt   time.Time `json:"created_at"`
}

type ObservationLog struct {
	EventID         string    `json:"event_id"`
	ObserverID      string    `json:"observer_id"`
	TargetID        string    `json:"target_id"`
	ObservationType string    `json:"observation_type"`
	Summary         string    `json:"summary,omitempty"`
	EvidenceRefs    []string  `json:"evidence_refs,omitempty"`
	Sensitivity     string    `json:"sensitivity"`
	ReviewStatus    string    `json:"review_status"`
	CreatedAt       time.Time `json:"created_at"`
}

type MetaProfileUpdate struct {
	UpdateID        string    `json:"update_id"`
	ObserverID      string    `json:"observer_id"`
	TargetID        string    `json:"target_id"`
	Section         string    `json:"section"`
	ProposedContent string    `json:"proposed_content"`
	EvidenceRefs    []string  `json:"evidence_refs,omitempty"`
	Sensitivity     string    `json:"sensitivity"`
	ReviewStatus    string    `json:"review_status"`
	CreatedAt       time.Time `json:"created_at"`
	ReviewedAt      time.Time `json:"reviewed_at,omitempty"`
}

type InterfaceSession struct {
	SessionID     string    `json:"session_id"`
	CharacterID   string    `json:"character_id"`
	InterfaceType string    `json:"interface_type"`
	SessionKey    string    `json:"session_key"`
	WorkstreamID  string    `json:"workstream_id,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	LastUsedAt    time.Time `json:"last_used_at,omitempty"`
}

type TriggerDefinition struct {
	TriggerID   string   `json:"trigger_id"`
	CharacterID string   `json:"character_id"`
	Category    string   `json:"category"`
	Keywords    []string `json:"keywords,omitempty"`
	Priority    int      `json:"priority,omitempty"`
}

type TriggerMatch struct {
	TriggerID   string  `json:"trigger_id"`
	CharacterID string  `json:"character_id"`
	Category    string  `json:"category"`
	Confidence  float64 `json:"confidence"`
}

type CanonicalResponsePolicy struct {
	ResponseID       string   `json:"response_id"`
	CooldownTurns    int      `json:"cooldown_turns,omitempty"`
	MaxPerSession    int      `json:"max_per_session,omitempty"`
	RequiredContexts []string `json:"required_contexts,omitempty"`
}

type CanonicalResponseDefinition struct {
	ResponseID       string   `json:"response_id"`
	CharacterID      string   `json:"character_id"`
	Category         string   `json:"category"`
	Response         string   `json:"response"`
	CooldownTurns    int      `json:"cooldown_turns,omitempty"`
	MaxPerSession    int      `json:"max_per_session,omitempty"`
	RequiredContexts []string `json:"required_contexts,omitempty"`
	Priority         int      `json:"priority,omitempty"`
}
