package browsertrace

import (
	"encoding/json"
	"fmt"
	"strings"

	domaintrace "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/browsertrace"
)

func BuildAPIArtifacts(result domaintrace.DiscoveryResult) []domaintrace.APIArtifact {
	return BuildAPIArtifactsWithValidations(result, nil)
}

func BuildAPIArtifactsWithValidations(result domaintrace.DiscoveryResult, validations []domaintrace.APICandidateValidationResult) []domaintrace.APIArtifact {
	now := result.Run.CreatedAt
	return []domaintrace.APIArtifact{
		{
			ArtifactID:   "art_openapi_" + result.Run.TraceRunID,
			TraceRunID:   result.Run.TraceRunID,
			WorkstreamID: result.Run.WorkstreamID,
			Type:         "observed_openapi",
			Title:        "Observed OpenAPI Draft",
			Status:       "generated",
			Content:      BuildObservedOpenAPIYAML(result),
			CreatedAt:    now,
		},
		{
			ArtifactID:   "art_coverage_" + result.Run.TraceRunID,
			TraceRunID:   result.Run.TraceRunID,
			WorkstreamID: result.Run.WorkstreamID,
			Type:         "coverage_report",
			Title:        "API Coverage Report",
			Status:       "generated",
			Content:      BuildCoverageMarkdown(result.Coverage),
			CreatedAt:    now,
		},
		{
			ArtifactID:   "art_endpoint_inventory_" + result.Run.TraceRunID,
			TraceRunID:   result.Run.TraceRunID,
			WorkstreamID: result.Run.WorkstreamID,
			Type:         "endpoint_inventory",
			Title:        "Endpoint Inventory",
			Status:       "generated",
			Content:      BuildEndpointInventoryJSON(result),
			CreatedAt:    now,
		},
		{
			ArtifactID:   "art_risk_" + result.Run.TraceRunID,
			TraceRunID:   result.Run.TraceRunID,
			WorkstreamID: result.Run.WorkstreamID,
			Type:         "risk_assessment",
			Title:        "API Risk Assessment",
			Status:       "generated",
			Content:      BuildRiskAssessmentMarkdown(result),
			CreatedAt:    now,
		},
		{
			ArtifactID:   "art_fetcher_plan_" + result.Run.TraceRunID,
			TraceRunID:   result.Run.TraceRunID,
			WorkstreamID: result.Run.WorkstreamID,
			Type:         "fetcher_plan",
			Title:        "Fetcher Plan",
			Status:       "draft",
			Content:      BuildFetcherPlanMarkdown(result, validations),
			CreatedAt:    now,
		},
		{
			ArtifactID:   "art_client_draft_" + result.Run.TraceRunID,
			TraceRunID:   result.Run.TraceRunID,
			WorkstreamID: result.Run.WorkstreamID,
			Type:         "client_draft",
			Title:        "Client Draft",
			Status:       "draft",
			Content:      BuildClientDraftMJS(result, validations),
			CreatedAt:    now,
		},
	}
}

func BuildEndpointInventoryJSON(result domaintrace.DiscoveryResult) string {
	type inventoryEndpoint struct {
		CandidateID          string                      `json:"candidate_id"`
		Method               string                      `json:"method"`
		ObservedURL          string                      `json:"observed_url"`
		TemplatedURL         string                      `json:"templated_url,omitempty"`
		PathTemplate         string                      `json:"path_template,omitempty"`
		QueryParams          []domaintrace.APIQueryParam `json:"query_params,omitempty"`
		AuthRequired         bool                        `json:"auth_required"`
		ContainsPersonalData string                      `json:"contains_personal_data"`
		RiskLevel            string                      `json:"risk_level"`
		Status               string                      `json:"status"`
		Confidence           float64                     `json:"confidence,omitempty"`
	}
	payload := struct {
		TraceRunID string              `json:"trace_run_id"`
		SiteID     string              `json:"site_id,omitempty"`
		Endpoints  []inventoryEndpoint `json:"endpoints"`
	}{
		TraceRunID: result.Run.TraceRunID,
		SiteID:     result.Run.SiteID,
		Endpoints:  make([]inventoryEndpoint, 0, len(result.Candidates)),
	}
	for _, candidate := range result.Candidates {
		payload.Endpoints = append(payload.Endpoints, inventoryEndpoint{
			CandidateID:          candidate.CandidateID,
			Method:               candidate.Method,
			ObservedURL:          candidate.ObservedURL,
			TemplatedURL:         candidate.TemplatedURL,
			PathTemplate:         candidate.PathTemplate,
			QueryParams:          candidate.QueryParams,
			AuthRequired:         candidate.AuthRequired,
			ContainsPersonalData: candidate.ContainsPersonalData,
			RiskLevel:            candidate.RiskLevel,
			Status:               candidate.Status,
			Confidence:           candidate.Confidence,
		})
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return `{"trace_run_id":"` + result.Run.TraceRunID + `","endpoints":[]}`
	}
	return string(data)
}

