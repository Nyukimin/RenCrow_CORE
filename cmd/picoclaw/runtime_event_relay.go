package main

import (
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/viewer"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/idlechat"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/orchestrator"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/voiceinput"
)

type idleAwareEventListener struct {
	hub      *viewer.EventHub
	monitor  *viewer.MonitorStore
	archive  *viewer.EventLogStore
	mu       sync.RWMutex
	idleChat *idlechat.IdleChatOrchestrator

	sideEffectsOnce sync.Once
	sideEffects     *voiceinput.SideEffects
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

func (l *idleAwareEventListener) enqueueRecordEvent(ev orchestrator.OrchestratorEvent) {
	l.sideEffectsOnce.Do(func() {
		l.sideEffects = voiceinput.NewSideEffects(256, 3*time.Second)
	})
	if l.sideEffects == nil {
		return
	}
	l.sideEffects.Enqueue(voiceinput.SideEffect{
		Name:      "viewer_event",
		SessionID: ev.SessionID,
		Run: func() error {
			return l.recordEventSync(ev)
		},
	})
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
