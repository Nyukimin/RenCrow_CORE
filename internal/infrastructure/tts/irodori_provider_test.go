package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"
)

func TestIrodoriProvider_SynthesizeBinaryWAV(t *testing.T) {
	tmpDir := t.TempDir()
	p := NewIrodoriProvider(IrodoriConfig{
		BaseURL:   "http://irodori.local",
		VoiceID:   "Female_01",
		VoiceName: "Mio",
	})
	p.client = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/tts":
			var payload struct {
				Voice string  `json:"voice"`
				Style string  `json:"style"`
				Text  string  `json:"text"`
				Speed float64 `json:"speed"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.Voice != "Mio" || payload.Style != "neutral" || payload.Text != "hello。" || payload.Speed != 1.2 {
				t.Fatalf("unexpected irodori payload: %+v", payload)
			}
			body := `{"audio_url":"http://irodori.local/audio/sample.wav"}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header)}, nil
		case r.Method == http.MethodGet && r.URL.Path == "/audio/sample.wav":
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString("RIFFirodori")),
				Header:     make(http.Header),
			}, nil
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		return nil, nil
	})}

	out, err := p.Synthesize(context.Background(), SynthesisInput{Text: "hello", OutputDir: tmpDir, FilePrefix: "irodori"})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if out.Provider != "irodori" || out.VoiceID != "mio" {
		t.Fatalf("unexpected output: %+v", out)
	}
	if out.AudioURL != "http://irodori.local/audio/sample.wav" {
		t.Fatalf("unexpected audio url: %q", out.AudioURL)
	}
	got, err := os.ReadFile(out.AudioFilePath)
	if err != nil || string(got) != "RIFFirodori" {
		t.Fatalf("unexpected wav output: err=%v got=%q", err, string(got))
	}
}

func TestIrodoriUploadedAudio(t *testing.T) {
	raw, err := json.Marshal(irodoriUploadedAudio("/tmp/reference.wav"))
	if err != nil {
		t.Fatalf("marshal uploaded audio: %v", err)
	}
	var got struct {
		Path string `json:"path"`
		Meta struct {
			Type string `json:"_type"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode uploaded audio: %v", err)
	}
	if got.Path != "/tmp/reference.wav" || got.Meta.Type != "gradio.FileData" {
		t.Fatalf("unexpected uploaded audio meta: %#v", got)
	}
	if irodoriUploadedAudio("") != nil {
		t.Fatal("expected nil uploaded audio for empty reference path")
	}
}

func TestRewriteLoopbackIrodoriFileURL(t *testing.T) {
	got := rewriteLoopbackIrodoriFileURL(
		"http://192.168.1.31:7870",
		"http://127.0.0.1:7870/gradio_api/file=/tmp/sample.wav",
	)
	want := "http://192.168.1.31:7870/gradio_api/file=/tmp/sample.wav"
	if got != want {
		t.Fatalf("unexpected rewritten url: got=%s want=%s", got, want)
	}
}

func TestIrodoriProvider_ResolvesShiroVoice(t *testing.T) {
	p := NewIrodoriProvider(IrodoriConfig{BaseURL: "http://irodori.local"})
	var gotVoice string
	p.client = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString("RIFFshiro")), Header: make(http.Header)}, nil
		}
		var payload struct {
			Voice string `json:"voice"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		gotVoice = payload.Voice
		body := `{"url":"/audio/shiro.wav"}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header)}, nil
	})}

	out, err := p.Synthesize(context.Background(), SynthesisInput{
		Text:      "hello",
		OutputDir: t.TempDir(),
		VoiceProfile: VoiceProfile{
			VoiceID: "shiro",
		},
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if out.VoiceID != "shiro" {
		t.Fatalf("expected shiro voice id, got %+v", out)
	}
	if gotVoice != "Shiro" {
		t.Fatalf("expected Shiro voice name, got %q", gotVoice)
	}
}

func TestIrodoriProvider_UsesConfiguredEndpointPathAndStyle(t *testing.T) {
	p := NewIrodoriProvider(IrodoriConfig{
		BaseURL:      "http://irodori.local",
		EndpointPath: "/synthesize",
	})
	p.client = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString("RIFFout")), Header: make(http.Header)}, nil
		}
		if r.URL.Path != "/synthesize" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var payload struct {
			Style string  `json:"style"`
			Speed float64 `json:"speed"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Style != "urgent" || payload.Speed != 1.2 {
			t.Fatalf("expected urgent style and default speed, got %+v", payload)
		}
		body := `{"audio":{"url":"/audio/out.wav"}}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header)}, nil
	})}

	if _, err := p.Synthesize(context.Background(), SynthesisInput{
		Text:      "hello",
		OutputDir: t.TempDir(),
		Emotion:   EmotionState{Emotion: "alert"},
	}); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestParseIrodoriAudioURL_GradioUpdateValue(t *testing.T) {
	body := `{"data":[{"visible":true,"value":{"path":"/tmp/sample.wav","url":"http://irodori.local/gradio_api/file=/tmp/sample.wav","orig_name":"sample.wav","mime_type":null},"__type__":"update"}]}`
	got, err := parseIrodoriAudioURL(bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got != "http://irodori.local/gradio_api/file=/tmp/sample.wav" {
		t.Fatalf("unexpected url: %s", got)
	}
}
