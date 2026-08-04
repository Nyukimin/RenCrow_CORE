package idlechat

import "time"

const (
	StoryEpisodeKind           = "story_reading"
	StoryEpisodeSchemaVersion  = "rencrow.idlechat.story-script.v1"
	StoryUtteranceNarration    = "narration"
	StoryUtteranceInterjection = "interjection"
	StoryProductionGenerating  = "generating"
	StoryProductionValidating  = "validating"
	StoryProductionNeedsRepair = "needs_repair"
	StoryProductionReady       = "ready"
	StoryProductionFailed      = "failed"
)

type StoryEpisodeArtifact struct {
	SchemaVersion           string                `json:"schema_version"`
	EpisodeID               string                `json:"episode_id"`
	Revision                int                   `json:"revision"`
	EpisodeKind             string                `json:"episode_kind"`
	GenerationID            string                `json:"generation_id"`
	ReplacementForEpisodeID string                `json:"replacement_for_episode_id,omitempty"`
	Source                  StoryEpisodeSource    `json:"source"`
	Reader                  string                `json:"reader"`
	Listener                string                `json:"listener"`
	Contract                StoryEpisodeContract  `json:"story_contract"`
	Ledger                  StoryEpisodeLedger    `json:"story_ledger"`
	Turns                   []StoryEpisodeTurn    `json:"turns"`
	ProductionStatus        string                `json:"production_status"`
	Validation              StoryValidationResult `json:"validation"`
	FixedPrefixLength       int                   `json:"fixed_prefix_length,omitempty"`
	RepairFromTurn          int                   `json:"repair_from_turn,omitempty"`
	SuffixRegenerations     int                   `json:"suffix_regenerations,omitempty"`
	PlayCount               int                   `json:"play_count,omitempty"`
	LastPlayedAt            *time.Time            `json:"last_played_at,omitempty"`
	CreatedAt               time.Time             `json:"created_at"`
	UpdatedAt               time.Time             `json:"updated_at"`
}

type StoryEpisodeSource struct {
	Title    string `json:"title"`
	Synopsis string `json:"synopsis"`
}

type StoryEpisodeContract struct {
	TransformationAxis string   `json:"transformation_axis"`
	Genre              string   `json:"genre"`
	InterestDirection  string   `json:"interest_direction"`
	InterestContract   []string `json:"interest_contract"`
	ContentMode        string   `json:"content_mode"`
}

type StoryEpisodeLedger struct {
	Entities    []StoryLedgerEntity   `json:"entities"`
	Relations   []StoryLedgerRelation `json:"relations"`
	WorldRules  []string              `json:"world_rules"`
	CoinedTerms []StoryLedgerTerm     `json:"coined_terms"`
}

type StoryLedgerEntity struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Reading string `json:"reading"`
	Role    string `json:"role"`
}

type StoryLedgerRelation struct {
	Subject  string `json:"subject"`
	Relation string `json:"relation"`
	Object   string `json:"object"`
}

type StoryLedgerTerm struct {
	Surface string `json:"surface"`
	Reading string `json:"reading"`
	Meaning string `json:"meaning"`
}

type StoryEpisodeTurn struct {
	TurnIndex     int    `json:"turn_index"`
	MessageID     string `json:"message_id,omitempty"`
	Speaker       string `json:"speaker"`
	UtteranceRole string `json:"utterance_role"`
	ReactsTo      int    `json:"reacts_to,omitempty"`
	DisplayText   string `json:"display_text"`
	SpeechText    string `json:"speech_text"`
}

type StorySemanticReview struct {
	Valid  bool                   `json:"valid"`
	Errors []StoryValidationError `json:"errors"`
}

type StoryValidationResult struct {
	Valid            bool                   `json:"valid"`
	FirstInvalidTurn int                    `json:"first_invalid_turn,omitempty"`
	Errors           []StoryValidationError `json:"errors,omitempty"`
}

type StoryValidationError struct {
	Code      string `json:"code"`
	TurnIndex int    `json:"turn_index,omitempty"`
	Field     string `json:"field,omitempty"`
	Evidence  string `json:"evidence,omitempty"`
}

type StoryEpisodeStockSnapshot struct {
	Enabled            bool                   `json:"enabled"`
	Ready              int                    `json:"ready"`
	Target             int                    `json:"target"`
	Missing            int                    `json:"missing"`
	NeedsRepair        int                    `json:"needs_repair"`
	Failed             int                    `json:"failed"`
	Filling            bool                   `json:"filling"`
	GenerationAttempts int                    `json:"generation_attempts"`
	LastFailurePhase   string                 `json:"last_failure_phase,omitempty"`
	LastError          string                 `json:"last_error,omitempty"`
	Episodes           []StoryEpisodeArtifact `json:"episodes,omitempty"`
}
