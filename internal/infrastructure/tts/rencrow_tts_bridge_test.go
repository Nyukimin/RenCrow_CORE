package tts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

func TestRenCrowTTSBridge_PushTextCallsSynthesis(t *testing.T) {
	var gotReqPath string
	var gotAudioURL string
	var gotHeader string
	var gotBody map[string]any

	sink := &sinkStub{}
	bridge := NewRenCrowTTSBridge(RenCrowTTSBridgeConfig{
		HTTPBaseURL: "http://tts.local",
		VoiceID:     "female_01",
		ProviderParams: map[string]any{
			"style": "Neutral",
		},
		Sink: sink,
		OnChunkReady: func(_, _ string, _ int, _, _, _, audioPath, audioURL string) {
			gotAudioURL = moduletts.ChooseNonEmpty(audioURL, audioPath)
		},
	})
	bridge.client = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		gotReqPath = r.URL.Path
		gotHeader = r.Header.Get("X-RenCrow-TTS-Request-Id")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"request_id":"req-1","audio_path":"cache\\x.wav"}`)),
		}, nil
	})}

	if err := bridge.StartSession(context.Background(), orchestrator.TTSSessionStart{
		SessionID:   "session-1",
		CharacterID: "mio",
		VoiceID:     "female_01",
	}); err != nil {
		t.Fatalf("start session failed: %v", err)
	}
	if err := bridge.PushText(context.Background(), "session-1", "こんにちは", &moduletts.EmotionState{}); err != nil {
		t.Fatalf("push text failed: %v", err)
	}

	if gotReqPath != "/synthesis" {
		t.Fatalf("unexpected path: %s", gotReqPath)
	}
	if strings.TrimSpace(gotHeader) == "" {
		t.Fatal("expected X-RenCrow-TTS-Request-Id header")
	}
	if gotBody["provider_params"] == nil {
		t.Fatalf("expected provider_params in request, got %+v", gotBody)
	}
	if gotBody["text"] != "😊こんにちは。" {
		t.Fatalf("expected punctuated text, got %+v", gotBody["text"])
	}
	if gotAudioURL != "http://tts.local/cache/x.wav" {
		t.Fatalf("unexpected audio url: %s", gotAudioURL)
	}
	if sink.calls != 1 {
		t.Fatalf("expected sink submit once, got %d", sink.calls)
	}
}

func TestRenCrowTTSBridge_FormatsProviderSpeechTextOnly(t *testing.T) {
	var gotBody map[string]any
	var readySpeech string
	var readyDisplay string
	bridge := NewRenCrowTTSBridge(RenCrowTTSBridgeConfig{
		HTTPBaseURL: "http://tts.local",
		VoiceID:     "female_01",
		OnChunkReady: func(_, _ string, _ int, _, speechText, displayText, _, _ string) {
			readySpeech = speechText
			readyDisplay = displayText
		},
	})
	bridge.client = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"audio_path":"cache\\formatted.wav"}`)),
		}, nil
	})}
	raw := "**重要**【本文】を `確認` して。"
	if err := bridge.StartSession(context.Background(), orchestrator.TTSSessionStart{
		SessionID:   "session-format",
		CharacterID: "mio",
		VoiceID:     "female_01",
	}); err != nil {
		t.Fatalf("start session failed: %v", err)
	}
	if err := bridge.PushTextWithDisplay(context.Background(), "session-format", raw, raw, nil); err != nil {
		t.Fatalf("push text failed: %v", err)
	}

	if gotBody["text"] != "😊重要「本文」を 確認 して。" {
		t.Fatalf("provider text = %#v", gotBody["text"])
	}
	if readySpeech != "😊重要「本文」を 確認 して。" {
		t.Fatalf("ready speech = %q", readySpeech)
	}
	if readyDisplay != raw {
		t.Fatalf("ready display = %q, want raw %q", readyDisplay, raw)
	}
}

