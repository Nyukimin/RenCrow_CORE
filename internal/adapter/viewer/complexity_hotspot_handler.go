package viewer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	complexityapp "github.com/Nyukimin/picoclaw_multiLLM/internal/application/complexity"
	domaincomplexity "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/complexity"
	domainsandbox "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/sandbox"
	domainskill "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/skillgovernance"
	domainworkstream "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/workstream"
)

type ComplexityHotspotLister interface {
	ListScanEvents(ctx context.Context, limit int) ([]domaincomplexity.ScanEvent, error)
	ListHotspots(ctx context.Context, limit int) ([]domaincomplexity.Hotspot, error)
	ListHotspotEvidence(ctx context.Context, limit int) ([]domaincomplexity.HotspotEvidence, error)
	ListReportArtifacts(ctx context.Context, limit int) ([]domaincomplexity.ReportArtifact, error)
}

type ComplexityHotspotStore interface {
	ComplexityHotspotLister
	SaveScanEvent(ctx context.Context, item domaincomplexity.ScanEvent) error
	SaveHotspot(ctx context.Context, item domaincomplexity.Hotspot) error
	SaveHotspotEvidence(ctx context.Context, item domaincomplexity.HotspotEvidence) error
	SaveReportArtifact(ctx context.Context, item domaincomplexity.ReportArtifact) error
}

type ComplexityHotspotAnalyzer interface {
	Scan(req complexityapp.ScanRequest) (domaincomplexity.ScanResult, error)
}

type ComplexityCoderDiffGenerator interface {
	GenerateConcreteDiff(ctx context.Context, req complexityapp.CoderDiffRequest) (complexityapp.CoderDiffResult, error)
}

var complexityCoderDiffGenerationTimeout = 10 * time.Second

type ComplexitySkillBootstrap interface {
	Record(ctx context.Context, task domainskill.TaskContext, usedSkillIDs []string) ([]domainskill.SkillTriggerLog, error)
}

type ComplexityWorkstreamArtifactSink interface {
	SaveArtifact(ctx context.Context, item domainworkstream.Artifact) error
}

type ComplexityProposalWorkstreamSink interface {
	SaveGoal(ctx context.Context, item domainworkstream.Goal) error
	SaveArtifact(ctx context.Context, item domainworkstream.Artifact) error
}

type ComplexityHotspotScanRequest struct {
	ScanID                string   `json:"scan_id"`
	WorkstreamID          string   `json:"workstream_id,omitempty"`
	Repo                  string   `json:"repo"`
	RootPath              string   `json:"root_path"`
	ScanScope             []string `json:"scan_scope,omitempty"`
	MaxHotspots           int      `json:"max_hotspots,omitempty"`
	ExcludeDirs           []string `json:"exclude_dirs,omitempty"`
	CandidatePatterns     []string `json:"candidate_patterns,omitempty"`
	AutoCandidatePatterns bool     `json:"auto_candidate_patterns,omitempty"`
	DCITraceLimit         int      `json:"dci_trace_limit,omitempty"`
}

type ComplexityHotspotProposalRequest struct {
	HotspotID                 string `json:"hotspot_id"`
	WorkstreamID              string `json:"workstream_id"`
	GoalID                    string `json:"goal_id,omitempty"`
	ArtifactID                string `json:"artifact_id,omitempty"`
	PromotionID               string `json:"promotion_id,omitempty"`
	SandboxID                 string `json:"sandbox_id,omitempty"`
	TargetPath                string `json:"target_path,omitempty"`
	DiffPath                  string `json:"diff_path,omitempty"`
	TestResultPath            string `json:"test_result_path,omitempty"`
	RollbackPlanPath          string `json:"rollback_plan_path,omitempty"`
	PostApplyVerificationPath string `json:"post_apply_verification_path,omitempty"`
	HumanApprovalStatus       string `json:"human_approval_status,omitempty"`
}

type ComplexityConcreteDiffRequest struct {
	HotspotID                 string `json:"hotspot_id"`
	WorkstreamID              string `json:"workstream_id,omitempty"`
	ArtifactID                string `json:"artifact_id,omitempty"`
	PromotionID               string `json:"promotion_id,omitempty"`
	SandboxID                 string `json:"sandbox_id,omitempty"`
	TargetPath                string `json:"target_path,omitempty"`
	DiffPath                  string `json:"diff_path,omitempty"`
	ConcreteDiff              string `json:"concrete_diff"`
	TestResultPath            string `json:"test_result_path,omitempty"`
	RollbackPlanPath          string `json:"rollback_plan_path,omitempty"`
	PostApplyVerificationPath string `json:"post_apply_verification_path,omitempty"`
	HumanApprovalStatus       string `json:"human_approval_status,omitempty"`
}

