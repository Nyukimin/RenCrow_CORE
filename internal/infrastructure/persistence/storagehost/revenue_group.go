package storagehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	domainrevenue "github.com/Nyukimin/RenCrow_CORE/internal/domain/revenue"
	persistrevenue "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/revenue"
)

const GroupRevenue = "revenue"

const (
	revenueListDefaultLimit = 50
	revenueListMaxLimit     = 1000
)

const (
	revenueOpListMarketResearch = "list_market_research_items"
	revenueOpListSNSMetrics     = "list_sns_post_metrics"
	revenueOpListProducts       = "list_products"
	revenueOpListCustomerVoices = "list_customer_voices"
	revenueOpListEvents         = "list_revenue_events"
	revenueOpListDecisions      = "list_policy_decision_records"
	revenueOpListDailyReports   = "list_daily_routine_reports"
	revenueOpListChannelDrafts  = "list_channel_drafts"
	revenueOpListSendRecords    = "list_external_send_apply_records"
	revenueOpListOpportunities  = "list_opportunities"
	revenueOpListEconomicTasks  = "list_economic_tasks"
	revenueOpListReflections    = "list_economic_reflections"
	revenueOpListDeliveries     = "list_deliveries"
	revenueOpSaveMarketResearch = persistrevenue.RevenueSaveMarketResearchItemStorageHostOperation
	revenueOpSaveSNSMetric      = persistrevenue.RevenueSaveSNSPostMetricStorageHostOperation
	revenueOpSaveProduct        = persistrevenue.RevenueSaveProductStorageHostOperation
	revenueOpSaveCustomerVoice  = persistrevenue.RevenueSaveCustomerVoiceStorageHostOperation
	revenueOpSaveRevenueEvent   = persistrevenue.RevenueSaveEventStorageHostOperation
	revenueOpFindOpportunity    = "find_opportunity_by_id"
	revenueOpSaveOpportunity    = persistrevenue.RevenueSaveOpportunityStorageHostOperation
	revenueOpSaveEconomicTask   = persistrevenue.RevenueSaveEconomicTaskStorageHostOperation
	revenueOpSaveReflection     = persistrevenue.RevenueSaveEconomicReflectionStorageHostOperation
	revenueOpSavePolicyDecision = persistrevenue.RevenueSavePolicyDecisionStorageHostOperation
	revenueOpSaveDailyReport    = persistrevenue.RevenueSaveDailyRoutineStorageHostOperation
	revenueOpSaveChannelDraft   = persistrevenue.RevenueSaveChannelDraftStorageHostOperation
	revenueOpSaveExternalApply  = persistrevenue.RevenueSaveExternalSendApplyStorageHostOperation
	revenueOpSaveDelivery       = persistrevenue.RevenueSaveDeliveryStorageHostOperation
)

