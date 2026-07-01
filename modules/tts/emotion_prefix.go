package tts

import "strings"

type EmojiPaletteItem struct {
	Emoji       string
	Label       string
	Instruction string
	Filename    string
}

var EmotionEmojiPaletteItems = []EmojiPaletteItem{
	{Emoji: "👂", Label: "囁き・耳元", Instruction: "耳元で囁くように", Filename: "01_whisper_close.wav"},
	{Emoji: "😏", Label: "からかう・甘える", Instruction: "からかいと甘えを混ぜて", Filename: "07_teasing.wav"},
	{Emoji: "🥺", Label: "震え声・自信なさげ", Instruction: "弱々しく自信なさげに", Filename: "08_timid.wav"},
	{Emoji: "🫶", Label: "優しく", Instruction: "やわらかく丁寧に", Filename: "13_tender.wav"},
	{Emoji: "😭", Label: "泣き声・悲しみ", Instruction: "泣きそうに悲しく", Filename: "14_sobbing.wav"},
	{Emoji: "😱", Label: "悲鳴・叫び", Instruction: "叫ぶように強く", Filename: "15_scream.wav"},
	{Emoji: "😪", Label: "眠そう・気だるげ", Instruction: "眠そうに気だるく", Filename: "16_sleepy.wav"},
	{Emoji: "😴", Label: "寝言・いびき", Instruction: "寝言のように", Filename: "17_sleep_talk.wav"},
	{Emoji: "⏩", Label: "早口・急いで", Instruction: "早口で急いで", Filename: "18_fast.wav"},
	{Emoji: "🐢", Label: "ゆっくり", Instruction: "ゆっくり落ち着いて", Filename: "20_slow.wav"},
	{Emoji: "😰", Label: "慌て・動揺・どもり", Instruction: "慌てて動揺気味に", Filename: "24_panic_stutter.wav"},
	{Emoji: "😆", Label: "喜び", Instruction: "弾むように嬉しく", Filename: "25_joy.wav"},
	{Emoji: "💥", Label: "勢いよく", Instruction: "勢いよくはっきりと", Filename: "26_forceful.wav"},
	{Emoji: "😠", Label: "怒り・不満", Instruction: "強く不満げに", Filename: "27_angry.wav"},
	{Emoji: "😲", Label: "驚き・感嘆", Instruction: "大きく驚いて", Filename: "28_surprise.wav"},
	{Emoji: "😖", Label: "苦しげ", Instruction: "つらそうに", Filename: "30_painful.wav"},
	{Emoji: "😟", Label: "心配そう", Instruction: "不安そうに", Filename: "31_worried.wav"},
	{Emoji: "🫣", Label: "照れ・恥ずかしそう", Instruction: "恥ずかしそうに", Filename: "32_shy.wav"},
	{Emoji: "🙄", Label: "呆れ", Instruction: "あきれたように", Filename: "33_exasperated.wav"},
	{Emoji: "😊", Label: "楽しげ・嬉しそう", Instruction: "自然に嬉しそうに", Filename: "34_cheerful.wav"},
	{Emoji: "😎", Label: "自信ありげ", Instruction: "余裕のある口調で", Filename: "35_confident.wav"},
	{Emoji: "🙏", Label: "懇願", Instruction: "懇願するように", Filename: "37_pleading.wav"},
	{Emoji: "🥴", Label: "酔っ払い", Instruction: "ふらついた雰囲気で", Filename: "38_drunken.wav"},
	{Emoji: "🤐", Label: "口を塞がれて", Instruction: "こもった声で", Filename: "40_muffled.wav"},
	{Emoji: "😌", Label: "安堵・満足", Instruction: "落ち着いて満足げに", Filename: "41_relieved.wav"},
	{Emoji: "🤔", Label: "疑問の声", Instruction: "疑問を含めて", Filename: "42_questioning.wav"},
	{Emoji: "💪", Label: "力強く", Instruction: "力を込めて", Filename: "43_stsaisinrong.wav"},
	{Emoji: "📖", Label: "ナレーション・独白", Instruction: "落ち着いたナレーション", Filename: "45_narration.wav"},
}

func EnsureEmotionPrefix(text string, emotion *EmotionState) string {
	return EnsureEmotionPrefixForCharacter(text, emotion, "")
}

func EnsureEmotionPrefixForCharacter(text string, emotion *EmotionState, characterID string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	if HasEmotionPrefix(trimmed) {
		return trimmed
	}
	return emotionPrefixForCharacterText(emotion, characterID, trimmed) + trimmed
}

func HasEmotionPrefix(text string) bool {
	trimmed := strings.TrimSpace(text)
	for _, item := range EmotionEmojiPaletteItems {
		if item.Emoji != "" && strings.HasPrefix(trimmed, item.Emoji) {
			return true
		}
	}
	return false
}

func emotionPrefixForState(emotion *EmotionState) string {
	return emotionPrefixForCharacterText(emotion, "", "")
}

