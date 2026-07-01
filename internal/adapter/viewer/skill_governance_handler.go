package viewer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	domainskill "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/skillgovernance"
)

const maxSkillGovernanceEvidenceBytes = 256 * 1024

type SkillGovernanceLister interface {
	ListSkillManifests(ctx context.Context, limit int) ([]domainskill.SkillManifest, error)
	ListSkillTriggerLogs(ctx context.Context, limit int) ([]domainskill.SkillTriggerLog, error)
	ListSkillChangeLogs(ctx context.Context, limit int) ([]domainskill.SkillChangeLog, error)
	ListContributionGateLogs(ctx context.Context, limit int) ([]domainskill.ContributionGateLog, error)
	ListExternalPRSubmitRecords(ctx context.Context, limit int) ([]domainskill.ExternalPRSubmitRecord, error)
	ListCoderTranscriptEntries(ctx context.Context, limit int) ([]domainskill.CoderTranscriptEntry, error)
}

type SkillTriggerLogSaver interface {
	SaveSkillTriggerLog(ctx context.Context, log domainskill.SkillTriggerLog) error
}

type ContributionGateLogSaver interface {
	SaveContributionGateLog(ctx context.Context, log domainskill.ContributionGateLog) error
}

type SkillChangeLogSaver interface {
	SaveSkillChangeLog(ctx context.Context, log domainskill.SkillChangeLog) error
}

type ExternalPRSubmitRecordSaver interface {
	SaveExternalPRSubmitRecord(ctx context.Context, record domainskill.ExternalPRSubmitRecord) error
}

type SkillGovernanceStore interface {
	SkillGovernanceLister
	SkillTriggerLogSaver
	ContributionGateLogSaver
	SkillChangeLogSaver
	ExternalPRSubmitRecordSaver
}

func HandleSkillGovernanceRecent(store SkillGovernanceLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "skill governance store unavailable", http.StatusServiceUnavailable)
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
		manifests, err := store.ListSkillManifests(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load skill manifests", http.StatusInternalServerError)
			return
		}
		triggers, err := store.ListSkillTriggerLogs(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load skill trigger logs", http.StatusInternalServerError)
			return
		}
		changes, err := store.ListSkillChangeLogs(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load skill change logs", http.StatusInternalServerError)
			return
		}
		contributions, err := store.ListContributionGateLogs(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load contribution gate logs", http.StatusInternalServerError)
			return
		}
		externalPRSubmits, err := store.ListExternalPRSubmitRecords(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load external PR submit records", http.StatusInternalServerError)
			return
		}
		transcripts, err := store.ListCoderTranscriptEntries(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load coder transcript logs", http.StatusInternalServerError)
			return
		}
		if externalPRSubmits == nil {
			externalPRSubmits = []domainskill.ExternalPRSubmitRecord{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"manifests":                      manifests,
			"trigger_logs":                   triggers,
			"change_logs":                    changes,
			"contributions":                  contributions,
			"external_pr_submit_records":     externalPRSubmits,
			"external_pr_adapter":            "unconfigured",
			"external_pr_adapter_configured": false,
			"human_approval_required_for_pr": true,
			"coder_transcripts":              transcripts,
		})
	}
}

type skillBootstrapRequest struct {
	Text         string   `json:"text"`
	Intent       string   `json:"intent,omitempty"`
	Agent        string   `json:"agent,omitempty"`
	WorkstreamID string   `json:"workstream_id,omitempty"`
	UsedSkillIDs []string `json:"used_skill_ids,omitempty"`
}

