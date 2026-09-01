package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	dcipersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/dci"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	agentOpsDCIIdentityAcceptanceOperation = "dci_identity_acceptance"
	agentOpsToolOutputMaxBytes             = 64 << 10

	agentOpsDCIWriteStore      = "dci"
	agentOpsDCIWriteOperation  = "search"
	agentOpsDCIRecallStore     = "dci"
	agentOpsDCIRecallOperation = "identity_evidence"
)

var (
	errAgentOpsRequestTooLarge        = errors.New("agent ops request is too large")
	errAgentOpsDCIIdentityUnavailable = errors.New("agent ops dci identity acceptance is unavailable")
	errAgentOpsDCIIdentityExecution   = errors.New("agent ops dci identity acceptance failed")
)

type agentOpsRequestBranch uint8

const (
	agentOpsRequestBranchLegacy agentOpsRequestBranch = iota
	agentOpsRequestBranchDCIIdentityAcceptance
)

// normalizeAgentOpsRequest keeps the public request a strict tagged union.
// The normalized values are written back so the fixed operation receives the
// same bounded query that was validated at the HTTP boundary.
func normalizeAgentOpsRequest(request *agentOpsRequest) (agentOpsRequestBranch, error) {
	if request == nil {
		return 0, errors.New("agent ops request is missing")
	}
	request.Message = strings.TrimSpace(request.Message)
	request.Operation = strings.TrimSpace(request.Operation)
	request.Query = strings.TrimSpace(request.Query)
	if len([]byte(request.Message)) > agentOpsMaxMessageBytes || len([]byte(request.Query)) > agentOpsMaxMessageBytes {
		return 0, errAgentOpsRequestTooLarge
	}
	switch {
	case request.Message != "" && request.Operation == "" && request.Query == "":
		return agentOpsRequestBranchLegacy, nil
	case request.Message == "" && request.Operation == agentOpsDCIIdentityAcceptanceOperation && request.Query != "":
		return agentOpsRequestBranchDCIIdentityAcceptance, nil
	default:
		return 0, errors.New("agent ops request shape is invalid")
	}
}

type agentOpsDCIIdentityAcceptanceResponse struct {
	SchemaVersion          string `json:"schema_version"`
	Status                 string `json:"status"`
	RequestID              string `json:"request_id"`
	AgentID                string `json:"agent_id"`
	Role                   string `json:"role"`
	Operation              string `json:"operation"`
	ActionID               string `json:"action_id"`
	TraceID                string `json:"trace_id"`
	FirstWriteReplay       bool   `json:"first_write_replay"`
	SecondWriteReplay      bool   `json:"second_write_replay"`
	EventCount             int    `json:"event_count"`
	StepCount              int    `json:"step_count"`
	EvidenceCount          int    `json:"evidence_count"`
	CurrentProjectionCount int    `json:"current_projection_count"`
	ArchiveProjectionCount int    `json:"archive_projection_count"`
	EventGraphSHA256       string `json:"event_graph_sha256"`
}

type agentOpsDCIWriteReceipt struct {
	Owner            string `json:"owner"`
	OwnerRoute       string `json:"owner_route"`
	AuditRef         string `json:"audit_ref"`
	ActorID          string `json:"actor_id"`
	AgentRole        string `json:"agent_role"`
	Purpose          string `json:"purpose"`
	DataScope        string `json:"data_scope"`
	Status           string `json:"status"`
	SchemaVersion    string `json:"schema_version"`
	MigrationState   string `json:"migration_state"`
	ValidationState  string `json:"validation_state"`
	IdempotentReplay bool   `json:"idempotent_replay"`
	PolicyRevision   string `json:"policy_revision"`
	CompletedAt      string `json:"completed_at"`
}

type agentOpsDCIRecallResult struct {
	Store     string                      `json:"store"`
	Operation string                      `json:"operation"`
	Records   []agentOpsDCIIdentityRecord `json:"records"`
	Partial   bool                        `json:"partial"`
	Evidence  agentOpsDCIRecallEvidence   `json:"evidence"`
}

type agentOpsDCIRecallEvidence struct {
	RequestID       string `json:"request_id"`
	ActorID         string `json:"actor_id"`
	AgentRole       string `json:"agent_role"`
	Purpose         string `json:"purpose"`
	DataScope       string `json:"data_scope"`
	Owner           string `json:"owner"`
	OwnerRoute      string `json:"owner_route"`
	RetrievedAt     string `json:"retrieved_at"`
	FreshnessState  string `json:"freshness_state"`
	ValidationState string `json:"validation_state"`
	BudgetLimit     int    `json:"budget_limit"`
	ReturnedCount   int    `json:"returned_count"`
}

