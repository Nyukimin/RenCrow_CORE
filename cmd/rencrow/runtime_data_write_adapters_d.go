package main

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	complexityapp "github.com/Nyukimin/RenCrow_CORE/internal/application/complexity"
	domaincomplexity "github.com/Nyukimin/RenCrow_CORE/internal/domain/complexity"
	domainsandbox "github.com/Nyukimin/RenCrow_CORE/internal/domain/sandbox"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

const (
	runtimeSandboxPromotionIDPrefix     = "sandbox-promotion/sha256:"
	runtimeSandboxPromotionGateIDPrefix = "sandbox-promotion-gate/sha256:"
	runtimeComplexityReviewIDPrefix     = "complexity-review/sha256:"
)

type runtimeSandboxPromotionGateStore interface {
	FindSandboxByID(context.Context, string) (domainsandbox.SandboxRecord, bool, error)
	FindSandboxArtifactByID(context.Context, string) (domainsandbox.SandboxArtifact, bool, error)
	FindPromotionRequestByID(context.Context, string) (domainsandbox.PromotionRequest, bool, error)
	SavePromotionRequest(context.Context, domainsandbox.PromotionRequest) error
	FindPromotionGateLogByID(context.Context, string) (domainsandbox.PromotionGateLog, bool, error)
	SavePromotionGateLog(context.Context, domainsandbox.PromotionGateLog) error
}

type runtimeComplexityHotspotReviewStore interface {
	FindHotspotByID(context.Context, string) (domaincomplexity.Hotspot, bool, error)
	FindReportArtifactByID(context.Context, string) (domaincomplexity.ReportArtifact, bool, error)
	SaveReportArtifact(context.Context, domaincomplexity.ReportArtifact) error
}

type runtimeSandboxPromotionGateWritePayload struct {
	SandboxID                       string `json:"sandbox_id"`
	TargetArtifactID                string `json:"target_artifact_id"`
	DiffArtifactID                  string `json:"diff_artifact_id"`
	TestResultArtifactID            string `json:"test_result_artifact_id"`
	RollbackPlanArtifactID          string `json:"rollback_plan_artifact_id"`
	Reason                          string `json:"reason"`
	PostApplyVerificationArtifactID string `json:"post_apply_verification_artifact_id,omitempty"`
	RiskLevel                       string `json:"risk_level,omitempty"`
}

type runtimeComplexityConcreteDiffReviewWritePayload struct {
	HotspotID    string `json:"hotspot_id"`
	ConcreteDiff string `json:"concrete_diff"`
}

type runtimeSandboxPromotionGateWriter struct {
	mu    sync.Mutex
	store runtimeSandboxPromotionGateStore
}

type runtimeComplexityHotspotReviewWriter struct {
	mu    sync.Mutex
	store runtimeComplexityHotspotReviewStore
}

func registerRuntimeDataWriteSandbox(r *runtimeDataWriteRegistry, store runtimeSandboxPromotionGateStore) error {
	if r == nil || store == nil {
		return fmt.Errorf("sandbox data write unavailable")
	}
	writer := &runtimeSandboxPromotionGateWriter{store: store}
	return r.RegisterWithContract("sandbox", "create_promotion_gate", dataRecallAccessInternal, runtimeDataWriteContract{
		RequiredPayloadFields: []string{
			"sandbox_id", "target_artifact_id", "diff_artifact_id", "test_result_artifact_id", "rollback_plan_artifact_id", "reason",
		},
		OptionalPayloadFields: []string{"post_apply_verification_artifact_id", "risk_level"},
	}, writer.write)
}

func registerRuntimeDataWriteComplexityHotspot(r *runtimeDataWriteRegistry, store runtimeComplexityHotspotReviewStore) error {
	if r == nil || store == nil {
		return fmt.Errorf("complexity hotspot data write unavailable")
	}
	writer := &runtimeComplexityHotspotReviewWriter{store: store}
	return r.RegisterWithContract("complexity_hotspot", "record_concrete_diff_review", dataRecallAccessInternal, runtimeDataWriteContract{
		RequiredPayloadFields: []string{"hotspot_id", "concrete_diff"},
	}, writer.write)
}

