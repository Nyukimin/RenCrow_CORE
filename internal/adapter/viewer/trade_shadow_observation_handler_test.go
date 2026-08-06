package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	applicationrecord "github.com/Nyukimin/RenCrow_CORE/internal/application/tradeshadowobservation"
)

type shadowObservationRunnerStub struct {
	got applicationrecord.Request
}

func (stub *shadowObservationRunnerStub) Record(_ context.Context, request applicationrecord.Request) (applicationrecord.Result, error) {
	stub.got = request
	return applicationrecord.Result{}, nil
}

func TestTradeShadowObservationRequiresExplicitRecordAndPreservesSafetyFlags(t *testing.T) {
	runner := &shadowObservationRunnerStub{}
	handler := HandleTradeShadowObservation(TradeShadowObservationOptions{Enabled: true, Runner: runner})
	body := validTradeShadowObservationViewerBody(true)
	request := httptest.NewRequest(http.MethodPost, "/viewer/trade/shadow/observations", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || runner.got.Observation.ActorID != "mio" || !runner.got.RequestAllowed {
		t.Fatalf("status=%d request=%+v body=%s", response.Code, runner.got, response.Body.String())
	}
	var projection tradeShadowObservationProjection
	if err := json.Unmarshal(response.Body.Bytes(), &projection); err != nil {
		t.Fatal(err)
	}
	if projection.Environment != "SHADOW" || projection.AuthorizesExternalExecution || projection.PortfolioMutated || projection.KnowledgePromoted {
		t.Fatalf("unsafe projection: %+v", projection)
	}
}

func TestTradeShadowObservationRejectsImplicitRecord(t *testing.T) {
	runner := &shadowObservationRunnerStub{}
	handler := HandleTradeShadowObservation(TradeShadowObservationOptions{Enabled: true, Runner: runner})
	request := httptest.NewRequest(http.MethodPost, "/viewer/trade/shadow/observations", bytes.NewReader(validTradeShadowObservationViewerBody(false)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || runner.got.RequestID != "" {
		t.Fatalf("status=%d request=%+v", response.Code, runner.got)
	}
}

func validTradeShadowObservationViewerBody(allow bool) []byte {
	value := map[string]any{
		"request_id": "shadow-1", "allow_record": allow,
		"observation": map[string]any{
			"idempotency_key": "key-1", "study_id": "study-1", "decision_id": "decision-1", "actor_id": "mio", "instrument_id": "JP-TEST",
			"decision_kind": "select", "market_observed_at": "2026-08-06T12:00:00Z", "context_snapshot_sha256": strings.Repeat("a", 64),
			"outcome_label_contract_sha256": strings.Repeat("b", 64), "reason_codes": []string{"ELIGIBLE"}, "evidence_refs": []string{"source/official-1"},
		},
	}
	payload, _ := json.Marshal(value)
	return payload
}
