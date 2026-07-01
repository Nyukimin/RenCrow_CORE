package viewer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sandboxapp "github.com/Nyukimin/picoclaw_multiLLM/internal/application/sandbox"
	domainsandbox "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/sandbox"
	domainworkstream "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/workstream"
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

type SandboxPromotionManualReviewWorkstreamSink interface {
	SaveGoal(ctx context.Context, item domainworkstream.Goal) error
	SaveArtifact(ctx context.Context, item domainworkstream.Artifact) error
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
		if req.HumanApprovalStatus == "" {
			req.HumanApprovalStatus = domainsandbox.ApprovalPending
		}
		decision := domainsandbox.EvaluatePromotionRequest(req)
		if err := store.SavePromotionRequest(r.Context(), req); err != nil {
			http.Error(w, "failed to save promotion request", http.StatusInternalServerError)
			return
		}
		var rollbackArtifact *domainsandbox.SandboxArtifact
		var verificationArtifact *domainsandbox.SandboxArtifact
		if req.RollbackPlanPath != "" {
			artifact := domainsandbox.SandboxArtifact{
				ArtifactID: fmt.Sprintf("art_rollback_%s", req.PromotionID),
				SandboxID:  req.SandboxID,
				Type:       "rollback_plan",
				FilePath:   req.RollbackPlanPath,
				Title:      "Rollback Plan",
				Status:     "pending_review",
				CreatedAt:  now,
			}
			if err := store.SaveSandboxArtifact(r.Context(), artifact); err != nil {
				http.Error(w, "failed to save rollback artifact", http.StatusInternalServerError)
				return
			}
			rollbackArtifact = &artifact
		}
		if req.PostApplyVerificationPath != "" {
			artifact := domainsandbox.SandboxArtifact{
				ArtifactID: fmt.Sprintf("art_post_apply_%s", req.PromotionID),
				SandboxID:  req.SandboxID,
				Type:       "post_apply_verification",
				FilePath:   req.PostApplyVerificationPath,
				Title:      "Post-apply Verification",
				Status:     "pending_review",
				CreatedAt:  now,
			}
			if err := store.SaveSandboxArtifact(r.Context(), artifact); err != nil {
				http.Error(w, "failed to save post-apply verification artifact", http.StatusInternalServerError)
				return
			}
			verificationArtifact = &artifact
		}
		log := domainsandbox.PromotionGateLog{
			EventID:               fmt.Sprintf("evt_promotion_gate_%d", now.UnixNano()),
			PromotionID:           req.PromotionID,
			GateStatus:            decision.Status,
			Reason:                decision.Reason,
			HumanApprovalStatus:   req.HumanApprovalStatus,
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
			HumanApprovalStatus:   domainsandbox.ApprovalGranted,
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
			HumanApprovalStatus:   domainsandbox.ApprovalGranted,
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

func HandleSandboxPromotionManualReview(previewer SandboxPromotionDiffPreviewer, workstreamSink SandboxPromotionManualReviewWorkstreamSink, sandboxSink SandboxPromotionStore) http.HandlerFunc {
	type request struct {
		Promotion    domainsandbox.PromotionRequest `json:"promotion"`
		WorkstreamID string                         `json:"workstream_id,omitempty"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if previewer == nil {
			http.Error(w, "sandbox promotion diff preview unavailable", http.StatusServiceUnavailable)
			return
		}
		if workstreamSink == nil {
			http.Error(w, "workstream store unavailable", http.StatusServiceUnavailable)
			return
		}
		defer r.Body.Close()
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid promotion manual review request", http.StatusBadRequest)
			return
		}
		workstreamID := strings.TrimSpace(req.WorkstreamID)
		if workstreamID == "" {
			workstreamID = strings.TrimSpace(req.Promotion.WorkstreamID)
		}
		if workstreamID == "" {
			http.Error(w, "workstream_id is required", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Promotion.PromotionID) == "" {
			http.Error(w, "promotion_id is required", http.StatusBadRequest)
			return
		}
		preview, err := previewer.PreviewPromotionDiff(r.Context(), req.Promotion)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		riskFlags := preview.RiskFlags
		if len(riskFlags) == 0 {
			for _, file := range preview.Files {
				riskFlags = mergeStringSet(riskFlags, file.RiskFlags...)
			}
		}
		if !preview.RequiresManualReview && len(riskFlags) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"preview": preview,
				"decision": domainsandbox.PromotionGateDecision{
					Status: domainsandbox.GateStatusRejected,
					Reason: "manual review workflow is only for high-risk promotion diffs",
				},
			})
			return
		}
		now := time.Now().UTC()
		suffix := safeIDComponent(req.Promotion.PromotionID)
		goal := domainworkstream.Goal{
			GoalID:       "goal_sandbox_manual_review_" + suffix,
			WorkstreamID: workstreamID,
			Title:        "高リスク promotion review: " + req.Promotion.PromotionID,
			Description:  sandboxPromotionManualReviewDescription(req.Promotion, riskFlags),
			SuccessCriteria: []string{
				"risk_flags を確認し、自動 apply / rollback しない理由を記録する",
				"diff、test result、rollback plan、post-apply verification の妥当性を確認する",
				"必要なら PR review checklist または migration review checklist に分岐する",
				"Human approval なしに正式環境へ適用しない",
			},
			Verification: []string{
				"Promotion diff preview の risk_flags / requires_manual_review を確認する",
				"対象 diff と test_result_path と rollback_plan_path を確認する",
				"DB migration / dependency / binary / rename / delete / new file の該当有無を確認する",
				"別 Goal / PR / migration review checklist の必要性を記録する",
			},
			Status:    domainworkstream.StatusWaiting,
			CreatedAt: now,
		}
		artifact := domainworkstream.Artifact{
			ArtifactID:   "art_sandbox_manual_review_" + suffix,
			WorkstreamID: workstreamID,
			Type:         "sandbox_promotion_manual_review",
			FilePath:     req.Promotion.DiffPath,
			Title:        "Sandbox promotion manual review: " + req.Promotion.PromotionID,
			Status:       "pending_review",
			CreatedAt:    now,
		}
		if err := workstreamSink.SaveGoal(r.Context(), goal); err != nil {
			http.Error(w, "failed to save manual review goal", http.StatusInternalServerError)
			return
		}
		if err := workstreamSink.SaveArtifact(r.Context(), artifact); err != nil {
			http.Error(w, "failed to save manual review artifact", http.StatusInternalServerError)
			return
		}
		var gateLog *domainsandbox.PromotionGateLog
		if sandboxSink != nil {
			log := domainsandbox.PromotionGateLog{
				EventID:             fmt.Sprintf("evt_promotion_manual_review_%d", now.UnixNano()),
				PromotionID:         req.Promotion.PromotionID,
				GateStatus:          domainsandbox.GateStatusNeedsReview,
				Reason:              "high-risk promotion routed to manual review workflow: " + strings.Join(riskFlags, ","),
				HumanApprovalStatus: req.Promotion.HumanApprovalStatus,
				CreatedAt:           now,
			}
			if err := sandboxSink.SavePromotionGateLog(r.Context(), log); err != nil {
				http.Error(w, "failed to save manual review gate log", http.StatusInternalServerError)
				return
			}
			gateLog = &log
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"preview":    preview,
			"goal":       goal,
			"artifact":   artifact,
			"gate_log":   gateLog,
			"risk_flags": riskFlags,
		})
	}
}

func HandleSandboxWorktreeCreate(manager SandboxWorktreeCreator, baseDir string) http.HandlerFunc {
	type request struct {
		RepoRoot      string `json:"repo_root"`
		RepoName      string `json:"repo_name"`
		Branch        string `json:"branch"`
		PathName      string `json:"path_name"`
		Purpose       string `json:"purpose"`
		OwnerAgent    string `json:"owner_agent"`
		WorkstreamID  string `json:"workstream_id"`
		GoalID        string `json:"goal_id"`
		HumanApproved bool   `json:"human_approved"`
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
			RepoRoot:      req.RepoRoot,
			BaseDir:       baseDir,
			RepoName:      req.RepoName,
			Branch:        req.Branch,
			PathName:      req.PathName,
			Purpose:       req.Purpose,
			OwnerAgent:    req.OwnerAgent,
			WorkstreamID:  req.WorkstreamID,
			GoalID:        req.GoalID,
			HumanApproved: req.HumanApproved,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, result)
	}
}

func sandboxPromotionManualReviewDescription(req domainsandbox.PromotionRequest, riskFlags []string) string {
	flags := "-"
	if len(riskFlags) > 0 {
		flags = strings.Join(riskFlags, ", ")
	}
	return fmt.Sprintf("promotion_id=%s sandbox_id=%s target=%s diff=%s risk_flags=%s reason=%s", req.PromotionID, req.SandboxID, req.TargetPath, req.DiffPath, flags, req.Reason)
}

func safeIDComponent(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	}
	return out
}

func mergeStringSet(items []string, more ...string) []string {
	seen := make(map[string]struct{}, len(items)+len(more))
	out := make([]string, 0, len(items)+len(more))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	for _, item := range more {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func HandleSandboxWorktreeClose(manager SandboxWorktreeCreator, baseDir string) http.HandlerFunc {
	type request struct {
		RepoRoot      string `json:"repo_root"`
		RepoName      string `json:"repo_name"`
		WorktreeID    string `json:"worktree_id"`
		WorktreePath  string `json:"worktree_path"`
		Branch        string `json:"branch"`
		OwnerAgent    string `json:"owner_agent"`
		SandboxID     string `json:"sandbox_id"`
		WorkstreamID  string `json:"workstream_id"`
		GoalID        string `json:"goal_id"`
		HumanApproved bool   `json:"human_approved"`
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
			RepoRoot:      req.RepoRoot,
			BaseDir:       baseDir,
			RepoName:      req.RepoName,
			WorktreeID:    req.WorktreeID,
			WorktreePath:  req.WorktreePath,
			Branch:        req.Branch,
			OwnerAgent:    req.OwnerAgent,
			SandboxID:     req.SandboxID,
			WorkstreamID:  req.WorkstreamID,
			GoalID:        req.GoalID,
			HumanApproved: req.HumanApproved,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, result)
	}
}
