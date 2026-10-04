package revenue

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	domainrevenue "github.com/Nyukimin/RenCrow_CORE/internal/domain/revenue"
)

const (
	RevenueSaveMarketResearchItemStorageHostOperation = "save_market_research_item"
	RevenueSaveSNSPostMetricStorageHostOperation      = "save_sns_post_metric"
	RevenueSaveProductStorageHostOperation            = "save_product"
	RevenueSaveCustomerVoiceStorageHostOperation      = "save_customer_voice"
	RevenueSaveEventStorageHostOperation              = "save_revenue_event"
	RevenueSaveEconomicTaskStorageHostOperation       = "save_economic_task"
	RevenueSaveEconomicReflectionStorageHostOperation = "save_economic_reflection"
	RevenueSavePolicyDecisionStorageHostOperation     = "save_policy_decision_record"
	RevenueSaveChannelDraftStorageHostOperation       = "save_channel_draft"
	RevenueSaveExternalSendApplyStorageHostOperation  = "save_external_send_apply_record"
	RevenueSaveDeliveryStorageHostOperation           = "save_delivery"
	RevenueSaveOpportunityStorageHostOperation        = "save_opportunity"
	RevenueSaveDailyRoutineStorageHostOperation       = "save_daily_routine_report"
	revenueStorageHostReceiptTable                    = "revenue_storagehost_operation_receipt"
)

var revenueStorageHostOpIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type RevenueStorageHostOperationIdentity struct {
	OpID             string
	Operation        string
	PayloadSHA256    string
	WriterGeneration int64
}

func (identity RevenueStorageHostOperationIdentity) validate() error {
	if !revenueStorageHostOpIDPattern.MatchString(identity.OpID) || len(identity.PayloadSHA256) != sha256.Size*2 || identity.WriterGeneration <= 0 {
		return errors.New("revenue storage-host operation identity is incomplete")
	}
	if !revenueStorageHostOperationAllowed(identity.Operation) {
		return errors.New("revenue storage-host operation is unsupported")
	}
	for _, char := range identity.PayloadSHA256 {
		if !(char >= '0' && char <= '9') && !(char >= 'a' && char <= 'f') {
			return errors.New("revenue storage-host payload hash is malformed")
		}
	}
	return nil
}

func revenueStorageHostOperationAllowed(operation string) bool {
	switch operation {
	case RevenueSaveMarketResearchItemStorageHostOperation,
		RevenueSaveSNSPostMetricStorageHostOperation,
		RevenueSaveProductStorageHostOperation,
		RevenueSaveCustomerVoiceStorageHostOperation,
		RevenueSaveEventStorageHostOperation,
		RevenueSaveOpportunityStorageHostOperation,
		RevenueSaveEconomicTaskStorageHostOperation,
		RevenueSaveEconomicReflectionStorageHostOperation,
		RevenueSavePolicyDecisionStorageHostOperation,
		RevenueSaveDailyRoutineStorageHostOperation,
		RevenueSaveChannelDraftStorageHostOperation,
		RevenueSaveExternalSendApplyStorageHostOperation,
		RevenueSaveDeliveryStorageHostOperation:
		return true
	default:
		return false
	}
}

func saveRevenueStorageHostRecord[T any](s *SQLiteStore, ctx context.Context, identity RevenueStorageHostOperationIdentity, table, idColumn, effectID, createdAt string, item T) (T, error) {
	var zero T
	encoded, err := s.saveRevenueStorageHostMutation(ctx, identity, table, idColumn, effectID, createdAt, item)
	if err != nil {
		return zero, err
	}
	var stored T
	if err := json.Unmarshal(encoded, &stored); err != nil {
		return zero, errors.New("revenue storage-host owner result is malformed")
	}
	return stored, nil
}

