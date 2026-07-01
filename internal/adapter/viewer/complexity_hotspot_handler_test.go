package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	complexityapp "github.com/Nyukimin/picoclaw_multiLLM/internal/application/complexity"
	domaincomplexity "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/complexity"
	domaindci "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/dci"
	domainsandbox "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/sandbox"
	domainskill "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/skillgovernance"
	domainworkstream "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/workstream"
)

type stubComplexityHotspotStore struct {
	scans    []domaincomplexity.ScanEvent
	hotspots []domaincomplexity.Hotspot
	evidence []domaincomplexity.HotspotEvidence
	reports  []domaincomplexity.ReportArtifact
}

func (s *stubComplexityHotspotStore) ListScanEvents(_ context.Context, _ int) ([]domaincomplexity.ScanEvent, error) {
	return s.scans, nil
}
func (s *stubComplexityHotspotStore) ListHotspots(_ context.Context, _ int) ([]domaincomplexity.Hotspot, error) {
	return s.hotspots, nil
}
func (s *stubComplexityHotspotStore) ListHotspotEvidence(_ context.Context, _ int) ([]domaincomplexity.HotspotEvidence, error) {
	return s.evidence, nil
}
func (s *stubComplexityHotspotStore) ListReportArtifacts(_ context.Context, _ int) ([]domaincomplexity.ReportArtifact, error) {
	return s.reports, nil
}
func (s *stubComplexityHotspotStore) SaveScanEvent(_ context.Context, item domaincomplexity.ScanEvent) error {
	if err := domaincomplexity.ValidateScanEvent(item); err != nil {
		return err
	}
	s.scans = append(s.scans, item)
	return nil
}
func (s *stubComplexityHotspotStore) SaveHotspot(_ context.Context, item domaincomplexity.Hotspot) error {
	if err := domaincomplexity.ValidateHotspot(item); err != nil {
		return err
	}
	s.hotspots = append(s.hotspots, item)
	return nil
}
func (s *stubComplexityHotspotStore) SaveHotspotEvidence(_ context.Context, item domaincomplexity.HotspotEvidence) error {
	if err := domaincomplexity.ValidateHotspotEvidence(item); err != nil {
		return err
	}
	s.evidence = append(s.evidence, item)
	return nil
}
func (s *stubComplexityHotspotStore) SaveReportArtifact(_ context.Context, item domaincomplexity.ReportArtifact) error {
	if err := domaincomplexity.ValidateReportArtifact(item); err != nil {
		return err
	}
	s.reports = append(s.reports, item)
	return nil
}

type stubComplexityAnalyzer struct {
	result   domaincomplexity.ScanResult
	requests []complexityapp.ScanRequest
}

func (s *stubComplexityAnalyzer) Scan(req complexityapp.ScanRequest) (domaincomplexity.ScanResult, error) {
	s.requests = append(s.requests, req)
	return s.result, nil
}

type stubComplexityCoderDiffGenerator struct {
	result   complexityapp.CoderDiffResult
	err      error
	requests []complexityapp.CoderDiffRequest
}

func (s *stubComplexityCoderDiffGenerator) GenerateConcreteDiff(_ context.Context, req complexityapp.CoderDiffRequest) (complexityapp.CoderDiffResult, error) {
	s.requests = append(s.requests, req)
	if s.err != nil {
		return complexityapp.CoderDiffResult{}, s.err
	}
	return s.result, nil
}

type blockingComplexityCoderDiffGenerator struct {
	requests []complexityapp.CoderDiffRequest
}

func (s *blockingComplexityCoderDiffGenerator) GenerateConcreteDiff(ctx context.Context, req complexityapp.CoderDiffRequest) (complexityapp.CoderDiffResult, error) {
	s.requests = append(s.requests, req)
	<-ctx.Done()
	return complexityapp.CoderDiffResult{}, ctx.Err()
}

type stubComplexityDCITraceStore struct {
	traces []domaindci.SearchTrace
}

func (s stubComplexityDCITraceStore) ListRecent(_ context.Context, _ int) ([]domaindci.SearchTrace, error) {
	return s.traces, nil
}

type stubComplexitySkillBootstrap struct {
	tasks []domainskill.TaskContext
	used  [][]string
}

func (s *stubComplexitySkillBootstrap) Record(_ context.Context, task domainskill.TaskContext, usedSkillIDs []string) ([]domainskill.SkillTriggerLog, error) {
	s.tasks = append(s.tasks, task)
	s.used = append(s.used, append([]string(nil), usedSkillIDs...))
	return []domainskill.SkillTriggerLog{{
		EventID: "evt_skill_1",
		SkillID: "core.codebase-complexity-hotspot",
		Status:  domainskill.TriggerStatusTriggered,
	}}, nil
}

type stubComplexityWorkstreamArtifactSink struct {
	artifacts []domainworkstream.Artifact
	goals     []domainworkstream.Goal
}

func (s *stubComplexityWorkstreamArtifactSink) SaveGoal(_ context.Context, item domainworkstream.Goal) error {
	if err := domainworkstream.ValidateGoal(item); err != nil {
		return err
	}
	s.goals = append(s.goals, item)
	return nil
}

