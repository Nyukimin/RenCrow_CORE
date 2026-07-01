package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	domainexecution "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/execution"
)

type evidenceStoreStub struct {
	items []domainexecution.ExecutionReport
	err   error
	limit int
}

func (s *evidenceStoreStub) ListRecent(_ context.Context, limit int) ([]domainexecution.ExecutionReport, error) {
	s.limit = limit
	if s.err != nil {
		return nil, s.err
	}
	return s.items, nil
}

func (s *evidenceStoreStub) GetByJobID(_ context.Context, jobID string) (domainexecution.ExecutionReport, error) {
	for _, item := range s.items {
		if item.JobID == jobID {
			return item, nil
		}
	}
	return domainexecution.ExecutionReport{}, context.Canceled
}

func (s *evidenceStoreStub) ListRecentUnique(_ context.Context, limit int) ([]domainexecution.ExecutionReport, error) {
	// For test purposes, just delegate to ListRecent (no duplicates in test data)
	return s.ListRecent(context.Background(), limit)
}

func (s *evidenceStoreStub) Summary(_ context.Context) (map[string]map[string]int, error) {
	out := map[string]map[string]int{
		"status": {
			"passed": 0,
			"failed": 0,
		},
		"error_kind": {
			"apply":  0,
			"verify": 0,
			"repair": 0,
			"none":   0,
		},
	}
	for _, it := range s.items {
		out["status"][it.Status]++
		k := it.ErrorKind
		if k == "" {
			k = "none"
		}
		out["error_kind"][k]++
	}
	return out, nil
}

func (s *evidenceStoreStub) SummaryUnique(ctx context.Context) (map[string]map[string]int, error) {
	// For test purposes, just delegate to Summary (no duplicates in test data)
	return s.Summary(ctx)
}

func TestHandleEvidenceRecent_Success(t *testing.T) {
	store := &evidenceStoreStub{items: []domainexecution.ExecutionReport{{
		JobID:      "job-1",
		Goal:       "TTS実装して",
		Status:     "passed",
		CreatedAt:  time.Now().UTC(),
		FinishedAt: time.Now().UTC(),
	}}}
	h := HandleEvidenceRecent(store)

	req := httptest.NewRequest(http.MethodGet, "/viewer/evidence/recent?limit=5", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if store.limit != 5 {
		t.Fatalf("expected limit=5, got %d", store.limit)
	}
	var out struct {
		Items []domainexecution.ExecutionReport `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(out.Items) != 1 || out.Items[0].JobID != "job-1" {
		t.Fatalf("unexpected items: %+v", out.Items)
	}
}

func TestHandleEvidenceRecent_InvalidLimit(t *testing.T) {
	store := &evidenceStoreStub{}
	h := HandleEvidenceRecent(store)

	req := httptest.NewRequest(http.MethodGet, "/viewer/evidence/recent?limit=bad", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestHandleEvidenceDetail_Success(t *testing.T) {
	store := &evidenceStoreStub{items: []domainexecution.ExecutionReport{{
		JobID:      "job-9",
		Goal:       "TTS実装して",
		Status:     "passed",
		CreatedAt:  time.Now().UTC(),
		FinishedAt: time.Now().UTC(),
	}}}
	h := HandleEvidenceDetail(store)

	req := httptest.NewRequest(http.MethodGet, "/viewer/evidence/detail?job_id=job-9", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestHandleEvidenceSummary_Success(t *testing.T) {
	store := &evidenceStoreStub{items: []domainexecution.ExecutionReport{
		{JobID: "j1", Status: "passed"},
		{JobID: "j2", Status: "failed", ErrorKind: "verify"},
	}}
	h := HandleEvidenceSummary(store)

	req := httptest.NewRequest(http.MethodGet, "/viewer/evidence/summary", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var out struct {
		Summary map[string]map[string]int `json:"summary"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if out.Summary["status"]["passed"] != 1 || out.Summary["status"]["failed"] != 1 {
		t.Fatalf("unexpected status summary: %+v", out.Summary)
	}
}
