package viewer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sandboxapp "github.com/Nyukimin/RenCrow_CORE/internal/application/sandbox"
	domainsandbox "github.com/Nyukimin/RenCrow_CORE/internal/domain/sandbox"
)

type SandboxLister interface {
	ListSandboxes(ctx context.Context, limit int) ([]domainsandbox.SandboxRecord, error)
	ListSandboxArtifacts(ctx context.Context, limit int) ([]domainsandbox.SandboxArtifact, error)
	ListPromotionRequests(ctx context.Context, limit int) ([]domainsandbox.PromotionRequest, error)
	ListPromotionGateLogs(ctx context.Context, limit int) ([]domainsandbox.PromotionGateLog, error)
}

type SandboxPromotionStore interface {
	SavePromotionRequest(ctx context.Context, req domainsandbox.PromotionRequest) error
	SavePromotionGateLog(ctx context.Context, log domainsandbox.PromotionGateLog) error
	SaveSandboxArtifact(ctx context.Context, artifact domainsandbox.SandboxArtifact) error
}

type SandboxWorktreeCreator interface {
	Create(ctx context.Context, opts sandboxapp.WorktreeSandboxCreateOptions) (sandboxapp.WorktreeSandboxCreateResult, error)
	Close(ctx context.Context, opts sandboxapp.WorktreeSandboxCloseOptions) (sandboxapp.WorktreeSandboxCloseResult, error)
}

type SandboxPostApplyVerifier interface {
	RunPostApplyVerification(ctx context.Context, req domainsandbox.PromotionApplyRequest) (sandboxapp.PostApplyVerificationResult, error)
}

type SandboxPromotionDiffApplier interface {
	ApplyPromotionDiff(ctx context.Context, req domainsandbox.PromotionApplyRequest) (sandboxapp.PromotionDiffApplyResult, error)
}

type SandboxPromotionDiffRollbacker interface {
	RollbackPromotionDiff(ctx context.Context, req domainsandbox.PromotionApplyRequest) (sandboxapp.PromotionDiffApplyResult, error)
}

type SandboxPromotionDiffPreviewer interface {
	PreviewPromotionDiff(ctx context.Context, req domainsandbox.PromotionRequest) (sandboxapp.PromotionDiffPreviewResult, error)
}

func HandleSandboxStatus(store SandboxLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			if r.URL.Query().Get("viewer_optional") == "1" {
				writeJSON(w, http.StatusOK, map[string]any{
					"ok":     false,
					"status": http.StatusServiceUnavailable,
					"error":  "sandbox store unavailable",
				})
				return
			}
			http.Error(w, "sandbox store unavailable", http.StatusServiceUnavailable)
			return
		}
		limit := 20
		if raw := r.URL.Query().Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n <= 0 {
				http.Error(w, "invalid limit", http.StatusBadRequest)
				return
			}
			if n > 100 {
				n = 100
			}
			limit = n
		}
		sandboxes, err := store.ListSandboxes(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load sandboxes", http.StatusInternalServerError)
			return
		}
		artifacts, err := store.ListSandboxArtifacts(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load sandbox artifacts", http.StatusInternalServerError)
			return
		}
		promotions, err := store.ListPromotionRequests(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load promotion requests", http.StatusInternalServerError)
			return
		}
		logs, err := store.ListPromotionGateLogs(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load promotion gate logs", http.StatusInternalServerError)
			return
		}
		decisions := make([]domainsandbox.PromotionGateDecision, 0, len(promotions))
		for _, promotion := range promotions {
			decisions = append(decisions, domainsandbox.EvaluatePromotionRequest(promotion))
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"sandboxes":  sandboxes,
			"artifacts":  artifacts,
			"promotions": promotions,
			"decisions":  decisions,
			"gate_logs":  logs,
		})
	}
}

