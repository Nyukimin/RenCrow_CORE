package idlechat

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
	modulechat "github.com/Nyukimin/picoclaw_multiLLM/modules/chat"
)

type TopicGenerationConfig struct {
	Enabled              bool
	CandidatesPerAttempt int
	MaxAttempts          int
	JudgeEnabled         bool
	MinJudgeTotal        int
	MinCategoryFit       int
	MinSafety            int
	RecentTopicWindow    int
	RecentSimilarity     float64
	LogCandidates        bool
	LogJudgeScores       bool
	ProviderName         string
	PromptPaths          TopicGenerationPromptPaths
}

type TopicGenerationPromptPaths struct {
	Common   string
	Single   string
	Double   string
	External string
	Movie    string
	News     string
	Forecast string
	Story    string
	Judge    string
}

type TopicGenerator struct {
	llm    llm.LLMProvider
	config TopicGenerationConfig
}

func NewTopicGenerator(provider llm.LLMProvider, config TopicGenerationConfig) *TopicGenerator {
	config = normalizeTopicGenerationConfig(config)
	return &TopicGenerator{llm: provider, config: config}
}

func normalizeTopicGenerationConfig(config TopicGenerationConfig) TopicGenerationConfig {
	if config.CandidatesPerAttempt <= 0 {
		config.CandidatesPerAttempt = 5
	}
	if config.MaxAttempts <= 0 {
		config.MaxAttempts = 3
	}
	if config.MinJudgeTotal <= 0 {
		config.MinJudgeTotal = modulechat.MinJudgeTotal
	}
	if config.MinCategoryFit <= 0 {
		config.MinCategoryFit = modulechat.MinCategoryFit
	}
	if config.MinSafety <= 0 {
		config.MinSafety = modulechat.MinSafety
	}
	if config.RecentTopicWindow <= 0 {
		config.RecentTopicWindow = 12
	}
	if config.RecentSimilarity <= 0 {
		config.RecentSimilarity = modulechat.RecentTopicSimilarityThreshold
	}
	return config
}

