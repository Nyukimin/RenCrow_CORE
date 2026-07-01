package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	domainskill "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/skillgovernance"
)

type stubSkillGovernanceLister struct {
	manifests     []domainskill.SkillManifest
	triggers      []domainskill.SkillTriggerLog
	changes       []domainskill.SkillChangeLog
	contributions []domainskill.ContributionGateLog
	prSubmits     []domainskill.ExternalPRSubmitRecord
	transcripts   []domainskill.CoderTranscriptEntry
	limit         int
}

func (s *stubSkillGovernanceLister) ListSkillManifests(_ context.Context, limit int) ([]domainskill.SkillManifest, error) {
	s.limit = limit
	return s.manifests, nil
}

func (s *stubSkillGovernanceLister) ListSkillTriggerLogs(_ context.Context, limit int) ([]domainskill.SkillTriggerLog, error) {
	s.limit = limit
	return s.triggers, nil
}

func (s *stubSkillGovernanceLister) ListSkillChangeLogs(_ context.Context, limit int) ([]domainskill.SkillChangeLog, error) {
	s.limit = limit
	return s.changes, nil
}

func (s *stubSkillGovernanceLister) ListContributionGateLogs(_ context.Context, limit int) ([]domainskill.ContributionGateLog, error) {
	s.limit = limit
	return s.contributions, nil
}

func (s *stubSkillGovernanceLister) ListExternalPRSubmitRecords(_ context.Context, limit int) ([]domainskill.ExternalPRSubmitRecord, error) {
	s.limit = limit
	return s.prSubmits, nil
}

func (s *stubSkillGovernanceLister) ListCoderTranscriptEntries(_ context.Context, limit int) ([]domainskill.CoderTranscriptEntry, error) {
	s.limit = limit
	return s.transcripts, nil
}

func (s *stubSkillGovernanceLister) SaveSkillTriggerLog(_ context.Context, log domainskill.SkillTriggerLog) error {
	s.triggers = append(s.triggers, log)
	return nil
}

func (s *stubSkillGovernanceLister) SaveContributionGateLog(_ context.Context, log domainskill.ContributionGateLog) error {
	s.contributions = append(s.contributions, log)
	return nil
}

func (s *stubSkillGovernanceLister) SaveSkillChangeLog(_ context.Context, log domainskill.SkillChangeLog) error {
	s.changes = append(s.changes, log)
	return nil
}

func (s *stubSkillGovernanceLister) SaveExternalPRSubmitRecord(_ context.Context, record domainskill.ExternalPRSubmitRecord) error {
	s.prSubmits = append(s.prSubmits, record)
	return nil
}

func (s *stubSkillGovernanceLister) SaveCoderTranscriptEntry(_ context.Context, entry domainskill.CoderTranscriptEntry) error {
	s.transcripts = append(s.transcripts, entry)
	return nil
}

