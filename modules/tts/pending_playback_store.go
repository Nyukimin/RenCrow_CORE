package tts

import (
	"strings"
	"sync"
)

type PendingPlaybackStore struct {
	mu                  sync.Mutex
	pending             map[string]chan struct{}
	byPublicPlaybackRef map[string]chan struct{}
	topicGate           map[string]chan struct{}
	topicByTTS          map[string]string
	playbackBySession   map[string]PlaybackGenerationIdentity
	topicPlaybackByTTS  map[string]PlaybackGenerationIdentity
	cancelledByRuntime  map[string]uint64
}

// PlaybackGenerationIdentity scopes transient pending playback to the
// IdleChat runtime and playback generation that created it.
type PlaybackGenerationIdentity struct {
	RuntimeEpoch       string
	PlaybackGeneration uint64
}

func (g PlaybackGenerationIdentity) valid() bool {
	return strings.TrimSpace(g.RuntimeEpoch) != "" && g.PlaybackGeneration > 0
}

func NewPendingPlaybackStore() *PendingPlaybackStore {
	return &PendingPlaybackStore{
		pending:             map[string]chan struct{}{},
		byPublicPlaybackRef: map[string]chan struct{}{},
		topicGate:           map[string]chan struct{}{},
		topicByTTS:          map[string]string{},
		playbackBySession:   map[string]PlaybackGenerationIdentity{},
		topicPlaybackByTTS:  map[string]PlaybackGenerationIdentity{},
		cancelledByRuntime:  map[string]uint64{},
	}
}

func (s *PendingPlaybackStore) Register(ttsSessionID, publicPlaybackRef string) <-chan struct{} {
	return s.RegisterForPlaybackGeneration(ttsSessionID, publicPlaybackRef, PlaybackGenerationIdentity{})
}

func (s *PendingPlaybackStore) RegisterForPlaybackGeneration(ttsSessionID, publicPlaybackRef string, identity PlaybackGenerationIdentity) <-chan struct{} {
	if s == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	ttsSessionID = strings.TrimSpace(ttsSessionID)
	publicPlaybackRef = strings.TrimSpace(publicPlaybackRef)
	ch := make(chan struct{})
	s.mu.Lock()
	identity.RuntimeEpoch = strings.TrimSpace(identity.RuntimeEpoch)
	if identity.valid() && s.playbackGenerationCancelledLocked(identity) {
		s.mu.Unlock()
		close(ch)
		return ch
	}
	s.pending[ttsSessionID] = ch
	if identity.valid() {
		s.playbackBySession[ttsSessionID] = identity
	} else {
		delete(s.playbackBySession, ttsSessionID)
	}
	if publicPlaybackRef != "" {
		s.byPublicPlaybackRef[publicPlaybackRef] = ch
	}
	s.mu.Unlock()
	return ch
}

func (s *PendingPlaybackStore) RegisterTopicGate(idleSessionID, ttsSessionID string) {
	s.RegisterTopicGateForPlayback(idleSessionID, ttsSessionID, PlaybackGenerationIdentity{})
}

func (s *PendingPlaybackStore) RegisterTopicGateForPlayback(idleSessionID, ttsSessionID string, identity PlaybackGenerationIdentity) {
	if s == nil {
		return
	}
	idleSessionID = strings.TrimSpace(idleSessionID)
	ttsSessionID = strings.TrimSpace(ttsSessionID)
	if idleSessionID == "" || ttsSessionID == "" {
		return
	}
	s.mu.Lock()
	identity.RuntimeEpoch = strings.TrimSpace(identity.RuntimeEpoch)
	if identity.valid() && s.playbackGenerationCancelledLocked(identity) {
		s.mu.Unlock()
		return
	}
	if _, ok := s.topicGate[idleSessionID]; !ok {
		s.topicGate[idleSessionID] = make(chan struct{})
	}
	s.topicByTTS[ttsSessionID] = idleSessionID
	if identity.valid() {
		s.topicPlaybackByTTS[ttsSessionID] = identity
	} else {
		delete(s.topicPlaybackByTTS, ttsSessionID)
	}
	s.mu.Unlock()
}