func HandleSkillGovernanceBootstrap(store SkillGovernanceStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "skill governance store unavailable", http.StatusServiceUnavailable)
			return
		}
		var req skillBootstrapRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid skill bootstrap payload", http.StatusBadRequest)
			return
		}
		if req.Text == "" && req.Intent == "" {
			http.Error(w, "text or intent is required", http.StatusBadRequest)
			return
		}
		manifests, err := store.ListSkillManifests(r.Context(), 1000)
		if err != nil {
			http.Error(w, "failed to load skill manifests", http.StatusInternalServerError)
			return
		}
		now := time.Now().UTC()
		logs := domainskill.BuildBootstrapTriggerLogs(manifests, domainskill.TaskContext{
			Text:         req.Text,
			Intent:       req.Intent,
			Agent:        req.Agent,
			WorkstreamID: req.WorkstreamID,
		}, req.UsedSkillIDs, now, func(index int, skillID string) string {
			return fmt.Sprintf("evt_skill_bootstrap_%d_%d", now.UnixNano(), index+1)
		})
		for _, log := range logs {
			if err := store.SaveSkillTriggerLog(r.Context(), log); err != nil {
				http.Error(w, "failed to save skill trigger log", http.StatusInternalServerError)
				return
			}
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"trigger_logs": logs,
		})
	}
}

func HandleSkillGovernanceContributionGate(store SkillGovernanceStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "skill governance store unavailable", http.StatusServiceUnavailable)
			return
		}
		var req domainskill.ContributionGateLog
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid contribution gate payload", http.StatusBadRequest)
			return
		}
		now := time.Now().UTC()
		eventID := req.EventID
		if eventID == "" {
			eventID = fmt.Sprintf("evt_contribution_gate_%d", now.UnixNano())
		}
		log, decision, err := domainskill.NewContributionGateLog(eventID, req, now)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := store.SaveContributionGateLog(r.Context(), log); err != nil {
			http.Error(w, "failed to save contribution gate log", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"gate_log": log,
			"decision": decision,
		})
	}
}

func HandleSkillGovernanceSkillChange(store SkillGovernanceStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "skill governance store unavailable", http.StatusServiceUnavailable)
			return
		}
		var req domainskill.SkillChangeLog
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid skill change payload", http.StatusBadRequest)
			return
		}
		now := time.Now().UTC()
		changeID := req.ChangeID
		if changeID == "" {
			changeID = fmt.Sprintf("chg_skill_%d", now.UnixNano())
		}
		log, decision, err := domainskill.NewSkillChangeLog(changeID, req, now)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := store.SaveSkillChangeLog(r.Context(), log); err != nil {
			http.Error(w, "failed to save skill change log", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"change_log": log,
			"decision":   decision,
		})
	}
}

func HandleSkillGovernanceExternalPRSubmit(store SkillGovernanceStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "skill governance store unavailable", http.StatusServiceUnavailable)
			return
		}
		var req domainskill.ExternalPRSubmitRecord
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid external PR submit payload", http.StatusBadRequest)
			return
		}
		if !req.HumanApproved {
			http.Error(w, "human approval is required before external PR submit", http.StatusForbidden)
			return
		}
		gate, ok, err := findPassedContributionGate(r.Context(), store, req.ContributionEventID, req.Repo)
		if err != nil {
			http.Error(w, "failed to load contribution gate logs", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "passed contribution gate is required before external PR submit", http.StatusConflict)
			return
		}
		now := time.Now().UTC()
		if req.TargetBranch == "" {
			req.TargetBranch = gate.TargetBranch
		}
		record, err := domainskill.NewBlockedExternalPRSubmitRecord(req, now)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := store.SaveExternalPRSubmitRecord(r.Context(), record); err != nil {
			http.Error(w, "failed to save external PR submit record", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"external_pr_submit_record":         record,
			"external_pr_created":               false,
			"post_submit_verified":              false,
			"human_approval_required_for_pr":    true,
			"external_pr_adapter_configuration": "required",
			"message":                           "external PR adapter is not configured; no PR was created",
		})
	}
}

func findPassedContributionGate(ctx context.Context, store SkillGovernanceLister, eventID string, repo string) (domainskill.ContributionGateLog, bool, error) {
	gates, err := store.ListContributionGateLogs(ctx, 1000)
	if err != nil {
		return domainskill.ContributionGateLog{}, false, err
	}
	for _, gate := range gates {
		if strings.TrimSpace(eventID) != "" && gate.EventID != eventID {
			continue
		}
		if strings.TrimSpace(repo) != "" && gate.Repo != repo {
			continue
		}
		if gate.GateStatus != domainskill.GateStatusPassed {
			continue
		}
		return gate, true, nil
	}
	return domainskill.ContributionGateLog{}, false, nil
}