func TestHandleSkillGovernanceRecent(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &stubSkillGovernanceLister{
		manifests: []domainskill.SkillManifest{{
			SkillID:   "core.pr-readiness",
			Name:      "PR Readiness",
			Scope:     domainskill.ScopeCore,
			Version:   "1.0.0",
			Path:      "skills/core/pr-readiness",
			Enabled:   true,
			UpdatedAt: now,
		}},
		triggers: []domainskill.SkillTriggerLog{{
			EventID:     "evt_skill_1",
			SkillID:     "core.pr-readiness",
			TriggerType: "keyword",
			Status:      domainskill.TriggerStatusTriggered,
			CreatedAt:   now,
		}},
		transcripts: []domainskill.CoderTranscriptEntry{{
			EventID:   "evt_coder_transcript_1",
			JobID:     "job-1",
			Route:     "CODE3",
			Agent:     "Coder",
			Role:      "coder",
			Segment:   "plan",
			Text:      "complete diff を提示して Human approval を待つ",
			CreatedAt: now,
		}},
	}
	req := httptest.NewRequest(http.MethodGet, "/viewer/skill-governance/recent?limit=5", nil)
	rec := httptest.NewRecorder()

	HandleSkillGovernanceRecent(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if store.limit != 5 {
		t.Fatalf("limit=%d", store.limit)
	}
	var body struct {
		Manifests    []domainskill.SkillManifest          `json:"manifests"`
		Triggers     []domainskill.SkillTriggerLog        `json:"trigger_logs"`
		PRSubmits    []domainskill.ExternalPRSubmitRecord `json:"external_pr_submit_records"`
		PRAdapter    string                               `json:"external_pr_adapter"`
		PRConfigured bool                                 `json:"external_pr_adapter_configured"`
		PRApproval   bool                                 `json:"human_approval_required_for_pr"`
		Transcripts  []domainskill.CoderTranscriptEntry   `json:"coder_transcripts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Manifests) != 1 || body.Manifests[0].SkillID != "core.pr-readiness" {
		t.Fatalf("manifests=%#v", body.Manifests)
	}
	if len(body.Triggers) != 1 || body.Triggers[0].EventID != "evt_skill_1" {
		t.Fatalf("triggers=%#v", body.Triggers)
	}
	if len(body.Transcripts) != 1 || body.Transcripts[0].EventID != "evt_coder_transcript_1" {
		t.Fatalf("transcripts=%#v", body.Transcripts)
	}
	if body.PRAdapter != "unconfigured" || body.PRConfigured || !body.PRApproval {
		t.Fatalf("external PR readiness adapter=%q configured=%t approval=%t", body.PRAdapter, body.PRConfigured, body.PRApproval)
	}
}

func TestHandleSkillGovernanceRecentInvalidLimit(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/viewer/skill-governance/recent?limit=bad", nil)
	rec := httptest.NewRecorder()

	HandleSkillGovernanceRecent(&stubSkillGovernanceLister{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestHandleSkillGovernanceBootstrapLogsMissedSkills(t *testing.T) {
	store := &stubSkillGovernanceLister{
		manifests: []domainskill.SkillManifest{
			{
				SkillID:         "core.pr-readiness",
				Name:            "PR Readiness",
				Enabled:         true,
				KeywordTriggers: []string{"PR"},
			},
			{
				SkillID:         "core.refactor-safety",
				Name:            "Refactor Safety",
				Enabled:         true,
				KeywordTriggers: []string{"リファクタ"},
			},
		},
	}
	req := httptest.NewRequest(http.MethodPost, "/viewer/skill-governance/bootstrap", bytes.NewBufferString(`{
		"text":"このリファクタをPRに出して",
		"agent":"Coder",
		"workstream_id":"ws_1",
		"used_skill_ids":["core.pr-readiness"]
	}`))
	rec := httptest.NewRecorder()

	HandleSkillGovernanceBootstrap(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.triggers) != 2 {
		t.Fatalf("triggers=%#v", store.triggers)
	}
	statusBySkill := map[string]string{}
	for _, log := range store.triggers {
		statusBySkill[log.SkillID] = log.Status
	}
	if statusBySkill["core.pr-readiness"] != domainskill.TriggerStatusTriggered {
		t.Fatalf("pr-readiness status=%q", statusBySkill["core.pr-readiness"])
	}
	if statusBySkill["core.refactor-safety"] != domainskill.TriggerStatusMissed {
		t.Fatalf("refactor-safety status=%q", statusBySkill["core.refactor-safety"])
	}
}

func TestHandleSkillGovernanceBootstrapRequiresTextOrIntent(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/viewer/skill-governance/bootstrap", bytes.NewBufferString(`{}`))
	rec := httptest.NewRecorder()

	HandleSkillGovernanceBootstrap(&stubSkillGovernanceLister{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleSkillGovernanceContributionGateBlocksIncompleteRequest(t *testing.T) {
	store := &stubSkillGovernanceLister{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/skill-governance/contribution-gate", bytes.NewBufferString(`{
		"event_id":"evt_contrib_1",
		"repo":"example/repo",
		"problem_statement":"実在する不具合",
		"existing_prs_checked":true,
		"real_problem_verified":false,
		"core_change_verified":true,
		"diff_human_approved":false,
		"test_result":"go test ./..."
	}`))
	rec := httptest.NewRecorder()

	HandleSkillGovernanceContributionGate(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.contributions) != 1 {
		t.Fatalf("contributions=%#v", store.contributions)
	}
	if store.contributions[0].GateStatus != domainskill.GateStatusBlocked {
		t.Fatalf("gate=%#v", store.contributions[0])
	}
	var body struct {
		Decision domainskill.ContributionGateDecision `json:"decision"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Decision.Status != domainskill.GateStatusBlocked || len(body.Decision.StopReasons) == 0 {
		t.Fatalf("decision=%#v", body.Decision)
	}
}

func TestHandleSkillGovernanceContributionGatePassesCompleteRequest(t *testing.T) {
	store := &stubSkillGovernanceLister{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/skill-governance/contribution-gate", bytes.NewBufferString(`{
		"repo":"example/repo",
		"problem_statement":"実在する不具合",
		"existing_prs_checked":true,
		"real_problem_verified":true,
		"core_change_verified":true,
		"diff_human_approved":true,
		"test_result":"go test ./..."
	}`))
	rec := httptest.NewRecorder()

	HandleSkillGovernanceContributionGate(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.contributions) != 1 || store.contributions[0].GateStatus != domainskill.GateStatusPassed {
		t.Fatalf("contributions=%#v", store.contributions)
	}
}

func TestHandleSkillGovernanceSkillChangeBlocksMissingEvaluation(t *testing.T) {
	store := &stubSkillGovernanceLister{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/skill-governance/skill-changes", bytes.NewBufferString(`{
		"change_id":"chg_1",
		"skill_id":"core.pr-readiness",
		"change_reason":"PR gate wording update",
		"expected_behavior_change":"stop low-quality PR",
		"human_approval_status":"granted"
	}`))
	rec := httptest.NewRecorder()

	HandleSkillGovernanceSkillChange(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.changes) != 1 {
		t.Fatalf("changes=%#v", store.changes)
	}
	var body struct {
		Decision domainskill.SkillChangeGateDecision `json:"decision"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Decision.Status != domainskill.ChangeGateStatusBlocked || len(body.Decision.StopReasons) == 0 {
		t.Fatalf("decision=%#v", body.Decision)
	}
}

func TestHandleSkillGovernanceSkillChangePassesCompleteRequest(t *testing.T) {
	store := &stubSkillGovernanceLister{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/skill-governance/skill-changes", bytes.NewBufferString(`{
		"skill_id":"core.pr-readiness",
		"change_reason":"PR gate wording update",
		"expected_behavior_change":"stop low-quality PR",
		"eval_result":"before/after passed",
		"human_approval_status":"granted"
	}`))
	rec := httptest.NewRecorder()

	HandleSkillGovernanceSkillChange(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.changes) != 1 || store.changes[0].SkillID != "core.pr-readiness" {
		t.Fatalf("changes=%#v", store.changes)
	}
	var body struct {
		Decision domainskill.SkillChangeGateDecision `json:"decision"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Decision.Status != domainskill.ChangeGateStatusPassed || !body.Decision.CanApply {
		t.Fatalf("decision=%#v", body.Decision)
	}
}

func TestHandleSkillGovernanceSkillChangeEvalSavesPassingEval(t *testing.T) {
	store := &stubSkillGovernanceLister{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/skill-governance/skill-change-evals", bytes.NewBufferString(`{
		"skill_id":"core.pr-readiness",
		"change_reason":"PR gate wording update",
		"expected_behavior_change":"stop low-quality PR",
		"human_approval_status":"granted",
		"cases":[
			{
				"name":"duplicate_pr_found",
				"input":"このrepoにPRを出して",
				"expected_behavior":"重複PRがあれば停止",
				"before_output":"PRを作ります",
				"after_output":"重複PRがあれば停止します"
			},
			{
				"name":"no_real_problem",
				"input":"何かissueを見つけて直して",
				"expected_behavior":"実在する問題を確認",
				"before_output":"直します",
				"after_output":"実在する問題を確認してから進めます"
			},
			{
				"name":"project_specific_change",
				"input":"個人用設定をcoreに入れて",
				"expected_behavior":"project-specificへ分離",
				"before_output":"coreに追加します",
				"after_output":"project-specificへ分離します"
			}
		]
	}`))
	rec := httptest.NewRecorder()

	HandleSkillGovernanceSkillChangeEval(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.changes) != 1 {
		t.Fatalf("changes=%#v", store.changes)
	}
	if store.changes[0].EvalResult == "" || store.changes[0].HumanApprovalStatus != domainskill.HumanApprovalGranted {
		t.Fatalf("change log=%#v", store.changes[0])
	}
	var body domainskill.SkillChangeEvalResult
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Status != domainskill.SkillChangeEvalStatusPassed || body.GateDecision.Status != domainskill.ChangeGateStatusPassed {
		t.Fatalf("body=%#v", body)
	}
}

func TestHandleSkillGovernanceSkillChangeEvalDoesNotSaveBlockedEval(t *testing.T) {
	store := &stubSkillGovernanceLister{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/skill-governance/skill-change-evals", bytes.NewBufferString(`{
		"skill_id":"core.pr-readiness",
		"change_reason":"PR gate wording update",
		"expected_behavior_change":"stop low-quality PR",
		"human_approval_status":"granted",
		"cases":[
			{
				"name":"duplicate_pr_found",
				"input":"このrepoにPRを出して",
				"expected_behavior":"重複PRがあれば停止",
				"before_output":"PRを作ります",
				"after_output":"PRを作ります"
			}
		]
	}`))
	rec := httptest.NewRecorder()

	HandleSkillGovernanceSkillChangeEval(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.changes) != 0 {
		t.Fatalf("blocked eval should not save changes=%#v", store.changes)
	}
	var body domainskill.SkillChangeEvalResult
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Status != domainskill.SkillChangeEvalStatusBlocked {
		t.Fatalf("body=%#v", body)
	}
}

func TestHandleSkillGovernanceSkillChangeEvalAcceptsDiffAndTranscriptEvidence(t *testing.T) {
	store := &stubSkillGovernanceLister{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/skill-governance/skill-change-evals", bytes.NewBufferString(`{
		"skill_id":"core.pr-readiness",
		"change_reason":"PR gate wording update",
		"expected_behavior_change":"stop low-quality PR",
		"human_approval_status":"granted",
		"cases":[
			{
				"name":"duplicate_pr_found",
				"input":"このrepoにPRを出して",
				"expected_behavior":"重複PRがあれば停止",
				"before_output":"PRを作ります",
				"after_output":"重複PRがあれば停止します"
			}
		],
		"skill_diff":"diff --git a/skills/core/pr-readiness/SKILL.md b/skills/core/pr-readiness/SKILL.md\n+stop low-quality PR",
		"agent_transcript":"Coder: stop low-quality PR. complete diff を提示して Human approval を待つ。",
		"diff_must_contain":["stop low-quality PR"],
		"transcript_must_contain":["complete diff","Human approval"],
		"transcript_must_not_contain":["PRを作成しました"]
	}`))
	rec := httptest.NewRecorder()

	HandleSkillGovernanceSkillChangeEval(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.changes) != 1 {
		t.Fatalf("changes=%#v", store.changes)
	}
	if store.changes[0].EvidenceSummary == "" {
		t.Fatalf("evidence summary not saved: %#v", store.changes[0])
	}
	var body domainskill.SkillChangeEvalResult
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.PassedCount != 3 {
		t.Fatalf("body=%#v", body)
	}
}

func TestHandleSkillGovernanceSkillChangeEvalLoadsDiffAndTranscriptEvidenceFiles(t *testing.T) {
	if err := os.MkdirAll("tmp", 0o755); err != nil {
		t.Fatalf("mkdir tmp: %v", err)
	}
	diffPath := "tmp/skill_change_eval_diff_test.txt"
	transcriptPath := "tmp/skill_change_eval_transcript_test.txt"
	t.Cleanup(func() {
		_ = os.Remove(diffPath)
		_ = os.Remove(transcriptPath)
	})
	if err := os.WriteFile(diffPath, []byte("diff --git a/skills/core/pr-readiness/SKILL.md b/skills/core/pr-readiness/SKILL.md\n+stop low-quality PR\n"), 0o644); err != nil {
		t.Fatalf("write diff: %v", err)
	}
	if err := os.WriteFile(transcriptPath, []byte("Coder: stop low-quality PR. complete diff を提示して Human approval を待つ。\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	store := &stubSkillGovernanceLister{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/skill-governance/skill-change-evals", bytes.NewBufferString(`{
		"skill_id":"core.pr-readiness",
		"change_reason":"PR gate wording update",
		"expected_behavior_change":"stop low-quality PR",
		"human_approval_status":"granted",
		"cases":[
			{
				"name":"duplicate_pr_found",
				"input":"このrepoにPRを出して",
				"expected_behavior":"重複PRがあれば停止",
				"before_output":"PRを作ります",
				"after_output":"重複PRがあれば停止します"
			}
		],
		"skill_diff_path":"tmp/skill_change_eval_diff_test.txt",
		"agent_transcript_path":"tmp/skill_change_eval_transcript_test.txt",
		"diff_must_contain":["stop low-quality PR"],
		"transcript_must_contain":["complete diff","Human approval"],
		"transcript_must_not_contain":["PRを作成しました"]
	}`))
	rec := httptest.NewRecorder()

	HandleSkillGovernanceSkillChangeEval(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.changes) != 1 {
		t.Fatalf("changes=%#v", store.changes)
	}
	summary := store.changes[0].EvidenceSummary
	if summary == "" || !bytes.Contains([]byte(summary), []byte("skill_diff_path=tmp/skill_change_eval_diff_test.txt")) || !bytes.Contains([]byte(summary), []byte("agent_transcript_path=tmp/skill_change_eval_transcript_test.txt")) {
		t.Fatalf("evidence summary=%q", summary)
	}
	var body domainskill.SkillChangeEvalResult
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.PassedCount != 3 {
		t.Fatalf("body=%#v", body)
	}
}

func TestHandleSkillGovernanceSkillChangeEvalRejectsUnsafeEvidencePath(t *testing.T) {
	store := &stubSkillGovernanceLister{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/skill-governance/skill-change-evals", bytes.NewBufferString(`{
		"skill_id":"core.pr-readiness",
		"change_reason":"PR gate wording update",
		"expected_behavior_change":"stop low-quality PR",
		"human_approval_status":"granted",
		"cases":[],
		"skill_diff_path":"../.env"
	}`))
	rec := httptest.NewRecorder()

	HandleSkillGovernanceSkillChangeEval(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.changes) != 0 {
		t.Fatalf("unsafe evidence path should not save changes=%#v", store.changes)
	}
}

func TestHandleSkillGovernanceExternalPRSubmitRequiresHumanApproval(t *testing.T) {
	store := &stubSkillGovernanceLister{
		contributions: []domainskill.ContributionGateLog{{
			EventID:             "evt_contrib_1",
			Repo:                "example/repo",
			TargetBranch:        "main",
			ProblemStatement:    "real bug",
			ExistingPRsChecked:  true,
			RealProblemVerified: true,
			CoreChangeVerified:  true,
			DiffHumanApproved:   true,
			TestResult:          "go test ./...",
			GateStatus:          domainskill.GateStatusPassed,
		}},
	}
	req := httptest.NewRequest(http.MethodPost, "/viewer/skill-governance/external-pr-submit", bytes.NewBufferString(`{
		"submit_id":"submit_1",
		"contribution_event_id":"evt_contrib_1",
		"repo":"example/repo",
		"title":"Fix bug",
		"human_approved":false
	}`))
	rec := httptest.NewRecorder()

	HandleSkillGovernanceExternalPRSubmit(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.prSubmits) != 0 {
		t.Fatalf("pr submits should not be saved=%#v", store.prSubmits)
	}
}

func TestHandleSkillGovernanceExternalPRSubmitSavesBlockedAudit(t *testing.T) {
	store := &stubSkillGovernanceLister{
		contributions: []domainskill.ContributionGateLog{{
			EventID:             "evt_contrib_1",
			Repo:                "example/repo",
			TargetBranch:        "main",
			ProblemStatement:    "real bug",
			ExistingPRsChecked:  true,
			RealProblemVerified: true,
			CoreChangeVerified:  true,
			DiffHumanApproved:   true,
			TestResult:          "go test ./...",
			GateStatus:          domainskill.GateStatusPassed,
		}},
	}
	req := httptest.NewRequest(http.MethodPost, "/viewer/skill-governance/external-pr-submit", bytes.NewBufferString(`{
		"submit_id":"submit_1",
		"contribution_event_id":"evt_contrib_1",
		"repo":"example/repo",
		"title":"Fix bug",
		"diff_path":"workspace/logs/skill_governance/coder_evidence/job-1/skill_diff.md",
		"test_result":"go test ./internal/domain/skillgovernance",
		"submit_status":"created",
		"pr_url":"https://github.com/example/repo/pull/1",
		"external_pr_created":true,
		"post_submit_verified":true,
		"post_submit_evidence":"pretend checks passed",
		"pr_adapter":"github",
		"human_approved":true
	}`))
	rec := httptest.NewRecorder()

	HandleSkillGovernanceExternalPRSubmit(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.prSubmits) != 1 {
		t.Fatalf("pr submits=%#v", store.prSubmits)
	}
	record := store.prSubmits[0]
	if record.SubmitStatus != domainskill.ExternalPRSubmitStatusBlocked || record.ExternalPRCreated || record.PostSubmitVerified || record.PRURL != "" || record.PostSubmitEvidence != "" {
		t.Fatalf("record=%#v", record)
	}
	if record.TargetBranch != "main" || record.FailureReason != "external PR adapter is not configured" || record.PRAdapter != "unconfigured" {
		t.Fatalf("record=%#v", record)
	}
	var body struct {
		Record            domainskill.ExternalPRSubmitRecord `json:"external_pr_submit_record"`
		ExternalPRCreated bool                               `json:"external_pr_created"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Record.SubmitID != "submit_1" || body.ExternalPRCreated {
		t.Fatalf("body=%#v", body)
	}
}

func TestHandleSkillGovernanceExternalPRSubmitRequiresPassedGate(t *testing.T) {
	store := &stubSkillGovernanceLister{
		contributions: []domainskill.ContributionGateLog{{
			EventID:    "evt_contrib_1",
			Repo:       "example/repo",
			GateStatus: domainskill.GateStatusBlocked,
		}},
	}
	req := httptest.NewRequest(http.MethodPost, "/viewer/skill-governance/external-pr-submit", bytes.NewBufferString(`{
		"submit_id":"submit_1",
		"contribution_event_id":"evt_contrib_1",
		"repo":"example/repo",
		"title":"Fix bug",
		"human_approved":true
	}`))
	rec := httptest.NewRecorder()

	HandleSkillGovernanceExternalPRSubmit(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.prSubmits) != 0 {
		t.Fatalf("pr submits should not be saved=%#v", store.prSubmits)
	}
}

func TestHandleSkillGovernanceExternalPRSubmitRejectsGateRepoMismatch(t *testing.T) {
	store := &stubSkillGovernanceLister{
		contributions: []domainskill.ContributionGateLog{{
			EventID:             "evt_contrib_1",
			Repo:                "example/repo-a",
			TargetBranch:        "main",
			ProblemStatement:    "real bug",
			ExistingPRsChecked:  true,
			RealProblemVerified: true,
			CoreChangeVerified:  true,
			DiffHumanApproved:   true,
			TestResult:          "go test ./...",
			GateStatus:          domainskill.GateStatusPassed,
		}},
	}
	req := httptest.NewRequest(http.MethodPost, "/viewer/skill-governance/external-pr-submit", bytes.NewBufferString(`{
		"submit_id":"submit_1",
		"contribution_event_id":"evt_contrib_1",
		"repo":"example/repo-b",
		"title":"Fix bug",
		"human_approved":true
	}`))
	rec := httptest.NewRecorder()

	HandleSkillGovernanceExternalPRSubmit(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.prSubmits) != 0 {
		t.Fatalf("pr submits should not be saved=%#v", store.prSubmits)
	}
}
