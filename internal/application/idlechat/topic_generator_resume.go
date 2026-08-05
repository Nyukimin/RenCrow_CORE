package idlechat

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
)

type TopicGenerationResumeState struct {
	Attempt    int
	Candidates []TopicCandidate
	Result     *TopicGenerationResult
}

type TopicGenerationProgress func(TopicGenerationResumeState) error

// GenerateInterestingTopicResumable is the durable-stage variant used by
// background Stock generation. Candidate generation and judging are separate
// resume boundaries; the foreground conversation may cancel between them.
func (g *TopicGenerator) GenerateInterestingTopicResumable(
	ctx context.Context,
	category TopicCategory,
	seed TopicSeed,
	recent []RecentTopic,
	resume TopicGenerationResumeState,
	progress TopicGenerationProgress,
) (*TopicGenerationResult, error) {
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
		return nil, err
	}
	if resume.Result != nil {
		result := *resume.Result
		return &result, nil
	}
	if resume.Attempt < 1 {
		resume.Attempt = 1
	}
	var lastErr error
	for attempt := resume.Attempt; attempt <= g.config.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		validCandidates := []TopicCandidate(nil)
		if attempt == resume.Attempt && len(resume.Candidates) > 0 {
			validCandidates = append(validCandidates, resume.Candidates...)
		} else {
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
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				lastErr = err
				continue
			}
			logIdleRaw(fmt.Sprintf("topic.candidates.generate attempt=%d category=%s", attempt, normalized), resp.Content)
			candidates, err := modulechat.ParseTopicCandidates(resp.Content)
			if err != nil {
				lastErr = err
				continue
			}
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
					Category: string(normalized), Strategy: modulechat.StrategyFromTopicCategory(normalized), Attempt: attempt,
					ErrorCode: ErrTopicGenerationNoCandidates.Error(), SeedSummary: summarizeTopicSeed(seed),
					CandidateCount: len(candidates), InvalidCandidates: invalids,
				})
				continue
			}
			if progress != nil {
				if err := progress(TopicGenerationResumeState{Attempt: attempt, Candidates: validCandidates}); err != nil {
					return nil, err
				}
			}
		}

		winner, judge, err := g.JudgeCandidates(ctx, normalized, seed, recent, validCandidates)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = err
			resume.Candidates = nil
			continue
		}
		if err := modulechat.ValidateTopicCandidate(normalized, seed, winner); err != nil {
			lastErr = err
			resume.Candidates = nil
			continue
		}
		if err := modulechat.CheckRecentTopicSimilarity(winner.Topic, recent, g.config.RecentSimilarity); err != nil {
			lastErr = err
			resume.Candidates = nil
			continue
		}
		result := &TopicGenerationResult{
			Topic: winner.Topic, Category: normalized, Strategy: modulechat.StrategyFromTopicCategory(normalized),
			InterestingnessAxis: winner.InterestingnessAxis, OpeningHook: winner.OpeningHook, Avoid: winner.Avoid,
			Seed: seed, Candidates: validCandidates, Judge: judge, Provider: g.providerName(),
		}
		if progress != nil {
			if err := progress(TopicGenerationResumeState{Attempt: attempt, Candidates: validCandidates, Result: result}); err != nil {
				return nil, err
			}
		}
		logTopicGenerated(result, attempt)
		return result, nil
	}
	log.Printf("[IdleChat] resumable topic generation exhausted: category=%s error=%v", normalized, lastErr)
	if lastErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrTopicGenerationFailed, lastErr)
	}
	return nil, ErrTopicGenerationFailed
}
