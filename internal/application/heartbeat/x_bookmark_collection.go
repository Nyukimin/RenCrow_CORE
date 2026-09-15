package heartbeat

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"
)

type XBookmarkCollectionReport struct {
	Collected       int
	Imported        int
	ExternalFetched int
	Summarized      int
	SummaryBlocked  int
	SummaryFailed   int
	SummaryReused   int
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

		report, err := s.runXBookmarkCollection(ctx)
		if err != nil {
			message := fmt.Sprintf("collection failed: %v", err)
			log.Printf("[Heartbeat] X Bookmark %s", message)
			s.emitEvent("heartbeat.x_bookmarks.error", message)
			return
		}
		message := fmt.Sprintf("collected=%d imported=%d external_fetched=%d summarized=%d summary_blocked=%d summary_failed=%d summary_reused=%d", report.Collected, report.Imported, report.ExternalFetched, report.Summarized, report.SummaryBlocked, report.SummaryFailed, report.SummaryReused)
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
	s.cancelXBookmarkCollection()
	s.waitForXBookmarkCollection()
}

func (s *HeartbeatService) cancelXBookmarkCollection() {
	if s == nil {
		return
	}
	s.xBookmarkMu.Lock()
	cancel := s.xBookmarkCancel
	s.xBookmarkMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *HeartbeatService) runXBookmarkCollection(ctx context.Context) (XBookmarkCollectionReport, error) {
	if s == nil || s.xBookmarkCollector == nil {
		return XBookmarkCollectionReport{}, errors.New("X Bookmark collector unavailable")
	}
	release, err := s.acquireCollectionWorker(ctx)
	if err != nil {
		return XBookmarkCollectionReport{}, err
	}
	defer release()
	workerCtx, _, finish, err := s.beginWorker(ctx, "X Bookmarkの外部記事を取得し本文から要約する", "x-bookmarks", "scheduled")
	if err != nil {
		return XBookmarkCollectionReport{}, err
	}
	report, err := s.xBookmarkCollector.Collect(workerCtx)
	if err == nil && report.SummaryFailed > 0 {
		err = fmt.Errorf("external link summaries failed: %d; source records retained", report.SummaryFailed)
	}
	return report, errors.Join(err, finish(err))
}
