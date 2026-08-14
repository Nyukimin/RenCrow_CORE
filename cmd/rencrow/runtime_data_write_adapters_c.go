package main

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	browsertraceapp "github.com/Nyukimin/RenCrow_CORE/internal/application/browsertrace"
	domainbrowser "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
	domainpersona "github.com/Nyukimin/RenCrow_CORE/internal/domain/persona"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

const (
	runtimePersonaObservationIDPrefix = "persona-observation/sha256:"
	runtimeBrowserValidationIDPrefix  = "browser-validation/sha256:"
)

type runtimePersonaObservationStore interface {
	SaveObservationLog(context.Context, domainpersona.ObservationLog) error
	FindObservationLogByID(context.Context, string) (domainpersona.ObservationLog, bool, error)
}

type runtimeBrowserTraceValidationStore interface {
	FindAPICandidateByID(context.Context, string) (domainbrowser.APICandidate, bool, error)
	SaveAPICandidateValidationResult(context.Context, domainbrowser.APICandidateValidationResult) error
	FindAPICandidateValidationResultByID(context.Context, string) (domainbrowser.APICandidateValidationResult, bool, error)
}

type runtimePersonaObservationWritePayload struct {
	ObservationType string   `json:"observation_type"`
	Summary         string   `json:"summary,omitempty"`
	EvidenceRefs    []string `json:"evidence_refs,omitempty"`
	Sensitivity     string   `json:"sensitivity"`
}

type runtimeBrowserTraceValidationWritePayload struct {
	CandidateID         string `json:"candidate_id"`
	ReviewNote          string `json:"review_note,omitempty"`
	TermsReviewed       *bool  `json:"terms_reviewed"`
	OfficialAPIReviewed *bool  `json:"official_api_reviewed"`
	PIIReviewed         *bool  `json:"pii_reviewed"`
	SchemaReviewed      *bool  `json:"schema_reviewed"`
	RiskReviewed        *bool  `json:"risk_reviewed"`
}

type runtimePersonaObservationWriter struct {
	mu    sync.Mutex
	store runtimePersonaObservationStore
}

type runtimeBrowserTraceValidationWriter struct {
	mu    sync.Mutex
	store runtimeBrowserTraceValidationStore
}

func registerRuntimeDataWritePersonaArchitecture(r *runtimeDataWriteRegistry, store runtimePersonaObservationStore) error {
	if r == nil || store == nil {
		return fmt.Errorf("persona architecture data write unavailable")
	}
	writer := &runtimePersonaObservationWriter{store: store}
	return r.RegisterWithContract("persona_architecture", "record_observation", dataRecallAccessUser, runtimeDataWriteContract{
		RequiredPayloadFields: []string{"observation_type", "sensitivity"},
		OptionalPayloadFields: []string{"summary", "evidence_refs"},
	}, writer.write)
}

func registerRuntimeDataWriteBrowserTraceToAPI(r *runtimeDataWriteRegistry, store runtimeBrowserTraceValidationStore) error {
	if r == nil || store == nil {
		return fmt.Errorf("browser trace data write unavailable")
	}
	writer := &runtimeBrowserTraceValidationWriter{store: store}
	return r.RegisterWithContract("browser_trace_to_api", "review_candidate", dataRecallAccessInternal, runtimeDataWriteContract{
		RequiredPayloadFields: []string{"candidate_id", "terms_reviewed", "official_api_reviewed", "pii_reviewed", "schema_reviewed", "risk_reviewed"},
		OptionalPayloadFields: []string{"review_note"},
	}, writer.write)
}