func emotionPrefixForCharacterText(emotion *EmotionState, characterID, text string) string {
	if isIdleChatTopicSpeechText(text) {
		return "😊"
	}
	feature := classifyEmotionText(text)
	speaker := strings.ToLower(strings.TrimSpace(characterID))
	switch speaker {
	case "mio", "female_01", "female_01_mio":
		if feature != "" {
			return prefixForFeature(feature, true)
		}
		if fromState := emotionPrefixForMioStateOnly(emotion); fromState != "" && fromState != "😌" {
			return fromState
		}
		return "😊"
	case "shiro", "male_01", "male":
		if feature != "" && isStrongEmotionFeature(feature, text) {
			return prefixForFeature(feature, false)
		}
		if fromState := emotionPrefixForStateOnly(emotion); fromState == "😰" || fromState == "💥" {
			return fromState
		}
		return "📖"
	default:
		if feature != "" {
			return prefixForFeature(feature, false)
		}
		return emotionPrefixForStateOnly(emotion)
	}
}

func isIdleChatTopicSpeechText(text string) bool {
	trimmed := strings.TrimSpace(text)
	return strings.HasPrefix(trimmed, "きょうのおだい")
}

func emotionPrefixForStateOnly(emotion *EmotionState) string {
	if emotion == nil {
		return "😌"
	}
	primary := strings.ToLower(strings.TrimSpace(emotion.PrimaryEmotion))
	if primary == "" {
		return "😌"
	}
	v := emotion.EmotionVector
	switch primary {
	case "alert":
		if v.Alertness >= 0.78 {
			return "😰"
		}
		return "😰"
	case "serious":
		return "🤔"
	case "cheerful":
		if v.Cheerfulness >= 0.72 {
			return "😆"
		}
		return "😊"
	case "warm":
		if v.Warmth >= 0.70 {
			return "🫶"
		}
		return "🫶"
	case "calm":
		return "😌"
	default:
		return "📖"
	}
}

func emotionPrefixForMioStateOnly(emotion *EmotionState) string {
	if emotion == nil {
		return "😊"
	}
	primary := strings.ToLower(strings.TrimSpace(emotion.PrimaryEmotion))
	if primary == "" {
		return "😊"
	}
	v := emotion.EmotionVector
	switch primary {
	case "alert":
		if v.Alertness >= 0.78 {
			return "😰"
		}
		return "😰"
	case "serious":
		return "🤔"
	case "cheerful":
		if v.Cheerfulness >= 0.78 {
			return "😆"
		}
		return "😊"
	case "warm":
		return "😊"
	case "calm":
		return "😊"
	default:
		return "😊"
	}
}

func classifyEmotionText(text string) string {
	lower := strings.ToLower(text)
	switch {
	case containsAny(lower, "ありがとう", "嬉しい", "楽しい", "最高", "よかった", "すごい", "素敵", "いいね", "やった", "成功", "できました", "完了", "thank"):
		return "joy"
	case containsAny(lower, "好き", "かわいい", "大切", "親し"):
		return "affection"
	case containsAny(lower, "ごめん", "すみません", "申し訳", "お願い", "頼む", "助けて"):
		return "plead"
	case containsAny(lower, "なぜ", "どうして", "考え", "迷", "かもしれ", "おそらく", "たぶん", "一方で", "ただし"):
		return "thinking"
	case containsAny(lower, "驚", "びっくり", "まさか", "はっと", "えっ", "!?"):
		return "surprise"
	case containsAny(lower, "怖", "恐", "不安", "心配", "危険", "緊張"):
		return "fear"
	case containsAny(lower, "急", "焦", "まずい", "大変", "すぐ", "警告", "注意", "エラー", "失敗"):
		return "alert"
	case containsAny(lower, "悲しい", "寂しい", "つらい", "泣", "落胆", "しょんぼり"):
		return "sad"
	case containsAny(lower, "怒", "不満", "むっと", "許せ", "ひどい"):
		return "anger"
	case containsAny(lower, "呆", "あきれ", "うんざり", "退屈", "だる"):
		return "tired"
	case containsAny(lower, "静か", "内緒", "ひそ", "そっと"):
		return "quiet"
	case containsAny(lower, "!", "！"):
		return "energy"
	default:
		return ""
	}
}

func prefixForFeature(feature string, expressive bool) string {
	switch feature {
	case "joy":
		if expressive {
			return "😆"
		}
		return "😊"
	case "affection":
		if expressive {
			return "🫶"
		}
		return "🫶"
	case "plead":
		return "🙏"
	case "thinking":
		return "🤔"
	case "surprise":
		if expressive {
			return "😲"
		}
		return "😲"
	case "fear":
		if expressive {
			return "😱"
		}
		return "😟"
	case "alert":
		if expressive {
			return "😰"
		}
		return "😰"
	case "sad":
		if expressive {
			return "😭"
		}
		return "😭"
	case "anger":
		if expressive {
			return "😠"
		}
		return "😠"
	case "tired":
		if expressive {
			return "🙄"
		}
		return "😒"
	case "quiet":
		return "👂"
	case "energy":
		if expressive {
			return "💥"
		}
		return "💪"
	default:
		return "😌"
	}
}

func isStrongEmotionFeature(feature, text string) bool {
	switch feature {
	case "surprise", "fear", "alert", "sad", "anger", "energy":
		return true
	case "joy":
		return containsAny(text, "最高", "やった", "すごい", "！", "!")
	case "affection":
		return containsAny(text, "大切", "好き", "安心")
	case "plead":
		return containsAny(text, "お願い", "助けて", "頼む")
	default:
		return false
	}
}
