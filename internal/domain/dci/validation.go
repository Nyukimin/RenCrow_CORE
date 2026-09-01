package dci

import (
	"fmt"
	"math"
	"strings"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func ValidateSearchTrace(trace SearchTrace) error {
	return validateSearchTrace(trace, false)
}

// ValidateStoredSearchTrace validates a trace read from the canonical DCI
// store. Historical migrated rows may be explicitly unattributed, but runtime
// writers must use ValidateSearchTrace and authenticated actor identity.
func ValidateStoredSearchTrace(trace SearchTrace) error {
	return validateSearchTrace(trace, true)
}

func validateSearchTrace(trace SearchTrace, allowLegacyUnattributed bool) error {
	if err := trace.TraceID.Validate(); err != nil {
		return fmt.Errorf("dci search trace trace_id: %w", err)
	}
	if err := trace.ActionID.Validate(); err != nil {
		return fmt.Errorf("dci search trace action_id: %w", err)
	}
	if trace.StartedAt.IsZero() {
		return fmt.Errorf("dci search trace started_at is required")
	}
	if trace.IdempotencyKey != "" && strings.TrimSpace(trace.IdempotencyKey) != trace.IdempotencyKey {
		return fmt.Errorf("dci search trace idempotency_key must not have surrounding whitespace")
	}
	switch trace.ActorAttribution {
	case ActorAttributionAuthenticated:
		if err := ValidateActor(trace.ActorKind, trace.ActorID); err != nil {
			return fmt.Errorf("dci search trace actor: %w", err)
		}
	case ActorAttributionLegacyUnattributed:
		if !allowLegacyUnattributed {
			return fmt.Errorf("dci search trace actor_attribution %q is not allowed for runtime results", trace.ActorAttribution)
		}
		if trace.ActorKind != "" || trace.ActorID != "" {
			return fmt.Errorf("dci search trace legacy_unattributed requires empty actor_kind and actor_id")
		}
	default:
		return fmt.Errorf("dci search trace invalid actor_attribution %q", trace.ActorAttribution)
	}
	if strings.TrimSpace(trace.Mode) == "" {
		return fmt.Errorf("dci search trace mode is required")
	}
	if strings.TrimSpace(trace.UserQuery) == "" {
		return fmt.Errorf("dci search trace user_query is required")
	}
	status := strings.TrimSpace(trace.Status)
	if status == "" {
		return fmt.Errorf("dci search trace status is required")
	}
	if !isSearchTraceStatus(status) {
		return fmt.Errorf("dci search trace invalid status %q", trace.Status)
	}
	if isTerminalSearchTraceStatus(status) && trace.EndedAt.IsZero() {
		return fmt.Errorf("dci search trace terminal status %q requires ended_at", status)
	}
	if !trace.EndedAt.IsZero() && trace.EndedAt.Before(trace.StartedAt) {
		return fmt.Errorf("dci search trace ended_at must be >= started_at")
	}
	if status == "failed" && strings.TrimSpace(trace.ErrorMessage) == "" {
		return fmt.Errorf("dci search trace failed status requires error_message")
	}
	if trace.FinalEvidenceCount < 0 {
		return fmt.Errorf("dci search trace final_evidence_count must be >= 0")
	}
	seenSteps := make(map[int]struct{}, len(trace.Steps))
	seenEvents := make(map[modulecore.EventID]struct{}, len(trace.Steps))
	for _, step := range trace.Steps {
		if err := ValidateSearchStep(step); err != nil {
			return err
		}
		if _, ok := seenSteps[step.StepNo]; ok {
			return fmt.Errorf("dci search trace duplicate step_no %d", step.StepNo)
		}
		seenSteps[step.StepNo] = struct{}{}
		if _, ok := seenEvents[step.EventID]; ok {
			return fmt.Errorf("dci search trace duplicate step event_id %q", step.EventID)
		}
		seenEvents[step.EventID] = struct{}{}
	}
	return nil
}

func ValidateSearchStep(step SearchStep) error {
	if step.StepNo <= 0 {
		return fmt.Errorf("dci search step step_no is required")
	}
	if err := step.EventID.Validate(); err != nil {
		return fmt.Errorf("dci search step event_id: %w", err)
	}
	if strings.TrimSpace(step.EventType) == "" {
		return fmt.Errorf("dci search step event_type is required")
	}
	if step.EventType != "dci.file.read" {
		return fmt.Errorf("dci search step invalid event_type %q", step.EventType)
	}
	if strings.TrimSpace(step.Tool) == "" {
		return fmt.Errorf("dci search step tool is required")
	}
	status := strings.TrimSpace(step.Status)
	if status == "" {
		return fmt.Errorf("dci search step status is required")
	}
	if !isSearchStepStatus(status) {
		return fmt.Errorf("dci search step invalid status %q", step.Status)
	}
	if status == "error" && strings.TrimSpace(step.ErrorMessage) == "" {
		return fmt.Errorf("dci search step error status requires error_message")
	}
	if step.ResultCount < 0 {
		return fmt.Errorf("dci search step result_count must be >= 0")
	}
	if step.CreatedAt.IsZero() {
		return fmt.Errorf("dci search step created_at is required")
	}
	return nil
}

func ValidateEvidence(evidence Evidence) error {
	if err := evidence.EvidenceID.Validate(); err != nil {
		return fmt.Errorf("dci evidence evidence_id: %w", err)
	}
	if err := evidence.CreatedByEventID.Validate(); err != nil {
		return fmt.Errorf("dci evidence created_by_event_id: %w", err)
	}
	if strings.TrimSpace(evidence.FilePath) == "" {
		return fmt.Errorf("dci evidence file_path is required")
	}
	if strings.TrimSpace(evidence.Snippet) == "" {
		return fmt.Errorf("dci evidence snippet is required")
	}
	if evidence.LineStart <= 0 {
		return fmt.Errorf("dci evidence line_start must be > 0")
	}
	if evidence.LineEnd < evidence.LineStart {
		return fmt.Errorf("dci evidence line_end must be >= line_start")
	}
	if math.IsNaN(evidence.Confidence) || evidence.Confidence < 0 || evidence.Confidence > 1 {
		return fmt.Errorf("dci evidence confidence must be between 0 and 1")
	}
	return nil
}

func ValidateEvidencePack(pack EvidencePack) error {
	if err := pack.ActionID.Validate(); err != nil {
		return fmt.Errorf("dci evidence pack action_id: %w", err)
	}
	if strings.TrimSpace(pack.Query) == "" {
		return fmt.Errorf("dci evidence pack query is required")
	}
	if math.IsNaN(pack.Confidence) || pack.Confidence < 0 || pack.Confidence > 1 {
		return fmt.Errorf("dci evidence pack confidence must be between 0 and 1")
	}
	seenDerivedTerms := make(map[string]struct{}, len(pack.DerivedTerms))
	for index, term := range pack.DerivedTerms {
		if term == "" || strings.TrimSpace(term) != term {
			return fmt.Errorf("dci evidence pack derived_terms[%d] must be nonblank and have no surrounding whitespace", index)
		}
		if _, exists := seenDerivedTerms[term]; exists {
			return fmt.Errorf("dci evidence pack duplicate derived term %q", term)
		}
		seenDerivedTerms[term] = struct{}{}
	}
	seenEvidenceIDs := make(map[modulecore.EvidenceID]struct{}, len(pack.Evidence))
	seenCreatedByEventIDs := make(map[modulecore.EventID]struct{}, len(pack.Evidence))
	for index, evidence := range pack.Evidence {
		if err := ValidateEvidence(evidence); err != nil {
			return fmt.Errorf("dci evidence pack evidence[%d]: %w", index, err)
		}
		if _, exists := seenEvidenceIDs[evidence.EvidenceID]; exists {
			return fmt.Errorf("dci evidence pack duplicate evidence_id %q", evidence.EvidenceID)
		}
		seenEvidenceIDs[evidence.EvidenceID] = struct{}{}
		if _, exists := seenCreatedByEventIDs[evidence.CreatedByEventID]; exists {
			return fmt.Errorf("dci evidence pack duplicate created_by_event_id %q", evidence.CreatedByEventID)
		}
		seenCreatedByEventIDs[evidence.CreatedByEventID] = struct{}{}
	}
	return nil
}

func ValidateSearchResult(result SearchResult) error {
	return validateSearchResult(result, false)
}

// ValidateStoredSearchResult applies the same referential and value rules to
// a persisted result while allowing an explicitly migrated unattributed
// history row.
func ValidateStoredSearchResult(result SearchResult) error {
	return validateSearchResult(result, true)
}

func validateSearchResult(result SearchResult, allowLegacyUnattributed bool) error {
	validateTrace := ValidateSearchTrace
	if allowLegacyUnattributed {
		validateTrace = ValidateStoredSearchTrace
	}
	if err := validateTrace(result.Trace); err != nil {
		return fmt.Errorf("dci search result trace: %w", err)
	}
	if err := ValidateEvidencePack(result.Pack); err != nil {
		return fmt.Errorf("dci search result pack: %w", err)
	}
	if result.Trace.UserQuery != result.Pack.Query {
		return fmt.Errorf("dci search result trace and pack query must match")
	}
	if !sameStringSlice(result.Trace.CorpusScope, result.Pack.CorpusScope) {
		return fmt.Errorf("dci search result trace and pack corpus_scope must match")
	}
	if result.Trace.FinalEvidenceCount != len(result.Pack.Evidence) {
		return fmt.Errorf("dci search result final_evidence_count must equal evidence count")
	}
	if result.Trace.ActionID != result.Pack.ActionID {
		return fmt.Errorf("dci search result trace and pack action_id must match")
	}
	stepEventIDs := make(map[modulecore.EventID]struct{}, len(result.Trace.Steps))
	for _, step := range result.Trace.Steps {
		stepEventIDs[step.EventID] = struct{}{}
	}
	for _, evidence := range result.Pack.Evidence {
		if _, isFileReadEvent := stepEventIDs[evidence.CreatedByEventID]; isFileReadEvent {
			return fmt.Errorf("dci search result evidence created_by_event_id cannot reuse a file-read step event_id")
		}
	}
	return nil
}

func sameStringSlice(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func ValidateActor(kind, id string) error {
	if kind == "" {
		return fmt.Errorf("actor_kind is required")
	}
	if kind != "agent" && kind != "user" {
		return fmt.Errorf("actor_kind %q is not an authenticated user or agent in canonical form", kind)
	}
	if strings.TrimSpace(kind) != kind {
		return fmt.Errorf("actor_kind must be lower-case and have no surrounding whitespace")
	}
	if id == "" {
		return fmt.Errorf("actor_id is required")
	}
	if strings.TrimSpace(id) != id {
		return fmt.Errorf("actor_id must not have surrounding whitespace")
	}
	return nil
}

func isSearchTraceStatus(status string) bool {
	switch status {
	case "completed", "failed":
		return true
	default:
		return false
	}
}

func isTerminalSearchTraceStatus(status string) bool {
	switch status {
	case "completed", "failed":
		return true
	default:
		return false
	}
}

func isSearchStepStatus(status string) bool {
	switch status {
	case "ok", "error", "stopped", "completed":
		return true
	default:
		return false
	}
}
