package tts

import (
	"strings"
	"sync"
)

type PublicSessionStore struct {
	mu                                   sync.Mutex
	routes                               map[string]*PublicSessionRoute
	stale                                map[string]uint64
	nextChunk                            map[string]int
	nextPublicPlaybackRef                map[string]int
	cancelledPlaybackGenerationByRuntime map[string]uint64
	generation                           uint64
}

// PublicSessionRouteProof is a read-only snapshot proving that an IdleChat
// public playback route was current in one store generation. The owner must
// validate it again before registration and every audio read.
type PublicSessionRouteProof struct {
	InternalSessionID  string
	PublicSessionID    string
	RuntimeEpoch       string
	PlaybackGeneration uint64
	StoreGeneration    uint64
	Current            bool
}

func NewPublicSessionStore() *PublicSessionStore {
	return &PublicSessionStore{
		routes:                               map[string]*PublicSessionRoute{},
		stale:                                map[string]uint64{},
		nextChunk:                            map[string]int{},
		nextPublicPlaybackRef:                map[string]int{},
		cancelledPlaybackGenerationByRuntime: map[string]uint64{},
	}
}

func (s *PublicSessionStore) CurrentRouteProof(internalSessionID string) (PublicSessionRouteProof, bool) {
	if s == nil || internalSessionID == "" || strings.TrimSpace(internalSessionID) != internalSessionID {
		return PublicSessionRouteProof{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	route := s.routes[internalSessionID]
	if route == nil || !route.IsCurrent(s.generation) ||
		!IsIdleChatPublicSession(route.PublicSessionID) || route.RuntimeEpoch == "" || route.PlaybackGeneration == 0 {
		return PublicSessionRouteProof{}, false
	}
	return PublicSessionRouteProof{
		InternalSessionID:  internalSessionID,
		PublicSessionID:    route.PublicSessionID,
		RuntimeEpoch:       route.RuntimeEpoch,
		PlaybackGeneration: route.PlaybackGeneration,
		StoreGeneration:    s.generation,
		Current:            true,
	}, true
}

func (s *PublicSessionStore) ValidateCurrentRouteProof(proof PublicSessionRouteProof) bool {
	if s == nil || !proof.Current || proof.InternalSessionID == "" ||
		strings.TrimSpace(proof.InternalSessionID) != proof.InternalSessionID ||
		proof.PublicSessionID == "" || strings.TrimSpace(proof.PublicSessionID) != proof.PublicSessionID ||
		!IsIdleChatPublicSession(proof.PublicSessionID) ||
		proof.RuntimeEpoch == "" || strings.TrimSpace(proof.RuntimeEpoch) != proof.RuntimeEpoch ||
		proof.PlaybackGeneration == 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if proof.StoreGeneration != s.generation {
		return false
	}
	route := s.routes[proof.InternalSessionID]
	return route != nil && route.IsCurrent(s.generation) &&
		route.PublicSessionID == proof.PublicSessionID &&
		route.RuntimeEpoch == proof.RuntimeEpoch &&
		route.PlaybackGeneration == proof.PlaybackGeneration
}

func (s *PublicSessionStore) ResetAll() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes = map[string]*PublicSessionRoute{}
	s.stale = map[string]uint64{}
	s.nextChunk = map[string]int{}
	s.nextPublicPlaybackRef = map[string]int{}
	s.cancelledPlaybackGenerationByRuntime = map[string]uint64{}
	s.generation++
}

func (s *PublicSessionStore) Register(reg PublicSessionRouteRegistration) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	reg.InternalSessionID = strings.TrimSpace(reg.InternalSessionID)
	reg.RuntimeEpoch = strings.TrimSpace(reg.RuntimeEpoch)
	if reg.RuntimeEpoch != "" && reg.PlaybackGeneration > 0 && reg.PlaybackGeneration <= s.cancelledPlaybackGenerationByRuntime[reg.RuntimeEpoch] {
		s.stale[reg.InternalSessionID] = s.generation
		delete(s.routes, reg.InternalSessionID)
		return false
	}
	reg.Generation = s.generation
	route, ok := NewPublicSessionRoute(reg)
	if !ok {
		return false
	}
	delete(s.stale, reg.InternalSessionID)
	s.routes[reg.InternalSessionID] = &route
	return true
}

