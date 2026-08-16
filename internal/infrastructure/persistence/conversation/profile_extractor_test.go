package conversation

import (
	"context"
	"strings"
	"testing"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

type profileExtractorRequestProvider struct {
	response string
	req      llm.GenerateRequest
}

func (p *profileExtractorRequestProvider) Generate(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.req = req
	return llm.GenerateResponse{Content: p.response}, nil
}

func (p *profileExtractorRequestProvider) Name() string { return "profile-extractor-test" }

func TestLLMProfileExtractorAcceptsOnlyStringPreferenceScalars(t *testing.T) {
	provider := &profileExtractorRequestProvider{
		response: `{"preferences":{"文字列":"SF"},"facts":[]}`,
	}
	extractor := NewLLMProfileExtractor(provider).WithMinimumUserMessages(1)
	thread := domconv.NewThread("profile-session", "profile-thread")
	thread.AddMessage(domconv.NewMessage(domconv.SpeakerUser, "SFと数値設定", nil))
	result, err := extractor.Extract(context.Background(), thread, domconv.UserProfile{})
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}
	if result.NewPreferences["文字列"] != "SF" || len(result.NewPreferences) != 1 {
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
		`{"preferences":{"number":3.25e+2},"facts":[]}`,
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

func TestLLMProfileExtractorRejectsNonExactJSONResponse(t *testing.T) {
	for _, response := range []string{
		`prefix {"preferences":{},"facts":[]}`,
		`{"preferences":{},"facts":[]} suffix`,
		`{"preferences":{},"facts":[],"unknown":true}`,
		`{"preferences":{},"facts":[],"facts":[]}`,
		`{"preferences":{"key":"one","key":"two"},"facts":[]}`,
	} {
		t.Run(response, func(t *testing.T) {
			provider := &profileExtractorRequestProvider{response: response}
			extractor := NewLLMProfileExtractor(provider).WithMinimumUserMessages(1)
			thread := domconv.NewThread("profile-session", "profile-thread")
			thread.AddMessage(domconv.NewMessage(domconv.SpeakerUser, "値", nil))
			if _, err := extractor.Extract(context.Background(), thread, domconv.UserProfile{}); err == nil {
				t.Fatal("Extract accepted non-exact JSON response")
			}
		})
	}
}

func TestLLMProfileExtractorRejectsOversizedOrInvalidFactOutput(t *testing.T) {
	for _, response := range []string{
		`{"preferences":{},"facts":[3]}`,
		`{"preferences":{},"facts":[""]}`,
		`{"preferences":{},"facts":["line\nbreak"]}`,
	} {
		t.Run(response, func(t *testing.T) {
			provider := &profileExtractorRequestProvider{response: response}
			extractor := NewLLMProfileExtractor(provider).WithMinimumUserMessages(1)
			thread := domconv.NewThread("profile-session", "profile-thread")
			thread.AddMessage(domconv.NewMessage(domconv.SpeakerUser, "値", nil))
			if _, err := extractor.Extract(context.Background(), thread, domconv.UserProfile{}); err == nil {
				t.Fatal("Extract accepted invalid fact output")
			}
		})
	}

	provider := &profileExtractorRequestProvider{response: strings.Repeat("x", 64*1024+1)}
	extractor := NewLLMProfileExtractor(provider).WithMinimumUserMessages(1)
	thread := domconv.NewThread("profile-session", "profile-thread")
	thread.AddMessage(domconv.NewMessage(domconv.SpeakerUser, "値", nil))
	if _, err := extractor.Extract(context.Background(), thread, domconv.UserProfile{}); err == nil {
		t.Fatal("Extract accepted oversized response")
	}
}
