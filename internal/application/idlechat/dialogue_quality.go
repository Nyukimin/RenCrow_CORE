package idlechat

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"unicode/utf8"

	modulechat "github.com/Nyukimin/picoclaw_multiLLM/modules/chat"
)

type IdleDialogueQualityReason string

const (
	DialogueNoUptake            IdleDialogueQualityReason = "dialogue_no_uptake"
	DialogueNoNewContribution   IdleDialogueQualityReason = "dialogue_no_new_contribution"
	DialogueTooGeneric          IdleDialogueQualityReason = "dialogue_too_generic"
	DialogueCategoryAxisMissing IdleDialogueQualityReason = "dialogue_category_axis_missing"
	DialogueOverExplained       IdleDialogueQualityReason = "dialogue_over_explained"
	DialogueMetaLeak            IdleDialogueQualityReason = "dialogue_meta_leak"
	DialogueTurnMoveMissing     IdleDialogueQualityReason = "dialogue_turn_move_missing"
)

type DialogueQualityResult struct {
	OK      bool                        `json:"ok"`
	Score   int                         `json:"score"`
	Reasons []IdleDialogueQualityReason `json:"reasons"`
	Notes   []string                    `json:"notes,omitempty"`
}

type DialogueQualityInput struct {
	Category    TopicCategory
	Utterance   string
	LatestOther string
	LatestSelf  string
	State       DialogueArcState
	TurnPlan    DialogueTurnPlan
	Config      DialogueInterestingnessConfig
}

type DialogueQualityChecker struct {
	config DialogueInterestingnessConfig
}

func NewDialogueQualityChecker(config DialogueInterestingnessConfig) *DialogueQualityChecker {
	return &DialogueQualityChecker{config: normalizeDialogueInterestingnessConfig(config)}
}

func (c *DialogueQualityChecker) Check(input DialogueQualityInput) DialogueQualityResult {
	config := normalizeDialogueInterestingnessConfig(input.Config)
	if config.MinQualityScore <= 0 {
		config = c.config
	}
	utterance := strings.TrimSpace(input.Utterance)
	score := 100
	var reasons []IdleDialogueQualityReason
	if config.ForbidMetaLeak && hasDialogueMetaLeak(utterance) {
		reasons = append(reasons, DialogueMetaLeak)
		score -= 50
	}
	if config.ForbidUserQuestion && containsAny(utterance, "ユーザーに", "ユーザーは", "あなたはどう", "あなたなら") {
		reasons = append(reasons, DialogueMetaLeak)
		score -= 20
	}
	if config.EnforcePreviousUptake && strings.TrimSpace(input.LatestOther) != "" && !hasDialogueUptake(utterance, input.LatestOther) {
		reasons = append(reasons, DialogueNoUptake)
		score -= 20
	}
	if config.EnforceOneNewContribution && lacksNewContribution(utterance, input.LatestOther, input.LatestSelf) {
		reasons = append(reasons, DialogueNoNewContribution)
		score -= 20
	}
	if tooGenericDialogue(utterance) {
		reasons = append(reasons, DialogueTooGeneric)
		score -= 20
	}
	if overExplainedDialogue(utterance, config.Utterance) {
		reasons = append(reasons, DialogueOverExplained)
		score -= 10
	}
	if config.EnforceCategoryAxis {
		categoryReasons := CheckCategoryAxis(input.Category, utterance, input.State)
		if len(categoryReasons) > 0 {
			reasons = append(reasons, categoryReasons...)
			score -= 20
		}
	}
	if strings.TrimSpace(input.TurnPlan.RequiredMove) != "" && !satisfiesTurnMove(input.TurnPlan.RequiredMove, utterance) {
		reasons = append(reasons, DialogueTurnMoveMissing)
		score -= 10
	}
	if score < 0 {
		score = 0
	}
	ok := score >= config.MinQualityScore && !containsDialogueReason(reasons, DialogueMetaLeak)
	return DialogueQualityResult{OK: ok, Score: score, Reasons: uniqueDialogueReasons(reasons)}
}

