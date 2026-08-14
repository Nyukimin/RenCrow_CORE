package main

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	tradeshadowobservation "github.com/Nyukimin/RenCrow_CORE/internal/application/tradeshadowobservation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	tradeclient "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tradeclient"
	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

const (
	runtimeTradeLedgerOutcomeReportOperation = "ledger_outcome_report"
	runtimeTradeLedgerObservationOperation   = "record_shadow_observation"
	runtimeTradeLedgerReadPurpose            = "ledger_memory_read"
	runtimeTradeLedgerWritePurpose           = "ledger_memory_write"
	runtimeTradeLedgerOutcomeLabelContract   = "a09f0619c4c02abaf496a3674a54549f14a6f4b6454e3d55c8793707803cbbfa"
)

var runtimeTradeLedgerOutcomeEventRefPattern = regexp.MustCompile(`^shadow-event/sha256:[0-9a-f]{64}$`)

// runtimeTradeLedgerClient is the narrow CORE boundary for owner-verified
// replay/market observations and the read-only Shadow outcome report.
type runtimeTradeLedgerClient interface {
	ReadReplayDecision(context.Context, string) (moduletrade.ReplayDecisionReadResponse, error)
	ReadMarketSnapshot(context.Context, string) (moduletrade.MarketSnapshotReadResponse, error)
	ShadowOutcomeReport(context.Context, string, string) (moduletrade.PrivateShadowOutcomeReport, error)
}

// runtimeTradeLedgerRecorder is implemented by the existing CORE Shadow
// observation service. Policy evaluation and module recording stay behind it.
type runtimeTradeLedgerRecorder interface {
	Record(context.Context, tradeshadowobservation.Request) (tradeshadowobservation.Result, error)
}

var _ runtimeTradeLedgerClient = (*tradeclient.Client)(nil)
var _ runtimeTradeLedgerRecorder = (*tradeshadowobservation.Service)(nil)

func registerRuntimeDataRecallTradeLedger(r *runtimeDataRecallRegistry, client runtimeTradeLedgerClient) error {
	if r == nil || client == nil {
		return fmt.Errorf("TRADE ledger recall unavailable")
	}
	return r.Register(runtimeTradeMemoryStore, runtimeTradeLedgerOutcomeReportOperation, dataRecallAccessInternal, func(ctx context.Context, request tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		return runtimeTradeLedgerRecallOutcomeReport(ctx, request, client)
	})
}

func registerRuntimeDataWriteTradeLedger(r *runtimeDataWriteRegistry, recorder runtimeTradeLedgerRecorder, client runtimeTradeLedgerClient) error {
	if r == nil || recorder == nil || client == nil {
		return fmt.Errorf("TRADE ledger write unavailable")
	}
	return r.RegisterWithContract(runtimeTradeMemoryStore, runtimeTradeLedgerObservationOperation, dataRecallAccessInternal, runtimeDataWriteContract{
		RequiredPayloadFields: []string{"decision_id", "study_id"},
	}, func(ctx context.Context, request tools.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
		return runtimeTradeLedgerWriteObservation(ctx, request, recorder, client)
	})
}

func runtimeTradeLedgerRecallOutcomeReport(ctx context.Context, request tools.DataRecallRequest, client runtimeTradeLedgerClient) (runtimeDataRecallResult, error) {
	outer, err := runtimeTradeMemoryOuterScope(ctx)
	if err != nil {
		return runtimeDataRecallResult{}, err
	}
	validationStudyID := request.Query
	eventRef := ""
	if runtimeTradeLedgerOutcomeEventRefPattern.MatchString(request.Query) {
		eventRef = request.Query
	} else if err := runtimeTradeMemoryValidateID(request.Query, "Shadow outcome report query"); err != nil {
		return runtimeDataRecallResult{}, err
	}
	childCtx, childID, err := runtimeTradeMemoryChildScope(ctx, outer, runtimeTradeMemoryStore+"/"+runtimeTradeLedgerOutcomeReportOperation, runtimeTradeMemoryChildInput{Query: request.Query}, runtimeTradeLedgerReadPurpose)
	if err != nil {
		return runtimeDataRecallResult{}, err
	}
	response, err := client.ShadowOutcomeReport(childCtx, childID, request.Query)
	if err != nil {
		return runtimeDataRecallResult{}, err
	}
	if eventRef != "" {
		if err := runtimeTradeMemoryValidateID(response.Report.StudyID, "Shadow outcome report study ID"); err != nil {
			return runtimeDataRecallResult{}, err
		}
		validationStudyID = response.Report.StudyID
	}
	if err := response.Validate(validationStudyID, childID); err != nil {
		return runtimeDataRecallResult{}, fmt.Errorf("validate TRADE Shadow outcome report response: %w", err)
	}
	if eventRef != "" && response.OwnerEvidence.ProvenanceRef != eventRef {
		return runtimeDataRecallResult{}, fmt.Errorf("TRADE Shadow outcome report provenance does not match the exact event query")
	}
	return newRuntimeDataRecallResult(request.Store, request.Operation, []map[string]any{runtimeTradeLedgerOutcomeReportProjection(response.Report, response.OwnerEvidence)}), nil
}