type RevenueGroupOwner interface {
	SaveMarketResearchItemForStorageHostOperation(context.Context, persistrevenue.RevenueStorageHostOperationIdentity, domainrevenue.MarketResearchItem) (domainrevenue.MarketResearchItem, error)
	LookupRevenueMarketResearchItemStorageHostReceipt(context.Context, persistrevenue.RevenueStorageHostOperationIdentity, domainrevenue.MarketResearchItem) (json.RawMessage, bool, error)
	SaveSNSPostMetricForStorageHostOperation(context.Context, persistrevenue.RevenueStorageHostOperationIdentity, domainrevenue.SNSPostMetric) (domainrevenue.SNSPostMetric, error)
	LookupRevenueSNSPostMetricStorageHostReceipt(context.Context, persistrevenue.RevenueStorageHostOperationIdentity, domainrevenue.SNSPostMetric) (json.RawMessage, bool, error)
	SaveProductForStorageHostOperation(context.Context, persistrevenue.RevenueStorageHostOperationIdentity, domainrevenue.Product) (domainrevenue.Product, error)
	LookupRevenueProductStorageHostReceipt(context.Context, persistrevenue.RevenueStorageHostOperationIdentity, domainrevenue.Product) (json.RawMessage, bool, error)
	SaveCustomerVoiceForStorageHostOperation(context.Context, persistrevenue.RevenueStorageHostOperationIdentity, domainrevenue.CustomerVoice) (domainrevenue.CustomerVoice, error)
	LookupRevenueCustomerVoiceStorageHostReceipt(context.Context, persistrevenue.RevenueStorageHostOperationIdentity, domainrevenue.CustomerVoice) (json.RawMessage, bool, error)
	SaveRevenueEventForStorageHostOperation(context.Context, persistrevenue.RevenueStorageHostOperationIdentity, domainrevenue.RevenueEvent) (domainrevenue.RevenueEvent, error)
	LookupRevenueEventStorageHostReceipt(context.Context, persistrevenue.RevenueStorageHostOperationIdentity, domainrevenue.RevenueEvent) (json.RawMessage, bool, error)
	ListMarketResearchItems(context.Context, int) ([]domainrevenue.MarketResearchItem, error)
	ListSNSPostMetrics(context.Context, int) ([]domainrevenue.SNSPostMetric, error)
	ListProducts(context.Context, int) ([]domainrevenue.Product, error)
	ListCustomerVoices(context.Context, int) ([]domainrevenue.CustomerVoice, error)
	ListRevenueEvents(context.Context, int) ([]domainrevenue.RevenueEvent, error)
	ListPolicyDecisionRecords(context.Context, int) ([]domainrevenue.PolicyDecisionRecord, error)
	ListDailyRoutineReports(context.Context, int) ([]domainrevenue.DailyRoutineReport, error)
	ListChannelDrafts(context.Context, int) ([]domainrevenue.ChannelDraft, error)
	ListExternalSendApplyRecords(context.Context, int) ([]domainrevenue.ExternalSendApplyRecord, error)
	ListOpportunities(context.Context, int) ([]domainrevenue.Opportunity, error)
	ListEconomicTasks(context.Context, int) ([]domainrevenue.EconomicTask, error)
	ListEconomicReflections(context.Context, int) ([]domainrevenue.EconomicReflection, error)
	ListDeliveries(context.Context, int) ([]domainrevenue.Delivery, error)
	FindOpportunityByID(context.Context, string) (domainrevenue.Opportunity, bool, error)
	SaveOpportunityForStorageHostOperation(context.Context, persistrevenue.RevenueStorageHostOperationIdentity, domainrevenue.Opportunity) (domainrevenue.Opportunity, error)
	LookupRevenueOpportunityStorageHostReceipt(context.Context, persistrevenue.RevenueStorageHostOperationIdentity, domainrevenue.Opportunity) (json.RawMessage, bool, error)
	SaveEconomicTaskForStorageHostOperation(context.Context, persistrevenue.RevenueStorageHostOperationIdentity, domainrevenue.EconomicTask) (domainrevenue.EconomicTask, error)
	LookupRevenueEconomicTaskStorageHostReceipt(context.Context, persistrevenue.RevenueStorageHostOperationIdentity, domainrevenue.EconomicTask) (json.RawMessage, bool, error)
	SaveEconomicReflectionForStorageHostOperation(context.Context, persistrevenue.RevenueStorageHostOperationIdentity, domainrevenue.EconomicReflection) (domainrevenue.EconomicReflection, error)
	LookupRevenueEconomicReflectionStorageHostReceipt(context.Context, persistrevenue.RevenueStorageHostOperationIdentity, domainrevenue.EconomicReflection) (json.RawMessage, bool, error)
	SavePolicyDecisionRecordForStorageHostOperation(context.Context, persistrevenue.RevenueStorageHostOperationIdentity, domainrevenue.PolicyDecisionRecord) (domainrevenue.PolicyDecisionRecord, error)
	LookupRevenuePolicyDecisionRecordStorageHostReceipt(context.Context, persistrevenue.RevenueStorageHostOperationIdentity, domainrevenue.PolicyDecisionRecord) (json.RawMessage, bool, error)
	SaveDailyRoutineReportForStorageHostOperation(context.Context, persistrevenue.RevenueStorageHostOperationIdentity, domainrevenue.DailyRoutineReport) (domainrevenue.DailyRoutineReport, error)
	LookupRevenueDailyRoutineStorageHostReceipt(context.Context, persistrevenue.RevenueStorageHostOperationIdentity, domainrevenue.DailyRoutineReport) (json.RawMessage, bool, error)
	SaveChannelDraftForStorageHostOperation(context.Context, persistrevenue.RevenueStorageHostOperationIdentity, domainrevenue.ChannelDraft) (domainrevenue.ChannelDraft, error)
	LookupRevenueChannelDraftStorageHostReceipt(context.Context, persistrevenue.RevenueStorageHostOperationIdentity, domainrevenue.ChannelDraft) (json.RawMessage, bool, error)
	SaveExternalSendApplyRecordForStorageHostOperation(context.Context, persistrevenue.RevenueStorageHostOperationIdentity, domainrevenue.ExternalSendApplyRecord) (domainrevenue.ExternalSendApplyRecord, error)
	LookupRevenueExternalSendApplyRecordStorageHostReceipt(context.Context, persistrevenue.RevenueStorageHostOperationIdentity, domainrevenue.ExternalSendApplyRecord) (json.RawMessage, bool, error)
	SaveDeliveryForStorageHostOperation(context.Context, persistrevenue.RevenueStorageHostOperationIdentity, domainrevenue.Delivery) (domainrevenue.Delivery, error)
	LookupRevenueDeliveryStorageHostReceipt(context.Context, persistrevenue.RevenueStorageHostOperationIdentity, domainrevenue.Delivery) (json.RawMessage, bool, error)
}

