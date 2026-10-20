package speech

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseExampleSpeechPlanPreservesDisplayText(t *testing.T) {
	raw := `{"schema_version":"rencrow.speech/1.0","utterances":[{"segments":[{"text":"え、","before":[{"kind":"surprise","strength":"light"}],"after":[{"kind":"pause","strength":"light"}]},{"text":"それ、本当にやったんですか？"}],"delivery":{"tone":"amused","attitude":"teasing","energy":"medium","pace":"normal","mode":"dialogue"},"pause_after_ms":150}]}`

	result := Parse(raw)
	if !result.Valid {
		t.Fatalf("Parse() invalid: %s", result.FallbackReason)
	}
	if result.DisplayText != "え、それ、本当にやったんですか？" {
		t.Fatalf("DisplayText = %q", result.DisplayText)
	}
	if result.Plan.SchemaVersion != SchemaVersion {
		t.Fatalf("schema version = %q", result.Plan.SchemaVersion)
	}
	if got := result.Plan.Utterances[0].Delivery; got.Tone != "amused" || got.Attitude != "teasing" || got.Mode != "dialogue" {
		t.Fatalf("delivery was not preserved: %#v", got)
	}
}

func TestDisplayTextSeparatesUtterancesWithOneSpace(t *testing.T) {
	result := Normalize(SpeechPlan{
		SchemaVersion: SchemaVersion,
		Utterances: []Utterance{
			{Segments: []Segment{{Text: "第一発話。"}}},
			{Segments: []Segment{{Text: "第二発話。"}}},
		},
	})

	if !result.Valid {
		t.Fatalf("Normalize() invalid: %s", result.FallbackReason)
	}
	if result.DisplayText != "第一発話。 第二発話。" {
		t.Fatalf("DisplayText = %q", result.DisplayText)
	}
}