type runtimeTradeLedgerObservationPayload struct {
	StudyID    string `json:"study_id"`
	DecisionID string `json:"decision_id"`
}

func runtimeTradeLedgerWriteObservation(ctx context.Context, request tools.DataWriteRequest, recorder runtimeTradeLedgerRecorder, client runtimeTradeLedgerClient) (runtimeDataWriteOwnerResult, error) {
	outer, err := runtimeTradeMemoryOuterScope(ctx)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if err := validateRuntimeDataWritePayloadKeys(request.Payload, map[string]struct{}{"study_id": {}, "decision_id": {}}); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	var payload runtimeTradeLedgerObservationPayload
	if err := decodeRuntimeDataWritePayload(request.Payload, &payload); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if err := runtimeTradeMemoryValidateID(payload.StudyID, "Shadow observation study ID"); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if err := runtimeTradeMemoryValidateID(payload.DecisionID, "Shadow observation decision ID"); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}

	replayCtx, replayChildID, err := runtimeTradeMemoryChildScope(ctx, outer, runtimeTradeMemoryStore+"/replay_decision", runtimeTradeMemoryChildInput{Query: payload.DecisionID}, runtimeTradeMemoryReplayReadPurpose)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	replayResponse, err := client.ReadReplayDecision(replayCtx, payload.DecisionID)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if err := replayResponse.Validate(replayChildID, replayChildID); err != nil {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("validate owner replay decision for Shadow observation: %w", err)
	}
	decision := replayResponse.Record
	if decision.DecisionID != payload.DecisionID {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("owner replay decision ID does not match the exact request")
	}

	marketCtx, marketChildID, err := runtimeTradeMemoryChildScope(ctx, outer, runtimeTradeMemoryStore+"/market_snapshot", runtimeTradeMemoryChildInput{Query: decision.SnapshotID}, runtimeTradeMemoryMarketReadPurpose)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	marketResponse, err := client.ReadMarketSnapshot(marketCtx, decision.SnapshotID)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if err := marketResponse.Validate(marketChildID, marketChildID); err != nil {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("validate owner market snapshot for Shadow observation: %w", err)
	}
	snapshot := marketResponse.Record
	if snapshot.SnapshotID != decision.SnapshotID || snapshot.RunID != decision.RunID || snapshot.InstrumentID != decision.InstrumentID || snapshot.TradeDate != decision.TradeDate {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("owner replay decision and market snapshot are not cross-bound")
	}

	decisionKind, ok := runtimeTradeLedgerDecisionKind(decision.Action)
	if !ok {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("owner replay decision action is not supported for Shadow observation")
	}
	contextSnapshotSHA256, err := runtimeTradeLedgerContextHash(decision.ContentHash)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	writeCtx, writeChildID, err := runtimeTradeMemoryChildScope(ctx, outer, runtimeTradeMemoryStore+"/"+runtimeTradeLedgerObservationOperation, runtimeTradeMemoryChildInput{Payload: runtimeTradeMemoryPayloadJSON(request.Payload)}, runtimeTradeLedgerWritePurpose)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	observation := moduletrade.ShadowObservationInput{
		IdempotencyKey:             writeChildID,
		StudyID:                    payload.StudyID,
		DecisionID:                 decision.DecisionID,
		ActorID:                    outer.ActorID,
		InstrumentID:               decision.InstrumentID,
		DecisionKind:               decisionKind,
		MarketObservedAt:           snapshot.AvailableAt,
		ContextSnapshotSHA256:      contextSnapshotSHA256,
		OutcomeLabelContractSHA256: runtimeTradeLedgerOutcomeLabelContract,
		ReasonCodes:                []string{"REPLAY_DECISION_OWNER_VERIFIED", "MARKET_SNAPSHOT_OWNER_VERIFIED"},
		EvidenceRefs:               []string{decision.DecisionID, snapshot.SnapshotID},
	}
	if err := observation.Validate(); err != nil {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("validate Shadow observation derived from owner data: %w", err)
	}
	result, err := recorder.Record(writeCtx, tradeshadowobservation.Request{
		RequestID:      writeChildID,
		TraceID:        outer.RequestID,
		Requester:      outer.ActorID,
		RequestAllowed: true,
		Observation:    observation,
	})
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if result.Record == nil {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("TRADE Shadow observation contract result is missing a record")
	}
	record := result.Record
	if result.AuthorizesExternalExecution || result.PolicyDecision.Status != "allowed" || record.ContractVersion != moduletrade.PrivateContractVersion || record.CorrelationID != writeChildID || record.RequestID != writeChildID || record.Environment != "SHADOW" || record.AuthorizesExternalExecution || record.PortfolioMutated || record.KnowledgePromoted || !reflect.DeepEqual(record.Event.ShadowObservationInput, observation) || record.OwnerReceipt.RequestID != writeChildID {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("TRADE Shadow observation contract result is not a successful non-executing owner record")
	}
	return runtimeTradeMemoryOwnerResult(record.OwnerReceipt), nil
}