func (g *TopicGenerator) GenerateInterestingTopic(ctx context.Context, category TopicCategory, seed TopicSeed, recent []RecentTopic) (*TopicGenerationResult, error) {
	if g == nil || g.llm == nil {
		return nil, fmt.Errorf("%w: topic generator provider unavailable", ErrTopicGenerationFailed)
	}
	normalized, err := modulechat.NormalizeTopicCategory(string(category))
	if err != nil {
		return nil, err
	}
	seed.Category = normalized
	seed.RecentTopics = recent
	if err := modulechat.ValidateSeedForCategory(normalized, seed); err != nil {
		logTopicDiagnostic(TopicGenerationDiagnostic{
			Category:     string(normalized),
			Strategy:     modulechat.StrategyFromTopicCategory(normalized),
			ErrorCode:    errorCodeForTopicGeneration(err),
			ErrorMessage: err.Error(),
			SeedSummary:  summarizeTopicSeed(seed),
		})
		return nil, err
	}

	var lastErr error
	for attempt := 1; attempt <= g.config.MaxAttempts; attempt++ {
		prompt, err := g.BuildGenerationPrompt(normalized, seed, recent, attempt, lastErr)
		if err != nil {
			return nil, err
		}
		resp, err := g.llm.Generate(ctx, llm.GenerateRequest{
			Messages: []llm.Message{
				{Role: "system", Content: topicGeneratorSystemPrompt()},
				{Role: "user", Content: prompt},
			},
			MaxTokens:       900,
			Temperature:     0.85 + float64(attempt-1)*0.05,
			ProviderOptions: map[string]any{"think": false},
		})
		if err != nil {
			lastErr = err
			logTopicDiagnostic(TopicGenerationDiagnostic{
				Category:     string(normalized),
				Strategy:     modulechat.StrategyFromTopicCategory(normalized),
				Attempt:      attempt,
				ErrorCode:    errorCodeForTopicGeneration(err),
				ErrorMessage: err.Error(),
				SeedSummary:  summarizeTopicSeed(seed),
			})
			continue
		}
		logIdleRaw(fmt.Sprintf("topic.candidates.generate attempt=%d category=%s", attempt, normalized), resp.Content)
		candidates, err := modulechat.ParseTopicCandidates(resp.Content)
		if err != nil {
			lastErr = err
			continue
		}

		validCandidates := make([]TopicCandidate, 0, len(candidates))
		invalids := make([]InvalidCandidateDiagnostic, 0)
		for _, candidate := range candidates {
			candidate.Topic = normalizeIdleTopic(candidate.Topic, normalized == TopicCategoryMovie)
			if strings.TrimSpace(candidate.InterestingnessAxis) == "" {
				candidate.InterestingnessAxis = modulechat.ExpectedAxisByCategory[normalized]
			}
			if err := modulechat.ValidateTopicCandidate(normalized, seed, candidate); err != nil {
				invalids = append(invalids, InvalidCandidateDiagnostic{Topic: candidate.Topic, Error: err.Error()})
				continue
			}
			if err := modulechat.CheckRecentTopicSimilarity(candidate.Topic, recent, g.config.RecentSimilarity); err != nil {
				invalids = append(invalids, InvalidCandidateDiagnostic{Topic: candidate.Topic, Error: err.Error()})
				continue
			}
			validCandidates = append(validCandidates, candidate)
		}
		if len(validCandidates) == 0 {
			lastErr = ErrTopicGenerationNoCandidates
			logTopicDiagnostic(TopicGenerationDiagnostic{
				Category:          string(normalized),
				Strategy:          modulechat.StrategyFromTopicCategory(normalized),
				Attempt:           attempt,
				ErrorCode:         ErrTopicGenerationNoCandidates.Error(),
				SeedSummary:       summarizeTopicSeed(seed),
				CandidateCount:    len(candidates),
				InvalidCandidates: invalids,
			})
			continue
		}

		winner, judge, err := g.JudgeCandidates(ctx, normalized, seed, recent, validCandidates)
		if err != nil {
			lastErr = err
			continue
		}
		if err := modulechat.ValidateTopicCandidate(normalized, seed, winner); err != nil {
			lastErr = err
			continue
		}
		if err := modulechat.CheckRecentTopicSimilarity(winner.Topic, recent, g.config.RecentSimilarity); err != nil {
			lastErr = err
			continue
		}
		result := &TopicGenerationResult{
			Topic:               winner.Topic,
			Category:            normalized,
			Strategy:            modulechat.StrategyFromTopicCategory(normalized),
			InterestingnessAxis: winner.InterestingnessAxis,
			OpeningHook:         winner.OpeningHook,
			Avoid:               winner.Avoid,
			Seed:                seed,
			Candidates:          validCandidates,
			Judge:               judge,
			Provider:            g.providerName(),
		}
		logTopicGenerated(result, attempt)
		return result, nil
	}
	logTopicDiagnostic(TopicGenerationDiagnostic{
		Category:     string(normalized),
		Strategy:     modulechat.StrategyFromTopicCategory(normalized),
		Attempt:      g.config.MaxAttempts,
		ErrorCode:    ErrTopicGenerationFailed.Error(),
		ErrorMessage: strings.TrimSpace(fmt.Sprint(lastErr)),
		SeedSummary:  summarizeTopicSeed(seed),
	})
	if lastErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrTopicGenerationFailed, lastErr)
	}
	return nil, ErrTopicGenerationFailed
}

func (g *TopicGenerator) JudgeCandidates(ctx context.Context, category TopicCategory, seed TopicSeed, recent []RecentTopic, candidates []TopicCandidate) (TopicCandidate, *TopicJudgeResult, error) {
	if len(candidates) == 0 {
		return TopicCandidate{}, nil, ErrTopicGenerationNoCandidates
	}
	if !g.config.JudgeEnabled {
		return candidates[0], nil, nil
	}
	prompt, err := g.BuildJudgePrompt(category, seed, recent, candidates)
	if err != nil {
		return TopicCandidate{}, nil, err
	}
	resp, err := g.llm.Generate(ctx, llm.GenerateRequest{
		Messages: []llm.Message{
			{Role: "system", Content: topicJudgeSystemPrompt()},
			{Role: "user", Content: prompt},
		},
		MaxTokens:       900,
		Temperature:     0.2,
		ProviderOptions: map[string]any{"think": false},
	})
	if err != nil {
		return TopicCandidate{}, nil, err
	}
	logIdleRaw(fmt.Sprintf("topic.judge category=%s", category), resp.Content)
	judge, err := modulechat.ParseTopicJudgeResult(resp.Content)
	if err != nil {
		return TopicCandidate{}, nil, err
	}
	winner, score, err := modulechat.ValidateJudgeResultWithThresholds(judge, candidates, g.config.MinJudgeTotal, g.config.MinCategoryFit, g.config.MinSafety)
	if err != nil {
		logTopicDiagnostic(TopicGenerationDiagnostic{
			Category:     string(category),
			Strategy:     modulechat.StrategyFromTopicCategory(category),
			ErrorCode:    errorCodeForTopicGeneration(err),
			ErrorMessage: err.Error(),
			WinnerTopic:  judge.WinnerTopic,
			JudgeTotal:   score.Total,
			SeedSummary:  summarizeTopicSeed(seed),
		})
		return TopicCandidate{}, &judge, err
	}
	return winner, &judge, nil
}