func HandleSandboxPromotionRequest(store SandboxPromotionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "sandbox store unavailable", http.StatusServiceUnavailable)
			return
		}
		defer r.Body.Close()
		var req domainsandbox.PromotionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid promotion request", http.StatusBadRequest)
			return
		}
		now := time.Now().UTC()
		if req.CreatedAt.IsZero() {
			req.CreatedAt = now
		}
		decision := domainsandbox.EvaluatePromotionRequest(req)
		if err := store.SavePromotionRequest(r.Context(), req); err != nil {
			http.Error(w, "failed to save promotion request", http.StatusInternalServerError)
			return
		}
		saveArtifact := func(artifactID, artifactType, filePath, title string) (*domainsandbox.SandboxArtifact, error) {
			if strings.TrimSpace(filePath) == "" {
				return nil, nil
			}
			artifact := domainsandbox.SandboxArtifact{
				ArtifactID: artifactID,
				SandboxID:  req.SandboxID,
				Type:       artifactType,
				FilePath:   filePath,
				Title:      title,
				Status:     "pending_review",
				CreatedAt:  now,
			}
			if err := store.SaveSandboxArtifact(r.Context(), artifact); err != nil {
				return nil, err
			}
			return &artifact, nil
		}
		targetArtifact, err := saveArtifact(fmt.Sprintf("art_target_%s", req.PromotionID), "target_file", req.TargetPath, "Target File")
		if err != nil {
			http.Error(w, "failed to save target artifact", http.StatusInternalServerError)
			return
		}
		diffArtifact, err := saveArtifact(fmt.Sprintf("art_diff_%s", req.PromotionID), "diff", req.DiffPath, "Diff")
		if err != nil {
			http.Error(w, "failed to save diff artifact", http.StatusInternalServerError)
			return
		}
		testResultArtifact, err := saveArtifact(fmt.Sprintf("art_test_result_%s", req.PromotionID), "test_result", req.TestResultPath, "Test Result")
		if err != nil {
			http.Error(w, "failed to save test result artifact", http.StatusInternalServerError)
			return
		}
		var rollbackArtifact *domainsandbox.SandboxArtifact
		var verificationArtifact *domainsandbox.SandboxArtifact
		rollbackArtifact, err = saveArtifact(fmt.Sprintf("art_rollback_%s", req.PromotionID), "rollback_plan", req.RollbackPlanPath, "Rollback Plan")
		if err != nil {
			http.Error(w, "failed to save rollback artifact", http.StatusInternalServerError)
			return
		}
		verificationArtifact, err = saveArtifact(fmt.Sprintf("art_post_apply_%s", req.PromotionID), "post_apply_verification", req.PostApplyVerificationPath, "Post-apply Verification")
		if err != nil {
			http.Error(w, "failed to save post-apply verification artifact", http.StatusInternalServerError)
			return
		}
		log := domainsandbox.PromotionGateLog{
			EventID:               fmt.Sprintf("evt_promotion_gate_%d", now.UnixNano()),
			PromotionID:           req.PromotionID,
			GateStatus:            decision.Status,
			Reason:                decision.Reason,
			PostApplyVerification: req.PostApplyVerificationPath,
			CreatedAt:             now,
		}
		if err := store.SavePromotionGateLog(r.Context(), log); err != nil {
			http.Error(w, "failed to save promotion gate log", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"promotion":                        req,
			"decision":                         decision,
			"gate_log":                         log,
			"target_artifact":                  targetArtifact,
			"diff_artifact":                    diffArtifact,
			"test_result_artifact":             testResultArtifact,
			"rollback_artifact":                rollbackArtifact,
			"post_apply_verification_artifact": verificationArtifact,
		})
	}
}

func HandleSandboxPromotionApply(store SandboxPromotionStore) http.HandlerFunc {
	return HandleSandboxPromotionApplyWithVerifier(store, nil)
}

func HandleSandboxPromotionApplyWithVerifier(store SandboxPromotionStore, verifier SandboxPostApplyVerifier) http.HandlerFunc {
	return HandleSandboxPromotionApplyWithVerifierAndApplier(store, verifier, nil)
}