func (w *runtimePersonaObservationWriter) write(ctx context.Context, request tools.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
	scope, err := runtimeDataWriteOwnerScope(ctx)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if strings.TrimSpace(scope.AuthenticatedUserID) == "" {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("authenticated user is required for persona observation")
	}
	payload, err := decodeRuntimePersonaObservationPayload(request.Payload)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	now := time.Now().UTC()
	status := "rejected"
	if payload.Sensitivity == "normal" {
		status = "adopted"
	}
	observation := domainpersona.ObservationLog{
		EventID:         runtimeDataWriteDerivedID(runtimePersonaObservationIDPrefix, scope.RequestID),
		ObserverID:      strings.TrimSpace(scope.ActorID),
		TargetID:        strings.TrimSpace(scope.AuthenticatedUserID),
		ObservationType: payload.ObservationType,
		Summary:         payload.Summary,
		EvidenceRefs:    payload.EvidenceRefs,
		Sensitivity:     payload.Sensitivity,
		ReviewStatus:    status,
		CreatedAt:       now,
	}
	if err := domainpersona.ValidateObservationLog(observation); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	existing, found, err := w.store.FindObservationLogByID(ctx, observation.EventID)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if found {
		if !runtimeDataWriteObservationsEqual(existing, observation) {
			return runtimeDataWriteOwnerResult{}, fmt.Errorf("persona observation idempotency payload mismatch")
		}
		return runtimeDataWriteOwnerResult{
			SchemaVersion:    "persona-observation/v1",
			MigrationState:   "embedded_current",
			ValidationState:  "owner_validated",
			AuditRef:         existing.EventID,
			IdempotencyKey:   scope.RequestID,
			IdempotentReplay: true,
			PolicyRevision:   runtimeDataWritePolicyRevision,
		}, nil
	}
	if err := w.store.SaveObservationLog(ctx, observation); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	return runtimeDataWriteOwnerResult{
		SchemaVersion:    "persona-observation/v1",
		MigrationState:   "embedded_current",
		ValidationState:  "owner_validated",
		AuditRef:         observation.EventID,
		IdempotencyKey:   scope.RequestID,
		IdempotentReplay: false,
		PolicyRevision:   runtimeDataWritePolicyRevision,
	}, nil
}

func (w *runtimeBrowserTraceValidationWriter) write(ctx context.Context, request tools.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
	scope, err := runtimeDataWriteOwnerScope(ctx)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	payload, err := decodeRuntimeBrowserTraceValidationPayload(request.Payload)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	candidate, found, err := w.store.FindAPICandidateByID(ctx, payload.CandidateID)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if !found {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("browser trace api candidate %q is not found", payload.CandidateID)
	}
	if err := domainbrowser.ValidateAPICandidate(candidate); err != nil {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("browser trace api candidate %q is invalid: %w", payload.CandidateID, err)
	}
	now := time.Now().UTC()
	validation, err := browsertraceapp.BuildValidationReview(browsertraceapp.ValidationReviewInput{
		ValidationID:        runtimeDataWriteDerivedID(runtimeBrowserValidationIDPrefix, scope.RequestID),
		CandidateID:         candidate.CandidateID,
		TraceRunID:          candidate.TraceRunID,
		Reviewer:            strings.TrimSpace(scope.ActorID),
		ReviewNote:          payload.ReviewNote,
		TermsReviewed:       *payload.TermsReviewed,
		OfficialAPIReviewed: *payload.OfficialAPIReviewed,
		PIIReviewed:         *payload.PIIReviewed,
		SchemaReviewed:      *payload.SchemaReviewed,
		RiskReviewed:        *payload.RiskReviewed,
		CreatedAt:           now,
	})
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	existing, found, err := w.store.FindAPICandidateValidationResultByID(ctx, validation.ValidationID)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if found {
		if !runtimeDataWriteBrowserValidationsEqual(existing, validation) {
			return runtimeDataWriteOwnerResult{}, fmt.Errorf("browser trace validation idempotency payload mismatch")
		}
		return runtimeDataWriteOwnerResult{
			SchemaVersion:    "browser-validation/v1",
			MigrationState:   "embedded_current",
			ValidationState:  "owner_validated",
			AuditRef:         existing.ValidationID,
			IdempotencyKey:   scope.RequestID,
			IdempotentReplay: true,
			PolicyRevision:   runtimeDataWritePolicyRevision,
		}, nil
	}
	if err := w.store.SaveAPICandidateValidationResult(ctx, validation); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	return runtimeDataWriteOwnerResult{
		SchemaVersion:    "browser-validation/v1",
		MigrationState:   "embedded_current",
		ValidationState:  "owner_validated",
		AuditRef:         validation.ValidationID,
		IdempotencyKey:   scope.RequestID,
		IdempotentReplay: false,
		PolicyRevision:   runtimeDataWritePolicyRevision,
	}, nil
}