type agentOpsDCIIdentityRecord struct {
	SchemaVersion          string `json:"schema_version"`
	Status                 string `json:"status"`
	ActionID               string `json:"action_id"`
	TraceID                string `json:"trace_id"`
	ActorKind              string `json:"actor_kind"`
	ActorID                string `json:"actor_id"`
	SearchStatus           string `json:"search_status"`
	EventCount             int    `json:"event_count"`
	StepCount              int    `json:"step_count"`
	EvidenceCount          int    `json:"evidence_count"`
	CurrentProjectionCount int    `json:"current_projection_count"`
	ArchiveProjectionCount int    `json:"archive_projection_count"`
	EventGraphSHA256       string `json:"event_graph_sha256"`
}

func (h *agentOpsHandler) executeAgentOpsDCIIdentityAcceptance(ctx context.Context, requestID, query string) (agentOpsDCIIdentityAcceptanceResponse, error) {
	if h == nil || h.toolExecutor == nil {
		return agentOpsDCIIdentityAcceptanceResponse{}, errAgentOpsDCIIdentityUnavailable
	}
	if !validAgentOpsDCIIdentityScope(ctx, requestID) {
		return agentOpsDCIIdentityAcceptanceResponse{}, errAgentOpsDCIIdentityUnavailable
	}

	writeArgs := map[string]interface{}{
		"store":     agentOpsDCIWriteStore,
		"operation": agentOpsDCIWriteOperation,
		"payload": map[string]interface{}{
			"query": query,
		},
	}
	firstRaw, err := h.toolExecutor.ExecuteTool(ctx, "data.write", writeArgs)
	if err != nil {
		return agentOpsDCIIdentityAcceptanceResponse{}, errAgentOpsDCIIdentityExecution
	}
	first, err := decodeAgentOpsDCIWriteReceipt(firstRaw)
	if err != nil {
		return agentOpsDCIIdentityAcceptanceResponse{}, errAgentOpsDCIIdentityExecution
	}
	firstActionID := modulecore.ActionID(first.AuditRef)

	secondRaw, err := h.toolExecutor.ExecuteTool(ctx, "data.write", writeArgs)
	if err != nil {
		return agentOpsDCIIdentityAcceptanceResponse{}, errAgentOpsDCIIdentityExecution
	}
	second, err := decodeAgentOpsDCIWriteReceipt(secondRaw)
	if err != nil || second.AuditRef != first.AuditRef || !second.IdempotentReplay {
		return agentOpsDCIIdentityAcceptanceResponse{}, errAgentOpsDCIIdentityExecution
	}

	recallArgs := map[string]interface{}{
		"store":     agentOpsDCIRecallStore,
		"operation": agentOpsDCIRecallOperation,
		"query":     first.AuditRef,
		"limit":     1,
	}
	recallRaw, err := h.toolExecutor.ExecuteTool(ctx, "data.recall", recallArgs)
	if err != nil {
		return agentOpsDCIIdentityAcceptanceResponse{}, errAgentOpsDCIIdentityExecution
	}
	record, err := decodeAgentOpsDCIRecallResult(recallRaw, requestID, firstActionID)
	if err != nil {
		return agentOpsDCIIdentityAcceptanceResponse{}, errAgentOpsDCIIdentityExecution
	}

	return agentOpsDCIIdentityAcceptanceResponse{
		SchemaVersion:          "rencrow.agent-ops.dci-identity-acceptance/v1",
		Status:                 "passed",
		RequestID:              requestID,
		AgentID:                "shiro",
		Role:                   "worker",
		Operation:              agentOpsDCIIdentityAcceptanceOperation,
		ActionID:               string(record.ActionID),
		TraceID:                string(record.TraceID),
		FirstWriteReplay:       first.IdempotentReplay,
		SecondWriteReplay:      second.IdempotentReplay,
		EventCount:             record.EventCount,
		StepCount:              record.StepCount,
		EvidenceCount:          record.EvidenceCount,
		CurrentProjectionCount: record.CurrentProjectionCount,
		ArchiveProjectionCount: record.ArchiveProjectionCount,
		EventGraphSHA256:       record.EventGraphSHA256,
	}, nil
}

func validAgentOpsDCIIdentityScope(ctx context.Context, requestID string) bool {
	scope, found := domaintool.ToolExecutionScopeFromContext(ctx)
	return found && scope.Validate() == nil &&
		scope.RequestID == requestID &&
		scope.ActorKind == domaintool.ActorKindAgent &&
		scope.ActorID == "shiro" &&
		scope.AgentRole == "worker" &&
		scope.Purpose == "ops" &&
		scope.AuthenticationSource == domaintool.AuthenticationSourceAgentOrchestrator &&
		scope.Allows(domaintool.DataScopeInternal)
}