// CancelPlaybackGeneration retires routes owned by a stopped IdleChat
// generation and rejects late registrations for that generation.
func (s *PublicSessionStore) CancelPlaybackGeneration(runtimeEpoch string, playbackGeneration uint64) []string {
	if s == nil {
		return nil
	}
	runtimeEpoch = strings.TrimSpace(runtimeEpoch)
	if runtimeEpoch == "" || playbackGeneration == 0 {
		return nil
	}
	s.mu.Lock()
	if playbackGeneration > s.cancelledPlaybackGenerationByRuntime[runtimeEpoch] {
		s.cancelledPlaybackGenerationByRuntime[runtimeEpoch] = playbackGeneration
	}
	retired := make([]string, 0)
	for internalSessionID, route := range s.routes {
		if route == nil || route.RuntimeEpoch != runtimeEpoch || route.PlaybackGeneration == 0 || route.PlaybackGeneration > playbackGeneration {
			continue
		}
		s.stale[internalSessionID] = s.generation
		delete(s.routes, internalSessionID)
		retired = append(retired, internalSessionID)
	}
	s.mu.Unlock()
	return retired
}

func (s *PublicSessionStore) ResetForIdleChat() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.generation++
	for internalSessionID := range s.routes {
		s.stale[internalSessionID] = s.generation
	}
	s.routes = map[string]*PublicSessionRoute{}
	s.pruneStaleLocked()
	s.nextChunk = map[string]int{}
	s.nextPublicPlaybackRef = map[string]int{}
}

func (s *PublicSessionStore) IsStale(internalSessionID string) bool {
	if s == nil {
		return false
	}
	internalSessionID = strings.TrimSpace(internalSessionID)
	if internalSessionID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.stale[internalSessionID]; ok {
		return true
	}
	route := s.routes[internalSessionID]
	return route != nil && route.IsStale(s.generation)
}

func (s *PublicSessionStore) MarkTimedOut(publicSessionID, messageID string, turnIndex int, allForSession bool) []string {
	if s == nil || strings.TrimSpace(publicSessionID) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	matched := make([]string, 0, 1)
	for internalSessionID, route := range s.routes {
		if route == nil || !route.MatchesTimeout(publicSessionID, messageID, turnIndex, allForSession) {
			continue
		}
		route.TimedOut = true
		matched = append(matched, internalSessionID)
	}
	return matched
}

func (s *PublicSessionStore) MarkTimeout(input PlaybackTimeoutInput) PlaybackTimeoutConsumption {
	consumption := BuildPlaybackTimeoutConsumption(input, nil)
	if consumption.SessionID == "" {
		return consumption
	}
	matched := s.MarkTimedOut(consumption.SessionID, consumption.MessageID, consumption.TurnIndex, consumption.AllForSession)
	return BuildPlaybackTimeoutConsumption(input, matched)
}

