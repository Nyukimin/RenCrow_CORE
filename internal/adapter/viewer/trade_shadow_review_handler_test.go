package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	applicationreview "github.com/Nyukimin/RenCrow_CORE/internal/application/tradeshadowreview"
)

type shadowReviewRunnerStub struct{ got applicationreview.Request }

func (stub *shadowReviewRunnerStub) Record(_ context.Context, request applicationreview.Request) (applicationreview.Result, error) {
	stub.got = request
	return applicationreview.Result{}, nil
}

func TestTradeShadowReviewRequiresExplicitRecordAndPreservesSafetyFlags(t *testing.T) {
	runner := &shadowReviewRunnerStub{}
	handler := HandleTradeShadowReview(TradeShadowReviewOptions{Enabled: true, Runner: runner})
	request := httptest.NewRequest(http.MethodPost, "/viewer/trade/shadow/outcomes/reviews", bytes.NewReader(validTradeShadowReviewViewerBody(true)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || runner.got.Review.StudyID != "study-1" || !runner.got.RequestAllowed {
		t.Fatalf("status=%d request=%+v body=%s", response.Code, runner.got, response.Body.String())
	}
	var projection tradeShadowReviewProjection
	if err := json.Unmarshal(response.Body.Bytes(), &projection); err != nil {
		t.Fatal(err)
	}
	if projection.Environment != "SHADOW" || projection.AuthorizesExternalExecution || projection.PortfolioMutated || projection.KnowledgePromoted {
		t.Fatalf("unsafe projection: %+v", projection)
	}
}

func TestTradeShadowReviewRejectsImplicitRecord(t *testing.T) {
	runner := &shadowReviewRunnerStub{}
	handler := HandleTradeShadowReview(TradeShadowReviewOptions{Enabled: true, Runner: runner})
	request := httptest.NewRequest(http.MethodPost, "/viewer/trade/shadow/outcomes/reviews", bytes.NewReader(validTradeShadowReviewViewerBody(false)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || runner.got.RequestID != "" {
		t.Fatalf("status=%d request=%+v", response.Code, runner.got)
	}
}

func validTradeShadowReviewViewerBody(allow bool) []byte {
	value := map[string]any{"request_id": "review-1", "allow_record": allow, "review": map[string]any{"idempotency_key": "review-key-1", "study_id": "study-1", "outcome_report_sha256": strings.Repeat("a", 64), "outcome_report_latest_event_hash": "sha256:" + strings.Repeat("b", 64), "reviewer_id": "reviewer-1", "reviewer_type": "independent", "review_decision": "accept", "reviewed_at": "2026-08-08T12:00:00Z", "review_reason_codes": []string{"REPORT_VALIDATED"}, "review_evidence_refs": []string{"review/record-1"}}}
	payload, _ := json.Marshal(value)
	return payload
}
