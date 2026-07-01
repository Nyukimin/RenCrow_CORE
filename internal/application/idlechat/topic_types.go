package idlechat

import (
	modulechat "github.com/Nyukimin/picoclaw_multiLLM/modules/chat"
)

type TopicCategory = modulechat.TopicCategory

const (
	TopicCategorySingle   = modulechat.TopicCategorySingle
	TopicCategoryDouble   = modulechat.TopicCategoryDouble
	TopicCategoryExternal = modulechat.TopicCategoryExternal
	TopicCategoryMovie    = modulechat.TopicCategoryMovie
	TopicCategoryNews     = modulechat.TopicCategoryNews
	TopicCategoryForecast = modulechat.TopicCategoryForecast
	TopicCategoryStory    = modulechat.TopicCategoryStory
)

var (
	ErrUnsupportedTopicCategory    = modulechat.ErrUnsupportedTopicCategory
	ErrTopicSeedUnavailable        = modulechat.ErrTopicSeedUnavailable
	ErrTopicGenerationInvalidJSON  = modulechat.ErrTopicGenerationInvalidJSON
	ErrTopicGenerationNoCandidates = modulechat.ErrTopicGenerationNoCandidates
	ErrTopicContractViolation      = modulechat.ErrTopicContractViolation
	ErrTopicJudgeInvalidJSON       = modulechat.ErrTopicJudgeInvalidJSON
	ErrTopicJudgeWinnerMissing     = modulechat.ErrTopicJudgeWinnerMissing
	ErrTopicJudgeLowScore          = modulechat.ErrTopicJudgeLowScore
	ErrRecentTopicExactDuplicate   = modulechat.ErrRecentTopicExactDuplicate
	ErrRecentTopicTooSimilar       = modulechat.ErrRecentTopicTooSimilar
	ErrTopicGenerationFailed       = modulechat.ErrTopicGenerationFailed
)

type TopicSeed = modulechat.TopicSeed
type ExternalMaterialSeed = modulechat.ExternalMaterialSeed
type RecentTopic = modulechat.RecentTopic
type TopicCandidate = modulechat.TopicCandidate
type TopicJudgeResult = modulechat.TopicJudgeResult
type TopicJudgeScore = modulechat.TopicJudgeScore
type TopicGenerationResult = modulechat.TopicGenerationResult
type TopicGenerationDiagnostic = modulechat.TopicGenerationDiagnostic
type InvalidCandidateDiagnostic = modulechat.InvalidCandidateDiagnostic