func TestNormalizeRemovesIrodoriControlEmojiButKeepsUnicodeText(t *testing.T) {
	controls := []string{"⏸️", "🤭", "😮‍💨", "😮", "👂", "😭", "😱", "🤧", "🥱", "🎵", "👌", "😲", "🤔", "😌"}
	text := "日本語" + strings.Join(controls, "") + " café — stays"
	result := Normalize(SpeechPlan{
		SchemaVersion: SchemaVersion,
		Utterances:    []Utterance{{Segments: []Segment{{Text: text}}}},
	})

	if !result.Valid {
		t.Fatalf("Normalize() invalid: %s", result.FallbackReason)
	}
	const want = "日本語 café — stays"
	if result.DisplayText != want {
		t.Fatalf("DisplayText = %q, want %q", result.DisplayText, want)
	}
	if got := result.Plan.Utterances[0].Segments[0].Text; got != want {
		t.Fatalf("Gateway segment text = %q, want %q", got, want)
	}
	serialized, err := json.Marshal(result.Plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, control := range controls {
		if strings.Contains(string(serialized), control) {
			t.Errorf("Gateway plan leaked control emoji %q: %s", control, serialized)
		}
	}
}

func TestNormalizePreservesGeneralEmojiForDisplayAndSuppressesSpeech(t *testing.T) {
	text := "Before" + gatewayRejectedRunesFixture() + "日本語 café after"
	result := Normalize(SpeechPlan{
		SchemaVersion: SchemaVersion,
		Utterances: []Utterance{{
			Segments: []Segment{{Text: text}},
			Delivery: Delivery{Tone: "amused", Attitude: "gentle", Energy: "medium", Pace: "normal", Mode: "dialogue"},
		}},
	})

	if result.Valid {
		t.Fatal("Normalize() accepted text containing symbols rejected by the speech gateway")
	}
	if result.FallbackReason != FallbackUnsupportedEmoji {
		t.Fatalf("FallbackReason = %q, want %q", result.FallbackReason, FallbackUnsupportedEmoji)
	}
	want := text
	if result.DisplayText != want {
		t.Fatalf("DisplayText = %q, want %q", result.DisplayText, want)
	}
	utterance := result.Plan.Utterances[0]
	if got := utterance.Segments[0].Text; got != want {
		t.Fatalf("Gateway segment text = %q, want %q", got, want)
	}
	if utterance.Delivery != (Delivery{Tone: "amused", Attitude: "gentle", Energy: "medium", Pace: "normal", Mode: "dialogue"}) {
		t.Fatalf("delivery changed while sanitizing text: %#v", utterance.Delivery)
	}
}

func TestInvalidPlanDisplayFallbacksRemoveOnlyIrodoriControls(t *testing.T) {
	text := "Before" + irodoriControlEmojiFixture() + gatewayRejectedRunesFixture() + "日本語 café after"
	want := "Before" + gatewayRejectedRunesFixture() + "日本語 café after"
	tests := []struct {
		name string
		plan SpeechPlan
	}{
		{
			name: "schema",
			plan: SpeechPlan{SchemaVersion: "rencrow.speech/unknown", Utterances: []Utterance{{Segments: []Segment{{Text: text}}}}},
		},
		{
			name: "structure",
			plan: SpeechPlan{SchemaVersion: SchemaVersion, Utterances: []Utterance{
				{Segments: []Segment{{Text: text}}},
				{},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Normalize(tt.plan)
			if result.Valid {
				t.Fatal("Normalize() accepted invalid plan")
			}
			wantDisplay := want
			if tt.name == "structure" {
				wantDisplay += DisplayUtteranceSeparator
			}
			if result.DisplayText != wantDisplay {
				t.Fatalf("DisplayText = %q, want %q", result.DisplayText, wantDisplay)
			}
			if got := result.Plan.Utterances[0].Segments[0].Text; got != want {
				t.Fatalf("fallback plan text = %q, want %q", got, want)
			}
		})
	}
}

func TestParseInvalidSchemaAndPlainTextFallbacksRemoveOnlyIrodoriControls(t *testing.T) {
	text := "Before" + irodoriControlEmojiFixture() + gatewayRejectedRunesFixture() + "日本語 café after"
	want := "Before" + gatewayRejectedRunesFixture() + "日本語 café after"
	rawInvalidSchema := `{"schema_version":7,"utterances":[{"segments":[{"text":"` + text + `"}]}]}`
	result := Parse(rawInvalidSchema)
	if result.Valid || result.FallbackReason != FallbackInvalidSchema {
		t.Fatalf("invalid schema result = %#v", result)
	}
	if result.DisplayText != want {
		t.Fatalf("invalid schema DisplayText = %q, want %q", result.DisplayText, want)
	}

	result = Parse(text)
	if result.Valid || result.FallbackReason != FallbackPlainText {
		t.Fatalf("plain-text result = %#v", result)
	}
	if result.DisplayText != want {
		t.Fatalf("plain-text DisplayText = %q, want %q", result.DisplayText, want)
	}
}

func gatewayRejectedRunesFixture() string {
	return string([]rune{
		'\u200d', '\u20e3', '\ufe0f',
		0x2300, 0x23ff, 0x2600, 0x27bf, 0x1f000, 0x1faff,
	})
}

func irodoriControlEmojiFixture() string {
	return strings.Join([]string{"⏸️", "🤭", "😮‍💨", "😮", "👂", "😭", "😱", "🤧", "🥱", "🎵", "👌", "😲", "🤔", "😌"}, "")
}

func TestNormalizePreservesMathDelimitersForSpeech(t *testing.T) {
	const text = "答えは⌈x⌉です。"
	result := Normalize(SpeechPlan{
		SchemaVersion: SchemaVersion,
		Utterances:    []Utterance{{Segments: []Segment{{Text: text}}}},
	})
	if !result.Valid {
		t.Fatalf("Normalize() rejected ordinary mathematical delimiters: %s", result.FallbackReason)
	}
	if result.DisplayText != text {
		t.Fatalf("DisplayText = %q, want %q", result.DisplayText, text)
	}
	if got := result.Plan.Utterances[0].Segments[0].Text; got != text {
		t.Fatalf("Gateway segment text = %q, want %q", got, text)
	}
}

func TestParsePreservesGeneralEmojiButSuppressesSpeech(t *testing.T) {
	text := "Before" + irodoriControlEmojiFixture() + gatewayRejectedRunesFixture() + " after"
	want := "Before" + gatewayRejectedRunesFixture() + " after"
	encoded, err := json.Marshal(SpeechPlan{
		SchemaVersion: SchemaVersion,
		Utterances:    []Utterance{{Segments: []Segment{{Text: text}}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	result := Parse(string(encoded))
	if result.Valid || result.FallbackReason != FallbackUnsupportedEmoji {
		t.Fatalf("Parse() result = %#v", result)
	}
	if result.DisplayText != want {
		t.Fatalf("DisplayText = %q, want %q", result.DisplayText, want)
	}
	if got := result.Plan.Utterances[0].Segments[0].Text; got != want {
		t.Fatalf("non-synthesizable plan text = %q, want preserved text %q", got, want)
	}
}

func TestParsePlainTextPreservesDisplayAndSuppressesSpeech(t *testing.T) {
	for _, raw := range []string{"mio: こんにちは、世界！", "[mio] こんにちは、世界！"} {
		result := Parse(raw)
		if result.Valid {
			t.Fatalf("Parse(%q) marked plain text as safe for speech synthesis", raw)
		}
		if result.FallbackReason != FallbackPlainText {
			t.Fatalf("Parse(%q) FallbackReason = %q, want %q", raw, result.FallbackReason, FallbackPlainText)
		}
		if result.DisplayText != raw {
			t.Fatalf("Parse(%q) DisplayText = %q, want original plain text", raw, result.DisplayText)
		}
	}
}

func TestNormalizeDefaultsUnknownDeliveryAndDropsExcessCues(t *testing.T) {
	plan := SpeechPlan{
		SchemaVersion: SchemaVersion,
		Utterances: []Utterance{{
			Segments: []Segment{{
				Text: "やった！",
				Before: []Cue{
					{Kind: "sigh", Strength: "light"},
					{Kind: "breath", Strength: "normal"},
					{Kind: "gasp", Strength: "strong"},
					{Kind: "unknown", Strength: "normal"},
				},
				After: []Cue{{Kind: "surprise", Strength: "light"}},
			}},
			Delivery:     Delivery{Tone: "alien", Attitude: "alien", Energy: "extreme", Pace: "instant", Mode: "telepathy"},
			PauseAfterMS: 225,
		}},
	}

	result := Normalize(plan)
	if !result.Valid {
		t.Fatalf("Normalize() invalid: %s", result.FallbackReason)
	}
	got := result.Plan.Utterances[0]
	if got.Delivery != (Delivery{Tone: "neutral", Attitude: "neutral", Energy: "medium", Pace: "normal", Mode: "dialogue"}) {
		t.Fatalf("delivery defaults = %#v", got.Delivery)
	}
	if got.PauseAfterMS != 150 {
		t.Fatalf("pause_after_ms = %d, want 150 for a tie", got.PauseAfterMS)
	}
	if len(got.Segments[0].Before) != 2 || got.Segments[0].Before[0].Kind != "sigh" || got.Segments[0].Before[1].Kind != "breath" {
		t.Fatalf("before cues = %#v", got.Segments[0].Before)
	}
	if len(got.Segments[0].After) != 0 {
		t.Fatalf("cue kinds beyond the first two should be dropped: %#v", got.Segments[0].After)
	}
}

func TestNormalizeSplitsLongSegmentsAtPunctuationWithoutChangingDisplay(t *testing.T) {
	text := strings.Repeat("あ。", 65)
	result := Normalize(SpeechPlan{
		SchemaVersion: SchemaVersion,
		Utterances: []Utterance{{
			Segments: []Segment{{Text: text}},
		}},
	})

	if !result.Valid {
		t.Fatalf("Normalize() invalid: %s", result.FallbackReason)
	}
	if result.DisplayText != text {
		t.Fatalf("DisplayText changed: got %d chars, want %d", len([]rune(result.DisplayText)), len([]rune(text)))
	}
	segments := result.Plan.Utterances[0].Segments
	if len(segments) != 2 {
		t.Fatalf("split segment count = %d, want 2", len(segments))
	}
	for i, segment := range segments {
		if got := len([]rune(segment.Text)); got > MaxSegmentRunes {
			t.Fatalf("segment %d has %d runes, limit %d", i, got, MaxSegmentRunes)
		}
	}
}

func TestParseKeepsFullDisplayWhenSpeechPlanExceeds600Chars(t *testing.T) {
	segments := []string{strings.Repeat("あ", 34), strings.Repeat("い", 34), strings.Repeat("う", 34)}
	utterances := make([]Utterance, 0, 6)
	for i := 0; i < 6; i++ {
		utterances = append(utterances, Utterance{
			Segments: []Segment{{Text: segments[0]}, {Text: segments[1]}, {Text: segments[2]}},
		})
	}
	encoded, err := json.Marshal(SpeechPlan{SchemaVersion: SchemaVersion, Utterances: utterances})
	if err != nil {
		t.Fatal(err)
	}

	result := Parse(string(encoded))
	if result.Valid {
		t.Fatal("Parse() accepted a display longer than 600 characters")
	}
	if result.FallbackReason != FallbackSpeechPlanLimitExceeded {
		t.Fatalf("FallbackReason = %q", result.FallbackReason)
	}
	if got, want := len([]rune(result.DisplayText)), 6*(34*3)+5; got != want {
		t.Fatalf("DisplayText length = %d, want preserved original length %d", got, want)
	}
}

func TestParseRejectsForeignFieldsAndDoesNotRenderCueSymbols(t *testing.T) {
	raw := `{"schema_version":"rencrow.speech/1.0","utterances":[{"segments":[{"text":"びっくりしました。","before":[{"kind":"surprise","strength":"light"}]}],"delivery":{"tone":"surprised","attitude":"neutral","energy":"medium","pace":"normal","mode":"dialogue"},"voice_id":"female_01","caption":"happy","model":"model-name"}]}`
	result := Parse(raw)
	if result.Valid {
		t.Fatal("Parse() accepted voice/model/caption fields outside SpeechPlan v1.0")
	}
	if result.FallbackReason != FallbackUnknownField {
		t.Fatalf("FallbackReason = %q", result.FallbackReason)
	}
	if result.DisplayText != "びっくりしました。" {
		t.Fatalf("DisplayText = %q", result.DisplayText)
	}
	if strings.ContainsAny(result.DisplayText, "😮🎤") {
		t.Fatalf("cue control symbols leaked into display: %q", result.DisplayText)
	}
	serialized, err := json.Marshal(result.Plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"voice_id", "caption", "model", "emoji", "target_url", "lora_adapter", "num_steps", "cfg_scale"} {
		if strings.Contains(string(serialized), forbidden) {
			t.Errorf("Gateway plan serialization contains forbidden field %q: %s", forbidden, serialized)
		}
	}
}

func TestParseOnlyRepairsMissingClosingDelimiters(t *testing.T) {
	raw := `{"schema_version":"rencrow.speech/1.0","utterances":[{"segments":[{"text":"戻せる本文。"}]}]`
	result := Parse(raw)
	if !result.Valid {
		t.Fatalf("Parse() did not accept safe missing-closer repair: %s", result.FallbackReason)
	}
	if result.DisplayText != "戻せる本文。" {
		t.Fatalf("DisplayText = %q", result.DisplayText)
	}

	unsafe := `{"schema_version":"rencrow.speech/1.0","utterances":[{"segments":[{"text":"末尾カンマ"},]}]`
	if result := Parse(unsafe); result.Valid || result.FallbackReason == "" {
		t.Fatalf("Parse() accepted unsafe JSON repair: %#v", result)
	}
}