type ComplexityCoderDiffRequest struct {
	HotspotID                 string `json:"hotspot_id"`
	WorkstreamID              string `json:"workstream_id,omitempty"`
	JobID                     string `json:"job_id,omitempty"`
	ArtifactID                string `json:"artifact_id,omitempty"`
	PromotionID               string `json:"promotion_id,omitempty"`
	SandboxID                 string `json:"sandbox_id,omitempty"`
	TargetPath                string `json:"target_path,omitempty"`
	DiffPath                  string `json:"diff_path,omitempty"`
	TestResultPath            string `json:"test_result_path,omitempty"`
	RollbackPlanPath          string `json:"rollback_plan_path,omitempty"`
	PostApplyVerificationPath string `json:"post_apply_verification_path,omitempty"`
	HumanApprovalStatus       string `json:"human_approval_status,omitempty"`
}

func HandleComplexityHotspotStatus(store ComplexityHotspotLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "complexity hotspot store unavailable", http.StatusServiceUnavailable)
			return
		}
		limit, err := parseViewerLimit(r.URL.Query().Get("limit"), 20, 100)
		if err != nil {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		scans, err := store.ListScanEvents(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load complexity scans", http.StatusInternalServerError)
			return
		}
		hotspots, err := store.ListHotspots(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load complexity hotspots", http.StatusInternalServerError)
			return
		}
		evidence, err := store.ListHotspotEvidence(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load complexity evidence", http.StatusInternalServerError)
			return
		}
		reports, err := store.ListReportArtifacts(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load complexity reports", http.StatusInternalServerError)
			return
		}
		if scans == nil {
			scans = []domaincomplexity.ScanEvent{}
		}
		if hotspots == nil {
			hotspots = []domaincomplexity.Hotspot{}
		}
		if evidence == nil {
			evidence = []domaincomplexity.HotspotEvidence{}
		}
		if reports == nil {
			reports = []domaincomplexity.ReportArtifact{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"scans":    scans,
			"hotspots": hotspots,
			"evidence": evidence,
			"reports":  reports,
		})
	}
}

