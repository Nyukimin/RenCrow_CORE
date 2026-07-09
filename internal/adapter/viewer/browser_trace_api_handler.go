package viewer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	browsertraceapp "github.com/Nyukimin/RenCrow_CORE/internal/application/browsertrace"
	domaintrace "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
)

type BrowserTraceAPILister interface {
	ListTraceRuns(ctx context.Context, limit int) ([]domaintrace.TraceRun, error)
	ListAPICandidates(ctx context.Context, limit int) ([]domaintrace.APICandidate, error)
	ListAPICandidateSchemas(ctx context.Context, limit int) ([]domaintrace.APICandidateSchema, error)
	ListAPICandidateValidationResults(ctx context.Context, limit int) ([]domaintrace.APICandidateValidationResult, error)
	ListAPICoverageReports(ctx context.Context, limit int) ([]domaintrace.APICoverageReport, error)
	ListAPIArtifacts(ctx context.Context, limit int) ([]domaintrace.APIArtifact, error)
}

type BrowserTraceAPIStore interface {
	BrowserTraceAPILister
	SaveTraceRun(ctx context.Context, item domaintrace.TraceRun) error
	SaveAPICandidate(ctx context.Context, item domaintrace.APICandidate) error
	SaveAPICandidateSchema(ctx context.Context, item domaintrace.APICandidateSchema) error
	SaveAPICandidateValidationResult(ctx context.Context, item domaintrace.APICandidateValidationResult) error
	SaveAPICoverageReport(ctx context.Context, item domaintrace.APICoverageReport) error
	SaveAPIArtifact(ctx context.Context, item domaintrace.APIArtifact) error
}

type BrowserTraceAPIDiscoverer interface {
	Discover(req browsertraceapp.DiscoverRequest) (domaintrace.DiscoveryResult, error)
}

type BrowserTraceAPICandidateSink interface {
	SaveBrowserTraceAPICandidates(ctx context.Context, result domaintrace.DiscoveryResult) error
}

type BrowserTraceWorkstreamArtifactSink interface {
	SaveArtifact(ctx context.Context, item domainworkstream.Artifact) error
}

type BrowserTraceAPIDiscoverRequest struct {
	TraceRunID      string    `json:"trace_run_id"`
	WorkstreamID    string    `json:"workstream_id,omitempty"`
	SiteID          string    `json:"site_id,omitempty"`
	Goal            string    `json:"goal,omitempty"`
	TracePath       string    `json:"trace_path"`
	RequestsPath    string    `json:"requests_path"`
	ResponsesPath   string    `json:"responses_path"`
	LivePolicyCheck bool      `json:"live_policy_check,omitempty"`
	CapturedAt      time.Time `json:"captured_at,omitempty"`
}

type BrowserTraceAPIFetcherProposalRequest struct {
	CandidateID   string `json:"candidate_id"`
	WorkstreamID  string `json:"workstream_id,omitempty"`
	HumanApproved bool   `json:"human_approved"`
}

type BrowserTraceAPIValidationReviewRequest struct {
	CandidateID         string `json:"candidate_id"`
	Reviewer            string `json:"reviewer"`
	ReviewNote          string `json:"review_note,omitempty"`
	HumanApproved       bool   `json:"human_approved"`
	TermsReviewed       bool   `json:"terms_reviewed"`
	OfficialAPIReviewed bool   `json:"official_api_reviewed"`
	PIIReviewed         bool   `json:"pii_reviewed"`
	SchemaReviewed      bool   `json:"schema_reviewed"`
	RiskReviewed        bool   `json:"risk_reviewed,omitempty"`
}

