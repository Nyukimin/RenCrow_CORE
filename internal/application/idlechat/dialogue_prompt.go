package idlechat

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type DialoguePromptInput struct {
	Result             TopicGenerationResult
	Plan               DialogueArcPlan
	State              DialogueArcState
	TurnPlan           DialogueTurnPlan
	Speaker            string
	PreviousUtterances []string
	Config             DialogueInterestingnessConfig
}

func BuildDialoguePrompt(input DialoguePromptInput) string {
	config := normalizeDialogueInterestingnessConfig(input.Config)
	template := readDialoguePromptTemplate(config.PromptPaths.Common, defaultDialogueCommonPrompt())
	categoryTemplate := readDialoguePromptTemplate(dialoguePromptPathForCategory(config.PromptPaths, input.Plan.Category), dialogueCategoryPromptText(input.Plan.Category))
	stateJSON, _ := json.MarshalIndent(input.State, "", "  ")
	values := map[string]string{
		"topic":                          input.Result.Topic,
		"category_for_internal_use_only": string(input.Plan.Category),
		"interestingness_axis_for_internal_use_only": input.Plan.InterestingnessAxis,
		"phase":                              input.TurnPlan.Phase,
		"required_move":                      input.TurnPlan.RequiredMove,
		"opening_hook_for_internal_use_only": input.Result.OpeningHook,
		"avoid_for_internal_use_only":        input.Result.Avoid,
		"speaker":                            input.Speaker,
		"previous_utterances":                strings.Join(input.PreviousUtterances, "\n"),
		"arc_state_json":                     string(stateJSON),
	}
	return renderTopicPromptPlaceholders(template+"\n\n"+categoryTemplate, values)
}

func BuildDialogueRetryPrompt(plan DialogueTurnPlan, quality DialogueQualityResult) string {
	reasons := make([]string, 0, len(quality.Reasons))
	for _, reason := range quality.Reasons {
		reasons = append(reasons, string(reason))
	}
	return fmt.Sprintf("自然な会話文で言い直してください。\n直前発話を必ず受け、このターンでは「%s」だけを足してください。\n内部メタや説明文は出さないでください。\n失敗理由: %s", strings.TrimSpace(plan.RequiredMove), strings.Join(reasons, ","))
}

func readDialoguePromptTemplate(path, fallback string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return fallback
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return fallback
	}
	if strings.TrimSpace(string(body)) == "" {
		return fallback
	}
	return string(body)
}

func dialoguePromptPathForCategory(paths DialoguePromptPaths, category TopicCategory) string {
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

func defaultDialogueCommonPrompt() string {
	return `あなたは RenCrow IdleChat の会話話者です。
Mio と Shiro が、採用済み topic について自然に会話します。

目的:
聞いているユーザーが、作業中でも耳を向けたくなる短い対話にしてください。

重要:
- topic を説明し直すのではなく、会話として少しずつ深めます。
- 直前の相手発話を必ず受けます。
- 1発話につき新しい貢献は1つだけです。
- 内部メタ、カテゴリ名、prompt、seed、provider、JSON は出しません。
- ユーザーに直接質問しません。
- 汎用相槌だけで終わりません。
- 末尾は自然な日本語の句点にします。

入力:
topic: {topic}
category: {category_for_internal_use_only}
interestingness_axis: {interestingness_axis_for_internal_use_only}
phase: {phase}
required_move: {required_move}
opening_hook: {opening_hook_for_internal_use_only}
avoid: {avoid_for_internal_use_only}
speaker: {speaker}
previous_utterances:
{previous_utterances}
arc_state:
{arc_state_json}

出力:
発話本文のみ。`
}

func dialogueCategoryPromptText(category TopicCategory) string {
	switch category {
	case TopicCategorySingle:
		return "Single: 細部を発見する。具体アンカー、違和感、判断の難しさから入る。"
	case TopicCategoryDouble:
		return "Double: 構造を発見する。2領域の距離感、特徴、共通構造の仮説を段階的に出す。"
	case TopicCategoryExternal:
		return "External: 偶然の素材に意味を発見する。取得経路を出さず、素材そのものから入る。"
	case TopicCategoryMovie:
		return "Movie: 存在しない映画の映像を発見する。あらすじを一括説明せず、映像、人物、葛藤、余韻を段階的に足す。"
	case TopicCategoryNews:
		return "News: 現実の出来事の影響を発見する。誰に影響するか、背景、論点、判断の難しさを扱い、煽らない。"
	case TopicCategoryForecast:
		return "Forecast: 未来の分岐を発見する。予言ではなく、兆し、メカニズム、主体、分岐、変数として扱う。"
	case TopicCategoryStory:
		return "Story: 既知の物語の別視点を発見する。元話の骨格を残し、視点変更と意味反転を扱う。"
	default:
		return ""
	}
}