func HandleComplexityHotspotScan(store ComplexityHotspotStore, analyzer ComplexityHotspotAnalyzer, skillBootstrap ComplexitySkillBootstrap, workstreamArtifactSink ComplexityWorkstreamArtifactSink, dciTraces any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil || analyzer == nil {
			http.Error(w, "complexity hotspot scan unavailable", http.StatusServiceUnavailable)
			return
		}
		var req ComplexityHotspotScanRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid complexity hotspot payload", http.StatusBadRequest)
			return
		}
		candidatePatterns := req.CandidatePatterns
		if req.AutoCandidatePatterns {
			if dciTraces == nil {
				http.Error(w, "dci trace store unavailable for complexity candidate extraction", http.StatusServiceUnavailable)
				return
			}
			derived, err := complexityapp.DeriveCandidatePatterns(r.Context(), dciTraces, req.DCITraceLimit)
			if err != nil {
				http.Error(w, "failed to derive complexity candidate patterns from dci traces", http.StatusInternalServerError)
				return
			}
			candidatePatterns = complexityapp.MergeCandidatePatterns(candidatePatterns, derived)
		}
		if skillBootstrap != nil {
			if _, err := skillBootstrap.Record(r.Context(), domainskill.TaskContext{
				Text:         complexitySkillTaskText(req),
				Intent:       "complexity_hotspot_scan",
				Agent:        "Coder",
				WorkstreamID: req.WorkstreamID,
			}, []string{"core.codebase-complexity-hotspot"}); err != nil {
				http.Error(w, "complexity skill bootstrap failed", http.StatusInternalServerError)
				return
			}
		}
		result, err := analyzer.Scan(complexityapp.ScanRequest{
			ScanID:            req.ScanID,
			WorkstreamID:      req.WorkstreamID,
			Repo:              req.Repo,
			RootPath:          req.RootPath,
			ScanScope:         req.ScanScope,
			MaxHotspots:       req.MaxHotspots,
			ExcludeDirs:       req.ExcludeDirs,
			CandidatePatterns: candidatePatterns,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if result.Scan.Status == "completed" && result.Scan.CompletedAt.IsZero() {
			result.Scan.CompletedAt = time.Now().UTC()
		}
		if err := store.SaveScanEvent(r.Context(), result.Scan); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for _, hotspot := range result.Hotspots {
			if err := store.SaveHotspot(r.Context(), hotspot); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		for _, evidence := range result.Evidence {
			if err := store.SaveHotspotEvidence(r.Context(), evidence); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		report := domaincomplexity.ReportArtifact{
			ArtifactID:   "art_complexity_" + result.Scan.ScanID,
			ScanID:       result.Scan.ScanID,
			WorkstreamID: result.Scan.WorkstreamID,
			Type:         "complexity_hotspot_report",
			Title:        "Complexity Hotspot Report",
			Status:       "generated",
			Content:      complexityapp.BuildReportMarkdown(result),
			CreatedAt:    result.Scan.CompletedAt,
		}
		if report.CreatedAt.IsZero() {
			report.CreatedAt = result.Scan.CreatedAt
		}
		if err := store.SaveReportArtifact(r.Context(), report); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if workstreamArtifactSink != nil && report.WorkstreamID != "" {
			if err := workstreamArtifactSink.SaveArtifact(r.Context(), domainworkstream.Artifact{
				ArtifactID:   report.ArtifactID,
				WorkstreamID: report.WorkstreamID,
				Type:         report.Type,
				Title:        report.Title,
				Status:       "pending_review",
				CreatedAt:    report.CreatedAt,
			}); err != nil {
				http.Error(w, "complexity workstream artifact registration failed", http.StatusInternalServerError)
				return
			}
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"scan":               result.Scan,
			"hotspots":           result.Hotspots,
			"evidence":           result.Evidence,
			"report":             report,
			"candidate_patterns": candidatePatterns,
		})
	}
}

func HandleComplexityHotspotProposal(store ComplexityHotspotLister, workstreamSink ComplexityProposalWorkstreamSink) http.HandlerFunc {
	return HandleComplexityHotspotProposalWithSandbox(store, workstreamSink, nil)
}

func HandleComplexityHotspotProposalWithSandbox(store ComplexityHotspotLister, workstreamSink ComplexityProposalWorkstreamSink, sandboxSink SandboxPromotionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil || workstreamSink == nil {
			http.Error(w, "complexity proposal mode unavailable", http.StatusServiceUnavailable)
			return
		}
		var req ComplexityHotspotProposalRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid complexity proposal payload", http.StatusBadRequest)
			return
		}
		req.HotspotID = strings.TrimSpace(req.HotspotID)
		req.WorkstreamID = strings.TrimSpace(req.WorkstreamID)
		if req.HotspotID == "" || req.WorkstreamID == "" {
			http.Error(w, "hotspot_id and workstream_id are required", http.StatusBadRequest)
			return
		}
		hotspot, ok, err := findComplexityHotspot(r.Context(), store, req.HotspotID)
		if err != nil {
			http.Error(w, "failed to load complexity hotspots", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "complexity hotspot not found", http.StatusNotFound)
			return
		}
		if strings.TrimSpace(req.SandboxID) != "" && sandboxSink == nil {
			http.Error(w, "sandbox promotion store unavailable", http.StatusServiceUnavailable)
			return
		}
		now := time.Now().UTC()
		goalID := strings.TrimSpace(req.GoalID)
		if goalID == "" {
			goalID = "goal_complexity_" + safeIDPart(hotspot.HotspotID)
		}
		artifactID := strings.TrimSpace(req.ArtifactID)
		if artifactID == "" {
			artifactID = "art_complexity_proposal_" + safeIDPart(hotspot.HotspotID)
		}
		goal := domainworkstream.Goal{
			GoalID:          goalID,
			WorkstreamID:    req.WorkstreamID,
			Title:           "Complexity proposal: " + hotspot.FilePath,
			Description:     complexityProposalDescription(hotspot),
			SuccessCriteria: complexityProposalSuccessCriteria(hotspot),
			Verification:    complexityProposalVerification(hotspot),
			Status:          domainworkstream.StatusWaiting,
			CreatedAt:       now,
		}
		artifact := domainworkstream.Artifact{
			ArtifactID:   artifactID,
			WorkstreamID: req.WorkstreamID,
			Type:         "complexity_patch_proposal_request",
			Title:        "Complexity proposal approval: " + hotspot.FilePath,
			Status:       "pending_review",
			CreatedAt:    now,
		}
		proposalArtifact := domaincomplexity.ReportArtifact{
			ArtifactID:   "art_complexity_patch_proposal_" + safeIDPart(hotspot.HotspotID),
			ScanID:       hotspot.ScanID,
			WorkstreamID: req.WorkstreamID,
			Type:         "complexity_patch_proposal",
			Title:        "Complexity Patch Proposal: " + hotspot.FilePath,
			Status:       "pending_review",
			Content:      complexityapp.BuildPatchProposalMarkdown(hotspot),
			CreatedAt:    now,
		}
		coderDiffArtifact := domaincomplexity.ReportArtifact{
			ArtifactID:   "art_complexity_coder_diff_request_" + safeIDPart(hotspot.HotspotID),
			ScanID:       hotspot.ScanID,
			WorkstreamID: req.WorkstreamID,
			Type:         "complexity_coder_diff_request",
			Title:        "Complexity Coder Diff Request: " + hotspot.FilePath,
			Status:       "pending_review",
			Content:      complexityapp.BuildCoderDiffRequestMarkdown(hotspot),
			CreatedAt:    now,
		}
		if saver, ok := store.(interface {
			SaveReportArtifact(context.Context, domaincomplexity.ReportArtifact) error
		}); ok {
			if err := saver.SaveReportArtifact(r.Context(), proposalArtifact); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := saver.SaveReportArtifact(r.Context(), coderDiffArtifact); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		if err := workstreamSink.SaveGoal(r.Context(), goal); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := workstreamSink.SaveArtifact(r.Context(), artifact); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		response := map[string]any{
			"hotspot":                 hotspot,
			"goal":                    goal,
			"artifact":                artifact,
			"proposal_artifact":       proposalArtifact,
			"coder_diff_request":      coderDiffArtifact,
			"human_approval_required": true,
			"patch_applied":           false,
		}
		if isHighRiskComplexityHotspot(hotspot) {
			reviewGoal := buildHighRiskComplexityReviewGoal(req.WorkstreamID, hotspot, now)
			reviewArtifact := buildHighRiskComplexityReviewArtifact(req.WorkstreamID, hotspot, now)
			if err := workstreamSink.SaveGoal(r.Context(), reviewGoal); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := workstreamSink.SaveArtifact(r.Context(), reviewArtifact); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			response["high_risk_review_goal"] = reviewGoal
			response["high_risk_review_artifact"] = reviewArtifact
			response["pr_branch_required"] = true
		}
		if strings.TrimSpace(req.SandboxID) != "" {
			if sandboxSink == nil {
				http.Error(w, "sandbox promotion store unavailable", http.StatusServiceUnavailable)
				return
			}
			promotion := buildComplexitySandboxPromotionRequest(req, hotspot, goalID, now)
			decision := domainsandbox.EvaluatePromotionRequest(promotion)
			if err := sandboxSink.SavePromotionRequest(r.Context(), promotion); err != nil {
				http.Error(w, "failed to save complexity sandbox promotion request", http.StatusInternalServerError)
				return
			}
			log := domainsandbox.PromotionGateLog{
				EventID:             fmt.Sprintf("evt_complexity_promotion_gate_%d", now.UnixNano()),
				PromotionID:         promotion.PromotionID,
				GateStatus:          decision.Status,
				Reason:              decision.Reason,
				HumanApprovalStatus: promotion.HumanApprovalStatus,
				CreatedAt:           now,
			}
			if err := sandboxSink.SavePromotionGateLog(r.Context(), log); err != nil {
				http.Error(w, "failed to save complexity sandbox promotion gate log", http.StatusInternalServerError)
				return
			}
			response["sandbox_promotion"] = promotion
			response["sandbox_decision"] = decision
			response["sandbox_gate_log"] = log
		}
		writeJSON(w, http.StatusCreated, response)
	}
}

