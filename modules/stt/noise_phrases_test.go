package stt

import "testing"

func TestNoisePhraseFilterMatchesWholeUtteranceKnownPhrases(t *testing.T) {
	f := NewNoisePhraseFilter(nil)
	for _, text := range []string{
		"ご視聴ありがとうございました",
		"ご視聴ありがとうございました。",
		" ご視聴 ありがとう ございました！ ",
		"チャンネル登録お願いします",
		"チャンネル登録よろしくお願いします。",
		"最後までご視聴いただきありがとうございました",
		"Thanks for watching!",
		"thank you for watching.",
		"ＴＨＡＮＫＳ ＦＯＲ ＷＡＴＣＨＩＮＧ",
	} {
		if _, ok := f.Match(text); !ok {
			t.Fatalf("Match(%q) = false, want a known hallucination phrase", text)
		}
	}
}

func TestNoisePhraseFilterIsConservative(t *testing.T) {
	f := NewNoisePhraseFilter(nil)
	for _, text := range []string{
		"ありがとうございました",
		"はい",
		"今日の天気を教えて。ご視聴ありがとうございました",
		"ご視聴ありがとうございましたと言って",
		"チャンネル登録の方法を教えて",
		"",
		"   ",
	} {
		if phrase, ok := f.Match(text); ok {
			t.Fatalf("Match(%q) matched %q; only whole-utterance known phrases may be dropped", text, phrase)
		}
	}
}

func TestNoisePhraseFilterAcceptsConfiguredExtraPhrases(t *testing.T) {
	f := NewNoisePhraseFilter([]string{" また見てね ", "", "  "})
	if _, ok := f.Match("また見てね。"); !ok {
		t.Fatal("configured phrase must be matched after normalisation")
	}
	if _, ok := f.Match("ご視聴ありがとうございました"); !ok {
		t.Fatal("configured phrases extend, not replace, the defaults")
	}
}

func TestNoisePhraseFilterNilReceiverMatchesNothing(t *testing.T) {
	var f *NoisePhraseFilter
	if _, ok := f.Match("ご視聴ありがとうございました"); ok {
		t.Fatal("nil filter must not match")
	}
}

func TestClassifyTranscript(t *testing.T) {
	f := NewNoisePhraseFilter(nil)
	tests := []struct {
		name      string
		raw       string
		wantText  string
		wantEmpty string
		wantErr   bool
	}{
		{"plain text", " 今日の予定を教えて\n", "今日の予定を教えて", "", false},
		{"empty result", "  \n", "", EmptyReasonNoSpeech, false},
		{"hallucination phrase", "ご視聴ありがとうございました。\n", "", EmptyReasonFiltered, false},
		{"model control marker", "<|channel>thought", "", EmptyReasonFiltered, false},
		{"llm style refusal is a provider error", "音声ファイルが添付されていないため書き起こしできません", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyTranscript(tt.raw, f)
			if got.Text != tt.wantText || got.EmptyReason != tt.wantEmpty || got.ProviderError != tt.wantErr {
				t.Fatalf("ClassifyTranscript(%q) = %+v", tt.raw, got)
			}
		})
	}
}
