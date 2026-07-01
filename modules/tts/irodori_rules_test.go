package tts

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestIrodoriSynthesisURL(t *testing.T) {
	tests := []struct {
		name         string
		baseURL      string
		endpointPath string
		want         string
	}{
		{name: "default endpoint", baseURL: "http://127.0.0.1:7870", want: "http://127.0.0.1:7870/api/tts"},
		{name: "configured endpoint", baseURL: "http://127.0.0.1:7870/", endpointPath: "/synthesize", want: "http://127.0.0.1:7870/synthesize"},
		{name: "base already has path", baseURL: "http://127.0.0.1:7870/custom", endpointPath: "/ignored", want: "http://127.0.0.1:7870/custom"},
		{name: "empty base", baseURL: " ", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IrodoriSynthesisURL(tt.baseURL, tt.endpointPath); got != tt.want {
				t.Fatalf("IrodoriSynthesisURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestApplyIrodoriRunGenerationDefaults(t *testing.T) {
	got := ApplyIrodoriRunGenerationDefaults(IrodoriRunGenerationConfig{})
	if got.Checkpoint != DefaultIrodoriCheckpoint ||
		got.ModelDevice != DefaultIrodoriModelDevice ||
		got.ModelPrecision != DefaultIrodoriModelPrecision ||
		got.CodecDevice != DefaultIrodoriCodecDevice ||
		got.CodecPrecision != DefaultIrodoriCodecPrecision ||
		got.NumSteps != DefaultIrodoriNumSteps ||
		got.NumCandidates != DefaultIrodoriNumCandidates ||
		got.CFGGuidanceMode != DefaultIrodoriCFGGuidanceMode ||
		got.CFGScaleText != DefaultIrodoriCFGScaleText ||
		got.CFGScaleSpeaker != DefaultIrodoriCFGScaleSpeaker ||
		got.CFGMinT != DefaultIrodoriCFGMinT ||
		got.CFGMaxT != DefaultIrodoriCFGMaxT ||
		!got.ContextKVCache {
		t.Fatalf("ApplyIrodoriRunGenerationDefaults() did not apply defaults: %#v", got)
	}
}

func TestResolveIrodoriVoiceIDAndName(t *testing.T) {
	if got := ResolveIrodoriVoiceID("male_01"); got != "shiro" {
		t.Fatalf("ResolveIrodoriVoiceID() = %q, want shiro", got)
	}
	if got := ResolveIrodoriVoiceID("female_01"); got != "mio" {
		t.Fatalf("ResolveIrodoriVoiceID() = %q, want mio", got)
	}
	if got := ResolveIrodoriVoiceName("shi-gozaki"); got != "Shiro" {
		t.Fatalf("ResolveIrodoriVoiceName() = %q, want Shiro", got)
	}
	if got := ResolveIrodoriVoiceName("Custom Voice"); got != "Custom Voice" {
		t.Fatalf("ResolveIrodoriVoiceName() = %q, want custom passthrough", got)
	}
}

func TestResolveIrodoriStyle(t *testing.T) {
	tests := []struct {
		name    string
		emotion IrodoriStyleEmotion
		want    string
	}{
		{name: "explicit alert", emotion: IrodoriStyleEmotion{Emotion: "alert"}, want: "urgent"},
		{name: "explicit cheerful", emotion: IrodoriStyleEmotion{Emotion: "happy"}, want: "bright"},
		{name: "explicit calm", emotion: IrodoriStyleEmotion{Emotion: "calm"}, want: "calm"},
		{name: "neutral empty", emotion: IrodoriStyleEmotion{}, want: "neutral"},
		{name: "intensity urgent", emotion: IrodoriStyleEmotion{Intensity: 0.8}, want: "urgent"},
		{name: "expressive bright", emotion: IrodoriStyleEmotion{Expressiveness: 0.7}, want: "bright"},
		{name: "slow calm", emotion: IrodoriStyleEmotion{Speed: 0.4}, want: "calm"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveIrodoriStyle(tt.emotion); got != tt.want {
				t.Fatalf("ResolveIrodoriStyle() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildIrodoriSynthesisPayload(t *testing.T) {
	got := BuildIrodoriSynthesisPayload(IrodoriSynthesisPayloadInput{
		Voice: " Mio ",
		Style: " bright ",
		Text:  "こんにちは",
	})
	if got.Voice != "Mio" || got.Style != "bright" || got.Text != "こんにちは。" {
		t.Fatalf("BuildIrodoriSynthesisPayload() = %#v", got)
	}
}

func TestBuildIrodoriUploadedAudioFileData(t *testing.T) {
	got, ok := BuildIrodoriUploadedAudioFileData(" /tmp/reference.wav ").(IrodoriUploadedAudioFileData)
	if !ok {
		t.Fatalf("BuildIrodoriUploadedAudioFileData() type = %T", got)
	}
	if got.Path != "/tmp/reference.wav" || got.Meta.Type != "gradio.FileData" {
		t.Fatalf("BuildIrodoriUploadedAudioFileData() = %#v", got)
	}
	if BuildIrodoriUploadedAudioFileData(" ") != nil {
		t.Fatal("expected nil uploaded audio for empty reference path")
	}
}

func TestIrodoriRunGenerationURL(t *testing.T) {
	got := IrodoriRunGenerationURL("http://127.0.0.1:7870")
	want := "http://127.0.0.1:7870/gradio_api/run/_run_generation"
	if got != want {
		t.Fatalf("IrodoriRunGenerationURL() = %q, want %q", got, want)
	}
	if got := IrodoriRunGenerationURL(want); got != want {
		t.Fatalf("IrodoriRunGenerationURL() should preserve full endpoint: got %q", got)
	}
}

func TestIrodoriRunGenerationData(t *testing.T) {
	audio := map[string]any{"path": "/tmp/ref.wav"}
	got := IrodoriRunGenerationData(IrodoriRunGenerationConfig{
		Checkpoint:            "ckpt",
		ModelDevice:           "cuda",
		ModelPrecision:        "bf16",
		CodecDevice:           "cpu",
		CodecPrecision:        "fp32",
		EnableWatermark:       true,
		NumSteps:              12,
		NumCandidates:         2,
		SeedRaw:               "123",
		CFGGuidanceMode:       "speaker",
		CFGScaleText:          1.2,
		CFGScaleSpeaker:       2.3,
		CFGScaleRaw:           "3.4",
		CFGMinT:               0.1,
		CFGMaxT:               0.9,
		ContextKVCache:        true,
		TruncationFactorRaw:   "0.8",
		RescaleKRaw:           "0.1",
		RescaleSigmaRaw:       "0.2",
		SpeakerKVScaleRaw:     "0.3",
		SpeakerKVMinTRaw:      "0.4",
		SpeakerKVMaxLayersRaw: "16",
	}, "hello", audio)
	want := []any{
		"ckpt", "cuda", "bf16", "cpu", "fp32", true,
		"hello", audio, 12, 2, "123", "speaker", 1.2, 2.3, "3.4",
		0.1, 0.9, true, "0.8", "0.1", "0.2", "0.3", "0.4", "16",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("IrodoriRunGenerationData() = %#v, want %#v", got, want)
	}
}

func TestRewriteLoopbackIrodoriFileURL(t *testing.T) {
	got := RewriteLoopbackIrodoriFileURL(
		"http://192.168.1.31:7870",
		"http://127.0.0.1:7870/gradio_api/file=/tmp/sample.wav",
	)
	want := "http://192.168.1.31:7870/gradio_api/file=/tmp/sample.wav"
	if got != want {
		t.Fatalf("RewriteLoopbackIrodoriFileURL() = %q, want %q", got, want)
	}
}

func TestResolveIrodoriAudioDownloadURL(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		want    string
		wantErr bool
	}{
		{name: "absolute path", rawURL: "/audio/out.wav", want: "http://irodori.local/audio/out.wav"},
		{name: "relative path", rawURL: "audio/out.wav", want: "http://irodori.local/audio/out.wav"},
		{name: "loopback full url", rawURL: "http://localhost:7870/gradio_api/file=/tmp/out.wav", want: "http://irodori.local/gradio_api/file=/tmp/out.wav"},
		{name: "empty", rawURL: " ", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveIrodoriAudioDownloadURL("http://irodori.local", tt.rawURL)
			if tt.wantErr {
				if err == nil {
					t.Fatal("ResolveIrodoriAudioDownloadURL() err = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveIrodoriAudioDownloadURL() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("ResolveIrodoriAudioDownloadURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseIrodoriSimpleAudioURL(t *testing.T) {
	raw := json.RawMessage(`{"audio":{"path":"/audio/nested.wav"},"path":"/audio/root.wav"}`)
	if got := ParseIrodoriSimpleAudioURL(raw); got != "/audio/nested.wav" {
		t.Fatalf("ParseIrodoriSimpleAudioURL() = %q, want nested path", got)
	}
}

func TestParseIrodoriAudioURLGradioUpdateValue(t *testing.T) {
	raw := json.RawMessage(`{"data":[{"visible":true,"value":{"path":"/tmp/sample.wav","url":"http://irodori.local/gradio_api/file=/tmp/sample.wav","orig_name":"sample.wav","mime_type":null},"__type__":"update"}]}`)
	got, err := ParseIrodoriAudioURL(raw)
	if err != nil {
		t.Fatalf("ParseIrodoriAudioURL() unexpected error: %v", err)
	}
	if got != "http://irodori.local/gradio_api/file=/tmp/sample.wav" {
		t.Fatalf("ParseIrodoriAudioURL() = %q", got)
	}
}

func TestParseIrodoriAudioURLRejectsMissingCandidate(t *testing.T) {
	if _, err := ParseIrodoriAudioURL(json.RawMessage(`{"data":[null]}`)); err == nil {
		t.Fatal("ParseIrodoriAudioURL() err = nil, want error")
	}
}