func HandleComplexityHotspotConcreteDiff(store ComplexityHotspotStore, workstreamSink ComplexityWorkstreamArtifactSink) http.HandlerFunc {
	return HandleComplexityHotspotConcreteDiffWithSandbox(store, workstreamSink, nil)
}

func HandleComplexityHotspotConcreteDiffWithSandbox(store ComplexityHotspotStore, workstreamSink ComplexityWorkstreamArtifactSink, sandboxSink SandboxPromotionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "complexity concrete diff mode unavailable", http.StatusServiceUnavailable)
			return
		}
		var req ComplexityConcreteDiffRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid complexity concrete diff payload", http.StatusBadRequest)
			return
		}
		req.HotspotID = strings.TrimSpace(req.HotspotID)
		if req.HotspotID == "" {
			http.Error(w, "hotspot_id is required", http.StatusBadRequest)
			return
		}
		hotspot, ok, err := findComplexityHotspot(r.Context(), store, req.HotspotID)
		if err != nil {
			http.Error(w, "failed to load complexity hotspots", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "complexity hotspot not found", http.StatusNotFound)
			return
		}
		if err := complexityapp.ValidateConcreteDiffForHotspot(hotspot, req.ConcreteDiff); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.SandboxID) != "" && sandboxSink == nil {
			http.Error(w, "sandbox promotion store unavailable", http.StatusServiceUnavailable)
			return
		}
		now := time.Now().UTC()
		artifactID := strings.TrimSpace(req.ArtifactID)
		if artifactID == "" {
			artifactID = "art_complexity_concrete_diff_" + safeIDPart(hotspot.HotspotID)
		}
		report := domaincomplexity.ReportArtifact{
			ArtifactID:   artifactID,
			ScanID:       hotspot.ScanID,
			WorkstreamID: strings.TrimSpace(req.WorkstreamID),
			Type:         "complexity_concrete_diff_proposal",
			Title:        "Complexity Concrete Diff Proposal: " + hotspot.FilePath,
			Status:       "pending_review",
			Content:      complexityapp.BuildConcreteDiffProposalMarkdown(hotspot, req.ConcreteDiff, req.TestResultPath, req.RollbackPlanPath),
			CreatedAt:    now,
		}
		if err := store.SaveReportArtifact(r.Context(), report); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		response := map[string]any{
			"hotspot":                 hotspot,
			"concrete_diff_artifact":  report,
			"human_approval_required": true,
			"patch_applied":           false,
		}
		if workstreamSink != nil && strings.TrimSpace(req.WorkstreamID) != "" {
			artifact := domainworkstream.Artifact{
				ArtifactID:   "art_workstream_" + safeIDPart(artifactID),
				WorkstreamID: strings.TrimSpace(req.WorkstreamID),
				Type:         "complexity_concrete_diff_review",
				Title:        "Complexity concrete diff review: " + hotspot.FilePath,
				Status:       "pending_review",
				CreatedAt:    now,
			}
			if err := workstreamSink.SaveArtifact(r.Context(), artifact); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			response["workstream_artifact"] = artifact
		}
		if strings.TrimSpace(req.SandboxID) != "" {
			if sandboxSink == nil {
				http.Error(w, "sandbox promotion store unavailable", http.StatusServiceUnavailable)
				return
			}
			promotion := buildComplexityConcreteDiffSandboxPromotionRequest(req, hotspot, now)
			decision := domainsandbox.EvaluatePromotionRequest(promotion)
			if err := sandboxSink.SavePromotionRequest(r.Context(), promotion); err != nil {
				http.Error(w, "failed to save complexity concrete diff sandbox promotion request", http.StatusInternalServerError)
				return
			}
			log := domainsandbox.PromotionGateLog{
				EventID:             fmt.Sprintf("evt_complexity_concrete_diff_gate_%d", now.UnixNano()),
				PromotionID:         promotion.PromotionID,
				GateStatus:          decision.Status,
				Reason:              decision.Reason,
				HumanApprovalStatus: promotion.HumanApprovalStatus,
				CreatedAt:           now,
			}
			if err := sandboxSink.SavePromotionGateLog(r.Context(), log); err != nil {
				http.Error(w, "failed to save complexity concrete diff sandbox promotion gate log", http.StatusInternalServerError)
				return
			}
			response["sandbox_promotion"] = promotion
			response["sandbox_decision"] = decision
			response["sandbox_gate_log"] = log
		}
		writeJSON(w, http.StatusCreated, response)
	}
}