func decodeAgentOpsDCIWriteReceipt(raw string) (agentOpsDCIWriteReceipt, error) {
	var receipt agentOpsDCIWriteReceipt
	if err := requireAgentOpsToolFields(raw,
		"owner", "owner_route", "audit_ref", "actor_id", "agent_role", "purpose", "data_scope", "status",
		"schema_version", "migration_state", "validation_state", "idempotent_replay", "policy_revision", "completed_at",
	); err != nil {
		return agentOpsDCIWriteReceipt{}, err
	}
	if err := decodeAgentOpsToolOutput(raw, &receipt); err != nil {
		return agentOpsDCIWriteReceipt{}, err
	}
	if receipt.Owner != agentOpsDCIWriteStore || receipt.OwnerRoute != agentOpsDCIWriteStore+"/"+agentOpsDCIWriteOperation ||
		receipt.ActorID != "shiro" || receipt.AgentRole != "worker" || receipt.Purpose != "ops" ||
		receipt.DataScope != domaintool.DataScopeInternal || receipt.Status != "completed" ||
		receipt.SchemaVersion != "dci-search/v2" || receipt.MigrationState != "embedded_current" ||
		receipt.ValidationState != "owner_validated" || receipt.PolicyRevision != runtimeDataWritePolicyRevision ||
		receipt.AuditRef == "" || modulecore.ActionID(receipt.AuditRef).Validate() != nil || !validAgentOpsTimestamp(receipt.CompletedAt) {
		return agentOpsDCIWriteReceipt{}, errAgentOpsDCIIdentityExecution
	}
	return receipt, nil
}

func decodeAgentOpsDCIRecallResult(raw, requestID string, actionID modulecore.ActionID) (dcipersistence.IdentityEvidence, error) {
	var result agentOpsDCIRecallResult
	if err := requireAgentOpsToolFields(raw, "store", "operation", "records", "partial", "evidence"); err != nil {
		return dcipersistence.IdentityEvidence{}, err
	}
	if err := decodeAgentOpsToolOutput(raw, &result); err != nil {
		return dcipersistence.IdentityEvidence{}, err
	}
	if result.Store != agentOpsDCIRecallStore || result.Operation != agentOpsDCIRecallOperation || result.Partial || len(result.Records) != 1 ||
		result.Evidence.RequestID != requestID || result.Evidence.ActorID != "shiro" || result.Evidence.AgentRole != "worker" ||
		result.Evidence.Purpose != "ops" || result.Evidence.DataScope != domaintool.DataScopeInternal ||
		result.Evidence.Owner != agentOpsDCIRecallStore || result.Evidence.OwnerRoute != agentOpsDCIRecallStore+"/"+agentOpsDCIRecallOperation ||
		!validAgentOpsTimestamp(result.Evidence.RetrievedAt) || result.Evidence.FreshnessState != "observed_at_read" ||
		result.Evidence.ValidationState != "owner_route_succeeded" || result.Evidence.BudgetLimit != 1 || result.Evidence.ReturnedCount != 1 {
		return dcipersistence.IdentityEvidence{}, errAgentOpsDCIIdentityExecution
	}
	record := result.Records[0].identityEvidence()
	if record.ActionID != actionID || record.ActorKind != string(domaintool.ActorKindAgent) || record.ActorID != "shiro" {
		return dcipersistence.IdentityEvidence{}, errAgentOpsDCIIdentityExecution
	}
	if err := dcipersistence.ValidateIdentityEvidence(record); err != nil {
		return dcipersistence.IdentityEvidence{}, errAgentOpsDCIIdentityExecution
	}
	return record, nil
}

func (r agentOpsDCIIdentityRecord) identityEvidence() dcipersistence.IdentityEvidence {
	return dcipersistence.IdentityEvidence{
		SchemaVersion:          r.SchemaVersion,
		Status:                 r.Status,
		ActionID:               modulecore.ActionID(r.ActionID),
		TraceID:                modulecore.TraceID(r.TraceID),
		ActorKind:              r.ActorKind,
		ActorID:                r.ActorID,
		SearchStatus:           r.SearchStatus,
		EventCount:             r.EventCount,
		StepCount:              r.StepCount,
		EvidenceCount:          r.EvidenceCount,
		CurrentProjectionCount: r.CurrentProjectionCount,
		ArchiveProjectionCount: r.ArchiveProjectionCount,
		EventGraphSHA256:       r.EventGraphSHA256,
	}
}

func decodeAgentOpsToolOutput(raw string, destination any) error {
	if len([]byte(raw)) == 0 || len([]byte(raw)) > agentOpsToolOutputMaxBytes {
		return errAgentOpsDCIIdentityExecution
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errAgentOpsDCIIdentityExecution
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errAgentOpsDCIIdentityExecution
	}
	return nil
}

func requireAgentOpsToolFields(raw string, fields ...string) error {
	if len([]byte(raw)) == 0 || len([]byte(raw)) > agentOpsToolOutputMaxBytes {
		return errAgentOpsDCIIdentityExecution
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	var values map[string]json.RawMessage
	if err := decoder.Decode(&values); err != nil || values == nil {
		return errAgentOpsDCIIdentityExecution
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errAgentOpsDCIIdentityExecution
	}
	for _, field := range fields {
		value, ok := values[field]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errAgentOpsDCIIdentityExecution
		}
	}
	return nil
}

func validAgentOpsTimestamp(value string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && !parsed.IsZero()
}