func (s *stubComplexityWorkstreamArtifactSink) SaveArtifact(_ context.Context, item domainworkstream.Artifact) error {
	if err := domainworkstream.ValidateArtifact(item); err != nil {
		return err
	}
	s.artifacts = append(s.artifacts, item)
	return nil
}

type stubComplexitySandboxPromotionSink struct {
	promotions []domainsandbox.PromotionRequest
	gateLogs   []domainsandbox.PromotionGateLog
	artifacts  []domainsandbox.SandboxArtifact
}

func (s *stubComplexitySandboxPromotionSink) SavePromotionRequest(_ context.Context, req domainsandbox.PromotionRequest) error {
	s.promotions = append(s.promotions, req)
	return nil
}

func (s *stubComplexitySandboxPromotionSink) SavePromotionGateLog(_ context.Context, log domainsandbox.PromotionGateLog) error {
	s.gateLogs = append(s.gateLogs, log)
	return nil
}

func (s *stubComplexitySandboxPromotionSink) SaveSandboxArtifact(_ context.Context, artifact domainsandbox.SandboxArtifact) error {
	s.artifacts = append(s.artifacts, artifact)
	return nil
}

func TestHandleComplexityHotspotStatus(t *testing.T) {
	store := &stubComplexityHotspotStore{
		scans: []domaincomplexity.ScanEvent{{
			ScanID: "scan_1",
			Repo:   "repo",
			Mode:   "report_only",
			Status: "completed",
		}},
		hotspots: []domaincomplexity.Hotspot{{
			HotspotID:           "hot_1",
			ScanID:              "scan_1",
			FilePath:            "src/app.go",
			HotspotType:         "nested_loop",
			EstimatedComplexity: "O(n^2)",
			RiskLevel:           "medium",
			Summary:             "nested loop",
		}},
	}
	req := httptest.NewRequest(http.MethodGet, "/viewer/complexity-hotspots", nil)
	rec := httptest.NewRecorder()
	HandleComplexityHotspotStatus(store).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body["hotspots"].([]any)) != 1 {
		t.Fatalf("body=%#v", body)
	}
}

func TestHandleComplexityHotspotScanStoresResult(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &stubComplexityHotspotStore{}
	analyzer := &stubComplexityAnalyzer{result: domaincomplexity.ScanResult{
		Scan: domaincomplexity.ScanEvent{
			ScanID:        "scan_1",
			Repo:          "repo",
			Mode:          "report_only",
			FilesScanned:  1,
			HotspotsFound: 1,
			Status:        "completed",
			CreatedAt:     now,
		},
		Hotspots: []domaincomplexity.Hotspot{{
			HotspotID:           "hot_1",
			ScanID:              "scan_1",
			FilePath:            "src/app.go",
			HotspotType:         "nested_loop",
			EstimatedComplexity: "O(n^2)",
			RiskLevel:           "medium",
			Summary:             "nested loop",
			CreatedAt:           now,
		}},
		Evidence: []domaincomplexity.HotspotEvidence{{
			EvidenceID: "ev_1",
			HotspotID:  "hot_1",
			FilePath:   "src/app.go",
			Snippet:    "for",
			CreatedAt:  now,
		}},
	}}
	payload := []byte(`{"scan_id":"scan_1","repo":"repo","root_path":"."}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/complexity-hotspots/scan", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	HandleComplexityHotspotScan(store, analyzer, nil, nil, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.scans) != 1 || len(store.hotspots) != 1 || len(store.evidence) != 1 {
		t.Fatalf("store=%#v", store)
	}
	if len(store.reports) != 1 || store.reports[0].Type != "complexity_hotspot_report" {
		t.Fatalf("reports=%#v", store.reports)
	}
	if !bytes.Contains([]byte(store.reports[0].Content), []byte("Complexity Hotspot Report")) {
		t.Fatalf("report content=%q", store.reports[0].Content)
	}
}

func TestHandleComplexityHotspotScanRecordsSkillBootstrap(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &stubComplexityHotspotStore{}
	analyzer := &stubComplexityAnalyzer{result: domaincomplexity.ScanResult{
		Scan: domaincomplexity.ScanEvent{
			ScanID:    "scan_1",
			Repo:      "repo",
			Mode:      "report_only",
			Status:    "completed",
			CreatedAt: now,
		},
	}}
	skills := &stubComplexitySkillBootstrap{}
	payload := []byte(`{"scan_id":"scan_1","repo":"repo","root_path":".","workstream_id":"ws_1","scan_scope":["internal"]}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/complexity-hotspots/scan", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	HandleComplexityHotspotScan(store, analyzer, skills, nil, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(skills.tasks) != 1 {
		t.Fatalf("skill tasks=%#v", skills.tasks)
	}
	if skills.tasks[0].Intent != "complexity_hotspot_scan" || skills.tasks[0].WorkstreamID != "ws_1" {
		t.Fatalf("skill task=%#v", skills.tasks[0])
	}
	if len(skills.used) != 1 || len(skills.used[0]) != 1 || skills.used[0][0] != "core.codebase-complexity-hotspot" {
		t.Fatalf("used=%#v", skills.used)
	}
}