func HandleComplexityHotspotCoderDiff(store ComplexityHotspotStore, generator ComplexityCoderDiffGenerator, workstreamSink ComplexityWorkstreamArtifactSink) http.HandlerFunc {
	return HandleComplexityHotspotCoderDiffWithSandbox(store, generator, workstreamSink, nil)
}

func HandleComplexityHotspotCoderDiffWithSandbox(store ComplexityHotspotStore, generator ComplexityCoderDiffGenerator, workstreamSink ComplexityWorkstreamArtifactSink, sandboxSink SandboxPromotionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil || generator == nil {
			http.Error(w, "complexity coder diff mode unavailable", http.StatusServiceUnavailable)
			return
		}
		var req ComplexityCoderDiffRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid complexity coder diff payload", http.StatusBadRequest)
			return
		}
		req.HotspotID = strings.TrimSpace(req.HotspotID)
		if req.HotspotID == "" {
			http.Error(w, "hotspot_id is required", http.StatusBadRequest)
			return
		}
		hotspot, ok, err := findComplexityHotspot(r.Context(), store, req.HotspotID)
		if err != nil {
			http.Error(w, "failed to load complexity hotspots", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "complexity hotspot not found", http.StatusNotFound)
			return
		}
		generationCtx := r.Context()
		var cancel context.CancelFunc
		if complexityCoderDiffGenerationTimeout > 0 {
			generationCtx, cancel = context.WithTimeout(r.Context(), complexityCoderDiffGenerationTimeout)
			defer cancel()
		}
		result, err := generator.GenerateConcreteDiff(generationCtx, complexityapp.CoderDiffRequest{
			Hotspot:      hotspot,
			Evidence:     findComplexityEvidenceForHotspot(r.Context(), store, hotspot.HotspotID),
			WorkstreamID: strings.TrimSpace(req.WorkstreamID),
			JobID:        strings.TrimSpace(req.JobID),
		})
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(generationCtx.Err(), context.DeadlineExceeded) {
				if saveErr := saveComplexityCoderDiffFailure(r.Context(), store, req, hotspot, "complexity coder diff generation timed out", time.Now().UTC()); saveErr != nil {
					http.Error(w, "failed to save complexity coder diff failure artifact", http.StatusInternalServerError)
					return
				}
				http.Error(w, "complexity coder diff generation timed out", http.StatusServiceUnavailable)
				return
			}
			if saveErr := saveComplexityCoderDiffFailure(r.Context(), store, req, hotspot, err.Error(), time.Now().UTC()); saveErr != nil {
				http.Error(w, "failed to save complexity coder diff failure artifact", http.StatusInternalServerError)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		concreteReq := ComplexityConcreteDiffRequest{
			HotspotID:                 req.HotspotID,
			WorkstreamID:              req.WorkstreamID,
			ArtifactID:                req.ArtifactID,
			PromotionID:               req.PromotionID,
			SandboxID:                 req.SandboxID,
			TargetPath:                req.TargetPath,
			DiffPath:                  req.DiffPath,
			ConcreteDiff:              result.ConcreteDiff,
			TestResultPath:            req.TestResultPath,
			RollbackPlanPath:          req.RollbackPlanPath,
			PostApplyVerificationPath: req.PostApplyVerificationPath,
			HumanApprovalStatus:       req.HumanApprovalStatus,
		}
		report, workstreamArtifact, sandboxPayload, err := saveComplexityConcreteDiffReview(r.Context(), store, workstreamSink, sandboxSink, concreteReq, hotspot, time.Now().UTC())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		response := map[string]any{
			"hotspot":                 hotspot,
			"coder_result":            result,
			"concrete_diff_artifact":  report,
			"human_approval_required": true,
			"patch_applied":           false,
		}
		if workstreamArtifact != nil {
			response["workstream_artifact"] = *workstreamArtifact
		}
		for key, value := range sandboxPayload {
			response[key] = value
		}
		writeJSON(w, http.StatusCreated, response)
	}
}