func BuildObservedOpenAPIYAML(result domaintrace.DiscoveryResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "openapi: 3.1.0\n")
	fmt.Fprintf(&b, "info:\n  title: Observed API\n  version: 0.0.0-observed\n")
	fmt.Fprintf(&b, "paths:\n")
	if len(result.Candidates) == 0 {
		fmt.Fprintf(&b, "  {}\n")
		return b.String()
	}
	for _, candidate := range result.Candidates {
		path := candidate.PathTemplate
		if path == "" {
			path = "/"
		}
		method := strings.ToLower(candidate.Method)
		if method == "" {
			method = "get"
		}
		fmt.Fprintf(&b, "  %s:\n", path)
		fmt.Fprintf(&b, "    %s:\n", method)
		fmt.Fprintf(&b, "      x-observed-url: %q\n", candidate.ObservedURL)
		fmt.Fprintf(&b, "      x-risk-level: %q\n", candidate.RiskLevel)
		fmt.Fprintf(&b, "      responses:\n")
		fmt.Fprintf(&b, "        '200':\n")
		fmt.Fprintf(&b, "          description: Observed response\n")
	}
	return b.String()
}

func BuildCoverageMarkdown(report domaintrace.APICoverageReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# API Coverage Report\n\n")
	writeList(&b, "Observed flows", report.ObservedFlows)
	writeList(&b, "Observed endpoints", report.ObservedEndpoints)
	writeList(&b, "Missing flows", report.MissingFlows)
	writeList(&b, "Recommended next traces", report.RecommendedNextTraces)
	return b.String()
}

func BuildRiskAssessmentMarkdown(result domaintrace.DiscoveryResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# API Risk Assessment\n\n")
	if len(result.Candidates) == 0 {
		fmt.Fprintf(&b, "No API candidates were observed.\n")
		return b.String()
	}
	for _, candidate := range result.Candidates {
		fmt.Fprintf(&b, "## %s %s\n\n", candidate.Method, candidate.PathTemplate)
		fmt.Fprintf(&b, "- Status: %s\n", candidate.Status)
		fmt.Fprintf(&b, "- Risk: %s\n", candidate.RiskLevel)
		fmt.Fprintf(&b, "- Auth required: %v\n", candidate.AuthRequired)
		fmt.Fprintf(&b, "- Contains personal data: %s\n", candidate.ContainsPersonalData)
		fmt.Fprintf(&b, "- Promote requirement: validator and human approval\n\n")
	}
	return b.String()
}

