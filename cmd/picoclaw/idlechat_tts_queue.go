package main

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/idlechat"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/orchestrator"
)

type idleChatTTSItem struct {
	bridge orchestrator.TTSBridge
	ev     idlechat.TimelineEvent
	done   chan struct{}
	gen    uint64
}

var (
	idleChatTTSOnce  sync.Once
	idleChatTTSQueue chan idleChatTTSItem
	idleChatTTSGen   atomic.Uint64
)

func emitIdleChatTTSAsync(bridge orchestrator.TTSBridge, ev idlechat.TimelineEvent) <-chan struct{} {
	if bridge == nil {
		return nil
	}
	done := make(chan struct{})
	ensureIdleChatTTSQueue()
	select {
	case idleChatTTSQueue <- idleChatTTSItem{bridge: bridge, ev: ev, done: done, gen: idleChatTTSGen.Load()}:
	default:
		log.Printf("[IdleChat] TTS queue full; dropping speech: from=%s session=%s", ev.From, ev.SessionID)
		close(done)
	}
	return done
}

func ensureIdleChatTTSQueue() {
	idleChatTTSOnce.Do(func() {
		idleChatTTSQueue = make(chan idleChatTTSItem, 512)
		go func() {
			for item := range idleChatTTSQueue {
				if item.gen != idleChatTTSGen.Load() {
					close(item.done)
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
				waitCh, ok := emitIdleChatTTS(ctx, item.bridge, item.ev)
				if !ok || waitCh == nil {
					cancel()
					close(item.done)
					continue
				}
				go func(ctx context.Context, cancel context.CancelFunc, done chan struct{}, waitCh <-chan struct{}) {
					defer close(done)
					defer cancel()
					select {
					case <-ctx.Done():
						clearIdleChatTTSPendingByChan(waitCh)
					case <-waitCh:
					}
				}(ctx, cancel, item.done, waitCh)
			}
		}()
	})
}

func resetIdleChatTTSQueue() {
	idleChatTTSGen.Add(1)
	resetTTSPublicSessionRoutesForIdleChat()
	clearAllIdleChatTTSPending()
	if idleChatTTSQueue == nil {
		return
	}
	for {
		select {
		case item := <-idleChatTTSQueue:
			close(item.done)
		default:
			return
		}
	}
}
