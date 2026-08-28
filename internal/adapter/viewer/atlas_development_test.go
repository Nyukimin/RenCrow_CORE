package viewer

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	appbacklog "github.com/Nyukimin/RenCrow_CORE/internal/application/backlog"
	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	methodology "github.com/Nyukimin/RenCrow_CORE/internal/domain/developmentmethodology"
	workstreampersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/workstream"
)

func TestAtlasDevelopmentOwnerAPIAndReadProjection(t *testing.T) {
	now := time.Date(2026, 8, 28, 20, 0, 0, 0, time.UTC)
	items := &atlasHTTPItemStore{items: []domainbacklog.Item{{SchemaVersion: 2, ItemID: "methodology", Title: "Methodology", ConceptState: domainbacklog.ConceptAdopted, DeliveryState: domainbacklog.DeliverySpec, ImplementationUnit: "unit-methodology", ImplementationRevision: 1, WorkstreamID: "ws-methodology", OwnerModule: domainbacklog.LifecycleOwnerModule, AdoptedAt: now.Add(-time.Hour).Format(time.RFC3339), CreatedAt: now.Format(time.RFC3339), UpdatedAt: now.Format(time.RFC3339)}}}
	service := appbacklog.NewService(items, workstreampersistence.NewJSONLStore(t.TempDir())).WithClock(func() time.Time { return now })
	token := "atlas-owner-token-012345678901234567890123"
	handler := NewAtlasHandler(service, "ren", []byte(token))
	spec := methodology.Specification{SchemaVersion: 1, SpecID: "spec-methodology", Title: "Methodology", Revision: 1, Status: methodology.SpecificationApproved, Source: "owner", ContentHash: methodology.HashContent("spec"), Purpose: "complete delivery", Problem: "partial checks", Scope: []string{"CORE"}, AcceptanceCriteria: []string{"viewer"}, CreatedAt: now, UpdatedAt: now}
	payload, _ := json.Marshal(map[string]any{"artifact_type": "specification", "trace_id": "trace-methodology", "payload": spec})
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/atlas/development/units/unit-methodology/artifacts", bytes.NewReader(payload))
	request.RemoteAddr = "127.0.0.1:42000"
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	request.Header.Set("X-RenCrow-Interaction-Profile", "cmd-control")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || !bytes.Contains(response.Body.Bytes(), []byte(`"spec_id":"spec-methodology"`)) {
		t.Fatalf("POST status=%d body=%s", response.Code, response.Body.String())
	}
	implementation_authorityPayload, _ := json.Marshal(map[string]any{"issuer": "ren", "scope": []string{"implementation"}, "reason": "explicit owner adoption", "expires_at": now.Add(24 * time.Hour)})
	implementation_authorityRequest := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/atlas/development/units/unit-methodology/implementation_authority", bytes.NewReader(implementation_authorityPayload))
	implementation_authorityRequest.RemoteAddr = "127.0.0.1:42000"
	implementation_authorityRequest.Header.Set("Authorization", "Bearer "+token)
	implementation_authorityRequest.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	implementation_authorityRequest.Header.Set("X-RenCrow-Interaction-Profile", "cmd-control")
	implementation_authorityResponse := httptest.NewRecorder()
	handler.ServeHTTP(implementation_authorityResponse, implementation_authorityRequest)
	if implementation_authorityResponse.Code != http.StatusCreated || !bytes.Contains(implementation_authorityResponse.Body.Bytes(), []byte(`"implementation_authority_token_id"`)) {
		t.Fatalf("implementation_authority status=%d body=%s", implementation_authorityResponse.Code, implementation_authorityResponse.Body.String())
	}
	read := httptest.NewRecorder()
	handler.ServeHTTP(read, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/viewer/atlas/development/units/unit-methodology", nil))
	if read.Code != http.StatusOK || !bytes.Contains(read.Body.Bytes(), []byte(`"unit_id":"unit-methodology"`)) {
		t.Fatalf("GET status=%d body=%s", read.Code, read.Body.String())
	}
	unauthorized := httptest.NewRecorder()
	bad := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/atlas/development/units/unit-methodology/artifacts", bytes.NewReader(payload))
	bad.RemoteAddr = "127.0.0.1:42000"
	handler.ServeHTTP(unauthorized, bad)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}
}