func saveComplexityCoderDiffFailure(ctx context.Context, store ComplexityHotspotStore, req ComplexityCoderDiffRequest, hotspot domaincomplexity.Hotspot, reason string, now time.Time) error {
	if store == nil {
		return nil
	}
	report := buildComplexityCoderDiffFailureArtifact(req, hotspot, reason, now)
	return store.SaveReportArtifact(ctx, report)
}

func buildComplexityCoderDiffFailureArtifact(req ComplexityCoderDiffRequest, hotspot domaincomplexity.Hotspot, reason string, now time.Time) domaincomplexity.ReportArtifact {
	jobID := strings.TrimSpace(req.JobID)
	idPart := jobID
	if idPart == "" {
		idPart = strings.TrimSpace(req.ArtifactID)
	}
	if idPart == "" {
		idPart = hotspot.HotspotID
	}
	artifactID := "art_complexity_coder_diff_failure_" + safeIDPart(idPart)
	if jobID == "" && strings.TrimSpace(req.ArtifactID) == "" {
		artifactID = fmt.Sprintf("%s_%d", artifactID, now.UnixNano())
	}
	content := strings.Join([]string{
		"# Complexity Coder Diff Failure",
		"",
		"Hotspot ID: `" + hotspot.HotspotID + "`",
		"Job ID: `" + nonEmptyOr(strings.TrimSpace(req.JobID), "(not provided)") + "`",
		"Workstream ID: `" + nonEmptyOr(strings.TrimSpace(req.WorkstreamID), "(not provided)") + "`",
		"Failure reason: `" + strings.TrimSpace(reason) + "`",
		"",
		"Patch applied: `false`",
		"Human approval required: `true` before any generated diff can be applied.",
		"",
		"This is a failure audit only. No review diff, Workstream apply artifact, Sandbox Promotion Request, or external PR was created.",
	}, "\n")
	return domaincomplexity.ReportArtifact{
		ArtifactID:   artifactID,
		ScanID:       hotspot.ScanID,
		WorkstreamID: strings.TrimSpace(req.WorkstreamID),
		Type:         "complexity_coder_diff_failure",
		Title:        "Complexity Coder Diff Failure: " + hotspot.FilePath,
		Status:       "failed",
		Content:      content,
		CreatedAt:    now,
	}
}

func nonEmptyOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func findComplexityEvidenceForHotspot(ctx context.Context, store ComplexityHotspotLister, hotspotID string) []domaincomplexity.HotspotEvidence {
	evidence, err := store.ListHotspotEvidence(ctx, 500)
	if err != nil {
		return nil
	}
	hotspotID = strings.TrimSpace(hotspotID)
	out := []domaincomplexity.HotspotEvidence{}
	for _, item := range evidence {
		if strings.TrimSpace(item.HotspotID) == hotspotID {
			out = append(out, item)
		}
	}
	return out
}