func (w *runtimeSandboxPromotionGateWriter) write(ctx context.Context, request tools.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
	scope, err := runtimeDataWriteOwnerScope(ctx)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	payload, err := decodeRuntimeSandboxPromotionGatePayload(request.Payload)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	sandbox, found, err := w.store.FindSandboxByID(ctx, payload.SandboxID)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if !found {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("sandbox %q is not found", payload.SandboxID)
	}
	if err := domainsandbox.ValidateSandboxRecord(sandbox); err != nil {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("sandbox %q is invalid: %w", payload.SandboxID, err)
	}
	if sandbox.SandboxID != payload.SandboxID {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("sandbox payload id %q does not match requested id %q", sandbox.SandboxID, payload.SandboxID)
	}
	if strings.TrimSpace(sandbox.Status) != domainsandbox.SandboxStatusActive {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("sandbox %q is not active", payload.SandboxID)
	}

	target, err := w.loadArtifact(ctx, sandbox, payload.TargetArtifactID, "target_file")
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	diff, err := w.loadArtifact(ctx, sandbox, payload.DiffArtifactID, "diff")
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	testResult, err := w.loadArtifact(ctx, sandbox, payload.TestResultArtifactID, "test_result")
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	rollbackPlan, err := w.loadArtifact(ctx, sandbox, payload.RollbackPlanArtifactID, "rollback_plan")
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	var postApplyVerification *domainsandbox.SandboxArtifact
	if payload.PostApplyVerificationArtifactID != "" {
		artifact, loadErr := w.loadArtifact(ctx, sandbox, payload.PostApplyVerificationArtifactID, "post_apply_verification")
		if loadErr != nil {
			return runtimeDataWriteOwnerResult{}, loadErr
		}
		postApplyVerification = &artifact
	}

	now := time.Now().UTC()
	promotion := domainsandbox.PromotionRequest{
		PromotionID:      runtimeDataWriteDerivedID(runtimeSandboxPromotionIDPrefix, scope.RequestID),
		SandboxID:        sandbox.SandboxID,
		WorkstreamID:     sandbox.WorkstreamID,
		GoalID:           sandbox.GoalID,
		RequestedBy:      strings.TrimSpace(scope.ActorID),
		TargetPath:       target.FilePath,
		DiffPath:         diff.FilePath,
		TestResultPath:   testResult.FilePath,
		RiskLevel:        payload.RiskLevel,
		Reason:           payload.Reason,
		RollbackPlanPath: rollbackPlan.FilePath,
		CreatedAt:        now,
	}
	if postApplyVerification != nil {
		promotion.PostApplyVerificationPath = postApplyVerification.FilePath
	}
	if err := domainsandbox.ValidatePromotionRequest(promotion); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	decision := domainsandbox.EvaluatePromotionRequest(promotion)
	if decision.Status != domainsandbox.GateStatusPassed {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("sandbox promotion gate is %s: %s", decision.Status, decision.Reason)
	}

	gate := domainsandbox.PromotionGateLog{
		EventID:     runtimeDataWriteDerivedID(runtimeSandboxPromotionGateIDPrefix, scope.RequestID),
		PromotionID: promotion.PromotionID,
		GateStatus:  domainsandbox.GateStatusPassed,
		Reason:      decision.Reason,
		CreatedAt:   now,
	}
	if postApplyVerification != nil {
		gate.PostApplyVerification = postApplyVerification.FilePath
	}
	if err := domainsandbox.ValidatePromotionGateLog(gate); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}

	existingPromotion, promotionFound, err := w.store.FindPromotionRequestByID(ctx, promotion.PromotionID)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	existingGate, gateFound, err := w.store.FindPromotionGateLogByID(ctx, gate.EventID)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if promotionFound && !runtimeDataWriteSandboxPromotionsEqual(existingPromotion, promotion) {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("sandbox promotion idempotency conflict: payload mismatch")
	}
	if gateFound && !runtimeDataWriteSandboxGatesEqual(existingGate, gate) {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("sandbox promotion gate idempotency conflict: payload mismatch")
	}
	if gateFound && !promotionFound {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("sandbox promotion gate idempotency conflict: request is missing")
	}
	if promotionFound && gateFound {
		return runtimeSandboxPromotionGateResult(existingGate.EventID, scope.RequestID, true), nil
	}
	if promotionFound {
		if err := w.store.SavePromotionGateLog(ctx, gate); err != nil {
			return runtimeDataWriteOwnerResult{}, err
		}
		// A prior attempt persisted the request but not its gate receipt. This
		// retry completes the Owner transaction for the first time, so it is not
		// an idempotent replay of a completed write.
		return runtimeSandboxPromotionGateResult(gate.EventID, scope.RequestID, false), nil
	}
	if err := w.store.SavePromotionRequest(ctx, promotion); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if err := w.store.SavePromotionGateLog(ctx, gate); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	return runtimeSandboxPromotionGateResult(gate.EventID, scope.RequestID, false), nil
}

