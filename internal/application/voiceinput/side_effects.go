package voiceinput

import (
	"log"
	"time"
)

type SideEffect struct {
	Name        string
	UtteranceID string
	SessionID   string
	Run         func() error
}

type SideEffects struct {
	ch      chan SideEffect
	timeout time.Duration
}

func NewSideEffects(size int, timeout time.Duration) *SideEffects {
	if size <= 0 {
		size = 256
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	s := &SideEffects{
		ch:      make(chan SideEffect, size),
		timeout: timeout,
	}
	go s.run()
	return s
}

func (s *SideEffects) Enqueue(effect SideEffect) bool {
	if s == nil || effect.Run == nil {
		return false
	}
	select {
	case s.ch <- effect:
		return true
	default:
		log.Printf("WARN: voiceinput side effect queue full name=%s utterance_id=%s session_id=%s", effect.Name, effect.UtteranceID, effect.SessionID)
		return false
	}
}

func (s *SideEffects) run() {
	for effect := range s.ch {
		name := effect.Name
		if name == "" {
			name = "unnamed"
		}
		started := time.Now()
		log.Printf("[voiceinput] side_effect start name=%s utterance_id=%s session_id=%s", name, effect.UtteranceID, effect.SessionID)
		err := effect.Run()
		elapsed := time.Since(started)
		if err != nil {
			log.Printf("[voiceinput] side_effect failed name=%s utterance_id=%s session_id=%s elapsed_ms=%d err=%v", name, effect.UtteranceID, effect.SessionID, elapsed.Milliseconds(), err)
			continue
		}
		if elapsed > s.timeout {
			log.Printf("[voiceinput] side_effect slow name=%s utterance_id=%s session_id=%s elapsed_ms=%d", name, effect.UtteranceID, effect.SessionID, elapsed.Milliseconds())
		}
		log.Printf("[voiceinput] side_effect completed name=%s utterance_id=%s session_id=%s elapsed_ms=%d", name, effect.UtteranceID, effect.SessionID, elapsed.Milliseconds())
	}
}
