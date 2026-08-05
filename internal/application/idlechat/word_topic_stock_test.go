package idlechat

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
)

type queuedIdleChatCodexGenerator struct {
	responses []string
	err       error
	requests  []string
}

func (g *queuedIdleChatCodexGenerator) Generate(_ context.Context, prompt string) (string, error) {
	g.requests = append(g.requests, prompt)
	if g.err != nil {
		return "", g.err
	}
	if len(g.responses) == 0 {
		return "", errors.New("no queued CodexExe response")
	}
	response := g.responses[0]
	g.responses = g.responses[1:]
	return response, nil
}

type countingWordTopicLLM struct{ calls int }

func (p *countingWordTopicLLM) Generate(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.calls++
	return llm.GenerateResponse{}, errors.New("Worker LLM must not be called")
}

func (p *countingWordTopicLLM) Name() string { return "worker" }

func TestWordTopicStockPersistsAndRejectsCrossCategoryDuplicate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "word_topic_stock.json")
	stock := newWordTopicStock(path)
	created := time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)
	item := WordPreparedTopic{
		Category: TopicCategorySingle,
		Topic:    "生成AIを店頭端末に入れるとき誰が最後の判断を持つか",
		Seed:     TopicSeed{Category: TopicCategorySingle, Genre1: "生成AI", Genre1Kind: topicWordKindStatic},
		Axis:     "観察",
		Created:  created,
	}
	if !stock.push(item) {
		t.Fatal("valid single topic was not added")
	}
	duplicate := item
	duplicate.Category = TopicCategoryDouble
	duplicate.Seed = TopicSeed{Category: TopicCategoryDouble, Genre1: "生成AI", Genre2: "防災"}
	duplicate.Axis = "接続"
	if stock.push(duplicate) {
		t.Fatal("same topic must not be added across categories")
	}

	reloaded := newWordTopicStock(path)
	snapshot := reloaded.snapshot()
	if snapshot.Total != 1 || snapshot.Categories[0].Count != 1 {
		t.Fatalf("reloaded stock = %+v", snapshot)
	}
	got := reloaded.pop(TopicCategorySingle)
	if got == nil || got.Topic != item.Topic {
		t.Fatalf("popped item = %+v", got)
	}
	if again := newWordTopicStock(path).snapshot(); again.Total != 0 {
		t.Fatalf("consumption was not persisted: %+v", again)
	}
}

func TestWordTopicStockRejectsReversedDoubleSeedPair(t *testing.T) {
	stock := newWordTopicStock(filepath.Join(t.TempDir(), "word_topic_stock.json"))
	first := WordPreparedTopic{
		Category: TopicCategoryDouble,
		Topic:    "生成AIと防災訓練に共通する判断を引き継ぐ仕組み",
		Seed:     TopicSeed{Category: TopicCategoryDouble, Genre1: "生成AI", Genre2: "防災"},
		Axis:     "接続",
		Created:  time.Now().UTC(),
	}
	if !stock.push(first) {
		t.Fatal("first double topic was not added")
	}
	second := first
	second.Topic = "防災訓練と生成AIに共通する失敗から更新する設計"
	second.Seed.Genre1, second.Seed.Genre2 = second.Seed.Genre2, second.Seed.Genre1
	if stock.push(second) {
		t.Fatal("reversed double seed pair must not be a separate stock item")
	}
}

func TestGenerateWordTopicUsesCodexExeForCandidatesAndJudgeOnly(t *testing.T) {
	topic := "生成AIを店頭端末に入れるとき誰が最後の判断を持つか"
	generator := &queuedIdleChatCodexGenerator{responses: []string{
		`{"candidates":[{"topic":"` + topic + `","interestingness_axis":"観察","opening_hook":"店頭の選択","avoid":"抽象的なAI論"}]}`,
		`{"winner_topic":"` + topic + `","scores":[{"topic":"` + topic + `","category_fit":5,"concreteness":5,"curiosity":5,"conversation_potential":5,"axis_strength":5,"novelty":5,"safety":5,"present_day_relevance":5,"total":40,"reason":"具体的"}]}`,
	}}
	worker := &countingWordTopicLLM{}
	o := NewIdleChatOrchestrator(worker, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 8, 0.7, nil, "")
	o.SetSpeakerProviders(map[string]llm.LLMProvider{"worker": worker, "chatworker": worker, "shiro": worker})
	o.SetTopicCodexGenerator(generator)
	result, err := o.generateWordTopicWithCodex(TopicSeed{Category: TopicCategorySingle, Genre1: "生成AI"})
	if err != nil {
		t.Fatalf("CodexExe word topic generation failed: %v", err)
	}
	if result.Topic != topic || result.Provider != "CodexExe" || result.Initiator != "shiro" {
		t.Fatalf("result = %+v", result)
	}
	if len(generator.requests) != 2 {
		t.Fatalf("CodexExe requests = %d, want candidates and judge", len(generator.requests))
	}
	if worker.calls != 0 {
		t.Fatalf("Worker LLM calls = %d", worker.calls)
	}
	if !strings.Contains(generator.requests[0], "category: single") || !strings.Contains(generator.requests[1], "topic judge") {
		t.Fatalf("unexpected CodexExe prompts: %#v", generator.requests)
	}
}

func TestGenerateWordTopicDoesNotFallbackWhenCodexExeFails(t *testing.T) {
	worker := &countingWordTopicLLM{}
	o := NewIdleChatOrchestrator(worker, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 8, 0.7, nil, "")
	o.SetSpeakerProviders(map[string]llm.LLMProvider{"worker": worker, "chatworker": worker, "shiro": worker})
	o.SetTopicCodexGenerator(&queuedIdleChatCodexGenerator{err: errors.New("CodexExe unavailable")})
	if _, err := o.generateWordTopicWithCodex(TopicSeed{Category: TopicCategorySingle, Genre1: "生成AI"}); err == nil {
		t.Fatal("CodexExe failure must be returned")
	}
	if worker.calls != 0 {
		t.Fatalf("Worker LLM fallback calls = %d", worker.calls)
	}
}
