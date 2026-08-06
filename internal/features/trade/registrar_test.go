package trade

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterRoutes(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Routes{
		Status:              func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) },
		PolicyEvaluate:      func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusAccepted) },
		RiskPreview:         func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusCreated) },
		SimulationCommit:    func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusOK) },
		ShadowObservation:   func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusPartialContent) },
		ShadowOutcome:       func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusResetContent) },
		ShadowOutcomeReport: func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusOK) },
		ShadowReview:        func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusCreated) },
	})
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/viewer/trade/status", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status=%d", response.Code)
	}
	policyResponse := httptest.NewRecorder()
	mux.ServeHTTP(policyResponse, httptest.NewRequest(http.MethodPost, "/viewer/trade/policy/evaluate", nil))
	if policyResponse.Code != http.StatusAccepted {
		t.Fatalf("policy status=%d", policyResponse.Code)
	}
	riskResponse := httptest.NewRecorder()
	mux.ServeHTTP(riskResponse, httptest.NewRequest(http.MethodPost, "/viewer/trade/risk-preview", nil))
	if riskResponse.Code != http.StatusCreated {
		t.Fatalf("risk preview status=%d", riskResponse.Code)
	}
	commitResponse := httptest.NewRecorder()
	mux.ServeHTTP(commitResponse, httptest.NewRequest(http.MethodPost, "/viewer/trade/simulation-commit", nil))
	if commitResponse.Code != http.StatusOK {
		t.Fatalf("simulation commit status=%d", commitResponse.Code)
	}
	shadowResponse := httptest.NewRecorder()
	mux.ServeHTTP(shadowResponse, httptest.NewRequest(http.MethodPost, "/viewer/trade/shadow/observations", nil))
	if shadowResponse.Code != http.StatusPartialContent {
		t.Fatalf("shadow observation status=%d", shadowResponse.Code)
	}
	outcomeResponse := httptest.NewRecorder()
	mux.ServeHTTP(outcomeResponse, httptest.NewRequest(http.MethodPost, "/viewer/trade/shadow/outcomes", nil))
	if outcomeResponse.Code != http.StatusResetContent {
		t.Fatalf("shadow outcome status=%d", outcomeResponse.Code)
	}
	reportResponse := httptest.NewRecorder()
	mux.ServeHTTP(reportResponse, httptest.NewRequest(http.MethodGet, "/viewer/trade/shadow/outcomes/report?study_id=study-1", nil))
	if reportResponse.Code != http.StatusOK {
		t.Fatalf("outcome report status=%d", reportResponse.Code)
	}
	reviewResponse := httptest.NewRecorder()
	mux.ServeHTTP(reviewResponse, httptest.NewRequest(http.MethodPost, "/viewer/trade/shadow/outcomes/reviews", nil))
	if reviewResponse.Code != http.StatusCreated {
		t.Fatalf("shadow review status=%d", reviewResponse.Code)
	}
}