type RevenueClient struct{ client *Client }

func NewRevenueClient(client *Client) *RevenueClient { return &RevenueClient{client: client} }

type revenueListPayload struct {
	Limit int `json:"limit"`
}

type revenueOpportunityPayload struct {
	Opportunity domainrevenue.Opportunity `json:"opportunity"`
}

type revenueDailyReportPayload struct {
	Report domainrevenue.DailyRoutineReport `json:"report"`
}

type revenueRecordPayload[T any] struct {
	Record T `json:"record"`
}

type revenueFindOpportunityPayload struct {
	OpportunityID string `json:"opportunity_id"`
}

type revenueFindOpportunityResult struct {
	Opportunity domainrevenue.Opportunity `json:"opportunity"`
	Found       bool                      `json:"found"`
}

func RegisterRevenueGroup(handler *Handler, owner RevenueGroupOwner) error {
	if handler == nil || owner == nil {
		return errors.New("storagehost: revenue owner is required")
	}
	registrations := []func() error{
		func() error {
			return registerRevenueList(handler, revenueOpListMarketResearch, owner.ListMarketResearchItems)
		},
		func() error { return registerRevenueList(handler, revenueOpListSNSMetrics, owner.ListSNSPostMetrics) },
		func() error { return registerRevenueList(handler, revenueOpListProducts, owner.ListProducts) },
		func() error {
			return registerRevenueList(handler, revenueOpListCustomerVoices, owner.ListCustomerVoices)
		},
		func() error { return registerRevenueList(handler, revenueOpListEvents, owner.ListRevenueEvents) },
		func() error {
			return registerRevenueList(handler, revenueOpListDecisions, owner.ListPolicyDecisionRecords)
		},
		func() error {
			return registerRevenueList(handler, revenueOpListDailyReports, owner.ListDailyRoutineReports)
		},
		func() error { return registerRevenueList(handler, revenueOpListChannelDrafts, owner.ListChannelDrafts) },
		func() error {
			return registerRevenueList(handler, revenueOpListSendRecords, owner.ListExternalSendApplyRecords)
		},
		func() error { return registerRevenueList(handler, revenueOpListOpportunities, owner.ListOpportunities) },
		func() error { return registerRevenueList(handler, revenueOpListEconomicTasks, owner.ListEconomicTasks) },
		func() error {
			return registerRevenueList(handler, revenueOpListReflections, owner.ListEconomicReflections)
		},
		func() error { return registerRevenueList(handler, revenueOpListDeliveries, owner.ListDeliveries) },
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	saveRegistrations := []func() error{
		func() error {
			return registerRevenueSaveRecord(handler, revenueOpSaveMarketResearch, domainrevenue.ValidateMarketResearchItem, owner.SaveMarketResearchItemForStorageHostOperation, owner.LookupRevenueMarketResearchItemStorageHostReceipt)
		},
		func() error {
			return registerRevenueSaveRecord(handler, revenueOpSaveSNSMetric, domainrevenue.ValidateSNSPostMetric, owner.SaveSNSPostMetricForStorageHostOperation, owner.LookupRevenueSNSPostMetricStorageHostReceipt)
		},
		func() error {
			return registerRevenueSaveRecord(handler, revenueOpSaveProduct, domainrevenue.ValidateProduct, owner.SaveProductForStorageHostOperation, owner.LookupRevenueProductStorageHostReceipt)
		},
		func() error {
			return registerRevenueSaveRecord(handler, revenueOpSaveCustomerVoice, domainrevenue.ValidateCustomerVoice, owner.SaveCustomerVoiceForStorageHostOperation, owner.LookupRevenueCustomerVoiceStorageHostReceipt)
		},
		func() error {
			return registerRevenueSaveRecord(handler, revenueOpSaveRevenueEvent, domainrevenue.ValidateRevenueEvent, owner.SaveRevenueEventForStorageHostOperation, owner.LookupRevenueEventStorageHostReceipt)
		},
		func() error {
			return registerRevenueSaveRecord(handler, revenueOpSaveEconomicTask, domainrevenue.ValidateEconomicTask, owner.SaveEconomicTaskForStorageHostOperation, owner.LookupRevenueEconomicTaskStorageHostReceipt)
		},
		func() error {
			return registerRevenueSaveRecord(handler, revenueOpSaveReflection, domainrevenue.ValidateEconomicReflection, owner.SaveEconomicReflectionForStorageHostOperation, owner.LookupRevenueEconomicReflectionStorageHostReceipt)
		},
		func() error {
			return registerRevenueSaveRecord(handler, revenueOpSavePolicyDecision, domainrevenue.ValidatePolicyDecisionRecord, owner.SavePolicyDecisionRecordForStorageHostOperation, owner.LookupRevenuePolicyDecisionRecordStorageHostReceipt)
		},
		func() error {
			return registerRevenueSaveRecord(handler, revenueOpSaveChannelDraft, domainrevenue.ValidateChannelDraft, owner.SaveChannelDraftForStorageHostOperation, owner.LookupRevenueChannelDraftStorageHostReceipt)
		},
		func() error {
			return registerRevenueSaveRecord(handler, revenueOpSaveExternalApply, domainrevenue.ValidateExternalSendApplyRecord, owner.SaveExternalSendApplyRecordForStorageHostOperation, owner.LookupRevenueExternalSendApplyRecordStorageHostReceipt)
		},
		func() error {
			return registerRevenueSaveRecord(handler, revenueOpSaveDelivery, domainrevenue.ValidateDelivery, owner.SaveDeliveryForStorageHostOperation, owner.LookupRevenueDeliveryStorageHostReceipt)
		},
	}
	for _, register := range saveRegistrations {
		if err := register(); err != nil {
			return err
		}
	}
	if err := handler.Register(GroupRevenue, revenueOpFindOpportunity, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		payload, err := decodeRevenuePayload[revenueFindOpportunityPayload](raw)
		if err != nil || payload.OpportunityID == "" {
			return nil, NewError(ErrorCodeSchemaRejected, "revenue opportunity lookup request rejected")
		}
		item, found, err := owner.FindOpportunityByID(ctx, payload.OpportunityID)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "revenue opportunity lookup failed")
		}
		return revenueFindOpportunityResult{Opportunity: item, Found: found}, nil
	}); err != nil {
		return err
	}
	if err := handler.RegisterRecoverable(GroupRevenue, revenueOpSaveOpportunity, func(ctx context.Context, mutation MutationMetadata) (any, error) {
		payload, err := decodeRevenuePayload[revenueOpportunityPayload](mutation.Payload)
		normalized := domainrevenue.NormalizeOpportunityEconomics(payload.Opportunity)
		if err != nil || domainrevenue.ValidateOpportunity(normalized) != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "revenue opportunity request rejected"))
		}
		identity, err := revenueOperationIdentity(mutation, revenueOpSaveOpportunity)
		if err != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "revenue operation identity rejected"))
		}
		return owner.SaveOpportunityForStorageHostOperation(ctx, identity, payload.Opportunity)
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		payload, err := decodeRevenuePayload[revenueOpportunityPayload](mutation.Payload)
		if err != nil {
			return UnknownOutcome(), nil
		}
		identity, err := revenueOperationIdentity(mutation, revenueOpSaveOpportunity)
		if err != nil {
			return UnknownOutcome(), nil
		}
		result, found, err := owner.LookupRevenueOpportunityStorageHostReceipt(ctx, identity, payload.Opportunity)
		if err != nil {
			return UnknownOutcome(), nil
		}
		if !found {
			return ConfirmedNotCommitted(), nil
		}
		var item domainrevenue.Opportunity
		if err := decodeRevenuePayloadBytes(result, &item); err != nil || item.OpportunityID != payload.Opportunity.OpportunityID {
			return UnknownOutcome(), nil
		}
		return Committed(item), nil
	}); err != nil {
		return err
	}
	return handler.RegisterRecoverable(GroupRevenue, revenueOpSaveDailyReport, func(ctx context.Context, mutation MutationMetadata) (any, error) {
		payload, err := decodeRevenuePayload[revenueDailyReportPayload](mutation.Payload)
		if err != nil || domainrevenue.ValidateDailyRoutineReport(payload.Report) != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "revenue daily report request rejected"))
		}
		identity, err := revenueOperationIdentity(mutation, revenueOpSaveDailyReport)
		if err != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "revenue operation identity rejected"))
		}
		return owner.SaveDailyRoutineReportForStorageHostOperation(ctx, identity, payload.Report)
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		payload, err := decodeRevenuePayload[revenueDailyReportPayload](mutation.Payload)
		if err != nil {
			return UnknownOutcome(), nil
		}
		identity, err := revenueOperationIdentity(mutation, revenueOpSaveDailyReport)
		if err != nil {
			return UnknownOutcome(), nil
		}
		result, found, err := owner.LookupRevenueDailyRoutineStorageHostReceipt(ctx, identity, payload.Report)
		if err != nil {
			return UnknownOutcome(), nil
		}
		if !found {
			return ConfirmedNotCommitted(), nil
		}
		var report domainrevenue.DailyRoutineReport
		if err := decodeRevenuePayloadBytes(result, &report); err != nil || report.ArtifactID != payload.Report.ArtifactID {
			return UnknownOutcome(), nil
		}
		return Committed(report), nil
	})
}