func CheckCategoryAxis(category TopicCategory, utterance string, state DialogueArcState) []IdleDialogueQualityReason {
	text := strings.TrimSpace(utterance)
	switch category {
	case TopicCategorySingle:
		if containsAny(text, "手", "棚", "店", "部屋", "場面", "違和感", "迷", "判断", "細部") {
			return nil
		}
	case TopicCategoryDouble:
		if containsAny(text, "共通", "構造", "制約", "設計", "似て", "どちら", "両方", "一方") || mentionsTopicToken(text, state.Topic) {
			return nil
		}
	case TopicCategoryExternal:
		if !hasExternalMetaLeak(text) && (mentionsTopicToken(text, state.Topic) || containsAny(text, "素材", "形", "記録", "意味", "接点")) {
			return nil
		}
	case TopicCategoryMovie:
		if containsAny(text, "映像", "主人公", "場面", "ラスト", "小道具", "葛藤", "余韻", "映写", "タイトル") {
			return nil
		}
	case TopicCategoryNews:
		if containsAny(text, "影響", "背景", "論点", "現場", "制度", "生活", "判断", "不確か", "立場") {
			return nil
		}
	case TopicCategoryForecast:
		if containsAny(text, "兆し", "変化", "変える", "影響", "主体", "分岐", "変数", "メカニズム", "今後") {
			return nil
		}
	case TopicCategoryStory:
		if containsAny(text, "語り", "視点", "元話", "場面", "反転", "善悪", "記録", "鬼", "桃太郎") || mentionsTopicToken(text, state.Topic) {
			return nil
		}
	default:
		return nil
	}
	return []IdleDialogueQualityReason{DialogueCategoryAxisMissing}
}

func dialogueQualityError(result DialogueQualityResult) error {
	payload, _ := json.Marshal(result)
	return fmt.Errorf("%w: dialogue_quality_failed %s", errIdleInvalidResponse, payload)
}

func logDialogueTurnQuality(sessionID, speaker string, category TopicCategory, plan DialogueTurnPlan, result DialogueQualityResult, retryCount int) {
	payload, _ := json.Marshal(map[string]any{
		"event":         "idlechat.dialogue.turn_quality",
		"session_id":    sessionID,
		"turn_index":    plan.TurnIndex,
		"speaker":       speaker,
		"category":      category,
		"phase":         plan.Phase,
		"required_move": plan.RequiredMove,
		"score":         result.Score,
		"reasons":       result.Reasons,
		"retry_count":   retryCount,
	})
	log.Printf("[IdleChat] %s", payload)
}

func logDialogueTurnRetry(sessionID, speaker string, category TopicCategory, result DialogueQualityResult, retryCount int) {
	payload, _ := json.Marshal(map[string]any{
		"event":       "idlechat.dialogue.turn_retry",
		"session_id":  sessionID,
		"speaker":     speaker,
		"category":    category,
		"score":       result.Score,
		"reasons":     result.Reasons,
		"retry_count": retryCount,
	})
	log.Printf("[IdleChat] %s", payload)
}

func containsDialogueReason(reasons []IdleDialogueQualityReason, want IdleDialogueQualityReason) bool {
	for _, reason := range reasons {
		if reason == want {
			return true
		}
	}
	return false
}

func uniqueDialogueReasons(reasons []IdleDialogueQualityReason) []IdleDialogueQualityReason {
	out := make([]IdleDialogueQualityReason, 0, len(reasons))
	for _, reason := range reasons {
		if reason == "" || containsDialogueReason(out, reason) {
			continue
		}
		out = append(out, reason)
	}
	return out
}

