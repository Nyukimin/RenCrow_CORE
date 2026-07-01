package tts

import "testing"

func TestResolveSBV2VoiceParams(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		wantName    string
		wantModelID int
	}{
		{name: "mio alias", raw: "mio", wantName: "amitaro", wantModelID: 0},
		{name: "female alias", raw: "female_01", wantName: "amitaro", wantModelID: 0},
		{name: "shiro alias", raw: "shiro", wantName: "shi-gozaki", wantModelID: 6},
		{name: "male alias", raw: "male_01", wantName: "shi-gozaki", wantModelID: 6},
		{name: "custom", raw: " custom-speaker ", wantName: "custom-speaker", wantModelID: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveSBV2VoiceParams(tt.raw)
			if got.Name != tt.wantName || got.ModelID != tt.wantModelID || got.SpeakerID != 0 || got.Style != "Neutral" {
				t.Fatalf("ResolveSBV2VoiceParams() = %#v", got)
			}
		})
	}
}

func TestSBV2VoiceURL(t *testing.T) {
	got := SBV2VoiceURL("http://sbv2.local", "hello", SBV2VoiceParams{Name: "amitaro", ModelID: 0, SpeakerID: 0, Style: "Neutral"})
	want := "http://sbv2.local/voice?model_id=0&speaker_id=0&style=Neutral&text=hello%E3%80%82"
	if got != want {
		t.Fatalf("SBV2VoiceURL() = %q, want %q", got, want)
	}
	got = SBV2VoiceURL("http://sbv2.local/voice", "hello!", SBV2VoiceParams{Name: "shi-gozaki", ModelID: 6, SpeakerID: 0, Style: "Neutral"})
	want = "http://sbv2.local/voice?model_id=6&speaker_id=0&style=Neutral&text=hello%21"
	if got != want {
		t.Fatalf("SBV2VoiceURL() should preserve /voice and punctuation: got %q", got)
	}
}

func TestSBV2EditorURL(t *testing.T) {
	if got := SBV2EditorURL("http://sbv2.local/api/synthesis", "models_info"); got != "http://sbv2.local/api/models_info" {
		t.Fatalf("SBV2EditorURL(models_info) = %q", got)
	}
	if got := SBV2EditorURL("http://sbv2.local/api/synthesis", "g2p"); got != "http://sbv2.local/api/g2p" {
		t.Fatalf("SBV2EditorURL(g2p) = %q", got)
	}
	if got := SBV2EditorURL("http://sbv2.local/api/synthesis", "synthesis"); got != "http://sbv2.local/api/synthesis" {
		t.Fatalf("SBV2EditorURL(default) = %q", got)
	}
}

func TestBuildSBV2EditorRequestPayloads(t *testing.T) {
	g2p := BuildSBV2G2PRequestPayload("こんにちは")
	if g2p.Text != "こんにちは。" {
		t.Fatalf("BuildSBV2G2PRequestPayload() = %#v", g2p)
	}

	moraToneList := []map[string]any{{"mora": "コ", "tone": 0}}
	got := BuildSBV2EditorSynthesisPayload(SBV2EditorSynthesisPayloadInput{
		Model:        " model ",
		ModelFile:    " file.safetensors ",
		Text:         "こんにちは",
		MoraToneList: moraToneList,
		Speaker:      " speaker ",
	})
	if got.Model != "model" || got.ModelFile != "file.safetensors" || got.Text != "こんにちは。" || got.Speaker != "speaker" {
		t.Fatalf("BuildSBV2EditorSynthesisPayload() = %#v", got)
	}
	if len(got.MoraToneList) != 1 || got.MoraToneList[0]["mora"] != "コ" {
		t.Fatalf("moraToneList not preserved: %#v", got.MoraToneList)
	}
	moraToneList = append(moraToneList, map[string]any{"mora": "ン"})
	if len(got.MoraToneList) != 1 {
		t.Fatalf("payload should copy moraToneList slice header, got %#v", got.MoraToneList)
	}
}

func TestEnsureTTSPunctuation(t *testing.T) {
	if got := EnsureTTSPunctuation("こんにちは"); got != "こんにちは。" {
		t.Fatalf("EnsureTTSPunctuation() = %q", got)
	}
	if got := EnsureTTSPunctuation("こんにちは！"); got != "こんにちは！" {
		t.Fatalf("EnsureTTSPunctuation() should preserve punctuation: %q", got)
	}
	if got := EnsureTTSPunctuation(" "); got != "" {
		t.Fatalf("EnsureTTSPunctuation() should trim empty text: %q", got)
	}
}
