package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	aiworkflowapp "github.com/Nyukimin/RenCrow_CORE/internal/application/aiworkflow"
	sandboxapp "github.com/Nyukimin/RenCrow_CORE/internal/application/sandbox"
	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	domainsandbox "github.com/Nyukimin/RenCrow_CORE/internal/domain/sandbox"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
)

type stubSandboxLister struct {
	sandboxes  []domainsandbox.SandboxRecord
	artifacts  []domainsandbox.SandboxArtifact
	promotions []domainsandbox.PromotionRequest
	gateLogs   []domainsandbox.PromotionGateLog
	limit      int
}

func (s *stubSandboxLister) ListSandboxes(_ context.Context, limit int) ([]domainsandbox.SandboxRecord, error) {
	s.limit = limit
	return s.sandboxes, nil
}

func (s *stubSandboxLister) ListSandboxArtifacts(_ context.Context, limit int) ([]domainsandbox.SandboxArtifact, error) {
	s.limit = limit
	return s.artifacts, nil
}

func (s *stubSandboxLister) ListPromotionRequests(_ context.Context, limit int) ([]domainsandbox.PromotionRequest, error) {
	s.limit = limit
	return s.promotions, nil
}

func (s *stubSandboxLister) ListPromotionGateLogs(_ context.Context, limit int) ([]domainsandbox.PromotionGateLog, error) {
	s.limit = limit
	return s.gateLogs, nil
}

type stubSandboxPromotionStore struct {
	promotions []domainsandbox.PromotionRequest
	gateLogs   []domainsandbox.PromotionGateLog
	artifacts  []domainsandbox.SandboxArtifact
}

type stubPostApplyVerifier struct {
	req    domainsandbox.PromotionApplyRequest
	result sandboxapp.PostApplyVerificationResult
	err    error
	called bool
}

type stubPromotionDiffApplier struct {
	req           domainsandbox.PromotionApplyRequest
	result        sandboxapp.PromotionDiffApplyResult
	previewResult sandboxapp.PromotionDiffPreviewResult
	err           error
	called        bool
}

func (s *stubPostApplyVerifier) RunPostApplyVerification(_ context.Context, req domainsandbox.PromotionApplyRequest) (sandboxapp.PostApplyVerificationResult, error) {
	s.called = true
	s.req = req
	if s.err != nil {
		return sandboxapp.PostApplyVerificationResult{}, s.err
	}
	return s.result, nil
}

func (s *stubPromotionDiffApplier) ApplyPromotionDiff(_ context.Context, req domainsandbox.PromotionApplyRequest) (sandboxapp.PromotionDiffApplyResult, error) {
	s.called = true
	s.req = req
	if s.err != nil {
		return sandboxapp.PromotionDiffApplyResult{}, s.err
	}
	return s.result, nil
}

func (s *stubPromotionDiffApplier) RollbackPromotionDiff(_ context.Context, req domainsandbox.PromotionApplyRequest) (sandboxapp.PromotionDiffApplyResult, error) {
	s.called = true
	s.req = req
	if s.err != nil {
		return sandboxapp.PromotionDiffApplyResult{}, s.err
	}
	return s.result, nil
}

func (s *stubPromotionDiffApplier) PreviewPromotionDiff(_ context.Context, req domainsandbox.PromotionRequest) (sandboxapp.PromotionDiffPreviewResult, error) {
	s.called = true
	s.req = domainsandbox.PromotionApplyRequest{Promotion: req}
	if s.err != nil {
		return sandboxapp.PromotionDiffPreviewResult{}, s.err
	}
	if s.previewResult.Status != "" {
		return s.previewResult, nil
	}
	return sandboxapp.PromotionDiffPreviewResult{
		DiffPath:     "/tmp/sandbox/diff.patch",
		FileCount:    1,
		AddedLines:   1,
		RemovedLines: 1,
		Status:       "previewed",
		Files: []sandboxapp.PromotionDiffFilePreview{{
			Path:         "docs/example.md",
			HunkCount:    1,
			AddedLines:   1,
			RemovedLines: 1,
		}},
	}, nil
}

type stubSandboxWorktreeCreator struct {
	createOpts   sandboxapp.WorktreeSandboxCreateOptions
	createResult sandboxapp.WorktreeSandboxCreateResult
	createErr    error
	closeOpts    sandboxapp.WorktreeSandboxCloseOptions
	closeResult  sandboxapp.WorktreeSandboxCloseResult
	closeErr     error
}

func (s *stubSandboxWorktreeCreator) Create(_ context.Context, opts sandboxapp.WorktreeSandboxCreateOptions) (sandboxapp.WorktreeSandboxCreateResult, error) {
	s.createOpts = opts
	return s.createResult, s.createErr
}

