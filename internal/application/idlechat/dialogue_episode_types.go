package idlechat

import "time"

const DialogueEpisodeSchemaVersion = 1

type DialogueProductionStatus string

const (
	DialogueProductionValidating  DialogueProductionStatus = "validating"
	DialogueProductionReady       DialogueProductionStatus = "ready"
	DialogueProductionNeedsRepair DialogueProductionStatus = "needs_repair"
	DialogueProductionFailed      DialogueProductionStatus = "failed"
)

// DialogueEpisodeTurn is one pre-generated, validated Mio/Shiro utterance.
type DialogueEpisodeTurn struct {
	TurnIndex   int    `json:"turn_index"`
	MessageID   string `json:"message_id"`
	Speaker     string `json:"speaker"`
	DisplayText string `json:"display_text"`
	SpeechText  string `json:"speech_text"`
}

type DialogueTurnValidationError struct {
	Code      string                      `json:"code"`
	TurnIndex int                         `json:"turn_index"`
	Evidence  string                      `json:"evidence"`
	Reasons   []IdleDialogueQualityReason `json:"reasons,omitempty"`
}

type DialogueEpisodeValidation struct {
	Valid            bool                          `json:"valid"`
	FirstInvalidTurn int                           `json:"first_invalid_turn,omitempty"`
	Errors           []DialogueTurnValidationError `json:"errors,omitempty"`
}

// DialogueEpisodeArtifact retains every generated revision, including invalid
// ones, so suffix repair is independently auditable.
type DialogueEpisodeArtifact struct {
	SchemaVersion       int                       `json:"schema_version"`
	EpisodeID           string                    `json:"episode_id"`
	GenerationID        string                    `json:"generation_id"`
	Revision            int                       `json:"revision"`
	SessionID           string                    `json:"session_id"`
	InitiatedBy         string                    `json:"initiated_by"`
	TopicResult         TopicGenerationResult     `json:"topic_result"`
	ArcPlan             DialogueArcPlan           `json:"arc_plan"`
	Participants        []string                  `json:"participants"`
	Turns               []DialogueEpisodeTurn     `json:"turns"`
	ProductionStatus    DialogueProductionStatus  `json:"production_status"`
	Validation          DialogueEpisodeValidation `json:"validation"`
	FixedPrefixLength   int                       `json:"fixed_prefix_length,omitempty"`
	RepairFromTurn      int                       `json:"repair_from_turn,omitempty"`
	SuffixRegenerations int                       `json:"suffix_regenerations"`
	CreatedAt           time.Time                 `json:"created_at"`
	UpdatedAt           time.Time                 `json:"updated_at"`
}
