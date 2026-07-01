package tts

import (
	"strings"
	"testing"
)

func TestEnsureEmotionPrefixAddsPaletteEmoji(t *testing.T) {
	text := EnsureEmotionPrefix("こんにちは。", &EmotionState{
		PrimaryEmotion: "cheerful",
		EmotionVector:  EmotionVector{Cheerfulness: 0.8},
	})
	if !strings.HasPrefix(text, "😆") {
		t.Fatalf("expected cheerful emoji prefix, got %q", text)
	}
	if !HasEmotionPrefix(text) {
		t.Fatalf("expected palette prefix to be detected: %q", text)
	}
}

func TestEnsureEmotionPrefixDoesNotDuplicatePaletteEmoji(t *testing.T) {
	text := EnsureEmotionPrefix("🤔 もう少し考えます。", &EmotionState{PrimaryEmotion: "calm"})
	if strings.Count(text, "🤔") != 1 {
		t.Fatalf("expected existing prefix to be preserved without duplication, got %q", text)
	}
}

func TestEnsureEmotionPrefixUsesDefaultWhenEmotionMissing(t *testing.T) {
	text := EnsureEmotionPrefix("本文です。", nil)
	if !strings.HasPrefix(text, "😌") {
		t.Fatalf("expected default calm prefix, got %q", text)
	}
}

func TestEnsureEmotionPrefixForCharacterUsesMioBrightBaseAndLargeSwing(t *testing.T) {
	base := EnsureEmotionPrefixForCharacter("今日は普通の話です。", nil, "mio")
	if !strings.HasPrefix(base, "😊") {
		t.Fatalf("expected Mio bright base prefix, got %q", base)
	}
	swung := EnsureEmotionPrefixForCharacter("最高！すごいね！", nil, "mio")
	if !strings.HasPrefix(swung, "😆") {
		t.Fatalf("expected Mio to swing brightly for joyful text, got %q", swung)
	}
}

func TestEnsureEmotionPrefixForCharacterKeepsMioWarmStateBright(t *testing.T) {
	text := EnsureEmotionPrefixForCharacter("その瞬間、乗り手の心の中もきっと大きな変化があったんじゃないかな。", &EmotionState{
		PrimaryEmotion: "warm",
		EmotionVector:  EmotionVector{Warmth: 0.80, Cheerfulness: 0.40},
	}, "mio")
	if !strings.HasPrefix(text, "😊") {
		t.Fatalf("expected Mio warm state to keep bright base, got %q", text)
	}
}

func TestEnsureEmotionPrefixForCharacterUsesMioAffectionOnlyForExplicitAffection(t *testing.T) {
	ordinarySupport := EnsureEmotionPrefixForCharacter("生徒のなぜに寄り添うような対話が鍵になりそうだよ。", nil, "mio")
	if !strings.HasPrefix(ordinarySupport, "🤔") {
		t.Fatalf("expected ordinary supportive thinking text not to become affection, got %q", ordinarySupport)
	}
	explicitAffection := EnsureEmotionPrefixForCharacter("その気持ち、すごく大切で好きだよ。", nil, "mio")
	if !strings.HasPrefix(explicitAffection, "🫶") {
		t.Fatalf("expected explicit affection to use affection prefix, got %q", explicitAffection)
	}
}

func TestEnsureEmotionPrefixForTopicAnnouncementUsesBrightPrefix(t *testing.T) {
	text := EnsureEmotionPrefixForCharacter("きょうのおだい、車輪の軌跡と乗り手の皮膚感覚。", &EmotionState{
		PrimaryEmotion: "warm",
		EmotionVector:  EmotionVector{Warmth: 0.85},
	}, "user")
	if !strings.HasPrefix(text, "😊きょうのおだい") {
		t.Fatalf("expected topic announcement to start with bright prefix, got %q", text)
	}
}

