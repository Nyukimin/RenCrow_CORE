package orchestrator

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/speech"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

type blockingSpeechPlanBridge struct {
	started atomic.Int32
	entered chan struct{}
}

func (*blockingSpeechPlanBridge) StartSession(context.Context, TTSSessionStart) error { return nil }
func (*blockingSpeechPlanBridge) PushText(context.Context, string, string, *moduletts.EmotionState) error {
	return nil
}
func (*blockingSpeechPlanBridge) EndSession(context.Context, string) error { return nil }
func (b *blockingSpeechPlanBridge) PushSpeechPlan(ctx context.Context, _, _, _, _ string, _ speech.SpeechPlan) error {
	if b.started.Add(1) == 1 {
		close(b.entered)
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestMessageTTSLifecycleSpeechContextCancellationStopsOnlySynthesis(t *testing.T) {
	bridge := &blockingSpeechPlanBridge{entered: make(chan struct{})}
	lifecycle := newMessageTTSLifecycle(bridge, nil, nil)
	latch := NewSpeechTurnCancellation()
	processCtx := WithSpeechTurnCancellation(context.Background(), latch)
	done := make(chan error, 1)
	go func() {
		done <- lifecycle.PushSpeechPlan(processCtx, "tts-current", string(modulecore.NewSessionID()), string(modulecore.NewTurnID()), "mio", speech.SpeechPlan{})
	}()
	<-bridge.entered
	latch.Cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("SpeechPlan result = %v, want context.Canceled", err)
	}
	if processCtx.Err() != nil {
		t.Fatalf("speech cancellation canceled the ProcessMessage context: %v", processCtx.Err())
	}
	if got := bridge.started.Load(); got != 1 {
		t.Fatalf("PushSpeechPlan call count = %d, want 1 in-flight request", got)
	}
}

func TestMessageTTSLifecycleForwardsPreCanceledTurnSoActionOwnerCanCloseAttempt(t *testing.T) {
	bridge := &blockingSpeechPlanBridge{entered: make(chan struct{})}
	lifecycle := newMessageTTSLifecycle(bridge, nil, nil)
	latch := NewSpeechTurnCancellation()
	latch.Cancel()
	ctx := WithSpeechTurnCancellation(context.Background(), latch)
	err := lifecycle.PushSpeechPlan(ctx, "tts-late", string(modulecore.NewSessionID()), string(modulecore.NewTurnID()), "mio", speech.SpeechPlan{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("late SpeechPlan result = %v, want context.Canceled", err)
	}
	if got := bridge.started.Load(); got != 1 {
		t.Fatalf("pre-canceled turn reached synthesis bridge %d times, want one canceled handoff", got)
	}
}