func runtimeTradeLedgerDecisionKind(action string) (string, bool) {
	switch action {
	case moduletrade.MemoryActionSelect:
		return "select", true
	case moduletrade.MemoryActionAvoid:
		return "exclude", true
	case moduletrade.MemoryActionObserve:
		return "abstain", true
	default:
		return "", false
	}
}

func runtimeTradeLedgerContextHash(contentHash string) (string, error) {
	const prefix = "sha256:"
	if !strings.HasPrefix(contentHash, prefix) || len(contentHash) != len(prefix)+64 {
		return "", fmt.Errorf("owner replay decision content hash must have an exact sha256 prefix")
	}
	return strings.TrimPrefix(contentHash, prefix), nil
}

func runtimeTradeLedgerOutcomeReportProjection(report moduletrade.ShadowOutcomeReport, evidence moduletrade.OwnerEvidence) map[string]any {
	return map[string]any{
		"schema_version": report.SchemaVersion, "contract_version": report.ContractVersion, "study_id": report.StudyID,
		"environment": report.Environment, "observation_count": report.ObservationCount, "outcome_count": report.OutcomeCount,
		"pending_outcome_count": report.PendingOutcomeCount, "label_counts": cloneRuntimeTradeLedgerCounts(report.LabelCounts),
		"return_count": report.ReturnCount, "return_sum_bps": report.ReturnSumBPS, "return_mean_bps": cloneRuntimeTradeLedgerFloat(report.ReturnMeanBPS),
		"benchmark_return_count": report.BenchmarkReturnCount, "benchmark_return_sum_bps": report.BenchmarkReturnSumBPS,
		"benchmark_return_mean_bps": cloneRuntimeTradeLedgerFloat(report.BenchmarkReturnMeanBPS), "excess_return_count": report.ExcessReturnCount,
		"excess_return_sum_bps": report.ExcessReturnSumBPS, "excess_return_mean_bps": cloneRuntimeTradeLedgerFloat(report.ExcessReturnMeanBPS),
		"label_contract_sha256": append([]string(nil), report.LabelContractSHA256...), "review_state": report.ReviewState,
		"latest_event_hash": report.LatestEventHash, "authorizes_external_execution": report.AuthorizesExternalExecution,
		"portfolio_mutated": report.PortfolioMutated, "knowledge_promoted": report.KnowledgePromoted, "owner_evidence": evidence,
	}
}

func cloneRuntimeTradeLedgerCounts(values map[string]int64) map[string]int64 {
	if values == nil {
		return nil
	}
	copyValues := make(map[string]int64, len(values))
	for key, value := range values {
		copyValues[key] = value
	}
	return copyValues
}

func cloneRuntimeTradeLedgerFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}
