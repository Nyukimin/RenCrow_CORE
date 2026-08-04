package idlechat

import "strings"

// DialogueContentMode controls how seriously an IdleChat topic is handled
// without replacing either Agent's canonical speaking style.
type DialogueContentMode string

const (
	DialogueContentModeSerious   DialogueContentMode = "serious"
	DialogueContentModeAssertive DialogueContentMode = "assertive"
	DialogueContentModeFree      DialogueContentMode = "free"
)

// DialogueContentPolicy is a deterministic, observable classification that is
// shared by current turn generation and the future episode producer.
type DialogueContentPolicy struct {
	Mode    DialogueContentMode `json:"mode"`
	Reasons []string            `json:"reasons"`
}

var dialogueSeriousWarTerms = []string{
	"戦争", "武力衝突", "紛争", "戦闘", "侵攻", "空爆", "砲撃", "虐殺", "停戦", "テロ攻撃", "テロ事件", "テロリズム",
}

var dialogueSeriousDisasterTerms = []string{
	"災害", "大地震", "地震", "津波", "洪水", "豪雨", "台風", "噴火", "土砂災害", "山火事",
	"被災", "避難", "救助", "犠牲者", "死者", "行方不明", "負傷者",
}

var dialoguePoliticalTerms = []string{
	"政治", "政策", "政府", "国会", "選挙", "政党", "議会", "首相", "大統領", "政権", "外交", "憲法",
}

var dialogueIdeologyTerms = []string{
	"思想", "イデオロギー", "民主主義", "社会主義", "共産主義", "資本主義", "保守派", "保守主義", "リベラル",
}

// ClassifyDialogueContentPolicy applies serious > assertive > free to the
// adopted topic and its source context. The result must be fixed before LLM
// generation and must not be inferred from generated wording.
func ClassifyDialogueContentPolicy(result TopicGenerationResult) DialogueContentPolicy {
	text := strings.ToLower(dialogueContentEvidence(result))
	var seriousReasons []string
	if containsDialogueContentTerm(text, dialogueSeriousWarTerms) {
		seriousReasons = append(seriousReasons, "war_or_armed_conflict")
	}
	if containsDialogueContentTerm(text, dialogueSeriousDisasterTerms) {
		seriousReasons = append(seriousReasons, "disaster_or_human_harm")
	}
	if len(seriousReasons) > 0 {
		return DialogueContentPolicy{Mode: DialogueContentModeSerious, Reasons: seriousReasons}
	}

	var assertiveReasons []string
	if containsDialogueContentTerm(text, dialoguePoliticalTerms) {
		assertiveReasons = append(assertiveReasons, "politics_or_public_policy")
	}
	if containsDialogueContentTerm(text, dialogueIdeologyTerms) {
		assertiveReasons = append(assertiveReasons, "ideology_or_values")
	}
	if len(assertiveReasons) > 0 {
		return DialogueContentPolicy{Mode: DialogueContentModeAssertive, Reasons: assertiveReasons}
	}
	return DialogueContentPolicy{Mode: DialogueContentModeFree, Reasons: []string{"default"}}
}

func dialogueContentEvidence(result TopicGenerationResult) string {
	parts := []string{
		result.Topic,
		result.InterestingnessAxis,
		result.OpeningHook,
		result.Avoid,
		result.Seed.Genre1,
		result.Seed.Genre2,
		result.Seed.Genre1Context,
		result.Seed.Genre2Context,
		result.Seed.ForecastDomain,
		strings.Join(result.Seed.TrendKeywords, " "),
		result.Seed.StoryBase,
		result.Seed.StoryTransform,
	}
	if external := result.Seed.ExternalMaterial; external != nil {
		parts = append(parts, external.Title, external.Summary, external.Category)
	}
	if news := result.Seed.News; news != nil {
		parts = append(parts, news.Title, news.Category, news.TranslatedBody, news.Summary, news.Perspective)
		for _, note := range news.TermNotes {
			parts = append(parts, note.Term, note.Explanation)
		}
	}
	return strings.Join(parts, "\n")
}

func containsDialogueContentTerm(text string, terms []string) bool {
	for _, term := range terms {
		if strings.Contains(text, strings.ToLower(term)) {
			return true
		}
	}
	return false
}

func normalizeDialogueContentPolicy(policy DialogueContentPolicy) DialogueContentPolicy {
	switch policy.Mode {
	case DialogueContentModeSerious, DialogueContentModeAssertive, DialogueContentModeFree:
	default:
		policy.Mode = DialogueContentModeFree
	}
	if len(policy.Reasons) == 0 {
		policy.Reasons = []string{"default"}
	}
	policy.Reasons = append([]string(nil), policy.Reasons...)
	return policy
}

func dialogueContentPolicyInstruction(policy DialogueContentPolicy) string {
	policy = normalizeDialogueContentPolicy(policy)
	switch policy.Mode {
	case DialogueContentModeSerious:
		return "口調はMioとShiroそれぞれのキャラクターのままにし、内容は真面目に扱う。被害や喪失を茶化さず、軽口、娯楽化、被害を矮小化する表現、扇情的な断定を避け、確認できた事実と不明点を分ける。"
	case DialogueContentModeAssertive:
		return "口調はMioとShiroそれぞれのキャラクターのままにし、政治・政策・制度・思想へ強い意見、率直な批判、異論を述べてよい。人工的に中立化せず、事実・推測・意見を分ける。個人属性への侮辱、差別・非人間化、暴力の扇動、根拠のない事実断定はしない。"
	default:
		return "口調はMioとShiroそれぞれのキャラクターのままにし、会話内容の温度はその時の気分に委ねてよい。Persona、事実性、反復、カテゴリ別禁止事項は守る。"
	}
}

func dialogueContentModeViolation(mode DialogueContentMode, utterance string) bool {
	text := strings.ToLower(strings.TrimSpace(utterance))
	switch mode {
	case DialogueContentModeSerious:
		return containsAny(text,
			"笑い話", "ネタにすれば", "ウケる", "面白すぎ", "エンタメとして", "ゲーム感覚", "ざまあ", "お祭り騒ぎ",
		)
	case DialogueContentModeAssertive:
		return containsAny(text,
			"政治の話なので意見は控", "思想の話なので意見は控", "中立であるべきなので意見は",
			"どちらも正しいことに", "賛否両論なので何も言えない", "意見するべきではない",
		)
	default:
		return false
	}
}
