package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	tradeclient "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tradeclient"
	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

const runtimeTradeMemoryStore = "investment"

const (
	runtimeTradeMemorySourceReadPurpose     = "source_memory_read"
	runtimeTradeMemorySourceWritePurpose    = "source_memory_write"
	runtimeTradeMemoryLearningReadPurpose   = "learning_memory_read"
	runtimeTradeMemoryLearningWritePurpose  = "learning_memory_write"
	runtimeTradeMemoryMarketReadPurpose     = "market_memory_read"
	runtimeTradeMemoryMarketWritePurpose    = "market_memory_write"
	runtimeTradeMemoryReplayReadPurpose     = "replay_memory_read"
	runtimeTradeMemoryReplayWritePurpose    = "replay_memory_write"
	runtimeTradeMemoryPortfolioReadPurpose  = "portfolio_memory_read"
	runtimeTradeMemoryPortfolioWritePurpose = "portfolio_memory_write"
)

var runtimeTradeMemoryIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,255}$`)
var runtimeTradeMemoryAuditRefPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// runtimeTradeMemoryClient is the narrow CORE-facing boundary for the
// TRADE-owned memory routes. The concrete client remains responsible for HTTP,
// authentication headers and module response validation.
type runtimeTradeMemoryClient interface {
	ReadSourceRecord(context.Context, string) (moduletrade.SourceRecordReadResponse, error)
	CollectSource(context.Context, string) (moduletrade.SourceRecordWriteResponse, error)
	ReadLearningCandidate(context.Context, string) (moduletrade.LearningCandidateReadResponse, error)
	ImportLearningCandidate(context.Context, string) (moduletrade.LearningCandidateWriteResponse, error)
	ReadMarketSnapshot(context.Context, string) (moduletrade.MarketSnapshotReadResponse, error)
	ImportMarketSnapshot(context.Context, string, string, string) (moduletrade.MarketSnapshotWriteResponse, error)
	ReadReplayDecision(context.Context, string) (moduletrade.ReplayDecisionReadResponse, error)
	RecordReplayDecision(context.Context, string, string, string, string) (moduletrade.ReplayDecisionWriteResponse, error)
	ReadPortfolioSnapshot(context.Context) (moduletrade.PortfolioSnapshotReadResponse, error)
	EnsurePortfolioInitialized(context.Context) (moduletrade.PortfolioSnapshotWriteResponse, error)
}

var _ runtimeTradeMemoryClient = (*tradeclient.Client)(nil)

type runtimeTradeMemoryChildInput struct {
	Query   string          `json:"query,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type runtimeTradeMemoryChildTuple struct {
	ParentRequestID string                       `json:"parent_request_id"`
	Route           string                       `json:"route"`
	Input           runtimeTradeMemoryChildInput `json:"input"`
}

func registerRuntimeDataRecallTradeMemory(r *runtimeDataRecallRegistry, client runtimeTradeMemoryClient) error {
	if r == nil || client == nil {
		return fmt.Errorf("TRADE memory recall unavailable")
	}
	if err := r.Register(runtimeTradeMemoryStore, "source_record", dataRecallAccessInternal, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		return runtimeTradeMemoryRecallSource(ctx, q, client)
	}); err != nil {
		return err
	}
	if err := r.Register(runtimeTradeMemoryStore, "learning_candidate", dataRecallAccessInternal, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		return runtimeTradeMemoryRecallLearning(ctx, q, client)
	}); err != nil {
		return err
	}
	if err := r.Register(runtimeTradeMemoryStore, "market_snapshot", dataRecallAccessInternal, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		return runtimeTradeMemoryRecallMarket(ctx, q, client)
	}); err != nil {
		return err
	}
	if err := r.Register(runtimeTradeMemoryStore, "replay_decision", dataRecallAccessInternal, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		return runtimeTradeMemoryRecallReplay(ctx, q, client)
	}); err != nil {
		return err
	}
	return r.Register(runtimeTradeMemoryStore, "portfolio_snapshot", dataRecallAccessInternal, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		return runtimeTradeMemoryRecallPortfolio(ctx, q, client)
	})
}

