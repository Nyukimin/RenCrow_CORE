package idlechat

import (
	"encoding/json"
	"log"
	"strings"
	"unicode/utf8"

	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
)

type DialogueDirector struct {
	config DialogueInterestingnessConfig
}

func NewDialogueDirector(config DialogueInterestingnessConfig) *DialogueDirector {
	return &DialogueDirector{config: normalizeDialogueInterestingnessConfig(config)}
}

func (d *DialogueDirector) BuildArcPlan(result TopicGenerationResult) DialogueArcPlan {
	category := result.Category
	if category == "" {
		category, _ = modulechat.NormalizeTopicCategory(result.Strategy)
	}
	spec := dialogueCategorySpec(category)
	turnCount := d.config.MaxTurnsPerTopic
	if turnCount <= 0 {
		turnCount = maxTurnsPerTopic
	}
	if turnCount > maxTurnsPerTopic {
		turnCount = maxTurnsPerTopic
	}
	plan := DialogueArcPlan{
		Topic:               result.Topic,
		Category:            category,
		Strategy:            result.Strategy,
		InterestingnessAxis: result.InterestingnessAxis,
		CoreQuestion:        spec.CoreQuestion,
		OpeningMove:         spec.OpeningMove,
		DevelopmentMoves:    append([]string(nil), spec.DevelopmentMoves...),
		DeepeningMoves:      append([]string(nil), spec.DeepeningMoves...),
		ClosingMove:         spec.ClosingMove,
		ForbiddenMoves:      append([]string(nil), spec.ForbiddenMoves...),
		SpeakerRoles: map[string]DialogueSpeakerRole{
			"mio":   spec.MioRole,
			"shiro": spec.ShiroRole,
		},
		TurnPlans: buildDialogueTurnPlans(turnCount, spec),
	}
	if plan.InterestingnessAxis == "" {
		plan.InterestingnessAxis = modulechat.ExpectedAxisByCategory[category]
	}
	return plan
}

func (d *DialogueDirector) NewArcState(sessionID string, result TopicGenerationResult, plan DialogueArcPlan) DialogueArcState {
	return DialogueArcState{
		SessionID: sessionID,
		Topic:     result.Topic,
		Category:  plan.Category,
		Phase:     "opening",
	}
}

func (d *DialogueDirector) UpdateArcState(state DialogueArcState, utterance string, plan DialogueTurnPlan, quality DialogueQualityResult) DialogueArcState {
	state.TurnIndex++
	state.Phase = plan.Phase
	if strings.TrimSpace(plan.RequiredMove) != "" {
		state.UsedMoves = appendUniqueString(state.UsedMoves, plan.RequiredMove)
	}
	state.ConcreteAnchors = appendUniqueLimited(state.ConcreteAnchors, extractDialogueConcreteAnchors(utterance), 12)
	if hasDialogueTension(utterance) {
		state.TensionPoints = appendUniqueLimited(state.TensionPoints, []string{truncate(utterance, 80)}, 8)
	}
	if strings.Contains(utterance, "？") || strings.Contains(utterance, "?") {
		state.OpenQuestions = appendUniqueLimited(state.OpenQuestions, []string{truncate(utterance, 80)}, 8)
	}
	for _, reason := range quality.Reasons {
		if reason != "" && reason != DialogueNoUptake {
			state.DullnessWarnings = appendUniqueString(state.DullnessWarnings, string(reason))
		}
	}
	return state
}

func (d *DialogueDirector) LogArcCreated(sessionID string, plan DialogueArcPlan) {
	payload, _ := json.Marshal(map[string]any{
		"event":                "idlechat.dialogue.arc_created",
		"session_id":           sessionID,
		"topic":                plan.Topic,
		"category":             plan.Category,
		"strategy":             plan.Strategy,
		"interestingness_axis": plan.InterestingnessAxis,
		"opening_move":         plan.OpeningMove,
		"turn_count":           len(plan.TurnPlans),
	})
	log.Printf("[IdleChat] %s", payload)
}

type dialogueCategoryDefinition struct {
	CoreQuestion     string
	OpeningMove      string
	DevelopmentMoves []string
	DeepeningMoves   []string
	ClosingMove      string
	ForbiddenMoves   []string
	MioRole          DialogueSpeakerRole
	ShiroRole        DialogueSpeakerRole
	RequiredMoves    []string
}

