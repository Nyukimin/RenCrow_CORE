package main

import (
	"context"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
)

type idleChatTTSItem struct {
	bridge    orchestrator.TTSBridge
	ev        idlechat.TimelineEvent
	lifecycle *idleChatTTSLifecycleController
	ctx       context.Context
	cancel    context.CancelFunc
	gen       uint64
}

var (
	idleChatTTSOnce     sync.Once
	idleChatTTSQueue    chan *idleChatTTSItem
	idleChatTTSGen      atomic.Uint64
	idleChatTTSActiveMu sync.Mutex
	idleChatTTSActive   = make(map[string]map[*idleChatTTSItem]struct{})
)

const idleChatTTSWorkerTimeout = 120 * time.Second

func emitIdleChatTTSAsync(bridge orchestrator.TTSBridge, ev idlechat.TimelineEvent) idlechat.TTSLifecycle {
	if bridge == nil {
		return idlechat.TTSLifecycle{}
	}
	controller := newIdleChatTTSLifecycleController()
	ctx, cancel := context.WithTimeout(context.Background(), idleChatTTSWorkerTimeout)
	item := &idleChatTTSItem{
		bridge:    bridge,
		ev:        ev,
		lifecycle: controller,
		ctx:       ctx,
		cancel:    cancel,
		gen:       idleChatTTSGen.Load(),
	}
	registerIdleChatTTSItem(item)
	ensureIdleChatTTSQueue()
	select {
	case idleChatTTSQueue <- item:
	default:
		log.Printf("[IdleChat] TTS queue full; dropping speech: from=%s session=%s", ev.From, ev.SessionID)
		cancelIdleChatTTSItem(item)
		controller.signalDone()
	}
	return controller.lifecycle()
}

func registerIdleChatTTSItem(item *idleChatTTSItem) {
	if item == nil {
		return
	}
	key := streamKey(item.ev.SessionID, item.ev.MessageID)
	idleChatTTSActiveMu.Lock()
	if idleChatTTSActive[key] == nil {
		idleChatTTSActive[key] = make(map[*idleChatTTSItem]struct{})
	}
	idleChatTTSActive[key][item] = struct{}{}
	idleChatTTSActiveMu.Unlock()
}

func unregisterIdleChatTTSItem(item *idleChatTTSItem) {
	if item == nil {
		return
	}
	key := streamKey(item.ev.SessionID, item.ev.MessageID)
	idleChatTTSActiveMu.Lock()
	items := idleChatTTSActive[key]
	delete(items, item)
	if len(items) == 0 {
		delete(idleChatTTSActive, key)
	}
	idleChatTTSActiveMu.Unlock()
}

func cancelIdleChatTTSItem(item *idleChatTTSItem) {
	if item == nil || item.cancel == nil {
		return
	}
	item.cancel()
}

func cancelIdleChatTTSTimeout(ev idlechat.TTSTimeoutEvent) {
	if ev.Kind == "session_audio_timeout" || strings.TrimSpace(ev.MessageID) == "" {
		cancelIdleChatTTSSession(ev.SessionID)
		return
	}
	key := streamKey(ev.SessionID, ev.MessageID)
	idleChatTTSActiveMu.Lock()
	items := make([]*idleChatTTSItem, 0, len(idleChatTTSActive[key]))
	for item := range idleChatTTSActive[key] {
		items = append(items, item)
	}
	idleChatTTSActiveMu.Unlock()
	for _, item := range items {
		cancelIdleChatTTSItem(item)
	}
}

func cancelIdleChatTTSSession(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	idleChatTTSActiveMu.Lock()
	items := make([]*idleChatTTSItem, 0)
	for _, keyed := range idleChatTTSActive {
		for item := range keyed {
			if strings.TrimSpace(item.ev.SessionID) == sessionID {
				items = append(items, item)
			}
		}
	}
	idleChatTTSActiveMu.Unlock()
	for _, item := range items {
		cancelIdleChatTTSItem(item)
	}
}

func cancelAllIdleChatTTS() {
	idleChatTTSActiveMu.Lock()
	items := make([]*idleChatTTSItem, 0)
	for _, keyed := range idleChatTTSActive {
		for item := range keyed {
			items = append(items, item)
		}
	}
	idleChatTTSActiveMu.Unlock()
	for _, item := range items {
		cancelIdleChatTTSItem(item)
	}
}

func ensureIdleChatTTSQueue() {
	idleChatTTSOnce.Do(func() {
		idleChatTTSQueue = make(chan *idleChatTTSItem, 512)
		go func() {
			for item := range idleChatTTSQueue {
				if item == nil {
					continue
				}
				if item.gen != idleChatTTSGen.Load() || item.ctx == nil || item.ctx.Err() != nil {
					cancelIdleChatTTSItem(item)
					item.lifecycle.signalDone()
					unregisterIdleChatTTSItem(item)
					continue
				}
				emitIdleChatTTSWithLifecycle(item.ctx, item.bridge, item.ev, item.lifecycle)
				item.cancel()
				unregisterIdleChatTTSItem(item)
			}
		}()
	})
}

func resetIdleChatTTSQueue() {
	idleChatTTSGen.Add(1)
	cancelAllIdleChatTTS()
	if idleChatTTSPrefetch != nil {
		idleChatTTSPrefetch.CancelAll()
	}
	clearAllIdleChatTTSPendingStale()
	resetTTSPublicSessionRoutesForIdleChat()
	if idleChatTTSQueue == nil {
		return
	}
	for {
		select {
		case item := <-idleChatTTSQueue:
			cancelIdleChatTTSItem(item)
			item.lifecycle.signalDone()
			unregisterIdleChatTTSItem(item)
		default:
			return
		}
	}
}