func TestRenCrowTTSBridge_NormalizesLocalAudioPathForViewer(t *testing.T) {
	outputDir := t.TempDir()
	audioPath := filepath.Join(outputDir, "viewer-tts-1.wav")
	var gotAudioPath string
	var gotAudioURL string

	bridge := NewRenCrowTTSBridge(RenCrowTTSBridgeConfig{
		HTTPBaseURL: "http://tts.local",
		OutputDir:   outputDir,
		VoiceID:     "female_01",
		OnChunkReady: func(_, _ string, _ int, _, _, _, audioPath, audioURL string) {
			gotAudioPath = audioPath
			gotAudioURL = audioURL
		},
	})
	bridge.client = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"request_id":"req-local","audio_path":` + strconv.Quote(audioPath) + `}`)),
		}, nil
	})}

	if err := bridge.PushText(context.Background(), "session-local", "こんにちは", nil); err != nil {
		t.Fatalf("push text failed: %v", err)
	}
	if gotAudioPath != "viewer-tts-1.wav" {
		t.Fatalf("audio_path = %q, want viewer-tts-1.wav", gotAudioPath)
	}
	if gotAudioURL != "" {
		t.Fatalf("audio_url = %q, want empty for local viewer audio", gotAudioURL)
	}
}

func TestRenCrowTTSBridge_SplitsTextWithSharedChunkPlan(t *testing.T) {
	var gotTexts []string
	var readyTexts []string
	var readyDisplays []string
	var readyIndexes []int

	sink := &sinkStub{}
	bridge := NewRenCrowTTSBridge(RenCrowTTSBridgeConfig{
		HTTPBaseURL: "http://tts.local",
		VoiceID:     "female_01",
		Sink:        sink,
		OnChunkReady: func(_, _ string, chunkIndex int, _, text, displayText, _, _ string) {
			readyIndexes = append(readyIndexes, chunkIndex)
			readyTexts = append(readyTexts, text)
			readyDisplays = append(readyDisplays, displayText)
		},
	})
	bridge.client = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		var gotBody map[string]any
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		gotTexts = append(gotTexts, gotBody["text"].(string))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"request_id":"req-split","audio_path":"cache\\x.wav"}`)),
		}, nil
	})}

	err := bridge.PushTextWithDisplay(
		context.Background(),
		"s-split",
		"あの砂利の積み重なりって、まるで誰かの秘密のメッセージみたいに見えたよね。そこにどんな物語が隠されてるんだろう？",
		"あの砂利の積み重なりって、まるで誰かの秘密のメッセージみたいに見えたよね。そこにどんな物語が隠されてるんだろう？",
		nil,
	)
	if err != nil {
		t.Fatalf("push text failed: %v", err)
	}

	want := []string{
		"😌あの砂利の積み重なりって、まるで誰かの秘密のメッセージみたいに見えたよね。",
		"😌そこにどんな物語が隠されてるんだろう？",
	}
	wantDisplay := []string{
		"あの砂利の積み重なりって、まるで誰かの秘密のメッセージみたいに見えたよね。",
		"そこにどんな物語が隠されてるんだろう？",
	}
	if len(gotTexts) != len(want) {
		t.Fatalf("expected %d synthesis requests, got %d: %#v", len(want), len(gotTexts), gotTexts)
	}
	for i := range want {
		if gotTexts[i] != want[i] {
			t.Fatalf("request text[%d] = %q, want %q", i, gotTexts[i], want[i])
		}
		if readyTexts[i] != want[i] || readyDisplays[i] != wantDisplay[i] {
			t.Fatalf("ready chunk[%d] speech/display = %q/%q, want %q/%q", i, readyTexts[i], readyDisplays[i], want[i], wantDisplay[i])
		}
		if readyIndexes[i] != i {
			t.Fatalf("chunk index[%d] = %d, want %d", i, readyIndexes[i], i)
		}
	}
	if sink.calls != len(want) {
		t.Fatalf("expected sink submit %d times, got %d", len(want), sink.calls)
	}
}

func TestRenCrowTTSBridge_PushTextReturnsErrorCode(t *testing.T) {
	bridge := NewRenCrowTTSBridge(RenCrowTTSBridgeConfig{
		HTTPBaseURL: "http://tts.local",
	})
	bridge.client = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_request","message":"bad provider_params"}}`)),
		}, nil
	})}

	err := bridge.PushText(context.Background(), "s1", "test", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !containsErrorCode(err, "invalid_request") {
		t.Fatalf("expected invalid_request code, got %v", err)
	}
}

