package idlechat

type DialogueInterestingnessConfig struct {
	Enabled                   bool
	MaxTurnsPerTopic          int
	MinQualityScore           int
	MaxQualityRetries         int
	EnforcePreviousUptake     bool
	EnforceOneNewContribution bool
	EnforceCategoryAxis       bool
	ForbidMetaLeak            bool
	ForbidUserQuestion        bool
	Utterance                 DialogueUtteranceConfig
	PromptPaths               DialoguePromptPaths
}

type DialogueUtteranceConfig struct {
	MinRunes              int
	MaxRunes              int
	PreferredMaxSentences int
}

type DialoguePromptPaths struct {
	Common   string
	Single   string
	Double   string
	External string
	Movie    string
	News     string
	Forecast string
	Story    string
}

const MinDialogueQualityScore = 70

func DefaultDialogueInterestingnessConfig() DialogueInterestingnessConfig {
	return DialogueInterestingnessConfig{
		Enabled:                   true,
		MaxTurnsPerTopic:          maxTurnsPerTopic,
		MinQualityScore:           MinDialogueQualityScore,
		MaxQualityRetries:         4,
		EnforcePreviousUptake:     true,
		EnforceOneNewContribution: true,
		EnforceCategoryAxis:       true,
		ForbidMetaLeak:            true,
		ForbidUserQuestion:        true,
		Utterance: DialogueUtteranceConfig{
			MinRunes:              20,
			MaxRunes:              160,
			PreferredMaxSentences: 2,
		},
		PromptPaths: DialoguePromptPaths{
			Common:   "prompts/idle_chat/dialogue_common.md",
			Single:   "prompts/idle_chat/dialogue_single.md",
			Double:   "prompts/idle_chat/dialogue_double.md",
			External: "prompts/idle_chat/dialogue_external.md",
			Movie:    "prompts/idle_chat/dialogue_movie.md",
			News:     "prompts/idle_chat/dialogue_news.md",
			Forecast: "prompts/idle_chat/dialogue_forecast.md",
			Story:    "prompts/idle_chat/dialogue_story.md",
		},
	}
}

func normalizeDialogueInterestingnessConfig(config DialogueInterestingnessConfig) DialogueInterestingnessConfig {
	defaults := DefaultDialogueInterestingnessConfig()
	if !config.EnforcePreviousUptake {
		config.EnforcePreviousUptake = defaults.EnforcePreviousUptake
	}
	if !config.EnforceOneNewContribution {
		config.EnforceOneNewContribution = defaults.EnforceOneNewContribution
	}
	if !config.EnforceCategoryAxis {
		config.EnforceCategoryAxis = defaults.EnforceCategoryAxis
	}
	if !config.ForbidMetaLeak {
		config.ForbidMetaLeak = defaults.ForbidMetaLeak
	}
	if !config.ForbidUserQuestion {
		config.ForbidUserQuestion = defaults.ForbidUserQuestion
	}
	if config.MaxTurnsPerTopic <= 0 {
		config.MaxTurnsPerTopic = defaults.MaxTurnsPerTopic
	}
	if config.MinQualityScore <= 0 {
		config.MinQualityScore = defaults.MinQualityScore
	}
	if config.MaxQualityRetries <= 0 {
		config.MaxQualityRetries = defaults.MaxQualityRetries
	}
	if config.Utterance.MinRunes <= 0 {
		config.Utterance.MinRunes = defaults.Utterance.MinRunes
	}
	if config.Utterance.MaxRunes <= 0 {
		config.Utterance.MaxRunes = defaults.Utterance.MaxRunes
	}
	if config.Utterance.PreferredMaxSentences <= 0 {
		config.Utterance.PreferredMaxSentences = defaults.Utterance.PreferredMaxSentences
	}
	if config.PromptPaths.Common == "" {
		config.PromptPaths.Common = defaults.PromptPaths.Common
	}
	if config.PromptPaths.Single == "" {
		config.PromptPaths.Single = defaults.PromptPaths.Single
	}
	if config.PromptPaths.Double == "" {
		config.PromptPaths.Double = defaults.PromptPaths.Double
	}
	if config.PromptPaths.External == "" {
		config.PromptPaths.External = defaults.PromptPaths.External
	}
	if config.PromptPaths.Movie == "" {
		config.PromptPaths.Movie = defaults.PromptPaths.Movie
	}
	if config.PromptPaths.News == "" {
		config.PromptPaths.News = defaults.PromptPaths.News
	}
	if config.PromptPaths.Forecast == "" {
		config.PromptPaths.Forecast = defaults.PromptPaths.Forecast
	}
	if config.PromptPaths.Story == "" {
		config.PromptPaths.Story = defaults.PromptPaths.Story
	}
	return config
}