func BuildFetcherPlanMarkdown(result domaintrace.DiscoveryResult, validations []domaintrace.APICandidateValidationResult) string {
	validationByCandidate := map[string]domaintrace.APICandidateValidationResult{}
	for _, validation := range validations {
		validationByCandidate[validation.CandidateID] = validation
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Fetcher Plan\n\n")
	fmt.Fprintf(&b, "Trace run: `%s`\n\n", result.Run.TraceRunID)
	if len(result.Candidates) == 0 {
		fmt.Fprintf(&b, "No API candidates were observed.\n")
		return b.String()
	}
	for _, candidate := range result.Candidates {
		validation, ok := validationByCandidate[candidate.CandidateID]
		status := "unknown"
		if ok {
			status = validation.Status
		}
		fmt.Fprintf(&b, "## %s %s\n\n", candidate.Method, candidate.PathTemplate)
		fmt.Fprintf(&b, "- Candidate: `%s`\n", candidate.CandidateID)
		fmt.Fprintf(&b, "- Observed URL: `%s`\n", candidate.ObservedURL)
		fmt.Fprintf(&b, "- Validation status: %s\n", status)
		fmt.Fprintf(&b, "- Fetcher action: ")
		if ok && validation.Passed {
			fmt.Fprintf(&b, "proposal allowed after human approval\n")
		} else {
			fmt.Fprintf(&b, "blocked until validator issues are resolved\n")
		}
		if ok && len(validation.Issues) > 0 {
			fmt.Fprintf(&b, "- Blocking issues:\n")
			for _, issue := range validation.Issues {
				fmt.Fprintf(&b, "  - `%s`: %s\n", issue.Code, issue.Message)
			}
		}
		fmt.Fprintf(&b, "- Required implementation checks: timeout, retry, rate limit, schema validation, staging output, no direct promoted DB write\n\n")
	}
	return b.String()
}

func BuildClientDraftMJS(result domaintrace.DiscoveryResult, validations []domaintrace.APICandidateValidationResult) string {
	validationByCandidate := map[string]domaintrace.APICandidateValidationResult{}
	for _, validation := range validations {
		validationByCandidate[validation.CandidateID] = validation
	}
	var b strings.Builder
	fmt.Fprintf(&b, "// Generated client draft from observed browser trace.\n")
	fmt.Fprintf(&b, "// Trace run: %s\n", result.Run.TraceRunID)
	fmt.Fprintf(&b, "// This draft is review-only. Do not run it against live services before validator and human approval pass.\n\n")
	fmt.Fprintf(&b, "export const traceRunId = %q;\n\n", result.Run.TraceRunID)
	fmt.Fprintf(&b, "export const endpoints = [\n")
	for _, candidate := range result.Candidates {
		validation, ok := validationByCandidate[candidate.CandidateID]
		allowed := ok && validation.Passed
		status := "unknown"
		if ok {
			status = validation.Status
		}
		fmt.Fprintf(&b, "  {\n")
		fmt.Fprintf(&b, "    candidateId: %q,\n", candidate.CandidateID)
		fmt.Fprintf(&b, "    method: %q,\n", candidate.Method)
		fmt.Fprintf(&b, "    pathTemplate: %q,\n", candidate.PathTemplate)
		fmt.Fprintf(&b, "    observedUrl: %q,\n", candidate.ObservedURL)
		fmt.Fprintf(&b, "    validationStatus: %q,\n", status)
		fmt.Fprintf(&b, "    fetchAllowedAfterHumanApproval: %t,\n", allowed)
		fmt.Fprintf(&b, "  },\n")
	}
	fmt.Fprintf(&b, "];\n\n")
	fmt.Fprintf(&b, "export async function fetchObservedEndpoint(endpoint, fetcher = fetch) {\n")
	fmt.Fprintf(&b, "  if (!endpoint.fetchAllowedAfterHumanApproval) {\n")
	fmt.Fprintf(&b, "    throw new Error(`API candidate ${endpoint.candidateId} is not validated for fetcher use`);\n")
	fmt.Fprintf(&b, "  }\n")
	fmt.Fprintf(&b, "  const response = await fetcher(endpoint.observedUrl, { method: endpoint.method });\n")
	fmt.Fprintf(&b, "  if (!response.ok) {\n")
	fmt.Fprintf(&b, "    throw new Error(`API request failed: ${response.status}`);\n")
	fmt.Fprintf(&b, "  }\n")
	fmt.Fprintf(&b, "  return response.json();\n")
	fmt.Fprintf(&b, "}\n")
	return b.String()
}

func BuildFetcherProposalMarkdown(candidate domaintrace.APICandidate, validation domaintrace.APICandidateValidationResult, schemas []domaintrace.APICandidateSchema) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Fetcher Proposal\n\n")
	fmt.Fprintf(&b, "Candidate: `%s`\n\n", candidate.CandidateID)
	fmt.Fprintf(&b, "## Endpoint\n\n")
	fmt.Fprintf(&b, "- Method: `%s`\n", candidate.Method)
	fmt.Fprintf(&b, "- Path template: `%s`\n", candidate.PathTemplate)
	fmt.Fprintf(&b, "- Observed URL: `%s`\n", candidate.ObservedURL)
	fmt.Fprintf(&b, "- Risk level: `%s`\n", candidate.RiskLevel)
	fmt.Fprintf(&b, "- Auth required: `%v`\n", candidate.AuthRequired)
	fmt.Fprintf(&b, "- Contains personal data: `%s`\n\n", candidate.ContainsPersonalData)
	fmt.Fprintf(&b, "## Validation\n\n")
	fmt.Fprintf(&b, "- Status: `%s`\n", validation.Status)
	fmt.Fprintf(&b, "- Passed: `%v`\n", validation.Passed)
	if len(validation.Issues) > 0 {
		fmt.Fprintf(&b, "- Issues:\n")
		for _, issue := range validation.Issues {
			fmt.Fprintf(&b, "  - `%s`: %s\n", issue.Code, issue.Message)
		}
	}
	fmt.Fprintf(&b, "\n## Schema Evidence\n\n")
	matched := 0
	for _, schema := range schemas {
		if schema.CandidateID != candidate.CandidateID {
			continue
		}
		matched++
		fmt.Fprintf(&b, "- `%s`: samples=%d confidence=%.2f\n", schema.SchemaType, schema.SampleCount, schema.Confidence)
	}
	if matched == 0 {
		fmt.Fprintf(&b, "- no schema sample recorded\n")
	}
	fmt.Fprintf(&b, "\n## Implementation Requirements\n\n")
	fmt.Fprintf(&b, "- read-only fetcher only\n")
	fmt.Fprintf(&b, "- timeout and retry policy required\n")
	fmt.Fprintf(&b, "- rate-limit behavior must be documented\n")
	fmt.Fprintf(&b, "- response schema validation required\n")
	fmt.Fprintf(&b, "- output must go to staging / Source Registry candidate first\n")
	fmt.Fprintf(&b, "- no direct promoted DB write\n")
	fmt.Fprintf(&b, "- credentials, cookies, session tokens, and CSRF tokens must not be persisted\n")
	fmt.Fprintf(&b, "- human review is required before implementation\n")
	return b.String()
}

func writeList(b *strings.Builder, title string, items []string) {
	fmt.Fprintf(b, "## %s\n\n", title)
	if len(items) == 0 {
		fmt.Fprintf(b, "- none\n\n")
		return
	}
	for _, item := range items {
		fmt.Fprintf(b, "- %s\n", item)
	}
	fmt.Fprintf(b, "\n")
}