func dialogueCategorySpec(category TopicCategory) dialogueCategoryDefinition {
	base := dialogueCategoryDefinition{
		CoreQuestion:     "このtopicから何が発見できるか",
		OpeningMove:      "topicの入口を具体化する",
		DevelopmentMoves: []string{"素材や場面を一つ増やす", "論点を一つ前に進める"},
		DeepeningMoves:   []string{"違和感、反例、制約のどれかを足す", "別の角度で見直す"},
		ClosingMove:      "小さな発見として着地する",
		ForbiddenMoves:   []string{"内部メタを出す", "汎用相槌だけで終わる", "ユーザーへ直接質問する"},
		MioRole:          DialogueSpeakerRole{Speaker: "mio", PrimaryMove: "場面、人間味、感情、比喩、聞きやすい橋渡しを担当する"},
		ShiroRole:        DialogueSpeakerRole{Speaker: "shiro", PrimaryMove: "構造、制約、論点、反例、整理を担当する"},
		RequiredMoves:    []string{"直前の相手発話を受ける", "新しい貢献を一つだけ足す", "ここまでの話を少し深める"},
	}
	switch category {
	case TopicCategorySingle:
		base.CoreQuestion = "細部から何が見えるか"
		base.OpeningMove = "topic内の具体アンカーを1つ拾い、場面を置く"
		base.RequiredMoves = []string{"具体アンカーを一つ拾う", "小さな違和感または判断の難しさを出す", "最初の具体物の意味を少し変える"}
		base.ForbiddenMoves = append(base.ForbiddenMoves, "一般論だけで終わる", "人物や物の手触りを消す")
	case TopicCategoryDouble:
		base.CoreQuestion = "2領域にどんな共通構造があるか"
		base.OpeningMove = "2つの領域の距離感を軽く示す"
		base.RequiredMoves = []string{"AとBの距離感を示す", "AとBそれぞれの特徴を出す", "共通構造の仮説を出す", "共通構造の仮説を出す", "第三の概念にまとめる"}
		base.ForbiddenMoves = append(base.ForbiddenMoves, "AもBも大切ですで終わる", "片方の話題だけで進む")
	case TopicCategoryExternal:
		base.CoreQuestion = "偶然の素材がどう意味化されるか"
		base.OpeningMove = "素材の具体的な特徴を1つ拾う"
		base.RequiredMoves = []string{"素材名または素材内容から始める", "素材とジャンルの接点を出す", "偶然ではなくこの組み合わせだから見える意味を出す"}
		base.ForbiddenMoves = append(base.ForbiddenMoves, "Wikipediaや検索結果など取得経路を出す", "Newsとして扱う")
	case TopicCategoryMovie:
		base.CoreQuestion = "存在しない映画のどんな映像が立ち上がるか"
		base.OpeningMove = "タイトルから浮かぶ最初の映像を出す"
		base.RequiredMoves = []string{"映像が浮かぶ描写を出す", "主人公または中心人物を出す", "葛藤または映画のルールを出す", "ラストシーンの余韻を出す"}
		base.ForbiddenMoves = append(base.ForbiddenMoves, "1発話で全あらすじを説明する", "実在作品として扱う")
	case TopicCategoryNews:
		base.CoreQuestion = "現実の出来事が誰にどう影響するか"
		base.OpeningMove = "ニュースが誰に影響するかを示す"
		base.RequiredMoves = []string{"誰に影響するかを出す", "論点または背景を出す", "判断の難しさを出す", "観察ポイントで終える"}
		base.ForbiddenMoves = append(base.ForbiddenMoves, "ニュース見出しの読み上げで終わる", "正解を断定する", "煽る")
	case TopicCategoryForecast:
		base.CoreQuestion = "未来の変化にどんな分岐があるか"
		base.OpeningMove = "現在の兆しを生活・仕事・創作・制度のどれかに置く"
		base.RequiredMoves = []string{"何が何を変えるかを出す", "変化のメカニズムを整理する", "影響を受ける主体を出す", "楽観分岐と慎重分岐を並べる", "今後見るべき変数で終える"}
		base.ForbiddenMoves = append(base.ForbiddenMoves, "未来を断定する", "便利になるだけで終わる")
	case TopicCategoryStory:
		base.CoreQuestion = "既知の物語がどの視点から語り直されるか"
		base.OpeningMove = "語り直しの視点をはっきり置く"
		base.RequiredMoves = []string{"語り手または視点を示す", "元話の既知場面との対応を出す", "善悪や意味の反転を出す", "元話に戻れる余韻を残す"}
		base.ForbiddenMoves = append(base.ForbiddenMoves, "元話が分からなくなる", "あらすじ説明だけになる")
	}
	return base
}

func buildDialogueTurnPlans(turnCount int, spec dialogueCategoryDefinition) []DialogueTurnPlan {
	plans := make([]DialogueTurnPlan, 0, turnCount)
	for i := 0; i < turnCount; i++ {
		phase := dialoguePhaseForTurn(i)
		move := spec.RequiredMoves[i%len(spec.RequiredMoves)]
		if phase == "opening" {
			move = spec.OpeningMove
		} else if phase == "closing" {
			move = spec.ClosingMove
		}
		plans = append(plans, DialogueTurnPlan{
			TurnIndex:    i + 1,
			Phase:        phase,
			RequiredMove: move,
			Avoid:        append([]string(nil), spec.ForbiddenMoves...),
		})
	}
	return plans
}

func dialoguePhaseForTurn(zeroBased int) string {
	switch {
	case zeroBased <= 1:
		return "opening"
	case zeroBased <= 4:
		return "development"
	case zeroBased <= 7:
		return "deepening"
	case zeroBased <= 9:
		return "reframing"
	default:
		return "closing"
	}
}

func extractDialogueConcreteAnchors(text string) []string {
	var out []string
	for _, token := range strings.FieldsFunc(text, func(r rune) bool {
		return r == '、' || r == '。' || r == ' ' || r == '\n' || r == '？' || r == '?' || r == '！' || r == '!'
	}) {
		token = strings.Trim(token, "「」『』（）()")
		if utf8.RuneCountInString(token) >= 3 && !containsAny(token, "です", "ます", "こと", "それ", "この", "その") {
			out = append(out, token)
		}
		if len(out) >= 3 {
			break
		}
	}
	return out
}

func hasDialogueTension(text string) bool {
	return containsAny(text, "違和感", "難し", "迷", "揺れ", "対立", "反例", "制約", "ただ", "けれど", "でも", "一方")
}

func appendUniqueString(items []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return items
	}
	for _, item := range items {
		if item == value {
			return items
		}
	}
	return append(items, value)
}

func appendUniqueLimited(items []string, values []string, limit int) []string {
	for _, value := range values {
		items = appendUniqueString(items, value)
	}
	if limit > 0 && len(items) > limit {
		return items[len(items)-limit:]
	}
	return items
}