func TestRenCrowTTSBridge_FiltersProviderParamsByAllowList(t *testing.T) {
	called := false

	bridge := NewRenCrowTTSBridge(RenCrowTTSBridgeConfig{
		HTTPBaseURL: "http://tts.local",
		ProviderParams: map[string]any{
			"style":         "Neutral",
			"style_weight":  2.8,
			"unsupported_x": 1,
		},
	})
	bridge.client = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"request_id":"req-2","audio_path":"cache\\x.wav"}`)),
		}, nil
	})}

	err := bridge.PushText(context.Background(), "s-filter", "test", nil)
	if err == nil {
		t.Fatal("expected invalid_request error for unknown provider_params key")
	}
	if !containsErrorCode(err, "invalid_request") {
		t.Fatalf("expected invalid_request code, got %v", err)
	}
	if called {
		t.Fatal("expected request to be rejected before HTTP call")
	}
}

func TestCT_SY_003_RequestIDHeaderPropagation(t *testing.T) {
	var gotHeader string

	bridge := NewRenCrowTTSBridge(RenCrowTTSBridgeConfig{
		HTTPBaseURL: "http://tts.local",
	})
	bridge.client = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		gotHeader = r.Header.Get("X-RenCrow-TTS-Request-Id")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"request_id":"req-ct003","audio_path":"cache\\x.wav"}`)),
		}, nil
	})}

	if err := bridge.PushText(context.Background(), "ct003-session", "test", nil); err != nil {
		t.Fatalf("push text failed: %v", err)
	}
	if strings.TrimSpace(gotHeader) == "" {
		t.Fatal("expected X-RenCrow-TTS-Request-Id header")
	}
}

func TestCT_SY_004_UnknownProviderParamKeyReturnsInvalidRequest(t *testing.T) {
	called := false
	bridge := NewRenCrowTTSBridge(RenCrowTTSBridgeConfig{
		HTTPBaseURL: "http://tts.local",
		ProviderParams: map[string]any{
			"style":         "Neutral",
			"unsupported_x": 1,
		},
	})
	bridge.client = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"request_id":"req-ct004","audio_path":"cache\\x.wav"}`)),
		}, nil
	})}

	err := bridge.PushText(context.Background(), "ct004-session", "test", nil)
	if err == nil {
		t.Fatal("expected invalid_request error for unknown provider_params key")
	}
	if !containsErrorCode(err, "invalid_request") {
		t.Fatalf("expected invalid_request code, got %v", err)
	}
	if called {
		t.Fatal("expected request to be rejected before HTTP call")
	}
}

func TestRenCrowTTSBridge_RequestIDFallbackPrefix(t *testing.T) {
	var gotHeader string

	bridge := NewRenCrowTTSBridge(RenCrowTTSBridgeConfig{
		HTTPBaseURL: "http://tts.local",
	})
	bridge.client = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		gotHeader = r.Header.Get("X-RenCrow-TTS-Request-Id")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"request_id":"req-3","audio_path":"cache\\x.wav"}`)),
		}, nil
	})}

	if err := bridge.PushText(context.Background(), "***", "test", nil); err != nil {
		t.Fatalf("push text failed: %v", err)
	}
	if !strings.HasPrefix(gotHeader, "ttsreq-") {
		t.Fatalf("expected fallback prefix in request id header, got %q", gotHeader)
	}
}

func TestRenCrowTTSBridge_InvalidProviderParamValueTypeReturnsInvalidRequest(t *testing.T) {
	called := false

	bridge := NewRenCrowTTSBridge(RenCrowTTSBridgeConfig{
		HTTPBaseURL: "http://tts.local",
		ProviderParams: map[string]any{
			"style":          123,
			"line_split":     "false",
			"noise":          "high",
			"style_weight":   2.5,
			"split_interval": 1,
		},
	})
	bridge.client = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"request_id":"req-4","audio_path":"cache\\x.wav"}`)),
		}, nil
	})}

	err := bridge.PushText(context.Background(), "s-type", "test", nil)
	if err == nil {
		t.Fatal("expected invalid_request error for invalid provider_params type")
	}
	if !containsErrorCode(err, "invalid_request") {
		t.Fatalf("expected invalid_request code, got %v", err)
	}
	if called {
		t.Fatal("expected request to be rejected before HTTP call")
	}
}

func TestCT_SY_002B_TextTooLongReturnsInvalidRequest(t *testing.T) {
	called := false
	bridge := NewRenCrowTTSBridge(RenCrowTTSBridgeConfig{
		HTTPBaseURL: "http://tts.local",
	})
	bridge.client = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"request_id":"req-long","audio_path":"cache\\x.wav"}`)),
		}, nil
	})}

	longText := strings.Repeat("あ", 1001)
	if got := utf8.RuneCountInString(longText); got <= 1000 {
		t.Fatalf("setup error: expected >1000 runes, got %d", got)
	}
	err := bridge.PushText(context.Background(), "s-long", longText, nil)
	if err == nil {
		t.Fatal("expected invalid_request error for too long text")
	}
	if !containsErrorCode(err, "invalid_request") {
		t.Fatalf("expected invalid_request code, got %v", err)
	}
	if called {
		t.Fatal("expected request to be rejected before HTTP call")
	}
}

