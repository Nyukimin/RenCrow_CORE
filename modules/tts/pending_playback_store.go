package tts

import (
	"strings"
	"sync"
)

type PendingPlaybackStore struct {
	mu         sync.Mutex
	pending    map[string]chan struct{}
	byResponse map[string]chan struct{}
	topicGate  map[string]chan struct{}
	topicByTTS map[string]string
}

func NewPendingPlaybackStore() *PendingPlaybackStore {
	return &PendingPlaybackStore{
		pending:    map[string]chan struct{}{},
		byResponse: map[string]chan struct{}{},
		topicGate:  map[string]chan struct{}{},
		topicByTTS: map[string]string{},
	}
}

func (s *PendingPlaybackStore) Register(ttsSessionID, responseID string) <-chan struct{} {
	if s == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	ttsSessionID = strings.TrimSpace(ttsSessionID)
	responseID = strings.TrimSpace(responseID)
	ch := make(chan struct{})
	s.mu.Lock()
	s.pending[ttsSessionID] = ch
	if responseID != "" {
		s.byResponse[responseID] = ch
	}
	s.mu.Unlock()
	return ch
}

func (s *PendingPlaybackStore) RegisterTopicGate(idleSessionID, ttsSessionID string) {
	if s == nil {
		return
	}
	idleSessionID = strings.TrimSpace(idleSessionID)
	ttsSessionID = strings.TrimSpace(ttsSessionID)
	if idleSessionID == "" || ttsSessionID == "" {
		return
	}
	s.mu.Lock()
	if _, ok := s.topicGate[idleSessionID]; !ok {
		s.topicGate[idleSessionID] = make(chan struct{})
	}
	s.topicByTTS[ttsSessionID] = idleSessionID
	s.mu.Unlock()
}

func (s *PendingPlaybackStore) CompleteByResponse(responseID string) PendingPlaybackCompletionAction {
	if s == nil {
		return BuildPendingPlaybackCompletionAction(responseID, "", "", false)
	}
	responseID = strings.TrimSpace(responseID)
	s.mu.Lock()
	ch, ok := s.byResponse[responseID]
	var topicCh chan struct{}
	action := BuildPendingPlaybackCompletionAction(responseID, "", "", false)
	if ok {
		delete(s.byResponse, responseID)
		for sessionID, sessionCh := range s.pending {
			if sessionCh == ch {
				delete(s.pending, sessionID)
				topicIdleSessionID := ""
				if idleSessionID, topicOK := s.topicByTTS[sessionID]; topicOK {
					delete(s.topicByTTS, sessionID)
					topicIdleSessionID = idleSessionID
					topicCh = s.topicGate[idleSessionID]
					delete(s.topicGate, idleSessionID)
				}
				action = BuildPendingPlaybackCompletionAction(responseID, sessionID, topicIdleSessionID, true)
				break
			}
		}
	}
	s.mu.Unlock()
	closePendingPlaybackChannels(action.ClosePendingWait, ch, action.CloseTopicGate, topicCh)
	return action
}

func (s *PendingPlaybackStore) Clear(ttsSessionID string) PendingPlaybackClearAction {
	if s == nil {
		return BuildPendingPlaybackClearAction(ttsSessionID, "", false)
	}
	ttsSessionID = strings.TrimSpace(ttsSessionID)
	s.mu.Lock()
	action := BuildPendingPlaybackClearAction(ttsSessionID, "", false)
	var ch chan struct{}
	var topicCh chan struct{}
	if pendingCh, ok := s.pending[ttsSessionID]; ok {
		ch = pendingCh
		delete(s.pending, ttsSessionID)
		for responseID, responseCh := range s.byResponse {
			if responseCh == pendingCh {
				delete(s.byResponse, responseID)
			}
		}
	}
	topicIdleSessionID := ""
	if idleSessionID, ok := s.topicByTTS[ttsSessionID]; ok {
		delete(s.topicByTTS, ttsSessionID)
		topicIdleSessionID = idleSessionID
		if gateCh := s.topicGate[idleSessionID]; gateCh != nil {
			topicCh = gateCh
			delete(s.topicGate, idleSessionID)
		}
	}
	if ch != nil {
		action = BuildPendingPlaybackClearAction(ttsSessionID, topicIdleSessionID, true)
	}
	s.mu.Unlock()
	closePendingPlaybackChannels(action.ClosePendingWait, ch, action.CloseTopicGate, topicCh)
	return action
}