func registerRuntimeDataWriteTradeMemory(r *runtimeDataWriteRegistry, client runtimeTradeMemoryClient) error {
	if r == nil || client == nil {
		return fmt.Errorf("TRADE memory write unavailable")
	}
	if err := r.RegisterWithContract(runtimeTradeMemoryStore, "collect_source", dataRecallAccessInternal, runtimeDataWriteContract{
		RequiredPayloadFields: []string{"source_definition_id"},
	}, func(ctx context.Context, request tools.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
		return runtimeTradeMemoryWriteSource(ctx, request, client)
	}); err != nil {
		return err
	}
	if err := r.RegisterWithContract(runtimeTradeMemoryStore, "import_learning_candidate", dataRecallAccessInternal, runtimeDataWriteContract{
		RequiredPayloadFields: []string{"candidate_definition_id"},
	}, func(ctx context.Context, request tools.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
		return runtimeTradeMemoryWriteLearning(ctx, request, client)
	}); err != nil {
		return err
	}
	if err := r.RegisterWithContract(runtimeTradeMemoryStore, "import_market_snapshot", dataRecallAccessInternal, runtimeDataWriteContract{
		RequiredPayloadFields: []string{"run_id", "instrument_id", "trade_date"},
	}, func(ctx context.Context, request tools.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
		return runtimeTradeMemoryWriteMarket(ctx, request, client)
	}); err != nil {
		return err
	}
	if err := r.RegisterWithContract(runtimeTradeMemoryStore, "record_replay_decision", dataRecallAccessInternal, runtimeDataWriteContract{
		RequiredPayloadFields: []string{"run_id", "instrument_id", "trade_date", "action"},
	}, func(ctx context.Context, request tools.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
		return runtimeTradeMemoryWriteReplay(ctx, request, client)
	}); err != nil {
		return err
	}
	return r.RegisterWithContract(runtimeTradeMemoryStore, "ensure_portfolio_initialized", dataRecallAccessInternal, runtimeDataWriteContract{}, func(ctx context.Context, request tools.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
		return runtimeTradeMemoryWritePortfolio(ctx, request, client)
	})
}

func runtimeTradeMemoryRecallSource(ctx context.Context, request tools.DataRecallRequest, client runtimeTradeMemoryClient) (runtimeDataRecallResult, error) {
	outer, err := runtimeTradeMemoryOuterScope(ctx)
	if err != nil {
		return runtimeDataRecallResult{}, err
	}
	if err := runtimeTradeMemoryValidateID(request.Query, "source record ID"); err != nil {
		return runtimeDataRecallResult{}, err
	}
	childCtx, childID, err := runtimeTradeMemoryChildScope(ctx, outer, runtimeTradeMemoryStore+"/source_record", runtimeTradeMemoryChildInput{Query: request.Query}, runtimeTradeMemorySourceReadPurpose)
	if err != nil {
		return runtimeDataRecallResult{}, err
	}
	response, err := client.ReadSourceRecord(childCtx, request.Query)
	if err != nil {
		return runtimeDataRecallResult{}, err
	}
	if err := response.Validate(childID, childID); err != nil {
		return runtimeDataRecallResult{}, fmt.Errorf("validate TRADE source record response: %w", err)
	}
	if response.Record.SourceRecordID != request.Query {
		return runtimeDataRecallResult{}, fmt.Errorf("TRADE source record response ID does not match the exact query")
	}
	return newRuntimeDataRecallResult(request.Store, request.Operation, []map[string]any{runtimeTradeMemorySourceProjection(response.Record, response.OwnerEvidence)}), nil
}

func runtimeTradeMemoryRecallLearning(ctx context.Context, request tools.DataRecallRequest, client runtimeTradeMemoryClient) (runtimeDataRecallResult, error) {
	outer, err := runtimeTradeMemoryOuterScope(ctx)
	if err != nil {
		return runtimeDataRecallResult{}, err
	}
	if err := runtimeTradeMemoryValidateID(request.Query, "learning candidate ID"); err != nil {
		return runtimeDataRecallResult{}, err
	}
	childCtx, childID, err := runtimeTradeMemoryChildScope(ctx, outer, runtimeTradeMemoryStore+"/learning_candidate", runtimeTradeMemoryChildInput{Query: request.Query}, runtimeTradeMemoryLearningReadPurpose)
	if err != nil {
		return runtimeDataRecallResult{}, err
	}
	response, err := client.ReadLearningCandidate(childCtx, request.Query)
	if err != nil {
		return runtimeDataRecallResult{}, err
	}
	if err := response.Validate(childID, childID); err != nil {
		return runtimeDataRecallResult{}, fmt.Errorf("validate TRADE learning candidate response: %w", err)
	}
	if response.Record.CandidateRecordID != request.Query {
		return runtimeDataRecallResult{}, fmt.Errorf("TRADE learning candidate response ID does not match the exact query")
	}
	return newRuntimeDataRecallResult(request.Store, request.Operation, []map[string]any{runtimeTradeMemoryLearningProjection(response.Record, response.OwnerEvidence)}), nil
}

