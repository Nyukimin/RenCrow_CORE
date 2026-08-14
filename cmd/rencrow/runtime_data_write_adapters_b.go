package main

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	domainadvisor "github.com/Nyukimin/RenCrow_CORE/internal/domain/advisor"
	domainskill "github.com/Nyukimin/RenCrow_CORE/internal/domain/skillgovernance"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

const (
	runtimeAdvisorAdoptionIDPrefix  = "advisor-adoption/sha256:"
	runtimeContributionGateIDPrefix = "contribution-gate/sha256:"
)

type runtimeAdvisorAdoptionStore interface {
	FindAdviceRunByID(context.Context, string) (domainadvisor.AdviceRunRecord, bool, error)
	SaveAdvisorAdoption(context.Context, domainadvisor.AdvisorAdoptionRecord) error
	FindAdvisorAdoptionByID(context.Context, string) (domainadvisor.AdvisorAdoptionRecord, bool, error)
}

type runtimeSkillContributionGateStore interface {
	SaveContributionGateLog(context.Context, domainskill.ContributionGateLog) error
	FindContributionGateByID(context.Context, string) (domainskill.ContributionGateLog, bool, error)
}

type runtimeAdvisorAdoptionWritePayload struct {
	RunID         string `json:"run_id"`
	TaskID        string `json:"task_id,omitempty"`
	Adopted       *bool  `json:"adopted"`
	Outcome       string `json:"outcome"`
	RevisionCount int    `json:"revision_count,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

type runtimeSkillContributionGateWritePayload struct {
	Repo                string `json:"repo"`
	TargetBranch        string `json:"target_branch,omitempty"`
	ProblemStatement    string `json:"problem_statement,omitempty"`
	ExistingPRsChecked  *bool  `json:"existing_prs_checked"`
	RealProblemVerified *bool  `json:"real_problem_verified"`
	CoreChangeVerified  *bool  `json:"core_change_verified"`
	DiffReviewed        *bool  `json:"diff_reviewed"`
	TestResult          string `json:"test_result,omitempty"`
}

type runtimeAdvisorAdoptionWriter struct {
	mu    sync.Mutex
	store runtimeAdvisorAdoptionStore
}

type runtimeSkillContributionGateWriter struct {
	mu    sync.Mutex
	store runtimeSkillContributionGateStore
}

func registerRuntimeDataWriteAdvisor(r *runtimeDataWriteRegistry, store runtimeAdvisorAdoptionStore) error {
	if r == nil || store == nil {
		return fmt.Errorf("advisor data write unavailable")
	}
	writer := &runtimeAdvisorAdoptionWriter{store: store}
	return r.RegisterWithContract("advisor", "record_adoption", dataRecallAccessInternal, runtimeDataWriteContract{
		RequiredPayloadFields: []string{"run_id", "adopted", "outcome"},
		OptionalPayloadFields: []string{"task_id", "revision_count", "reason"},
	}, writer.write)
}

func registerRuntimeDataWriteSkillGovernance(r *runtimeDataWriteRegistry, store runtimeSkillContributionGateStore) error {
	if r == nil || store == nil {
		return fmt.Errorf("skill governance data write unavailable")
	}
	writer := &runtimeSkillContributionGateWriter{store: store}
	return r.RegisterWithContract("skill_governance", "record_contribution_gate", dataRecallAccessInternal, runtimeDataWriteContract{
		RequiredPayloadFields: []string{"repo", "existing_prs_checked", "real_problem_verified", "core_change_verified", "diff_reviewed"},
		OptionalPayloadFields: []string{"target_branch", "problem_statement", "test_result"},
	}, writer.write)
}

func (w *runtimeAdvisorAdoptionWriter) write(ctx context.Context, request tools.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
	scope, err := runtimeDataWriteOwnerScope(ctx)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	payload, err := decodeRuntimeAdvisorAdoptionPayload(request.Payload)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	run, found, err := w.store.FindAdviceRunByID(ctx, payload.RunID)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if !found {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("advisor advice run %q is not found", payload.RunID)
	}
	if err := run.Validate(); err != nil {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("advisor advice run %q is invalid: %w", payload.RunID, err)
	}
	now := time.Now().UTC()
	adoption := domainadvisor.AdvisorAdoptionRecord{
		AdoptionID:     runtimeDataWriteDerivedID(runtimeAdvisorAdoptionIDPrefix, scope.RequestID),
		RunID:          payload.RunID,
		TaskID:         payload.TaskID,
		AdvisorID:      run.AdvisorID,
		AdoptedByAgent: strings.TrimSpace(scope.ActorID),
		Adopted:        *payload.Adopted,
		Outcome:        payload.Outcome,
		RevisionCount:  payload.RevisionCount,
		Reason:         payload.Reason,
		CreatedAt:      now,
	}
	if err := adoption.Validate(); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	existing, found, err := w.store.FindAdvisorAdoptionByID(ctx, adoption.AdoptionID)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if found {
		if !runtimeDataWriteAdvisorAdoptionsEqual(existing, adoption) {
			return runtimeDataWriteOwnerResult{}, fmt.Errorf("advisor adoption idempotency payload mismatch")
		}
		return runtimeDataWriteOwnerResult{
			SchemaVersion: "advisor-adoption/v1", MigrationState: "embedded_current", ValidationState: "owner_validated",
			AuditRef: existing.AdoptionID, IdempotencyKey: scope.RequestID, IdempotentReplay: true, PolicyRevision: runtimeDataWritePolicyRevision,
		}, nil
	}
	if err := w.store.SaveAdvisorAdoption(ctx, adoption); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	return runtimeDataWriteOwnerResult{
		SchemaVersion: "advisor-adoption/v1", MigrationState: "embedded_current", ValidationState: "owner_validated",
		AuditRef: adoption.AdoptionID, IdempotencyKey: scope.RequestID, IdempotentReplay: false, PolicyRevision: runtimeDataWritePolicyRevision,
	}, nil
}

func (w *runtimeSkillContributionGateWriter) write(ctx context.Context, request tools.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
	scope, err := runtimeDataWriteOwnerScope(ctx)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	payload, err := decodeRuntimeSkillContributionGatePayload(request.Payload)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	now := time.Now().UTC()
	input := domainskill.ContributionGateLog{
		Repo:                payload.Repo,
		TargetBranch:        payload.TargetBranch,
		ProblemStatement:    payload.ProblemStatement,
		ExistingPRsChecked:  *payload.ExistingPRsChecked,
		RealProblemVerified: *payload.RealProblemVerified,
		CoreChangeVerified:  *payload.CoreChangeVerified,
		DiffReviewed:        *payload.DiffReviewed,
		TestResult:          payload.TestResult,
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	eventID := runtimeDataWriteDerivedID(runtimeContributionGateIDPrefix, scope.RequestID)
	gate, _, err := domainskill.NewContributionGateLog(eventID, input, now)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if err := domainskill.ValidateContributionGateLog(gate); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	existing, found, err := w.store.FindContributionGateByID(ctx, gate.EventID)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if found {
		if !runtimeDataWriteContributionGatesEqual(existing, gate) {
			return runtimeDataWriteOwnerResult{}, fmt.Errorf("skill contribution gate idempotency payload mismatch")
		}
		return runtimeDataWriteOwnerResult{
			SchemaVersion: "skill-contribution-gate/v1", MigrationState: "embedded_current", ValidationState: "owner_validated",
			AuditRef: existing.EventID, IdempotencyKey: scope.RequestID, IdempotentReplay: true, PolicyRevision: runtimeDataWritePolicyRevision,
		}, nil
	}
	if err := w.store.SaveContributionGateLog(ctx, gate); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	return runtimeDataWriteOwnerResult{
		SchemaVersion: "skill-contribution-gate/v1", MigrationState: "embedded_current", ValidationState: "owner_validated",
		AuditRef: gate.EventID, IdempotencyKey: scope.RequestID, IdempotentReplay: false, PolicyRevision: runtimeDataWritePolicyRevision,
	}, nil
}

func decodeRuntimeAdvisorAdoptionPayload(payload map[string]any) (runtimeAdvisorAdoptionWritePayload, error) {
	if err := validateRuntimeDataWritePayloadKeys(payload, map[string]struct{}{
		"run_id": {}, "task_id": {}, "adopted": {}, "outcome": {}, "revision_count": {}, "reason": {},
	}); err != nil {
		return runtimeAdvisorAdoptionWritePayload{}, err
	}
	var decoded runtimeAdvisorAdoptionWritePayload
	if err := decodeRuntimeDataWritePayload(payload, &decoded); err != nil {
		return runtimeAdvisorAdoptionWritePayload{}, err
	}
	if decoded.Adopted == nil {
		return runtimeAdvisorAdoptionWritePayload{}, fmt.Errorf("adopted is required")
	}
	decoded.RunID = strings.TrimSpace(decoded.RunID)
	decoded.TaskID = strings.TrimSpace(decoded.TaskID)
	decoded.Outcome = strings.TrimSpace(decoded.Outcome)
	decoded.Reason = strings.TrimSpace(decoded.Reason)
	if decoded.RunID == "" {
		return runtimeAdvisorAdoptionWritePayload{}, fmt.Errorf("run_id is required")
	}
	if decoded.RevisionCount < 0 {
		return runtimeAdvisorAdoptionWritePayload{}, fmt.Errorf("revision_count must be >= 0")
	}
	return decoded, nil
}

func decodeRuntimeSkillContributionGatePayload(payload map[string]any) (runtimeSkillContributionGateWritePayload, error) {
	if err := validateRuntimeDataWritePayloadKeys(payload, map[string]struct{}{
		"repo": {}, "target_branch": {}, "problem_statement": {}, "existing_prs_checked": {},
		"real_problem_verified": {}, "core_change_verified": {}, "diff_reviewed": {}, "test_result": {},
	}); err != nil {
		return runtimeSkillContributionGateWritePayload{}, err
	}
	var decoded runtimeSkillContributionGateWritePayload
	if err := decodeRuntimeDataWritePayload(payload, &decoded); err != nil {
		return runtimeSkillContributionGateWritePayload{}, err
	}
	if decoded.ExistingPRsChecked == nil || decoded.RealProblemVerified == nil || decoded.CoreChangeVerified == nil || decoded.DiffReviewed == nil {
		return runtimeSkillContributionGateWritePayload{}, fmt.Errorf("contribution gate checks are required")
	}
	decoded.Repo = strings.TrimSpace(decoded.Repo)
	decoded.TargetBranch = strings.TrimSpace(decoded.TargetBranch)
	decoded.ProblemStatement = strings.TrimSpace(decoded.ProblemStatement)
	decoded.TestResult = strings.TrimSpace(decoded.TestResult)
	if decoded.Repo == "" {
		return runtimeSkillContributionGateWritePayload{}, fmt.Errorf("repo is required")
	}
	return decoded, nil
}

func runtimeDataWriteAdvisorAdoptionsEqual(left, right domainadvisor.AdvisorAdoptionRecord) bool {
	left.CreatedAt = time.Time{}
	right.CreatedAt = time.Time{}
	return reflect.DeepEqual(left, right)
}

func runtimeDataWriteContributionGatesEqual(left, right domainskill.ContributionGateLog) bool {
	left.CreatedAt = time.Time{}
	right.CreatedAt = time.Time{}
	return reflect.DeepEqual(left, right)
}