func (s *stubSandboxWorktreeCreator) Close(_ context.Context, opts sandboxapp.WorktreeSandboxCloseOptions) (sandboxapp.WorktreeSandboxCloseResult, error) {
	s.closeOpts = opts
	return s.closeResult, s.closeErr
}

func (s *stubSandboxPromotionStore) SavePromotionRequest(_ context.Context, req domainsandbox.PromotionRequest) error {
	s.promotions = append(s.promotions, req)
	return nil
}

func (s *stubSandboxPromotionStore) SavePromotionGateLog(_ context.Context, log domainsandbox.PromotionGateLog) error {
	s.gateLogs = append(s.gateLogs, log)
	return nil
}

func (s *stubSandboxPromotionStore) SaveSandboxArtifact(_ context.Context, artifact domainsandbox.SandboxArtifact) error {
	s.artifacts = append(s.artifacts, artifact)
	return nil
}

func TestHandleSandboxStatus(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &stubSandboxLister{
		sandboxes: []domainsandbox.SandboxRecord{{
			SandboxID: "sbx_1",
			Type:      "code",
			Path:      "sandbox/ws/sbx_1",
			Status:    domainsandbox.SandboxStatusActive,
			CreatedAt: now,
		}},
		artifacts: []domainsandbox.SandboxArtifact{{
			ArtifactID: "art_1",
			SandboxID:  "sbx_1",
			Type:       "report",
			FilePath:   "sandbox/ws/sbx_1/reports/report.md",
			Status:     "draft",
			CreatedAt:  now,
		}},
		promotions: []domainsandbox.PromotionRequest{{
			PromotionID:         "prom_1",
			SandboxID:           "sbx_1",
			TargetPath:          "docs/example.md",
			DiffPath:            "sandbox/ws/sbx_1/diff.patch",
			Reason:              "docs update",
			TestResultPath:      "sandbox/ws/sbx_1/test.txt",
			RollbackPlanPath:    "sandbox/ws/sbx_1/rollback.md",
			HumanApprovalStatus: domainsandbox.ApprovalGranted,
			CreatedAt:           now,
		}},
	}
	req := httptest.NewRequest(http.MethodGet, "/viewer/sandbox?limit=5", nil)
	rec := httptest.NewRecorder()

	HandleSandboxStatus(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if store.limit != 5 {
		t.Fatalf("limit = %d", store.limit)
	}
	var body struct {
		Sandboxes  []domainsandbox.SandboxRecord         `json:"sandboxes"`
		Artifacts  []domainsandbox.SandboxArtifact       `json:"artifacts"`
		Promotions []domainsandbox.PromotionRequest      `json:"promotions"`
		Decisions  []domainsandbox.PromotionGateDecision `json:"decisions"`
		GateLogs   []domainsandbox.PromotionGateLog      `json:"gate_logs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Sandboxes) != 1 || body.Sandboxes[0].SandboxID != "sbx_1" {
		t.Fatalf("sandboxes = %#v", body.Sandboxes)
	}
	if len(body.Artifacts) != 1 || body.Artifacts[0].ArtifactID != "art_1" {
		t.Fatalf("artifacts = %#v", body.Artifacts)
	}
	if len(body.Decisions) != 1 || body.Decisions[0].Status != domainsandbox.GateStatusApproved {
		t.Fatalf("decisions = %#v", body.Decisions)
	}
	if len(body.GateLogs) != 0 {
		t.Fatalf("gate logs = %#v", body.GateLogs)
	}
}

func TestHandleSandboxStatusInvalidLimit(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/viewer/sandbox?limit=bad", nil)
	rec := httptest.NewRecorder()

	HandleSandboxStatus(&stubSandboxLister{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestHandleSandboxStatusRequiresStore(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/viewer/sandbox", nil)
	rec := httptest.NewRecorder()

	HandleSandboxStatus(nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "sandbox store unavailable") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestHandleSandboxStatusOptionalUnavailable(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/viewer/sandbox?viewer_optional=1", nil)
	rec := httptest.NewRecorder()

	HandleSandboxStatus(nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":503`) || !strings.Contains(rec.Body.String(), "sandbox store unavailable") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestHandleSandboxPromotionRequest(t *testing.T) {
	store := &stubSandboxPromotionStore{}
	body := []byte(`{
		"promotion_id":"prom_1",
		"sandbox_id":"sbx_1",
		"target_path":"docs/example.md",
		"diff_path":"sandbox/sbx_1/diff.patch",
		"reason":"docs update"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/sandbox/promotions", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleSandboxPromotionRequest(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.promotions) != 1 || store.promotions[0].HumanApprovalStatus != domainsandbox.ApprovalPending {
		t.Fatalf("promotions = %#v", store.promotions)
	}
	if len(store.gateLogs) != 1 || store.gateLogs[0].GateStatus != domainsandbox.GateStatusNeedsMoreTest {
		t.Fatalf("gate logs = %#v", store.gateLogs)
	}
	var response struct {
		Decision domainsandbox.PromotionGateDecision `json:"decision"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.Decision.Status != domainsandbox.GateStatusNeedsMoreTest {
		t.Fatalf("decision = %#v", response.Decision)
	}
}

func TestHandleSandboxPromotionRequestRegistersRollbackArtifact(t *testing.T) {
	store := &stubSandboxPromotionStore{}
	body := []byte(`{
		"promotion_id":"prom_1",
		"sandbox_id":"sbx_1",
		"target_path":"docs/example.md",
		"diff_path":"sandbox/sbx_1/diff.patch",
		"reason":"docs update",
		"test_result_path":"sandbox/sbx_1/reports/test.txt",
		"rollback_plan_path":"sandbox/sbx_1/reports/rollback.md",
		"human_approval_status":"granted"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/sandbox/promotions", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleSandboxPromotionRequest(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.artifacts) != 1 {
		t.Fatalf("expected rollback artifact, got %#v", store.artifacts)
	}
	artifact := store.artifacts[0]
	if artifact.Type != "rollback_plan" || artifact.FilePath != "sandbox/sbx_1/reports/rollback.md" || artifact.Status != "pending_review" {
		t.Fatalf("unexpected rollback artifact: %#v", artifact)
	}
	var response struct {
		RollbackArtifact *domainsandbox.SandboxArtifact `json:"rollback_artifact"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.RollbackArtifact == nil || response.RollbackArtifact.Type != "rollback_plan" {
		t.Fatalf("missing rollback artifact in response: %#v", response.RollbackArtifact)
	}
}

func TestHandleSandboxPromotionRequestRegistersPostApplyVerificationArtifact(t *testing.T) {
	store := &stubSandboxPromotionStore{}
	body := []byte(`{
		"promotion_id":"prom_1",
		"sandbox_id":"sbx_1",
		"target_path":"docs/example.md",
		"diff_path":"sandbox/sbx_1/diff.patch",
		"reason":"docs update",
		"test_result_path":"sandbox/sbx_1/reports/test.txt",
		"rollback_plan_path":"sandbox/sbx_1/reports/rollback.md",
		"post_apply_verification_path":"sandbox/sbx_1/reports/post_apply.md",
		"human_approval_status":"granted"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/sandbox/promotions", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleSandboxPromotionRequest(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.artifacts) != 2 {
		t.Fatalf("expected rollback and post-apply artifacts, got %#v", store.artifacts)
	}
	artifact := store.artifacts[1]
	if artifact.Type != "post_apply_verification" || artifact.FilePath != "sandbox/sbx_1/reports/post_apply.md" || artifact.Status != "pending_review" {
		t.Fatalf("unexpected post-apply artifact: %#v", artifact)
	}
	if len(store.gateLogs) != 1 || store.gateLogs[0].PostApplyVerification != "sandbox/sbx_1/reports/post_apply.md" {
		t.Fatalf("gate logs = %#v", store.gateLogs)
	}
	var response struct {
		PostApplyArtifact *domainsandbox.SandboxArtifact `json:"post_apply_verification_artifact"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.PostApplyArtifact == nil || response.PostApplyArtifact.Type != "post_apply_verification" {
		t.Fatalf("missing post-apply artifact in response: %#v", response.PostApplyArtifact)
	}
}

func TestHandleSandboxPromotionApplyRecordsPostApplyVerification(t *testing.T) {
	store := &stubSandboxPromotionStore{}
	body := []byte(`{
		"promotion":{
			"promotion_id":"prom_1",
			"sandbox_id":"sbx_1",
			"target_path":"docs/example.md",
			"diff_path":"sandbox/sbx_1/diff.patch",
			"reason":"docs update",
			"test_result_path":"sandbox/sbx_1/reports/test.txt",
			"rollback_plan_path":"sandbox/sbx_1/reports/rollback.md",
			"human_approval_status":"granted"
		},
		"applied_by":"Worker",
		"apply_target":"feature/sandbox",
		"post_apply_verification_path":"sandbox/sbx_1/reports/post_apply.md",
		"human_approved":true
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/sandbox/promotions/apply", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleSandboxPromotionApply(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.gateLogs) != 1 || store.gateLogs[0].GateStatus != domainsandbox.GateStatusApplied {
		t.Fatalf("gate logs = %#v", store.gateLogs)
	}
	if store.gateLogs[0].PostApplyVerification != "sandbox/sbx_1/reports/post_apply.md" {
		t.Fatalf("post apply verification = %q", store.gateLogs[0].PostApplyVerification)
	}
	if len(store.artifacts) != 1 {
		t.Fatalf("artifacts = %#v", store.artifacts)
	}
	artifact := store.artifacts[0]
	if artifact.Type != "post_apply_verification" || artifact.Status != "completed" {
		t.Fatalf("artifact = %#v", artifact)
	}
	var response struct {
		Decision domainsandbox.PromotionApplyDecision `json:"decision"`
		Artifact *domainsandbox.SandboxArtifact       `json:"post_apply_verification_artifact"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.Decision.Status != domainsandbox.GateStatusApplied || response.Artifact == nil {
		t.Fatalf("response = %#v", response)
	}
}

func TestHandleSandboxPromotionApplyRunsPostApplyVerificationCommand(t *testing.T) {
	store := &stubSandboxPromotionStore{}
	verifier := &stubPostApplyVerifier{
		result: sandboxapp.PostApplyVerificationResult{
			Command:    "go test ./pkg/rencrowclient",
			OutputPath: "/tmp/sandbox/post_apply.md",
			Status:     "completed",
			Output:     "ok",
		},
	}
	body := []byte(`{
		"promotion":{
			"promotion_id":"prom_1",
			"sandbox_id":"sbx_1",
			"target_path":"docs/example.md",
			"diff_path":"sandbox/sbx_1/diff.patch",
			"reason":"docs update",
			"test_result_path":"sandbox/sbx_1/reports/test.txt",
			"rollback_plan_path":"sandbox/sbx_1/reports/rollback.md",
			"human_approval_status":"granted"
		},
		"post_apply_verification_path":"post_apply.md",
		"post_apply_verification_command":"go test ./pkg/rencrowclient",
		"human_approved":true
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/sandbox/promotions/apply", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleSandboxPromotionApplyWithVerifier(store, verifier).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !verifier.called || verifier.req.PostApplyVerificationCommand != "go test ./pkg/rencrowclient" {
		t.Fatalf("verifier = %#v", verifier)
	}
	if len(store.gateLogs) != 1 || store.gateLogs[0].GateStatus != domainsandbox.GateStatusApplied {
		t.Fatalf("gate logs = %#v", store.gateLogs)
	}
	var response struct {
		Result *sandboxapp.PostApplyVerificationResult `json:"post_apply_verification_result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.Result == nil || response.Result.Status != "completed" || response.Result.Output != "ok" {
		t.Fatalf("response result = %#v", response.Result)
	}
}

func TestHandleSandboxPromotionApplyRejectsVerificationCommandWithoutRunner(t *testing.T) {
	store := &stubSandboxPromotionStore{}
	body := []byte(`{
		"promotion":{
			"promotion_id":"prom_1",
			"sandbox_id":"sbx_1",
			"target_path":"docs/example.md",
			"diff_path":"sandbox/sbx_1/diff.patch",
			"reason":"docs update",
			"test_result_path":"sandbox/sbx_1/reports/test.txt",
			"rollback_plan_path":"sandbox/sbx_1/reports/rollback.md",
			"human_approval_status":"granted"
		},
		"post_apply_verification_path":"post_apply.md",
		"post_apply_verification_command":"go test ./pkg/rencrowclient",
		"human_approved":true
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/sandbox/promotions/apply", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleSandboxPromotionApply(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.gateLogs) != 0 || len(store.artifacts) != 0 {
		t.Fatalf("unexpected writes logs=%#v artifacts=%#v", store.gateLogs, store.artifacts)
	}
}

func TestHandleSandboxPromotionApplyAppliesDiffBeforeVerification(t *testing.T) {
	store := &stubSandboxPromotionStore{}
	applier := &stubPromotionDiffApplier{
		result: sandboxapp.PromotionDiffApplyResult{
			DiffPath:     "/tmp/sandbox/diff.patch",
			ApplyRoot:    "/tmp/worktree",
			AppliedFiles: []string{"docs/example.md"},
			Status:       "applied",
		},
	}
	verifier := &stubPostApplyVerifier{
		result: sandboxapp.PostApplyVerificationResult{Status: "completed", Output: "ok"},
	}
	body := []byte(`{
		"promotion":{
			"promotion_id":"prom_1",
			"sandbox_id":"sbx_1",
			"target_path":"docs/example.md",
			"diff_path":"sandbox/sbx_1/diff.patch",
			"reason":"docs update",
			"test_result_path":"sandbox/sbx_1/reports/test.txt",
			"rollback_plan_path":"sandbox/sbx_1/reports/rollback.md",
			"human_approval_status":"granted"
		},
		"post_apply_verification_path":"post_apply.md",
		"post_apply_verification_command":"go test ./pkg/rencrowclient",
		"human_approved":true
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/sandbox/promotions/apply", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleSandboxPromotionApplyWithVerifierAndApplier(store, verifier, applier).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !applier.called || applier.req.Promotion.PromotionID != "prom_1" {
		t.Fatalf("applier = %#v", applier)
	}
	if !verifier.called {
		t.Fatal("expected verifier after diff apply")
	}
	if len(store.gateLogs) != 1 || store.gateLogs[0].GateStatus != domainsandbox.GateStatusApplied {
		t.Fatalf("gate logs = %#v", store.gateLogs)
	}
	var response struct {
		Result *sandboxapp.PromotionDiffApplyResult `json:"diff_apply_result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.Result == nil || response.Result.Status != "applied" || len(response.Result.AppliedFiles) != 1 {
		t.Fatalf("response result = %#v", response.Result)
	}
}

func TestHandleSandboxPromotionApplyDoesNotApplyDiffWithoutHumanApproval(t *testing.T) {
	store := &stubSandboxPromotionStore{}
	applier := &stubPromotionDiffApplier{}
	body := []byte(`{
		"promotion":{
			"promotion_id":"prom_1",
			"sandbox_id":"sbx_1",
			"target_path":"docs/example.md",
			"diff_path":"sandbox/sbx_1/diff.patch",
			"reason":"docs update",
			"test_result_path":"sandbox/sbx_1/reports/test.txt",
			"rollback_plan_path":"sandbox/sbx_1/reports/rollback.md",
			"human_approval_status":"granted"
		},
		"post_apply_verification_path":"post_apply.md",
		"human_approved":false
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/sandbox/promotions/apply", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleSandboxPromotionApplyWithVerifierAndApplier(store, nil, applier).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if applier.called {
		t.Fatal("diff applier must not run before approval gate passes")
	}
	if len(store.gateLogs) != 0 || len(store.artifacts) != 0 {
		t.Fatalf("unexpected writes logs=%#v artifacts=%#v", store.gateLogs, store.artifacts)
	}
}

func TestHandleSandboxPromotionApplyFailsWhenDiffApplyFails(t *testing.T) {
	store := &stubSandboxPromotionStore{}
	applier := &stubPromotionDiffApplier{err: errors.New("diff mismatch")}
	body := []byte(`{
		"promotion":{
			"promotion_id":"prom_1",
			"sandbox_id":"sbx_1",
			"target_path":"docs/example.md",
			"diff_path":"sandbox/sbx_1/diff.patch",
			"reason":"docs update",
			"test_result_path":"sandbox/sbx_1/reports/test.txt",
			"rollback_plan_path":"sandbox/sbx_1/reports/rollback.md",
			"human_approval_status":"granted"
		},
		"post_apply_verification_path":"post_apply.md",
		"human_approved":true
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/sandbox/promotions/apply", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleSandboxPromotionApplyWithVerifierAndApplier(store, nil, applier).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !applier.called {
		t.Fatal("expected diff applier call")
	}
	if len(store.gateLogs) != 0 || len(store.artifacts) != 0 {
		t.Fatalf("unexpected writes logs=%#v artifacts=%#v", store.gateLogs, store.artifacts)
	}
}

func TestHandleSandboxPromotionApplyFailsWhenVerificationCommandFails(t *testing.T) {
	store := &stubSandboxPromotionStore{}
	verifier := &stubPostApplyVerifier{err: errors.New("verification failed")}
	body := []byte(`{
		"promotion":{
			"promotion_id":"prom_1",
			"sandbox_id":"sbx_1",
			"target_path":"docs/example.md",
			"diff_path":"sandbox/sbx_1/diff.patch",
			"reason":"docs update",
			"test_result_path":"sandbox/sbx_1/reports/test.txt",
			"rollback_plan_path":"sandbox/sbx_1/reports/rollback.md",
			"human_approval_status":"granted"
		},
		"post_apply_verification_path":"post_apply.md",
		"post_apply_verification_command":"go test ./pkg/rencrowclient",
		"human_approved":true
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/sandbox/promotions/apply", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleSandboxPromotionApplyWithVerifier(store, verifier).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.gateLogs) != 0 || len(store.artifacts) != 0 {
		t.Fatalf("unexpected writes logs=%#v artifacts=%#v", store.gateLogs, store.artifacts)
	}
}

func TestHandleSandboxPromotionRollbackRunsReverseDiffAndRecordsLog(t *testing.T) {
	store := &stubSandboxPromotionStore{}
	rollbacker := &stubPromotionDiffApplier{
		result: sandboxapp.PromotionDiffApplyResult{
			DiffPath:     "/tmp/sandbox/diff.patch",
			ApplyRoot:    "/tmp/worktree",
			AppliedFiles: []string{"docs/example.md"},
			Status:       "rolled_back",
		},
	}
	body := []byte(`{
		"promotion":{
			"promotion_id":"prom_1",
			"sandbox_id":"sbx_1",
			"target_path":"docs/example.md",
			"diff_path":"sandbox/sbx_1/diff.patch",
			"reason":"docs update",
			"test_result_path":"sandbox/sbx_1/reports/test.txt",
			"rollback_plan_path":"sandbox/sbx_1/reports/rollback.md",
			"human_approval_status":"granted"
		},
		"apply_target":"feature/sandbox",
		"post_apply_verification_path":"post_rollback.md",
		"human_approved":true
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/sandbox/promotions/rollback", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleSandboxPromotionRollback(store, rollbacker).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !rollbacker.called || rollbacker.req.Promotion.PromotionID != "prom_1" {
		t.Fatalf("rollbacker = %#v", rollbacker)
	}
	if len(store.gateLogs) != 1 || store.gateLogs[0].GateStatus != domainsandbox.GateStatusRolledBack {
		t.Fatalf("gate logs = %#v", store.gateLogs)
	}
	if len(store.artifacts) != 1 || store.artifacts[0].Type != "rollback_execution" || store.artifacts[0].Status != "completed" {
		t.Fatalf("artifacts = %#v", store.artifacts)
	}
	var response struct {
		Decision domainsandbox.PromotionApplyDecision `json:"decision"`
		Result   sandboxapp.PromotionDiffApplyResult  `json:"rollback_result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.Decision.Status != domainsandbox.GateStatusRolledBack || response.Result.Status != "rolled_back" {
		t.Fatalf("response = %#v", response)
	}
}

func TestHandleSandboxPromotionRollbackDoesNotRunWithoutHumanApproval(t *testing.T) {
	store := &stubSandboxPromotionStore{}
	rollbacker := &stubPromotionDiffApplier{}
	body := []byte(`{
		"promotion":{
			"promotion_id":"prom_1",
			"sandbox_id":"sbx_1",
			"target_path":"docs/example.md",
			"diff_path":"sandbox/sbx_1/diff.patch",
			"reason":"docs update",
			"test_result_path":"sandbox/sbx_1/reports/test.txt",
			"rollback_plan_path":"sandbox/sbx_1/reports/rollback.md",
			"human_approval_status":"granted"
		},
		"post_apply_verification_path":"post_rollback.md",
		"human_approved":false
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/sandbox/promotions/rollback", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleSandboxPromotionRollback(store, rollbacker).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if rollbacker.called {
		t.Fatal("rollbacker must not run before approval gate passes")
	}
	if len(store.gateLogs) != 0 || len(store.artifacts) != 0 {
		t.Fatalf("unexpected writes logs=%#v artifacts=%#v", store.gateLogs, store.artifacts)
	}
}

func TestHandleSandboxPromotionDiffPreview(t *testing.T) {
	previewer := &stubPromotionDiffApplier{}
	body := []byte(`{
		"promotion_id":"prom_1",
		"sandbox_id":"sbx_1",
		"target_path":"docs/example.md",
		"diff_path":"sandbox/sbx_1/diff.patch",
		"reason":"docs update",
		"test_result_path":"sandbox/sbx_1/reports/test.txt",
		"rollback_plan_path":"sandbox/sbx_1/reports/rollback.md",
		"human_approval_status":"granted"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/sandbox/promotions/preview", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleSandboxPromotionDiffPreview(previewer).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !previewer.called || previewer.req.Promotion.PromotionID != "prom_1" {
		t.Fatalf("previewer = %#v", previewer)
	}
	if !strings.Contains(rec.Body.String(), `"file_count":1`) || !strings.Contains(rec.Body.String(), `"path":"docs/example.md"`) {
		t.Fatalf("response = %s", rec.Body.String())
	}
}

func TestHandleSandboxPromotionApplyRejectsWithoutHumanApproval(t *testing.T) {
	store := &stubSandboxPromotionStore{}
	body := []byte(`{
		"promotion":{
			"promotion_id":"prom_1",
			"sandbox_id":"sbx_1",
			"target_path":"docs/example.md",
			"diff_path":"sandbox/sbx_1/diff.patch",
			"reason":"docs update",
			"test_result_path":"sandbox/sbx_1/reports/test.txt",
			"rollback_plan_path":"sandbox/sbx_1/reports/rollback.md",
			"human_approval_status":"granted"
		},
		"post_apply_verification_path":"sandbox/sbx_1/reports/post_apply.md",
		"human_approved":false
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/sandbox/promotions/apply", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleSandboxPromotionApply(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.gateLogs) != 0 || len(store.artifacts) != 0 {
		t.Fatalf("unexpected writes logs=%#v artifacts=%#v", store.gateLogs, store.artifacts)
	}
}

func TestHandleSandboxWorktreeCreateRequiresHumanApproval(t *testing.T) {
	creator := &stubSandboxWorktreeCreator{createErr: errors.New("human_approved=true is required to create a worktree sandbox")}
	body := []byte(`{"branch":"feature/sandbox","human_approved":false}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/sandbox/worktrees/create", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleSandboxWorktreeCreate(creator, "../worktrees").ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleSandboxWorktreeCreate(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	creator := &stubSandboxWorktreeCreator{
		createResult: sandboxapp.WorktreeSandboxCreateResult{
			Worktree: aiworkflowapp.WorktreeCreateResult{
				Worktree: domainai.WorktreeRegistry{
					WorktreeID: "worktree:repo:feature-sandbox",
					Path:       "/tmp/worktrees/repo-feature-sandbox",
					Branch:     "feature/sandbox",
					Status:     "active",
					CreatedAt:  now,
				},
			},
			Sandbox: domainsandbox.SandboxRecord{
				SandboxID:    "sandbox:worktree:repo:feature-sandbox",
				WorkstreamID: "ws_1",
				GoalID:       "goal_1",
				Type:         "code_worktree",
				Path:         "/tmp/worktrees/repo-feature-sandbox",
				Status:       domainsandbox.SandboxStatusActive,
				CreatedAt:    now,
			},
		},
	}
	body := []byte(`{
		"repo_root":"/repo",
		"repo_name":"repo",
		"branch":"feature/sandbox",
		"path_name":"repo-feature-sandbox",
		"purpose":"sandbox code change",
		"owner_agent":"Worker",
		"workstream_id":"ws_1",
		"goal_id":"goal_1",
		"human_approved":true
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/sandbox/worktrees/create", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleSandboxWorktreeCreate(creator, "../worktrees").ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if creator.createOpts.BaseDir != "../worktrees" || creator.createOpts.WorkstreamID != "ws_1" || !creator.createOpts.HumanApproved {
		t.Fatalf("opts = %#v", creator.createOpts)
	}
	var response sandboxapp.WorktreeSandboxCreateResult
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.Sandbox.Type != "code_worktree" || response.Sandbox.WorkstreamID != "ws_1" {
		t.Fatalf("response = %#v", response)
	}
}

func TestHandleSandboxWorktreeCloseRequiresHumanApproval(t *testing.T) {
	creator := &stubSandboxWorktreeCreator{closeErr: errors.New("human_approved=true is required to close a worktree sandbox")}
	body := []byte(`{"worktree_path":"/tmp/worktrees/repo-feature-sandbox","human_approved":false}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/sandbox/worktrees/close", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleSandboxWorktreeClose(creator, "../worktrees").ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleSandboxWorktreeClose(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	creator := &stubSandboxWorktreeCreator{
		closeResult: sandboxapp.WorktreeSandboxCloseResult{
			Worktree: aiworkflowapp.WorktreeCloseResult{
				Worktree: domainai.WorktreeRegistry{
					WorktreeID: "worktree:repo:feature-sandbox",
					Path:       "/tmp/worktrees/repo-feature-sandbox",
					Branch:     "feature/sandbox",
					Status:     "closed",
					CreatedAt:  now,
					ClosedAt:   now,
				},
			},
			Sandbox: domainsandbox.SandboxRecord{
				SandboxID:    "sandbox:worktree:repo:feature-sandbox",
				WorkstreamID: "ws_1",
				GoalID:       "goal_1",
				Type:         "code_worktree",
				Path:         "/tmp/worktrees/repo-feature-sandbox",
				Status:       domainsandbox.SandboxStatusClosed,
				CreatedAt:    now,
				ClosedAt:     now,
			},
		},
	}
	body := []byte(`{
		"repo_root":"/repo",
		"repo_name":"repo",
		"worktree_id":"worktree:repo:feature-sandbox",
		"worktree_path":"/tmp/worktrees/repo-feature-sandbox",
		"branch":"feature/sandbox",
		"owner_agent":"Worker",
		"sandbox_id":"sandbox:worktree:repo:feature-sandbox",
		"workstream_id":"ws_1",
		"goal_id":"goal_1",
		"human_approved":true
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/sandbox/worktrees/close", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleSandboxWorktreeClose(creator, "../worktrees").ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if creator.closeOpts.BaseDir != "../worktrees" || creator.closeOpts.SandboxID != "sandbox:worktree:repo:feature-sandbox" || !creator.closeOpts.HumanApproved {
		t.Fatalf("opts = %#v", creator.closeOpts)
	}
	var response sandboxapp.WorktreeSandboxCloseResult
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.Sandbox.Status != domainsandbox.SandboxStatusClosed || response.Sandbox.WorkstreamID != "ws_1" {
		t.Fatalf("response = %#v", response)
	}
}

func TestHandleSandboxPromotionManualReviewCreatesWorkstreamGoalAndArtifact(t *testing.T) {
	workstream := &stubWorkstreamLister{}
	sandboxStore := &stubSandboxPromotionStore{}
	previewer := &stubPromotionDiffApplier{
		previewResult: sandboxapp.PromotionDiffPreviewResult{
			DiffPath:             "/tmp/sandbox/diff.patch",
			FileCount:            2,
			RiskFlags:            []string{"dependency_change", "db_migration"},
			RequiresManualReview: true,
			Status:               "needs_manual_review",
			Files: []sandboxapp.PromotionDiffFilePreview{{
				Path:                 "go.mod",
				RiskFlags:            []string{"dependency_change"},
				RequiresManualReview: true,
			}},
		},
	}
	body := []byte(`{
		"promotion":{
			"promotion_id":"prom_risky",
			"sandbox_id":"sbx_1",
			"workstream_id":"ws_1",
			"target_path":"go.mod",
			"diff_path":"sandbox/sbx_1/diff.patch",
			"reason":"dependency and migration change",
			"test_result_path":"sandbox/sbx_1/reports/test.txt",
			"rollback_plan_path":"sandbox/sbx_1/reports/rollback.md",
			"human_approval_status":"pending"
		}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/sandbox/promotions/manual-review", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleSandboxPromotionManualReview(previewer, workstream, sandboxStore).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(workstream.savedGoals) != 1 {
		t.Fatalf("saved goals = %#v", workstream.savedGoals)
	}
	goal := workstream.savedGoals[0]
	if goal.WorkstreamID != "ws_1" || goal.Status != "waiting" || !strings.Contains(goal.Description, "dependency_change") {
		t.Fatalf("goal = %#v", goal)
	}
	if len(workstream.artifacts) != 1 {
		t.Fatalf("saved artifacts = %#v", workstream.artifacts)
	}
	artifact := workstream.artifacts[0]
	if artifact.Type != "sandbox_promotion_manual_review" || artifact.Status != "pending_review" || artifact.FilePath != "sandbox/sbx_1/diff.patch" {
		t.Fatalf("artifact = %#v", artifact)
	}
	if len(sandboxStore.gateLogs) != 1 || sandboxStore.gateLogs[0].GateStatus != domainsandbox.GateStatusNeedsReview {
		t.Fatalf("gate logs = %#v", sandboxStore.gateLogs)
	}
	var response struct {
		RiskFlags []string                  `json:"risk_flags"`
		Goal      domainworkstream.Goal     `json:"goal"`
		Artifact  domainworkstream.Artifact `json:"artifact"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(response.RiskFlags) != 2 || response.Goal.GoalID == "" || response.Artifact.ArtifactID == "" {
		t.Fatalf("response = %#v", response)
	}
}

func TestHandleSandboxPromotionManualReviewRejectsLowRiskPreview(t *testing.T) {
	workstream := &stubWorkstreamLister{}
	sandboxStore := &stubSandboxPromotionStore{}
	previewer := &stubPromotionDiffApplier{
		previewResult: sandboxapp.PromotionDiffPreviewResult{
			DiffPath: "/tmp/sandbox/diff.patch",
			Status:   "previewed",
			Files: []sandboxapp.PromotionDiffFilePreview{{
				Path: "docs/example.md",
			}},
		},
	}
	body := []byte(`{
		"promotion":{
			"promotion_id":"prom_safe",
			"sandbox_id":"sbx_1",
			"workstream_id":"ws_1",
			"target_path":"docs/example.md",
			"diff_path":"sandbox/sbx_1/diff.patch",
			"reason":"docs update"
		}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/sandbox/promotions/manual-review", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleSandboxPromotionManualReview(previewer, workstream, sandboxStore).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(workstream.savedGoals) != 0 || len(workstream.artifacts) != 0 || len(sandboxStore.gateLogs) != 0 {
		t.Fatalf("unexpected writes goals=%#v artifacts=%#v logs=%#v", workstream.savedGoals, workstream.artifacts, sandboxStore.gateLogs)
	}
}