func (g *TopicGenerator) providerName() string {
	if name := strings.TrimSpace(g.config.ProviderName); name != "" {
		return name
	}
	if g.llm != nil {
		return strings.TrimSpace(g.llm.Name())
	}
	return ""
}

func topicGeneratorSystemPrompt() string {
	return "あなたはRenCrow IdleChatのtopic generatorです。出力はJSONのみ。候補生成だけを行い、カテゴリ判定・Viewer表示・TTS読み上げ・ログ記録は実装コードに任せます。"
}

func topicJudgeSystemPrompt() string {
	return "あなたはRenCrow IdleChatのtopic judgeです。候補に存在するtopicだけを採点し、winner_topicを1つ選びます。出力はJSONのみ。"
}

func logTopicGenerated(result *TopicGenerationResult, attempt int) {
	if result == nil {
		return
	}
	total := 0
	if result.Judge != nil {
		for _, score := range result.Judge.Scores {
			if strings.TrimSpace(score.Topic) == result.Topic {
				total = score.Total
				break
			}
		}
	}
	data := map[string]any{
		"event":                "idlechat.topic.generated",
		"category":             result.Category,
		"strategy":             result.Strategy,
		"topic":                result.Topic,
		"interestingness_axis": result.InterestingnessAxis,
		"opening_hook":         result.OpeningHook,
		"avoid":                result.Avoid,
		"provider":             result.Provider,
		"attempt":              attempt,
		"candidate_count":      len(result.Candidates),
		"judge_total":          total,
		"seed":                 result.Seed,
	}
	payload, _ := json.Marshal(data)
	log.Printf("[IdleChat] %s", payload)
}

func logTopicDiagnostic(diag TopicGenerationDiagnostic) {
	payload, _ := json.Marshal(map[string]any{
		"event":              "idlechat.topic.generation_failed",
		"category":           diag.Category,
		"strategy":           diag.Strategy,
		"attempt":            diag.Attempt,
		"error_code":         diag.ErrorCode,
		"message":            diag.ErrorMessage,
		"seed_summary":       diag.SeedSummary,
		"candidate_count":    diag.CandidateCount,
		"invalid_candidates": diag.InvalidCandidates,
		"winner_topic":       diag.WinnerTopic,
		"judge_total":        diag.JudgeTotal,
	})
	log.Printf("[IdleChat] %s", payload)
}

func errorCodeForTopicGeneration(err error) string {
	if err == nil {
		return ""
	}
	for _, target := range []error{
		ErrUnsupportedTopicCategory,
		ErrTopicSeedUnavailable,
		ErrTopicGenerationInvalidJSON,
		ErrTopicGenerationNoCandidates,
		ErrTopicContractViolation,
		ErrTopicJudgeInvalidJSON,
		ErrTopicJudgeWinnerMissing,
		ErrTopicJudgeLowScore,
		ErrRecentTopicExactDuplicate,
		ErrRecentTopicTooSimilar,
		ErrTopicGenerationFailed,
	} {
		if strings.Contains(err.Error(), target.Error()) {
			return target.Error()
		}
	}
	return "provider_error"
}

func summarizeTopicSeed(seed TopicSeed) string {
	parts := []string{string(seed.Category)}
	if seed.Genre1 != "" {
		parts = append(parts, "genre_1="+seed.Genre1)
	}
	if seed.Genre2 != "" {
		parts = append(parts, "genre_2="+seed.Genre2)
	}
	if seed.ExternalMaterial != nil {
		parts = append(parts, "external="+seed.ExternalMaterial.Title)
	}
	if seed.News != nil {
		parts = append(parts, "news="+seed.News.Title)
	}
	if seed.ForecastDomain != "" {
		parts = append(parts, "forecast_domain="+seed.ForecastDomain)
	}
	if seed.StoryBase != "" {
		parts = append(parts, "story_base="+seed.StoryBase)
	}
	return strings.Join(parts, " ")
}
