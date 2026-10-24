package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestForegroundStopCancelsOnlyStoppedTTSSpeechGeneration(t *testing.T) {
	previousPrefetch := idleChatTTSPrefetch
	idleChatTTSPrefetch = newIdleChatTTSPrefetchManager(&idleChatPrefetchMockBridge{})
	resetIdleChatTTSQueue()
	resetTTSPublicSessionStateForTest()
	clearAllIdleChatTTSPending()
	items := make([]*idleChatTTSItem, 0, 2)
	t.Cleanup(func() {
		if idleChatTTSPrefetch != nil {
			idleChatTTSPrefetch.CancelAll()
		}
		cancelAllIdleChatTTS()
		for _, item := range items {
			unregisterIdleChatTTSItem(item)
		}
		clearAllIdleChatTTSPending()
		resetTTSPublicSessionStateForTest()
		idleChatTTSPrefetch = previousPrefetch
	})

	runtimeEpoch := "runtime-generation-cancel-" + string(modulecore.NewTraceID())
	o := idlechat.NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 1, 2, 0.7, nil, "")
	o.SetRuntimeEpoch(runtimeEpoch)
	if err := o.StartForecastMode(); err != nil {
		t.Fatalf("StartForecastMode: %v", err)
	}
	oldGeneration := o.PlaybackControlSnapshot().PlaybackGeneration
	if oldGeneration == 0 {
		t.Fatal("started playback generation is zero")
	}

	makeItem := func(generation uint64, name string) *idleChatTTSItem {
		ctx, cancel := context.WithCancel(context.Background())
		item := &idleChatTTSItem{
			ev: idlechat.TimelineEvent{
				SessionID:          name + "-session",
				MessageID:          name + "-message",
				TraceID:            modulecore.NewTraceID(),
				RuntimeEpoch:       runtimeEpoch,
				PlaybackGeneration: generation,
			},
			ctx:    ctx,
			cancel: cancel,
			gen:    idleChatTTSGen.Load(),
		}
		registerIdleChatTTSItem(item)
		items = append(items, item)
		return item
	}

	oldItem := makeItem(oldGeneration, "old")
	oldPending := registerIdleChatTTSPendingForPlayback("old-internal-session", "old-playback-ref", runtimeEpoch, oldGeneration)
	registerTTSPublicSessionForPlayback("old-internal-route", "old-public-session", "old-playback-ref", "old-message", 1, runtimeEpoch, oldGeneration)
	oldPrefetch := idleChatTTSPrefetch.stream("old-prefetch-session", "old-prefetch-message", idlechat.TTSPrefetchEvent{
		SessionID: "old-prefetch-session", MessageID: "old-prefetch-message", TraceID: modulecore.NewTraceID(),
		RuntimeEpoch: runtimeEpoch, PlaybackGeneration: oldGeneration,
	})

	callbackStarted := make(chan struct{})
	callbackRelease := make(chan struct{})
	callbackDone := make(chan struct{})
	var releaseCallbackOnce sync.Once
	releaseCallback := func() { releaseCallbackOnce.Do(func() { close(callbackRelease) }) }
	t.Cleanup(releaseCallback)
	o.SetInterruptHandler(func(epoch string, generation uint64) {
		if epoch != runtimeEpoch || generation != oldGeneration {
			t.Errorf("cancellation identity = (%q, %d), want (%q, %d)", epoch, generation, runtimeEpoch, oldGeneration)
		}
		close(callbackStarted)
		<-callbackRelease
		cancelIdleChatTTSGeneration(epoch, generation)
		close(callbackDone)
	})
	stopCtx, cancelStop := context.WithTimeout(context.Background(), time.Second)
	defer cancelStop()
	type stopResult struct {
		receipt idlechat.PlaybackStopReceipt
		err     error
	}
	stopDone := make(chan stopResult, 1)
	go func() {
		receipt, err := o.StopPlaybackForForeground(stopCtx, idlechat.PlaybackStopRequest{
			RuntimeEpoch: runtimeEpoch, StateRevision: 1, Reason: "foreground_chat",
		})
		stopDone <- stopResult{receipt: receipt, err: err}
	}()
	var result stopResult
	select {
	case result = <-stopDone:
	case <-time.After(250 * time.Millisecond):
		releaseCallback()
		<-stopDone
		t.Fatal("StopPlaybackForForeground waited for the TTS cancellation callback")
	}
	if result.err != nil {
		t.Fatalf("StopPlaybackForForeground: %v", result.err)
	}
	receipt := result.receipt
	if receipt.Generation <= oldGeneration {
		t.Fatalf("stopped generation = %d, want newer than %d", receipt.Generation, oldGeneration)
	}
	select {
	case <-callbackStarted:
	case <-time.After(time.Second):
		t.Fatal("asynchronous TTS cancellation callback did not start")
	}

	newItem := makeItem(receipt.Generation, "new")
	newPending := registerIdleChatTTSPendingForPlayback("new-internal-session", "new-playback-ref", runtimeEpoch, receipt.Generation)
	registerTTSPublicSessionForPlayback("new-internal-route", "new-public-session", "new-playback-ref", "new-message", 1, runtimeEpoch, receipt.Generation)
	newPrefetch := idleChatTTSPrefetch.stream("new-prefetch-session", "new-prefetch-message", idlechat.TTSPrefetchEvent{
		SessionID: "new-prefetch-session", MessageID: "new-prefetch-message", TraceID: modulecore.NewTraceID(),
		RuntimeEpoch: runtimeEpoch, PlaybackGeneration: receipt.Generation,
	})

	releaseCallback()
	select {
	case <-callbackDone:
	case <-time.After(time.Second):
		t.Fatal("delayed TTS cancellation callback did not complete")
	}
	if oldItem.ctx.Err() == nil {
		t.Fatal("stopped generation queue item was not cancelled")
	}
	if newItem.ctx.Err() != nil {
		t.Fatalf("new generation queue item was cancelled: %v", newItem.ctx.Err())
	}
	select {
	case <-oldPending:
	default:
		t.Fatal("stopped generation playback wait was not released")
	}
	select {
	case <-newPending:
		t.Fatal("new generation playback wait was released")
	default:
	}
	if !isStaleTTSPublicSession("old-internal-route") {
		t.Fatal("stopped generation route was not retired")
	}
	if isStaleTTSPublicSession("new-internal-route") {
		t.Fatal("new generation route was retired")
	}
	if oldPrefetch.ctx.Err() == nil {
		t.Fatal("stopped generation prefetch stream was not cancelled")
	}
	if newPrefetch.ctx.Err() != nil {
		t.Fatalf("new generation prefetch stream was cancelled: %v", newPrefetch.ctx.Err())
	}

	lateCtx, lateCancel := context.WithCancel(context.Background())
	lateItem := &idleChatTTSItem{
		ev: idlechat.TimelineEvent{
			SessionID: "late-old-session", MessageID: "late-old-message", TraceID: modulecore.NewTraceID(),
			RuntimeEpoch: runtimeEpoch, PlaybackGeneration: oldGeneration,
		},
		lifecycle: newIdleChatTTSLifecycleController(), ctx: lateCtx, cancel: lateCancel,
	}
	if registerIdleChatTTSItem(lateItem) {
		t.Fatal("late old generation queue item was registered")
	}
	if lateItem.ctx.Err() == nil {
		t.Fatal("late old generation queue item was not cancelled")
	}
	latePending := registerIdleChatTTSPendingForPlayback("late-old-pending", "late-old-playback-ref", runtimeEpoch, oldGeneration)
	select {
	case <-latePending:
	default:
		t.Fatal("late old generation playback wait was not rejected")
	}
	registerTTSPublicSessionForPlayback("late-old-route", "late-old-public", "late-old-ref", "late-old-message", 1, runtimeEpoch, oldGeneration)
	if !isStaleTTSPublicSession("late-old-route") {
		t.Fatal("late old generation public route was registered")
	}
	if stream := idleChatTTSPrefetch.stream("late-old-prefetch", "late-old-message", idlechat.TTSPrefetchEvent{
		SessionID: "late-old-prefetch", MessageID: "late-old-message", TraceID: modulecore.NewTraceID(),
		RuntimeEpoch: runtimeEpoch, PlaybackGeneration: oldGeneration,
	}); stream != nil {
		t.Fatal("late old generation prefetch stream was registered")
	}
}
