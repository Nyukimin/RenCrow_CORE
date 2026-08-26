package main

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

type idleChatTTSPrefetchManager struct {
	bridge  orchestrator.TTSBridge
	mu      sync.Mutex
	streams map[string]*idleChatTTSPrefetchStream
}

type idleChatTTSPrefetchStream struct {
	manager           *idleChatTTSPrefetchManager
	bridge            orchestrator.TTSBridge
	key               string
	sessionID         string
	internalSessionID string
	responseID        string
	publicSessionID   string
	messageID         string
	speaker           string
	target            string
	turnIndex         int
	voiceProfile      string
	queue             chan string
	ctx               context.Context
	cancel            context.CancelFunc
	lifecycle         *idleChatTTSLifecycleController
	mu                sync.Mutex
	started           bool
	closed            bool
	failed            bool
	expectPlaybackAck bool
	cleanupOnce       sync.Once
	endOnce           sync.Once
	endErr            error
	finalEvent        idlechat.TimelineEvent
	chunker           moduletts.StreamChunker
}

func newIdleChatTTSPrefetchManager(bridge orchestrator.TTSBridge) *idleChatTTSPrefetchManager {
	if bridge == nil {
		return nil
	}
	return &idleChatTTSPrefetchManager{
		bridge:  bridge,
		streams: make(map[string]*idleChatTTSPrefetchStream),
	}
}

func (m *idleChatTTSPrefetchManager) Push(ev idlechat.TTSPrefetchEvent) {
	if m == nil || m.bridge == nil || strings.TrimSpace(ev.SessionID) == "" || strings.TrimSpace(ev.MessageID) == "" {
		return
	}
	stream := m.stream(ev.SessionID, ev.MessageID, ev)
	stream.enqueue(ev.Token)
}

func (m *idleChatTTSPrefetchManager) Close(ev idlechat.TimelineEvent) (idlechat.TTSLifecycle, bool) {
	if m == nil || m.bridge == nil || strings.TrimSpace(ev.SessionID) == "" || strings.TrimSpace(ev.MessageID) == "" {
		return idlechat.TTSLifecycle{}, false
	}
	key := streamKey(ev.SessionID, ev.MessageID)
	m.mu.Lock()
	stream := m.streams[key]
	m.mu.Unlock()
	if stream == nil {
		stream = m.stream(ev.SessionID, ev.MessageID, idlechat.TTSPrefetchEvent{
			SessionID: ev.SessionID,
			MessageID: ev.MessageID,
			From:      ev.From,
			To:        ev.To,
			TurnIndex: ev.TurnIndex,
		})
	}
	return stream.close(ev)
}

func (m *idleChatTTSPrefetchManager) CancelTimeout(ev idlechat.TTSTimeoutEvent) {
	if m == nil {
		return
	}
	m.cancelMatching(ev.SessionID, ev.MessageID, ev.Kind == "session_audio_timeout")
}

func (m *idleChatTTSPrefetchManager) CancelAll() {
	if m == nil {
		return
	}
	m.mu.Lock()
	streams := make([]*idleChatTTSPrefetchStream, 0, len(m.streams))
	for _, stream := range m.streams {
		streams = append(streams, stream)
	}
	m.streams = make(map[string]*idleChatTTSPrefetchStream)
	m.mu.Unlock()
	for _, stream := range streams {
		stream.cancelForStale()
	}
}

func (m *idleChatTTSPrefetchManager) cancelMatching(sessionID, messageID string, allForSession bool) {
	sessionID = strings.TrimSpace(sessionID)
	messageID = strings.TrimSpace(messageID)
	m.mu.Lock()
	streams := make([]*idleChatTTSPrefetchStream, 0)
	for _, stream := range m.streams {
		if stream == nil || strings.TrimSpace(stream.sessionID) != sessionID {
			continue
		}
		if !allForSession && messageID != "" && strings.TrimSpace(stream.messageID) != messageID {
			continue
		}
		streams = append(streams, stream)
	}
	m.mu.Unlock()
	for _, stream := range streams {
		stream.cancelForStale()
	}
}

func (m *idleChatTTSPrefetchManager) HasActive(sessionID, messageID string) bool {
	if m == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(messageID) == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	stream, ok := m.streams[streamKey(sessionID, messageID)]
	if !ok || stream == nil {
		return false
	}
	stream.mu.Lock()
	closed := stream.closed
	stream.mu.Unlock()
	return !closed
}