func TestCT_SY_002C_SpeedLessOrEqualZeroReturnsInvalidRequest(t *testing.T) {
	called := false
	bridge := NewRenCrowTTSBridge(RenCrowTTSBridgeConfig{
		HTTPBaseURL: "http://tts.local",
	})
	bridge.client = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"request_id":"req-speed","audio_path":"cache\\x.wav"}`)),
		}, nil
	})}

	err := bridge.PushText(context.Background(), "s-speed", "test", &moduletts.EmotionState{
		Prosody: moduletts.Prosody{Speed: -0.1},
	})
	if err == nil {
		t.Fatal("expected invalid_request error for speed <= 0")
	}
	if !containsErrorCode(err, "invalid_request") {
		t.Fatalf("expected invalid_request code, got %v", err)
	}
	if called {
		t.Fatal("expected request to be rejected before HTTP call")
	}
}

func TestCT_SY_002D_LengthLessOrEqualZeroReturnsInvalidRequest(t *testing.T) {
	called := false
	bridge := NewRenCrowTTSBridge(RenCrowTTSBridgeConfig{
		HTTPBaseURL: "http://tts.local",
		ProviderParams: map[string]any{
			"length": 0,
		},
	})
	bridge.client = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"request_id":"req-length","audio_path":"cache\\x.wav"}`)),
		}, nil
	})}

	err := bridge.PushText(context.Background(), "s-length", "test", nil)
	if err == nil {
		t.Fatal("expected invalid_request error for length <= 0")
	}
	if !containsErrorCode(err, "invalid_request") {
		t.Fatalf("expected invalid_request code, got %v", err)
	}
	if called {
		t.Fatal("expected request to be rejected before HTTP call")
	}
}

func TestRenCrowTTSBridge_LineSplitStringIsNormalizedToBool(t *testing.T) {
	var gotBody map[string]any

	bridge := NewRenCrowTTSBridge(RenCrowTTSBridgeConfig{
		HTTPBaseURL: "http://tts.local",
		ProviderParams: map[string]any{
			"line_split": "yes",
			"style":      "Neutral",
			"language":   "JP",
		},
	})
	bridge.client = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"request_id":"req-linesplit","audio_path":"cache\\x.wav"}`)),
		}, nil
	})}

	if err := bridge.PushText(context.Background(), "s-linesplit", "test", nil); err != nil {
		t.Fatalf("push text failed: %v", err)
	}
	pp, ok := gotBody["provider_params"].(map[string]any)
	if !ok {
		t.Fatalf("provider_params missing: %+v", gotBody)
	}
	if pp["line_split"] != true {
		t.Fatalf("expected line_split=true, got %+v", pp["line_split"])
	}
}

func TestRenCrowTTSBridge_RetryOnEngineUnavailable(t *testing.T) {
	attempts := 0
	bridge := NewRenCrowTTSBridge(RenCrowTTSBridgeConfig{
		HTTPBaseURL: "http://tts.local",
	})
	bridge.client = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		if attempts < 3 {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"engine_unavailable","message":"warming up"}}`)),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"request_id":"req-retry","audio_path":"cache\\x.wav"}`)),
		}, nil
	})}

	if err := bridge.PushText(context.Background(), "s-retry", "test", nil); err != nil {
		t.Fatalf("push text failed after retries: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts (1 + 2 retries), got %d", attempts)
	}
}

func TestRenCrowTTSBridge_HTTPSWithTLSSkipVerify(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"request_id":"req-https","audio_path":"cache\\x.wav"}`)
	}))
	defer srv.Close()

	bridge := NewRenCrowTTSBridge(RenCrowTTSBridgeConfig{
		HTTPBaseURL:   srv.URL,
		TLSSkipVerify: true,
	})

	if err := bridge.PushText(context.Background(), "https-session", "test", nil); err != nil {
		t.Fatalf("expected https request to succeed with tls_skip_verify, got: %v", err)
	}
}

type sinkStub struct {
	calls int
}

func containsErrorCode(err error, code string) bool {
	if err == nil {
		return false
	}
	msg := strings.ToUpper(strings.ReplaceAll(err.Error(), "-", "_"))
	want := strings.ToUpper(strings.ReplaceAll(code, "-", "_"))
	return strings.Contains(msg, "CODE="+want)
}

func (s *sinkStub) SubmitChunk(_ context.Context, _ string, _ audioChunk) error {
	s.calls++
	return nil
}

func (s *sinkStub) CompleteSession(_ context.Context, _ string) error {
	return nil
}