func decodeRuntimePersonaObservationPayload(payload map[string]any) (runtimePersonaObservationWritePayload, error) {
	if err := validateRuntimeDataWritePayloadKeys(payload, map[string]struct{}{
		"observation_type": {}, "summary": {}, "evidence_refs": {}, "sensitivity": {},
	}); err != nil {
		return runtimePersonaObservationWritePayload{}, err
	}
	var decoded runtimePersonaObservationWritePayload
	if err := decodeRuntimeDataWritePayload(payload, &decoded); err != nil {
		return runtimePersonaObservationWritePayload{}, err
	}
	decoded.ObservationType = strings.TrimSpace(decoded.ObservationType)
	decoded.Summary = strings.TrimSpace(decoded.Summary)
	decoded.Sensitivity = strings.TrimSpace(decoded.Sensitivity)
	if decoded.ObservationType == "" {
		return runtimePersonaObservationWritePayload{}, fmt.Errorf("observation_type is required")
	}
	if decoded.Sensitivity == "" {
		return runtimePersonaObservationWritePayload{}, fmt.Errorf("sensitivity is required")
	}
	if len(decoded.EvidenceRefs) > 0 {
		trimmed := make([]string, len(decoded.EvidenceRefs))
		for i, value := range decoded.EvidenceRefs {
			trimmed[i] = strings.TrimSpace(value)
			if trimmed[i] == "" {
				return runtimePersonaObservationWritePayload{}, fmt.Errorf("evidence_refs[%d] is required", i)
			}
		}
		decoded.EvidenceRefs = trimmed
	} else {
		decoded.EvidenceRefs = nil
	}
	return decoded, nil
}

func decodeRuntimeBrowserTraceValidationPayload(payload map[string]any) (runtimeBrowserTraceValidationWritePayload, error) {
	if err := validateRuntimeDataWritePayloadKeys(payload, map[string]struct{}{
		"candidate_id": {}, "review_note": {}, "terms_reviewed": {}, "official_api_reviewed": {},
		"pii_reviewed": {}, "schema_reviewed": {}, "risk_reviewed": {},
	}); err != nil {
		return runtimeBrowserTraceValidationWritePayload{}, err
	}
	var decoded runtimeBrowserTraceValidationWritePayload
	if err := decodeRuntimeDataWritePayload(payload, &decoded); err != nil {
		return runtimeBrowserTraceValidationWritePayload{}, err
	}
	decoded.CandidateID = strings.TrimSpace(decoded.CandidateID)
	decoded.ReviewNote = strings.TrimSpace(decoded.ReviewNote)
	if decoded.CandidateID == "" {
		return runtimeBrowserTraceValidationWritePayload{}, fmt.Errorf("candidate_id is required")
	}
	if decoded.TermsReviewed == nil || decoded.OfficialAPIReviewed == nil || decoded.PIIReviewed == nil || decoded.SchemaReviewed == nil || decoded.RiskReviewed == nil {
		return runtimeBrowserTraceValidationWritePayload{}, fmt.Errorf("validation review checks are required")
	}
	return decoded, nil
}

func runtimeDataWriteObservationsEqual(left, right domainpersona.ObservationLog) bool {
	left.CreatedAt = time.Time{}
	right.CreatedAt = time.Time{}
	return reflect.DeepEqual(left, right)
}

func runtimeDataWriteBrowserValidationsEqual(left, right domainbrowser.APICandidateValidationResult) bool {
	left.CreatedAt = time.Time{}
	right.CreatedAt = time.Time{}
	return reflect.DeepEqual(left, right)
}