func registerRevenueList[T any](handler *Handler, op string, list func(context.Context, int) ([]T, error)) error {
	return handler.Register(GroupRevenue, op, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		payload, err := decodeRevenuePayload[revenueListPayload](raw)
		if err != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "revenue list request rejected")
		}
		limit, err := normalizeRevenueListLimit(payload.Limit)
		if err != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "revenue list request rejected")
		}
		items, err := list(ctx, limit)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "revenue list failed")
		}
		if len(items) > limit {
			return nil, NewError(ErrorCodeStoreUnavailable, "revenue owner exceeded the requested list bound")
		}
		return items, nil
	})
}

func normalizeRevenueListLimit(limit int) (int, error) {
	if limit > revenueListMaxLimit {
		return 0, errors.New("revenue list limit exceeds the supported maximum")
	}
	if limit <= 0 {
		return revenueListDefaultLimit, nil
	}
	return limit, nil
}

func registerRevenueSaveRecord[T any](
	handler *Handler,
	op string,
	validate func(T) error,
	save func(context.Context, persistrevenue.RevenueStorageHostOperationIdentity, T) (T, error),
	lookup func(context.Context, persistrevenue.RevenueStorageHostOperationIdentity, T) (json.RawMessage, bool, error),
) error {
	return handler.RegisterRecoverable(GroupRevenue, op, func(ctx context.Context, mutation MutationMetadata) (any, error) {
		payload, err := decodeRevenuePayload[revenueRecordPayload[T]](mutation.Payload)
		if err != nil || validate(payload.Record) != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "revenue record request rejected"))
		}
		identity, err := revenueOperationIdentity(mutation, op)
		if err != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "revenue operation identity rejected"))
		}
		return save(ctx, identity, payload.Record)
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		payload, err := decodeRevenuePayload[revenueRecordPayload[T]](mutation.Payload)
		if err != nil {
			return UnknownOutcome(), nil
		}
		identity, err := revenueOperationIdentity(mutation, op)
		if err != nil {
			return UnknownOutcome(), nil
		}
		result, found, err := lookup(ctx, identity, payload.Record)
		if err != nil {
			return UnknownOutcome(), nil
		}
		if !found {
			return ConfirmedNotCommitted(), nil
		}
		var record T
		if err := decodeRevenuePayloadBytes(result, &record); err != nil {
			return UnknownOutcome(), nil
		}
		return Committed(record), nil
	})
}

