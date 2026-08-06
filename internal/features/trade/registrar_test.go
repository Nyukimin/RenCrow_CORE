package trade

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterRoutes(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Routes{
		Status:         func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) },
		PolicyEvaluate: func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusAccepted) },
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
}