func HandleBrowserTraceAPIStatus(store BrowserTraceAPILister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "browser trace api store unavailable", http.StatusServiceUnavailable)
			return
		}
		limit, err := parseViewerLimit(r.URL.Query().Get("limit"), 20, 100)
		if err != nil {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		runs, err := store.ListTraceRuns(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load trace runs", http.StatusInternalServerError)
			return
		}
		candidates, err := store.ListAPICandidates(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load api candidates", http.StatusInternalServerError)
			return
		}
		schemas, err := store.ListAPICandidateSchemas(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load api schemas", http.StatusInternalServerError)
			return
		}
		validations, err := store.ListAPICandidateValidationResults(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load api validation results", http.StatusInternalServerError)
			return
		}
		coverage, err := store.ListAPICoverageReports(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load api coverage reports", http.StatusInternalServerError)
			return
		}
		artifacts, err := store.ListAPIArtifacts(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load api artifacts", http.StatusInternalServerError)
			return
		}
		if runs == nil {
			runs = []domaintrace.TraceRun{}
		}
		if candidates == nil {
			candidates = []domaintrace.APICandidate{}
		}
		if schemas == nil {
			schemas = []domaintrace.APICandidateSchema{}
		}
		if validations == nil {
			validations = []domaintrace.APICandidateValidationResult{}
		}
		if coverage == nil {
			coverage = []domaintrace.APICoverageReport{}
		}
		if artifacts == nil {
			artifacts = []domaintrace.APIArtifact{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"trace_runs":       runs,
			"api_candidates":   candidates,
			"api_schemas":      schemas,
			"api_validations":  validations,
			"coverage_reports": coverage,
			"api_artifacts":    artifacts,
		})
	}
}

func HandleBrowserTraceAPIDiscover(store BrowserTraceAPIStore, discoverer BrowserTraceAPIDiscoverer, candidateSink BrowserTraceAPICandidateSink, workstreamArtifactSink BrowserTraceWorkstreamArtifactSink) http.HandlerFunc {
	return HandleBrowserTraceAPIDiscoverWithPolicy(store, discoverer, candidateSink, workstreamArtifactSink, browsertraceapp.DefaultValidationPolicy())
}