func (w *runtimeSandboxPromotionGateWriter) loadArtifact(ctx context.Context, sandbox domainsandbox.SandboxRecord, artifactID, expectedType string) (domainsandbox.SandboxArtifact, error) {
	artifact, found, err := w.store.FindSandboxArtifactByID(ctx, artifactID)
	if err != nil {
		return domainsandbox.SandboxArtifact{}, err
	}
	if !found {
		return domainsandbox.SandboxArtifact{}, fmt.Errorf("sandbox artifact %q is not found", artifactID)
	}
	if err := domainsandbox.ValidateSandboxArtifact(artifact); err != nil {
		return domainsandbox.SandboxArtifact{}, fmt.Errorf("sandbox artifact %q is invalid: %w", artifactID, err)
	}
	if artifact.ArtifactID != artifactID {
		return domainsandbox.SandboxArtifact{}, fmt.Errorf("sandbox artifact payload id %q does not match requested id %q", artifact.ArtifactID, artifactID)
	}
	if artifact.SandboxID != sandbox.SandboxID {
		return domainsandbox.SandboxArtifact{}, fmt.Errorf("sandbox artifact %q belongs to sandbox %q, not %q", artifactID, artifact.SandboxID, sandbox.SandboxID)
	}
	if artifact.Type != expectedType {
		return domainsandbox.SandboxArtifact{}, fmt.Errorf("sandbox artifact %q has type %q, want %q", artifactID, artifact.Type, expectedType)
	}
	return artifact, nil
}

func (w *runtimeComplexityHotspotReviewWriter) write(ctx context.Context, request tools.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
	scope, err := runtimeDataWriteOwnerScope(ctx)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	payload, err := decodeRuntimeComplexityConcreteDiffReviewPayload(request.Payload)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	hotspot, found, err := w.store.FindHotspotByID(ctx, payload.HotspotID)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if !found {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("complexity hotspot %q is not found", payload.HotspotID)
	}
	if err := domaincomplexity.ValidateHotspot(hotspot); err != nil {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("complexity hotspot %q is invalid: %w", payload.HotspotID, err)
	}
	if hotspot.HotspotID != payload.HotspotID {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("complexity hotspot payload id %q does not match requested id %q", hotspot.HotspotID, payload.HotspotID)
	}
	if err := complexityapp.ValidateConcreteDiffForHotspot(hotspot, payload.ConcreteDiff); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}

	report := domaincomplexity.ReportArtifact{
		ArtifactID: runtimeDataWriteDerivedID(runtimeComplexityReviewIDPrefix, scope.RequestID),
		ScanID:     hotspot.ScanID,
		Type:       "complexity_concrete_diff_review",
		Title:      "Concrete diff review: " + hotspot.HotspotID,
		Status:     "generated",
		Content:    complexityapp.BuildConcreteDiffProposalMarkdown(hotspot, payload.ConcreteDiff, "", ""),
		CreatedAt:  time.Now().UTC(),
	}
	if err := domaincomplexity.ValidateReportArtifact(report); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}

	existing, found, err := w.store.FindReportArtifactByID(ctx, report.ArtifactID)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if found {
		if existing.ArtifactID != report.ArtifactID {
			return runtimeDataWriteOwnerResult{}, fmt.Errorf("complexity report artifact payload id %q does not match requested id %q", existing.ArtifactID, report.ArtifactID)
		}
		if !runtimeDataWriteComplexityReportsEqual(existing, report) {
			return runtimeDataWriteOwnerResult{}, fmt.Errorf("complexity concrete diff review idempotency conflict: payload mismatch")
		}
		return runtimeComplexityConcreteDiffReviewResult(existing.ArtifactID, scope.RequestID, true), nil
	}
	if err := w.store.SaveReportArtifact(ctx, report); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	return runtimeComplexityConcreteDiffReviewResult(report.ArtifactID, scope.RequestID, false), nil
}