func (m *idleChatTTSPrefetchManager) removeStream(key string, stream *idleChatTTSPrefetchStream) {
	if m == nil || stream == nil {
		return
	}
	m.mu.Lock()
	if current := m.streams[key]; current == stream {
		delete(m.streams, key)
	}
	m.mu.Unlock()
}

func (m *idleChatTTSPrefetchManager) stream(sessionID, messageID string, ev idlechat.TTSPrefetchEvent) *idleChatTTSPrefetchStream {
	key := streamKey(sessionID, messageID)
	m.mu.Lock()
	defer m.mu.Unlock()
	if stream := m.streams[key]; stream != nil {
		return stream
	}
	stream := &idleChatTTSPrefetchStream{
		manager:         m,
		bridge:          m.bridge,
		key:             key,
		queue:           make(chan string, 128),
		speaker:         strings.TrimSpace(ev.From),
		target:          strings.TrimSpace(ev.To),
		sessionID:       strings.TrimSpace(ev.SessionID),
		publicSessionID: strings.TrimSpace(ev.SessionID),
		messageID:       strings.TrimSpace(ev.MessageID),
		turnIndex:       ev.TurnIndex,
		responseID:      nextTTSPublicResponseIDForMessage(strings.TrimSpace(ev.SessionID), strings.TrimSpace(ev.MessageID)),
	}
	// Timeout ownership belongs to the orchestrator's Ready/Done waits. A
	// prefetch stream may exist before its final utterance is known, so a second
	// deadline here would consume the drain budget prematurely.
	stream.ctx, stream.cancel = context.WithCancel(context.Background())
	stream.lifecycle = newIdleChatTTSLifecycleController()
	go stream.run()
	m.streams[key] = stream
	return stream
}

func streamKey(sessionID, messageID string) string {
	return strings.TrimSpace(sessionID) + "\x00" + strings.TrimSpace(messageID)
}

func (s *idleChatTTSPrefetchStream) enqueue(token string) {
	token = strings.TrimSpace(token)
	if s == nil || token == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.queue == nil {
		return
	}
	select {
	case s.queue <- token:
	default:
		log.Printf("[IdleChat] TTS prefetch queue full; dropping token: key=%s", s.key)
	}
}

func (s *idleChatTTSPrefetchStream) run() {
	defer s.manager.removeStream(s.key, s)
	defer s.cancel()
	defer s.lifecycle.signalDone()
	defer func() {
		s.mu.Lock()
		internalSessionID := s.internalSessionID
		s.mu.Unlock()
		unregisterIdleChatTTSSynthesisLifecycle(internalSessionID, s.lifecycle)
	}()
	for token := range s.queue {
		if s.ctx.Err() != nil {
			break
		}
		s.consumeToken(token)
	}
	s.finalizeAfterQueueDrain()
}

func (s *idleChatTTSPrefetchStream) consumeToken(token string) {
	if s == nil || token == "" || s.ctx == nil || s.ctx.Err() != nil {
		return
	}
	for _, chunk := range s.chunker.AcceptToken(token) {
		s.pushChunk(chunk)
	}
}

func (s *idleChatTTSPrefetchStream) pushPreparedChunk(text, displayText string, emotion *moduletts.EmotionState) error {
	if s == nil || s.ctx == nil || s.ctx.Err() != nil {
		if s != nil && s.ctx != nil {
			return s.ctx.Err()
		}
		return context.Canceled
	}
	s.mu.Lock()
	sessionID := s.internalSessionID
	s.mu.Unlock()
	if strings.TrimSpace(sessionID) == "" {
		return context.Canceled
	}
	if displayBridge, ok := s.bridge.(orchestrator.TTSDisplayBridge); ok {
		return displayBridge.PushTextWithDisplay(s.ctx, sessionID, text, displayText, emotion)
	}
	return s.bridge.PushText(s.ctx, sessionID, text, emotion)
}