func HandleBrowserTraceAPIDiscoverWithPolicy(store BrowserTraceAPIStore, discoverer BrowserTraceAPIDiscoverer, candidateSink BrowserTraceAPICandidateSink, workstreamArtifactSink BrowserTraceWorkstreamArtifactSink, validationPolicy browsertraceapp.ValidationPolicy) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil || discoverer == nil {
			http.Error(w, "browser trace api discovery unavailable", http.StatusServiceUnavailable)
			return
		}
		var req BrowserTraceAPIDiscoverRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid browser trace api payload", http.StatusBadRequest)
			return
		}
		result, err := discoverer.Discover(browsertraceapp.DiscoverRequest{
			TraceRunID:    req.TraceRunID,
			WorkstreamID:  req.WorkstreamID,
			SiteID:        req.SiteID,
			Goal:          req.Goal,
			TracePath:     req.TracePath,
			RequestsPath:  req.RequestsPath,
			ResponsesPath: req.ResponsesPath,
			CapturedAt:    req.CapturedAt,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := store.SaveTraceRun(r.Context(), result.Run); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for _, candidate := range result.Candidates {
			if err := store.SaveAPICandidate(r.Context(), candidate); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		for _, schema := range result.Schemas {
			if err := store.SaveAPICandidateSchema(r.Context(), schema); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		validations := browsertraceapp.ValidateAPICandidates(result.Candidates, validationPolicy, result.Run.CreatedAt)
		if req.LivePolicyCheck {
			validations = browsertraceapp.ValidateAPICandidatesWithLivePolicy(r.Context(), result.Candidates, validationPolicy, result.Run.CreatedAt, browsertraceapp.HTTPPolicyChecker{})
		}
		for _, validation := range validations {
			if err := store.SaveAPICandidateValidationResult(r.Context(), validation); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		if err := store.SaveAPICoverageReport(r.Context(), result.Coverage); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		artifacts := browsertraceapp.BuildAPIArtifactsWithValidations(result, validations)
		for _, artifact := range artifacts {
			if err := store.SaveAPIArtifact(r.Context(), artifact); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		if candidateSink != nil {
			if err := candidateSink.SaveBrowserTraceAPICandidates(r.Context(), result); err != nil {
				http.Error(w, "browser trace api candidate staging failed", http.StatusInternalServerError)
				return
			}
		}
		if workstreamArtifactSink != nil && result.Run.WorkstreamID != "" {
			for _, artifact := range artifacts {
				if err := workstreamArtifactSink.SaveArtifact(r.Context(), domainworkstream.Artifact{
					ArtifactID:   artifact.ArtifactID,
					WorkstreamID: result.Run.WorkstreamID,
					Type:         "browser_trace_" + artifact.Type,
					Title:        artifact.Title,
					Status:       "pending_review",
					CreatedAt:    artifact.CreatedAt,
				}); err != nil {
					http.Error(w, "browser trace workstream artifact registration failed", http.StatusInternalServerError)
					return
				}
			}
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"trace_run":       result.Run,
			"api_candidates":  result.Candidates,
			"api_schemas":     result.Schemas,
			"api_validations": validations,
			"coverage_report": result.Coverage,
			"api_artifacts":   artifacts,
		})
	}
}

func HandleBrowserTraceAPIValidationReview(store BrowserTraceAPIStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "browser trace api store unavailable", http.StatusServiceUnavailable)
			return
		}
		defer r.Body.Close()
		var req BrowserTraceAPIValidationReviewRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid validation review request", http.StatusBadRequest)
			return
		}
		if req.CandidateID == "" {
			http.Error(w, "candidate_id is required", http.StatusBadRequest)
			return
		}
		if req.Reviewer == "" {
			http.Error(w, "reviewer is required", http.StatusBadRequest)
			return
		}
		candidates, err := store.ListAPICandidates(r.Context(), 500)
		if err != nil {
			http.Error(w, "failed to load api candidates", http.StatusInternalServerError)
			return
		}
		var candidate *domaintrace.APICandidate
		for i := range candidates {
			if candidates[i].CandidateID == req.CandidateID {
				candidate = &candidates[i]
				break
			}
		}
		if candidate == nil {
			http.Error(w, "api candidate not found", http.StatusNotFound)
			return
		}
		now := time.Now().UTC()
		issues := browserTraceValidationReviewIssues(req)
		validation := domaintrace.APICandidateValidationResult{
			ValidationID: fmt.Sprintf("api_val_review_%s_%d", req.CandidateID, now.UnixNano()),
			CandidateID:  req.CandidateID,
			TraceRunID:   candidate.TraceRunID,
			Passed:       len(issues) == 0,
			Status:       "validated",
			Issues:       issues,
			CreatedAt:    now,
		}
		if !validation.Passed {
			validation.Status = "needs_review"
		}
		if err := store.SaveAPICandidateValidationResult(r.Context(), validation); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"candidate":            candidate,
			"validation":           validation,
			"official_promotion":   false,
			"implementation_apply": false,
		})
	}
}

func browserTraceValidationReviewIssues(req BrowserTraceAPIValidationReviewRequest) []domaintrace.APIValidationIssue {
	var issues []domaintrace.APIValidationIssue
	add := func(code, message, severity string) {
		issues = append(issues, domaintrace.APIValidationIssue{Code: code, Message: message, Severity: severity})
	}
	if !req.HumanApproved {
		add("human_approval_required", "human approval is required before marking an API candidate validated", "high")
	}
	if !req.TermsReviewed {
		add("terms_review_required", "terms, robots, API policy, and rate limit review must be recorded", "high")
	}
	if !req.OfficialAPIReviewed {
		add("official_api_review_required", "official API, RSS, Atom, or public feed alternative review must be recorded", "medium")
	}
	if !req.PIIReviewed {
		add("pii_review_required", "personal data safety review must be recorded", "high")
	}
	if !req.SchemaReviewed {
		add("schema_review_required", "schema and response sample review must be recorded", "medium")
	}
	if !req.RiskReviewed {
		add("risk_review_required", "risk review must be recorded before fetcher proposal", "medium")
	}
	return issues
}

