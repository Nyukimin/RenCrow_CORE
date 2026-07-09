package idlechat

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
)

func (g *TopicGenerator) BuildGenerationPrompt(category TopicCategory, seed TopicSeed, recent []RecentTopic, attempt int, lastErr error) (string, error) {
	paths := g.config.PromptPaths
	if strings.TrimSpace(paths.Common) == "" || strings.TrimSpace(promptPathForCategory(paths, category)) == "" {
		return modulechat.BuildTopicGenerationPrompt(category, seed, recent, g.config.CandidatesPerAttempt, attempt, lastErr)
	}
	common, err := os.ReadFile(paths.Common)
	if err != nil {
		return "", fmt.Errorf("read topic generator common prompt: %w", err)
	}
	categoryPrompt, err := os.ReadFile(promptPathForCategory(paths, category))
	if err != nil {
		return "", fmt.Errorf("read topic generator category prompt: %w", err)
	}
	seedJSON, _ := json.MarshalIndent(seed, "", "  ")
	recentJSON, _ := json.MarshalIndent(recent, "", "  ")
	prompt := string(common) + "\n\n" + string(categoryPrompt)
	prompt = renderTopicPromptPlaceholders(prompt, map[string]string{
		"candidate_count":    fmt.Sprint(g.config.CandidatesPerAttempt),
		"category":           string(category),
		"seed_json":          string(seedJSON),
		"recent_topics_json": string(recentJSON),
	})
	if attempt >= 2 {
		prompt += fmt.Sprintf("\n\n再生成条件:\n- attempt=%d。\n- 前回失敗理由を避ける。\n", attempt)
		if lastErr != nil {
			prompt += "- 前回失敗理由: " + strings.TrimSpace(lastErr.Error()) + "\n"
		}
	}
	return prompt, nil
}

func (g *TopicGenerator) BuildJudgePrompt(category TopicCategory, seed TopicSeed, recent []RecentTopic, candidates []TopicCandidate) (string, error) {
	if strings.TrimSpace(g.config.PromptPaths.Judge) == "" {
		return modulechat.BuildTopicJudgePrompt(category, seed, recent, candidates)
	}
	template, err := os.ReadFile(g.config.PromptPaths.Judge)
	if err != nil {
		return "", fmt.Errorf("read topic judge prompt: %w", err)
	}
	seedJSON, _ := json.MarshalIndent(seed, "", "  ")
	recentJSON, _ := json.MarshalIndent(recent, "", "  ")
	candidatesJSON, _ := json.MarshalIndent(candidates, "", "  ")
	return renderTopicPromptPlaceholders(string(template), map[string]string{
		"category":           string(category),
		"seed_json":          string(seedJSON),
		"recent_topics_json": string(recentJSON),
		"candidates_json":    string(candidatesJSON),
	}), nil
}

func promptPathForCategory(paths TopicGenerationPromptPaths, category TopicCategory) string {
	switch category {
	case TopicCategorySingle:
		return paths.Single
	case TopicCategoryDouble:
		return paths.Double
	case TopicCategoryExternal:
		return paths.External
	case TopicCategoryMovie:
		return paths.Movie
	case TopicCategoryNews:
		return paths.News
	case TopicCategoryForecast:
		return paths.Forecast
	case TopicCategoryStory:
		return paths.Story
	default:
		return ""
	}
}

func renderTopicPromptPlaceholders(template string, values map[string]string) string {
	return modulechat.RenderTopicPromptPlaceholders(template, values)
}

func topicCategoryGenerationRules(category TopicCategory) string {
	return modulechat.TopicCategoryGenerationRules(category)
}