func HandleSandboxPromotionApplyWithVerifierAndApplier(store SandboxPromotionStore, verifier SandboxPostApplyVerifier, applier SandboxPromotionDiffApplier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "sandbox store unavailable", http.StatusServiceUnavailable)
			return
		}
		defer r.Body.Close()
		var req domainsandbox.PromotionApplyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid promotion apply request", http.StatusBadRequest)
			return
		}
		decision := domainsandbox.EvaluatePromotionApplyRequest(req)
		if decision.Status != domainsandbox.GateStatusApplied {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"decision": decision,
			})
			return
		}
		var diffApplyResult *sandboxapp.PromotionDiffApplyResult
		if applier != nil {
			result, err := applier.ApplyPromotionDiff(r.Context(), req)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			diffApplyResult = &result
		}
		var verificationResult *sandboxapp.PostApplyVerificationResult
		if strings.TrimSpace(req.PostApplyVerificationCommand) != "" && verifier == nil {
			http.Error(w, "post-apply verification runner unavailable", http.StatusServiceUnavailable)
			return
		}
		if verifier != nil {
			result, err := verifier.RunPostApplyVerification(r.Context(), req)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if result.Status != "" {
				verificationResult = &result
			}
		}
		now := time.Now().UTC()
		artifact := domainsandbox.SandboxArtifact{
			ArtifactID: fmt.Sprintf("art_post_apply_verified_%s", req.Promotion.PromotionID),
			SandboxID:  req.Promotion.SandboxID,
			Type:       "post_apply_verification",
			FilePath:   req.PostApplyVerificationPath,
			Title:      "Post-apply Verification",
			Status:     "completed",
			CreatedAt:  now,
		}
		if err := store.SaveSandboxArtifact(r.Context(), artifact); err != nil {
			http.Error(w, "failed to save post-apply verification artifact", http.StatusInternalServerError)
			return
		}
		reason := decision.Reason
		if req.ApplyTarget != "" {
			reason = fmt.Sprintf("%s: %s", reason, req.ApplyTarget)
		}
		if diffApplyResult != nil {
			reason = fmt.Sprintf("%s; applied_files=%d", reason, len(diffApplyResult.AppliedFiles))
		}
		log := domainsandbox.PromotionGateLog{
			EventID:               fmt.Sprintf("evt_promotion_applied_%d", now.UnixNano()),
			PromotionID:           req.Promotion.PromotionID,
			GateStatus:            decision.Status,
			Reason:                reason,
			PostApplyVerification: req.PostApplyVerificationPath,
			CreatedAt:             now,
		}
		if err := store.SavePromotionGateLog(r.Context(), log); err != nil {
			http.Error(w, "failed to save promotion apply log", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"decision":                         decision,
			"diff_apply_result":                diffApplyResult,
			"gate_log":                         log,
			"post_apply_verification_artifact": artifact,
			"post_apply_verification_result":   verificationResult,
		})
	}
}

func HandleSandboxPromotionRollback(store SandboxPromotionStore, rollbacker SandboxPromotionDiffRollbacker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "sandbox store unavailable", http.StatusServiceUnavailable)
			return
		}
		if rollbacker == nil {
			http.Error(w, "sandbox promotion rollback unavailable", http.StatusServiceUnavailable)
			return
		}
		defer r.Body.Close()
		var req domainsandbox.PromotionApplyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid promotion rollback request", http.StatusBadRequest)
			return
		}
		decision := domainsandbox.EvaluatePromotionRollbackRequest(req)
		if decision.Status != domainsandbox.GateStatusRolledBack {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"decision": decision,
			})
			return
		}
		result, err := rollbacker.RollbackPromotionDiff(r.Context(), req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		now := time.Now().UTC()
		artifact := domainsandbox.SandboxArtifact{
			ArtifactID: fmt.Sprintf("art_rollback_executed_%s", req.Promotion.PromotionID),
			SandboxID:  req.Promotion.SandboxID,
			Type:       "rollback_execution",
			FilePath:   req.Promotion.RollbackPlanPath,
			Title:      "Rollback Execution",
			Status:     "completed",
			CreatedAt:  now,
		}
		if err := store.SaveSandboxArtifact(r.Context(), artifact); err != nil {
			http.Error(w, "failed to save rollback artifact", http.StatusInternalServerError)
			return
		}
		reason := fmt.Sprintf("%s; rolled_back_files=%d", decision.Reason, len(result.AppliedFiles))
		if req.ApplyTarget != "" {
			reason = fmt.Sprintf("%s: %s", reason, req.ApplyTarget)
		}
		log := domainsandbox.PromotionGateLog{
			EventID:               fmt.Sprintf("evt_rollback_executed_%d", now.UnixNano()),
			PromotionID:           req.Promotion.PromotionID,
			GateStatus:            decision.Status,
			Reason:                reason,
			PostApplyVerification: req.PostApplyVerificationPath,
			CreatedAt:             now,
		}
		if err := store.SavePromotionGateLog(r.Context(), log); err != nil {
			http.Error(w, "failed to save promotion rollback log", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"decision":          decision,
			"rollback_result":   result,
			"rollback_artifact": artifact,
			"gate_log":          log,
		})
	}
}

