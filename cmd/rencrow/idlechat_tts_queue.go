package main

import (
	"context"
	"log"
	"strings"
	"sync"
	"sync/atomic"

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
	idleChatTTSOnce                       sync.Once
	idleChatTTSQueue                      chan *idleChatTTSItem
	idleChatTTSGen                        atomic.Uint64
	idleChatTTSActiveMu                   sync.Mutex
	idleChatTTSActive                     = make(map[string]map[*idleChatTTSItem]struct{})
	idleChatTTSCancelledGenerationByEpoch = make(map[string]uint64)
)

func emitIdleChatTTSAsync(bridge orchestrator.TTSBridge, ev idlechat.TimelineEvent) idlechat.TTSLifecycle {
	invalidTrace := ev.TraceID.Validate() != nil
	if bridge == nil || invalidTrace || !hasIdleChatViewerClients() {
		if bridge != nil {
			if invalidTrace {
				log.Printf("[IdleChat] TTS synthesis rejected before enqueue because trace is invalid: session=%s response=%s", strings.TrimSpace(ev.SessionID), strings.TrimSpace(ev.MessageID))
			} else {
				log.Printf("[IdleChat] TTS synthesis skipped before enqueue because no active audio Viewer is connected: session=%s response=%s", strings.TrimSpace(ev.SessionID), strings.TrimSpace(ev.MessageID))
			}
		}
		return idlechat.TTSLifecycle{}
	}
	controller := newIdleChatTTSLifecycleController()
	// The orchestrator owns the user-visible readiness and session-drain
	// deadlines. Starting another deadline when an item is enqueued makes a
	// bounded multi-chunk utterance lose part of its budget before synthesis.
	ctx, cancel := context.WithCancel(context.Background())
	item := &idleChatTTSItem{
		bridge:    bridge,
		ev:        ev,
		lifecycle: controller,
		ctx:       ctx,
		cancel:    cancel,
		gen:       idleChatTTSGen.Load(),
	}
	if !registerIdleChatTTSItem(item) {
		return idlechat.TTSLifecycle{}
	}
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

func registerIdleChatTTSItem(item *idleChatTTSItem) bool {
	if item == nil {
		return false
	}
	key := streamKey(item.ev.SessionID, item.ev.MessageID, string(item.ev.TraceID))
	idleChatTTSActiveMu.Lock()
	if idleChatTTSGenerationCancelledLocked(item.ev.RuntimeEpoch, item.ev.PlaybackGeneration) {
		idleChatTTSActiveMu.Unlock()
		cancelIdleChatTTSItem(item)
		item.lifecycle.signalDone()
		return false
	}
	if idleChatTTSActive[key] == nil {
		idleChatTTSActive[key] = make(map[*idleChatTTSItem]struct{})
	}
	idleChatTTSActive[key][item] = struct{}{}
	idleChatTTSActiveMu.Unlock()
	return true
}

func unregisterIdleChatTTSItem(item *idleChatTTSItem) {
	if item == nil {
		return
	}
	key := streamKey(item.ev.SessionID, item.ev.MessageID, string(item.ev.TraceID))
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
	if ev.TraceID.Validate() != nil {
		return
	}
	traceID := strings.TrimSpace(string(ev.TraceID))
	if ev.Kind == "session_audio_timeout" || strings.TrimSpace(ev.MessageID) == "" {
		cancelIdleChatTTSSession(ev.SessionID, traceID)
		return
	}
	idleChatTTSActiveMu.Lock()
	items := make([]*idleChatTTSItem, 0)
	for _, keyed := range idleChatTTSActive {
		for item := range keyed {
			if strings.TrimSpace(item.ev.SessionID) == strings.TrimSpace(ev.SessionID) &&
				strings.TrimSpace(item.ev.MessageID) == strings.TrimSpace(ev.MessageID) &&
				strings.TrimSpace(string(item.ev.TraceID)) == traceID {
				items = append(items, item)
			}
		}
	}
	idleChatTTSActiveMu.Unlock()
	for _, item := range items {
		cancelIdleChatTTSItem(item)
	}
}

func cancelIdleChatTTSSession(sessionID, traceID string) {
	sessionID = strings.TrimSpace(sessionID)
	traceID = strings.TrimSpace(traceID)
	if sessionID == "" {
		return
	}
	idleChatTTSActiveMu.Lock()
	items := make([]*idleChatTTSItem, 0)
	for _, keyed := range idleChatTTSActive {
		for item := range keyed {
			if strings.TrimSpace(item.ev.SessionID) == sessionID && strings.TrimSpace(string(item.ev.TraceID)) == traceID {
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

func cancelIdleChatTTSGeneration(runtimeEpoch string, playbackGeneration uint64) {
	runtimeEpoch = strings.TrimSpace(runtimeEpoch)
	if runtimeEpoch == "" || playbackGeneration == 0 {
		return
	}
	idleChatTTSActiveMu.Lock()
	if playbackGeneration > idleChatTTSCancelledGenerationByEpoch[runtimeEpoch] {
		idleChatTTSCancelledGenerationByEpoch[runtimeEpoch] = playbackGeneration
	}
	items := make([]*idleChatTTSItem, 0)
	for _, keyed := range idleChatTTSActive {
		for item := range keyed {
			if item.ev.RuntimeEpoch == runtimeEpoch && item.ev.PlaybackGeneration == playbackGeneration {
				items = append(items, item)
			}
		}
	}
	idleChatTTSActiveMu.Unlock()
	for _, item := range items {
		cancelIdleChatTTSItem(item)
		item.lifecycle.signalDone()
	}

	if idleChatTTSPrefetch != nil {
		idleChatTTSPrefetch.CancelGeneration(runtimeEpoch, playbackGeneration)
	}
	clearIdleChatTTSPendingGeneration(runtimeEpoch, playbackGeneration)
	retireTTSPublicSessionGeneration(runtimeEpoch, playbackGeneration)
}

func idleChatTTSGenerationCancelledLocked(runtimeEpoch string, playbackGeneration uint64) bool {
	runtimeEpoch = strings.TrimSpace(runtimeEpoch)
	return runtimeEpoch != "" && playbackGeneration != 0 && playbackGeneration <= idleChatTTSCancelledGenerationByEpoch[runtimeEpoch]
}

func idleChatTTSGenerationCancelled(runtimeEpoch string, playbackGeneration uint64) bool {
	idleChatTTSActiveMu.Lock()
	defer idleChatTTSActiveMu.Unlock()
	return idleChatTTSGenerationCancelledLocked(runtimeEpoch, playbackGeneration)
}

func ensureIdleChatTTSQueue() {
	idleChatTTSOnce.Do(func() {
		idleChatTTSQueue = make(chan *idleChatTTSItem, 512)
		go func() {
			for item := range idleChatTTSQueue {
				if item == nil {
					continue
				}
				if item.gen != idleChatTTSGen.Load() || idleChatTTSGenerationCancelled(item.ev.RuntimeEpoch, item.ev.PlaybackGeneration) || item.ctx == nil || item.ctx.Err() != nil {
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
