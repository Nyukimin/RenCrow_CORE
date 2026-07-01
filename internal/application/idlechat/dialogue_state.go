package idlechat

type DialogueSpeakerRole struct {
	Speaker       string   `json:"speaker"`
	PrimaryMove   string   `json:"primary_move"`
	SecondaryMove string   `json:"secondary_move,omitempty"`
	Avoid         []string `json:"avoid,omitempty"`
}

type DialogueTurnPlan struct {
	TurnIndex        int      `json:"turn_index"`
	Phase            string   `json:"phase"`
	RequiredMove     string   `json:"required_move"`
	PreferredSpeaker string   `json:"preferred_speaker,omitempty"`
	Avoid            []string `json:"avoid,omitempty"`
}

type DialogueArcPlan struct {
	Topic               string                         `json:"topic"`
	Category            TopicCategory                  `json:"category"`
	Strategy            string                         `json:"strategy"`
	InterestingnessAxis string                         `json:"interestingness_axis"`
	CoreQuestion        string                         `json:"core_question"`
	OpeningMove         string                         `json:"opening_move"`
	DevelopmentMoves    []string                       `json:"development_moves"`
	DeepeningMoves      []string                       `json:"deepening_moves"`
	ClosingMove         string                         `json:"closing_move"`
	ForbiddenMoves      []string                       `json:"forbidden_moves"`
	SpeakerRoles        map[string]DialogueSpeakerRole `json:"speaker_roles"`
	TurnPlans           []DialogueTurnPlan             `json:"turn_plans"`
}

type DialogueArcState struct {
	SessionID        string        `json:"session_id"`
	Topic            string        `json:"topic"`
	Category         TopicCategory `json:"category"`
	TurnIndex        int           `json:"turn_index"`
	Phase            string        `json:"phase"`
	EstablishedFacts []string      `json:"established_facts,omitempty"`
	OpenQuestions    []string      `json:"open_questions,omitempty"`
	TensionPoints    []string      `json:"tension_points,omitempty"`
	ConcreteAnchors  []string      `json:"concrete_anchors,omitempty"`
	UsedMoves        []string      `json:"used_moves,omitempty"`
	DullnessWarnings []string      `json:"dullness_warnings,omitempty"`
}
