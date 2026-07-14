package advisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	advisorDomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/advisor"
)

type AdviceRequester interface {
	RequestAdvice(ctx context.Context, req advisorDomain.AdviceRequest) (advisorDomain.AdviceResult, error)
}

type Store interface {
	SaveAdviceRun(ctx context.Context, item advisorDomain.AdviceRunRecord) error
	ListAdviceRuns(ctx context.Context, limit int) ([]advisorDomain.AdviceRunRecord, error)
	SaveAdvisorAdoption(ctx context.Context, item advisorDomain.AdvisorAdoptionRecord) error
	ListAdvisorAdoptions(ctx context.Context, limit int) ([]advisorDomain.AdvisorAdoptionRecord, error)
	SaveAdvisorScoreSnapshot(ctx context.Context, item advisorDomain.AdvisorScoreSnapshot) error
	ListAdvisorScoreSnapshots(ctx context.Context, limit int) ([]advisorDomain.AdvisorScoreSnapshot, error)
}

type RecordingService struct {
	inner AdviceRequester
	store Store
	now   func() time.Time
	mu    sync.RWMutex
	last  error
}

func NewRecordingService(inner AdviceRequester, store Store, now func() time.Time) *RecordingService {
	if now == nil {
		now = time.Now
	}
	return &RecordingService{inner: inner, store: store, now: now}
}

func (s *RecordingService) RequestAdvice(ctx context.Context, req advisorDomain.AdviceRequest) (advisorDomain.AdviceResult, error) {
	if s == nil || s.inner == nil {
		return advisorDomain.AdviceResult{}, context.Canceled
	}
	callStarted := s.now().UTC()
	result, callErr := s.inner.RequestAdvice(ctx, req)
	finished := s.now().UTC()
	started := result.StartedAt
	if started.IsZero() {
		started = callStarted
	}
	if !result.CompletedAt.IsZero() {
		finished = result.CompletedAt
	}
	status := strings.TrimSpace(result.Status)
	if status == "" {
		if callErr != nil {
			status = advisorDomain.StatusFailed
		} else {
			status = advisorDomain.StatusCompleted
		}
	}
	approvalMode := strings.TrimSpace(req.ApprovalMode)
	if approvalMode == "" {
		approvalMode = "advice_only"
	}
	record := advisorDomain.AdviceRunRecord{
		RunID:            uuid.NewString(),
		RequestID:        req.ID,
		TaskID:           req.TaskID,
		RequestedByAgent: req.RequestedByAgent,
		AdvisorID:        req.AdvisorID,
		Purpose:          req.Purpose,
		PromptHash:       hashText(req.Prompt),
		RiskClass:        req.RiskClass,
		ApprovalMode:     approvalMode,
		Status:           advisorDomain.AdviceStatus(status),
		Summary:          boundedSummary(result.Summary, 512),
		OutputHash:       hashText(result.OutputText()),
		StartedAt:        started,
		FinishedAt:       finished,
		LatencyMillis:    max(0, finished.Sub(started).Milliseconds()),
	}
	if callErr != nil {
		record.Error = boundedSummary(callErr.Error(), 512)
	}
	if s.store != nil {
		if err := record.Validate(); err == nil {
			if err := s.store.SaveAdviceRun(ctx, record); err != nil {
				s.setStoreError(err)
				log.Printf("WARN: failed to record advisor run: %v", err)
			} else {
				s.setStoreError(nil)
			}
		} else {
			s.setStoreError(err)
			log.Printf("WARN: invalid advisor run record: %v", err)
		}
	}
	return result, callErr
}

func (s *RecordingService) LastStoreError() error {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.last
}

func (s *RecordingService) setStoreError(err error) {
	s.mu.Lock()
	s.last = err
	s.mu.Unlock()
}

func BuildScoreSnapshot(runs []advisorDomain.AdviceRunRecord, adoptions []advisorDomain.AdvisorAdoptionRecord, windowStart, windowEnd time.Time) advisorDomain.AdvisorScoreSnapshot {
	filtered := make([]advisorDomain.AdviceRunRecord, 0, len(runs))
	for _, run := range runs {
		if !windowStart.IsZero() && run.StartedAt.Before(windowStart) {
			continue
		}
		if !windowEnd.IsZero() && run.StartedAt.After(windowEnd) {
			continue
		}
		filtered = append(filtered, run)
	}
	snapshot := advisorDomain.AdvisorScoreSnapshot{
		SnapshotID:   uuid.NewString(),
		WindowStart:  windowStart,
		WindowEnd:    windowEnd,
		CreatedAt:    time.Now().UTC(),
		RequestCount: len(filtered),
	}
	if len(filtered) > 0 {
		snapshot.AdvisorID = filtered[0].AdvisorID
	}
	knownRuns := make(map[string]struct{}, len(filtered))
	var latencyTotal int64
	for _, run := range filtered {
		knownRuns[run.RunID] = struct{}{}
		latencyTotal += run.LatencyMillis
		switch string(run.Status) {
		case advisorDomain.StatusCompleted:
			snapshot.CompletedCount++
		case advisorDomain.StatusUnavailable:
			snapshot.UnavailableCount++
		case advisorDomain.StatusFailed, advisorDomain.StatusRejected:
			snapshot.FailedCount++
		}
	}
	if snapshot.RequestCount > 0 {
		snapshot.AvgLatencyMillis = latencyTotal / int64(snapshot.RequestCount)
	}
	var revisions int
	for _, adoption := range adoptions {
		if _, ok := knownRuns[adoption.RunID]; !ok || !adoption.Adopted {
			continue
		}
		snapshot.AdoptedCount++
		revisions += adoption.RevisionCount
		if adoption.Outcome == "success" {
			snapshot.SuccessCount++
		}
	}
	if snapshot.AdoptedCount > 0 {
		snapshot.AvgRevisionCount = float64(revisions) / float64(snapshot.AdoptedCount)
	}
	completionRate := ratio(snapshot.CompletedCount, snapshot.RequestCount)
	adoptionRate := ratio(snapshot.AdoptedCount, snapshot.CompletedCount)
	successRate := ratio(snapshot.SuccessCount, snapshot.AdoptedCount)
	latencyPenalty := math.Min(float64(snapshot.AvgLatencyMillis)/600000, 1) * 0.10
	snapshot.Score = 0.35*completionRate + 0.35*successRate + 0.20*adoptionRate + 0.10*math.Max(0, 1-snapshot.AvgRevisionCount/5) - latencyPenalty
	snapshot.Score = math.Max(0, math.Min(1, snapshot.Score))
	return snapshot
}

func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func hashText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func boundedSummary(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
