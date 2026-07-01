package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleLive2DCharacter(t *testing.T) {
	// Note: This test requires Live2D HTML files to exist at:
	// internal/adapter/viewer/assets/images/mio/Mio_透過版.html
	// internal/adapter/viewer/assets/images/shiro/Shiro_透過版.html
	// Skip if files don't exist in test environment
	t.Skip("Skipping Live2D character test - requires large HTML files")
}

func TestHandleLive2DCharacterEmbed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/viewer/live2d/embed?character_id=mio&emotion=happy&mode=live", nil)
	w := httptest.NewRecorder()

	HandleLive2DCharacterEmbed(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("HandleLive2DCharacterEmbed() status = %d, want %d", w.Code, http.StatusOK)
	}

	body := w.Body.String()
	if !strings.Contains(body, "<!DOCTYPE html>") {
		t.Error("HandleLive2DCharacterEmbed() should return HTML")
	}
	if !strings.Contains(body, "happy") {
		t.Error("HandleLive2DCharacterEmbed() should include emotion")
	}
}

func TestHandleLive2DCharacterEmbedNormalFitsMioToFrame(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/viewer/live2d/character?character_id=mio&emotion=normal&mode=normal&hide_ui=true", nil)
	w := httptest.NewRecorder()

	HandleLive2DCharacter(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("HandleLive2DCharacter() status = %d, want %d", w.Code, http.StatusOK)
	}
	body := w.Body.String()
	for _, want := range []string{
		"--mio-fit-scale: 1.62",
		"transform-origin: center bottom !important",
		"object-position: center bottom !important",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("embed body missing %q", want)
		}
	}
}

func TestHandleLive2DChatAPI(t *testing.T) {
	reqBody := `{"message":"こんにちは","character_id":"mio","mode":"normal"}`
	req := httptest.NewRequest(http.MethodPost, "/viewer/api/chat", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	HandleLive2DChatAPI(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("HandleLive2DChatAPI() status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp ChatResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("HandleLive2DChatAPI() failed to decode response: %v", err)
	}

	if resp.CharacterID != "mio" {
		t.Errorf("HandleLive2DChatAPI() character_id = %q, want %q", resp.CharacterID, "mio")
	}

	if resp.Message == "" {
		t.Error("HandleLive2DChatAPI() message should not be empty")
	}

	if resp.Emotion == "" {
		t.Error("HandleLive2DChatAPI() emotion should not be empty")
	}

	if resp.Live2DURL == "" {
		t.Error("HandleLive2DChatAPI() live2d_url should not be empty")
	}
}

type stubLive2DResponder struct {
	message string
}

func (s stubLive2DResponder) RespondLive2DChat(_ context.Context, _ string, _ string, message string) (string, error) {
	return s.message + ":" + message, nil
}

func TestHandleLive2DChatAPIUsesResponder(t *testing.T) {
	reqBody := `{"message":"こんにちは","character_id":"mio","mode":"normal","session_id":"s1"}`
	req := httptest.NewRequest(http.MethodPost, "/viewer/api/chat", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	HandleLive2DChatAPIWithResponder(stubLive2DResponder{message: "LLM"})(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp ChatResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Message != "LLM:こんにちは" {
		t.Fatalf("message=%q", resp.Message)
	}
}

func TestDetectEmotion(t *testing.T) {
	tests := []struct {
		name string
		text string
		want EmotionType
	}{
		{
			name: "happy - Japanese",
			text: "とても嬉しいです",
			want: EmotionHappy,
		},
		{
			name: "happy - English",
			text: "I'm so happy!",
			want: EmotionHappy,
		},
		{
			name: "happy - emoji",
			text: "Great work! 😊",
			want: EmotionHappy,
		},
		{
			name: "sad - Japanese",
			text: "悲しいです",
			want: EmotionSad,
		},
		{
			name: "sad - apology",
			text: "申し訳ございません",
			want: EmotionSad,
		},
		{
			name: "angry - Japanese",
			text: "怒っています",
			want: EmotionAngry,
		},
		{
			name: "surprise - Japanese",
			text: "驚きました！",
			want: EmotionSurprise,
		},
		{
			name: "think - Japanese",
			text: "考えてみます",
			want: EmotionThink,
		},
		{
			name: "think - question mark",
			text: "それは何ですか？",
			want: EmotionThink,
		},
		{
			name: "normal - no keywords",
			text: "Hello there",
			want: EmotionNormal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectEmotion(tt.text)
			if got != tt.want {
				t.Errorf("DetectEmotion(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestBuildChatResponse(t *testing.T) {
	tests := []struct {
		name        string
		message     string
		characterID string
		mode        string
		wantEmotion EmotionType
	}{
		{
			name:        "happy message - normal mode",
			message:     "ありがとうございます！",
			characterID: "mio",
			mode:        "normal",
			wantEmotion: EmotionHappy,
		},
		{
			name:        "sad message - live mode",
			message:     "申し訳ございません",
			characterID: "shiro",
			mode:        "live",
			wantEmotion: EmotionSad,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := BuildChatResponse(tt.message, tt.characterID, tt.mode)

			if resp.Message != tt.message {
				t.Errorf("BuildChatResponse() message = %q, want %q", resp.Message, tt.message)
			}

			if resp.CharacterID != tt.characterID {
				t.Errorf("BuildChatResponse() character_id = %q, want %q", resp.CharacterID, tt.characterID)
			}

			if resp.Emotion != tt.wantEmotion {
				t.Errorf("BuildChatResponse() emotion = %v, want %v", resp.Emotion, tt.wantEmotion)
			}

			if resp.Live2DURL == "" {
				t.Error("BuildChatResponse() live2d_url should not be empty")
			}

			if resp.Live2DEmbedURL == "" {
				t.Error("BuildChatResponse() live2d_embed_url should not be empty")
			}

			if tt.mode == "live" && !strings.Contains(resp.Live2DURL, "mode=live") {
				t.Error("BuildChatResponse() live2d_url should include mode=live for live mode")
			}
		})
	}
}
