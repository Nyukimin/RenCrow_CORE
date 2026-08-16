package conversation

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

// mockLLMProvider はテスト用のLLMプロバイダーモック
type mockLLMProvider struct {
	response string
	err      error
	calls    int
	requests []llm.GenerateRequest
}

func (m *mockLLMProvider) Generate(_ context.Context, request llm.GenerateRequest) (llm.GenerateResponse, error) {
	m.calls++
	m.requests = append(m.requests, request)
	return llm.GenerateResponse{Content: m.response}, m.err
}
func (m *mockLLMProvider) Name() string { return "mock" }

type unnamedLLMProvider struct{ mockLLMProvider }

func (*unnamedLLMProvider) Name() string { return "  " }

func newTestThread(msgs ...string) *domconv.Thread {
	t := domconv.NewThread("sess-test", "programming")
	for i, msg := range msgs {
		speaker := domconv.SpeakerUser
		if i%2 == 1 {
			speaker = domconv.SpeakerMio
		}
		t.AddMessage(domconv.NewMessage(speaker, msg, nil))
	}
	return t
}

// --- Summarize テスト ---

func TestLLMSummarizer_Summarize_Success(t *testing.T) {
	provider := &mockLLMProvider{response: `{"summary":" Go言語の基本を学んだ会話 ","keywords":["Go","プログラミング","言語"]}`}
	s := NewLLMSummarizer(provider)
	thread := newTestThread("Go言語について教えて", "Go言語はシンプルで高速です")

	got, err := s.Summarize(context.Background(), thread)
	if err != nil {
		t.Fatalf("Summarize failed: %v", err)
	}
	if got.Summary != "Go言語の基本を学んだ会話" {
		t.Errorf("expected LLM summary, got: %q", got.Summary)
	}
	if len(got.Keywords) != 3 || got.Keywords[0] != "Go" {
		t.Errorf("unexpected keywords: %#v", got.Keywords)
	}
	if got.Provider != "mock" {
		t.Errorf("expected provider mock, got %q", got.Provider)
	}
	if provider.calls != 1 || len(provider.requests) != 1 {
		t.Fatalf("expected one provider call, got %d", provider.calls)
	}
	if provider.requests[0].ResponseFormat != llm.ResponseFormatJSONObject {
		t.Fatalf("expected JSON response format, got %q", provider.requests[0].ResponseFormat)
	}
}

func TestLLMSummarizer_Summarize_LLMErrorIsUnavailable(t *testing.T) {
	provider := &mockLLMProvider{err: errors.New("provider detail must not escape")}
	s := NewLLMSummarizer(provider)
	thread := newTestThread("こんにちは", "やあ")

	residual, err := s.Summarize(context.Background(), thread)
	if !errors.Is(err, domconv.ErrThreadSummarizerUnavailable) {
		t.Fatalf("expected unavailable error, got %v", err)
	}
	if residual.Provider != "mock" {
		t.Fatalf("provider identity was lost on error: %#v", residual)
	}
	if strings.Contains(err.Error(), "provider detail") {
		t.Fatal("provider error detail escaped summarizer boundary")
	}
}

func TestLLMSummarizerRejectsProviderWithoutStableIdentity(t *testing.T) {
	provider := &unnamedLLMProvider{mockLLMProvider: mockLLMProvider{response: `{"summary":"summary","keywords":["one","two","three"]}`}}
	residual, err := NewLLMSummarizer(provider).Summarize(context.Background(), newTestThread("hello"))
	if !errors.Is(err, domconv.ErrThreadSummarizerNotConfigured) {
		t.Fatalf("error=%v, want not configured", err)
	}
	if residual.Provider != domconv.ThreadSummaryProviderNotConfigured {
		t.Fatalf("provider=%q, want stable not-configured identity", residual.Provider)
	}
	if provider.calls != 0 {
		t.Fatalf("unnamed provider was invoked %d times", provider.calls)
	}
}

func TestLLMSummarizer_Summarize_EmptyThread(t *testing.T) {
	provider := &mockLLMProvider{response: `{"summary":"empty","keywords":["a","b","c"]}`}
	s := NewLLMSummarizer(provider)
	thread := domconv.NewThread("sess", "general")

	_, err := s.Summarize(context.Background(), thread)
	if err == nil {
		t.Fatal("expected error on empty thread, got nil")
	}
}

func TestBuildSummarizePromptPreservesCharacterAttribution(t *testing.T) {
	thread := domconv.NewThread("shared", "general")
	thread.AddMessage(domconv.NewMessage(domconv.SpeakerUser, "合言葉は青い水路", nil))
	thread.AddMessage(domconv.NewMessage(domconv.SpeakerMio, "覚えたよ", nil))
	thread.AddMessage(domconv.NewMessage(domconv.SpeakerShiro, "確認しました", nil))
	thread.AddMessage(domconv.NewMessage(domconv.SpeakerKuro, "分析しました", nil))
	thread.AddMessage(domconv.NewMessage(domconv.SpeakerMidori, "物語にしました", nil))

	prompt := buildSummarizePrompt(thread)
	for _, marker := range []string{"[mio]", "[shiro]", "[kuro]", "[midori]", "別のAgentへ帰属させない"} {
		if !strings.Contains(prompt, marker) {
			t.Fatalf("summary prompt missing %q: %s", marker, prompt)
		}
	}
}