func saveComplexityConcreteDiffReview(ctx context.Context, store ComplexityHotspotStore, workstreamSink ComplexityWorkstreamArtifactSink, sandboxSink SandboxPromotionStore, req ComplexityConcreteDiffRequest, hotspot domaincomplexity.Hotspot, now time.Time) (domaincomplexity.ReportArtifact, *domainworkstream.Artifact, map[string]any, error) {
	if err := complexityapp.ValidateConcreteDiffForHotspot(hotspot, req.ConcreteDiff); err != nil {
		return domaincomplexity.ReportArtifact{}, nil, nil, err
	}
	artifactID := strings.TrimSpace(req.ArtifactID)
	if artifactID == "" {
		artifactID = "art_complexity_concrete_diff_" + safeIDPart(hotspot.HotspotID)
	}
	report := domaincomplexity.ReportArtifact{
		ArtifactID:   artifactID,
		ScanID:       hotspot.ScanID,
		WorkstreamID: strings.TrimSpace(req.WorkstreamID),
		Type:         "complexity_concrete_diff_proposal",
		Title:        "Complexity Concrete Diff Proposal: " + hotspot.FilePath,
		Status:       "pending_review",
		Content:      complexityapp.BuildConcreteDiffProposalMarkdown(hotspot, req.ConcreteDiff, req.TestResultPath, req.RollbackPlanPath),
		CreatedAt:    now,
	}
	if err := store.SaveReportArtifact(ctx, report); err != nil {
		return domaincomplexity.ReportArtifact{}, nil, nil, err
	}
	var workstreamArtifact *domainworkstream.Artifact
	if workstreamSink != nil && strings.TrimSpace(req.WorkstreamID) != "" {
		artifact := domainworkstream.Artifact{
			ArtifactID:   "art_workstream_" + safeIDPart(artifactID),
			WorkstreamID: strings.TrimSpace(req.WorkstreamID),
			Type:         "complexity_concrete_diff_review",
			Title:        "Complexity concrete diff review: " + hotspot.FilePath,
			Status:       "pending_review",
			CreatedAt:    now,
		}
		if err := workstreamSink.SaveArtifact(ctx, artifact); err != nil {
			return domaincomplexity.ReportArtifact{}, nil, nil, err
		}
		workstreamArtifact = &artifact
	}
	sandboxPayload := map[string]any{}
	if strings.TrimSpace(req.SandboxID) != "" {
		if sandboxSink == nil {
			return domaincomplexity.ReportArtifact{}, nil, nil, fmt.Errorf("sandbox promotion store unavailable")
		}
		promotion := buildComplexityConcreteDiffSandboxPromotionRequest(req, hotspot, now)
		decision := domainsandbox.EvaluatePromotionRequest(promotion)
		if err := sandboxSink.SavePromotionRequest(ctx, promotion); err != nil {
			return domaincomplexity.ReportArtifact{}, nil, nil, fmt.Errorf("failed to save complexity concrete diff sandbox promotion request")
		}
		log := domainsandbox.PromotionGateLog{
			EventID:             fmt.Sprintf("evt_complexity_concrete_diff_gate_%d", now.UnixNano()),
			PromotionID:         promotion.PromotionID,
			GateStatus:          decision.Status,
			Reason:              decision.Reason,
			HumanApprovalStatus: promotion.HumanApprovalStatus,
			CreatedAt:           now,
		}
		if err := sandboxSink.SavePromotionGateLog(ctx, log); err != nil {
			return domaincomplexity.ReportArtifact{}, nil, nil, fmt.Errorf("failed to save complexity concrete diff sandbox promotion gate log")
		}
		sandboxPayload["sandbox_promotion"] = promotion
		sandboxPayload["sandbox_decision"] = decision
		sandboxPayload["sandbox_gate_log"] = log
	}
	return report, workstreamArtifact, sandboxPayload, nil
}

func isHighRiskComplexityHotspot(hotspot domaincomplexity.Hotspot) bool {
	return strings.EqualFold(strings.TrimSpace(hotspot.RiskLevel), "high")
}

func buildHighRiskComplexityReviewGoal(workstreamID string, hotspot domaincomplexity.Hotspot, now time.Time) domainworkstream.Goal {
	return domainworkstream.Goal{
		GoalID:       "goal_complexity_high_risk_" + safeIDPart(hotspot.HotspotID),
		WorkstreamID: workstreamID,
		Title:        "High-risk complexity review: " + hotspot.FilePath,
		Description: strings.Join([]string{
			"High-risk complexity optimization must be handled as a separate review workflow.",
			"Hotspot: " + hotspot.HotspotID,
			"Type: " + hotspot.HotspotType,
			"File: " + hotspot.FilePath,
			"Risk: " + hotspot.RiskLevel,
		}, "\n"),
		SuccessCriteria: []string{
			"対象 hotspot だけを扱う別 Goal / PR として分離する",
			"具体 diff 生成前にリスク、挙動互換性、rollback plan を明文化する",
			"DB/API/cache/concurrency などの高リスク変更を低リスク refactor と混ぜない",
			"Sandbox Promotion Gate で diff / test / rollback / Human approval を満たすまで approve しない",
		},
		Verification: append([]string{
			"High-risk review checklist",
			"Sandbox Promotion Gate decision",
			"Human approval",
		}, hotspot.RequiredTests...),
		Status:    domainworkstream.StatusWaiting,
		CreatedAt: now,
	}
}

func buildHighRiskComplexityReviewArtifact(workstreamID string, hotspot domaincomplexity.Hotspot, now time.Time) domainworkstream.Artifact {
	return domainworkstream.Artifact{
		ArtifactID:   "art_complexity_high_risk_review_" + safeIDPart(hotspot.HotspotID),
		WorkstreamID: workstreamID,
		Type:         "complexity_high_risk_review_request",
		Title:        "High-risk complexity review: " + hotspot.FilePath,
		Status:       "pending_review",
		CreatedAt:    now,
	}
}

