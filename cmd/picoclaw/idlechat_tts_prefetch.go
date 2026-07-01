package main

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/idlechat"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/orchestrator"
	moduletts "github.com/Nyukimin/picoclaw_multiLLM/modules/tts"
)

type idleChatTTSPrefetchManager struct {
	bridge  orchestrator.TTSBridge
	mu      sync.Mutex
	streams map[string]*idleChatTTSPrefetchStream
}

type idleChatTTSPrefetchStream struct {
	bridge            orchestrator.TTSBridge
	key               string
	sessionID         string
	responseID        string
	publicSessionID   string
	messageID         string
	speaker           string
	target            string
	turnIndex         int
	voiceProfile      string
	queue             chan string
	wg                sync.WaitGroup
	mu                sync.Mutex
	started           bool
	closed            bool
	expectPlaybackAck bool
	waitCh            <-chan struct{}
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

func (m *idleChatTTSPrefetchManager) Close(ev idlechat.TimelineEvent) (<-chan struct{}, bool) {
	if m == nil || m.bridge == nil || strings.TrimSpace(ev.SessionID) == "" || strings.TrimSpace(ev.MessageID) == "" {
		return nil, false
	}
	key := streamKey(ev.SessionID, ev.MessageID)
	m.mu.Lock()
	stream := m.streams[key]
	if stream != nil {
		delete(m.streams, key)
	}
	m.mu.Unlock()
	if stream == nil {
		return emitIdleChatTTS(context.Background(), m.bridge, ev)
	}
	waitCh, ok := stream.close(ev)
	if !ok {
		return emitIdleChatTTS(context.Background(), m.bridge, ev)
	}
	return waitCh, true
}

func (m *idleChatTTSPrefetchManager) HasActive(sessionID, messageID string) bool {
	if m == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(messageID) == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.streams[streamKey(sessionID, messageID)]
	return ok
}

func (m *idleChatTTSPrefetchManager) stream(sessionID, messageID string, ev idlechat.TTSPrefetchEvent) *idleChatTTSPrefetchStream {
	key := streamKey(sessionID, messageID)
	m.mu.Lock()
	defer m.mu.Unlock()
	if stream := m.streams[key]; stream != nil {
		return stream
	}
	stream := &idleChatTTSPrefetchStream{
		bridge:          m.bridge,
		key:             key,
		queue:           make(chan string, 128),
		speaker:         strings.TrimSpace(ev.From),
		target:          strings.TrimSpace(ev.To),
		sessionID:       strings.TrimSpace(ev.SessionID),
		publicSessionID: strings.TrimSpace(ev.SessionID),
		messageID:       strings.TrimSpace(ev.MessageID),
		turnIndex:       ev.TurnIndex,
	}
	stream.wg.Add(1)
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
	closed := s.closed
	queue := s.queue
	s.mu.Unlock()
	if closed || queue == nil {
		return
	}
	select {
	case queue <- token:
	default:
		log.Printf("[IdleChat] TTS prefetch queue full; dropping token: key=%s", s.key)
	}
}

func (s *idleChatTTSPrefetchStream) run() {
	defer s.wg.Done()
	for token := range s.queue {
		s.consumeToken(token)
	}
	s.finalizeAfterQueueDrain()
}

func (s *idleChatTTSPrefetchStream) consumeToken(token string) {
	if s == nil || token == "" {
		return
	}
	for _, chunk := range s.chunker.AcceptToken(token) {
		s.pushChunk(chunk)
	}
}

func (s *idleChatTTSPrefetchStream) pushChunk(text string) {
	text = strings.TrimSpace(text)
	if s == nil || text == "" {
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
	waitCh := s.waitCh
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
			waitCh = registerIdleChatTTSPending(plan.SessionID, plan.ResponseID)
		} else {
			log.Printf("[IdleChat] TTS playback wait skipped because no Viewer SSE clients are connected: session=%s response=%s", plan.SessionID, plan.ResponseID)
		}
		if err := s.bridge.StartSession(context.Background(), orchestrator.TTSSessionStart{
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
			if expectPlaybackAck {
				clearIdleChatTTSPending(plan.SessionID)
			} else {
				clearTTSPublicSession(plan.SessionID)
			}
			log.Printf("[IdleChat] TTS prefetch start failed: %v", err)
			return
		}
		if displayBridge, ok := s.bridge.(orchestrator.TTSDisplayBridge); ok {
			if err := displayBridge.PushTextWithDisplay(context.Background(), plan.SessionID, plan.SpeechText, plan.DisplayText, &emotion); err != nil {
				log.Printf("[IdleChat] TTS prefetch push failed: %v", err)
			}
		} else if err := s.bridge.PushText(context.Background(), plan.SessionID, plan.SpeechText, &emotion); err != nil {
			log.Printf("[IdleChat] TTS prefetch push failed: %v", err)
		}
		s.mu.Lock()
		s.started = true
		s.sessionID = plan.SessionID
		s.responseID = plan.ResponseID
		s.publicSessionID = plan.PublicSessionID
		s.messageID = plan.MessageID
		s.turnIndex = plan.TurnIndex
		s.voiceProfile = plan.VoiceProfile
		s.expectPlaybackAck = expectPlaybackAck
		s.waitCh = waitCh
		s.mu.Unlock()
		return
	}

	emotion := moduletts.PlanEmotion(moduletts.EmotionInput{
		Event:        moduletts.IdleChatTTSEventName,
		Text:         filtered,
		Context:      moduletts.EmotionContext{TimeOfDay: idleChatTimeOfDay(), Urgency: moduletts.IdleChatTTSUrgencyNormal},
		VoiceProfile: voiceProfile,
	})
	if displayBridge, ok := s.bridge.(orchestrator.TTSDisplayBridge); ok {
		if err := displayBridge.PushTextWithDisplay(context.Background(), s.sessionID, filtered, text, &emotion); err != nil {
			log.Printf("[IdleChat] TTS prefetch push failed: %v", err)
		}
	} else if err := s.bridge.PushText(context.Background(), s.sessionID, filtered, &emotion); err != nil {
		log.Printf("[IdleChat] TTS prefetch push failed: %v", err)
	}
}

func (s *idleChatTTSPrefetchStream) finalizeAfterQueueDrain() {
	s.mu.Lock()
	started := s.started
	sessionID := s.sessionID
	expectPlaybackAck := s.expectPlaybackAck
	waitCh := s.waitCh
	finalEvent := s.finalEvent
	closed := s.closed
	voiceProfile := s.voiceProfile
	s.mu.Unlock()

	if !started || !closed {
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
			filtered := moduletts.FilterSpeakableText("agent.response", idleChatRoute, chunk)
			if filtered == "" {
				continue
			}
			if displayBridge, ok := s.bridge.(orchestrator.TTSDisplayBridge); ok {
				if err := displayBridge.PushTextWithDisplay(context.Background(), sessionID, filtered, chunk, &emotion); err != nil {
					log.Printf("[IdleChat] TTS prefetch push failed: %v", err)
				}
			} else if err := s.bridge.PushText(context.Background(), sessionID, filtered, &emotion); err != nil {
				log.Printf("[IdleChat] TTS prefetch push failed: %v", err)
			}
		}
	}
	if err := s.bridge.EndSession(context.Background(), sessionID); err != nil {
		log.Printf("[IdleChat] TTS prefetch end failed: %v", err)
		if expectPlaybackAck {
			clearIdleChatTTSPending(sessionID)
		} else {
			clearTTSPublicSession(sessionID)
		}
		return
	}
	if !expectPlaybackAck {
		clearTTSPublicSession(sessionID)
	}
	_ = waitCh
}

func (s *idleChatTTSPrefetchStream) close(ev idlechat.TimelineEvent) (<-chan struct{}, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	if s.closed {
		waitCh := s.waitCh
		s.mu.Unlock()
		return waitCh, true
	}
	s.closed = true
	s.finalEvent = ev
	close(s.queue)
	waitCh := s.waitCh
	s.mu.Unlock()

	s.wg.Wait()
	if !s.started {
		return emitIdleChatTTS(context.Background(), s.bridge, ev)
	}
	return waitCh, true
}