func HandleBrowserTraceAPIFetcherProposal(store BrowserTraceAPIStore, workstreamArtifactSink BrowserTraceWorkstreamArtifactSink) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "browser trace api store unavailable", http.StatusServiceUnavailable)
			return
		}
		defer r.Body.Close()
		var req BrowserTraceAPIFetcherProposalRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid fetcher proposal request", http.StatusBadRequest)
			return
		}
		if req.CandidateID == "" {
			http.Error(w, "candidate_id is required", http.StatusBadRequest)
			return
		}
		if !req.HumanApproved {
			http.Error(w, "human_approved=true is required before fetcher proposal", http.StatusBadRequest)
			return
		}
		candidates, err := store.ListAPICandidates(r.Context(), 500)
		if err != nil {
			http.Error(w, "failed to load api candidates", http.StatusInternalServerError)
			return
		}
		var candidate *domaintrace.APICandidate
		for i := range candidates {
			if candidates[i].CandidateID == req.CandidateID {
				candidate = &candidates[i]
				break
			}
		}
		if candidate == nil {
			http.Error(w, "api candidate not found", http.StatusNotFound)
			return
		}
		validations, err := store.ListAPICandidateValidationResults(r.Context(), 500)
		if err != nil {
			http.Error(w, "failed to load api validations", http.StatusInternalServerError)
			return
		}
		var validation *domaintrace.APICandidateValidationResult
		for i := range validations {
			if validations[i].CandidateID == req.CandidateID {
				if validation == nil || validations[i].CreatedAt.After(validation.CreatedAt) {
					validation = &validations[i]
				}
			}
		}
		if validation == nil || !validation.Passed || validation.Status != "validated" {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"status":     "blocked",
				"reason":     "validated api candidate is required before fetcher proposal",
				"validation": validation,
			})
			return
		}
		schemas, err := store.ListAPICandidateSchemas(r.Context(), 500)
		if err != nil {
			http.Error(w, "failed to load api schemas", http.StatusInternalServerError)
			return
		}
		now := time.Now().UTC()
		artifact := domaintrace.APIArtifact{
			ArtifactID:   "art_fetcher_proposal_" + req.CandidateID,
			TraceRunID:   candidate.TraceRunID,
			WorkstreamID: req.WorkstreamID,
			Type:         "fetcher_proposal",
			Title:        "Fetcher Proposal: " + req.CandidateID,
			Status:       "pending_review",
			Content:      browsertraceapp.BuildFetcherProposalMarkdown(*candidate, *validation, schemas),
			CreatedAt:    now,
		}
		if err := store.SaveAPIArtifact(r.Context(), artifact); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var workstreamArtifact *domainworkstream.Artifact
		if workstreamArtifactSink != nil && req.WorkstreamID != "" {
			item := domainworkstream.Artifact{
				ArtifactID:   artifact.ArtifactID,
				WorkstreamID: req.WorkstreamID,
				Type:         "browser_trace_fetcher_proposal",
				Title:        artifact.Title,
				Status:       "pending_review",
				CreatedAt:    artifact.CreatedAt,
			}
			if err := workstreamArtifactSink.SaveArtifact(r.Context(), item); err != nil {
				http.Error(w, "fetcher proposal workstream artifact registration failed", http.StatusInternalServerError)
				return
			}
			workstreamArtifact = &item
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"api_artifact":         artifact,
			"workstream_artifact":  workstreamArtifact,
			"candidate":            candidate,
			"validation":           validation,
			"official_promotion":   false,
			"implementation_apply": false,
		})
	}
}