func TestLLMSummarizer_Summarize_StrictResponse(t *testing.T) {
	tests := []struct {
		name     string
		response string
	}{
		{name: "unknown key", response: `{"summary":"ok","keywords":["a","b","c"],"extra":true}`},
		{name: "duplicate key", response: `{"summary":"ok","summary":"again","keywords":["a","b","c"]}`},
		{name: "trailing data", response: `{"summary":"ok","keywords":["a","b","c"]}{}`},
		{name: "markdown", response: "```json\n{\"summary\":\"ok\",\"keywords\":[\"a\",\"b\",\"c\"]}\n```"},
		{name: "too few keywords", response: `{"summary":"ok","keywords":["a","b"]}`},
		{name: "casefold duplicate", response: `{"summary":"ok","keywords":["Go","go","lang"]}`},
		{name: "forbidden newline", response: "{\"summary\":\"bad\\nvalue\",\"keywords\":[\"a\",\"b\",\"c\"]}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &mockLLMProvider{response: tt.response}
			_, err := NewLLMSummarizer(provider).Summarize(context.Background(), newTestThread("test"))
			if !errors.Is(err, domconv.ErrThreadSummarizerInvalid) {
				t.Fatalf("expected invalid response, got %v", err)
			}
		})
	}
}

func TestLLMSummarizer_Summarize_NormalizesUnicodeWhitespace(t *testing.T) {
	provider := &mockLLMProvider{response: "{\"summary\":\"  一\u00a0二\\t三  \",\"keywords\":[\" 一 \",\"二\",\"三\"]}"}
	got, err := NewLLMSummarizer(provider).Summarize(context.Background(), newTestThread("test"))
	if err != nil {
		t.Fatalf("Summarize failed: %v", err)
	}
	if got.Summary != "一 二 三" || got.Keywords[0] != "一" {
		t.Fatalf("unexpected normalized residual: %#v", got)
	}
}

func TestLLMSummarizer_Summarize_RejectsOversizedResponse(t *testing.T) {
	provider := &mockLLMProvider{response: strings.Repeat("x", maxThreadSummaryResponseBytes+1)}
	_, err := NewLLMSummarizer(provider).Summarize(context.Background(), newTestThread("test"))
	if !errors.Is(err, domconv.ErrThreadSummarizerInvalid) {
		t.Fatalf("expected invalid oversized response, got %v", err)
	}
}

func TestLLMSummarizer_Summarize_RejectsInvalidUTF8(t *testing.T) {
	provider := &mockLLMProvider{response: "{\"summary\":\"" + string([]byte{0xff}) + "\",\"keywords\":[\"a\",\"b\",\"c\"]}"}
	_, err := NewLLMSummarizer(provider).Summarize(context.Background(), newTestThread("test"))
	if !errors.Is(err, domconv.ErrThreadSummarizerInvalid) {
		t.Fatalf("expected invalid UTF-8 response, got %v", err)
	}
}

func TestLLMSummarizer_CurrentContractUsesOneProviderCall(t *testing.T) {
	provider := &mockLLMProvider{response: `{"summary":"要約","keywords":["a","b","c"]}`}
	s := NewLLMSummarizer(provider)
	thread := newTestThread("test", "test")

	if _, err := s.Summarize(context.Background(), thread); err != nil {
		t.Fatalf("ExtractKeywords failed: %v", err)
	}
	if provider.calls != 1 {
		t.Fatalf("current contract made %d provider calls, want 1", provider.calls)
	}
}

func TestLLMDailyDigestSummarizer_SummarizeDailyDigest(t *testing.T) {
	provider := &mockLLMProvider{response: "LLM版Daily Digest"}
	s := NewLLMDailyDigestSummarizer(provider)

	got, err := s.SummarizeDailyDigest(context.Background(), time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC), "ai", l1sqlite.L1DailyDigestSlotMorning, []l1sqlite.L1NewsItem{{
		SourceID:     "rss:test",
		SourceURL:    "https://example.com/news",
		PublishedAt:  time.Date(2026, 5, 5, 8, 0, 0, 0, time.UTC),
		RawText:      "ニュース本文",
		SummaryDraft: "ニュース要約案",
	}})
	if err != nil {
		t.Fatalf("SummarizeDailyDigest failed: %v", err)
	}
	if got != "LLM版Daily Digest" {
		t.Fatalf("unexpected digest summary: %q", got)
	}
}
