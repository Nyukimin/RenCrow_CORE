package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

type profilePromotionListerStub struct {
	jobs []domainmemory.ProfilePromotionJob
}

func (s profilePromotionListerStub) ListProfilePromotionJobs(context.Context, int) ([]domainmemory.ProfilePromotionJob, error) {
	return s.jobs, nil
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
