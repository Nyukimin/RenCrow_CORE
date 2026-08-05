package heartbeat

import (
	"context"
	"fmt"
	"log"
	"time"
)

type XBookmarkCollectionReport struct {
	Collected       int
	Imported        int
	ExternalFetched int
}

type XBookmarkCollector interface {
	Collect(ctx context.Context) (XBookmarkCollectionReport, error)
}

func (s *HeartbeatService) WithXBookmarkCollection(collector XBookmarkCollector, interval, timeout time.Duration, runOnStart bool) *HeartbeatService {
	s.xBookmarkCollector = collector
	s.xBookmarkInterval = interval
	s.xBookmarkTimeout = timeout
	s.xBookmarkRunOnStart = runOnStart
	return s
}

func (s *HeartbeatService) startXBookmarkCollection() bool {
	if s == nil || s.xBookmarkCollector == nil {
		return false
	}
	s.xBookmarkMu.Lock()
	if s.xBookmarkRunning {
		s.xBookmarkMu.Unlock()
		s.emitEvent("heartbeat.x_bookmarks.skipped_running", "collection already running")
		log.Printf("[Heartbeat] X Bookmark collection skipped: already running")
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.xBookmarkTimeout)
	s.xBookmarkRunning = true
	s.xBookmarkCancel = cancel
	s.xBookmarkWG.Add(1)
	s.xBookmarkMu.Unlock()

	s.emitEvent("heartbeat.x_bookmarks.started", "collection started")
	log.Printf("[Heartbeat] X Bookmark collection started")
	go func() {
		defer s.xBookmarkWG.Done()
		defer func() {
			cancel()
			s.xBookmarkMu.Lock()
			s.xBookmarkRunning = false
			s.xBookmarkCancel = nil
			s.xBookmarkMu.Unlock()
		}()

		report, err := s.xBookmarkCollector.Collect(ctx)
		if err != nil {
			message := fmt.Sprintf("collection failed: %v", err)
			log.Printf("[Heartbeat] X Bookmark %s", message)
			s.emitEvent("heartbeat.x_bookmarks.error", message)
			return
		}
		message := fmt.Sprintf("collected=%d imported=%d external_fetched=%d", report.Collected, report.Imported, report.ExternalFetched)
		log.Printf("[Heartbeat] X Bookmark collection completed: %s", message)
		s.emitEvent("heartbeat.x_bookmarks.completed", message)
	}()
	return true
}

func (s *HeartbeatService) waitForXBookmarkCollection() {
	if s != nil {
		s.xBookmarkWG.Wait()
	}
}

func (s *HeartbeatService) stopXBookmarkCollection() {
	if s == nil {
		return
	}
	s.xBookmarkMu.Lock()
	cancel := s.xBookmarkCancel
	s.xBookmarkMu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.waitForXBookmarkCollection()
}
