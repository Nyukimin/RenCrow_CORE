package tts

import "testing"

func TestFilterSpeakableText_IgnoresThinking(t *testing.T) {
	got := FilterSpeakableText("agent.thinking", "CHAT", "途中")
	if got != "" {
		t.Fatalf("expected empty for thinking, got %q", got)
	}
}

func TestFilterSpeakableText_StripsNotesAndURL(t *testing.T) {
	in := "（internal note）おはようございます。\nhttps://example.com"
	got := FilterSpeakableText("agent.response", "CHAT", in)
	if got != "おはようございます。" {
		t.Fatalf("unexpected filtered text: %q", got)
	}
}

func TestFilterSpeakableText_StripsAckPrefix(t *testing.T) {
	in := "はい、承知いたしました。おはようございます！"
	got := FilterSpeakableText("agent.response", "CHAT", in)
	if got != "おはようございます！" {
		t.Fatalf("unexpected filtered text: %q", got)
	}
}

func TestFilterSpeakableText_ReplacesAgentNamesForSpeech(t *testing.T) {
	in := "Mio が Shiro に相談して、Aka と Ao と Gin、mio と shiro と aka と ao と gin でも確認します。"
	got := FilterSpeakableText("agent.response", "CHAT", in)
	want := "みお が しろ に相談して、あか と あお と ぎん、みお と しろ と あか と あお と ぎん でも確認します。"
	if got != want {
		t.Fatalf("unexpected filtered text: got %q want %q", got, want)
	}
}

func TestFilterSpeakableText_StripsLeadingPunctuation(t *testing.T) {
	in := "、、。。! ! みお、今日はいい天気です。"
	got := FilterSpeakableText("agent.response", "CHAT", in)
	want := "みお、今日はいい天気です。"
	if got != want {
		t.Fatalf("unexpected filtered text: got %q want %q", got, want)
	}
}

func TestFilterSpeakableText_PreservesIdleChatTopicPause(t *testing.T) {
	in := "きょうのおだいです、震災の追悼の杜で、記憶と風景の関係をどう捉えたらどうだろう？です！"
	got := FilterSpeakableText("agent.response", "IDLECHAT", in)
	want := "きょうのおだいです、震災の追悼の杜で、記憶と風景の関係をどう捉えたらどうだろう？です！"
	if got != want {
		t.Fatalf("unexpected filtered text: got %q want %q", got, want)
	}
}