func (s *PendingPlaybackStore) CompleteByPublicPlaybackRef(publicPlaybackRef string) PendingPlaybackCompletionAction {
	if s == nil {
		return BuildPendingPlaybackCompletionAction(publicPlaybackRef, "", "", false)
	}
	publicPlaybackRef = strings.TrimSpace(publicPlaybackRef)
	s.mu.Lock()
	ch, ok := s.byPublicPlaybackRef[publicPlaybackRef]
	var topicCh chan struct{}
	action := BuildPendingPlaybackCompletionAction(publicPlaybackRef, "", "", false)
	if ok {
		delete(s.byPublicPlaybackRef, publicPlaybackRef)
		for sessionID, sessionCh := range s.pending {
			if sessionCh == ch {
				delete(s.pending, sessionID)
				delete(s.playbackBySession, sessionID)
				topicIdleSessionID := ""
				if idleSessionID, topicOK := s.topicByTTS[sessionID]; topicOK {
					delete(s.topicByTTS, sessionID)
					delete(s.topicPlaybackByTTS, sessionID)
					topicIdleSessionID = idleSessionID
					topicCh = s.topicGate[idleSessionID]
					delete(s.topicGate, idleSessionID)
				}
				action = BuildPendingPlaybackCompletionAction(publicPlaybackRef, sessionID, topicIdleSessionID, true)
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
		delete(s.playbackBySession, ttsSessionID)
		for publicPlaybackRef, responseCh := range s.byPublicPlaybackRef {
			if responseCh == pendingCh {
				delete(s.byPublicPlaybackRef, publicPlaybackRef)
			}
		}
	}
	topicIdleSessionID := ""
	if idleSessionID, ok := s.topicByTTS[ttsSessionID]; ok {
		delete(s.topicByTTS, ttsSessionID)
		delete(s.topicPlaybackByTTS, ttsSessionID)
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
			delete(s.playbackBySession, sessionID)
			targetCh = ch
			for publicPlaybackRef, responseCh := range s.byPublicPlaybackRef {
				if responseCh == ch {
					delete(s.byPublicPlaybackRef, publicPlaybackRef)
				}
			}
			topicIdleSessionID := ""
			if idleSessionID, ok := s.topicByTTS[sessionID]; ok {
				delete(s.topicByTTS, sessionID)
				delete(s.topicPlaybackByTTS, sessionID)
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
	s.byPublicPlaybackRef = map[string]chan struct{}{}
	s.topicGate = map[string]chan struct{}{}
	s.topicByTTS = map[string]string{}
	s.playbackBySession = map[string]PlaybackGenerationIdentity{}
	s.topicPlaybackByTTS = map[string]PlaybackGenerationIdentity{}
	s.cancelledByRuntime = map[string]uint64{}
	s.mu.Unlock()

	for _, ch := range pending {
		close(ch)
	}
	for _, ch := range topicGates {
		close(ch)
	}
	return sessionIDs
}

// CancelPlaybackGeneration releases pending waits for a stopped IdleChat
// generation while preserving waits registered by newer generations.
func (s *PendingPlaybackStore) CancelPlaybackGeneration(runtimeEpoch string, playbackGeneration uint64) []string {
	if s == nil {
		return nil
	}
	runtimeEpoch = strings.TrimSpace(runtimeEpoch)
	if runtimeEpoch == "" || playbackGeneration == 0 {
		return nil
	}
	s.mu.Lock()
	if playbackGeneration > s.cancelledByRuntime[runtimeEpoch] {
		s.cancelledByRuntime[runtimeEpoch] = playbackGeneration
	}
	pendingChannels := make(map[chan struct{}]struct{})
	topicChannels := make(map[chan struct{}]struct{})
	sessions := make([]string, 0)
	for sessionID, identity := range s.playbackBySession {
		if identity.RuntimeEpoch != runtimeEpoch || identity.PlaybackGeneration > playbackGeneration {
			continue
		}
		ch := s.pending[sessionID]
		if ch != nil {
			pendingChannels[ch] = struct{}{}
			for ref, response := range s.byPublicPlaybackRef {
				if response == ch {
					delete(s.byPublicPlaybackRef, ref)
				}
			}
		}
		delete(s.pending, sessionID)
		delete(s.playbackBySession, sessionID)
		sessions = append(sessions, sessionID)
	}
	for ttsSessionID, identity := range s.topicPlaybackByTTS {
		if identity.RuntimeEpoch != runtimeEpoch || identity.PlaybackGeneration > playbackGeneration {
			continue
		}
		idleSessionID := s.topicByTTS[ttsSessionID]
		delete(s.topicPlaybackByTTS, ttsSessionID)
		delete(s.topicByTTS, ttsSessionID)
		if idleSessionID == "" || s.topicSessionHasOwnerLocked(idleSessionID) {
			continue
		}
		if ch := s.topicGate[idleSessionID]; ch != nil {
			topicChannels[ch] = struct{}{}
			delete(s.topicGate, idleSessionID)
		}
	}
	s.mu.Unlock()
	for ch := range pendingChannels {
		close(ch)
	}
	for ch := range topicChannels {
		close(ch)
	}
	return sessions
}

func (s *PendingPlaybackStore) playbackGenerationCancelledLocked(identity PlaybackGenerationIdentity) bool {
	return identity.PlaybackGeneration <= s.cancelledByRuntime[identity.RuntimeEpoch]
}

func (s *PendingPlaybackStore) topicSessionHasOwnerLocked(idleSessionID string) bool {
	for _, current := range s.topicByTTS {
		if current == idleSessionID {
			return true
		}
	}
	return false
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
	publicPlaybackRefs := make([]string, 0, len(s.byPublicPlaybackRef))
	for publicPlaybackRef := range s.byPublicPlaybackRef {
		publicPlaybackRefs = append(publicPlaybackRefs, publicPlaybackRef)
	}
	return BuildPendingPlaybackSnapshot(sessionIDs, publicPlaybackRefs, len(s.topicGate), len(s.topicByTTS))
}

func closePendingPlaybackChannels(closePending bool, pendingCh chan struct{}, closeTopic bool, topicCh chan struct{}) {
	if closePending && pendingCh != nil {
		close(pendingCh)
	}
	if closeTopic && topicCh != nil {
		close(topicCh)
	}
}
