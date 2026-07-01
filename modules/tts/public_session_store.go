package tts

import (
	"strings"
	"sync"
)

type PublicSessionStore struct {
	mu           sync.Mutex
	routes       map[string]*PublicSessionRoute
	stale        map[string]uint64
	nextChunk    map[string]int
	nextResponse map[string]int
	generation   uint64
}

func NewPublicSessionStore() *PublicSessionStore {
	return &PublicSessionStore{
		routes:       map[string]*PublicSessionRoute{},
		stale:        map[string]uint64{},
		nextChunk:    map[string]int{},
		nextResponse: map[string]int{},
	}
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
	s.nextResponse = map[string]int{}
	s.generation = 0
}

func (s *PublicSessionStore) Register(reg PublicSessionRouteRegistration) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	reg.InternalSessionID = strings.TrimSpace(reg.InternalSessionID)
	reg.Generation = s.generation
	route, ok := NewPublicSessionRoute(reg)
	if !ok {
		return false
	}
	delete(s.stale, reg.InternalSessionID)
	s.routes[reg.InternalSessionID] = &route
	return true
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
	s.nextResponse = map[string]int{}
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

func (s *PublicSessionStore) ResolveResponse(internalSessionID string) string {
	if s == nil {
		return ""
	}
	internalSessionID = strings.TrimSpace(internalSessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if route := s.routes[internalSessionID]; route != nil {
		return route.Response()
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

func (s *PublicSessionStore) ClearByResponse(responseID string) {
	if s == nil {
		return
	}
	responseID = strings.TrimSpace(responseID)
	if responseID == "" {
		return
	}
	s.mu.Lock()
	for internalSessionID, route := range s.routes {
		if route != nil && route.Response() == responseID {
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
	s.nextResponse = map[string]int{}
}

func (s *PublicSessionStore) NextResponseID(publicSessionID string) string {
	if s == nil || strings.TrimSpace(publicSessionID) == "" {
		return ""
	}
	publicSessionID = strings.TrimSpace(publicSessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	resolved := ResolveNextPublicResponseID(publicSessionID, s.nextResponse[publicSessionID])
	if resolved.Advance {
		s.nextResponse[publicSessionID] = resolved.NextResponseNumber
	}
	return resolved.ResponseID
}

func (s *PublicSessionStore) NextResponseIDForMessage(publicSessionID, messageID string) string {
	if s == nil || strings.TrimSpace(publicSessionID) == "" {
		return ""
	}
	publicSessionID = strings.TrimSpace(publicSessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	resolved := ResolvePublicResponseIDForMessage(publicSessionID, messageID, s.nextResponse[publicSessionID])
	if resolved.Advance && s.nextResponse[publicSessionID] < resolved.NextResponseNumber {
		s.nextResponse[publicSessionID] = resolved.NextResponseNumber
	}
	return resolved.ResponseID
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
	return BuildPublicPlaybackSnapshot(currentRoutes, staleRoutes, len(s.nextChunk), len(s.nextResponse))
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