func (s *SQLiteStore) SaveMarketResearchItemForStorageHostOperation(ctx context.Context, identity RevenueStorageHostOperationIdentity, item domainrevenue.MarketResearchItem) (domainrevenue.MarketResearchItem, error) {
	if err := domainrevenue.ValidateMarketResearchItem(item); err != nil {
		return domainrevenue.MarketResearchItem{}, err
	}
	return saveRevenueStorageHostRecord(s, ctx, identity, "market_research_item", "item_id", item.ItemID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) LookupRevenueMarketResearchItemStorageHostReceipt(ctx context.Context, identity RevenueStorageHostOperationIdentity, expected domainrevenue.MarketResearchItem) (json.RawMessage, bool, error) {
	if err := domainrevenue.ValidateMarketResearchItem(expected); err != nil {
		return nil, false, err
	}
	return s.lookupRevenueStorageHostMutation(ctx, identity, "market_research_item", "item_id", expected.ItemID, expected)
}

func (s *SQLiteStore) SaveSNSPostMetricForStorageHostOperation(ctx context.Context, identity RevenueStorageHostOperationIdentity, item domainrevenue.SNSPostMetric) (domainrevenue.SNSPostMetric, error) {
	if err := domainrevenue.ValidateSNSPostMetric(item); err != nil {
		return domainrevenue.SNSPostMetric{}, err
	}
	return saveRevenueStorageHostRecord(s, ctx, identity, "sns_post_metric", "post_id", item.PostID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) LookupRevenueSNSPostMetricStorageHostReceipt(ctx context.Context, identity RevenueStorageHostOperationIdentity, expected domainrevenue.SNSPostMetric) (json.RawMessage, bool, error) {
	if err := domainrevenue.ValidateSNSPostMetric(expected); err != nil {
		return nil, false, err
	}
	return s.lookupRevenueStorageHostMutation(ctx, identity, "sns_post_metric", "post_id", expected.PostID, expected)
}

func (s *SQLiteStore) SaveProductForStorageHostOperation(ctx context.Context, identity RevenueStorageHostOperationIdentity, item domainrevenue.Product) (domainrevenue.Product, error) {
	if err := domainrevenue.ValidateProduct(item); err != nil {
		return domainrevenue.Product{}, err
	}
	return saveRevenueStorageHostRecord(s, ctx, identity, "product_catalog", "product_id", item.ProductID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) LookupRevenueProductStorageHostReceipt(ctx context.Context, identity RevenueStorageHostOperationIdentity, expected domainrevenue.Product) (json.RawMessage, bool, error) {
	if err := domainrevenue.ValidateProduct(expected); err != nil {
		return nil, false, err
	}
	return s.lookupRevenueStorageHostMutation(ctx, identity, "product_catalog", "product_id", expected.ProductID, expected)
}

func (s *SQLiteStore) SaveCustomerVoiceForStorageHostOperation(ctx context.Context, identity RevenueStorageHostOperationIdentity, item domainrevenue.CustomerVoice) (domainrevenue.CustomerVoice, error) {
	if err := domainrevenue.ValidateCustomerVoice(item); err != nil {
		return domainrevenue.CustomerVoice{}, err
	}
	return saveRevenueStorageHostRecord(s, ctx, identity, "customer_voice", "voice_id", item.VoiceID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) LookupRevenueCustomerVoiceStorageHostReceipt(ctx context.Context, identity RevenueStorageHostOperationIdentity, expected domainrevenue.CustomerVoice) (json.RawMessage, bool, error) {
	if err := domainrevenue.ValidateCustomerVoice(expected); err != nil {
		return nil, false, err
	}
	return s.lookupRevenueStorageHostMutation(ctx, identity, "customer_voice", "voice_id", expected.VoiceID, expected)
}

func (s *SQLiteStore) SaveRevenueEventForStorageHostOperation(ctx context.Context, identity RevenueStorageHostOperationIdentity, item domainrevenue.RevenueEvent) (domainrevenue.RevenueEvent, error) {
	if err := domainrevenue.ValidateRevenueEvent(item); err != nil {
		return domainrevenue.RevenueEvent{}, err
	}
	return saveRevenueStorageHostRecord(s, ctx, identity, "revenue_event", "event_id", item.EventID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) LookupRevenueEventStorageHostReceipt(ctx context.Context, identity RevenueStorageHostOperationIdentity, expected domainrevenue.RevenueEvent) (json.RawMessage, bool, error) {
	if err := domainrevenue.ValidateRevenueEvent(expected); err != nil {
		return nil, false, err
	}
	return s.lookupRevenueStorageHostMutation(ctx, identity, "revenue_event", "event_id", expected.EventID, expected)
}

func (s *SQLiteStore) SaveEconomicTaskForStorageHostOperation(ctx context.Context, identity RevenueStorageHostOperationIdentity, item domainrevenue.EconomicTask) (domainrevenue.EconomicTask, error) {
	if err := domainrevenue.ValidateEconomicTask(item); err != nil {
		return domainrevenue.EconomicTask{}, err
	}
	return saveRevenueStorageHostRecord(s, ctx, identity, "economic_task", "task_id", item.TaskID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) LookupRevenueEconomicTaskStorageHostReceipt(ctx context.Context, identity RevenueStorageHostOperationIdentity, expected domainrevenue.EconomicTask) (json.RawMessage, bool, error) {
	if err := domainrevenue.ValidateEconomicTask(expected); err != nil {
		return nil, false, err
	}
	return s.lookupRevenueStorageHostMutation(ctx, identity, "economic_task", "task_id", expected.TaskID, expected)
}

func (s *SQLiteStore) SaveEconomicReflectionForStorageHostOperation(ctx context.Context, identity RevenueStorageHostOperationIdentity, item domainrevenue.EconomicReflection) (domainrevenue.EconomicReflection, error) {
	if err := domainrevenue.ValidateEconomicReflection(item); err != nil {
		return domainrevenue.EconomicReflection{}, err
	}
	return saveRevenueStorageHostRecord(s, ctx, identity, "economic_reflection", "reflection_id", item.ReflectionID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) LookupRevenueEconomicReflectionStorageHostReceipt(ctx context.Context, identity RevenueStorageHostOperationIdentity, expected domainrevenue.EconomicReflection) (json.RawMessage, bool, error) {
	if err := domainrevenue.ValidateEconomicReflection(expected); err != nil {
		return nil, false, err
	}
	return s.lookupRevenueStorageHostMutation(ctx, identity, "economic_reflection", "reflection_id", expected.ReflectionID, expected)
}

func (s *SQLiteStore) SavePolicyDecisionRecordForStorageHostOperation(ctx context.Context, identity RevenueStorageHostOperationIdentity, item domainrevenue.PolicyDecisionRecord) (domainrevenue.PolicyDecisionRecord, error) {
	if err := domainrevenue.ValidatePolicyDecisionRecord(item); err != nil {
		return domainrevenue.PolicyDecisionRecord{}, err
	}
	return saveRevenueStorageHostRecord(s, ctx, identity, "policy_decision", "decision_id", item.DecisionID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) LookupRevenuePolicyDecisionRecordStorageHostReceipt(ctx context.Context, identity RevenueStorageHostOperationIdentity, expected domainrevenue.PolicyDecisionRecord) (json.RawMessage, bool, error) {
	if err := domainrevenue.ValidatePolicyDecisionRecord(expected); err != nil {
		return nil, false, err
	}
	return s.lookupRevenueStorageHostMutation(ctx, identity, "policy_decision", "decision_id", expected.DecisionID, expected)
}

func (s *SQLiteStore) SaveChannelDraftForStorageHostOperation(ctx context.Context, identity RevenueStorageHostOperationIdentity, item domainrevenue.ChannelDraft) (domainrevenue.ChannelDraft, error) {
	if err := domainrevenue.ValidateChannelDraft(item); err != nil {
		return domainrevenue.ChannelDraft{}, err
	}
	return saveRevenueStorageHostRecord(s, ctx, identity, "channel_draft", "artifact_id", string(item.ArtifactID), item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) LookupRevenueChannelDraftStorageHostReceipt(ctx context.Context, identity RevenueStorageHostOperationIdentity, expected domainrevenue.ChannelDraft) (json.RawMessage, bool, error) {
	if err := domainrevenue.ValidateChannelDraft(expected); err != nil {
		return nil, false, err
	}
	return s.lookupRevenueStorageHostMutation(ctx, identity, "channel_draft", "artifact_id", string(expected.ArtifactID), expected)
}

func (s *SQLiteStore) SaveExternalSendApplyRecordForStorageHostOperation(ctx context.Context, identity RevenueStorageHostOperationIdentity, item domainrevenue.ExternalSendApplyRecord) (domainrevenue.ExternalSendApplyRecord, error) {
	if err := domainrevenue.ValidateExternalSendApplyRecord(item); err != nil {
		return domainrevenue.ExternalSendApplyRecord{}, err
	}
	return saveRevenueStorageHostRecord(s, ctx, identity, "external_send_apply", "action_id", string(item.ActionID), item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) LookupRevenueExternalSendApplyRecordStorageHostReceipt(ctx context.Context, identity RevenueStorageHostOperationIdentity, expected domainrevenue.ExternalSendApplyRecord) (json.RawMessage, bool, error) {
	if err := domainrevenue.ValidateExternalSendApplyRecord(expected); err != nil {
		return nil, false, err
	}
	return s.lookupRevenueStorageHostMutation(ctx, identity, "external_send_apply", "action_id", string(expected.ActionID), expected)
}

func (s *SQLiteStore) SaveDeliveryForStorageHostOperation(ctx context.Context, identity RevenueStorageHostOperationIdentity, item domainrevenue.Delivery) (domainrevenue.Delivery, error) {
	if err := domainrevenue.ValidateDelivery(item); err != nil {
		return domainrevenue.Delivery{}, err
	}
	return saveRevenueStorageHostRecord(s, ctx, identity, "delivery", "delivery_id", item.DeliveryID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) LookupRevenueDeliveryStorageHostReceipt(ctx context.Context, identity RevenueStorageHostOperationIdentity, expected domainrevenue.Delivery) (json.RawMessage, bool, error) {
	if err := domainrevenue.ValidateDelivery(expected); err != nil {
		return nil, false, err
	}
	return s.lookupRevenueStorageHostMutation(ctx, identity, "delivery", "delivery_id", expected.DeliveryID, expected)
}

func (s *SQLiteStore) SaveOpportunityForStorageHostOperation(ctx context.Context, identity RevenueStorageHostOperationIdentity, item domainrevenue.Opportunity) (domainrevenue.Opportunity, error) {
	if operation, ok := revenueStorageHostOperationForEffect("opportunity", "opportunity_id"); !ok || identity.Operation != operation {
		return domainrevenue.Opportunity{}, errors.New("revenue storage-host operation does not match opportunity effect")
	}
	item = domainrevenue.NormalizeOpportunityEconomics(item)
	if err := domainrevenue.ValidateOpportunity(item); err != nil {
		return domainrevenue.Opportunity{}, err
	}
	encoded, err := s.saveRevenueStorageHostMutation(ctx, identity, "opportunity", "opportunity_id", item.OpportunityID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
	if err != nil {
		return domainrevenue.Opportunity{}, err
	}
	var stored domainrevenue.Opportunity
	if err := json.Unmarshal(encoded, &stored); err != nil {
		return domainrevenue.Opportunity{}, errors.New("revenue opportunity owner result is malformed")
	}
	return stored, nil
}

func (s *SQLiteStore) LookupRevenueOpportunityStorageHostReceipt(ctx context.Context, identity RevenueStorageHostOperationIdentity, expected domainrevenue.Opportunity) (json.RawMessage, bool, error) {
	if operation, ok := revenueStorageHostOperationForEffect("opportunity", "opportunity_id"); !ok || identity.Operation != operation {
		return nil, false, errors.New("revenue storage-host operation does not match opportunity receipt")
	}
	expected = domainrevenue.NormalizeOpportunityEconomics(expected)
	if err := domainrevenue.ValidateOpportunity(expected); err != nil {
		return nil, false, err
	}
	return s.lookupRevenueStorageHostMutation(ctx, identity, "opportunity", "opportunity_id", expected.OpportunityID, expected)
}

func (s *SQLiteStore) SaveDailyRoutineReportForStorageHostOperation(ctx context.Context, identity RevenueStorageHostOperationIdentity, item domainrevenue.DailyRoutineReport) (domainrevenue.DailyRoutineReport, error) {
	if operation, ok := revenueStorageHostOperationForEffect("revenue_daily_routine_report", "artifact_id"); !ok || identity.Operation != operation {
		return domainrevenue.DailyRoutineReport{}, errors.New("revenue storage-host operation does not match daily report effect")
	}
	if err := domainrevenue.ValidateDailyRoutineReport(item); err != nil {
		return domainrevenue.DailyRoutineReport{}, err
	}
	encoded, err := s.saveRevenueStorageHostMutation(ctx, identity, "revenue_daily_routine_report", "artifact_id", string(item.ArtifactID), item.CreatedAt.Format(timeFormatRFC3339Nano), item)
	if err != nil {
		return domainrevenue.DailyRoutineReport{}, err
	}
	var stored domainrevenue.DailyRoutineReport
	if err := json.Unmarshal(encoded, &stored); err != nil {
		return domainrevenue.DailyRoutineReport{}, errors.New("revenue daily routine report result is malformed")
	}
	return stored, nil
}

func (s *SQLiteStore) LookupRevenueDailyRoutineStorageHostReceipt(ctx context.Context, identity RevenueStorageHostOperationIdentity, expected domainrevenue.DailyRoutineReport) (json.RawMessage, bool, error) {
	if operation, ok := revenueStorageHostOperationForEffect("revenue_daily_routine_report", "artifact_id"); !ok || identity.Operation != operation {
		return nil, false, errors.New("revenue storage-host operation does not match daily report receipt")
	}
	if err := domainrevenue.ValidateDailyRoutineReport(expected); err != nil {
		return nil, false, err
	}
	return s.lookupRevenueStorageHostMutation(ctx, identity, "revenue_daily_routine_report", "artifact_id", string(expected.ArtifactID), expected)
}

func (s *SQLiteStore) saveRevenueStorageHostMutation(ctx context.Context, identity RevenueStorageHostOperationIdentity, table, idColumn, effectID, createdAt string, item any) (json.RawMessage, error) {
	if err := identity.validate(); err != nil {
		return nil, err
	}
	if s == nil || s.db == nil {
		return nil, errors.New("revenue sqlite store is closed")
	}
	resultJSON, err := json.Marshal(item)
	if err != nil {
		return nil, err
	}
	resultHash := fmt.Sprintf("%x", sha256.Sum256(resultJSON))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if result, found, err := lookupRevenueStorageHostReceipt(ctx, tx, identity, table, idColumn, effectID, resultJSON); err != nil {
		return nil, err
	} else if found {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return result, nil
	}
	query := fmt.Sprintf("INSERT OR REPLACE INTO %s (%s, created_at, payload) VALUES (?, ?, ?)", table, idColumn)
	if _, err := tx.ExecContext(ctx, query, effectID, createdAt, string(resultJSON)); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO `+revenueStorageHostReceiptTable+` (op_id, operation, payload_sha256, writer_generation, effect_table, effect_id, result_json, result_sha256) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, identity.OpID, identity.Operation, identity.PayloadSHA256, identity.WriterGeneration, table, effectID, string(resultJSON), resultHash); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return resultJSON, nil
}

func (s *SQLiteStore) lookupRevenueStorageHostMutation(ctx context.Context, identity RevenueStorageHostOperationIdentity, table, idColumn, effectID string, expected any) (json.RawMessage, bool, error) {
	if err := identity.validate(); err != nil {
		return nil, false, err
	}
	if operation, ok := revenueStorageHostOperationForEffect(table, idColumn); !ok || identity.Operation != operation {
		return nil, false, errors.New("revenue storage-host operation/effect binding is invalid")
	}
	if s == nil || s.db == nil {
		return nil, false, errors.New("revenue sqlite store is closed")
	}
	expectedJSON, err := json.Marshal(expected)
	if err != nil {
		return nil, false, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	result, found, err := lookupRevenueStorageHostReceipt(ctx, tx, identity, table, idColumn, effectID, expectedJSON)
	if err != nil || found {
		return result, found, err
	}
	var payload string
	query := fmt.Sprintf("SELECT payload FROM %s WHERE %s = ?", table, idColumn)
	err = tx.QueryRowContext(ctx, query, effectID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return nil, false, errors.New("revenue effect exists without its exact storage-host receipt")
}

type revenueStorageHostReceiptQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func lookupRevenueStorageHostReceipt(ctx context.Context, query revenueStorageHostReceiptQuery, identity RevenueStorageHostOperationIdentity, table, idColumn, effectID string, expectedJSON []byte) (json.RawMessage, bool, error) {
	if operation, ok := revenueStorageHostOperationForEffect(table, idColumn); !ok || identity.Operation != operation {
		return nil, false, errors.New("revenue storage-host operation/effect binding is invalid")
	}
	var operation, payloadHash, effectTable, storedEffectID, resultJSON, resultHash string
	var generation int64
	err := query.QueryRowContext(ctx, `SELECT operation, payload_sha256, writer_generation, effect_table, effect_id, result_json, result_sha256 FROM `+revenueStorageHostReceiptTable+` WHERE op_id = ?`, identity.OpID).Scan(&operation, &payloadHash, &generation, &effectTable, &storedEffectID, &resultJSON, &resultHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if operation != identity.Operation || payloadHash != identity.PayloadSHA256 || generation != identity.WriterGeneration || effectTable != table || storedEffectID != effectID {
		return nil, false, errors.New("revenue storage-host receipt binding mismatch")
	}
	if fmt.Sprintf("%x", sha256.Sum256([]byte(resultJSON))) != resultHash || !bytes.Equal(expectedJSON, []byte(resultJSON)) {
		return nil, false, errors.New("revenue storage-host result proof is malformed or substituted")
	}
	return json.RawMessage(resultJSON), true, nil
}

func revenueStorageHostOperationForEffect(table, idColumn string) (string, bool) {
	switch {
	case table == "market_research_item" && idColumn == "item_id":
		return RevenueSaveMarketResearchItemStorageHostOperation, true
	case table == "sns_post_metric" && idColumn == "post_id":
		return RevenueSaveSNSPostMetricStorageHostOperation, true
	case table == "product_catalog" && idColumn == "product_id":
		return RevenueSaveProductStorageHostOperation, true
	case table == "customer_voice" && idColumn == "voice_id":
		return RevenueSaveCustomerVoiceStorageHostOperation, true
	case table == "revenue_event" && idColumn == "event_id":
		return RevenueSaveEventStorageHostOperation, true
	case table == "opportunity" && idColumn == "opportunity_id":
		return RevenueSaveOpportunityStorageHostOperation, true
	case table == "economic_task" && idColumn == "task_id":
		return RevenueSaveEconomicTaskStorageHostOperation, true
	case table == "economic_reflection" && idColumn == "reflection_id":
		return RevenueSaveEconomicReflectionStorageHostOperation, true
	case table == "policy_decision" && idColumn == "decision_id":
		return RevenueSavePolicyDecisionStorageHostOperation, true
	case table == "revenue_daily_routine_report" && idColumn == "artifact_id":
		return RevenueSaveDailyRoutineStorageHostOperation, true
	case table == "channel_draft" && idColumn == "artifact_id":
		return RevenueSaveChannelDraftStorageHostOperation, true
	case table == "external_send_apply" && idColumn == "action_id":
		return RevenueSaveExternalSendApplyStorageHostOperation, true
	case table == "delivery" && idColumn == "delivery_id":
		return RevenueSaveDeliveryStorageHostOperation, true
	default:
		return "", false
	}
}
