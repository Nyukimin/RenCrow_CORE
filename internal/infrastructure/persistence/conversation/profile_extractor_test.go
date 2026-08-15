package conversation

import (
	"context"
	"strings"
	"testing"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"clean json", `{"preferences": {}, "facts": []}`, `{"preferences": {}, "facts": []}`},
		{"with prefix", `Here is the result: {"preferences": {"好み": "SF"}, "facts": ["猫が好き"]}`, `{"preferences": {"好み": "SF"}, "facts": ["猫が好き"]}`},
		{"with suffix", `{"preferences": {}, "facts": []} end`, `{"preferences": {}, "facts": []}`},
		{"no json", "no json here", "{}"},
		{"empty", "", "{}"},
		{"nested", `{"a": {"b": "c"}}`, `{"a": {"b": "c"}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractJSON(tt.input)
			if got != tt.want {
				t.Errorf("extractJSON(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

type profileExtractorRequestProvider struct {
	response string
	req      llm.GenerateRequest
}

func (p *profileExtractorRequestProvider) Generate(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.req = req
	return llm.GenerateResponse{Content: p.response}, nil
}

func (p *profileExtractorRequestProvider) Name() string { return "profile-extractor-test" }

func TestLLMProfileExtractorCanonicalizesStringAndNumberPreferenceScalars(t *testing.T) {
	provider := &profileExtractorRequestProvider{
		response: `{"preferences":{"文字列":"SF","数値":3.25e+2},"facts":[]}`,
	}
	extractor := NewLLMProfileExtractor(provider).WithMinimumUserMessages(1)
	thread := domconv.NewThread("profile-session", "profile-thread")
	thread.AddMessage(domconv.NewMessage(domconv.SpeakerUser, "SFと数値設定", nil))
	result, err := extractor.Extract(context.Background(), thread, domconv.UserProfile{})
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}
	if result.NewPreferences["文字列"] != "SF" || result.NewPreferences["数値"] != "3.25e+2" {
		t.Fatalf("preferences=%v", result.NewPreferences)
	}
	if provider.req.ResponseFormat != llm.ResponseFormatJSONObject {
		t.Fatalf("ResponseFormat=%q want=%q", provider.req.ResponseFormat, llm.ResponseFormatJSONObject)
	}
	if !strings.Contains(provider.req.Messages[0].Content, "JSON の文字列") {
		t.Fatalf("prompt does not require string preference values: %s", provider.req.Messages[0].Content)
	}
}

func TestLLMProfileExtractorRejectsNonScalarPreferenceValues(t *testing.T) {
	for _, response := range []string{
		`{"preferences":{"object":{"nested":true}},"facts":[]}`,
		`{"preferences":{"array":["x"]},"facts":[]}`,
		`{"preferences":{"boolean":true},"facts":[]}`,
		`{"preferences":{"null":null},"facts":[]}`,
	} {
		t.Run(response, func(t *testing.T) {
			provider := &profileExtractorRequestProvider{response: response}
			extractor := NewLLMProfileExtractor(provider).WithMinimumUserMessages(1)
			thread := domconv.NewThread("profile-session", "profile-thread")
			thread.AddMessage(domconv.NewMessage(domconv.SpeakerUser, "値", nil))
			if _, err := extractor.Extract(context.Background(), thread, domconv.UserProfile{}); err == nil {
				t.Fatal("Extract succeeded for non-scalar preference value")
			}
		})
	}
}