func runtimeTradeMemoryRecallMarket(ctx context.Context, request tools.DataRecallRequest, client runtimeTradeMemoryClient) (runtimeDataRecallResult, error) {
	outer, err := runtimeTradeMemoryOuterScope(ctx)
	if err != nil {
		return runtimeDataRecallResult{}, err
	}
	if err := runtimeTradeMemoryValidateID(request.Query, "market snapshot ID"); err != nil {
		return runtimeDataRecallResult{}, err
	}
	childCtx, childID, err := runtimeTradeMemoryChildScope(ctx, outer, runtimeTradeMemoryStore+"/market_snapshot", runtimeTradeMemoryChildInput{Query: request.Query}, runtimeTradeMemoryMarketReadPurpose)
	if err != nil {
		return runtimeDataRecallResult{}, err
	}
	response, err := client.ReadMarketSnapshot(childCtx, request.Query)
	if err != nil {
		return runtimeDataRecallResult{}, err
	}
	if err := response.Validate(childID, childID); err != nil {
		return runtimeDataRecallResult{}, fmt.Errorf("validate TRADE market snapshot response: %w", err)
	}
	if response.Record.SnapshotID != request.Query {
		return runtimeDataRecallResult{}, fmt.Errorf("TRADE market snapshot response ID does not match the exact query")
	}
	return newRuntimeDataRecallResult(request.Store, request.Operation, []map[string]any{runtimeTradeMemoryMarketProjection(response.Record, response.OwnerEvidence)}), nil
}

func runtimeTradeMemoryRecallReplay(ctx context.Context, request tools.DataRecallRequest, client runtimeTradeMemoryClient) (runtimeDataRecallResult, error) {
	outer, err := runtimeTradeMemoryOuterScope(ctx)
	if err != nil {
		return runtimeDataRecallResult{}, err
	}
	if err := runtimeTradeMemoryValidateID(request.Query, "replay decision ID"); err != nil {
		return runtimeDataRecallResult{}, err
	}
	childCtx, childID, err := runtimeTradeMemoryChildScope(ctx, outer, runtimeTradeMemoryStore+"/replay_decision", runtimeTradeMemoryChildInput{Query: request.Query}, runtimeTradeMemoryReplayReadPurpose)
	if err != nil {
		return runtimeDataRecallResult{}, err
	}
	response, err := client.ReadReplayDecision(childCtx, request.Query)
	if err != nil {
		return runtimeDataRecallResult{}, err
	}
	if err := response.Validate(childID, childID); err != nil {
		return runtimeDataRecallResult{}, fmt.Errorf("validate TRADE replay decision response: %w", err)
	}
	if response.Record.DecisionID != request.Query {
		return runtimeDataRecallResult{}, fmt.Errorf("TRADE replay decision response ID does not match the exact query")
	}
	return newRuntimeDataRecallResult(request.Store, request.Operation, []map[string]any{runtimeTradeMemoryReplayProjection(response.Record, response.OwnerEvidence)}), nil
}