func buildComplexitySandboxPromotionRequest(req ComplexityHotspotProposalRequest, hotspot domaincomplexity.Hotspot, goalID string, now time.Time) domainsandbox.PromotionRequest {
	promotionID := strings.TrimSpace(req.PromotionID)
	if promotionID == "" {
		promotionID = "promo_complexity_" + safeIDPart(hotspot.HotspotID)
	}
	targetPath := strings.TrimSpace(req.TargetPath)
	if targetPath == "" {
		targetPath = hotspot.FilePath
	}
	humanApproval := strings.TrimSpace(req.HumanApprovalStatus)
	if humanApproval == "" {
		humanApproval = domainsandbox.ApprovalPending
	}
	reason := strings.TrimSpace("Complexity hotspot proposal for " + hotspot.HotspotType + ": " + hotspot.Summary)
	if reason == "Complexity hotspot proposal for :" {
		reason = "Complexity hotspot proposal"
	}
	return domainsandbox.PromotionRequest{
		PromotionID:               promotionID,
		SandboxID:                 strings.TrimSpace(req.SandboxID),
		WorkstreamID:              strings.TrimSpace(req.WorkstreamID),
		GoalID:                    goalID,
		RequestedBy:               "complexity_hotspot",
		TargetPath:                targetPath,
		DiffPath:                  strings.TrimSpace(req.DiffPath),
		TestResultPath:            strings.TrimSpace(req.TestResultPath),
		RiskLevel:                 hotspot.RiskLevel,
		Reason:                    reason,
		RollbackPlanPath:          strings.TrimSpace(req.RollbackPlanPath),
		PostApplyVerificationPath: strings.TrimSpace(req.PostApplyVerificationPath),
		HumanApprovalStatus:       humanApproval,
		CreatedAt:                 now,
	}
}

func buildComplexityConcreteDiffSandboxPromotionRequest(req ComplexityConcreteDiffRequest, hotspot domaincomplexity.Hotspot, now time.Time) domainsandbox.PromotionRequest {
	promotionID := strings.TrimSpace(req.PromotionID)
	if promotionID == "" {
		promotionID = "promo_complexity_diff_" + safeIDPart(hotspot.HotspotID)
	}
	targetPath := strings.TrimSpace(req.TargetPath)
	if targetPath == "" {
		targetPath = hotspot.FilePath
	}
	humanApproval := strings.TrimSpace(req.HumanApprovalStatus)
	if humanApproval == "" {
		humanApproval = domainsandbox.ApprovalPending
	}
	return domainsandbox.PromotionRequest{
		PromotionID:               promotionID,
		SandboxID:                 strings.TrimSpace(req.SandboxID),
		WorkstreamID:              strings.TrimSpace(req.WorkstreamID),
		GoalID:                    "goal_complexity_" + safeIDPart(hotspot.HotspotID),
		RequestedBy:               "complexity_concrete_diff",
		TargetPath:                targetPath,
		DiffPath:                  strings.TrimSpace(req.DiffPath),
		TestResultPath:            strings.TrimSpace(req.TestResultPath),
		RiskLevel:                 hotspot.RiskLevel,
		Reason:                    "Concrete diff proposal for complexity hotspot " + hotspot.HotspotID,
		RollbackPlanPath:          strings.TrimSpace(req.RollbackPlanPath),
		PostApplyVerificationPath: strings.TrimSpace(req.PostApplyVerificationPath),
		HumanApprovalStatus:       humanApproval,
		CreatedAt:                 now,
	}
}

func complexitySkillTaskText(req ComplexityHotspotScanRequest) string {
	parts := []string{"complexity hotspot scan", req.Repo}
	parts = append(parts, req.ScanScope...)
	return strings.TrimSpace(strings.Join(parts, " "))
}

func findComplexityHotspot(ctx context.Context, store ComplexityHotspotLister, hotspotID string) (domaincomplexity.Hotspot, bool, error) {
	hotspots, err := store.ListHotspots(ctx, 500)
	if err != nil {
		return domaincomplexity.Hotspot{}, false, err
	}
	for _, hotspot := range hotspots {
		if hotspot.HotspotID == hotspotID {
			return hotspot, true, nil
		}
	}
	return domaincomplexity.Hotspot{}, false, nil
}

func complexityProposalDescription(hotspot domaincomplexity.Hotspot) string {
	parts := []string{
		"Report-only complexity hotspot selected for proposal mode.",
		"Hotspot: " + hotspot.HotspotID,
		"Type: " + hotspot.HotspotType,
		"File: " + hotspot.FilePath,
		"Risk: " + hotspot.RiskLevel,
	}
	if hotspot.LineStart > 0 {
		parts = append(parts, "Line: "+strconv.Itoa(hotspot.LineStart))
	}
	if strings.TrimSpace(hotspot.SuggestedImprovement) != "" {
		parts = append(parts, "Suggested improvement: "+hotspot.SuggestedImprovement)
	}
	return strings.Join(parts, "\n")
}

func complexityProposalSuccessCriteria(hotspot domaincomplexity.Hotspot) []string {
	criteria := []string{
		"対象 hotspot 以外の無関係な変更を混ぜない",
		"挙動互換性を維持する",
		"Human approval なしに patch を適用しない",
	}
	if hotspot.EstimatedComplexity != "" && hotspot.EstimatedAfter != "" {
		criteria = append(criteria, "複雑性見積もりを "+hotspot.EstimatedComplexity+" から "+hotspot.EstimatedAfter+" へ改善する案を説明する")
	}
	return criteria
}

func complexityProposalVerification(hotspot domaincomplexity.Hotspot) []string {
	verification := append([]string(nil), hotspot.RequiredTests...)
	if len(verification) == 0 {
		verification = []string{"既存テストを実行する", "対象ファイルの差分をレビューする"}
	}
	return verification
}

func safeIDPart(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range raw {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}
