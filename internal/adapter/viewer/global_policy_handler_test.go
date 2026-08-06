package viewer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	domainpolicy "github.com/Nyukimin/RenCrow_CORE/internal/domain/policybundle"
)

func TestHandleGlobalPolicyStatus(t *testing.T) {
	provider := globalPolicyStatusStub{status: domainpolicy.Status{
		State:                domainpolicy.StateInvalid,
		ContractRevision:     domainpolicy.ContractRevision,
		DisabledCapabilities: []string{"financial_order"},
		Error:                "bundle content hash mismatch",
	}}
	handler := HandleGlobalPolicyStatus(provider)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/viewer/policy/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d", recorder.Code)
	}
	var got domainpolicy.Status
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.State != domainpolicy.StateInvalid || got.ContractRevision != domainpolicy.ContractRevision {
		t.Fatalf("unexpected payload: %+v", got)
	}
}

func TestHandleGlobalPolicyStatusRejectsNonGET(t *testing.T) {
	handler := HandleGlobalPolicyStatus(globalPolicyStatusStub{})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/viewer/policy/status", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d", recorder.Code)
	}
}

type globalPolicyStatusStub struct {
	status domainpolicy.Status
}

func (s globalPolicyStatusStub) Status() domainpolicy.Status { return s.status }