func TestHandleComplexityHotspotScanRegistersWorkstreamArtifact(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &stubComplexityHotspotStore{}
	workstreamSink := &stubComplexityWorkstreamArtifactSink{}
	analyzer := &stubComplexityAnalyzer{result: domaincomplexity.ScanResult{
		Scan: domaincomplexity.ScanEvent{
			ScanID:       "scan_1",
			WorkstreamID: "ws_1",
			Repo:         "repo",
			Mode:         "report_only",
			Status:       "completed",
			CreatedAt:    now,
			CompletedAt:  now,
		},
	}}
	payload := []byte(`{"scan_id":"scan_1","repo":"repo","root_path":".","workstream_id":"ws_1"}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/complexity-hotspots/scan", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	HandleComplexityHotspotScan(store, analyzer, nil, workstreamSink, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(workstreamSink.artifacts) != 1 {
		t.Fatalf("workstream artifacts=%#v", workstreamSink.artifacts)
	}
	if workstreamSink.artifacts[0].WorkstreamID != "ws_1" || workstreamSink.artifacts[0].Status != "pending_review" || workstreamSink.artifacts[0].Type != "complexity_hotspot_report" {
		t.Fatalf("unexpected workstream artifact=%#v", workstreamSink.artifacts[0])
	}
}

func TestHandleComplexityHotspotScanDerivesCandidatePatternsFromDCITrace(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &stubComplexityHotspotStore{}
	analyzer := &stubComplexityAnalyzer{result: domaincomplexity.ScanResult{
		Scan: domaincomplexity.ScanEvent{
			ScanID:    "scan_1",
			Repo:      "repo",
			Mode:      "report_only",
			Status:    "completed",
			CreatedAt: now,
		},
	}}
	dciStore := stubComplexityDCITraceStore{traces: []domaindci.SearchTrace{{
		EventID:   "evt_dci_1",
		UserQuery: "heavyLookup の repeated lookup を探す",
		Status:    "completed",
		Steps: []domaindci.SearchStep{{
			Tool:        "rg",
			CommandText: `rg "orders.find|users.find" internal/application`,
			FilePath:    "internal/application/example/orders.go",
			Status:      "ok",
			CreatedAt:   now,
		}},
	}}}
	payload := []byte(`{"scan_id":"scan_1","repo":"repo","root_path":".","candidate_patterns":["manualPattern"],"auto_candidate_patterns":true}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/complexity-hotspots/scan", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	HandleComplexityHotspotScan(store, analyzer, nil, nil, dciStore).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(analyzer.requests) != 1 {
		t.Fatalf("requests=%#v", analyzer.requests)
	}
	patterns := analyzer.requests[0].CandidatePatterns
	for _, want := range []string{"manualPattern", "heavyLookup", "orders.find", "users.find", "orders"} {
		if !containsString(patterns, want) {
			t.Fatalf("patterns=%#v missing %q", patterns, want)
		}
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body["candidate_patterns"].([]any)) == 0 {
		t.Fatalf("missing candidate_patterns in response: %#v", body)
	}
}

