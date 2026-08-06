package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	applicationrecord "github.com/Nyukimin/RenCrow_CORE/internal/application/tradeshadowoutcome"
)

type shadowOutcomeRunnerStub struct {
	got applicationrecord.Request
}

func (stub *shadowOutcomeRunnerStub) Record(_ context.Context, request applicationrecord.Request) (applicationrecord.Result, error) {
	stub.got = request
	return applicationrecord.Result{}, nil
}

func TestTradeShadowOutcomeRequiresExplicitRecordAndPreservesSafetyFlags(t *testing.T) {
	runner := &shadowOutcomeRunnerStub{}
	handler := HandleTradeShadowOutcome(TradeShadowOutcomeOptions{Enabled: true, Runner: runner})
	request := httptest.NewRequest(http.MethodPost, "/viewer/trade/shadow/outcomes", bytes.NewReader(validTradeShadowOutcomeViewerBody(true)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || runner.got.Outcome.DecisionID != "decision-1" || !runner.got.RequestAllowed {
		t.Fatalf("status=%d request=%+v body=%s", response.Code, runner.got, response.Body.String())
	}
	var projection tradeShadowOutcomeProjection
	if err := json.Unmarshal(response.Body.Bytes(), &projection); err != nil {
		t.Fatal(err)
	}
	if projection.Environment != "SHADOW" || projection.AuthorizesExternalExecution || projection.PortfolioMutated || projection.KnowledgePromoted {
		t.Fatalf("unsafe projection: %+v", projection)
	}
}

func TestTradeShadowOutcomeRejectsImplicitRecord(t *testing.T) {
	runner := &shadowOutcomeRunnerStub{}
	handler := HandleTradeShadowOutcome(TradeShadowOutcomeOptions{Enabled: true, Runner: runner})
	request := httptest.NewRequest(http.MethodPost, "/viewer/trade/shadow/outcomes", bytes.NewReader(validTradeShadowOutcomeViewerBody(false)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || runner.got.RequestID != "" {
		t.Fatalf("status=%d request=%+v", response.Code, runner.got)
	}
}

func validTradeShadowOutcomeViewerBody(allow bool) []byte {
	value := map[string]any{
		"request_id": "outcome-1", "allow_record": allow,
		"outcome": map[string]any{
			"idempotency_key": "outcome-key-1", "study_id": "study-1", "decision_id": "decision-1", "market_observed_at": "2026-08-06T12:00:00Z",
			"outcome_label": "success", "outcome_observed_at": "2026-08-07T12:00:00Z", "outcome_snapshot_sha256": strings.Repeat("c", 64),
			"outcome_reason_codes": []string{"THESIS_CONFIRMED"}, "outcome_evidence_refs": []string{"source/outcome-1"}, "outcome_label_contract_sha256": strings.Repeat("b", 64),
		},
	}
	payload, _ := json.Marshal(value)
	return payload
}
