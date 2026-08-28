package backlog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	methodology "github.com/Nyukimin/RenCrow_CORE/internal/domain/developmentmethodology"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
)

const developmentArtifactPrefix = "development."

const developmentArtifactSegmentMaxRunes = 128

const developmentArtifactProjectionLimit = 5000

const (
	DevelopmentArtifactSpecification           = "specification"
	DevelopmentArtifactPlan                    = "plan"
	DevelopmentArtifactImplementationAuthority = "implementation_authority_token"
	DevelopmentArtifactRuling                  = "ruling"
	DevelopmentArtifactEvidence                = "evidence"
	DevelopmentArtifactReview                  = "review"
	DevelopmentArtifactLedger                  = "ledger"
)

type developmentArtifactStore interface {
	SaveArtifact(context.Context, domainworkstream.Artifact) error
	ListArtifacts(context.Context, int) ([]domainworkstream.Artifact, error)
}

type SaveDevelopmentArtifactRequest struct {
	ArtifactType string          `json:"artifact_type"`
	Payload      json.RawMessage `json:"payload"`
	TraceID      string          `json:"trace_id,omitempty"`
}

type IssueDevelopmentImplementationAuthorityRequest struct {
	ImplementationAuthorityTokenID string    `json:"implementation_authority_token_id,omitempty"`
	Issuer                         string    `json:"issuer"`
	Scope                          []string  `json:"scope"`
	Reason                         string    `json:"reason"`
	ExpiresAt                      time.Time `json:"expires_at"`
}

