package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

type profilePromotionListerStub struct {
	jobs []domainmemory.ProfilePromotionJob
}

func (s profilePromotionListerStub) ListProfilePromotionJobs(context.Context, int) ([]domainmemory.ProfilePromotionJob, error) {
	return s.jobs, nil
}

func (s profilePromotionListerStub) ProfilePromotionDiagnostics(context.Context) (domainmemory.ProfilePromotionDiagnostics, error) {
	report := domainmemory.ProfilePromotionDiagnostics{StateCounts: map[string]int{}}
	for _, job := range s.jobs {
		report.StateCounts[job.State]++
		if job.State == domainmemory.ProfilePromotionFailed {
			report.FailedCount++
		}
	}
	return report, nil
}

type profilePromotionDiagnosticsStub struct {
	profilePromotionListerStub
	report domainmemory.ProfilePromotionDiagnostics
	retry  domainmemory.ProfilePromotionRetryResult
}

func (s profilePromotionDiagnosticsStub) ProfilePromotionDiagnostics(context.Context) (domainmemory.ProfilePromotionDiagnostics, error) {
	return s.report, nil
}

func (s profilePromotionDiagnosticsStub) RetryFailedProfilePromotionJobs(context.Context, time.Time) (domainmemory.ProfilePromotionRetryResult, error) {
	return s.retry, nil
}

func TestHandleMemoryProfilePromotionsExposesFailedJobs(t *testing.T) {
	handler := HandleMemoryProfilePromotions(profilePromotionListerStub{jobs: []domainmemory.ProfilePromotionJob{
		{EvidenceEventID: "evt-1", State: domainmemory.ProfilePromotionPending},
		{EvidenceEventID: "evt-2", State: domainmemory.ProfilePromotionFailed, AttemptCount: 5, LastError: "bad json"},
	}})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/memory/profile-promotions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Status string                             `json:"status"`
		Failed int                                `json:"failed_count"`
		Jobs   []domainmemory.ProfilePromotionJob `json:"jobs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "needs_review" || body.Failed != 1 || len(body.Jobs) != 2 {
		t.Fatalf("body=%+v", body)
	}
}

func TestHandleMemoryProfilePromotionsUsesAllRowDiagnostics(t *testing.T) {
	handler := HandleMemoryProfilePromotions(profilePromotionDiagnosticsStub{
		profilePromotionListerStub: profilePromotionListerStub{jobs: []domainmemory.ProfilePromotionJob{{EvidenceEventID: "page-1", State: domainmemory.ProfilePromotionFailed}}},
		report: domainmemory.ProfilePromotionDiagnostics{
			StateCounts:                map[string]int{domainmemory.ProfilePromotionPending: 3, domainmemory.ProfilePromotionFailed: 1222},
			FailedCount:                1222,
			RetryableFailedCount:       1191,
			MissingEvidenceFailedCount: 31,
			DBPoolStats:                domainmemory.L1DBPoolStats{Max: 1, Open: 1, InUse: 0, Idle: 1, PoolWaitCount: 7, PoolWaitDurationMS: 42},
		},
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/memory/profile-promotions?limit=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Jobs                       []domainmemory.ProfilePromotionJob `json:"jobs"`
		StateCounts                map[string]int                     `json:"state_counts"`
		FailedCount                int                                `json:"failed_count"`
		RetryableFailedCount       int                                `json:"retryable_failed_count"`
		MissingEvidenceFailedCount int                                `json:"missing_evidence_failed_count"`
		DBPoolStats                domainmemory.L1DBPoolStats         `json:"db_pool_stats"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Jobs) != 1 || body.StateCounts[domainmemory.ProfilePromotionFailed] != 1222 || body.FailedCount != 1222 || body.RetryableFailedCount != 1191 || body.MissingEvidenceFailedCount != 31 {
		t.Fatalf("body diagnostics=%+v", body)
	}
	if body.DBPoolStats.Max != 1 || body.DBPoolStats.PoolWaitCount != 7 || body.DBPoolStats.PoolWaitDurationMS != 42 {
		t.Fatalf("body pool stats=%+v", body.DBPoolStats)
	}
}

func TestHandleMemoryProfilePromotionRetry(t *testing.T) {
	store := profilePromotionDiagnosticsStub{retry: domainmemory.ProfilePromotionRetryResult{RequeuedCount: 1191, MissingEvidenceCount: 31}}
	handler := HandleMemoryProfilePromotionRetry(store)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/viewer/memory/profile-promotions/retry", nil)
	req.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	req.Header.Set("X-RenCrow-Interaction-Profile", "cmd-control")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body domainmemory.ProfilePromotionRetryResult
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.RequeuedCount != 1191 || body.MissingEvidenceCount != 31 {
		t.Fatalf("retry body=%+v", body)
	}
	for name, headers := range map[string][2]string{
		"missing profile": {"", ""},
		"spoofed client":  {"RenCrow_PORTAL", "cmd-control"},
		"other profile":   {"RenCrow_CMD", "cmd-chat"},
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/viewer/memory/profile-promotions/retry", nil)
			req.Header.Set("X-RenCrow-Client", headers[0])
			req.Header.Set("X-RenCrow-Interaction-Profile", headers[1])
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status=%d want=%d", rec.Code, http.StatusForbidden)
			}
		})
	}
	get := httptest.NewRecorder()
	getReq := httptest.NewRequest(http.MethodGet, "/viewer/memory/profile-promotions/retry", nil)
	getReq.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	getReq.Header.Set("X-RenCrow-Interaction-Profile", "cmd-control")
	handler.ServeHTTP(get, getReq)
	if get.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status=%d want=%d", get.Code, http.StatusMethodNotAllowed)
	}
}
