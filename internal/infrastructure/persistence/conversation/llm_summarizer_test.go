package conversation

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	domconv "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
)

// mockLLMProvider はテスト用のLLMプロバイダーモック
type mockLLMProvider struct {
	response string
	err      error
}

func (m *mockLLMProvider) Generate(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
	return llm.GenerateResponse{Content: m.response}, m.err
}
func (m *mockLLMProvider) Name() string { return "mock" }

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
	provider := &mockLLMProvider{response: "Go言語の基本を学んだ会話"}
	s := NewLLMSummarizer(provider)
	thread := newTestThread("Go言語について教えて", "Go言語はシンプルで高速です")

	got, err := s.Summarize(context.Background(), thread)
	if err != nil {
		t.Fatalf("Summarize failed: %v", err)
	}
	if got != "Go言語の基本を学んだ会話" {
		t.Errorf("expected LLM summary, got: %q", got)
	}
}

func TestLLMSummarizer_Summarize_LLMError_FallsBack(t *testing.T) {
	provider := &mockLLMProvider{err: fmt.Errorf("API error")}
	s := NewLLMSummarizer(provider)
	thread := newTestThread("こんにちは", "やあ")

	// LLMエラー時はエラーを返す（呼び出し元でフォールバック）
	_, err := s.Summarize(context.Background(), thread)
	if err == nil {
		t.Fatal("expected error on LLM failure, got nil")
	}
}

func TestLLMSummarizer_Summarize_EmptyThread(t *testing.T) {
	provider := &mockLLMProvider{response: "empty"}
	s := NewLLMSummarizer(provider)
	thread := domconv.NewThread("sess", "general")

	_, err := s.Summarize(context.Background(), thread)
	if err == nil {
		t.Fatal("expected error on empty thread, got nil")
	}
}

// --- ExtractKeywords テスト ---

func TestLLMSummarizer_ExtractKeywords_Success(t *testing.T) {
	// LLMが改行区切りのキーワードを返す想定
	provider := &mockLLMProvider{response: "Go\nプログラミング\n並行処理"}
	s := NewLLMSummarizer(provider)
	thread := newTestThread("Goの並行処理を教えて", "goroutineを使います")

	keywords, err := s.ExtractKeywords(context.Background(), thread)
	if err != nil {
		t.Fatalf("ExtractKeywords failed: %v", err)
	}
	if len(keywords) != 3 {
		t.Fatalf("expected 3 keywords, got %d: %v", len(keywords), keywords)
	}
	if keywords[0] != "Go" {
		t.Errorf("expected first keyword 'Go', got %q", keywords[0])
	}
}

func TestLLMSummarizer_ExtractKeywords_CommaOrNewline(t *testing.T) {
	// カンマ区切りも対応
	provider := &mockLLMProvider{response: "Go, 並行処理, goroutine"}
	s := NewLLMSummarizer(provider)
	thread := newTestThread("test", "test")

	keywords, err := s.ExtractKeywords(context.Background(), thread)
	if err != nil {
		t.Fatalf("ExtractKeywords failed: %v", err)
	}
	if len(keywords) == 0 {
		t.Fatal("expected keywords, got none")
	}
	for _, kw := range keywords {
		if strings.TrimSpace(kw) == "" {
			t.Error("empty keyword found")
		}
	}
}

func TestLLMSummarizer_ExtractKeywords_LLMError(t *testing.T) {
	provider := &mockLLMProvider{err: fmt.Errorf("API error")}
	s := NewLLMSummarizer(provider)
	thread := newTestThread("test", "test")

	_, err := s.ExtractKeywords(context.Background(), thread)
	if err == nil {
		t.Fatal("expected error on LLM failure, got nil")
	}
}

func TestLLMDailyDigestSummarizer_SummarizeDailyDigest(t *testing.T) {
	provider := &mockLLMProvider{response: "LLM版Daily Digest"}
	s := NewLLMDailyDigestSummarizer(provider)

	got, err := s.SummarizeDailyDigest(context.Background(), time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC), "ai", L1DailyDigestSlotMorning, []L1NewsItem{{
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
