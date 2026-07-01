package tts

import (
	"strings"
	"sync"
	"time"
)

const (
	ViewerActiveKindAudio = "audio"
	ViewerActiveKindInput = "input"
)

type ViewerActiveControlSnapshot struct {
	ActiveAudioViewerID string `json:"active_audio_viewer_id"`
	ActiveInputViewerID string `json:"active_input_viewer_id"`
}

type ViewerActiveControlStore struct {
	mu                  sync.RWMutex
	activeAudioViewerID string
	activeInputViewerID string
	activeAudioUpdated  time.Time
	activeInputUpdated  time.Time
	ownerTTL            time.Duration
}

func NewViewerActiveControlStore(ownerTTL time.Duration) *ViewerActiveControlStore {
	return &ViewerActiveControlStore{ownerTTL: ownerTTL}
}

func (s *ViewerActiveControlStore) Claim(kind, viewerClientID string) ViewerActiveControlSnapshot {
	if s == nil {
		return ViewerActiveControlSnapshot{}
	}
	id := strings.TrimSpace(viewerClientID)
	kind = strings.TrimSpace(kind)
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredLocked(now)
	switch kind {
	case ViewerActiveKindAudio:
		s.activeAudioViewerID = id
		s.activeAudioUpdated = now
	case ViewerActiveKindInput:
		s.activeInputViewerID = id
		s.activeInputUpdated = now
	}
	return s.snapshotLocked()
}

func (s *ViewerActiveControlStore) Heartbeat(kind, viewerClientID string) ViewerActiveControlSnapshot {
	if s == nil {
		return ViewerActiveControlSnapshot{}
	}
	id := strings.TrimSpace(viewerClientID)
	kind = strings.TrimSpace(kind)
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredLocked(now)
	switch kind {
	case ViewerActiveKindAudio:
		if id != "" && s.activeAudioViewerID == id {
			s.activeAudioUpdated = now
		}
	case ViewerActiveKindInput:
		if id != "" && s.activeInputViewerID == id {
			s.activeInputUpdated = now
		}
	}
	return s.snapshotLocked()
}

func (s *ViewerActiveControlStore) Release(kind, viewerClientID string) ViewerActiveControlSnapshot {
	if s == nil {
		return ViewerActiveControlSnapshot{}
	}
	id := strings.TrimSpace(viewerClientID)
	kind = strings.TrimSpace(kind)
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredLocked(now)
	switch kind {
	case ViewerActiveKindAudio:
		if id != "" && s.activeAudioViewerID == id {
			s.activeAudioViewerID = ""
			s.activeAudioUpdated = time.Time{}
		}
	case ViewerActiveKindInput:
		if id != "" && s.activeInputViewerID == id {
			s.activeInputViewerID = ""
			s.activeInputUpdated = time.Time{}
		}
	}
	return s.snapshotLocked()
}

func (s *ViewerActiveControlStore) IsActiveAudio(viewerClientID string) bool {
	if s == nil {
		return false
	}
	id := strings.TrimSpace(viewerClientID)
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredLocked(now)
	return id != "" && s.activeAudioViewerID != "" && s.activeAudioViewerID == id
}

func (s *ViewerActiveControlStore) Snapshot() ViewerActiveControlSnapshot {
	if s == nil {
		return ViewerActiveControlSnapshot{}
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredLocked(now)
	return s.snapshotLocked()
}

func (s *ViewerActiveControlStore) pruneExpiredLocked(now time.Time) {
	if s.ownerTTL <= 0 || now.IsZero() {
		return
	}
	if s.activeAudioViewerID != "" && !s.activeAudioUpdated.IsZero() && now.Sub(s.activeAudioUpdated) > s.ownerTTL {
		s.activeAudioViewerID = ""
		s.activeAudioUpdated = time.Time{}
	}
	if s.activeInputViewerID != "" && !s.activeInputUpdated.IsZero() && now.Sub(s.activeInputUpdated) > s.ownerTTL {
		s.activeInputViewerID = ""
		s.activeInputUpdated = time.Time{}
	}
}

func (s *ViewerActiveControlStore) snapshotLocked() ViewerActiveControlSnapshot {
	return ViewerActiveControlSnapshot{
		ActiveAudioViewerID: s.activeAudioViewerID,
		ActiveInputViewerID: s.activeInputViewerID,
	}
}