func revenueOperationIdentity(mutation MutationMetadata, operation string) (persistrevenue.RevenueStorageHostOperationIdentity, error) {
	if mutation.OpID == "" || len(mutation.Payload) == 0 || mutation.JournalGeneration <= 0 || mutation.RequestGeneration <= 0 {
		return persistrevenue.RevenueStorageHostOperationIdentity{}, errors.New("revenue mutation identity is incomplete")
	}
	return persistrevenue.RevenueStorageHostOperationIdentity{
		OpID: mutation.OpID, Operation: operation,
		PayloadSHA256: payloadHash(GroupRevenue, operation, mutation.Payload), WriterGeneration: mutation.JournalGeneration,
	}, nil
}

func decodeRevenuePayload[T any](raw json.RawMessage) (T, error) {
	var result T
	if err := decodeRevenuePayloadBytes(raw, &result); err != nil {
		return result, err
	}
	return result, nil
}

func decodeRevenuePayloadBytes(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("revenue payload contains trailing value")
		}
		return err
	}
	return nil
}

func newRevenueRecordOperation[T any](client *Client, op, requestID string, item T) *Operation {
	operation := client.NewOperation(GroupRevenue, op, revenueRecordPayload[T]{Record: item})
	if requestID != "" {
		operation.OpID = requestID
	}
	return operation
}