func runtimeTradeMemoryRecallPortfolio(ctx context.Context, request tools.DataRecallRequest, client runtimeTradeMemoryClient) (runtimeDataRecallResult, error) {
	outer, err := runtimeTradeMemoryOuterScope(ctx)
	if err != nil {
		return runtimeDataRecallResult{}, err
	}
	auditRef := ""
	if request.Query == "current" {
		// current is the independent read form; no response hash binding is required.
	} else if runtimeTradeMemoryAuditRefPattern.MatchString(request.Query) {
		auditRef = request.Query
	} else {
		return runtimeDataRecallResult{}, fmt.Errorf("portfolio snapshot query must be current or an exact sha256 audit_ref")
	}
	childCtx, childID, err := runtimeTradeMemoryChildScope(ctx, outer, runtimeTradeMemoryStore+"/portfolio_snapshot", runtimeTradeMemoryChildInput{Query: request.Query}, runtimeTradeMemoryPortfolioReadPurpose)
	if err != nil {
		return runtimeDataRecallResult{}, err
	}
	response, err := client.ReadPortfolioSnapshot(childCtx)
	if err != nil {
		return runtimeDataRecallResult{}, err
	}
	if err := response.Validate(childID, childID); err != nil {
		return runtimeDataRecallResult{}, fmt.Errorf("validate TRADE portfolio snapshot response: %w", err)
	}
	if auditRef != "" {
		if response.Snapshot.LatestEventHash != auditRef {
			return runtimeDataRecallResult{}, fmt.Errorf("TRADE portfolio snapshot hash does not match the exact audit_ref")
		}
		if response.OwnerEvidence.ProvenanceRef != auditRef {
			return runtimeDataRecallResult{}, fmt.Errorf("TRADE portfolio snapshot provenance does not match the exact audit_ref")
		}
	}
	return newRuntimeDataRecallResult(request.Store, request.Operation, []map[string]any{runtimeTradeMemoryPortfolioProjection(response.Snapshot, response.OwnerEvidence)}), nil
}

type runtimeTradeMemorySourcePayload struct {
	SourceDefinitionID string `json:"source_definition_id"`
}

type runtimeTradeMemoryLearningPayload struct {
	CandidateDefinitionID string `json:"candidate_definition_id"`
}

type runtimeTradeMemoryMarketPayload struct {
	RunID        string `json:"run_id"`
	InstrumentID string `json:"instrument_id"`
	TradeDate    string `json:"trade_date"`
}

type runtimeTradeMemoryReplayPayload struct {
	RunID        string `json:"run_id"`
	InstrumentID string `json:"instrument_id"`
	TradeDate    string `json:"trade_date"`
	Action       string `json:"action"`
}

func runtimeTradeMemoryWriteSource(ctx context.Context, request tools.DataWriteRequest, client runtimeTradeMemoryClient) (runtimeDataWriteOwnerResult, error) {
	outer, err := runtimeTradeMemoryOuterScope(ctx)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if err := validateRuntimeDataWritePayloadKeys(request.Payload, map[string]struct{}{"source_definition_id": {}}); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	var payload runtimeTradeMemorySourcePayload
	if err := decodeRuntimeDataWritePayload(request.Payload, &payload); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	childInput := runtimeTradeMemoryChildInput{Payload: runtimeTradeMemoryPayloadJSON(request.Payload)}
	childCtx, childID, err := runtimeTradeMemoryChildScope(ctx, outer, runtimeTradeMemoryStore+"/collect_source", childInput, runtimeTradeMemorySourceWritePurpose)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if err := (moduletrade.CollectSourceRequest{ContractVersion: moduletrade.MemoryOwnerContractVersion, RequestID: childID, SourceDefinitionID: payload.SourceDefinitionID}).Validate(); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	response, err := client.CollectSource(childCtx, payload.SourceDefinitionID)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if err := response.Validate(childID); err != nil {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("validate TRADE source collect response: %w", err)
	}
	if response.Record.SourceDefinitionID != payload.SourceDefinitionID {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("TRADE source collect response definition ID does not match the exact request")
	}
	return runtimeTradeMemoryOwnerResult(response.OwnerReceipt), nil
}