func HandleSandboxPromotionDiffPreview(previewer SandboxPromotionDiffPreviewer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if previewer == nil {
			http.Error(w, "sandbox promotion diff preview unavailable", http.StatusServiceUnavailable)
			return
		}
		defer r.Body.Close()
		var req domainsandbox.PromotionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid promotion diff preview request", http.StatusBadRequest)
			return
		}
		preview, err := previewer.PreviewPromotionDiff(r.Context(), req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"preview": preview})
	}
}

func HandleSandboxWorktreeCreate(manager SandboxWorktreeCreator, baseDir string) http.HandlerFunc {
	type request struct {
		RepoRoot     string `json:"repo_root"`
		RepoName     string `json:"repo_name"`
		Branch       string `json:"branch"`
		DetachedRef  string `json:"detached_ref"`
		PathName     string `json:"path_name"`
		Purpose      string `json:"purpose"`
		OwnerAgent   string `json:"owner_agent"`
		WorkstreamID string `json:"workstream_id"`
		GoalID       string `json:"goal_id"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if manager == nil {
			http.Error(w, "sandbox worktree manager unavailable", http.StatusServiceUnavailable)
			return
		}
		defer r.Body.Close()
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid sandbox worktree request", http.StatusBadRequest)
			return
		}
		result, err := manager.Create(r.Context(), sandboxapp.WorktreeSandboxCreateOptions{
			RepoRoot:     req.RepoRoot,
			BaseDir:      baseDir,
			RepoName:     req.RepoName,
			Branch:       req.Branch,
			DetachedRef:  req.DetachedRef,
			PathName:     req.PathName,
			Purpose:      req.Purpose,
			OwnerAgent:   req.OwnerAgent,
			WorkstreamID: req.WorkstreamID,
			GoalID:       req.GoalID,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, result)
	}
}

func HandleSandboxWorktreeClose(manager SandboxWorktreeCreator, baseDir string) http.HandlerFunc {
	type request struct {
		RepoRoot     string `json:"repo_root"`
		RepoName     string `json:"repo_name"`
		WorktreeID   string `json:"worktree_id"`
		WorktreePath string `json:"worktree_path"`
		Branch       string `json:"branch"`
		OwnerAgent   string `json:"owner_agent"`
		SandboxID    string `json:"sandbox_id"`
		WorkstreamID string `json:"workstream_id"`
		GoalID       string `json:"goal_id"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if manager == nil {
			http.Error(w, "sandbox worktree manager unavailable", http.StatusServiceUnavailable)
			return
		}
		defer r.Body.Close()
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid sandbox worktree close request", http.StatusBadRequest)
			return
		}
		result, err := manager.Close(r.Context(), sandboxapp.WorktreeSandboxCloseOptions{
			RepoRoot:     req.RepoRoot,
			BaseDir:      baseDir,
			RepoName:     req.RepoName,
			WorktreeID:   req.WorktreeID,
			WorktreePath: req.WorktreePath,
			Branch:       req.Branch,
			OwnerAgent:   req.OwnerAgent,
			SandboxID:    req.SandboxID,
			WorkstreamID: req.WorkstreamID,
			GoalID:       req.GoalID,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, result)
	}
}
