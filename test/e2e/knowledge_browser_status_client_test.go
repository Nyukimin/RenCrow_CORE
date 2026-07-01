//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/pkg/rencrowclient"
)

func TestE2E_KnowledgeMemoryAndBrowserTraceStatusClientCurrentView(t *testing.T) {
	if os.Getenv("PICOCLAW_LIVE_E2E") != "1" {
		t.Skip("set PICOCLAW_LIVE_E2E=1 to verify live Knowledge Memory and Browser Trace API status clients")
	}

	baseURL := liveBaseURL()
	client, err := rencrowclient.New(baseURL, rencrowclient.WithHTTPClient(&http.Client{Timeout: 5 * time.Second}))
	if err != nil {
		t.Fatalf("create RenCrow client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	runID := time.Now().UTC().Format("20060102150405.000000000")
	newsID := "news_status_e2e_" + runID
	if _, err := client.CreateKnowledgeNewsItem(ctx, rencrowclient.KnowledgeNewsItem{
		ItemID:    newsID,
		Source:    "e2e",
		Topic:     "knowledge memory status current view",
		Summary:   "fresh status item avoids stale malformed live records",
		Status:    "candidate",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateKnowledgeNewsItem() live call failed at %s: %v", baseURL, err)
	}

	knowledge, err := client.KnowledgeMemoryStatus(ctx, 1)
	if err != nil {
		t.Fatalf("KnowledgeMemoryStatus() live call failed at %s: %v", baseURL, err)
	}
	t.Logf("Knowledge Memory current view items=%d", totalKnowledgeMemoryItems(knowledge))

	browserTrace, err := client.BrowserTraceAPIStatus(ctx, 10)
	if err != nil {
		t.Fatalf("BrowserTraceAPIStatus() live call failed at %s: %v", baseURL, err)
	}
	t.Logf("Browser Trace API current view items=%d", totalBrowserTraceAPIItems(browserTrace))

	_, err = client.SourceRegistryStatus(ctx, false)
	if err == nil {
		t.Fatalf("SourceRegistryStatus() unexpectedly succeeded; L1 source registry is disabled in this live config")
	}
	apiErr, ok := err.(*rencrowclient.APIError)
	if !ok || apiErr.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("SourceRegistryStatus() error = %T %v, want APIError 503", err, err)
	}
	if !strings.Contains(apiErr.Body, "source registry unavailable") {
		t.Fatalf("SourceRegistryStatus() body=%q, want unavailable message", apiErr.Body)
	}
}

func TestE2E_KnowledgeMemoryCreateReviewPromoteCurrentView(t *testing.T) {
	if os.Getenv("PICOCLAW_LIVE_E2E") != "1" {
		t.Skip("set PICOCLAW_LIVE_E2E=1 to verify live Knowledge Memory create/review/promote flow")
	}

	baseURL := liveBaseURL()
	client, err := rencrowclient.New(baseURL, rencrowclient.WithHTTPClient(&http.Client{Timeout: 5 * time.Second}))
	if err != nil {
		t.Fatalf("create RenCrow client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	runID := time.Now().UTC().Format("20060102150405.000000000")
	newsID := "news_e2e_" + runID
	ruleID := "rule_e2e_" + runID

	if _, err := client.CreateKnowledgeNewsItem(ctx, rencrowclient.KnowledgeNewsItem{
		ItemID:    newsID,
		Source:    "e2e",
		Topic:     "knowledge memory live review",
		Summary:   "candidate news item for create review promote E2E",
		Durable:   true,
		Status:    "candidate",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateKnowledgeNewsItem() live call failed: %v", err)
	}
	newsReview, err := client.ReviewKnowledgeMemory(ctx, rencrowclient.KnowledgeMemoryReviewRequest{
		DetailType:   "news_knowledge",
		ID:           newsID,
		ReviewStatus: "approved",
		Promote:      true,
		ReviewedBy:   "live-e2e",
	})
	if err != nil {
		t.Fatalf("ReviewKnowledgeMemory(news) live call failed: %v", err)
	}
	if !newsReview.Promoted || newsReview.Comparison.TargetStatus != "promoted" {
		t.Fatalf("news review=%#v", newsReview)
	}

	if _, err := client.CreateKnowledgeDailyIntakeRule(ctx, rencrowclient.KnowledgeDailyIntakeRule{
		RuleID:    ruleID,
		UserID:    "ren",
		Topic:     "knowledge memory live intake",
		Cadence:   "daily",
		Status:    "candidate",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateKnowledgeDailyIntakeRule() live call failed: %v", err)
	}
	ruleReview, err := client.ReviewKnowledgeMemory(ctx, rencrowclient.KnowledgeMemoryReviewRequest{
		DetailType:   "daily_intake_rule",
		ID:           ruleID,
		ReviewStatus: "approved",
		Promote:      true,
		ReviewedBy:   "live-e2e",
	})
	if err != nil {
		t.Fatalf("ReviewKnowledgeMemory(daily_intake_rule) live call failed: %v", err)
	}
	if !ruleReview.Promoted || ruleReview.Comparison.TargetStatus != "enabled" {
		t.Fatalf("rule review=%#v", ruleReview)
	}

	status, err := client.KnowledgeMemoryStatus(ctx, 2)
	if err != nil {
		t.Fatalf("KnowledgeMemoryStatus() live call failed: %v", err)
	}
	news, ok := findKnowledgeNews(status, newsID)
	if !ok || news.Status != "promoted" {
		t.Fatalf("news current view item=%#v found=%t", news, ok)
	}
	rule, ok := findKnowledgeDailyRule(status, ruleID)
	if !ok || rule.Status != "enabled" {
		t.Fatalf("daily rule current view item=%#v found=%t", rule, ok)
	}
	t.Logf("knowledge memory live E2E news_id=%s rule_id=%s news_target=%s rule_target=%s", newsID, ruleID, newsReview.Comparison.FormalTarget, ruleReview.Comparison.FormalTarget)
}

func TestE2E_BrowserTraceAPIDiscoverValidateAndFetcherProposal(t *testing.T) {
	if os.Getenv("PICOCLAW_LIVE_E2E") != "1" {
		t.Skip("set PICOCLAW_LIVE_E2E=1 to verify live Browser Trace API discover/validate/proposal flow")
	}

	baseURL := liveBaseURL()
	client, err := rencrowclient.New(baseURL, rencrowclient.WithHTTPClient(&http.Client{Timeout: 5 * time.Second}))
	if err != nil {
		t.Fatalf("create RenCrow client: %v", err)
	}

	runID := "trace_e2e_" + time.Now().UTC().Format("20060102150405.000000000")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	traceDir := filepath.Join(wd, ".o11y", "browser-trace-e2e", runID)
	if err := os.MkdirAll(traceDir, 0755); err != nil {
		t.Fatalf("create trace dir: %v", err)
	}
	defer os.RemoveAll(traceDir)

	requestsPath := filepath.Join(traceDir, "requests.jsonl")
	responsesPath := filepath.Join(traceDir, "responses.jsonl")
	if err := writeBrowserTraceJSONL(requestsPath, map[string]any{
		"request_id": "req_1",
		"method":     "GET",
		"url":        "https://example.test/api/e2e/items?limit=1",
		"headers":    map[string]string{"Accept": "application/json"},
	}); err != nil {
		t.Fatalf("write requests: %v", err)
	}
	if err := writeBrowserTraceJSONL(responsesPath, map[string]any{
		"request_id": "req_1",
		"url":        "https://example.test/api/e2e/items?limit=1",
		"status":     200,
		"body":       `{"items":[{"id":"item_1","title":"Browser Trace E2E"}]}`,
	}); err != nil {
		t.Fatalf("write responses: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	discovered, err := client.DiscoverBrowserTraceAPI(ctx, rencrowclient.BrowserTraceAPIDiscoverRequest{
		TraceRunID:    runID,
		SiteID:        "browser-trace-e2e",
		Goal:          "verify live Browser Trace API discover validate proposal flow",
		TracePath:     traceDir,
		RequestsPath:  requestsPath,
		ResponsesPath: responsesPath,
	})
	if err != nil {
		t.Fatalf("DiscoverBrowserTraceAPI() live call failed: %v", err)
	}
	if len(discovered.APICandidates) != 1 || len(discovered.APISchemas) == 0 || len(discovered.APIArtifacts) == 0 {
		t.Fatalf("discovered=%#v", discovered)
	}
	candidate := discovered.APICandidates[0]
	if len(discovered.APIValidations) != 1 || discovered.APIValidations[0].Passed || discovered.APIValidations[0].Status != "needs_review" {
		t.Fatalf("initial validation should require review, got %#v", discovered.APIValidations)
	}

	review, err := client.ValidateBrowserTraceAPICandidate(ctx, rencrowclient.BrowserTraceAPIValidationReviewRequest{
		CandidateID:         candidate.CandidateID,
		Reviewer:            "live-e2e",
		ReviewNote:          "local fixture only; no official promotion or implementation apply",
		HumanApproved:       true,
		TermsReviewed:       true,
		OfficialAPIReviewed: true,
		PIIReviewed:         true,
		SchemaReviewed:      true,
		RiskReviewed:        true,
	})
	if err != nil {
		t.Fatalf("ValidateBrowserTraceAPICandidate() live call failed: %v", err)
	}
	if !review.Validation.Passed || review.Validation.Status != "validated" {
		t.Fatalf("review=%#v", review)
	}

	proposal, err := client.CreateBrowserTraceAPIFetcherProposal(ctx, rencrowclient.BrowserTraceAPIFetcherProposalRequest{
		CandidateID:   candidate.CandidateID,
		HumanApproved: true,
	})
	if err != nil {
		t.Fatalf("CreateBrowserTraceAPIFetcherProposal() live call failed: %v", err)
	}
	if proposal.OfficialPromotion || proposal.ImplementationApply || proposal.APIArtifact.Status != "pending_review" {
		t.Fatalf("proposal=%#v", proposal)
	}

	status, err := client.BrowserTraceAPIStatus(ctx, 50)
	if err != nil {
		t.Fatalf("BrowserTraceAPIStatus() live call failed: %v", err)
	}
	if !browserTraceStatusContains(status, runID, candidate.CandidateID, proposal.APIArtifact.ArtifactID) {
		t.Fatalf("browser trace current view missing run/candidate/proposal: run_id=%s candidate_id=%s artifact_id=%s status=%#v", runID, candidate.CandidateID, proposal.APIArtifact.ArtifactID, status)
	}
	t.Logf("browser trace live E2E trace_run_id=%s candidate_id=%s validation_id=%s proposal_artifact_id=%s", runID, candidate.CandidateID, review.Validation.ValidationID, proposal.APIArtifact.ArtifactID)
}

func writeBrowserTraceJSONL(path string, value any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(value); err != nil {
		return err
	}
	return nil
}

func totalKnowledgeMemoryItems(status rencrowclient.KnowledgeMemoryStatus) int {
	return len(status.PersonalArchive) +
		len(status.CreativeKnowledge) +
		len(status.NewsKnowledge) +
		len(status.DailyIntakeRules) +
		len(status.TemporalMarkers) +
		len(status.DreamRuns)
}

func findKnowledgeNews(status rencrowclient.KnowledgeMemoryStatus, id string) (rencrowclient.KnowledgeNewsItem, bool) {
	for _, item := range status.NewsKnowledge {
		if item.ItemID == id {
			return item, true
		}
	}
	return rencrowclient.KnowledgeNewsItem{}, false
}

func findKnowledgeDailyRule(status rencrowclient.KnowledgeMemoryStatus, id string) (rencrowclient.KnowledgeDailyIntakeRule, bool) {
	for _, item := range status.DailyIntakeRules {
		if item.RuleID == id {
			return item, true
		}
	}
	return rencrowclient.KnowledgeDailyIntakeRule{}, false
}

func totalBrowserTraceAPIItems(status rencrowclient.BrowserTraceAPIStatus) int {
	return len(status.TraceRuns) +
		len(status.APICandidates) +
		len(status.APISchemas) +
		len(status.APIValidations) +
		len(status.CoverageReports) +
		len(status.APIArtifacts)
}

func browserTraceStatusContains(status rencrowclient.BrowserTraceAPIStatus, runID string, candidateID string, artifactID string) bool {
	hasRun := false
	for _, item := range status.TraceRuns {
		if item.TraceRunID == runID {
			hasRun = true
			break
		}
	}
	hasCandidate := false
	for _, item := range status.APICandidates {
		if item.CandidateID == candidateID {
			hasCandidate = true
			break
		}
	}
	hasArtifact := false
	for _, item := range status.APIArtifacts {
		if item.ArtifactID == artifactID && item.Status == "pending_review" {
			hasArtifact = true
			break
		}
	}
	return hasRun && hasCandidate && hasArtifact
}