func runtimeTradeMemoryWriteLearning(ctx context.Context, request tools.DataWriteRequest, client runtimeTradeMemoryClient) (runtimeDataWriteOwnerResult, error) {
	outer, err := runtimeTradeMemoryOuterScope(ctx)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if err := validateRuntimeDataWritePayloadKeys(request.Payload, map[string]struct{}{"candidate_definition_id": {}}); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	var payload runtimeTradeMemoryLearningPayload
	if err := decodeRuntimeDataWritePayload(request.Payload, &payload); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	childCtx, childID, err := runtimeTradeMemoryChildScope(ctx, outer, runtimeTradeMemoryStore+"/import_learning_candidate", runtimeTradeMemoryChildInput{Payload: runtimeTradeMemoryPayloadJSON(request.Payload)}, runtimeTradeMemoryLearningWritePurpose)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if err := (moduletrade.ImportLearningCandidateRequest{ContractVersion: moduletrade.MemoryOwnerContractVersion, RequestID: childID, CandidateDefinitionID: payload.CandidateDefinitionID}).Validate(); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	response, err := client.ImportLearningCandidate(childCtx, payload.CandidateDefinitionID)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if err := response.Validate(childID); err != nil {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("validate TRADE learning candidate import response: %w", err)
	}
	if response.Record.CandidateDefinitionID != payload.CandidateDefinitionID {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("TRADE learning candidate import response definition ID does not match the exact request")
	}
	return runtimeTradeMemoryOwnerResult(response.OwnerReceipt), nil
}

func runtimeTradeMemoryWriteMarket(ctx context.Context, request tools.DataWriteRequest, client runtimeTradeMemoryClient) (runtimeDataWriteOwnerResult, error) {
	outer, err := runtimeTradeMemoryOuterScope(ctx)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if err := validateRuntimeDataWritePayloadKeys(request.Payload, map[string]struct{}{"run_id": {}, "instrument_id": {}, "trade_date": {}}); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	var payload runtimeTradeMemoryMarketPayload
	if err := decodeRuntimeDataWritePayload(request.Payload, &payload); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	childCtx, childID, err := runtimeTradeMemoryChildScope(ctx, outer, runtimeTradeMemoryStore+"/import_market_snapshot", runtimeTradeMemoryChildInput{Payload: runtimeTradeMemoryPayloadJSON(request.Payload)}, runtimeTradeMemoryMarketWritePurpose)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	input := moduletrade.ImportMarketSnapshotRequest{ContractVersion: moduletrade.MemoryOwnerContractVersion, RequestID: childID, RunID: payload.RunID, InstrumentID: payload.InstrumentID, TradeDate: payload.TradeDate}
	if err := input.Validate(); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	response, err := client.ImportMarketSnapshot(childCtx, payload.RunID, payload.InstrumentID, payload.TradeDate)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if err := response.Validate(childID); err != nil {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("validate TRADE market snapshot import response: %w", err)
	}
	if response.Record.RunID != payload.RunID || response.Record.InstrumentID != payload.InstrumentID || response.Record.TradeDate != payload.TradeDate {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("TRADE market snapshot import response observation does not match the exact request")
	}
	return runtimeTradeMemoryOwnerResult(response.OwnerReceipt), nil
}

func runtimeTradeMemoryWriteReplay(ctx context.Context, request tools.DataWriteRequest, client runtimeTradeMemoryClient) (runtimeDataWriteOwnerResult, error) {
	outer, err := runtimeTradeMemoryOuterScope(ctx)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if err := validateRuntimeDataWritePayloadKeys(request.Payload, map[string]struct{}{"run_id": {}, "instrument_id": {}, "trade_date": {}, "action": {}}); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	var payload runtimeTradeMemoryReplayPayload
	if err := decodeRuntimeDataWritePayload(request.Payload, &payload); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	childCtx, childID, err := runtimeTradeMemoryChildScope(ctx, outer, runtimeTradeMemoryStore+"/record_replay_decision", runtimeTradeMemoryChildInput{Payload: runtimeTradeMemoryPayloadJSON(request.Payload)}, runtimeTradeMemoryReplayWritePurpose)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	input := moduletrade.RecordReplayDecisionRequest{ContractVersion: moduletrade.MemoryOwnerContractVersion, RequestID: childID, RunID: payload.RunID, InstrumentID: payload.InstrumentID, TradeDate: payload.TradeDate, Action: payload.Action}
	if err := input.Validate(); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	response, err := client.RecordReplayDecision(childCtx, payload.RunID, payload.InstrumentID, payload.TradeDate, payload.Action)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if err := response.Validate(childID); err != nil {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("validate TRADE replay decision response: %w", err)
	}
	if response.Record.RunID != payload.RunID || response.Record.InstrumentID != payload.InstrumentID || response.Record.TradeDate != payload.TradeDate || response.Record.Action != payload.Action {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("TRADE replay decision response observation does not match the exact request")
	}
	return runtimeTradeMemoryOwnerResult(response.OwnerReceipt), nil
}

