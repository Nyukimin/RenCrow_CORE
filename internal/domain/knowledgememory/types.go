package knowledgememory

import "time"

type PersonalArchiveEntry struct {
	EntryID      string    `json:"entry_id"`
	UserID       string    `json:"user_id"`
	SourceRef    string    `json:"source_ref,omitempty"`
	OriginalText string    `json:"original_text"`
	Protected    bool      `json:"protected"`
	CreatedAt    time.Time `json:"created_at"`
}

type CreativeKnowledgeItem struct {
	ItemID       string    `json:"item_id"`
	Title        string    `json:"title"`
	CreatorNames []string  `json:"creator_names,omitempty"`
	WorkType     string    `json:"work_type,omitempty"`
	RelatedWorks []string  `json:"related_works,omitempty"`
	ContentHints []string  `json:"content_hints,omitempty"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
}

type NewsKnowledgeItem struct {
	ItemID    string    `json:"item_id"`
	Source    string    `json:"source"`
	Topic     string    `json:"topic"`
	EventDate string    `json:"event_date,omitempty"`
	URL       string    `json:"url,omitempty"`
	Summary   string    `json:"summary,omitempty"`
	Durable   bool      `json:"durable"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type DailyIntakeRule struct {
	RuleID     string    `json:"rule_id"`
	UserID     string    `json:"user_id"`
	Topic      string    `json:"topic"`
	SourceHint string    `json:"source_hint,omitempty"`
	Cadence    string    `json:"cadence"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
}

type TemporalMemoryMarker struct {
	MarkerID    string    `json:"marker_id"`
	UserID      string    `json:"user_id,omitempty"`
	Layer       string    `json:"layer"`
	ReferenceID string    `json:"reference_id"`
	Summary     string    `json:"summary"`
	AccessCount int       `json:"access_count,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type DreamConsolidationRun struct {
	RunID        string    `json:"run_id"`
	Scope        []string  `json:"scope,omitempty"`
	IdeaSeeds    []string  `json:"idea_seeds,omitempty"`
	Status       string    `json:"status"`
	ReviewStatus string    `json:"review_status"`
	CreatedAt    time.Time `json:"created_at"`
}