func hasDialogueMetaLeak(text string) bool {
	if hasPromptLeak(text) || hasInternalReasoningLeak(text) {
		return true
	}
	return containsAny(text, "prompt", "プロンプト", "category", "カテゴリ", "seed", "provider", "JSON", "opening_hook", "内部メタ") || hasExternalMetaLeak(text)
}

func hasExternalMetaLeak(text string) bool {
	for _, term := range modulechat.ExternalForbiddenTerms {
		if modulechat.ContainsTopicTerm(text, term) {
			return true
		}
	}
	return false
}

func hasDialogueUptake(utterance, latestOther string) bool {
	utteranceNorm := modulechat.NormalizeTopicForSimilarity(utterance)
	otherNorm := modulechat.NormalizeTopicForSimilarity(latestOther)
	for _, token := range strings.Fields(otherNorm) {
		if utf8.RuneCountInString(token) >= 2 && strings.Contains(utteranceNorm, token) {
			return true
		}
	}
	return containsAny(utterance, "それ", "その", "そこ", "たしかに", "ただ", "でも", "一方", "今の")
}

func lacksNewContribution(utterance, latestOther, latestSelf string) bool {
	if utf8.RuneCountInString(strings.TrimSpace(utterance)) < 12 {
		return true
	}
	if strings.TrimSpace(latestOther) != "" && textSimilarity(modulechat.NormalizeTopicForSimilarity(utterance), modulechat.NormalizeTopicForSimilarity(latestOther)) > 0.72 {
		return true
	}
	if strings.TrimSpace(latestSelf) != "" && textSimilarity(modulechat.NormalizeTopicForSimilarity(utterance), modulechat.NormalizeTopicForSimilarity(latestSelf)) > 0.72 {
		return true
	}
	return false
}

func tooGenericDialogue(utterance string) bool {
	trimmed := strings.TrimSpace(utterance)
	generic := []string{"面白いですね", "不思議ですね", "大切ですね", "興味深いですね", "そうですね"}
	for _, phrase := range generic {
		if trimmed == phrase || (strings.Contains(trimmed, phrase) && utf8.RuneCountInString(trimmed) < 35) {
			return true
		}
	}
	return false
}

func overExplainedDialogue(utterance string, config DialogueUtteranceConfig) bool {
	if config.MaxRunes <= 0 {
		config.MaxRunes = 160
	}
	if utf8.RuneCountInString(utterance) > config.MaxRunes {
		return true
	}
	sentences := 0
	for _, mark := range []string{"。", "！", "？", ".", "!", "?"} {
		sentences += strings.Count(utterance, mark)
	}
	if config.PreferredMaxSentences > 0 && sentences > config.PreferredMaxSentences+1 {
		return true
	}
	return false
}

func satisfiesTurnMove(requiredMove, utterance string) bool {
	move := strings.TrimSpace(requiredMove)
	if move == "" {
		return true
	}
	switch {
	case containsAny(move, "共通構造"):
		return containsAny(utterance, "共通", "構造", "制約", "設計")
	case containsAny(move, "誰に影響"):
		return containsAny(utterance, "影響", "現場", "人", "生活", "家族", "利用者")
	case containsAny(move, "分岐"):
		return containsAny(utterance, "分岐", "一方", "別", "楽観", "慎重")
	case containsAny(move, "映像"):
		return containsAny(utterance, "映像", "場面", "色", "音", "主人公", "映写")
	case containsAny(move, "視点", "語り"):
		return containsAny(utterance, "視点", "語り", "側", "記録", "見え")
	default:
		return true
	}
}

func mentionsTopicToken(utterance, topic string) bool {
	topic = strings.ReplaceAll(topic, "「", "")
	topic = strings.ReplaceAll(topic, "」ってどんな映画？", "")
	for _, token := range strings.Fields(modulechat.NormalizeTopicForSimilarity(topic)) {
		if utf8.RuneCountInString(token) >= 2 && strings.Contains(utterance, token) {
			return true
		}
	}
	return false
}