func (s *idleChatTTSPrefetchStream) cleanupStale() {
	if s == nil {
		return
	}
	s.cleanupOnce.Do(func() {
		s.mu.Lock()
		internalSessionID := strings.TrimSpace(s.internalSessionID)
		expectPlaybackAck := s.expectPlaybackAck
		s.mu.Unlock()
		if internalSessionID == "" {
			return
		}
		if expectPlaybackAck {
			clearIdleChatTTSPendingStale(internalSessionID)
			return
		}
		retireTTSPublicSession(internalSessionID)
	})
}

func (s *idleChatTTSPrefetchStream) endSession() error {
	if s == nil {
		return nil
	}
	s.endOnce.Do(func() {
		s.mu.Lock()
		internalSessionID := strings.TrimSpace(s.internalSessionID)
		s.mu.Unlock()
		if internalSessionID == "" {
			return
		}
		s.endErr = s.bridge.EndSession(s.ctx, internalSessionID)
	})
	return s.endErr
}

func (s *idleChatTTSPrefetchStream) cancelForStale() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.queue)
	}
	s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	s.cleanupStale()
	s.lifecycle.signalDone()
}

func (s *idleChatTTSPrefetchStream) pushChunk(text string) {
	text = strings.TrimSpace(text)
	if s == nil || text == "" || s.ctx == nil || s.ctx.Err() != nil {
		return
	}
	filtered := moduletts.FilterSpeakableText("agent.response", idleChatRoute, text)
	if filtered == "" {
		return
	}

	s.mu.Lock()
	started := s.started
	speaker := s.speaker
	turnIndex := s.turnIndex
	responseID := s.responseID
	publicSessionID := s.publicSessionID
	voiceProfile := s.voiceProfile
	s.mu.Unlock()

	if !started {
		plan, ok := moduletts.BuildIdleChatTTSPlan(moduletts.IdleChatTTSPlanInput{
			PublicSessionID: publicSessionID,
			ResponseID:      responseID,
			MessageID:       s.messageID,
			TurnIndex:       turnIndex,
			Speaker:         speaker,
			SpeechText:      filtered,
			DisplayText:     text,
			TimeOfDay:       idleChatTimeOfDay(),
			Now:             time.Now(),
		})
		if !ok {
			return
		}
		emotion := moduletts.PlanEmotion(moduletts.EmotionInput{
			Event: plan.Event,
			Text:  plan.SpeechText,
			Context: moduletts.EmotionContext{
				ConversationMode: plan.ConversationMode,
				TimeOfDay:        plan.TimeOfDay,
				Urgency:          plan.Urgency,
			},
			VoiceProfile: plan.VoiceProfile,
		})

		expectPlaybackAck := hasIdleChatViewerClients()
		registerTTSPublicSessionWithMessage(plan.SessionID, plan.PublicSessionID, plan.ResponseID, plan.MessageID, plan.TurnIndex)
		if expectPlaybackAck {
			registerIdleChatTTSPending(plan.SessionID, plan.ResponseID)
		} else {
			log.Printf("[IdleChat] TTS playback wait skipped because no Viewer SSE clients are connected: session=%s response=%s", plan.SessionID, plan.ResponseID)
		}
		s.mu.Lock()
		s.internalSessionID = plan.SessionID
		s.expectPlaybackAck = expectPlaybackAck
		s.voiceProfile = plan.VoiceProfile
		s.mu.Unlock()
		registerIdleChatTTSSynthesisLifecycle(plan.SessionID, s.lifecycle)
		if err := s.bridge.StartSession(s.ctx, orchestrator.TTSSessionStart{
			SessionID:        plan.SessionID,
			ResponseID:       plan.ResponseID,
			CharacterID:      plan.CharacterID,
			VoiceID:          plan.VoiceID,
			SpeechMode:       plan.SpeechMode,
			Event:            plan.Event,
			ConversationMode: plan.ConversationMode,
			Context: moduletts.EmotionContext{
				ConversationMode: plan.ConversationMode,
				TimeOfDay:        plan.TimeOfDay,
				Urgency:          plan.Urgency,
			},
			VoiceProfile:          plan.VoiceProfile,
			UserAttentionRequired: false,
		}); err != nil {
			s.mu.Lock()
			s.failed = true
			s.mu.Unlock()
			s.cleanupStale()
			log.Printf("[IdleChat] TTS prefetch start failed: %v", err)
			s.cancelForStale()
			return
		}
		if err := s.pushPreparedChunk(plan.SpeechText, plan.DisplayText, &emotion); err != nil {
			log.Printf("[IdleChat] TTS prefetch push failed: %v", err)
			s.mu.Lock()
			s.failed = true
			s.mu.Unlock()
			s.cleanupStale()
			s.cancelForStale()
			return
		}
		s.mu.Lock()
		s.started = true
		s.voiceProfile = plan.VoiceProfile
		s.mu.Unlock()
		s.lifecycle.signalReady()
		return
	}

	emotion := moduletts.PlanEmotion(moduletts.EmotionInput{
		Event:        moduletts.IdleChatTTSEventName,
		Text:         filtered,
		Context:      moduletts.EmotionContext{TimeOfDay: idleChatTimeOfDay(), Urgency: moduletts.IdleChatTTSUrgencyNormal},
		VoiceProfile: voiceProfile,
	})
	if err := s.pushPreparedChunk(filtered, text, &emotion); err != nil {
		log.Printf("[IdleChat] TTS prefetch push failed: %v", err)
		s.mu.Lock()
		s.failed = true
		s.mu.Unlock()
		s.cleanupStale()
		s.cancelForStale()
	}
}