func runtimeTradeMemoryWritePortfolio(ctx context.Context, request tools.DataWriteRequest, client runtimeTradeMemoryClient) (runtimeDataWriteOwnerResult, error) {
	outer, err := runtimeTradeMemoryOuterScope(ctx)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if request.Payload == nil || len(request.Payload) != 0 {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("portfolio initialization payload must be exactly an empty object")
	}
	childCtx, childID, err := runtimeTradeMemoryChildScope(ctx, outer, runtimeTradeMemoryStore+"/ensure_portfolio_initialized", runtimeTradeMemoryChildInput{Payload: runtimeTradeMemoryPayloadJSON(request.Payload)}, runtimeTradeMemoryPortfolioWritePurpose)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	response, err := client.EnsurePortfolioInitialized(childCtx)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if err := response.Validate(childID); err != nil {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("validate TRADE portfolio initialization response: %w", err)
	}
	return runtimeTradeMemoryOwnerResult(response.OwnerReceipt), nil
}

func runtimeTradeMemoryOuterScope(ctx context.Context) (domaintool.ToolExecutionScope, error) {
	scope, found := domaintool.ToolExecutionScopeFromContext(ctx)
	if !found {
		return domaintool.ToolExecutionScope{}, fmt.Errorf("trusted Agent scope is missing")
	}
	if err := scope.Validate(); err != nil {
		return domaintool.ToolExecutionScope{}, fmt.Errorf("trusted Agent scope is invalid: %w", err)
	}
	if scope.ActorKind != domaintool.ActorKindAgent || !scope.Allows(domaintool.DataScopeInternal) {
		return domaintool.ToolExecutionScope{}, fmt.Errorf("TRADE memory requires internal Agent scope")
	}
	if scope.ActorID != "shiro" || scope.AgentRole != "worker" {
		return domaintool.ToolExecutionScope{}, fmt.Errorf("TRADE memory Agent and role are not permitted")
	}
	return scope, nil
}

func runtimeTradeMemoryChildScope(ctx context.Context, outer domaintool.ToolExecutionScope, route string, input runtimeTradeMemoryChildInput, purpose string) (context.Context, string, error) {
	canonical, err := json.Marshal(runtimeTradeMemoryChildTuple{ParentRequestID: outer.RequestID, Route: route, Input: input})
	if err != nil {
		return nil, "", fmt.Errorf("canonicalize TRADE memory child request: %w", err)
	}
	digest := sha256.Sum256(canonical)
	childID := "trade-memory-" + strings.ToLower(hex.EncodeToString(digest[:]))
	if len(childID) > 128 {
		return nil, "", fmt.Errorf("TRADE memory child request ID exceeds 128 bytes")
	}
	childCtx, err := domaintool.DeriveAgentToolExecutionScope(ctx, childID, outer.ActorID, outer.AgentRole, purpose, true)
	if err != nil {
		return nil, "", fmt.Errorf("derive TRADE memory child scope: %w", err)
	}
	return childCtx, childID, nil
}

func runtimeTradeMemoryPayloadJSON(payload map[string]any) json.RawMessage {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return encoded
}

func runtimeTradeMemoryValidateID(value, name string) error {
	if !runtimeTradeMemoryIDPattern.MatchString(value) || strings.ContainsAny(value, `/\\`) {
		return fmt.Errorf("TRADE memory %s is invalid", name)
	}
	return nil
}

func runtimeTradeMemoryOwnerResult(receipt moduletrade.OwnerReceipt) runtimeDataWriteOwnerResult {
	return runtimeDataWriteOwnerResult{
		SchemaVersion:    strconv.Itoa(receipt.SchemaVersion),
		MigrationState:   receipt.MigrationState,
		ValidationState:  receipt.ValidationState,
		AuditRef:         receipt.AuditRef,
		IdempotencyKey:   receipt.RequestID,
		IdempotentReplay: receipt.IdempotentReplay,
		PolicyRevision:   receipt.PolicyRevision,
	}
}

