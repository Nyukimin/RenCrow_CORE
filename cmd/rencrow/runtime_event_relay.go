package main

import (
	"errors"
	"strings"
	"sync"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
)

type idleAwareEventListener struct {
	hub           *viewer.EventHub
	monitor       *viewer.MonitorStore
	archive       *viewer.CanonicalEventLog
	mu            sync.RWMutex
	publicationMu sync.Mutex
	idleChat      *idlechat.IdleChatOrchestrator
}

var errCanonicalEventArchiveRequired = errors.New("canonical event archive is required")

func (l *idleAwareEventListener) SetIdleChat(idle *idlechat.IdleChatOrchestrator) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.idleChat = idle
}

func (l *idleAwareEventListener) OnEvent(ev orchestrator.OrchestratorEvent) error {
	if l == nil || l.archive == nil {
		return errCanonicalEventArchiveRequired
	}

	// Canonical persistence is the single publication gate. No projection or
	// user-facing side effect may happen before this append succeeds.
	l.publicationMu.Lock()
	persisted, err := l.archive.AppendSequenced(ev)
	if err != nil {
		l.publicationMu.Unlock()
		return err
	}
	ev = persisted

	if l.monitor != nil {
		l.monitor.OnEvent(ev)
	}
	if l.hub != nil {
		l.hub.OnEvent(ev)
	}
	l.publicationMu.Unlock()

	if shouldStopIdleChatByEvent(ev) {
		l.mu.RLock()
		idle := l.idleChat
		l.mu.RUnlock()
		if idle != nil {
			idle.NotifyActivity()
		}
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
