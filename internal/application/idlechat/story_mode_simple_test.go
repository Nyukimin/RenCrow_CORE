package idlechat

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
)

type blockingStoryProvider struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func TestValidateSimpleStoryDraftRejectsWeakStory(t *testing.T) {
	result := validateSimpleStoryDraft("桃太郎", "AIロボット", "もし桃太郎がAIロボットだったら面白いです。", "仮説だけの短文です。")
	if result.Valid {
		t.Fatal("expected invalid story")
	}
	if result.Reason == "" {
		t.Fatal("expected validation reason")
	}
}

func TestValidateSimpleStoryDraftAcceptsChangedProtagonistStory(t *testing.T) {
	body := strings.Repeat("AIロボットは村の回覧板を解析し、犬と猿と雉に役割を配った。鬼ヶ島では交渉ログを突きつけ、盗まれた米俵を取り戻した。", 3)
	result := validateSimpleStoryDraft("桃太郎", "AIロボット", "桃と回覧板のロボ太郎", body)
	if !result.Valid {
		t.Fatalf("expected valid story, got %s", result.Reason)
	}
}

func TestSimpleStoryTopicKeepsBaseAndTransform(t *testing.T) {
	result := buildSimpleStoryTopicResult("桃太郎", "AIロボット")
	if result.Category != TopicCategoryStory {
		t.Fatalf("category = %q, want story", result.Category)
	}
	if result.Strategy != "story-simple" {
		t.Fatalf("strategy = %q, want story-simple", result.Strategy)
	}
	if !strings.Contains(result.Topic, "桃太郎") || !strings.Contains(result.Topic, "AIロボット") || !strings.Contains(result.Topic, "語り直") {
		t.Fatalf("story topic lost base or transform axis: %q", result.Topic)
	}
	if err := modulechat.ValidateTopicCandidate(TopicCategoryStory, result.Seed, result.Candidates[0]); err != nil {
		t.Fatalf("story topic candidate should satisfy contract: %v", err)
	}
}

func TestRunSimpleStorySessionDoesNotDropGeneratedBodyWithLegacyValidationText(t *testing.T) {
	provider := &queuedQualityProvider{responses: []string{
		"【もしもの桃太郎】\nもし桃太郎がAIロボットだったら面白いです。",
		"QUALITY: pass\nISSUES:\n- なし\nPROMPT_FIX: ",
	}}
	o := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")
	closed := make(chan struct{})
	close(closed)
	o.SetEventEmitter(func(TimelineEvent) <-chan struct{} {
		return closed
	})

	o.RunSimpleStorySession()

	history := o.GetHistory(1)
	if len(history) != 1 {
		t.Fatalf("history count=%d, want 1", len(history))
	}
	if history[0].LoopRestarted || history[0].LoopReason != "" {
		t.Fatalf("legacy validation should not reject generated story body: %#v", history[0])
	}
	if history[0].StoryText == "" {
		t.Fatal("generated story body should be stored")
	}
}

func (p *blockingStoryProvider) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	started := false
	p.once.Do(func() {
		close(p.started)
		started = true
	})
	if !started {
		return llm.GenerateResponse{Content: "QUALITY: pass\nISSUES:\n- 大きな損耗は検出されませんでした。\nPROMPT_FIX: ", FinishReason: "stop"}, nil
	}
	select {
	case <-p.release:
	case <-ctx.Done():
		return llm.GenerateResponse{}, ctx.Err()
	}
	return llm.GenerateResponse{Content: "【テスト物語】\n最初の段落です。次の段落です。", FinishReason: "stop"}, nil
}

func (p *blockingStoryProvider) Name() string {
	return "blocking-story"
}

func TestRunSimpleStorySessionEmitsIntroBeforeGenerationCompletes(t *testing.T) {
	provider := &blockingStoryProvider{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	o := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")

	events := make(chan TimelineEvent, 32)
	o.SetEventEmitter(func(ev TimelineEvent) <-chan struct{} {
		events <- ev
		return nil
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		o.RunSimpleStorySession()
	}()

	select {
	case ev := <-events:
		if ev.Type != "idlechat.viewer" {
			t.Fatalf("first event type = %q, want idlechat.viewer", ev.Type)
		}
		if ev.Content == "" {
			t.Fatal("intro event content is empty")
		}
	case <-time.After(time.Second):
		t.Fatal("no viewer intro emitted before generation completed")
	}

	if got := o.CurrentTopic(); got == "" {
		t.Fatal("current topic is empty while story generation is active")
	}

	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("story generation did not start after intro")
	}

	close(provider.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("story session did not finish")
	}
}

func TestRunSimpleStorySessionEmitsUniqueStoryTTSMessageIDs(t *testing.T) {
	body := strings.Repeat(strings.Join(protagonistOptions, "と")+"が村の困りごとを調べ、仲間の反応を変えながら事件を解決した。", 3)
	provider := &queuedQualityProvider{responses: []string{
		"【主人公たちの改変昔話】\n" + body,
		"QUALITY: pass\nISSUES:\n- なし\nPROMPT_FIX: ",
	}}
	o := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")

	seen := map[string]bool{}
	var ttsIDs []string
	closed := make(chan struct{})
	close(closed)
	o.SetEventEmitter(func(ev TimelineEvent) <-chan struct{} {
		if ev.Type != "idlechat.tts" {
			return nil
		}
		if ev.MessageID == "" {
			t.Fatal("story TTS message id is empty")
		}
		if seen[ev.MessageID] {
			t.Fatalf("duplicate story TTS message id: %s", ev.MessageID)
		}
		seen[ev.MessageID] = true
		ttsIDs = append(ttsIDs, ev.MessageID)
		return closed
	})

	o.RunSimpleStorySession()

	if len(ttsIDs) < 4 {
		t.Fatalf("story TTS id count=%d, want at least 4: %#v", len(ttsIDs), ttsIDs)
	}
	for i, id := range ttsIDs {
		want := fmt.Sprintf(":story:%04d", i+1)
		if !strings.Contains(id, want) {
			t.Fatalf("story TTS id[%d]=%q, want sequential suffix containing %q", i, id, want)
		}
	}
}

func TestStartModesExposeNonEmptyCurrentTopicImmediately(t *testing.T) {
	tests := []struct {
		name  string
		start func(*IdleChatOrchestrator) error
	}{
		{
			name:  "forecast",
			start: func(o *IdleChatOrchestrator) error { return o.StartForecastMode() },
		},
		{
			name:  "story",
			start: func(o *IdleChatOrchestrator) error { return o.StartStoryMode() },
		},
		{
			name:  "story-simple",
			start: func(o *IdleChatOrchestrator) error { return o.StartSimpleStoryMode() },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")
			if err := tt.start(o); err != nil {
				t.Fatalf("start failed: %v", err)
			}
			if got := o.CurrentTopic(); got == "" {
				t.Fatal("current topic is empty immediately after start")
			}
		})
	}
}