func TestHandleComplexityHotspotScanAutoCandidatePatternsRequiresDCITraceStore(t *testing.T) {
	store := &stubComplexityHotspotStore{}
	analyzer := &stubComplexityAnalyzer{}
	payload := []byte(`{"scan_id":"scan_1","repo":"repo","root_path":".","auto_candidate_patterns":true}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/complexity-hotspots/scan", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	HandleComplexityHotspotScan(store, analyzer, nil, nil, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(analyzer.requests) != 0 {
		t.Fatalf("analyzer should not run without dci trace store: %#v", analyzer.requests)
	}
}

func TestHandleComplexityHotspotProposalCreatesGoalAndPendingArtifact(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &stubComplexityHotspotStore{hotspots: []domaincomplexity.Hotspot{{
		HotspotID:            "hot_1",
		ScanID:               "scan_1",
		FilePath:             "internal/application/example.go",
		LineStart:            12,
		HotspotType:          "nested_lookup",
		EstimatedComplexity:  "O(n*m)",
		EstimatedAfter:       "O(n)",
		RiskLevel:            "medium",
		Summary:              "map path contains find lookup",
		SuggestedImprovement: "Map lookupへ変更する案を検討する",
		RequiredTests:        []string{"重複データ", "順序が重要なデータ"},
		CreatedAt:            now,
	}}}
	workstreamSink := &stubComplexityWorkstreamArtifactSink{}
	payload := []byte(`{"hotspot_id":"hot_1","workstream_id":"ws_1"}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/complexity-hotspots/proposals", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	HandleComplexityHotspotProposal(store, workstreamSink).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(workstreamSink.goals) != 1 {
		t.Fatalf("goals=%#v", workstreamSink.goals)
	}
	goal := workstreamSink.goals[0]
	if goal.WorkstreamID != "ws_1" || goal.Status != domainworkstream.StatusWaiting {
		t.Fatalf("goal=%#v", goal)
	}
	if !containsString(goal.Verification, "重複データ") {
		t.Fatalf("goal verification=%#v", goal.Verification)
	}
	if len(workstreamSink.artifacts) != 1 {
		t.Fatalf("artifacts=%#v", workstreamSink.artifacts)
	}
	artifact := workstreamSink.artifacts[0]
	if artifact.WorkstreamID != "ws_1" || artifact.Type != "complexity_patch_proposal_request" || artifact.Status != "pending_review" {
		t.Fatalf("artifact=%#v", artifact)
	}
	if len(store.reports) != 2 {
		t.Fatalf("proposal reports=%#v", store.reports)
	}
	if store.reports[0].Type != "complexity_patch_proposal" || store.reports[0].Status != "pending_review" {
		t.Fatalf("proposal report=%#v", store.reports[0])
	}
	if store.reports[1].Type != "complexity_coder_diff_request" || store.reports[1].Status != "pending_review" {
		t.Fatalf("coder diff request=%#v", store.reports[1])
	}
	for _, want := range []string{"Complexity Patch Proposal", "Human approval is required", "Map lookup", "External PR Review Checklist", "Migration / High-risk Review Checklist", "one PR to one hotspot"} {
		if !bytes.Contains([]byte(store.reports[0].Content), []byte(want)) {
			t.Fatalf("proposal report missing %q:\n%s", want, store.reports[0].Content)
		}
	}
	for _, want := range []string{"Coder Concrete Diff Request", "Generate one minimal concrete diff proposal", "Sandbox Promotion Gate", "External PR Review Checklist", "Migration / High-risk Review Checklist"} {
		if !bytes.Contains([]byte(store.reports[1].Content), []byte(want)) {
			t.Fatalf("coder diff request missing %q:\n%s", want, store.reports[1].Content)
		}
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["patch_applied"] != false || body["human_approval_required"] != true {
		t.Fatalf("body=%#v", body)
	}
	if body["proposal_artifact"] == nil {
		t.Fatalf("missing proposal_artifact in response: %#v", body)
	}
	if body["coder_diff_request"] == nil {
		t.Fatalf("missing coder_diff_request in response: %#v", body)
	}
}

func TestHandleComplexityHotspotProposalBranchesHighRiskToSeparateReview(t *testing.T) {
	store := &stubComplexityHotspotStore{hotspots: []domaincomplexity.Hotspot{{
		HotspotID:            "hot_1",
		ScanID:               "scan_1",
		FilePath:             "internal/application/example.go",
		LineStart:            12,
		HotspotType:          "n_plus_one_candidate",
		EstimatedComplexity:  "O(n*io)",
		EstimatedAfter:       "O(io)",
		RiskLevel:            "high",
		Summary:              "loop contains DB access",
		SuggestedImprovement: "batch query を検討する",
		RequiredTests:        []string{"DB query count"},
		CreatedAt:            time.Now().UTC(),
	}}}
	workstreamSink := &stubComplexityWorkstreamArtifactSink{}
	payload := []byte(`{"hotspot_id":"hot_1","workstream_id":"ws_1"}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/complexity-hotspots/proposals", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	HandleComplexityHotspotProposal(store, workstreamSink).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(workstreamSink.goals) != 2 {
		t.Fatalf("goals=%#v", workstreamSink.goals)
	}
	reviewGoal := workstreamSink.goals[1]
	if reviewGoal.Status != domainworkstream.StatusWaiting || !containsString(reviewGoal.SuccessCriteria, "対象 hotspot だけを扱う別 Goal / PR として分離する") {
		t.Fatalf("reviewGoal=%#v", reviewGoal)
	}
	if len(workstreamSink.artifacts) != 2 {
		t.Fatalf("artifacts=%#v", workstreamSink.artifacts)
	}
	reviewArtifact := workstreamSink.artifacts[1]
	if reviewArtifact.Type != "complexity_high_risk_review_request" || reviewArtifact.Status != "pending_review" {
		t.Fatalf("reviewArtifact=%#v", reviewArtifact)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["high_risk_review_goal"] == nil || body["high_risk_review_artifact"] == nil || body["pr_branch_required"] != true {
		t.Fatalf("body=%#v", body)
	}
}

func TestHandleComplexityHotspotProposalCreatesSandboxPromotionDraft(t *testing.T) {
	store := &stubComplexityHotspotStore{hotspots: []domaincomplexity.Hotspot{{
		HotspotID:           "hot_1",
		ScanID:              "scan_1",
		FilePath:            "internal/application/example.go",
		LineStart:           12,
		HotspotType:         "nested_lookup",
		EstimatedComplexity: "O(n*m)",
		EstimatedAfter:      "O(n)",
		RiskLevel:           "medium",
		Summary:             "map path contains find lookup",
		RequiredTests:       []string{"重複データ"},
		CreatedAt:           time.Now().UTC(),
	}}}
	workstreamSink := &stubComplexityWorkstreamArtifactSink{}
	sandboxSink := &stubComplexitySandboxPromotionSink{}
	payload := []byte(`{
		"hotspot_id":"hot_1",
		"workstream_id":"ws_1",
		"sandbox_id":"sbx_1",
		"diff_path":"sandbox/ws_1/diff/hot_1.patch",
		"test_result_path":"sandbox/ws_1/reports/test.txt",
		"rollback_plan_path":"sandbox/ws_1/reports/rollback.md",
		"post_apply_verification_path":"sandbox/ws_1/reports/post_apply.md",
		"human_approval_status":"granted"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/complexity-hotspots/proposals", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	HandleComplexityHotspotProposalWithSandbox(store, workstreamSink, sandboxSink).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(sandboxSink.promotions) != 1 {
		t.Fatalf("promotions=%#v", sandboxSink.promotions)
	}
	promotion := sandboxSink.promotions[0]
	if promotion.SandboxID != "sbx_1" || promotion.WorkstreamID != "ws_1" || promotion.TargetPath != "internal/application/example.go" {
		t.Fatalf("promotion=%#v", promotion)
	}
	if promotion.RequestedBy != "complexity_hotspot" || promotion.RiskLevel != "medium" || promotion.HumanApprovalStatus != domainsandbox.ApprovalGranted {
		t.Fatalf("promotion metadata=%#v", promotion)
	}
	if len(sandboxSink.gateLogs) != 1 {
		t.Fatalf("gateLogs=%#v", sandboxSink.gateLogs)
	}
	if sandboxSink.gateLogs[0].GateStatus != domainsandbox.GateStatusApproved {
		t.Fatalf("gate log=%#v", sandboxSink.gateLogs[0])
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["patch_applied"] != false || body["sandbox_promotion"] == nil || body["sandbox_decision"] == nil {
		t.Fatalf("body=%#v", body)
	}
	decision := body["sandbox_decision"].(map[string]any)
	if decision["status"] != domainsandbox.GateStatusApproved {
		t.Fatalf("decision=%#v", decision)
	}
}

func TestHandleComplexityHotspotProposalCreatesNeedsReviewSandboxPromotionWhenDiffMissing(t *testing.T) {
	store := &stubComplexityHotspotStore{hotspots: []domaincomplexity.Hotspot{{
		HotspotID:           "hot_1",
		ScanID:              "scan_1",
		FilePath:            "internal/application/example.go",
		HotspotType:         "nested_lookup",
		EstimatedComplexity: "O(n*m)",
		RiskLevel:           "medium",
		Summary:             "map path contains find lookup",
		CreatedAt:           time.Now().UTC(),
	}}}
	workstreamSink := &stubComplexityWorkstreamArtifactSink{}
	sandboxSink := &stubComplexitySandboxPromotionSink{}
	payload := []byte(`{"hotspot_id":"hot_1","workstream_id":"ws_1","sandbox_id":"sbx_1"}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/complexity-hotspots/proposals", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	HandleComplexityHotspotProposalWithSandbox(store, workstreamSink, sandboxSink).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(sandboxSink.promotions) != 1 || len(sandboxSink.gateLogs) != 1 {
		t.Fatalf("promotions=%#v gateLogs=%#v", sandboxSink.promotions, sandboxSink.gateLogs)
	}
	if sandboxSink.promotions[0].HumanApprovalStatus != domainsandbox.ApprovalPending {
		t.Fatalf("promotion=%#v", sandboxSink.promotions[0])
	}
	if sandboxSink.gateLogs[0].GateStatus != domainsandbox.GateStatusNeedsMoreTest {
		t.Fatalf("gate log=%#v", sandboxSink.gateLogs[0])
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	decision := body["sandbox_decision"].(map[string]any)
	missing := decision["missing_requirements"].([]any)
	for _, want := range []string{"diff_path", "test_result_path", "rollback_plan_path", "human_approval"} {
		found := false
		for _, got := range missing {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing requirements=%#v missing %q", missing, want)
		}
	}
}

func TestHandleComplexityHotspotProposalWithSandboxStoreUnavailableHasNoPartialWrites(t *testing.T) {
	store := &stubComplexityHotspotStore{hotspots: []domaincomplexity.Hotspot{{
		HotspotID:           "hot_1",
		ScanID:              "scan_1",
		FilePath:            "internal/application/example.go",
		HotspotType:         "nested_lookup",
		EstimatedComplexity: "O(n*m)",
		RiskLevel:           "medium",
		Summary:             "map path contains find lookup",
		CreatedAt:           time.Now().UTC(),
	}}}
	workstreamSink := &stubComplexityWorkstreamArtifactSink{}
	payload := []byte(`{"hotspot_id":"hot_1","workstream_id":"ws_1","sandbox_id":"sbx_1"}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/complexity-hotspots/proposals", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	HandleComplexityHotspotProposalWithSandbox(store, workstreamSink, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.reports) != 0 || len(workstreamSink.goals) != 0 || len(workstreamSink.artifacts) != 0 {
		t.Fatalf("unexpected partial writes reports=%#v goals=%#v artifacts=%#v", store.reports, workstreamSink.goals, workstreamSink.artifacts)
	}
}

func TestHandleComplexityHotspotConcreteDiffStoresReviewOnlyArtifact(t *testing.T) {
	store := &stubComplexityHotspotStore{hotspots: []domaincomplexity.Hotspot{{
		HotspotID:           "hot_1",
		ScanID:              "scan_1",
		FilePath:            "internal/application/example.go",
		HotspotType:         "nested_lookup",
		EstimatedComplexity: "O(n*m)",
		RiskLevel:           "medium",
		Summary:             "map path contains find lookup",
		CreatedAt:           time.Now().UTC(),
	}}}
	workstreamSink := &stubComplexityWorkstreamArtifactSink{}
	payload := []byte(`{
		"hotspot_id":"hot_1",
		"workstream_id":"ws_1",
		"concrete_diff":"diff --git a/internal/application/example.go b/internal/application/example.go\n--- a/internal/application/example.go\n+++ b/internal/application/example.go\n@@ -1 +1 @@\n-old\n+new",
		"test_result_path":"sandbox/ws_1/reports/test.txt",
		"rollback_plan_path":"sandbox/ws_1/reports/rollback.md"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/complexity-hotspots/concrete-diffs", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	HandleComplexityHotspotConcreteDiff(store, workstreamSink).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.reports) != 1 {
		t.Fatalf("reports=%#v", store.reports)
	}
	report := store.reports[0]
	if report.Type != "complexity_concrete_diff_proposal" || report.Status != "pending_review" {
		t.Fatalf("report=%#v", report)
	}
	for _, want := range []string{"Complexity Concrete Diff Proposal", "Patch applied: `false`", "Human approval required: `true`", "```diff", "Sandbox Promotion Gate"} {
		if !bytes.Contains([]byte(report.Content), []byte(want)) {
			t.Fatalf("report missing %q:\n%s", want, report.Content)
		}
	}
	if len(workstreamSink.artifacts) != 1 || workstreamSink.artifacts[0].Type != "complexity_concrete_diff_review" {
		t.Fatalf("workstream artifacts=%#v", workstreamSink.artifacts)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["patch_applied"] != false || body["human_approval_required"] != true || body["concrete_diff_artifact"] == nil {
		t.Fatalf("body=%#v", body)
	}
}

func TestHandleComplexityHotspotConcreteDiffWithSandboxStoreUnavailableHasNoPartialWrites(t *testing.T) {
	store := &stubComplexityHotspotStore{hotspots: []domaincomplexity.Hotspot{{
		HotspotID:           "hot_1",
		ScanID:              "scan_1",
		FilePath:            "internal/application/example.go",
		HotspotType:         "nested_lookup",
		EstimatedComplexity: "O(n*m)",
		RiskLevel:           "medium",
		Summary:             "map path contains find lookup",
		CreatedAt:           time.Now().UTC(),
	}}}
	workstreamSink := &stubComplexityWorkstreamArtifactSink{}
	payload := []byte(`{
		"hotspot_id":"hot_1",
		"workstream_id":"ws_1",
		"sandbox_id":"sbx_1",
		"concrete_diff":"diff --git a/internal/application/example.go b/internal/application/example.go\n--- a/internal/application/example.go\n+++ b/internal/application/example.go\n@@ -1 +1 @@\n-old\n+new"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/complexity-hotspots/concrete-diffs", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	HandleComplexityHotspotConcreteDiffWithSandbox(store, workstreamSink, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.reports) != 0 || len(workstreamSink.artifacts) != 0 {
		t.Fatalf("unexpected partial writes reports=%#v artifacts=%#v", store.reports, workstreamSink.artifacts)
	}
}

func TestHandleComplexityHotspotConcreteDiffCreatesSandboxPromotionGate(t *testing.T) {
	store := &stubComplexityHotspotStore{hotspots: []domaincomplexity.Hotspot{{
		HotspotID:           "hot_1",
		ScanID:              "scan_1",
		FilePath:            "internal/application/example.go",
		HotspotType:         "nested_lookup",
		EstimatedComplexity: "O(n*m)",
		RiskLevel:           "medium",
		Summary:             "map path contains find lookup",
		CreatedAt:           time.Now().UTC(),
	}}}
	sandboxSink := &stubComplexitySandboxPromotionSink{}
	payload := []byte(`{
		"hotspot_id":"hot_1",
		"workstream_id":"ws_1",
		"sandbox_id":"sbx_1",
		"diff_path":"sandbox/ws_1/diff/hot_1.patch",
		"test_result_path":"sandbox/ws_1/reports/test.txt",
		"rollback_plan_path":"sandbox/ws_1/reports/rollback.md",
		"post_apply_verification_path":"sandbox/ws_1/reports/post_apply.md",
		"human_approval_status":"granted",
		"concrete_diff":"diff --git a/internal/application/example.go b/internal/application/example.go\n--- a/internal/application/example.go\n+++ b/internal/application/example.go\n@@ -1 +1 @@\n-old\n+new"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/complexity-hotspots/concrete-diffs", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	HandleComplexityHotspotConcreteDiffWithSandbox(store, nil, sandboxSink).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(sandboxSink.promotions) != 1 || sandboxSink.promotions[0].RequestedBy != "complexity_concrete_diff" {
		t.Fatalf("promotions=%#v", sandboxSink.promotions)
	}
	if len(sandboxSink.gateLogs) != 1 || sandboxSink.gateLogs[0].GateStatus != domainsandbox.GateStatusApproved {
		t.Fatalf("gateLogs=%#v", sandboxSink.gateLogs)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	decision := body["sandbox_decision"].(map[string]any)
	if decision["status"] != domainsandbox.GateStatusApproved {
		t.Fatalf("decision=%#v", decision)
	}
}

func TestHandleComplexityHotspotConcreteDiffRejectsWrongFileScope(t *testing.T) {
	store := &stubComplexityHotspotStore{hotspots: []domaincomplexity.Hotspot{{
		HotspotID:           "hot_1",
		ScanID:              "scan_1",
		FilePath:            "internal/application/example.go",
		HotspotType:         "nested_lookup",
		EstimatedComplexity: "O(n*m)",
		RiskLevel:           "medium",
		Summary:             "map path contains find lookup",
		CreatedAt:           time.Now().UTC(),
	}}}
	payload := []byte(`{
		"hotspot_id":"hot_1",
		"concrete_diff":"diff --git a/internal/application/other.go b/internal/application/other.go\n--- a/internal/application/other.go\n+++ b/internal/application/other.go\n@@ -1 +1 @@\n-old\n+new"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/complexity-hotspots/concrete-diffs", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	HandleComplexityHotspotConcreteDiff(store, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleComplexityHotspotCoderDiffGeneratesReviewOnlyArtifact(t *testing.T) {
	store := &stubComplexityHotspotStore{hotspots: []domaincomplexity.Hotspot{{
		HotspotID:           "hot_1",
		ScanID:              "scan_1",
		FilePath:            "internal/application/example.go",
		HotspotType:         "repeated_lookup",
		EstimatedComplexity: "O(n*m)",
		RiskLevel:           "medium",
		Summary:             "repeated lookup",
	}}, evidence: []domaincomplexity.HotspotEvidence{{
		EvidenceID: "ev_1",
		HotspotID:  "hot_1",
		FilePath:   "internal/application/example.go",
		LineStart:  1,
		LineEnd:    3,
		Snippet:    "for _, item := range items {\n\t_ = item\n}",
		Reason:     "loop evidence",
	}}}
	workstreamSink := &stubComplexityWorkstreamArtifactSink{}
	sandboxSink := &stubComplexitySandboxPromotionSink{}
	diff := "diff --git a/internal/application/example.go b/internal/application/example.go\n--- a/internal/application/example.go\n+++ b/internal/application/example.go\n@@ -1 +1 @@\n-old\n+new"
	generator := &stubComplexityCoderDiffGenerator{result: complexityapp.CoderDiffResult{
		JobID:        "job_1",
		Prompt:       "prompt",
		RawResponse:  "```diff\n" + diff + "\n```",
		ConcreteDiff: diff,
	}}
	payload := []byte(`{
		"hotspot_id":"hot_1",
		"workstream_id":"ws_1",
		"sandbox_id":"sbx_1",
		"diff_path":"sandbox/ws_1/diff/hot_1.patch"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/complexity-hotspots/coder-diffs", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	HandleComplexityHotspotCoderDiffWithSandbox(store, generator, workstreamSink, sandboxSink).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(generator.requests) != 1 || generator.requests[0].Hotspot.HotspotID != "hot_1" {
		t.Fatalf("generator requests=%#v", generator.requests)
	}
	if len(generator.requests[0].Evidence) != 1 || generator.requests[0].Evidence[0].EvidenceID != "ev_1" {
		t.Fatalf("generator evidence=%#v", generator.requests[0].Evidence)
	}
	if len(store.reports) != 1 || store.reports[0].Type != "complexity_concrete_diff_proposal" {
		t.Fatalf("reports=%#v", store.reports)
	}
	if len(workstreamSink.artifacts) != 1 || workstreamSink.artifacts[0].Type != "complexity_concrete_diff_review" {
		t.Fatalf("workstream artifacts=%#v", workstreamSink.artifacts)
	}
	if len(sandboxSink.promotions) != 1 || sandboxSink.promotions[0].RequestedBy != "complexity_concrete_diff" {
		t.Fatalf("sandbox promotions=%#v", sandboxSink.promotions)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json decode: %v", err)
	}
	if body["patch_applied"] != false || body["human_approval_required"] != true || body["coder_result"] == nil {
		t.Fatalf("unexpected response=%#v", body)
	}
}

func TestHandleComplexityHotspotCoderDiffRejectsUnavailableGeneratorBeforeSaving(t *testing.T) {
	store := &stubComplexityHotspotStore{hotspots: []domaincomplexity.Hotspot{{
		HotspotID:   "hot_1",
		ScanID:      "scan_1",
		FilePath:    "internal/application/example.go",
		HotspotType: "repeated_lookup",
		RiskLevel:   "medium",
		Summary:     "repeated lookup",
	}}}
	workstreamSink := &stubComplexityWorkstreamArtifactSink{}
	sandboxSink := &stubComplexitySandboxPromotionSink{}
	payload := []byte(`{"hotspot_id":"hot_1","workstream_id":"ws_1","sandbox_id":"sbx_1"}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/complexity-hotspots/coder-diffs", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	HandleComplexityHotspotCoderDiffWithSandbox(store, nil, workstreamSink, sandboxSink).ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.reports) != 0 || len(workstreamSink.artifacts) != 0 || len(sandboxSink.promotions) != 0 {
		t.Fatalf("unexpected partial save reports=%#v artifacts=%#v promotions=%#v", store.reports, workstreamSink.artifacts, sandboxSink.promotions)
	}
}

func TestHandleComplexityHotspotCoderDiffRecordsGeneratorErrorAuditWithoutReviewApplyArtifacts(t *testing.T) {
	store := &stubComplexityHotspotStore{hotspots: []domaincomplexity.Hotspot{{
		HotspotID:   "hot_1",
		ScanID:      "scan_1",
		FilePath:    "internal/application/example.go",
		HotspotType: "repeated_lookup",
		RiskLevel:   "medium",
		Summary:     "repeated lookup",
	}}}
	generator := &stubComplexityCoderDiffGenerator{err: errors.New("coder output did not contain unified diff")}
	workstreamSink := &stubComplexityWorkstreamArtifactSink{}
	sandboxSink := &stubComplexitySandboxPromotionSink{}
	payload := []byte(`{"hotspot_id":"hot_1","workstream_id":"ws_1","sandbox_id":"sbx_1"}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/complexity-hotspots/coder-diffs", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	HandleComplexityHotspotCoderDiffWithSandbox(store, generator, workstreamSink, sandboxSink).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(generator.requests) != 1 {
		t.Fatalf("generator requests=%d", len(generator.requests))
	}
	if len(store.reports) != 1 {
		t.Fatalf("reports=%#v", store.reports)
	}
	report := store.reports[0]
	if report.Type != "complexity_coder_diff_failure" || report.Status != "failed" {
		t.Fatalf("failure report=%#v", report)
	}
	if !bytes.Contains([]byte(report.Content), []byte("coder output did not contain unified diff")) || !bytes.Contains([]byte(report.Content), []byte("Patch applied: `false`")) {
		t.Fatalf("failure report content=%s", report.Content)
	}
	if len(workstreamSink.artifacts) != 0 || len(sandboxSink.promotions) != 0 {
		t.Fatalf("unexpected apply artifacts=%#v promotions=%#v", workstreamSink.artifacts, sandboxSink.promotions)
	}
}

func TestHandleComplexityHotspotCoderDiffRecordsTimeoutAuditWithoutReviewApplyArtifacts(t *testing.T) {
	oldTimeout := complexityCoderDiffGenerationTimeout
	complexityCoderDiffGenerationTimeout = 10 * time.Millisecond
	t.Cleanup(func() { complexityCoderDiffGenerationTimeout = oldTimeout })

	store := &stubComplexityHotspotStore{hotspots: []domaincomplexity.Hotspot{{
		HotspotID:   "hot_1",
		ScanID:      "scan_1",
		FilePath:    "internal/application/example.go",
		HotspotType: "repeated_lookup",
		RiskLevel:   "medium",
		Summary:     "repeated lookup",
	}}}
	generator := &blockingComplexityCoderDiffGenerator{}
	workstreamSink := &stubComplexityWorkstreamArtifactSink{}
	sandboxSink := &stubComplexitySandboxPromotionSink{}
	payload := []byte(`{"hotspot_id":"hot_1","workstream_id":"ws_1","sandbox_id":"sbx_1"}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/complexity-hotspots/coder-diffs", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	HandleComplexityHotspotCoderDiffWithSandbox(store, generator, workstreamSink, sandboxSink).ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("complexity coder diff generation timed out")) {
		t.Fatalf("body=%q", rec.Body.String())
	}
	if len(generator.requests) != 1 {
		t.Fatalf("generator requests=%d", len(generator.requests))
	}
	if len(store.reports) != 1 {
		t.Fatalf("reports=%#v", store.reports)
	}
	report := store.reports[0]
	if report.Type != "complexity_coder_diff_failure" || report.Status != "failed" {
		t.Fatalf("failure report=%#v", report)
	}
	if !bytes.Contains([]byte(report.Content), []byte("complexity coder diff generation timed out")) || !bytes.Contains([]byte(report.Content), []byte("Patch applied: `false`")) {
		t.Fatalf("failure report content=%s", report.Content)
	}
	if len(workstreamSink.artifacts) != 0 || len(sandboxSink.promotions) != 0 {
		t.Fatalf("unexpected apply artifacts=%#v promotions=%#v", workstreamSink.artifacts, sandboxSink.promotions)
	}
}

func TestHandleComplexityHotspotProposalRejectsMissingWorkstream(t *testing.T) {
	store := &stubComplexityHotspotStore{hotspots: []domaincomplexity.Hotspot{{
		HotspotID:           "hot_1",
		ScanID:              "scan_1",
		FilePath:            "src/app.go",
		HotspotType:         "nested_loop",
		EstimatedComplexity: "O(n^2)",
		RiskLevel:           "medium",
		Summary:             "nested loop",
	}}}
	payload := []byte(`{"hotspot_id":"hot_1"}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/complexity-hotspots/proposals", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	HandleComplexityHotspotProposal(store, &stubComplexityWorkstreamArtifactSink{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
