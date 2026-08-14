package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"time"

	revenueapp "github.com/Nyukimin/RenCrow_CORE/internal/application/revenue"
	domainrevenue "github.com/Nyukimin/RenCrow_CORE/internal/domain/revenue"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

const runtimeDataWritePolicyRevision = "core-db-memory-owner/v1"

const (
	runtimeGoalIDPrefix        = "goal/sha256:"
	runtimeTraceIDPrefix       = "trace/sha256:"
	runtimeOpportunityIDPrefix = "opportunity/sha256:"
)

type runtimeWorkstreamGoalStore interface {
	SaveGoal(context.Context, domainworkstream.Goal) error
	FindGoalByID(context.Context, string) (domainworkstream.Goal, bool, error)
}

type runtimeRevenueOpportunityStore interface {
	SaveOpportunity(context.Context, domainrevenue.Opportunity) error
	FindOpportunityByID(context.Context, string) (domainrevenue.Opportunity, bool, error)
}

type runtimeWorkstreamGoalWritePayload struct {
	WorkstreamID    string   `json:"workstream_id"`
	Title           string   `json:"title"`
	Description     string   `json:"description,omitempty"`
	SuccessCriteria []string `json:"success_criteria"`
	Verification    []string `json:"verification"`
}

type runtimeRevenueOpportunityWritePayload struct {
	SourceKind      string  `json:"source_kind"`
	Title           string  `json:"title"`
	Summary         string  `json:"summary,omitempty"`
	TargetCustomer  string  `json:"target_customer,omitempty"`
	ExpectedRevenue int     `json:"expected_revenue,omitempty"`
	ExpectedCost    int     `json:"expected_cost,omitempty"`
	ReuseValue      float64 `json:"reuse_value,omitempty"`
	AutomationRate  float64 `json:"automation_rate,omitempty"`
	StrategicValue  float64 `json:"strategic_value,omitempty"`
	RiskScore       float64 `json:"risk_score,omitempty"`
}

type runtimeWorkstreamGoalWriter struct {
	mu    sync.Mutex
	store runtimeWorkstreamGoalStore
}

type runtimeRevenueOpportunityWriter struct {
	mu    sync.Mutex
	store runtimeRevenueOpportunityStore
}

func registerRuntimeDataWriteWorkstream(r *runtimeDataWriteRegistry, store runtimeWorkstreamGoalStore) error {
	if r == nil || store == nil {
		return fmt.Errorf("workstream data write unavailable")
	}
	writer := &runtimeWorkstreamGoalWriter{store: store}
	return r.RegisterWithContract("workstream", "create_goal", dataRecallAccessUser, runtimeDataWriteContract{
		RequiredPayloadFields: []string{"workstream_id", "title", "success_criteria", "verification"},
		OptionalPayloadFields: []string{"description"},
	}, writer.write)
}

func registerRuntimeDataWriteRevenue(r *runtimeDataWriteRegistry, store runtimeRevenueOpportunityStore) error {
	if r == nil || store == nil {
		return fmt.Errorf("revenue data write unavailable")
	}
	writer := &runtimeRevenueOpportunityWriter{store: store}
	return r.RegisterWithContract("revenue", "draft_opportunity", dataRecallAccessInternal, runtimeDataWriteContract{
		RequiredPayloadFields: []string{"source_kind", "title"},
		OptionalPayloadFields: []string{"summary", "target_customer", "expected_revenue", "expected_cost", "reuse_value", "automation_rate", "strategic_value", "risk_score"},
	}, writer.write)
}

