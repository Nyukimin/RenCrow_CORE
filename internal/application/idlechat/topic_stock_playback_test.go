package idlechat

import (
	"errors"
	"testing"
	"time"
)

func TestTopicStockPlaybackMovesForwardAndBackWithoutRestoringConsumedStock(t *testing.T) {
	wordStock := newWordTopicStock("")
	if !wordStock.push(WordPreparedTopic{
		Category: TopicCategorySingle, Topic: "駅前の店で防災設備を選ぶ最後の判断者は誰か",
		Seed: TopicSeed{Category: TopicCategorySingle, Genre1: "防災"}, Axis: "観察",
		GenerationID: "word-1", Created: time.Now().UTC(),
	}) {
		t.Fatal("word topic push failed")
	}
	forecastStock := newForecastTopicStock("")
	domain := forecastDomains[0]
	if !forecastStock.push(domain.Name, PreparedTopic{
		Domain: domain, Topic: "AIの普及で地域の窓口が2年後に担う相談の変化",
		Seeds: []string{"窓口", "AI"}, GenerationID: "forecast-1", Created: time.Now().UTC(),
	}) {
		t.Fatal("forecast topic push failed")
	}

	orchestrator := &IdleChatOrchestrator{wordTopicStock: wordStock, topicStockBuf: forecastStock}
	first, err := orchestrator.selectTopicStockPlaybackItem(TopicStockPlaybackPlay, wordPlaybackID("word-1"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Stock != "single" || wordStock.count(TopicCategorySingle) != 0 {
		t.Fatalf("first=%+v word_count=%d", first, wordStock.count(TopicCategorySingle))
	}

	second, err := orchestrator.selectTopicStockPlaybackItem(TopicStockPlaybackNext, "")
	if err != nil {
		t.Fatal(err)
	}
	if second.Stock != "forecast" || forecastStock.count(domain.Name) != 0 {
		t.Fatalf("second=%+v forecast_count=%d", second, forecastStock.count(domain.Name))
	}

	previous, err := orchestrator.selectTopicStockPlaybackItem(TopicStockPlaybackPrevious, "")
	if err != nil {
		t.Fatal(err)
	}
	if previous.ID != first.ID {
		t.Fatalf("previous=%q want=%q", previous.ID, first.ID)
	}
	if wordStock.count(TopicCategorySingle) != 0 || forecastStock.count(domain.Name) != 0 {
		t.Fatal("previous must replay history without returning items to stock")
	}

	snapshot := orchestrator.TopicStockPlaybackSnapshot()
	if snapshot.Current == nil || snapshot.Current.ID != first.ID || snapshot.Position != 1 || snapshot.HistorySize != 2 || snapshot.CanPrevious || !snapshot.CanNext {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestTopicStockPlaybackRejectsPreviousBeforeHistory(t *testing.T) {
	orchestrator := &IdleChatOrchestrator{}
	_, err := orchestrator.selectTopicStockPlaybackItem(TopicStockPlaybackPrevious, "")
	if !errors.Is(err, ErrTopicStockNoPrevious) {
		t.Fatalf("err=%v", err)
	}
}