func decodeRuntimeSandboxPromotionGatePayload(payload map[string]any) (runtimeSandboxPromotionGateWritePayload, error) {
	if err := validateRuntimeDataWritePayloadKeys(payload, map[string]struct{}{
		"sandbox_id": {}, "target_artifact_id": {}, "diff_artifact_id": {}, "test_result_artifact_id": {},
		"rollback_plan_artifact_id": {}, "reason": {}, "post_apply_verification_artifact_id": {}, "risk_level": {},
	}); err != nil {
		return runtimeSandboxPromotionGateWritePayload{}, err
	}
	var decoded runtimeSandboxPromotionGateWritePayload
	if err := decodeRuntimeDataWritePayload(payload, &decoded); err != nil {
		return runtimeSandboxPromotionGateWritePayload{}, err
	}
	decoded.SandboxID = strings.TrimSpace(decoded.SandboxID)
	decoded.TargetArtifactID = strings.TrimSpace(decoded.TargetArtifactID)
	decoded.DiffArtifactID = strings.TrimSpace(decoded.DiffArtifactID)
	decoded.TestResultArtifactID = strings.TrimSpace(decoded.TestResultArtifactID)
	decoded.RollbackPlanArtifactID = strings.TrimSpace(decoded.RollbackPlanArtifactID)
	decoded.Reason = strings.TrimSpace(decoded.Reason)
	decoded.PostApplyVerificationArtifactID = strings.TrimSpace(decoded.PostApplyVerificationArtifactID)
	decoded.RiskLevel = strings.TrimSpace(decoded.RiskLevel)
	for field, value := range map[string]string{
		"sandbox_id": decoded.SandboxID, "target_artifact_id": decoded.TargetArtifactID, "diff_artifact_id": decoded.DiffArtifactID,
		"test_result_artifact_id": decoded.TestResultArtifactID, "rollback_plan_artifact_id": decoded.RollbackPlanArtifactID, "reason": decoded.Reason,
	} {
		if value == "" {
			return runtimeSandboxPromotionGateWritePayload{}, fmt.Errorf("%s is required", field)
		}
	}
	return decoded, nil
}

func decodeRuntimeComplexityConcreteDiffReviewPayload(payload map[string]any) (runtimeComplexityConcreteDiffReviewWritePayload, error) {
	if err := validateRuntimeDataWritePayloadKeys(payload, map[string]struct{}{"hotspot_id": {}, "concrete_diff": {}}); err != nil {
		return runtimeComplexityConcreteDiffReviewWritePayload{}, err
	}
	var decoded runtimeComplexityConcreteDiffReviewWritePayload
	if err := decodeRuntimeDataWritePayload(payload, &decoded); err != nil {
		return runtimeComplexityConcreteDiffReviewWritePayload{}, err
	}
	decoded.HotspotID = strings.TrimSpace(decoded.HotspotID)
	decoded.ConcreteDiff = strings.TrimSpace(decoded.ConcreteDiff)
	if decoded.HotspotID == "" {
		return runtimeComplexityConcreteDiffReviewWritePayload{}, fmt.Errorf("hotspot_id is required")
	}
	if decoded.ConcreteDiff == "" {
		return runtimeComplexityConcreteDiffReviewWritePayload{}, fmt.Errorf("concrete_diff is required")
	}
	return decoded, nil
}

func runtimeSandboxPromotionGateResult(auditRef, requestID string, replay bool) runtimeDataWriteOwnerResult {
	return runtimeDataWriteOwnerResult{
		SchemaVersion:    "sandbox-promotion-gate/v1",
		MigrationState:   "embedded_current",
		ValidationState:  "owner_validated",
		AuditRef:         auditRef,
		IdempotencyKey:   requestID,
		IdempotentReplay: replay,
		PolicyRevision:   runtimeDataWritePolicyRevision,
	}
}

func runtimeComplexityConcreteDiffReviewResult(auditRef, requestID string, replay bool) runtimeDataWriteOwnerResult {
	return runtimeDataWriteOwnerResult{
		SchemaVersion:    "complexity-concrete-diff-review/v1",
		MigrationState:   "embedded_current",
		ValidationState:  "owner_validated",
		AuditRef:         auditRef,
		IdempotencyKey:   requestID,
		IdempotentReplay: replay,
		PolicyRevision:   runtimeDataWritePolicyRevision,
	}
}

func runtimeDataWriteSandboxPromotionsEqual(left, right domainsandbox.PromotionRequest) bool {
	left.CreatedAt = time.Time{}
	right.CreatedAt = time.Time{}
	return reflect.DeepEqual(left, right)
}

func runtimeDataWriteSandboxGatesEqual(left, right domainsandbox.PromotionGateLog) bool {
	left.CreatedAt = time.Time{}
	right.CreatedAt = time.Time{}
	return reflect.DeepEqual(left, right)
}

func runtimeDataWriteComplexityReportsEqual(left, right domaincomplexity.ReportArtifact) bool {
	left.CreatedAt = time.Time{}
	right.CreatedAt = time.Time{}
	return reflect.DeepEqual(left, right)
}