func saveRevenueRecordOperation[T any](ctx context.Context, client *Client, operation *Operation) (T, error) {
	var result T
	if err := client.Do(ctx, operation, &result); err != nil {
		var zero T
		return zero, err
	}
	return result, nil
}

func (client *RevenueClient) NewSaveMarketResearchItemOperation(requestID string, item domainrevenue.MarketResearchItem) *Operation {
	return newRevenueRecordOperation(client.client, revenueOpSaveMarketResearch, requestID, item)
}

func (client *RevenueClient) SaveMarketResearchItem(ctx context.Context, requestID string, item domainrevenue.MarketResearchItem) (domainrevenue.MarketResearchItem, error) {
	return saveRevenueRecordOperation[domainrevenue.MarketResearchItem](ctx, client.client, client.NewSaveMarketResearchItemOperation(requestID, item))
}

func (client *RevenueClient) NewSaveSNSPostMetricOperation(requestID string, item domainrevenue.SNSPostMetric) *Operation {
	return newRevenueRecordOperation(client.client, revenueOpSaveSNSMetric, requestID, item)
}

func (client *RevenueClient) SaveSNSPostMetric(ctx context.Context, requestID string, item domainrevenue.SNSPostMetric) (domainrevenue.SNSPostMetric, error) {
	return saveRevenueRecordOperation[domainrevenue.SNSPostMetric](ctx, client.client, client.NewSaveSNSPostMetricOperation(requestID, item))
}

func (client *RevenueClient) NewSaveProductOperation(requestID string, item domainrevenue.Product) *Operation {
	return newRevenueRecordOperation(client.client, revenueOpSaveProduct, requestID, item)
}

func (client *RevenueClient) SaveProduct(ctx context.Context, requestID string, item domainrevenue.Product) (domainrevenue.Product, error) {
	return saveRevenueRecordOperation[domainrevenue.Product](ctx, client.client, client.NewSaveProductOperation(requestID, item))
}

func (client *RevenueClient) NewSaveCustomerVoiceOperation(requestID string, item domainrevenue.CustomerVoice) *Operation {
	return newRevenueRecordOperation(client.client, revenueOpSaveCustomerVoice, requestID, item)
}

func (client *RevenueClient) SaveCustomerVoice(ctx context.Context, requestID string, item domainrevenue.CustomerVoice) (domainrevenue.CustomerVoice, error) {
	return saveRevenueRecordOperation[domainrevenue.CustomerVoice](ctx, client.client, client.NewSaveCustomerVoiceOperation(requestID, item))
}

func (client *RevenueClient) NewSaveRevenueEventOperation(requestID string, item domainrevenue.RevenueEvent) *Operation {
	return newRevenueRecordOperation(client.client, revenueOpSaveRevenueEvent, requestID, item)
}

func (client *RevenueClient) SaveRevenueEvent(ctx context.Context, requestID string, item domainrevenue.RevenueEvent) (domainrevenue.RevenueEvent, error) {
	return saveRevenueRecordOperation[domainrevenue.RevenueEvent](ctx, client.client, client.NewSaveRevenueEventOperation(requestID, item))
}

func (client *RevenueClient) NewSaveEconomicTaskOperation(requestID string, item domainrevenue.EconomicTask) *Operation {
	return newRevenueRecordOperation(client.client, revenueOpSaveEconomicTask, requestID, item)
}

func (client *RevenueClient) SaveEconomicTask(ctx context.Context, requestID string, item domainrevenue.EconomicTask) (domainrevenue.EconomicTask, error) {
	return saveRevenueRecordOperation[domainrevenue.EconomicTask](ctx, client.client, client.NewSaveEconomicTaskOperation(requestID, item))
}

func (client *RevenueClient) NewSaveEconomicReflectionOperation(requestID string, item domainrevenue.EconomicReflection) *Operation {
	return newRevenueRecordOperation(client.client, revenueOpSaveReflection, requestID, item)
}

