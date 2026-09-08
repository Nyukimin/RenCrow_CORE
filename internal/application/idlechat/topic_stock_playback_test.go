package idlechat

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestTopicStockPlaybackMovesForwardAndBackWithoutRestoringConsumedStock(t *testing.T) {
	wordStock := newWordTopicStock("")
	wordRunID := modulecore.NewRunID()
	wordTaskID := modulecore.NewTaskID()
	added, pushErr := wordStock.push(WordPreparedTopic{
		Category: TopicCategorySingle, Topic: "駅前の店で防災設備を選ぶ最後の判断者は誰か",
		Seed: TopicSeed{Category: TopicCategorySingle, Genre1: "防災"}, Axis: "観察",
		TaskID: wordTaskID, RunID: wordRunID, Created: time.Now().UTC(),
	})
	if pushErr != nil {
		t.Fatalf("word topic push failed: %v", pushErr)
	}
	if !added {
		t.Fatal("word topic push failed")
	}
	forecastStock := newForecastTopicStock("")
	domain := forecastDomains[0]
	forecastRunID := modulecore.NewRunID()
	forecastTaskID := modulecore.NewTaskID()
	if added, err := forecastStock.push(domain.Name, PreparedTopic{
		Domain: domain, Topic: "AIの普及で地域の窓口が2年後に担う相談の変化",
		Seeds: []string{"窓口", "AI"}, TaskID: forecastTaskID, RunID: forecastRunID, Created: time.Now().UTC(),
	}); err != nil || !added {
		t.Fatal("forecast topic push failed")
	}

	orchestrator := &IdleChatOrchestrator{wordTopicStock: wordStock, topicStockBuf: forecastStock}
	first, err := orchestrator.selectTopicStockPlaybackItem(TopicStockPlaybackPlay, wordPlaybackID(wordRunID))
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

func TestForecastPublicationWaitsForCheckpointCleanup(t *testing.T) {
	dir := t.TempDir()
	stock := newForecastTopicStock(filepath.Join(dir, "stock.json"))
	taskID, runID := testIdleChatRunIdentityPair()
	domain := forecastDomains[0]
	if added, err := stock.push(domain.Name, PreparedTopic{Domain: domain, Topic: "saved forecast", TaskID: taskID, RunID: runID, Created: time.Now().UTC()}); err != nil || !added {
		t.Fatalf("push: %v", err)
	}
	cp := NewGenerationCheckpointStore(filepath.Join(dir, "checkpoints.json"))
	key := "forecast:" + domain.Name
	if err := cp.Put(GenerationCheckpoint{Key: key, Kind: "forecast", TaskID: taskID, RunID: runID}); err != nil {
		t.Fatal(err)
	}
	o := &IdleChatOrchestrator{topicStockBuf: stock}
	o.SetGenerationCheckpointStore(cp)
	if got := o.availableTopicStockPlaybackItems(); len(got) != 0 {
		t.Fatal("pending forecast was listed")
	}
	if _, err := o.consumeTopicStockPlaybackItem(forecastPlaybackID(runID)); err == nil {
		t.Fatal("pending forecast was consumed")
	}
	if stock.count(domain.Name) != 1 {
		t.Fatal("pending artifact was removed")
	}
	if err := cp.Delete(key); err != nil {
		t.Fatal(err)
	}
	if _, err := o.consumeTopicStockPlaybackItem(forecastPlaybackID(runID)); err != nil {
		t.Fatal(err)
	}
}
