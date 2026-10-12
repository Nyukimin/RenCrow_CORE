package conversation

import (
	"strings"
	"sync"
	"testing"
)

func TestStripLeadingSpeakerLabels(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "plain label", content: "[mio] こんにちは", want: "こんにちは"},
		{name: "case and colon", content: " [MIO]：こんにちは ", want: "こんにちは"},
		{name: "ascii colon and spaces", content: "[midori] : こんにちは", want: "こんにちは"},
		{name: "label without separator", content: "[shiro]からの報告です", want: "からの報告です"},
		{name: "doubled label", content: "[midori] [midori] 夕焼けです", want: "夕焼けです"},
		{name: "doubled label without space", content: "[midori][midori]夕焼けです", want: "夕焼けです"},
		{name: "mixed other agents", content: "[midori] [shiro] [kuro] 夕焼けです", want: "夕焼けです"},
		{name: "full width space", content: "　[mio]　こんにちは", want: "こんにちは"},
		{name: "newline after label", content: "[kuro]\n\n結論です", want: "結論です"},
		{name: "every known name", content: "[mio][shiro][kuro][midori][aka][ao][kin][gin]本文", want: "本文"},
		{name: "label only", content: "[midori]", want: ""},
		{name: "doubled label only", content: " [midori] [midori] \n", want: ""},
		{name: "empty", content: "", want: ""},
		{name: "whitespace only", content: " 　\n", want: ""},
		{name: "no label is trimmed only", content: "  こんにちは  ", want: "こんにちは"},
		{name: "embedded label stays", content: "引用: [mio] こんにちは", want: "引用: [mio] こんにちは"},
		{name: "label after body stays", content: "こんにちは [midori] です", want: "こんにちは [midori] です"},
		{name: "unknown bracket stays", content: "[注] 補足です", want: "[注] 補足です"},
		{name: "unknown name stays", content: "[someone] 補足です", want: "[someone] 補足です"},
		{name: "unknown bracket before known label stays", content: "[注] [midori] 補足です", want: "[注] [midori] 補足です"},
		{name: "known label then unknown bracket", content: "[midori] [注] 補足です", want: "[注] 補足です"},
		{name: "partial label stays", content: "[mid", want: "[mid"},
		{name: "kelvin sign is not k", content: "[Kin] 本文", want: "[Kin] 本文"},
		{name: "body colon is kept", content: "[mio] 結論: 実行します", want: "結論: 実行します"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripLeadingSpeakerLabels(tt.content); got != tt.want {
				t.Fatalf("StripLeadingSpeakerLabels(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

// 履歴へ入れる `[speaker] msg` の形式を作る RecallPack.ToPromptMessages と、
// StripLeadingSpeakerLabels は逆変換でなければならない（形式のownerが同じpackage）。
func TestStripLeadingSpeakerLabelsInvertsRecallPackHistoryFormat(t *testing.T) {
	speakers := []Speaker{SpeakerMio, SpeakerShiro, SpeakerKuro, SpeakerMidori, Speaker("heavy"), Speaker("wild")} // 旧別名は canonical 名のラベルになる
	pack := &RecallPack{}
	for _, speaker := range speakers {
		pack.ShortContext = append(pack.ShortContext, Message{Speaker: speaker, Msg: "本文です"})
	}
	assistant := 0
	for _, message := range pack.ToPromptMessages() {
		if message.Role != "assistant" {
			continue
		}
		assistant++
		canonical, _ := CanonicalChatAgentSpeaker(pack.ShortContext[assistant-1].Speaker)
		if want := "[" + string(canonical) + "] 本文です"; message.Content != want {
			t.Fatalf("history format changed: %q, want %q", message.Content, want)
		}
		if got := StripLeadingSpeakerLabels(message.Content); got != "本文です" {
			t.Fatalf("Strip(%q) = %q", message.Content, got)
		}
	}
	if assistant != len(speakers) {
		t.Fatalf("assistant history messages = %d, want %d", assistant, len(speakers))
	}
}

func TestAgentLabelNamesCoverEverySpeakerConstant(t *testing.T) {
	names := map[string]bool{}
	for _, name := range AgentLabelNames() {
		if name != strings.ToLower(name) || strings.TrimSpace(name) == "" {
			t.Fatalf("label name %q must be a non-empty lower-case name", name)
		}
		names[name] = true
	}
	for _, speaker := range []Speaker{SpeakerMio, SpeakerShiro, SpeakerKuro, SpeakerMidori, SpeakerAka, SpeakerAo, SpeakerKin, SpeakerGin} {
		if !names[string(speaker)] {
			t.Errorf("AgentLabelNames() is missing %q", speaker)
		}
	}
	for _, role := range []Speaker{SpeakerUser, SpeakerSystem, SpeakerTool, SpeakerMemory} {
		if names[string(role)] {
			t.Errorf("AgentLabelNames() must not contain non-Agent speaker %q", role)
		}
	}
	// 履歴に `[speaker]` として入る名前（canonical 名）は全て除去対象。
	for _, speaker := range []Speaker{SpeakerMio, SpeakerShiro, SpeakerKuro, SpeakerMidori, Speaker("heavy"), Speaker("wild")} {
		if canonical, ok := CanonicalChatAgentSpeaker(speaker); ok && !names[string(canonical)] {
			t.Errorf("history label %q is not a known label name", canonical)
		}
	}
}

type streamRecorder struct {
	tokens []string
}

func (r *streamRecorder) next(token string) { r.tokens = append(r.tokens, token) }
func (r *streamRecorder) text() string      { return strings.Join(r.tokens, "") }

func runLabelStream(tokens []string) *streamRecorder {
	rec := &streamRecorder{}
	callback, flush := NewSpeakerLabelStreamFilter(rec.next)
	for _, token := range tokens {
		callback(token)
	}
	flush()
	return rec
}

func TestSpeakerLabelStreamFilter(t *testing.T) {
	tests := []struct {
		name   string
		tokens []string
		want   string
	}{
		{name: "split label from production", tokens: []string{"[mid", "ori", "]", " ", "こん", "にちは"}, want: "こんにちは"},
		{name: "mio regression: separator and space across deltas", tokens: []string{" [", "mi", "o]", "：", " こん", "にちは"}, want: "こんにちは"},
		{name: "one character deltas", tokens: strings.Split("[midori] こんにちは", ""), want: "こんにちは"},
		{name: "label in one delta", tokens: []string{"[midori] こんにちは"}, want: "こんにちは"},
		{name: "doubled label split", tokens: []string{"[mid", "ori] [mid", "ori]", " 夕", "焼け"}, want: "夕焼け"},
		{name: "other agent label", tokens: []string{"[", "shiro", "]", "：", "報告", "です"}, want: "報告です"},
		{name: "leading full width space", tokens: []string{"　", "[mio]", "　", "こんにちは"}, want: "こんにちは"},
		{name: "case insensitive", tokens: []string{"[MI", "o]", "こんにちは"}, want: "こんにちは"},
		{name: "no label", tokens: []string{"こん", "にちは"}, want: "こんにちは"},
		{name: "leading whitespace without label", tokens: []string{"\n", "こんにちは"}, want: "こんにちは"},
		{name: "embedded label stays", tokens: []string{"引用: ", "[mio]", " です"}, want: "引用: [mio] です"},
		{name: "label after body stays", tokens: []string{"こんにちは", " [midori]", " です"}, want: "こんにちは [midori] です"},
		{name: "second utterance label inside body stays", tokens: []string{"[mio] 一文目。", "[shiro] 二文目"}, want: "一文目。[shiro] 二文目"},
		{name: "unknown bracket", tokens: []string{"[", "注", "]", " 補足"}, want: "[注] 補足"},
		{name: "unknown name with known prefix", tokens: []string{"[mi", "x]", " 補足"}, want: "[mix] 補足"},
		{name: "known label then unknown bracket", tokens: []string{"[midori] ", "[注] ", "補足"}, want: "[注] 補足"},
		{name: "label only", tokens: []string{"[mid", "ori", "]"}, want: ""},
		{name: "doubled label only", tokens: []string{"[midori]", " ", "[midori]", " "}, want: ""},
		{name: "empty stream", tokens: nil, want: ""},
		{name: "empty tokens", tokens: []string{"", "", "こんにちは"}, want: "こんにちは"},
		{name: "truncated partial label is flushed", tokens: []string{"[mid"}, want: "[mid"},
		{name: "partial label then ordinary text", tokens: []string{"[mid", "night] だった"}, want: "[midnight] だった"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runLabelStream(tt.tokens).text(); got != tt.want {
				t.Fatalf("stream = %q, want %q", got, tt.want)
			}
		})
	}
}

// 確定した後は遅延させない。ラベルを捨てた直後の最初の本文 delta がそのまま流れ、
// その後の delta は1つも保留されない。
func TestSpeakerLabelStreamFilterDoesNotDelayDecidedText(t *testing.T) {
	rec := &streamRecorder{}
	callback, _ := NewSpeakerLabelStreamFilter(rec.next)

	callback("[mid")
	callback("ori] ")
	if len(rec.tokens) != 0 {
		t.Fatalf("label must not be emitted, got %q", rec.tokens)
	}
	callback("こん")
	if got := rec.text(); got != "こん" {
		t.Fatalf("first body delta must be emitted immediately, got %q", got)
	}
	callback("にちは")
	callback("。")
	if got := rec.text(); got != "こんにちは。" {
		t.Fatalf("later deltas must pass through immediately, got %q", got)
	}

	// ラベルで始まらない応答は、先頭が `[` でない限り最初の delta から流れる。
	rec = &streamRecorder{}
	callback, _ = NewSpeakerLabelStreamFilter(rec.next)
	callback("こん")
	if got := rec.text(); got != "こん" {
		t.Fatalf("plain text must be emitted immediately, got %q", got)
	}
}

// 保留するのは既知ラベルの接頭辞である間だけ。接頭辞でなくなった最初の文字で、
// それまでの保留分をまとめて流す。
func TestSpeakerLabelStreamFilterHoldEndsAtFirstMismatch(t *testing.T) {
	rec := &streamRecorder{}
	callback, _ := NewSpeakerLabelStreamFilter(rec.next)
	for _, token := range strings.Split("[zzzzzzzzzzzzzzzz", "") {
		callback(token)
	}
	if got := rec.text(); got != "[zzzzzzzzzzzzzzzz" {
		t.Fatalf("unknown bracket must stream through, got %q", got)
	}
	if rec.tokens[0] != "[z" {
		t.Fatalf("hold must end at the first non-matching character, got first delta %q", rec.tokens[0])
	}
	if len(rec.tokens) != 16 {
		t.Fatalf("later deltas must not be held, got %d deltas", len(rec.tokens))
	}
}

func TestSpeakerLabelStreamFilterNilNext(t *testing.T) {
	callback, flush := NewSpeakerLabelStreamFilter(nil)
	if callback != nil {
		t.Fatal("nil next must stay nil so that callers keep their non-streaming path")
	}
	flush() // must not panic
}

func TestSpeakerLabelStreamFilterFlushIsIdempotent(t *testing.T) {
	rec := &streamRecorder{}
	callback, flush := NewSpeakerLabelStreamFilter(rec.next)
	callback("[mid")
	flush()
	flush()
	callback("night")
	if got := rec.text(); got != "[midnight" {
		t.Fatalf("stream = %q, want %q", got, "[midnight")
	}
}

// 非streamとstreamは、同じ入力に対して同じ本文になる。
func TestSpeakerLabelStreamMatchesNonStreamStrip(t *testing.T) {
	inputs := []string{
		"[midori] [midori] 夕焼けです",
		"[shiro]：報告",
		"[注] 補足",
		"引用: [mio] こんにちは",
		"[mio]",
		"",
		"[mid",
		"　[kuro]　結論",
	}
	for _, input := range inputs {
		rec := runLabelStream(strings.Split(input, ""))
		want := StripLeadingSpeakerLabels(input)
		if got := strings.TrimSpace(rec.text()); got != want {
			t.Errorf("input %q: stream %q != strip %q", input, got, want)
		}
	}
}

// providerは通常 callback を直列に呼ぶが、flush が別 goroutine から呼ばれても安全である。
func TestSpeakerLabelStreamFilterIsSafeForConcurrentUse(t *testing.T) {
	var mu sync.Mutex
	total := 0
	callback, flush := NewSpeakerLabelStreamFilter(func(token string) {
		mu.Lock()
		total += len(token)
		mu.Unlock()
	})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				callback("x")
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		flush()
	}()
	wg.Wait()
	flush()
	if total != 800 {
		t.Fatalf("emitted %d bytes, want 800", total)
	}
}
