package viewer

import "strings"

// PublicCurrentActivity is the safe shared descriptor used by public living
// surfaces and owner task projections. It deliberately contains no Task or
// session identifier.
type PublicCurrentActivity struct {
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Phase    string `json:"phase"`
	Revision uint64 `json:"revision"`
}

// FormatPublicCurrentActivity normalizes durable activity metadata into the
// fixed public vocabulary. Free-form labels and phases never pass through.
func FormatPublicCurrentActivity(kind, label, phase, actorID string, revision uint64) (PublicCurrentActivity, bool) {
	if revision == 0 {
		return PublicCurrentActivity{}, false
	}
	result := PublicCurrentActivity{Kind: strings.TrimSpace(kind), Revision: revision}
	switch result.Kind {
	case "work":
		result.Label, result.Phase = publicWorkLabel(label), "working"
	case "chat":
		result.Label, result.Phase = publicChatLabel(label, actorID), "talking"
	case "hobby":
		result.Label, result.Phase = publicHobbyLabel(label, actorID), "active"
	case "rest":
		result.Label, result.Phase = "休憩中", "resting"
	case "sleep":
		switch strings.TrimSpace(phase) {
		case "sleep_window":
			result.Label, result.Phase = "就寝予定", "sleep_window"
		case "sleeping":
			result.Label, result.Phase = "就寝中", "sleeping"
		case "waking":
			result.Label, result.Phase = "起床中", "waking"
		default:
			result.Label, result.Phase = "就寝準備中", "preparing"
		}
	case "moving":
		result.Label, result.Phase = "移動中", "in_transit"
	case "game":
		switch strings.TrimSpace(phase) {
		case "playing":
			result.Label, result.Phase = publicGameLabel(label), "playing"
		case "preparing", "launching":
			result.Label, result.Phase = publicGamePreparationLabel(label), "preparing"
		default:
			return PublicCurrentActivity{}, false
		}
	case "evaluating":
		result.Label, result.Phase = "次の活動を選択中", "waiting"
	case "routine":
		result.Label, result.Phase = "活動中", "active"
	default:
		return PublicCurrentActivity{}, false
	}
	return result, true
}

func publicWorkLabel(label string) string {
	switch strings.TrimSpace(label) {
	case "Diary", "daily_diary", "CORE agent routine: daily diary":
		return "日記を書く"
	case "review", "Review", "code_review", "CORE review":
		return "レビュー中"
	case "research", "Research", "CORE research":
		return "調査中"
	case "analysis", "Analysis", "CORE analysis":
		return "分析中"
	case "writing", "Writing", "CORE writing":
		return "執筆中"
	default:
		return "作業中"
	}
}

func publicChatLabel(label, actorID string) string {
	const prefix = "chat-with-"
	partner, ok := strings.CutPrefix(label, prefix)
	if !ok || !publicCurrentActivityActor(partner) || partner == actorID {
		return "会話中"
	}
	names := map[string]string{"mio": "ミオ", "shiro": "シロ", "midori": "ミドリ", "kuro": "クロ", "aka": "アカ", "ao": "アオ", "kin": "キン", "gin": "ギン"}
	return names[partner] + "と会話中"
}

func publicHobbyLabel(label, actorID string) string {
	translations := map[string]string{
		"fashion": "ファッション", "video_creativity": "動画の企画", "film_research": "映画リサーチ",
		"fitness_research": "トレーニング", "otaku_culture": "オタク文化", "creative_mirror_ideas": "ミラーポーズ",
		"reading": "読書", "manga_culture": "マンガ文化", "music_culture": "音楽", "bl_story_ideas": "物語の企画",
	}
	allowed := map[string]map[string]struct{}{
		"mio":    {"fashion": {}, "video_creativity": {}},
		"shiro":  {"film_research": {}, "fitness_research": {}},
		"kuro":   {"otaku_culture": {}, "creative_mirror_ideas": {}},
		"midori": {"reading": {}, "manga_culture": {}, "music_culture": {}, "bl_story_ideas": {}},
	}
	if _, ok := allowed[actorID][label]; !ok {
		return "趣味中"
	}
	return translations[label] + "中"
}

func publicGameLabel(label string) string {
	switch strings.TrimSpace(label) {
	case "nethack", "NetHack":
		return "NetHackをプレイ中"
	case "territory_commander", "Territory Commander":
		return "Territory Commanderをプレイ中"
	case "survival_garden", "Survival Garden":
		return "Survival Gardenをプレイ中"
	case "herzog_zwei", "Herzog Zwei", "ヘルツォーク・ツヴァイ":
		return "ヘルツォーク・ツヴァイをプレイ中"
	default:
		return "ゲーム中"
	}
}

func publicGamePreparationLabel(label string) string {
	name := strings.TrimSuffix(publicGameLabel(label), "をプレイ中")
	if name == "ゲーム中" {
		return "ゲームを準備中"
	}
	return name + "を準備中"
}

func publicCurrentActivityActor(actorID string) bool {
	switch actorID {
	case "mio", "shiro", "midori", "kuro", "aka", "ao", "kin", "gin":
		return true
	default:
		return false
	}
}