func TestEnsureEmotionPrefixForCharacterKeepsShiroCalmUnlessStrong(t *testing.T) {
	base := EnsureEmotionPrefixForCharacter("少し考えてみよう。", nil, "shiro")
	if !strings.HasPrefix(base, "📖") {
		t.Fatalf("expected Shiro calm base prefix, got %q", base)
	}
	swung := EnsureEmotionPrefixForCharacter("エラーです、すぐ注意してください！", nil, "shiro")
	if !strings.HasPrefix(swung, "😰") {
		t.Fatalf("expected Shiro to swing only on strong emotion, got %q", swung)
	}
}

func TestEmotionEmojiPaletteContainsOnlySelectedVoiceCues(t *testing.T) {
	want := []EmojiPaletteItem{
		{Emoji: "👂", Label: "囁き・耳元", Filename: "01_whisper_close.wav"},
		{Emoji: "😏", Label: "からかう・甘える", Filename: "07_teasing.wav"},
		{Emoji: "🥺", Label: "震え声・自信なさげ", Filename: "08_timid.wav"},
		{Emoji: "🫶", Label: "優しく", Filename: "13_tender.wav"},
		{Emoji: "😭", Label: "泣き声・悲しみ", Filename: "14_sobbing.wav"},
		{Emoji: "😱", Label: "悲鳴・叫び", Filename: "15_scream.wav"},
		{Emoji: "😪", Label: "眠そう・気だるげ", Filename: "16_sleepy.wav"},
		{Emoji: "😴", Label: "寝言・いびき", Filename: "17_sleep_talk.wav"},
		{Emoji: "⏩", Label: "早口・急いで", Filename: "18_fast.wav"},
		{Emoji: "🐢", Label: "ゆっくり", Filename: "20_slow.wav"},
		{Emoji: "😰", Label: "慌て・動揺・どもり", Filename: "24_panic_stutter.wav"},
		{Emoji: "😆", Label: "喜び", Filename: "25_joy.wav"},
		{Emoji: "💥", Label: "勢いよく", Filename: "26_forceful.wav"},
		{Emoji: "😠", Label: "怒り・不満", Filename: "27_angry.wav"},
		{Emoji: "😲", Label: "驚き・感嘆", Filename: "28_surprise.wav"},
		{Emoji: "😖", Label: "苦しげ", Filename: "30_painful.wav"},
		{Emoji: "😟", Label: "心配そう", Filename: "31_worried.wav"},
		{Emoji: "🫣", Label: "照れ・恥ずかしそう", Filename: "32_shy.wav"},
		{Emoji: "🙄", Label: "呆れ", Filename: "33_exasperated.wav"},
		{Emoji: "😊", Label: "楽しげ・嬉しそう", Filename: "34_cheerful.wav"},
		{Emoji: "😎", Label: "自信ありげ", Filename: "35_confident.wav"},
		{Emoji: "🙏", Label: "懇願", Filename: "37_pleading.wav"},
		{Emoji: "🥴", Label: "酔っ払い", Filename: "38_drunken.wav"},
		{Emoji: "🤐", Label: "口を塞がれて", Filename: "40_muffled.wav"},
		{Emoji: "😌", Label: "安堵・満足", Filename: "41_relieved.wav"},
		{Emoji: "🤔", Label: "疑問の声", Filename: "42_questioning.wav"},
		{Emoji: "💪", Label: "力強く", Filename: "43_stsaisinrong.wav"},
		{Emoji: "📖", Label: "ナレーション・独白", Filename: "45_narration.wav"},
	}
	if len(EmotionEmojiPaletteItems) != len(want) {
		t.Fatalf("palette length = %d, want %d", len(EmotionEmojiPaletteItems), len(want))
	}
	for i, item := range want {
		got := EmotionEmojiPaletteItems[i]
		if got.Emoji != item.Emoji || got.Label != item.Label || got.Filename != item.Filename {
			t.Fatalf("palette[%d] = emoji=%q label=%q filename=%q, want emoji=%q label=%q filename=%q", i, got.Emoji, got.Label, got.Filename, item.Emoji, item.Label, item.Filename)
		}
	}
}