func (client *RevenueClient) SaveEconomicReflection(ctx context.Context, requestID string, item domainrevenue.EconomicReflection) (domainrevenue.EconomicReflection, error) {
	return saveRevenueRecordOperation[domainrevenue.EconomicReflection](ctx, client.client, client.NewSaveEconomicReflectionOperation(requestID, item))
}

func (client *RevenueClient) NewSavePolicyDecisionRecordOperation(requestID string, item domainrevenue.PolicyDecisionRecord) *Operation {
	return newRevenueRecordOperation(client.client, revenueOpSavePolicyDecision, requestID, item)
}

func (client *RevenueClient) SavePolicyDecisionRecord(ctx context.Context, requestID string, item domainrevenue.PolicyDecisionRecord) (domainrevenue.PolicyDecisionRecord, error) {
	return saveRevenueRecordOperation[domainrevenue.PolicyDecisionRecord](ctx, client.client, client.NewSavePolicyDecisionRecordOperation(requestID, item))
}

func (client *RevenueClient) NewSaveChannelDraftOperation(requestID string, item domainrevenue.ChannelDraft) *Operation {
	return newRevenueRecordOperation(client.client, revenueOpSaveChannelDraft, requestID, item)
}

func (client *RevenueClient) SaveChannelDraft(ctx context.Context, requestID string, item domainrevenue.ChannelDraft) (domainrevenue.ChannelDraft, error) {
	return saveRevenueRecordOperation[domainrevenue.ChannelDraft](ctx, client.client, client.NewSaveChannelDraftOperation(requestID, item))
}

func (client *RevenueClient) NewSaveExternalSendApplyRecordOperation(requestID string, item domainrevenue.ExternalSendApplyRecord) *Operation {
	return newRevenueRecordOperation(client.client, revenueOpSaveExternalApply, requestID, item)
}

func (client *RevenueClient) SaveExternalSendApplyRecord(ctx context.Context, requestID string, item domainrevenue.ExternalSendApplyRecord) (domainrevenue.ExternalSendApplyRecord, error) {
	return saveRevenueRecordOperation[domainrevenue.ExternalSendApplyRecord](ctx, client.client, client.NewSaveExternalSendApplyRecordOperation(requestID, item))
}

func (client *RevenueClient) NewSaveDeliveryOperation(requestID string, item domainrevenue.Delivery) *Operation {
	return newRevenueRecordOperation(client.client, revenueOpSaveDelivery, requestID, item)
}

func (client *RevenueClient) SaveDelivery(ctx context.Context, requestID string, item domainrevenue.Delivery) (domainrevenue.Delivery, error) {
	return saveRevenueRecordOperation[domainrevenue.Delivery](ctx, client.client, client.NewSaveDeliveryOperation(requestID, item))
}

func (client *RevenueClient) NewSaveOpportunityOperation(requestID string, item domainrevenue.Opportunity) *Operation {
	operation := client.client.NewOperation(GroupRevenue, revenueOpSaveOpportunity, revenueOpportunityPayload{Opportunity: item})
	if requestID != "" {
		operation.OpID = requestID
	}
	return operation
}
func (client *RevenueClient) SaveOpportunity(ctx context.Context, requestID string, item domainrevenue.Opportunity) (domainrevenue.Opportunity, error) {
	var result domainrevenue.Opportunity
	if err := client.client.Do(ctx, client.NewSaveOpportunityOperation(requestID, item), &result); err != nil {
		return domainrevenue.Opportunity{}, err
	}
	return result, nil
}
func (client *RevenueClient) NewSaveDailyRoutineReportOperation(requestID string, item domainrevenue.DailyRoutineReport) *Operation {
	operation := client.client.NewOperation(GroupRevenue, revenueOpSaveDailyReport, revenueDailyReportPayload{Report: item})
	if requestID != "" {
		operation.OpID = requestID
	}
	return operation
}
func (client *RevenueClient) SaveDailyRoutineReport(ctx context.Context, requestID string, item domainrevenue.DailyRoutineReport) (domainrevenue.DailyRoutineReport, error) {
	var result domainrevenue.DailyRoutineReport
	if err := client.client.Do(ctx, client.NewSaveDailyRoutineReportOperation(requestID, item), &result); err != nil {
		return domainrevenue.DailyRoutineReport{}, err
	}
	return result, nil
}
func (client *RevenueClient) FindOpportunityByID(ctx context.Context, id string) (domainrevenue.Opportunity, bool, error) {
	var result revenueFindOpportunityResult
	if err := client.client.Call(ctx, GroupRevenue, revenueOpFindOpportunity, revenueFindOpportunityPayload{OpportunityID: id}, &result); err != nil {
		return domainrevenue.Opportunity{}, false, err
	}
	return result.Opportunity, result.Found, nil
}

