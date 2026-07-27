package main

import (
	"strings"
	"sync"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
)

type idleAwareEventListener struct {
	hub      *viewer.EventHub
	monitor  *viewer.MonitorStore
	archive  *viewer.EventLogStore
	mu       sync.RWMutex
	idleChat *idlechat.IdleChatOrchestrator

	recorderOnce sync.Once
	recorder     *eventRecorder
}

func (l *idleAwareEventListener) SetIdleChat(idle *idlechat.IdleChatOrchestrator) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.idleChat = idle
}

func (l *idleAwareEventListener) OnEvent(ev orchestrator.OrchestratorEvent) {
	// Live Viewer delivery is on the user-facing path for Chat and voice input.
	// Archive and monitor updates are useful, but they must not delay the next
	// orchestrator event such as voice_direct agent.response.
	l.hub.OnEvent(ev)
	if !shouldStopIdleChatByEvent(ev) {
		l.enqueueRecordEvent(ev)
		return
	}
	l.mu.RLock()
	idle := l.idleChat
	l.mu.RUnlock()
	if idle != nil {
		idle.NotifyActivity()
	}
	l.enqueueRecordEvent(ev)
}

// enqueueRecordEvent は記録パスへイベントを渡す
//
// docs/10_ログ仕様.md「記録と配信の分離」に従い、配信用の SideEffects キュー
// （満杯時に破棄する）とは別経路にする。以前は同じキューを共有していたため、
// 配信のための drop がアーカイブへの永続化そのものを欠落させていた。
func (l *idleAwareEventListener) enqueueRecordEvent(ev orchestrator.OrchestratorEvent) {
	l.recorderOnce.Do(func() {
		l.recorder = newEventRecorder(l.recordEventSync, 256)
	})
	if l.recorder == nil {
		return
	}
	l.recorder.Record(ev)
}

// Close は記録キューを排出してから停止する
func (l *idleAwareEventListener) Close() {
	if l == nil || l.recorder == nil {
		return
	}
	l.recorder.Close()
}

func (l *idleAwareEventListener) recordEventSync(ev orchestrator.OrchestratorEvent) error {
	if l.archive != nil {
		if err := l.archive.Append(ev); err != nil {
			return err
		}
	}
	if l.monitor != nil {
		l.monitor.OnEvent(ev)
	}
	return nil
}

func shouldStopIdleChatByEvent(ev orchestrator.OrchestratorEvent) bool {
	if strings.EqualFold(ev.Route, "IDLECHAT") {
		return false
	}
	if ev.Type == "tts.audio_chunk" || strings.EqualFold(ev.From, "tts") {
		return false
	}
	if ev.Type == "message.received" {
		return true
	}
	if ev.Type == "entry.stage" {
		stage := strings.ToLower(strings.TrimSpace(ev.Content))
		return stage == "received" || stage == "planning"
	}
	return false
}