func (w *runtimeWorkstreamGoalWriter) write(ctx context.Context, request tools.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
	scope, err := runtimeDataWriteOwnerScope(ctx)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	payload, err := decodeRuntimeWorkstreamGoalPayload(request.Payload)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	now := time.Now().UTC()
	goal := domainworkstream.Goal{
		GoalID:          runtimeDataWriteDerivedID(runtimeGoalIDPrefix, scope.RequestID),
		TraceID:         runtimeDataWriteDerivedID(runtimeTraceIDPrefix, scope.RequestID),
		WorkstreamID:    payload.WorkstreamID,
		Title:           payload.Title,
		Description:     payload.Description,
		SuccessCriteria: payload.SuccessCriteria,
		Verification:    payload.Verification,
		Status:          domainworkstream.StatusDraft,
		CreatedAt:       now,
	}
	if err := domainworkstream.ValidateGoal(goal); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	existing, found, err := w.store.FindGoalByID(ctx, goal.GoalID)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if found {
		if !runtimeDataWriteGoalsEqual(existing, goal) {
			return runtimeDataWriteOwnerResult{}, fmt.Errorf("workstream goal idempotency payload mismatch")
		}
		return runtimeDataWriteOwnerResult{
			SchemaVersion:    "workstream-goal/v1",
			MigrationState:   "embedded_current",
			ValidationState:  "owner_validated",
			AuditRef:         existing.GoalID,
			IdempotencyKey:   scope.RequestID,
			IdempotentReplay: true,
			PolicyRevision:   runtimeDataWritePolicyRevision,
		}, nil
	}
	if err := w.store.SaveGoal(ctx, goal); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	return runtimeDataWriteOwnerResult{
		SchemaVersion:    "workstream-goal/v1",
		MigrationState:   "embedded_current",
		ValidationState:  "owner_validated",
		AuditRef:         goal.GoalID,
		IdempotencyKey:   scope.RequestID,
		IdempotentReplay: false,
		PolicyRevision:   runtimeDataWritePolicyRevision,
	}, nil
}

func (w *runtimeRevenueOpportunityWriter) write(ctx context.Context, request tools.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
	scope, err := runtimeDataWriteOwnerScope(ctx)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	payload, err := decodeRuntimeRevenueOpportunityPayload(request.Payload)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	now := time.Now().UTC()
	opportunity := domainrevenue.Opportunity{
		OpportunityID:   runtimeDataWriteDerivedID(runtimeOpportunityIDPrefix, scope.RequestID),
		SourceKind:      payload.SourceKind,
		Title:           payload.Title,
		Summary:         payload.Summary,
		TargetCustomer:  payload.TargetCustomer,
		ExpectedRevenue: payload.ExpectedRevenue,
		ExpectedCost:    payload.ExpectedCost,
		ReuseValue:      payload.ReuseValue,
		AutomationRate:  payload.AutomationRate,
		StrategicValue:  payload.StrategicValue,
		RiskScore:       payload.RiskScore,
		CreatedAt:       now,
	}
	expected := opportunity
	expected.TraceID = runtimeDataWriteDerivedID(runtimeTraceIDPrefix, scope.RequestID)
	expected = domainrevenue.NormalizeOpportunityEconomics(expected)
	if err := domainrevenue.ValidateOpportunity(expected); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	existing, found, err := w.store.FindOpportunityByID(ctx, opportunity.OpportunityID)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if found {
		if !runtimeDataWriteOpportunitiesEqual(existing, expected) {
			return runtimeDataWriteOwnerResult{}, fmt.Errorf("revenue opportunity idempotency payload mismatch")
		}
		return runtimeDataWriteOwnerResult{
			SchemaVersion:    "revenue-opportunity/v1",
			MigrationState:   "embedded_current",
			ValidationState:  "owner_validated",
			AuditRef:         existing.OpportunityID,
			IdempotencyKey:   scope.RequestID,
			IdempotentReplay: true,
			PolicyRevision:   runtimeDataWritePolicyRevision,
		}, nil
	}

	service := revenueapp.NewEconomicService(w.store, func() time.Time { return now }).WithTraceIDGenerator(func() string {
		return expected.TraceID
	})
	created, err := service.DraftOpportunity(ctx, opportunity)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	return runtimeDataWriteOwnerResult{
		SchemaVersion:    "revenue-opportunity/v1",
		MigrationState:   "embedded_current",
		ValidationState:  "owner_validated",
		AuditRef:         created.OpportunityID,
		IdempotencyKey:   scope.RequestID,
		IdempotentReplay: false,
		PolicyRevision:   runtimeDataWritePolicyRevision,
	}, nil
}

