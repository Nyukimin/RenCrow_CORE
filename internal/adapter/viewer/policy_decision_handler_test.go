package viewer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	domainpolicy "github.com/Nyukimin/RenCrow_CORE/internal/domain/policydecision"
)

type policyDecisionListerStub struct {
	limit int
}

func (s *policyDecisionListerStub) List(_ context.Context, limit int) ([]domainpolicy.Record, error) {
	s.limit = limit
	return nil, nil
}

func TestHandlePolicyDecisionsUsesBoundedLimit(t *testing.T) {
	store := &policyDecisionListerStub{}
	recorder := httptest.NewRecorder()
	HandlePolicyDecisions(store).ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "/viewer/policy/decisions?limit=3", nil),
	)
	if recorder.Code != http.StatusOK || store.limit != 3 {
		t.Fatalf("status=%d limit=%d", recorder.Code, store.limit)
	}
}

func TestHandlePolicyDecisionsRejectsInvalidLimit(t *testing.T) {
	recorder := httptest.NewRecorder()
	HandlePolicyDecisions(&policyDecisionListerStub{}).ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "/viewer/policy/decisions?limit=101", nil),
	)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", recorder.Code)
	}
}