func HandleSkillGovernanceSkillChangeEval(store SkillGovernanceStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "skill governance store unavailable", http.StatusServiceUnavailable)
			return
		}
		var req domainskill.SkillChangeEvalRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid skill change eval payload", http.StatusBadRequest)
			return
		}
		if err := hydrateSkillChangeEvalEvidence(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		now := time.Now().UTC()
		changeID := req.ChangeID
		if changeID == "" {
			changeID = fmt.Sprintf("chg_eval_%d", now.UnixNano())
		}
		req.ChangeID = changeID
		result := domainskill.RunSkillChangeEval(req)
		if result.Status != domainskill.SkillChangeEvalStatusPassed {
			writeJSON(w, http.StatusBadRequest, result)
			return
		}
		log, decision, err := domainskill.NewSkillChangeLog(changeID, result.ChangeLog, now)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if decision.Status != domainskill.ChangeGateStatusPassed {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"eval_result":   result,
				"gate_decision": decision,
			})
			return
		}
		if err := store.SaveSkillChangeLog(r.Context(), log); err != nil {
			http.Error(w, "failed to save skill change eval log", http.StatusInternalServerError)
			return
		}
		result.ChangeLog = log
		result.GateDecision = decision
		writeJSON(w, http.StatusCreated, result)
	}
}

func hydrateSkillChangeEvalEvidence(req *domainskill.SkillChangeEvalRequest) error {
	if strings.TrimSpace(req.SkillDiff) == "" && strings.TrimSpace(req.SkillDiffPath) != "" {
		content, err := readSkillGovernanceEvidenceFile(req.SkillDiffPath)
		if err != nil {
			return fmt.Errorf("invalid skill_diff_path: %w", err)
		}
		req.SkillDiff = content
	}
	if strings.TrimSpace(req.AgentTranscript) == "" && strings.TrimSpace(req.AgentTranscriptPath) != "" {
		content, err := readSkillGovernanceEvidenceFile(req.AgentTranscriptPath)
		if err != nil {
			return fmt.Errorf("invalid agent_transcript_path: %w", err)
		}
		req.AgentTranscript = content
	}
	return nil
}

func readSkillGovernanceEvidenceFile(rawPath string) (string, error) {
	path := strings.TrimSpace(rawPath)
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("absolute path is not allowed")
	}
	clean := filepath.Clean(path)
	if clean == "." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", fmt.Errorf("path traversal is not allowed")
	}
	if isDeniedSkillGovernanceEvidencePath(clean) {
		return "", fmt.Errorf("secret or internal path is not allowed")
	}
	if !isAllowedSkillGovernanceEvidencePath(clean) {
		return "", fmt.Errorf("path must be under skills/, commands/, docs/, vault/, workspace/, sandbox/, tmp/, logs/, or .o11y/")
	}
	info, err := os.Stat(clean)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("directory is not allowed")
	}
	if info.Size() > maxSkillGovernanceEvidenceBytes {
		return "", fmt.Errorf("evidence file is too large")
	}
	data, err := os.ReadFile(clean)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func isAllowedSkillGovernanceEvidencePath(path string) bool {
	allowed := []string{
		"skills" + string(filepath.Separator),
		"commands" + string(filepath.Separator),
		"docs" + string(filepath.Separator),
		"vault" + string(filepath.Separator),
		"workspace" + string(filepath.Separator),
		"sandbox" + string(filepath.Separator),
		"tmp" + string(filepath.Separator),
		"logs" + string(filepath.Separator),
		".o11y" + string(filepath.Separator),
	}
	for _, prefix := range allowed {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func isDeniedSkillGovernanceEvidencePath(path string) bool {
	lower := strings.ToLower(filepath.ToSlash(path))
	parts := strings.Split(lower, "/")
	for _, part := range parts {
		switch part {
		case ".git", "secrets", "private", ".venv", "venv", "node_modules":
			return true
		}
	}
	base := filepath.Base(lower)
	if base == ".env" || strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") || strings.HasSuffix(base, ".p12") ||
		base == "id_rsa" || base == "credentials.json" || base == "token.json" || base == "cookies.sqlite" {
		return true
	}
	return false
}