func (s *idleChatTTSPrefetchStream) finalizeAfterQueueDrain() {
	s.mu.Lock()
	started := s.started
	failed := s.failed
	expectPlaybackAck := s.expectPlaybackAck
	finalEvent := s.finalEvent
	closed := s.closed
	voiceProfile := s.voiceProfile
	s.mu.Unlock()

	if !closed {
		return
	}
	if s.ctx == nil || s.ctx.Err() != nil || failed {
		if started {
			if err := s.endSession(); err != nil {
				log.Printf("[IdleChat] TTS prefetch end after cancellation failed: %v", err)
			}
			s.cleanupStale()
		}
		return
	}
	if !started {
		if strings.TrimSpace(finalEvent.Content) == "" && strings.TrimSpace(finalEvent.RawContent) == "" {
			return
		}
		emitIdleChatTTSWithLifecycle(s.ctx, s.bridge, finalEvent, s.lifecycle)
		return
	}

	finalText := strings.TrimSpace(finalEvent.RawContent)
	if finalText == "" {
		finalText = strings.TrimSpace(finalEvent.Content)
	}
	if finalText != "" {
		emotion := moduletts.PlanEmotion(moduletts.EmotionInput{
			Event: moduletts.IdleChatTTSEventName,
			Text:  finalText,
			Context: moduletts.EmotionContext{
				TimeOfDay: idleChatTimeOfDay(),
				Urgency:   moduletts.IdleChatTTSUrgencyNormal,
			},
			VoiceProfile: voiceProfile,
		})
		for _, chunk := range s.chunker.FinalizeAll(finalText) {
			if s.ctx.Err() != nil {
				break
			}
			filtered := moduletts.FilterSpeakableText("agent.response", idleChatRoute, chunk)
			if filtered == "" {
				continue
			}
			if err := s.pushPreparedChunk(filtered, chunk, &emotion); err != nil {
				log.Printf("[IdleChat] TTS prefetch push failed: %v", err)
				s.mu.Lock()
				s.failed = true
				s.mu.Unlock()
				s.cleanupStale()
				s.cancelForStale()
				return
			}
		}
	}
	if err := s.endSession(); err != nil {
		log.Printf("[IdleChat] TTS prefetch end failed: %v", err)
		s.cleanupStale()
		return
	}
	if !expectPlaybackAck {
		s.mu.Lock()
		internalSessionID := s.internalSessionID
		s.mu.Unlock()
		clearTTSPublicSession(internalSessionID)
	}
}

func (s *idleChatTTSPrefetchStream) close(ev idlechat.TimelineEvent) (idlechat.TTSLifecycle, bool) {
	if s == nil {
		return idlechat.TTSLifecycle{}, false
	}
	s.mu.Lock()
	if s.closed {
		lifecycle := s.lifecycle.lifecycle()
		s.mu.Unlock()
		return lifecycle, true
	}
	s.closed = true
	s.finalEvent = ev
	close(s.queue)
	lifecycle := s.lifecycle.lifecycle()
	s.mu.Unlock()
	return lifecycle, true
}
