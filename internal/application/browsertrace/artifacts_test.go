package browsertrace

import (
	"strings"
	"testing"
	"time"

	domaintrace "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/browsertrace"
)

func TestBuildAPIArtifactsCreatesOpenAPICoverageInventoryAndRisk(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	artifacts := BuildAPIArtifacts(domaintrace.DiscoveryResult{
		Run: domaintrace.TraceRun{
			TraceRunID:   "trace_1",
			WorkstreamID: "ws_1",
			TracePath:    "traces/trace_1",
			CreatedAt:    now,
		},
		Candidates: []domaintrace.APICandidate{{
			CandidateID:          "api_cand_1",
			TraceRunID:           "trace_1",
			Method:               "GET",
			ObservedURL:          "https://example.com/api/items",
			PathTemplate:         "/api/items",
			ContainsPersonalData: "unknown",
			RiskLevel:            "low",
			Status:               "candidate",
			CreatedAt:            now,
		}},
		Coverage: domaintrace.APICoverageReport{
			ReportID:          "coverage_1",
			TraceRunID:        "trace_1",
			ObservedEndpoints: []string{"GET /api/items"},
			MissingFlows:      []string{"terms review"},
			CreatedAt:         now,
		},
	})
	if len(artifacts) != 6 {
		t.Fatalf("artifacts=%#v", artifacts)
	}
	if artifacts[0].Type != "observed_openapi" || !strings.Contains(artifacts[0].Content, "openapi: 3.1.0") {
		t.Fatalf("openapi artifact=%#v", artifacts[0])
	}
	if artifacts[1].Type != "coverage_report" || !strings.Contains(artifacts[1].Content, "GET /api/items") {
		t.Fatalf("coverage artifact=%#v", artifacts[1])
	}
	if artifacts[2].Type != "endpoint_inventory" || !strings.Contains(artifacts[2].Content, `"candidate_id": "api_cand_1"`) {
		t.Fatalf("endpoint inventory artifact=%#v", artifacts[2])
	}
	if artifacts[3].Type != "risk_assessment" || !strings.Contains(artifacts[3].Content, "validator and human approval") {
		t.Fatalf("risk artifact=%#v", artifacts[3])
	}
	if artifacts[4].Type != "fetcher_plan" || !strings.Contains(artifacts[4].Content, "blocked until validator issues are resolved") {
		t.Fatalf("fetcher plan artifact=%#v", artifacts[4])
	}
	if artifacts[5].Type != "client_draft" || !strings.Contains(artifacts[5].Content, "not validated for fetcher use") {
		t.Fatalf("client draft artifact=%#v", artifacts[5])
	}
}

func TestBuildAPIArtifactsWithValidationsAllowsFetcherPlanForValidatedCandidate(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	artifacts := BuildAPIArtifactsWithValidations(domaintrace.DiscoveryResult{
		Run: domaintrace.TraceRun{
			TraceRunID: "trace_1",
			TracePath:  "traces/trace_1",
			CreatedAt:  now,
		},
		Candidates: []domaintrace.APICandidate{{
			CandidateID:          "api_cand_1",
			TraceRunID:           "trace_1",
			Method:               "GET",
			ObservedURL:          "https://example.com/api/items",
			PathTemplate:         "/api/items",
			ContainsPersonalData: "none",
			RiskLevel:            "low",
			Status:               "candidate",
			CreatedAt:            now,
		}},
		Coverage: domaintrace.APICoverageReport{
			ReportID:   "coverage_1",
			TraceRunID: "trace_1",
			CreatedAt:  now,
		},
	}, []domaintrace.APICandidateValidationResult{{
		ValidationID: "api_val_1",
		CandidateID:  "api_cand_1",
		TraceRunID:   "trace_1",
		Passed:       true,
		Status:       "validated",
		CreatedAt:    now,
	}})

	if !strings.Contains(artifacts[4].Content, "proposal allowed after human approval") {
		t.Fatalf("fetcher plan artifact=%#v", artifacts[4])
	}
	if !strings.Contains(artifacts[5].Content, "fetchAllowedAfterHumanApproval: true") {
		t.Fatalf("client draft artifact=%#v", artifacts[5])
	}
}
