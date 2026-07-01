package tts

import (
	"strings"
	"testing"
)

func TestFormatIdleChatTTSTextTopicAnnouncementUsesSpeechPrefix(t *testing.T) {
	got := FormatIdleChatTTSText(IdleChatSpeechInput{
		From:    "user",
		To:      "mio",
		Content: "今日のお題（news）: 医療制度の検討が現場に与える影響",
	})
	if got != "きょうのおだい、医療制度の検討が現場に与える影響。" {
		t.Fatalf("unexpected topic speech text: %q", got)
	}
}

func TestFormatIdleChatDisplayTextTopicAnnouncementUsesDisplayPrefix(t *testing.T) {
	got := FormatIdleChatDisplayText(IdleChatSpeechInput{
		From:    "user",
		To:      "mio",
		Content: "今日のお題（news）: 医療制度の検討が現場に与える影響",
	})
	if got != "今日のお題：医療制度の検討が現場に与える影響" {
		t.Fatalf("unexpected topic display text: %q", got)
	}
}

func TestFormatIdleChatTTSTextRemovesNotes(t *testing.T) {
	got := FormatIdleChatTTSText(IdleChatSpeechInput{
		From:    "mio",
		Content: "今回のまとめです。\n注記: テンプレ反復で打ち切り\n\n本文を読み上げます。",
	})
	if strings.Contains(got, "注記:") {
		t.Fatalf("note leaked into speech text: %q", got)
	}
	if !strings.Contains(got, "今回のまとめです。") || !strings.Contains(got, "本文を読み上げます。") {
		t.Fatalf("unexpected speech text: %q", got)
	}
}

func TestStripIdleChatSpeakerAndReasoningLinesPrefersCurrentSpeaker(t *testing.T) {
	got := StripIdleChatSpeakerAndReasoningLines("mio: いい感じだね\nshiro: 静かに見よう", "shiro")
	if got != "静かに見よう" {
		t.Fatalf("unexpected speaker-filtered text: %q", got)
	}
}
