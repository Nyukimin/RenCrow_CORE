package idlechat

import (
	"strings"
	"testing"
	"time"
)

func TestForecastGenerationRejectsTopicAlreadyInWordStock(t *testing.T) {
	topic := "生活の記録をAIがどう変えるか"
	wordStock := newWordTopicStock("")
	if !wordStock.push(WordPreparedTopic{
		Category: TopicCategorySingle, Topic: topic,
		Seed: TopicSeed{Category: TopicCategorySingle, Genre1: "生活"}, Axis: "観察",
	}) {
		t.Fatal("failed to prepare word stock")
	}
	generator := &queuedIdleChatCodexGenerator{responses: []string{
		topicCandidatesJSON(topic, "変化の分岐"),
		topicCandidatesJSON(topic, "変化の分岐"),
		topicCandidatesJSON(topic, "変化の分岐"),
	}}
	o := NewIdleChatOrchestrator(nil, nil, []string{"mio", "shiro"}, 5, 2, 0.7, nil, "")
	o.wordTopicStock = wordStock
	o.SetTopicCodexGenerator(generator)
	if got, failure := o.generateForecastTopic(ForecastDomain{Name: "AI技術"}, []string{"生活記録AI"}); failure == nil || got != "" {
		t.Fatalf("duplicate Forecast topic must fail: topic=%q failure=%+v", got, failure)
	}
	if len(generator.requests) == 0 || !strings.Contains(generator.requests[0], `"forecast_horizon": "1〜2年"`) || !strings.Contains(generator.requests[0], "seed.forecast_horizon") {
		t.Fatalf("Forecast horizon contract missing from CodexExe prompt: %#v", generator.requests)
	}
}

func TestWordGenerationRejectsTopicAlreadyInForecastStock(t *testing.T) {
	topic := "生活の記録をAIがどう変えるか"
	forecastStock := newForecastTopicStock("")
	if !forecastStock.push("AI技術", PreparedTopic{Domain: ForecastDomain{Name: "AI技術"}, Topic: topic, Created: time.Now().UTC()}) {
		t.Fatal("failed to prepare Forecast stock")
	}
	generator := &queuedIdleChatCodexGenerator{responses: []string{
		topicCandidatesJSON(topic, "観察"),
		topicCandidatesJSON(topic, "観察"),
		topicCandidatesJSON(topic, "観察"),
	}}
	o := NewIdleChatOrchestrator(nil, nil, []string{"mio", "shiro"}, 5, 2, 0.7, nil, "")
	o.topicStockBuf = forecastStock
	o.SetTopicCodexGenerator(generator)
	if got, err := o.generateWordTopicWithCodex(TopicSeed{Category: TopicCategorySingle, Genre1: "生活"}); err == nil || got != nil {
		t.Fatalf("duplicate word topic must fail: result=%+v error=%v", got, err)
	}
}
