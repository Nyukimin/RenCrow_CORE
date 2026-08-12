package personrelatedcatalog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPIdentityResolverUsesFixedSameOriginEndpoint(t *testing.T) {
	var got IdentityResolveRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != identityResolverEndpointPath {
			t.Fatalf("path=%q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("method=%s", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if got.PersonName != "宮崎駿" || got.ConfirmedExternalIDs["wikidata_qid"] != "Q42" {
			t.Fatalf("request=%#v", got)
		}
		_ = json.NewEncoder(w).Encode(identityResolveWireResponse{
			Status: "confirmed", ReasonCode: "exact_authority_cross_reference", RetrievedAt: "2026-08-12T00:00:00Z", ExpiresAt: "2026-11-10T00:00:00Z",
			Candidates: []IdentityEvidence{{Authority: "wikidata_qid", ExternalID: "Q42", CanonicalURL: "https://www.wikidata.org/wiki/Q42", State: IdentityStatusConfirmed, EvidenceSource: "wikidata", EvidenceURL: "https://www.wikidata.org/wiki/Q42", RetrievedAt: "2026-08-12T00:00:00Z", MatchedFields: []string{"birth_date"}}},
		})
	}))
	defer server.Close()
	resolver := NewHTTPIdentityResolver(server.URL, time.Second)
	result, err := resolver.ResolveIdentity(context.Background(), IdentityResolveRequest{RunID: "run-1", MovieCatalogPersonID: "p1", PersonName: "宮崎駿", PublicPersonURL: "https://eiga.com/person/p1", ConfirmedExternalIDs: map[string]string{"wikidata_qid": "Q42"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != IdentityStatusConfirmed || len(result.Candidates) != 1 || result.Candidates[0].ExternalID != "Q42" {
		t.Fatalf("result=%#v", result)
	}
}

func TestHTTPIdentityResolverRejectsFreeFormRequestFieldsAndInvalidResponse(t *testing.T) {
	resolver := NewHTTPIdentityResolver("http://127.0.0.1:1", time.Second)
	if _, err := resolver.ResolveIdentity(context.Background(), IdentityResolveRequest{RunID: "run", MovieCatalogPersonID: "p", PersonName: "人物", PublicPersonURL: "https://eiga.com/p", ConfirmedExternalIDs: map[string]string{"": "Q"}}); err == nil || !strings.Contains(err.Error(), "confirmed ID") {
		t.Fatalf("invalid request err=%v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "candidate"})
	}))
	defer server.Close()
	if _, err := NewHTTPIdentityResolver(server.URL, time.Second).ResolveIdentity(context.Background(), IdentityResolveRequest{RunID: "run", MovieCatalogPersonID: "p", PersonName: "人物"}); err == nil {
		t.Fatal("candidate response unexpectedly accepted")
	}
}
