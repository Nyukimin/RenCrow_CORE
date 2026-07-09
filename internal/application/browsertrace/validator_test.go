package browsertrace

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	domaintrace "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
)

func TestValidateAPICandidatesRequiresReviewForTermsPIIAndAuth(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	results := ValidateAPICandidates([]domaintrace.APICandidate{{
		CandidateID:          "api_cand_1",
		TraceRunID:           "trace_1",
		Method:               "POST",
		ObservedURL:          "https://example.com/api/search",
		AuthRequired:         true,
		ContainsPersonalData: "unknown",
		RiskLevel:            "medium",
		Status:               "candidate",
		CreatedAt:            now,
	}}, DefaultValidationPolicy(), now)

	if len(results) != 1 {
		t.Fatalf("results=%#v", results)
	}
	result := results[0]
	if result.Passed || result.Status != "needs_review" {
		t.Fatalf("expected needs_review, got %#v", result)
	}
	wantCodes := map[string]bool{
		"read_only_review_required":   false,
		"terms_review_required":       false,
		"official_api_check_required": false,
		"auth_review_required":        false,
		"pii_review_required":         false,
		"risk_review_required":        false,
	}
	for _, issue := range result.Issues {
		if _, ok := wantCodes[issue.Code]; ok {
			wantCodes[issue.Code] = true
		}
	}
	for code, seen := range wantCodes {
		if !seen {
			t.Fatalf("missing issue %s in %#v", code, result.Issues)
		}
	}
}

func TestValidateAPICandidatesWithLivePolicyConfirmsRobotsAndRateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: *\nAllow: /\n"))
		case "/api/items":
			w.Header().Set("RateLimit-Limit", "60")
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	results := ValidateAPICandidatesWithLivePolicy(context.Background(), []domaintrace.APICandidate{{
		CandidateID:          "api_cand_1",
		TraceRunID:           "trace_1",
		Method:               "GET",
		ObservedURL:          server.URL + "/api/items",
		ContainsPersonalData: "none",
		RiskLevel:            "low",
		Status:               "candidate",
		CreatedAt:            now,
	}}, ValidationPolicy{ReadOnlyOnly: true, RequireLivePolicyCheck: true}, now, HTTPPolicyChecker{})

	if len(results) != 1 || !results[0].Passed || results[0].Status != "validated" {
		t.Fatalf("expected validated result, got %#v", results)
	}
}

func TestValidateAPICandidatesWithLivePolicyRequiresReviewWhenRateLimitMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: *\nAllow: /\n"))
		case "/api/items":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	results := ValidateAPICandidatesWithLivePolicy(context.Background(), []domaintrace.APICandidate{{
		CandidateID:          "api_cand_1",
		TraceRunID:           "trace_1",
		Method:               "GET",
		ObservedURL:          server.URL + "/api/items",
		ContainsPersonalData: "none",
		RiskLevel:            "low",
		Status:               "candidate",
		CreatedAt:            now,
	}}, ValidationPolicy{ReadOnlyOnly: true, RequireLivePolicyCheck: true}, now, HTTPPolicyChecker{})

	if len(results) != 1 || results[0].Passed || results[0].Status != "needs_review" {
		t.Fatalf("expected needs_review result, got %#v", results)
	}
	seen := false
	for _, issue := range results[0].Issues {
		if issue.Code == "rate_limit_review_required" {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("issues = %#v", results[0].Issues)
	}
}

func TestValidateAPICandidatesCanPassWhenPolicyChecksAreSatisfied(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	results := ValidateAPICandidates([]domaintrace.APICandidate{{
		CandidateID:          "api_cand_1",
		TraceRunID:           "trace_1",
		Method:               "GET",
		ObservedURL:          "https://example.com/api/items",
		ContainsPersonalData: "none",
		RiskLevel:            "low",
		Status:               "candidate",
		CreatedAt:            now,
	}}, ValidationPolicy{ReadOnlyOnly: true}, now)

	if len(results) != 1 || !results[0].Passed || results[0].Status != "validated" {
		t.Fatalf("expected validated result, got %#v", results)
	}
}

func TestValidateAPICandidatesRequiresReviewForDeniedSensitiveFlow(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	results := ValidateAPICandidates([]domaintrace.APICandidate{{
		CandidateID:          "api_cand_1",
		TraceRunID:           "trace_1",
		Method:               "GET",
		ObservedURL:          "https://example.com/api/payment/status",
		TemplatedURL:         "https://example.com/api/payment/status",
		PathTemplate:         "/api/payment/status",
		ContainsPersonalData: "none",
		RiskLevel:            "low",
		Status:               "candidate",
		CreatedAt:            now,
	}}, ValidationPolicy{ReadOnlyOnly: true, DenySensitiveFlows: []string{"payment"}}, now)

	if len(results) != 1 || results[0].Passed || results[0].Status != "needs_review" {
		t.Fatalf("expected sensitive flow review, got %#v", results)
	}
	seen := false
	for _, issue := range results[0].Issues {
		if issue.Code == "sensitive_flow_review_required" {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("issues = %#v", results[0].Issues)
	}
}