func (s *PendingPlaybackStore) ClearByWait(target <-chan struct{}) PendingPlaybackClearAction {
	if s == nil || target == nil {
		return BuildPendingPlaybackClearAction("", "", false)
	}
	s.mu.Lock()
	var topicCh chan struct{}
	var targetCh chan struct{}
	action := BuildPendingPlaybackClearAction("", "", false)
	for sessionID, ch := range s.pending {
		if (<-chan struct{})(ch) == target {
			delete(s.pending, sessionID)
			targetCh = ch
			for responseID, responseCh := range s.byResponse {
				if responseCh == ch {
					delete(s.byResponse, responseID)
				}
			}
			topicIdleSessionID := ""
			if idleSessionID, ok := s.topicByTTS[sessionID]; ok {
				delete(s.topicByTTS, sessionID)
				topicIdleSessionID = idleSessionID
				topicCh = s.topicGate[idleSessionID]
				delete(s.topicGate, idleSessionID)
			}
			action = BuildPendingPlaybackClearAction(sessionID, topicIdleSessionID, true)
			break
		}
	}
	s.mu.Unlock()
	closePendingPlaybackChannels(action.ClosePendingWait, targetCh, action.CloseTopicGate, topicCh)
	return action
}

func (s *PendingPlaybackStore) ClearAll() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	pending := make([]chan struct{}, 0, len(s.pending))
	sessionIDs := make([]string, 0, len(s.pending))
	seen := map[chan struct{}]struct{}{}
	for sessionID, ch := range s.pending {
		sessionIDs = append(sessionIDs, sessionID)
		if _, ok := seen[ch]; ok {
			continue
		}
		seen[ch] = struct{}{}
		pending = append(pending, ch)
	}
	topicGates := make([]chan struct{}, 0, len(s.topicGate))
	for _, ch := range s.topicGate {
		topicGates = append(topicGates, ch)
	}
	s.pending = map[string]chan struct{}{}
	s.byResponse = map[string]chan struct{}{}
	s.topicGate = map[string]chan struct{}{}
	s.topicByTTS = map[string]string{}
	s.mu.Unlock()

	for _, ch := range pending {
		close(ch)
	}
	for _, ch := range topicGates {
		close(ch)
	}
	return sessionIDs
}

func (s *PendingPlaybackStore) WaitTopicGate(idleSessionID string) {
	if s == nil {
		return
	}
	idleSessionID = strings.TrimSpace(idleSessionID)
	s.mu.Lock()
	ch := s.topicGate[idleSessionID]
	s.mu.Unlock()
	if ch == nil {
		return
	}
	<-ch
}

func (s *PendingPlaybackStore) Snapshot() PendingPlaybackSnapshot {
	if s == nil {
		return BuildPendingPlaybackSnapshot(nil, nil, 0, 0)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sessionIDs := make([]string, 0, len(s.pending))
	for sessionID := range s.pending {
		sessionIDs = append(sessionIDs, sessionID)
	}
	responseIDs := make([]string, 0, len(s.byResponse))
	for responseID := range s.byResponse {
		responseIDs = append(responseIDs, responseID)
	}
	return BuildPendingPlaybackSnapshot(sessionIDs, responseIDs, len(s.topicGate), len(s.topicByTTS))
}

func closePendingPlaybackChannels(closePending bool, pendingCh chan struct{}, closeTopic bool, topicCh chan struct{}) {
	if closePending && pendingCh != nil {
		close(pendingCh)
	}
	if closeTopic && topicCh != nil {
		close(topicCh)
	}
}
