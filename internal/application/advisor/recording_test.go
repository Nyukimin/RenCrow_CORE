package advisor

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	advisorDomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/advisor"
)

type recordingInner struct {
	result advisorDomain.AdviceResult
	err    error
}

func (s recordingInner) RequestAdvice(context.Context, advisorDomain.AdviceRequest) (advisorDomain.AdviceResult, error) {
	return s.result, s.err
}

type recordingStore struct {
	runs      []advisorDomain.AdviceRunRecord
	adoptions []advisorDomain.AdvisorAdoptionRecord
	snapshots []advisorDomain.AdvisorScoreSnapshot
	err       error
}

func (s *recordingStore) SaveAdviceRun(_ context.Context, item advisorDomain.AdviceRunRecord) error {
	s.runs = append(s.runs, item)
	return s.err
}

func (s *recordingStore) ListAdviceRuns(context.Context, int) ([]advisorDomain.AdviceRunRecord, error) {
	return append([]advisorDomain.AdviceRunRecord(nil), s.runs...), s.err
}

func (s *recordingStore) SaveAdvisorAdoption(_ context.Context, item advisorDomain.AdvisorAdoptionRecord) error {
	s.adoptions = append(s.adoptions, item)
	return s.err
}

func (s *recordingStore) ListAdvisorAdoptions(context.Context, int) ([]advisorDomain.AdvisorAdoptionRecord, error) {
	return append([]advisorDomain.AdvisorAdoptionRecord(nil), s.adoptions...), s.err
}

func (s *recordingStore) SaveAdvisorScoreSnapshot(_ context.Context, item advisorDomain.AdvisorScoreSnapshot) error {
	s.snapshots = append(s.snapshots, item)
	return s.err
}

func (s *recordingStore) ListAdvisorScoreSnapshots(context.Context, int) ([]advisorDomain.AdvisorScoreSnapshot, error) {
	return append([]advisorDomain.AdvisorScoreSnapshot(nil), s.snapshots...), s.err
}

func TestRecordingServiceStoresHashesWithoutPromptBody(t *testing.T) {
	started := time.Date(2026, 7, 14, 1, 2, 3, 0, time.UTC)
	store := &recordingStore{}
	service := NewRecordingService(recordingInner{result: advisorDomain.AdviceResult{
		RequestID:   "req-1",
		AdvisorID:   advisorDomain.AdvisorCodex,
		Status:      advisorDomain.StatusCompleted,
		Summary:     "safe summary",
		StartedAt:   started,
		CompletedAt: started.Add(1500 * time.Millisecond),
	}}, store, func() time.Time { return started.Add(2 * time.Second) })

	result, err := service.RequestAdvice(context.Background(), advisorDomain.AdviceRequest{
		ID:               "req-1",
		TaskID:           "task-1",
		RequestedByAgent: "shiro",
		AdvisorID:        advisorDomain.AdvisorCodex,
		Purpose:          "review",
		Prompt:           "secret prompt body",
		RiskClass:        "low",
		ApprovalMode:     "advice_only",
	})
	if err != nil {
		t.Fatalf("RequestAdvice failed: %v", err)
	}
	if result.Summary != "safe summary" || len(store.runs) != 1 {
		t.Fatalf("unexpected result=%#v runs=%#v", result, store.runs)
	}
	run := store.runs[0]
	if run.PromptHash == "" || run.PromptHash == "secret prompt body" {
		t.Fatalf("prompt hash was not recorded safely: %q", run.PromptHash)
	}
	if run.OutputHash == "" || run.LatencyMillis != 1500 {
		t.Fatalf("output hash or latency missing: %#v", run)
	}
}

func TestRecordingServiceDegradesGracefullyWhenStoreFails(t *testing.T) {
	store := &recordingStore{err: errors.New("disk unavailable")}
	service := NewRecordingService(recordingInner{result: advisorDomain.AdviceResult{
		AdvisorID: advisorDomain.AdvisorCodex,
		Status:    advisorDomain.StatusCompleted,
		Summary:   "advice",
	}}, store, time.Now)

	result, err := service.RequestAdvice(context.Background(), advisorDomain.AdviceRequest{
		ID:               "req-1",
		RequestedByAgent: "shiro",
		AdvisorID:        advisorDomain.AdvisorCodex,
		Purpose:          "review",
		Prompt:           "prompt",
		ApprovalMode:     "advice_only",
	})
	if err != nil || result.Status != advisorDomain.StatusCompleted {
		t.Fatalf("store failure must not fail advice: result=%#v err=%v", result, err)
	}
	if service.LastStoreError() == nil {
		t.Fatal("expected store error to remain observable")
	}
}

func TestBuildScoreSnapshotUsesSpecifiedFormula(t *testing.T) {
	start := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	runs := []advisorDomain.AdviceRunRecord{
		{RunID: "run-1", AdvisorID: advisorDomain.AdvisorCodex, Status: advisorDomain.AdviceStatus(advisorDomain.StatusCompleted), LatencyMillis: 1000, StartedAt: start},
		{RunID: "run-2", AdvisorID: advisorDomain.AdvisorCodex, Status: advisorDomain.AdviceStatus(advisorDomain.StatusCompleted), LatencyMillis: 1000, StartedAt: start.Add(time.Hour)},
	}
	adoptions := []advisorDomain.AdvisorAdoptionRecord{
		{RunID: "run-1", AdvisorID: advisorDomain.AdvisorCodex, Adopted: true, Outcome: "success", RevisionCount: 1},
	}
	snapshot := BuildScoreSnapshot(runs, adoptions, start, start.Add(24*time.Hour))
	if snapshot.RequestCount != 2 || snapshot.CompletedCount != 2 || snapshot.AdoptedCount != 1 || snapshot.SuccessCount != 1 {
		t.Fatalf("unexpected counts: %#v", snapshot)
	}
	const want = 0.8798333333333334
	if math.Abs(snapshot.Score-want) > 0.000001 {
		t.Fatalf("score=%f, want=%f", snapshot.Score, want)
	}
}
