package xbookmarkworkflow

import "errors"

var ErrSourceNotFound = errors.New("x bookmark source record not found")

const (
	WorkflowImagePromptDraw        = "image_prompt_draw"
	WorkflowAITipRenCrowEvaluation = "ai_tip_rencrow_evaluation"

	StatusCompleted = "completed"
	StatusSkipped   = "skipped"
	StatusBlocked   = "blocked"
	StatusError     = "error"
)

type Media struct {
	Type   string `json:"type"`
	URL    string `json:"url"`
	Alt    string `json:"alt,omitempty"`
	Poster string `json:"poster,omitempty"`
}

type SourceRecord struct {
	ID             string              `json:"id"`
	Title          string              `json:"title"`
	SourceURL      string              `json:"source_url"`
	RawText        string              `json:"raw_text"`
	AuthorName     string              `json:"author_name,omitempty"`
	AuthorUsername string              `json:"author_username,omitempty"`
	Media          []Media             `json:"media,omitempty"`
	References     []map[string]string `json:"references,omitempty"`
}

type RunRequest struct {
	Workflow       string `json:"workflow"`
	SourceRecordID string `json:"source_record_id"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

type Result struct {
	ID               string   `json:"id"`
	SourceRecordID   string   `json:"source_record_id"`
	SourceURL        string   `json:"source_url"`
	Workflow         string   `json:"workflow"`
	WorkflowRevision string   `json:"workflow_revision"`
	Status           string   `json:"status"`
	Decision         string   `json:"decision,omitempty"`
	Reason           string   `json:"reason,omitempty"`
	Prompt           string   `json:"prompt,omitempty"`
	NegativePrompt   string   `json:"negative_prompt,omitempty"`
	PromptSource     string   `json:"prompt_source,omitempty"`
	Association      string   `json:"association,omitempty"`
	ImageID          string   `json:"image_id,omitempty"`
	ImageProfile     string   `json:"image_profile,omitempty"`
	Summary          string   `json:"summary,omitempty"`
	Rationale        string   `json:"rationale,omitempty"`
	EvidenceURLs     []string `json:"evidence_urls,omitempty"`
	Improvement      string   `json:"improvement,omitempty"`
	AffectedModules  []string `json:"affected_modules,omitempty"`
	Prerequisites    []string `json:"prerequisites,omitempty"`
	Cost             string   `json:"cost,omitempty"`
	Risks            []string `json:"risks,omitempty"`
	ValidationPlan   []string `json:"validation_plan,omitempty"`
	Priority         string   `json:"priority,omitempty"`
	BacklogItemID    string   `json:"backlog_item_id,omitempty"`
	FailureStage     string   `json:"failure_stage,omitempty"`
	Error            string   `json:"error,omitempty"`
	ExecutionAlias   string   `json:"execution_alias"`
	CreatedAt        string   `json:"created_at"`
	UpdatedAt        string   `json:"updated_at"`
}

type ResultQuery struct {
	SourceRecordID string
	Workflow       string
	Limit          int
}