func (client *RevenueClient) ListMarketResearchItems(ctx context.Context, limit int) ([]domainrevenue.MarketResearchItem, error) {
	return revenueListCall[domainrevenue.MarketResearchItem](ctx, client.client, revenueOpListMarketResearch, limit)
}
func (client *RevenueClient) ListSNSPostMetrics(ctx context.Context, limit int) ([]domainrevenue.SNSPostMetric, error) {
	return revenueListCall[domainrevenue.SNSPostMetric](ctx, client.client, revenueOpListSNSMetrics, limit)
}
func (client *RevenueClient) ListProducts(ctx context.Context, limit int) ([]domainrevenue.Product, error) {
	return revenueListCall[domainrevenue.Product](ctx, client.client, revenueOpListProducts, limit)
}
func (client *RevenueClient) ListCustomerVoices(ctx context.Context, limit int) ([]domainrevenue.CustomerVoice, error) {
	return revenueListCall[domainrevenue.CustomerVoice](ctx, client.client, revenueOpListCustomerVoices, limit)
}
func (client *RevenueClient) ListRevenueEvents(ctx context.Context, limit int) ([]domainrevenue.RevenueEvent, error) {
	return revenueListCall[domainrevenue.RevenueEvent](ctx, client.client, revenueOpListEvents, limit)
}
func (client *RevenueClient) ListPolicyDecisionRecords(ctx context.Context, limit int) ([]domainrevenue.PolicyDecisionRecord, error) {
	return revenueListCall[domainrevenue.PolicyDecisionRecord](ctx, client.client, revenueOpListDecisions, limit)
}
func (client *RevenueClient) ListDailyRoutineReports(ctx context.Context, limit int) ([]domainrevenue.DailyRoutineReport, error) {
	return revenueListCall[domainrevenue.DailyRoutineReport](ctx, client.client, revenueOpListDailyReports, limit)
}
func (client *RevenueClient) ListChannelDrafts(ctx context.Context, limit int) ([]domainrevenue.ChannelDraft, error) {
	return revenueListCall[domainrevenue.ChannelDraft](ctx, client.client, revenueOpListChannelDrafts, limit)
}
func (client *RevenueClient) ListExternalSendApplyRecords(ctx context.Context, limit int) ([]domainrevenue.ExternalSendApplyRecord, error) {
	return revenueListCall[domainrevenue.ExternalSendApplyRecord](ctx, client.client, revenueOpListSendRecords, limit)
}
func (client *RevenueClient) ListOpportunities(ctx context.Context, limit int) ([]domainrevenue.Opportunity, error) {
	return revenueListCall[domainrevenue.Opportunity](ctx, client.client, revenueOpListOpportunities, limit)
}
func (client *RevenueClient) ListEconomicTasks(ctx context.Context, limit int) ([]domainrevenue.EconomicTask, error) {
	return revenueListCall[domainrevenue.EconomicTask](ctx, client.client, revenueOpListEconomicTasks, limit)
}
func (client *RevenueClient) ListEconomicReflections(ctx context.Context, limit int) ([]domainrevenue.EconomicReflection, error) {
	return revenueListCall[domainrevenue.EconomicReflection](ctx, client.client, revenueOpListReflections, limit)
}
func (client *RevenueClient) ListDeliveries(ctx context.Context, limit int) ([]domainrevenue.Delivery, error) {
	return revenueListCall[domainrevenue.Delivery](ctx, client.client, revenueOpListDeliveries, limit)
}

func revenueListCall[T any](ctx context.Context, client *Client, op string, limit int) ([]T, error) {
	effectiveLimit, err := normalizeRevenueListLimit(limit)
	if err != nil {
		return nil, NewError(ErrorCodeSchemaRejected, "revenue list limit rejected")
	}
	var items []T
	if err := client.Call(ctx, GroupRevenue, op, revenueListPayload{Limit: effectiveLimit}, &items); err != nil {
		return nil, err
	}
	if len(items) > effectiveLimit {
		return nil, NewError(ErrorCodeStoreUnavailable, "revenue storage host exceeded the requested list bound")
	}
	return items, nil
}
