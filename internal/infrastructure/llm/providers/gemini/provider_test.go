package gemini

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestConvertMessages(t *testing.T) {
	tests := []struct {
		name     string
		messages []llm.Message
		want     []geminiContent
	}{
		{
			name: "system role を user に変換",
			messages: []llm.Message{
				{Role: "system", Content: "You are a helpful assistant"},
				{Role: "user", Content: "Hello"},
			},
			want: []geminiContent{
				{Role: "user", Parts: []geminiPart{{Text: "You are a helpful assistant"}}},
				{Role: "user", Parts: []geminiPart{{Text: "Hello"}}},
			},
		},
		{
			name: "assistant role を model に変換",
			messages: []llm.Message{
				{Role: "user", Content: "Hello"},
				{Role: "assistant", Content: "Hi there!"},
			},
			want: []geminiContent{
				{Role: "user", Parts: []geminiPart{{Text: "Hello"}}},
				{Role: "model", Parts: []geminiPart{{Text: "Hi there!"}}},
			},
		},
		{
			name: "複数メッセージ",
			messages: []llm.Message{
				{Role: "system", Content: "System prompt"},
				{Role: "user", Content: "Question 1"},
				{Role: "assistant", Content: "Answer 1"},
				{Role: "user", Content: "Question 2"},
			},
			want: []geminiContent{
				{Role: "user", Parts: []geminiPart{{Text: "System prompt"}}},
				{Role: "user", Parts: []geminiPart{{Text: "Question 1"}}},
				{Role: "model", Parts: []geminiPart{{Text: "Answer 1"}}},
				{Role: "user", Parts: []geminiPart{{Text: "Question 2"}}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertMessages(tt.messages)

			if len(got) != len(tt.want) {
				t.Errorf("convertMessages() length = %d, want %d", len(got), len(tt.want))
				return
			}

			for i := range got {
				if got[i].Role != tt.want[i].Role {
					t.Errorf("convertMessages()[%d].Role = %s, want %s", i, got[i].Role, tt.want[i].Role)
				}
				if len(got[i].Parts) != len(tt.want[i].Parts) {
					t.Errorf("convertMessages()[%d].Parts length = %d, want %d", i, len(got[i].Parts), len(tt.want[i].Parts))
					continue
				}
				if got[i].Parts[0].Text != tt.want[i].Parts[0].Text {
					t.Errorf("convertMessages()[%d].Parts[0].Text = %s, want %s", i, got[i].Parts[0].Text, tt.want[i].Parts[0].Text)
				}
			}
		})
	}
}

func TestNewProvider(t *testing.T) {
	apiKey := "test-api-key"
	model := "gemini-2.0-flash-exp"

	provider := NewProvider(apiKey, model)

	if provider.apiKey != apiKey {
		t.Errorf("NewProvider().apiKey = %s, want %s", provider.apiKey, apiKey)
	}
	if provider.model != model {
		t.Errorf("NewProvider().model = %s, want %s", provider.model, model)
	}
	if provider.client == nil {
		t.Error("NewProvider().client is nil")
	}
}

func TestName(t *testing.T) {
	provider := NewProvider("test-key", "test-model")
	if name := provider.Name(); name != "gemini" {
		t.Errorf("Name() = %s, want 'gemini'", name)
	}
}

func TestGenerateSuccessAndChatDelegation(t *testing.T) {
	var captured *http.Request
	provider := NewProvider("test-key", "gemini-test")
	provider.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		captured = req
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("ReadAll request body failed: %v", err)
		}
		if !strings.Contains(string(body), `"role":"user"`) {
			t.Fatalf("unexpected request body: %s", body)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}`)),
			Header:     make(http.Header),
		}, nil
	})}

	resp, err := provider.Generate(context.Background(), llm.GenerateRequest{
		Messages:    []llm.Message{{Role: "system", Content: "be helpful"}},
		MaxTokens:   64,
		Temperature: 0.2,
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if resp.Content != "hello" {
		t.Fatalf("unexpected content: %q", resp.Content)
	}
	if captured == nil || captured.Method != http.MethodPost || !strings.Contains(captured.URL.String(), "gemini-test:generateContent?key=test-key") {
		t.Fatalf("unexpected request: %#v", captured)
	}
	if got := captured.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %q", got)
	}

	chatResp, err := provider.Chat(context.Background(), llm.ChatRequest{
		Messages:    []llm.ChatMessage{{Role: "user", Content: "hello"}},
		Temperature: 0.1,
	})
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if chatResp.Message.Role != "assistant" || chatResp.Message.Content != "hello" || !chatResp.Done || chatResp.FinishReason != "stop" {
		t.Fatalf("unexpected chat response: %#v", chatResp)
	}
}

func TestGenerateErrorPaths(t *testing.T) {
	tests := []struct {
		name      string
		transport http.RoundTripper
		want      string
	}{
		{
			name: "http request",
			transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return nil, io.ErrUnexpectedEOF
			}),
			want: "http request",
		},
		{
			name: "status",
			transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader("bad key"))}, nil
			}),
			want: "gemini API error: bad key (status 400)",
		},
		{
			name: "decode",
			transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{bad json"))}, nil
			}),
			want: "decode response",
		},
		{
			name: "no candidates",
			transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"candidates":[]}`))}, nil
			}),
			want: "no candidates",
		},
		{
			name: "no parts",
			transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"candidates":[{"content":{"parts":[]}}]}`))}, nil
			}),
			want: "no parts",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := NewProvider("key", "model")
			provider.client = &http.Client{Transport: tt.transport}
			_, err := provider.Generate(context.Background(), llm.GenerateRequest{Messages: []llm.Message{{Role: "user", Content: "hi"}}})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected %q error, got %v", tt.want, err)
			}
		})
	}
}

func TestConvertMessagesPartFallbacks(t *testing.T) {
	got := convertMessages([]llm.Message{
		{
			Role:    "user",
			Content: "fallback text",
			Parts: []llm.MessagePart{
				{Type: llm.MessagePartText},
				{Type: llm.MessagePartImage, MimeType: "", Data: []byte("skip")},
				{Type: llm.MessagePartAudio, MimeType: "audio/wav"},
			},
		},
		{
			Role:    "user",
			Content: "fallback only",
			Parts: []llm.MessagePart{
				{Type: llm.MessagePartImage, MimeType: "", Data: []byte("skip")},
			},
		},
	})

	if len(got[0].Parts) != 1 || got[0].Parts[0].Text != "fallback text" {
		t.Fatalf("text part should fall back to message content, got %#v", got[0].Parts)
	}
	if len(got[1].Parts) != 1 || got[1].Parts[0].Text != "fallback only" {
		t.Fatalf("empty parts should fall back to content, got %#v", got[1].Parts)
	}
}