func decodeRuntimeWorkstreamGoalPayload(payload map[string]any) (runtimeWorkstreamGoalWritePayload, error) {
	if err := validateRuntimeDataWritePayloadKeys(payload, map[string]struct{}{
		"workstream_id": {}, "title": {}, "description": {}, "success_criteria": {}, "verification": {},
	}); err != nil {
		return runtimeWorkstreamGoalWritePayload{}, err
	}
	var decoded runtimeWorkstreamGoalWritePayload
	if err := decodeRuntimeDataWritePayload(payload, &decoded); err != nil {
		return runtimeWorkstreamGoalWritePayload{}, err
	}
	decoded.WorkstreamID = strings.TrimSpace(decoded.WorkstreamID)
	decoded.Title = strings.TrimSpace(decoded.Title)
	decoded.Description = strings.TrimSpace(decoded.Description)
	var err error
	decoded.SuccessCriteria, err = trimRuntimeDataWriteEntries(decoded.SuccessCriteria, "success_criteria")
	if err != nil {
		return runtimeWorkstreamGoalWritePayload{}, err
	}
	decoded.Verification, err = trimRuntimeDataWriteEntries(decoded.Verification, "verification")
	if err != nil {
		return runtimeWorkstreamGoalWritePayload{}, err
	}
	return decoded, nil
}

func decodeRuntimeRevenueOpportunityPayload(payload map[string]any) (runtimeRevenueOpportunityWritePayload, error) {
	if err := validateRuntimeDataWritePayloadKeys(payload, map[string]struct{}{
		"source_kind": {}, "title": {}, "summary": {}, "target_customer": {}, "expected_revenue": {},
		"expected_cost": {}, "reuse_value": {}, "automation_rate": {}, "strategic_value": {}, "risk_score": {},
	}); err != nil {
		return runtimeRevenueOpportunityWritePayload{}, err
	}
	var decoded runtimeRevenueOpportunityWritePayload
	if err := decodeRuntimeDataWritePayload(payload, &decoded); err != nil {
		return runtimeRevenueOpportunityWritePayload{}, err
	}
	decoded.SourceKind = strings.TrimSpace(decoded.SourceKind)
	decoded.Title = strings.TrimSpace(decoded.Title)
	decoded.Summary = strings.TrimSpace(decoded.Summary)
	decoded.TargetCustomer = strings.TrimSpace(decoded.TargetCustomer)
	return decoded, nil
}

func validateRuntimeDataWritePayloadKeys(payload map[string]any, allowed map[string]struct{}) error {
	for key := range payload {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("owner payload contains unsupported field %q", key)
		}
		if payload[key] == nil {
			return fmt.Errorf("owner payload field %q must not be null", key)
		}
	}
	return nil
}

func decodeRuntimeDataWritePayload(payload map[string]any, target any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("owner payload is invalid: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("owner payload is invalid: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("owner payload contains trailing data")
		}
		return fmt.Errorf("owner payload is invalid: %w", err)
	}
	return nil
}

func trimRuntimeDataWriteEntries(values []string, field string) ([]string, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("%s is required", field)
	}
	trimmed := make([]string, len(values))
	for i, value := range values {
		trimmed[i] = strings.TrimSpace(value)
		if trimmed[i] == "" {
			return nil, fmt.Errorf("%s[%d] is required", field, i)
		}
	}
	return trimmed, nil
}

func runtimeDataWriteOwnerScope(ctx context.Context) (domaintool.ToolExecutionScope, error) {
	scope, found := domaintool.ToolExecutionScopeFromContext(ctx)
	if !found {
		return domaintool.ToolExecutionScope{}, fmt.Errorf("trusted Tool execution scope is missing")
	}
	if err := scope.Validate(); err != nil || scope.ActorKind != domaintool.ActorKindAgent || strings.TrimSpace(scope.RequestID) == "" {
		return domaintool.ToolExecutionScope{}, fmt.Errorf("trusted Tool execution scope is invalid")
	}
	scope.RequestID = strings.TrimSpace(scope.RequestID)
	return scope, nil
}

func runtimeDataWriteDerivedID(prefix, requestID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(requestID)))
	return prefix + hex.EncodeToString(digest[:])
}

func runtimeDataWriteGoalsEqual(left, right domainworkstream.Goal) bool {
	left.CreatedAt = time.Time{}
	right.CreatedAt = time.Time{}
	return reflect.DeepEqual(left, right)
}

func runtimeDataWriteOpportunitiesEqual(left, right domainrevenue.Opportunity) bool {
	left.CreatedAt = time.Time{}
	left.UpdatedAt = time.Time{}
	right.CreatedAt = time.Time{}
	right.UpdatedAt = time.Time{}
	return reflect.DeepEqual(left, right)
}
