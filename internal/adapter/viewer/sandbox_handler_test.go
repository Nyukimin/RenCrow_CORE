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
			PromotionID:      "prom_1",
			SandboxID:        "sbx_1",
			TargetPath:       "docs/example.md",
			DiffPath:         "sandbox/ws/sbx_1/diff.patch",
			Reason:           "docs update",
			TestResultPath:   "sandbox/ws/sbx_1/test.txt",
			RollbackPlanPath: "sandbox/ws/sbx_1/rollback.md",
			CreatedAt:        now,
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
	if len(body.Decisions) != 1 || body.Decisions[0].Status != domainsandbox.GateStatusPassed {
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
	if len(store.promotions) != 1 {
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
		"rollback_plan_path":"sandbox/sbx_1/reports/rollback.md"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/sandbox/promotions", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleSandboxPromotionRequest(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.artifacts) != 4 {
		t.Fatalf("expected target, diff, test, and rollback artifacts, got %#v", store.artifacts)
	}
	artifact := store.artifacts[3]
	if artifact.Type != "rollback_plan" || artifact.FilePath != "sandbox/sbx_1/reports/rollback.md" || artifact.Status != "pending_review" {
		t.Fatalf("unexpected rollback artifact: %#v", artifact)
	}
	var response struct {
		RollbackArtifact      *domainsandbox.SandboxArtifact `json:"rollback_artifact"`
		PostApplyVerification *domainsandbox.SandboxArtifact `json:"post_apply_verification_artifact"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.RollbackArtifact == nil || response.RollbackArtifact.Type != "rollback_plan" {
		t.Fatalf("missing rollback artifact in response: %#v", response.RollbackArtifact)
	}
	if response.PostApplyVerification != nil {
		t.Fatalf("optional post-apply artifact should be omitted: %#v", response.PostApplyVerification)
	}
}

func TestHandleSandboxPromotionRequestRegistersCompleteArtifactSet(t *testing.T) {
	store := &stubSandboxPromotionStore{}
	body := []byte(`{
		"promotion_id":"prom_complete",
		"sandbox_id":"sbx_complete",
		"target_path":"workspace/sbx_complete/target.go",
		"diff_path":"workspace/sbx_complete/change.diff",
		"reason":"promote the reviewed change",
		"test_result_path":"workspace/sbx_complete/test.txt",
		"rollback_plan_path":"workspace/sbx_complete/rollback.md",
		"post_apply_verification_path":"workspace/sbx_complete/post-apply.txt"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/sandbox/promotions", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleSandboxPromotionRequest(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	want := []domainsandbox.SandboxArtifact{
		{ArtifactID: "art_target_prom_complete", SandboxID: "sbx_complete", Type: "target_file", FilePath: "workspace/sbx_complete/target.go", Title: "Target File", Status: "pending_review"},
		{ArtifactID: "art_diff_prom_complete", SandboxID: "sbx_complete", Type: "diff", FilePath: "workspace/sbx_complete/change.diff", Title: "Diff", Status: "pending_review"},
		{ArtifactID: "art_test_result_prom_complete", SandboxID: "sbx_complete", Type: "test_result", FilePath: "workspace/sbx_complete/test.txt", Title: "Test Result", Status: "pending_review"},
		{ArtifactID: "art_rollback_prom_complete", SandboxID: "sbx_complete", Type: "rollback_plan", FilePath: "workspace/sbx_complete/rollback.md", Title: "Rollback Plan", Status: "pending_review"},
		{ArtifactID: "art_post_apply_prom_complete", SandboxID: "sbx_complete", Type: "post_apply_verification", FilePath: "workspace/sbx_complete/post-apply.txt", Title: "Post-apply Verification", Status: "pending_review"},
	}
	if len(store.artifacts) != len(want) {
		t.Fatalf("artifacts = %#v, want %d artifacts", store.artifacts, len(want))
	}
	createdAt := store.artifacts[0].CreatedAt
	for i, expected := range want {
		got := store.artifacts[i]
		if got.ArtifactID != expected.ArtifactID || got.SandboxID != expected.SandboxID || got.Type != expected.Type || got.FilePath != expected.FilePath || got.Title != expected.Title || got.Status != expected.Status || got.CreatedAt.IsZero() || !got.CreatedAt.Equal(createdAt) {
			t.Fatalf("artifact[%d] = %#v, want %#v with non-zero CreatedAt", i, got, expected)
		}
	}
	var response struct {
		TargetArtifact        *domainsandbox.SandboxArtifact `json:"target_artifact"`
		DiffArtifact          *domainsandbox.SandboxArtifact `json:"diff_artifact"`
		TestResultArtifact    *domainsandbox.SandboxArtifact `json:"test_result_artifact"`
		RollbackArtifact      *domainsandbox.SandboxArtifact `json:"rollback_artifact"`
		PostApplyVerification *domainsandbox.SandboxArtifact `json:"post_apply_verification_artifact"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	responseArtifacts := []*domainsandbox.SandboxArtifact{response.TargetArtifact, response.DiffArtifact, response.TestResultArtifact, response.RollbackArtifact, response.PostApplyVerification}
	for i, artifact := range responseArtifacts {
		if artifact == nil {
			t.Fatalf("response missing artifact[%d]: %#v", i, response)
		}
		if artifact.ArtifactID != want[i].ArtifactID || artifact.Type != want[i].Type || artifact.FilePath != want[i].FilePath || artifact.Status != want[i].Status {
			t.Fatalf("response artifact[%d] = %#v, want %#v", i, artifact, want[i])
		}
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
		"post_apply_verification_path":"sandbox/sbx_1/reports/post_apply.md"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/sandbox/promotions", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleSandboxPromotionRequest(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.artifacts) != 5 {
		t.Fatalf("expected complete artifact set, got %#v", store.artifacts)
	}
	artifact := store.artifacts[4]
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
			"rollback_plan_path":"sandbox/sbx_1/reports/rollback.md"
		},
		"applied_by":"Worker",
		"apply_target":"feature/sandbox",
		"post_apply_verification_path":"sandbox/sbx_1/reports/post_apply.md"
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
			"rollback_plan_path":"sandbox/sbx_1/reports/rollback.md"
		},
		"post_apply_verification_path":"post_apply.md",
		"post_apply_verification_command":"go test ./pkg/rencrowclient"
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
			"rollback_plan_path":"sandbox/sbx_1/reports/rollback.md"
		},
		"post_apply_verification_path":"post_apply.md",
		"post_apply_verification_command":"go test ./pkg/rencrowclient"
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
			"rollback_plan_path":"sandbox/sbx_1/reports/rollback.md"
		},
		"post_apply_verification_path":"post_apply.md",
		"post_apply_verification_command":"go test ./pkg/rencrowclient"
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

func TestHandleSandboxPromotionApplyRunsAfterPolicyChecksPass(t *testing.T) {
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
			"rollback_plan_path":"sandbox/sbx_1/reports/rollback.md"
		},
		"post_apply_verification_path":"post_apply.md"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/sandbox/promotions/apply", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleSandboxPromotionApplyWithVerifierAndApplier(store, nil, applier).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !applier.called {
		t.Fatal("diff applier must run after policy checks pass")
	}
	if len(store.gateLogs) != 1 || len(store.artifacts) != 1 {
		t.Fatalf("missing audit writes logs=%#v artifacts=%#v", store.gateLogs, store.artifacts)
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
			"rollback_plan_path":"sandbox/sbx_1/reports/rollback.md"
		},
		"post_apply_verification_path":"post_apply.md"
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
			"rollback_plan_path":"sandbox/sbx_1/reports/rollback.md"
		},
		"post_apply_verification_path":"post_apply.md",
		"post_apply_verification_command":"go test ./pkg/rencrowclient"
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
			"rollback_plan_path":"sandbox/sbx_1/reports/rollback.md"
		},
		"apply_target":"feature/sandbox",
		"post_apply_verification_path":"post_rollback.md"
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

func TestHandleSandboxPromotionRollbackRunsAfterPolicyChecksPass(t *testing.T) {
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
			"rollback_plan_path":"sandbox/sbx_1/reports/rollback.md"
		},
		"post_apply_verification_path":"post_rollback.md"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/sandbox/promotions/rollback", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleSandboxPromotionRollback(store, rollbacker).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !rollbacker.called {
		t.Fatal("rollbacker must run after policy checks pass")
	}
	if len(store.gateLogs) != 1 || len(store.artifacts) != 1 {
		t.Fatalf("missing audit writes logs=%#v artifacts=%#v", store.gateLogs, store.artifacts)
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
		"rollback_plan_path":"sandbox/sbx_1/reports/rollback.md"
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

func TestHandleSandboxPromotionApplyRecordsAudit(t *testing.T) {
	store := &stubSandboxPromotionStore{}
	body := []byte(`{
		"promotion":{
			"promotion_id":"prom_1",
			"sandbox_id":"sbx_1",
			"target_path":"docs/example.md",
			"diff_path":"sandbox/sbx_1/diff.patch",
			"reason":"docs update",
			"test_result_path":"sandbox/sbx_1/reports/test.txt",
			"rollback_plan_path":"sandbox/sbx_1/reports/rollback.md"
		},
		"post_apply_verification_path":"sandbox/sbx_1/reports/post_apply.md"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/sandbox/promotions/apply", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleSandboxPromotionApply(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.gateLogs) != 1 || len(store.artifacts) != 1 {
		t.Fatalf("missing audit writes logs=%#v artifacts=%#v", store.gateLogs, store.artifacts)
	}
}

func TestHandleSandboxWorktreeCreateSurfacesPolicyError(t *testing.T) {
	creator := &stubSandboxWorktreeCreator{createErr: errors.New("protected branch is not allowed")}
	body := []byte(`{"branch":"feature/sandbox"}`)
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
		"goal_id":"goal_1"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/sandbox/worktrees/create", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleSandboxWorktreeCreate(creator, "../worktrees").ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if creator.createOpts.BaseDir != "../worktrees" || creator.createOpts.WorkstreamID != "ws_1" {
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

func TestHandleSandboxWorktreeCreateForwardsDetachedRef(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	creator := &stubSandboxWorktreeCreator{
		createResult: sandboxapp.WorktreeSandboxCreateResult{
			Worktree: aiworkflowapp.WorktreeCreateResult{
				Worktree: domainai.WorktreeRegistry{
					WorktreeID: "worktree:repo:detached-custom",
					Path:       "/tmp/worktrees/repo-detached-custom",
					Branch:     "0123456789abcdef0123456789abcdef01234567",
					Status:     "active",
					CreatedAt:  now,
				},
			},
		},
	}
	body := []byte(`{
		"repo_root":"/repo",
		"repo_name":"repo",
		"detached_ref":"HEAD",
		"path_name":"repo-detached-custom",
		"owner_agent":"Worker"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/sandbox/worktrees/create", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleSandboxWorktreeCreate(creator, "../worktrees").ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if creator.createOpts.DetachedRef != "HEAD" || creator.createOpts.Branch != "" {
		t.Fatalf("opts = %#v", creator.createOpts)
	}
	var response sandboxapp.WorktreeSandboxCreateResult
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.Worktree.Worktree.Branch != creator.createResult.Worktree.Worktree.Branch {
		t.Fatalf("response resolved branch = %q, want %q", response.Worktree.Worktree.Branch, creator.createResult.Worktree.Worktree.Branch)
	}
}

func TestHandleSandboxWorktreeCloseSurfacesPolicyError(t *testing.T) {
	creator := &stubSandboxWorktreeCreator{closeErr: errors.New("protected worktree close is blocked by policy")}
	body := []byte(`{"worktree_path":"/tmp/worktrees/repo-feature-sandbox"}`)
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
		"goal_id":"goal_1"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/sandbox/worktrees/close", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleSandboxWorktreeClose(creator, "../worktrees").ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if creator.closeOpts.BaseDir != "../worktrees" || creator.closeOpts.SandboxID != "sandbox:worktree:repo:feature-sandbox" {
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