type DevelopmentEvent struct {
	Type       string         `json:"type"`
	UnitID     string         `json:"unit_id"`
	ArtifactID string         `json:"artifact_id"`
	TraceID    string         `json:"trace_id,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	Fields     map[string]any `json:"fields,omitempty"`
}

type DevelopmentEventSink interface {
	AppendDevelopmentEvent(context.Context, DevelopmentEvent) error
}

func (s *Service) emitDevelopmentTransition(ctx context.Context, eventType string, item domainbacklog.Item, target, requestID, reason string) error {
	if s.developmentEvents == nil || strings.TrimSpace(item.ImplementationUnit) == "" {
		return nil
	}
	safeRequestID := methodology.RedactSecrets(strings.TrimSpace(requestID))
	artifactID := strings.Join([]string{"transition", boundedDevelopmentArtifactSegment(item.ImplementationUnit), boundedDevelopmentArtifactSegment(target), boundedDevelopmentArtifactSegment(safeRequestID)}, ":")
	fields := map[string]any{"target_stage": target, "item_id": item.ItemID, "implementation_revision": item.ImplementationRevision}
	if strings.TrimSpace(reason) != "" {
		fields["reason"] = methodology.RedactSecrets(reason)
	}
	return s.developmentEvents.AppendDevelopmentEvent(ctx, DevelopmentEvent{Type: eventType, UnitID: item.ImplementationUnit, ArtifactID: artifactID, TraceID: safeRequestID, CreatedAt: s.now(), Fields: fields})
}

type DevelopmentProjection struct {
	UnitID                       string                                    `json:"unit_id"`
	WorkstreamID                 string                                    `json:"workstream_id"`
	Specification                *methodology.Specification                `json:"specification,omitempty"`
	Plan                         *methodology.Plan                         `json:"plan,omitempty"`
	ImplementationAuthorityToken *methodology.ImplementationAuthorityToken `json:"implementation_authority_token,omitempty"`
	Ledger                       *methodology.Ledger                       `json:"ledger,omitempty"`
	Tasks                        []methodology.Task                        `json:"tasks"`
	Rulings                      []methodology.Ruling                      `json:"rulings"`
	Evidence                     []methodology.EvidenceReceipt             `json:"evidence"`
	Reviews                      []methodology.ReviewRecord                `json:"reviews"`
	Artifacts                    []domainworkstream.Artifact               `json:"artifacts"`
}

func (s *Service) Development(ctx context.Context, unitID string) (DevelopmentProjection, error) {
	item, err := s.findByUnit(ctx, unitID)
	if err != nil {
		return DevelopmentProjection{}, err
	}
	store, ok := s.workstream.(developmentArtifactStore)
	if !ok {
		return DevelopmentProjection{}, errors.New("development artifact store unavailable")
	}
	artifacts, err := listCompleteDevelopmentArtifacts(ctx, store)
	if err != nil {
		return DevelopmentProjection{}, err
	}
	projection := DevelopmentProjection{UnitID: item.ImplementationUnit, WorkstreamID: item.WorkstreamID, Tasks: []methodology.Task{}, Rulings: []methodology.Ruling{}, Evidence: []methodology.EvidenceReceipt{}, Reviews: []methodology.ReviewRecord{}, Artifacts: []domainworkstream.Artifact{}}
	for _, artifact := range artifacts {
		if artifact.WorkstreamID != item.WorkstreamID || !strings.HasPrefix(artifact.Type, developmentArtifactPrefix) {
			continue
		}
		projection.Artifacts = append(projection.Artifacts, artifact)
	}
	sort.Slice(projection.Artifacts, func(i, j int) bool {
		left, right := projection.Artifacts[i], projection.Artifacts[j]
		if left.CreatedAt.Equal(right.CreatedAt) {
			return left.ArtifactID < right.ArtifactID
		}
		return left.CreatedAt.Before(right.CreatedAt)
	})
	for _, artifact := range projection.Artifacts {
		kind := strings.TrimPrefix(artifact.Type, developmentArtifactPrefix)
		normalized, _, normalizeErr := validateAndNormalizeDevelopmentArtifact(kind, artifact.Payload, item.ImplementationUnit, artifact.CreatedAt)
		if normalizeErr != nil {
			return DevelopmentProjection{}, fmt.Errorf("development artifact %s: %w", artifact.ArtifactID, normalizeErr)
		}
		if err := applyDevelopmentArtifact(&projection, kind, normalized); err != nil {
			return DevelopmentProjection{}, fmt.Errorf("development artifact %s: %w", artifact.ArtifactID, err)
		}
	}
	if projection.Ledger != nil {
		projection.Tasks = append([]methodology.Task(nil), projection.Ledger.Tasks...)
		projection.Rulings = mergeDevelopmentRulings(projection.Rulings, projection.Ledger.Rulings)
		projection.Evidence = mergeDevelopmentEvidence(projection.Evidence, projection.Ledger.EvidenceRefs)
		projection.Reviews = mergeDevelopmentReviews(projection.Reviews, projection.Ledger.ReviewRecords)
	}
	if err := validateDevelopmentProjectionBindings(item, projection); err != nil {
		return DevelopmentProjection{}, err
	}
	return projection, nil
}

func mergeDevelopmentRulings(groups ...[]methodology.Ruling) []methodology.Ruling {
	out := []methodology.Ruling{}
	seen := map[string]bool{}
	for _, group := range groups {
		for _, item := range group {
			if !seen[item.RulingID] {
				seen[item.RulingID] = true
				out = append(out, item)
			}
		}
	}
	return out
}

func mergeDevelopmentEvidence(groups ...[]methodology.EvidenceReceipt) []methodology.EvidenceReceipt {
	out := []methodology.EvidenceReceipt{}
	seen := map[string]bool{}
	for _, group := range groups {
		for _, item := range group {
			if !seen[item.EvidenceID] {
				seen[item.EvidenceID] = true
				out = append(out, item)
			}
		}
	}
	return out
}

func mergeDevelopmentReviews(groups ...[]methodology.ReviewRecord) []methodology.ReviewRecord {
	out := []methodology.ReviewRecord{}
	seen := map[string]bool{}
	for _, group := range groups {
		for _, item := range group {
			if !seen[item.ReviewID] {
				seen[item.ReviewID] = true
				out = append(out, item)
			}
		}
	}
	return out
}

func (s *Service) SaveDevelopmentArtifact(ctx context.Context, unitID string, request SaveDevelopmentArtifactRequest) (DevelopmentProjection, bool, error) {
	s.developmentMu.Lock()
	defer s.developmentMu.Unlock()
	return s.saveDevelopmentArtifactLocked(ctx, unitID, request, false)
}

func (s *Service) saveDevelopmentArtifactLocked(ctx context.Context, unitID string, request SaveDevelopmentArtifactRequest, allowAuthority bool) (DevelopmentProjection, bool, error) {
	item, err := s.findByUnit(ctx, unitID)
	if err != nil {
		return DevelopmentProjection{}, false, err
	}
	store, ok := s.workstream.(developmentArtifactStore)
	if !ok {
		return DevelopmentProjection{}, false, errors.New("development artifact store unavailable")
	}
	kind := strings.ToLower(strings.TrimSpace(request.ArtifactType))
	if kind == DevelopmentArtifactImplementationAuthority && !allowAuthority {
		return DevelopmentProjection{}, false, errors.New("implementation authority is issued only by the adopted-unit owner route")
	}
	if containsDevelopmentSecret(request.TraceID) {
		return DevelopmentProjection{}, false, errors.New("trace_id contains secret material")
	}
	payload, id, err := validateAndNormalizeDevelopmentArtifact(kind, request.Payload, item.ImplementationUnit, s.now())
	if err != nil {
		return DevelopmentProjection{}, false, err
	}
	artifactID := developmentArtifactID(item.ImplementationUnit, kind, id)
	artifacts, err := listCompleteDevelopmentArtifacts(ctx, store)
	if err != nil {
		return DevelopmentProjection{}, false, err
	}
	for _, existing := range artifacts {
		if existing.ArtifactID != artifactID {
			continue
		}
		if bytes.Equal(bytes.TrimSpace(existing.Payload), bytes.TrimSpace(payload)) {
			if s.developmentEvents != nil {
				for _, event := range developmentEventsForArtifact(kind, artifactID, item.ImplementationUnit, request.TraceID, payload, s.now()) {
					if err := s.developmentEvents.AppendDevelopmentEvent(ctx, event); err != nil {
						return DevelopmentProjection{}, false, fmt.Errorf("append development replay event: %w", err)
					}
				}
			}
			projection, loadErr := s.Development(ctx, unitID)
			return projection, false, loadErr
		}
		return DevelopmentProjection{}, false, methodology.ErrIdempotencyConflict
	}
	current, loadErr := s.Development(ctx, unitID)
	if loadErr != nil {
		return DevelopmentProjection{}, false, loadErr
	}
	if err := validateDevelopmentArtifactAgainstCurrent(kind, payload, current); err != nil {
		return DevelopmentProjection{}, false, err
	}
	if kind == DevelopmentArtifactLedger {
		var next methodology.Ledger
		if err := json.Unmarshal(payload, &next); err != nil {
			return DevelopmentProjection{}, false, err
		}
		if current.Ledger == nil {
			if err := validateInitialDevelopmentLedger(next); err != nil {
				return DevelopmentProjection{}, false, err
			}
		} else if err := methodology.ValidateLedgerProgress(*current.Ledger, next); err != nil {
			return DevelopmentProjection{}, false, err
		}
	}
	now := s.now()
	artifact := domainworkstream.Artifact{ArtifactID: artifactID, TraceID: strings.TrimSpace(request.TraceID), WorkstreamID: item.WorkstreamID, Type: developmentArtifactPrefix + kind, Title: kind + " for " + item.ImplementationUnit, Status: "verified", Payload: payload, CreatedAt: now, UpdatedAt: now}
	if err := store.SaveArtifact(ctx, artifact); err != nil {
		return DevelopmentProjection{}, false, err
	}
	if s.developmentEvents != nil {
		for _, event := range developmentEventsForArtifact(kind, artifactID, item.ImplementationUnit, request.TraceID, payload, now) {
			if err := s.developmentEvents.AppendDevelopmentEvent(ctx, event); err != nil {
				return DevelopmentProjection{}, false, fmt.Errorf("append development event: %w", err)
			}
		}
	}
	projection, err := s.Development(ctx, unitID)
	return projection, true, err
}

func listCompleteDevelopmentArtifacts(ctx context.Context, store developmentArtifactStore) ([]domainworkstream.Artifact, error) {
	artifacts, err := store.ListArtifacts(ctx, developmentArtifactProjectionLimit+1)
	if err != nil {
		return nil, err
	}
	if len(artifacts) > developmentArtifactProjectionLimit {
		return nil, fmt.Errorf("development artifact projection exceeds bounded complete-read limit %d", developmentArtifactProjectionLimit)
	}
	return artifacts, nil
}

func (s *Service) IssueDevelopmentImplementationAuthority(ctx context.Context, unitID string, request IssueDevelopmentImplementationAuthorityRequest) (DevelopmentProjection, bool, error) {
	s.developmentMu.Lock()
	defer s.developmentMu.Unlock()
	item, err := s.findByUnit(ctx, unitID)
	if err != nil {
		return DevelopmentProjection{}, false, err
	}
	if item.ConceptState != "ADOPTED" || strings.TrimSpace(item.AdoptedAt) == "" {
		return DevelopmentProjection{}, false, errors.New("implementation_authority requires an adopted implementation unit")
	}
	projection, err := s.Development(ctx, unitID)
	if err != nil {
		return DevelopmentProjection{}, false, err
	}
	if projection.Specification == nil || projection.Specification.Status != methodology.SpecificationApproved {
		return DevelopmentProjection{}, false, errors.New("approved specification artifact is required")
	}
	tokenID := strings.TrimSpace(request.ImplementationAuthorityTokenID)
	if tokenID == "" {
		tokenID = "implementation_authority:" + unitID + ":" + projection.Specification.ContentHash[:12]
	}
	adoptedAt, err := time.Parse(time.RFC3339, item.AdoptedAt)
	if err != nil {
		return DevelopmentProjection{}, false, errors.New("adoption timestamp is invalid")
	}
	adoption := methodology.AdoptionEvidence{EvidenceID: "atlas-adoption:" + item.ItemID, UnitID: unitID, SpecRef: projection.Specification.SpecID, SpecHash: projection.Specification.ContentHash, Decision: "ADOPTED", Verified: true, CreatedAt: adoptedAt}
	token, err := methodology.IssueImplementationAuthorityToken(methodology.ImplementationAuthorityRequest{ImplementationAuthorityTokenID: tokenID, UnitID: unitID, SpecRef: projection.Specification.SpecID, SpecHash: projection.Specification.ContentHash, Issuer: strings.TrimSpace(request.Issuer), Scope: append([]string(nil), request.Scope...), Reason: strings.TrimSpace(request.Reason), IssuedAt: s.now(), ExpiresAt: request.ExpiresAt}, adoption, methodology.ClockFunc(s.clock))
	if err != nil {
		return DevelopmentProjection{}, false, err
	}
	payload, err := json.Marshal(token)
	if err != nil {
		return DevelopmentProjection{}, false, err
	}
	return s.saveDevelopmentArtifactLocked(ctx, unitID, SaveDevelopmentArtifactRequest{ArtifactType: DevelopmentArtifactImplementationAuthority, Payload: payload}, true)
}

func (s *Service) validateDevelopmentStageGate(ctx context.Context, item domainbacklog.Item, target string) error {
	projection, err := s.Development(ctx, item.ImplementationUnit)
	if err != nil {
		if strings.Contains(err.Error(), "development artifact store unavailable") {
			return ErrLifecycleStoreUnavailable
		}
		return err
	}
	// Legacy units with no methodology artifacts remain on the existing v2
	// lifecycle. Once a methodology artifact exists, every later stage fails
	// closed through this single owner gate.
	if len(projection.Artifacts) == 0 {
		return nil
	}
	if projection.Specification == nil || projection.Plan == nil || projection.ImplementationAuthorityToken == nil || projection.Ledger == nil {
		return errors.New("development methodology spec, plan, implementation_authority, and ledger are required")
	}
	if err := methodology.ValidatePlanAgainstSpecification(*projection.Plan, *projection.Specification); err != nil {
		return err
	}
	if err := methodology.ValidateImplementationAuthorityTokenAt(*projection.ImplementationAuthorityToken, s.now()); err != nil {
		return err
	}
	if !developmentAuthorityAllows(*projection.ImplementationAuthorityToken, target) {
		return errors.New("implementation authority scope does not permit target stage")
	}
	ledger := *projection.Ledger
	switch target {
	case domainbacklog.DeliveryTDDRed:
		if len(ledger.Worktrees) == 0 {
			return errors.New("verified isolated worktree is required")
		}
		for _, worktree := range ledger.Worktrees {
			if err := methodology.ValidateWorktreeGate(worktree); err != nil {
				return err
			}
		}
		if len(ledger.BaselineEvidence) == 0 {
			return errors.New("verified baseline is required")
		}
		for _, baseline := range ledger.BaselineEvidence {
			if err := methodology.ValidateBaselineGate(baseline); err != nil {
				return err
			}
		}
		red, ok := findMethodologyEvidence(ledger.EvidenceRefs, domainbacklog.DeliveryTDDRed, "tdd_red", "execution_report")
		if !ok {
			return errors.New("verified RED evidence is required")
		}
		if err := methodology.ValidateTDDRedEvidence(red); err != nil {
			return err
		}
	case domainbacklog.DeliveryTDDGreen:
		red, redOK := findMethodologyEvidence(ledger.EvidenceRefs, domainbacklog.DeliveryTDDRed, "tdd_red", "execution_report")
		green, greenOK := findMethodologyEvidence(ledger.EvidenceRefs, domainbacklog.DeliveryTDDGreen, "tdd_green")
		if !redOK || !greenOK {
			return errors.New("verified RED and GREEN evidence are required")
		}
		if err := methodology.ValidateTDDGreenEvidence(green, red); err != nil {
			return err
		}
	case domainbacklog.DeliveryRefactor:
		green, greenOK := findMethodologyEvidence(ledger.EvidenceRefs, domainbacklog.DeliveryTDDGreen, "tdd_green")
		refactor, refactorOK := findMethodologyEvidence(ledger.EvidenceRefs, domainbacklog.DeliveryRefactor, "refactor")
		if !greenOK || !refactorOK {
			return errors.New("verified GREEN and REFACTOR evidence are required")
		}
		if err := methodology.ValidateRefactorEvidence(refactor, green); err != nil {
			return err
		}
	case domainbacklog.DeliveryE2EPredeploy:
		if !hasMethodologyReview(ledger.ReviewRecords, methodology.ReviewTypeTask) || !hasMethodologyReview(ledger.ReviewRecords, methodology.ReviewTypeBranch) {
			return errors.New("independent task and branch review are required")
		}
	case domainbacklog.DeliveryBuild:
		if _, ok := findMethodologyEvidence(ledger.EvidenceRefs, domainbacklog.DeliveryBuild, "build", "artifact"); !ok {
			return errors.New("verified build artifact receipt is required")
		}
	case domainbacklog.DeliveryDeploy:
		if _, ok := findMethodologyEvidence(ledger.EvidenceRefs, "ECOSYSTEM_VERIFIED", "ecosystem_verified", "ecosystem"); !ok {
			return errors.New("verified ecosystem receipt is required")
		}
		if _, ok := findMethodologyEvidence(ledger.EvidenceRefs, domainbacklog.DeliveryDeploy, "deploy", "deploy_receipt"); !ok {
			return errors.New("verified deployment receipt is required")
		}
	case domainbacklog.DeliveryRestart:
		if _, ok := findMethodologyEvidence(ledger.EvidenceRefs, domainbacklog.DeliveryRestart, "restart", "restart_receipt"); !ok {
			return errors.New("verified restart receipt is required")
		}
		if _, ok := findMethodologyEvidence(ledger.EvidenceRefs, "PROCESS_IDENTITY_VERIFIED", "process_identity", "process_identity_verified", "runtime_identity"); !ok {
			return errors.New("verified process identity receipt is required")
		}
	case domainbacklog.DeliveryPostDeployVerify:
		if _, ok := findMethodologyEvidence(ledger.EvidenceRefs, domainbacklog.DeliveryPostDeployVerify, "readiness", "health", "post_deploy_verify"); !ok {
			return errors.New("verified readiness receipt is required")
		}
	case domainbacklog.DeliveryLiveVerified:
		if err := ledger.ValidateLiveGate(); err != nil {
			return err
		}
	}
	return nil
}

func findMethodologyEvidence(receipts []methodology.EvidenceReceipt, stage string, kinds ...string) (methodology.EvidenceReceipt, bool) {
	for _, receipt := range receipts {
		if !strings.EqualFold(receipt.Stage, stage) {
			continue
		}
		for _, kind := range kinds {
			if strings.EqualFold(receipt.EvidenceType, kind) {
				return receipt, true
			}
		}
	}
	return methodology.EvidenceReceipt{}, false
}

func hasMethodologyReview(reviews []methodology.ReviewRecord, kind methodology.ReviewType) bool {
	for _, review := range reviews {
		if review.ReviewType == kind && review.Verdict == methodology.ReviewAccepted {
			return true
		}
	}
	return false
}

func developmentEventsForArtifact(kind, artifactID, unitID, traceID string, payload []byte, now time.Time) []DevelopmentEvent {
	eventType := map[string]string{
		DevelopmentArtifactSpecification:           "specification_recorded",
		DevelopmentArtifactPlan:                    "development_plan_recorded",
		DevelopmentArtifactImplementationAuthority: "implementation_authority_recorded",
		DevelopmentArtifactRuling:                  "ruling_recorded",
		DevelopmentArtifactEvidence:                "evidence_recorded",
		DevelopmentArtifactReview:                  "review_completed",
		DevelopmentArtifactLedger:                  "state_checkpoint_recorded",
	}[kind]
	fields := map[string]any{"artifact_type": kind}
	if kind == DevelopmentArtifactEvidence {
		var receipt methodology.EvidenceReceipt
		if json.Unmarshal(payload, &receipt) == nil {
			fields["stage"] = receipt.Stage
			fields["evidence_type"] = receipt.EvidenceType
			switch strings.ToUpper(strings.TrimSpace(receipt.Stage)) {
			case "TDD_RED":
				eventType = "tdd_red_verified"
			case "TDD_GREEN":
				eventType = "tdd_green_verified"
			case "BUILD":
				eventType = "build_completed"
			case "ECOSYSTEM_VERIFIED":
				eventType = "ecosystem_verified"
			case "DEPLOY":
				eventType = "deploy_completed"
			case "RESTART":
				eventType = "restart_completed"
			case "POST_DEPLOY_VERIFY", "READINESS":
				eventType = "readiness_verified"
			case "PRODUCTION_VERIFIED":
				eventType = "production_verified"
			case "VIEWER_VERIFIED":
				eventType = "viewer_verified"
			case "LIVE_VERIFIED":
				eventType = "live_verified"
			}
		}
	}
	events := []DevelopmentEvent{{Type: eventType, UnitID: unitID, ArtifactID: artifactID, TraceID: strings.TrimSpace(traceID), CreatedAt: now, Fields: fields}}
	appendEvent := func(eventType string, eventFields map[string]any) {
		events = append(events, DevelopmentEvent{Type: eventType, UnitID: unitID, ArtifactID: artifactID, TraceID: strings.TrimSpace(traceID), CreatedAt: now, Fields: eventFields})
	}
	if kind == DevelopmentArtifactPlan {
		var plan methodology.Plan
		if json.Unmarshal(payload, &plan) == nil {
			for _, task := range plan.Tasks {
				if strings.TrimSpace(task.AssignedSkill) != "" {
					appendEvent("skill_selected", map[string]any{"artifact_type": kind, "task_id": task.TaskID, "skill_id": task.AssignedSkill})
				}
			}
		}
	}
	if kind == DevelopmentArtifactLedger {
		var ledger methodology.Ledger
		if json.Unmarshal(payload, &ledger) == nil {
			if len(ledger.Worktrees) > 0 {
				appendEvent("worktree_created", map[string]any{"artifact_type": kind, "count": len(ledger.Worktrees)})
			}
			if len(ledger.BaselineEvidence) > 0 {
				appendEvent("baseline_verified", map[string]any{"artifact_type": kind, "count": len(ledger.BaselineEvidence)})
			}
			for _, assignment := range ledger.Assignments {
				appendEvent("skill_assigned", map[string]any{"artifact_type": kind, "task_id": assignment.TaskID, "skill_id": assignment.Skill, "role": assignment.Role})
			}
			if stateEvent := developmentStateEvent(ledger.CurrentState); stateEvent != "" {
				appendEvent(stateEvent, map[string]any{"artifact_type": kind, "state": ledger.CurrentState})
			}
		}
	}
	return events
}

func developmentStateEvent(state string) string {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case string(methodology.TaskRedVerified):
		return "tdd_red_verified"
	case string(methodology.TaskGreenVerified):
		return "tdd_green_verified"
	case string(methodology.TaskRefactored):
		return "refactor_completed"
	case string(methodology.TaskReviewed):
		return "review_completed"
	case string(methodology.TaskDone):
		return "live_verified"
	case string(methodology.TaskBlocked), string(methodology.TaskFailed), string(methodology.TaskCancelled):
		return "terminal_outcome_recorded"
	default:
		return ""
	}
}

func validateAndNormalizeDevelopmentArtifact(kind string, raw json.RawMessage, unitID string, now time.Time) (json.RawMessage, string, error) {
	if len(bytes.TrimSpace(raw)) == 0 || len(raw) > 512<<10 {
		return nil, "", errors.New("bounded development artifact payload is required")
	}
	marshal := func(value any, id string) (json.RawMessage, string, error) {
		payload, err := json.Marshal(value)
		return payload, strings.TrimSpace(id), err
	}
	switch kind {
	case DevelopmentArtifactSpecification:
		var value methodology.Specification
		if err := strictDevelopmentJSON(raw, &value); err != nil {
			return nil, "", err
		}
		if err := methodology.ValidateSpecification(value); err != nil {
			return nil, "", err
		}
		payload, id, err := marshal(value, versionedDevelopmentArtifactIdentity(value.SpecID, fmt.Sprintf("%d", value.Revision)))
		if err != nil {
			return nil, "", err
		}
		var persisted methodology.Specification
		if err := json.Unmarshal(payload, &persisted); err != nil {
			return nil, "", err
		}
		if err := methodology.ValidateSpecification(persisted); err != nil {
			return nil, "", fmt.Errorf("redacted specification is not hash-consistent: %w", err)
		}
		return payload, id, nil
	case DevelopmentArtifactPlan:
		var value methodology.Plan
		if err := strictDevelopmentJSON(raw, &value); err != nil {
			return nil, "", err
		}
		if value.ImplementationUnitID != unitID {
			return nil, "", errors.New("plan belongs to another implementation unit")
		}
		if err := methodology.ValidatePlan(value); err != nil {
			return nil, "", err
		}
		return marshal(value, versionedDevelopmentArtifactIdentity(value.PlanID, value.Revision))
	case DevelopmentArtifactImplementationAuthority:
		var value methodology.ImplementationAuthorityToken
		if err := strictDevelopmentJSON(raw, &value); err != nil {
			return nil, "", err
		}
		if value.UnitID != unitID {
			return nil, "", errors.New("implementation_authority token belongs to another implementation unit")
		}
		if err := methodology.ValidateImplementationAuthorityTokenAt(value, now.UTC()); err != nil {
			return nil, "", err
		}
		return marshal(value, value.ImplementationAuthorityTokenID)
	case DevelopmentArtifactRuling:
		var value methodology.Ruling
		if err := strictDevelopmentJSON(raw, &value); err != nil {
			return nil, "", err
		}
		if value.UnitID != unitID {
			return nil, "", errors.New("ruling belongs to another implementation unit")
		}
		if err := methodology.ValidateRuling(value); err != nil {
			return nil, "", err
		}
		return marshal(value, value.RulingID)
	case DevelopmentArtifactEvidence:
		var value methodology.EvidenceReceipt
		if err := strictDevelopmentJSON(raw, &value); err != nil {
			return nil, "", err
		}
		if value.UnitID != unitID {
			return nil, "", errors.New("evidence belongs to another implementation unit")
		}
		if err := methodology.ValidateEvidenceReceipt(value); err != nil {
			return nil, "", err
		}
		return marshal(value, value.EvidenceID)
	case DevelopmentArtifactReview:
		var value methodology.ReviewRecord
		if err := strictDevelopmentJSON(raw, &value); err != nil {
			return nil, "", err
		}
		if value.UnitID != unitID {
			return nil, "", errors.New("review belongs to another implementation unit")
		}
		if err := methodology.ValidateReviewRecord(value); err != nil {
			return nil, "", err
		}
		return marshal(value, value.ReviewID)
	case DevelopmentArtifactLedger:
		var value methodology.Ledger
		if err := strictDevelopmentJSON(raw, &value); err != nil {
			return nil, "", err
		}
		if value.UnitID != unitID {
			return nil, "", errors.New("ledger belongs to another implementation unit")
		}
		if err := value.Validate(); err != nil {
			return nil, "", err
		}
		payload, _, err := marshal(value, value.PlanID)
		if err != nil {
			return nil, "", err
		}
		return payload, value.PlanID + "-checkpoint-" + methodology.HashContent(string(payload))[:16], nil
	default:
		return nil, "", fmt.Errorf("unsupported development artifact type %q", kind)
	}
}

func versionedDevelopmentArtifactIdentity(logicalID, revision string) string {
	logicalID = strings.TrimSpace(logicalID)
	revision = strings.TrimSpace(revision)
	if revision == "" {
		return logicalID
	}
	return logicalID + "-revision-" + revision
}

func developmentArtifactID(unitID, kind, identity string) string {
	return strings.Join([]string{
		"development",
		boundedDevelopmentArtifactSegment(unitID),
		boundedDevelopmentArtifactSegment(kind),
		boundedDevelopmentArtifactSegment(identity),
	}, ":")
}

func boundedDevelopmentArtifactSegment(value string) string {
	raw := strings.TrimSpace(value)
	segment := safeSegment(raw)
	if segment == "" {
		segment = "item"
	}
	runes := []rune(segment)
	if segment == raw && len(runes) <= developmentArtifactSegmentMaxRunes {
		return segment
	}
	digest := methodology.HashContent(raw)
	const digestSuffixLength = 17 // '-' plus the first 16 SHA-256 characters.
	maxPrefixLength := developmentArtifactSegmentMaxRunes - digestSuffixLength
	if maxPrefixLength < 1 {
		return digest[:developmentArtifactSegmentMaxRunes]
	}
	if len(runes) > maxPrefixLength {
		runes = runes[:maxPrefixLength]
	}
	return string(runes) + "-" + digest[:16]
}

func containsDevelopmentSecret(value string) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed != "" && methodology.RedactSecrets(trimmed) != trimmed
}

func validateInitialDevelopmentLedger(ledger methodology.Ledger) error {
	if err := ledger.Validate(); err != nil {
		return err
	}
	if !strings.EqualFold(ledger.CurrentState, string(methodology.TaskPending)) {
		return errors.New("initial ledger must start in PENDING")
	}
	if len(ledger.Worktrees) == 0 || len(ledger.BaselineEvidence) == 0 {
		return errors.New("initial ledger requires an isolated worktree and verified baseline")
	}
	return nil
}

func validateDevelopmentProjectionBindings(item domainbacklog.Item, projection DevelopmentProjection) error {
	if projection.Specification != nil && projection.Plan != nil {
		if err := methodology.ValidatePlanAgainstSpecification(*projection.Plan, *projection.Specification); err != nil {
			return err
		}
	}
	if projection.ImplementationAuthorityToken != nil {
		if projection.Specification == nil || item.ConceptState != domainbacklog.ConceptAdopted {
			return errors.New("implementation authority requires current adopted specification")
		}
		adoptedAt, err := time.Parse(time.RFC3339, item.AdoptedAt)
		if err != nil {
			return errors.New("adoption timestamp is invalid")
		}
		adoption := methodology.AdoptionEvidence{EvidenceID: "atlas-adoption:" + item.ItemID, UnitID: item.ImplementationUnit, SpecRef: projection.Specification.SpecID, SpecHash: projection.Specification.ContentHash, Decision: "ADOPTED", Verified: true, CreatedAt: adoptedAt}
		if err := projection.ImplementationAuthorityToken.ValidateFor(adoption, projection.ImplementationAuthorityToken.IssuedAt); err != nil {
			return err
		}
	}
	if projection.Ledger != nil {
		if projection.Plan == nil || projection.Specification == nil {
			return errors.New("ledger requires current plan and specification")
		}
		ledger := projection.Ledger
		if ledger.UnitID != item.ImplementationUnit || ledger.PlanID != projection.Plan.PlanID || ledger.SpecRef != projection.Specification.SpecID || !strings.EqualFold(ledger.SpecHash, projection.Specification.ContentHash) || ledger.Revision != projection.Plan.Revision {
			return errors.New("ledger belongs to another unit, plan, specification, or revision")
		}
		if err := ledger.Validate(); err != nil {
			return err
		}
	}
	for _, receipt := range projection.Evidence {
		if projection.Plan == nil || receipt.UnitID != item.ImplementationUnit || receipt.PlanID != projection.Plan.PlanID || !strings.EqualFold(receipt.SpecHash, projection.Plan.SpecHash) || receipt.ValidForRevision != projection.Plan.Revision {
			return errors.New("evidence belongs to another unit, plan, specification, or revision")
		}
	}
	for _, review := range projection.Reviews {
		if projection.Plan == nil || review.UnitID != item.ImplementationUnit || review.PlanID != projection.Plan.PlanID || !strings.EqualFold(review.SpecHash, projection.Plan.SpecHash) || review.ValidForRevision != projection.Plan.Revision {
			return errors.New("review belongs to another unit, plan, specification, or revision")
		}
	}
	return nil
}

func developmentAuthorityAllows(token methodology.ImplementationAuthorityToken, target string) bool {
	for _, scope := range token.Scope {
		normalized := strings.ToLower(strings.TrimSpace(scope))
		if normalized == "implementation" || normalized == strings.ToLower(strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}

func validateDevelopmentArtifactAgainstCurrent(kind string, payload []byte, current DevelopmentProjection) error {
	switch kind {
	case DevelopmentArtifactSpecification:
		var value methodology.Specification
		if err := json.Unmarshal(payload, &value); err != nil {
			return err
		}
		if current.Plan != nil && current.Plan.SpecHash != value.ContentHash {
			return errors.New("specification revision requires the current plan to be closed")
		}
	case DevelopmentArtifactPlan:
		var value methodology.Plan
		if err := json.Unmarshal(payload, &value); err != nil {
			return err
		}
		if current.Specification == nil {
			return errors.New("plan requires current specification")
		}
		if err := methodology.ValidatePlanAgainstSpecification(value, *current.Specification); err != nil {
			return err
		}
		if current.Ledger != nil && current.Ledger.PlanID != value.PlanID {
			return errors.New("plan replacement requires the current ledger to be closed")
		}
	case DevelopmentArtifactEvidence:
		var value methodology.EvidenceReceipt
		if err := json.Unmarshal(payload, &value); err != nil {
			return err
		}
		if current.Plan == nil || value.PlanID != current.Plan.PlanID || !strings.EqualFold(value.SpecHash, current.Plan.SpecHash) || value.ValidForRevision != current.Plan.Revision {
			return errors.New("evidence is not bound to the current plan revision")
		}
	case DevelopmentArtifactReview:
		var value methodology.ReviewRecord
		if err := json.Unmarshal(payload, &value); err != nil {
			return err
		}
		if current.Plan == nil || value.PlanID != current.Plan.PlanID || !strings.EqualFold(value.SpecHash, current.Plan.SpecHash) || value.ValidForRevision != current.Plan.Revision {
			return errors.New("review is not bound to the current plan revision")
		}
	case DevelopmentArtifactRuling:
		var value methodology.Ruling
		if err := json.Unmarshal(payload, &value); err != nil {
			return err
		}
		if current.Plan == nil || value.PlanID != current.Plan.PlanID || value.SpecRef != current.Plan.SpecRef {
			return errors.New("ruling is not bound to the current plan")
		}
	}
	return nil
}

func strictDevelopmentJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("development artifact must contain exactly one JSON value")
	}
	return nil
}

func applyDevelopmentArtifact(projection *DevelopmentProjection, kind string, payload json.RawMessage) error {
	switch kind {
	case DevelopmentArtifactSpecification:
		var value methodology.Specification
		if err := json.Unmarshal(payload, &value); err != nil {
			return err
		}
		projection.Specification = &value
	case DevelopmentArtifactPlan:
		var value methodology.Plan
		if err := json.Unmarshal(payload, &value); err != nil {
			return err
		}
		projection.Plan = &value
	case DevelopmentArtifactImplementationAuthority:
		var value methodology.ImplementationAuthorityToken
		if err := json.Unmarshal(payload, &value); err != nil {
			return err
		}
		projection.ImplementationAuthorityToken = &value
	case DevelopmentArtifactLedger:
		var value methodology.Ledger
		if err := json.Unmarshal(payload, &value); err != nil {
			return err
		}
		projection.Ledger = &value
	case DevelopmentArtifactRuling:
		var value methodology.Ruling
		if err := json.Unmarshal(payload, &value); err != nil {
			return err
		}
		projection.Rulings = append(projection.Rulings, value)
	case DevelopmentArtifactEvidence:
		var value methodology.EvidenceReceipt
		if err := json.Unmarshal(payload, &value); err != nil {
			return err
		}
		projection.Evidence = append(projection.Evidence, value)
	case DevelopmentArtifactReview:
		var value methodology.ReviewRecord
		if err := json.Unmarshal(payload, &value); err != nil {
			return err
		}
		projection.Reviews = append(projection.Reviews, value)
	}
	return nil
}