func runtimeTradeMemorySourceProjection(record moduletrade.SourceRecord, evidence moduletrade.OwnerEvidence) map[string]any {
	return map[string]any{
		"record_version": record.RecordVersion, "source_record_id": record.SourceRecordID, "capture_nonce": record.CaptureNonce,
		"source_definition_id": record.SourceDefinitionID, "source_definition_hash": record.SourceDefinitionHash,
		"title": record.Title, "publisher": record.Publisher, "jurisdiction": record.Jurisdiction, "category": record.Category,
		"language": record.Language, "source_url": record.SourceURL, "final_url": record.FinalURL, "terms_reference": record.TermsReference,
		"license_status": record.LicenseStatus, "status": record.Status, "observed_at": record.ObservedAt,
		"point_in_time_available_at": record.PointInTimeAvailableAt, "http_status": record.HTTPStatus, "media_type": record.MediaType,
		"content_encoding": record.ContentEncoding, "etag": record.ETag, "last_modified": record.LastModified,
		"byte_size": record.ByteSize, "content_hash": record.ContentHash, "tags": append([]string(nil), record.Tags...),
		"owner_evidence": evidence,
	}
}

func runtimeTradeMemoryLearningProjection(record moduletrade.LearningCandidateRecord, evidence moduletrade.OwnerEvidence) map[string]any {
	return map[string]any{
		"record_version": record.RecordVersion, "candidate_record_id": record.CandidateRecordID,
		"candidate_definition_id": record.CandidateDefinitionID, "status": record.Status, "title": record.Title,
		"statement": record.Statement, "bound_sources": append([]moduletrade.BoundSource(nil), record.BoundSources...),
		"applicability": append([]string(nil), record.Applicability...), "limitations": append([]string(nil), record.Limitations...),
		"invalidation_conditions": append([]string(nil), record.InvalidationConditions...), "tags": append([]string(nil), record.Tags...),
		"content_hash": record.ContentHash, "owner_evidence": evidence,
	}
}

func runtimeTradeMemoryMarketProjection(record moduletrade.MarketSnapshot, evidence moduletrade.OwnerEvidence) map[string]any {
	return map[string]any{
		"snapshot_id": record.SnapshotID, "schema_version": record.SchemaVersion, "instrument_id": record.InstrumentID,
		"symbol": record.Symbol, "name": record.Name, "asset_type": record.AssetType, "venue": record.Venue,
		"currency": record.Currency, "trade_date": record.TradeDate, "available_at": record.AvailableAt,
		"open": record.Open, "high": record.High, "low": record.Low, "close": record.Close, "adj_close": record.AdjClose,
		"volume": record.Volume, "source_name": record.SourceName, "run_id": record.RunID, "plan_id": record.PlanID,
		"plan_hash": record.PlanHash, "dataset_id": record.DatasetID, "dataset_hash": record.DatasetHash,
		"dataset_source_ref": record.DatasetSourceRef, "code_revision": record.CodeRevision, "content_hash": record.ContentHash,
		"owner_evidence": evidence,
	}
}

func runtimeTradeMemoryReplayProjection(record moduletrade.ReplayDecision, evidence moduletrade.OwnerEvidence) map[string]any {
	return map[string]any{
		"decision_id": record.DecisionID, "schema_version": record.SchemaVersion, "snapshot_id": record.SnapshotID,
		"run_id": record.RunID, "instrument_id": record.InstrumentID, "trade_date": record.TradeDate,
		"action": record.Action, "content_hash": record.ContentHash, "owner_evidence": evidence,
	}
}

func runtimeTradeMemoryPortfolioProjection(snapshot moduletrade.PortfolioSnapshot, evidence moduletrade.OwnerEvidence) map[string]any {
	return map[string]any{
		"schema_version": snapshot.SchemaVersion, "portfolio_id": snapshot.PortfolioID, "mode": snapshot.Mode,
		"guaranteed": snapshot.Guaranteed, "initial_cash_jpy": snapshot.InitialCashJPY, "cash_jpy": snapshot.CashJPY,
		"realized_pnl_jpy": snapshot.RealizedPnLJPY, "unrealized_pnl_jpy": snapshot.UnrealizedPnLJPY,
		"nav_jpy": snapshot.NAVJPY, "valuation_status": snapshot.ValuationStatus, "positions": append([]moduletrade.PortfolioPosition(nil), snapshot.Positions...),
		"event_count": snapshot.EventCount, "latest_event_hash": snapshot.LatestEventHash, "owner_evidence": evidence,
	}
}