func (s *PublicSessionStore) ResolveChunk(internalSessionID string, internalChunkIndex int) PublicChunkResolution {
	if s == nil {
		return ResolvePublicChunk(nil, internalSessionID, internalChunkIndex, 0)
	}
	internalSessionID = strings.TrimSpace(internalSessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	route := s.routes[internalSessionID]
	next := 0
	if route != nil {
		next = s.nextChunk[route.PublicSessionID]
	}
	resolved := ResolvePublicChunk(route, internalSessionID, internalChunkIndex, next)
	if resolved.Assigned {
		s.nextChunk[resolved.SessionID] = resolved.NextChunkNumber
	}
	return resolved
}

func (s *PublicSessionStore) ResolveSession(internalSessionID string) string {
	if s == nil {
		return strings.TrimSpace(internalSessionID)
	}
	internalSessionID = strings.TrimSpace(internalSessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if route := s.routes[internalSessionID]; route != nil {
		return route.PublicSessionOrFallback(internalSessionID)
	}
	return internalSessionID
}

func (s *PublicSessionStore) ResolvePublicPlaybackRef(internalSessionID string) string {
	if s == nil {
		return ""
	}
	internalSessionID = strings.TrimSpace(internalSessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if route := s.routes[internalSessionID]; route != nil {
		return route.PublicPlaybackRefValue()
	}
	return ""
}

func (s *PublicSessionStore) ResolveMessage(internalSessionID string) (string, int, string) {
	if s == nil {
		return "", 0, ""
	}
	internalSessionID = strings.TrimSpace(internalSessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if route := s.routes[internalSessionID]; route != nil {
		return route.Message()
	}
	return "", 0, ""
}

func (s *PublicSessionStore) Clear(internalSessionID string) {
	if s == nil {
		return
	}
	internalSessionID = strings.TrimSpace(internalSessionID)
	if internalSessionID == "" {
		return
	}
	s.mu.Lock()
	delete(s.routes, internalSessionID)
	delete(s.stale, internalSessionID)
	s.mu.Unlock()
}

// Retire removes a route while retaining a stale marker so late chunk or
// completion callbacks cannot recreate an already-cancelled session.
func (s *PublicSessionStore) Retire(internalSessionID string) {
	if s == nil {
		return
	}
	internalSessionID = strings.TrimSpace(internalSessionID)
	if internalSessionID == "" {
		return
	}
	s.mu.Lock()
	s.stale[internalSessionID] = s.generation
	delete(s.routes, internalSessionID)
	s.mu.Unlock()
}

func (s *PublicSessionStore) ClearByPublicPlaybackRef(publicPlaybackRef string) {
	if s == nil {
		return
	}
	publicPlaybackRef = strings.TrimSpace(publicPlaybackRef)
	if publicPlaybackRef == "" {
		return
	}
	s.mu.Lock()
	for internalSessionID, route := range s.routes {
		if route != nil && route.PublicPlaybackRefValue() == publicPlaybackRef {
			delete(s.routes, internalSessionID)
			break
		}
	}
	s.mu.Unlock()
}

// RetireByPublicPlaybackRef removes a response route while retaining its stale marker.
func (s *PublicSessionStore) RetireByPublicPlaybackRef(publicPlaybackRef string) {
	if s == nil {
		return
	}
	publicPlaybackRef = strings.TrimSpace(publicPlaybackRef)
	if publicPlaybackRef == "" {
		return
	}
	s.mu.Lock()
	for internalSessionID, route := range s.routes {
		if route != nil && route.PublicPlaybackRefValue() == publicPlaybackRef {
			s.stale[internalSessionID] = s.generation
			delete(s.routes, internalSessionID)
			break
		}
	}
	s.mu.Unlock()
}

func (s *PublicSessionStore) ClearSequencesIfNoRoutes() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, route := range s.routes {
		if route != nil && route.IsCurrent(s.generation) {
			return
		}
	}
	s.nextChunk = map[string]int{}
	s.nextPublicPlaybackRef = map[string]int{}
}

func (s *PublicSessionStore) NextPublicPlaybackRef(publicSessionID string) string {
	if s == nil || strings.TrimSpace(publicSessionID) == "" {
		return ""
	}
	publicSessionID = strings.TrimSpace(publicSessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	resolved := ResolveNextPublicPlaybackRef(publicSessionID, s.nextPublicPlaybackRef[publicSessionID])
	if resolved.Advance {
		s.nextPublicPlaybackRef[publicSessionID] = resolved.NextPublicPlaybackRefNumber
	}
	return resolved.PublicPlaybackRef
}

func (s *PublicSessionStore) NextPublicPlaybackRefForMessage(publicSessionID, messageID string) string {
	if s == nil || strings.TrimSpace(publicSessionID) == "" {
		return ""
	}
	publicSessionID = strings.TrimSpace(publicSessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	resolved := ResolvePublicPlaybackRefForMessage(publicSessionID, messageID, s.nextPublicPlaybackRef[publicSessionID])
	if resolved.Advance && s.nextPublicPlaybackRef[publicSessionID] < resolved.NextPublicPlaybackRefNumber {
		s.nextPublicPlaybackRef[publicSessionID] = resolved.NextPublicPlaybackRefNumber
	}
	return resolved.PublicPlaybackRef
}

func (s *PublicSessionStore) Snapshot() PublicPlaybackSnapshot {
	if s == nil {
		return BuildPublicPlaybackSnapshot(0, 0, 0, 0)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	currentRoutes := 0
	staleRoutes := 0
	for _, route := range s.routes {
		if route == nil || route.IsStale(s.generation) {
			staleRoutes++
			continue
		}
		currentRoutes++
	}
	return BuildPublicPlaybackSnapshot(currentRoutes, staleRoutes, len(s.nextChunk), len(s.nextPublicPlaybackRef))
}

func (s *PublicSessionStore) pruneStaleLocked() {
	if s.generation <= 2 {
		return
	}
	minGeneration := s.generation - 2
	for internalSessionID, generation := range s.stale {
		if generation < minGeneration {
			delete(s.stale, internalSessionID)
		}
	}
}
