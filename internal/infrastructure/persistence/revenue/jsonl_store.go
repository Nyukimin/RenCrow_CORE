package revenue

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	domainrevenue "github.com/Nyukimin/RenCrow_CORE/internal/domain/revenue"
)

type JSONLStore struct {
	marketPath        string
	snsPostMetricPath string
	productPath       string
	customerVoicePath string
	revenueEventPath  string
	humanDecisionPath string
	dailyRoutinePath  string
	channelDraftPath  string
	externalSendPath  string
}

func NewJSONLStore(root string) *JSONLStore {
	if root == "" {
		root = "workspace/logs/revenue"
	}
	return &JSONLStore{
		marketPath:        filepath.Join(root, "market_research_item.jsonl"),
		snsPostMetricPath: filepath.Join(root, "sns_post_metric.jsonl"),
		productPath:       filepath.Join(root, "product_catalog.jsonl"),
		customerVoicePath: filepath.Join(root, "customer_voice.jsonl"),
		revenueEventPath:  filepath.Join(root, "revenue_event.jsonl"),
		humanDecisionPath: filepath.Join(root, "human_decision_gate.jsonl"),
		dailyRoutinePath:  filepath.Join(root, "daily_routine_report.jsonl"),
		channelDraftPath:  filepath.Join(root, "channel_draft.jsonl"),
		externalSendPath:  filepath.Join(root, "external_send_apply.jsonl"),
	}
}

func (s *JSONLStore) SaveMarketResearchItem(_ context.Context, item domainrevenue.MarketResearchItem) error {
	if err := domainrevenue.ValidateMarketResearchItem(item); err != nil {
		return err
	}
	return appendJSONL(s.marketPath, item)
}

func (s *JSONLStore) ListMarketResearchItems(_ context.Context, limit int) ([]domainrevenue.MarketResearchItem, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []domainrevenue.MarketResearchItem
	if err := readJSONL(s.marketPath, func(line []byte) error {
		var item domainrevenue.MarketResearchItem
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	}); err != nil {
		return nil, err
	}
	return reverseLimit(items, limit), nil
}

func (s *JSONLStore) SaveSNSPostMetric(_ context.Context, item domainrevenue.SNSPostMetric) error {
	if err := domainrevenue.ValidateSNSPostMetric(item); err != nil {
		return err
	}
	return appendJSONL(s.snsPostMetricPath, item)
}

func (s *JSONLStore) ListSNSPostMetrics(_ context.Context, limit int) ([]domainrevenue.SNSPostMetric, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []domainrevenue.SNSPostMetric
	if err := readJSONL(s.snsPostMetricPath, func(line []byte) error {
		var item domainrevenue.SNSPostMetric
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	}); err != nil {
		return nil, err
	}
	return reverseLimit(items, limit), nil
}

func (s *JSONLStore) SaveProduct(_ context.Context, item domainrevenue.Product) error {
	if err := domainrevenue.ValidateProduct(item); err != nil {
		return err
	}
	return appendJSONL(s.productPath, item)
}

func (s *JSONLStore) ListProducts(_ context.Context, limit int) ([]domainrevenue.Product, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []domainrevenue.Product
	if err := readJSONL(s.productPath, func(line []byte) error {
		var item domainrevenue.Product
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	}); err != nil {
		return nil, err
	}
	return reverseLimit(items, limit), nil
}

func (s *JSONLStore) SaveCustomerVoice(_ context.Context, item domainrevenue.CustomerVoice) error {
	if err := domainrevenue.ValidateCustomerVoice(item); err != nil {
		return err
	}
	return appendJSONL(s.customerVoicePath, item)
}

func (s *JSONLStore) ListCustomerVoices(_ context.Context, limit int) ([]domainrevenue.CustomerVoice, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []domainrevenue.CustomerVoice
	if err := readJSONL(s.customerVoicePath, func(line []byte) error {
		var item domainrevenue.CustomerVoice
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	}); err != nil {
		return nil, err
	}
	return reverseLimit(items, limit), nil
}

func (s *JSONLStore) SaveRevenueEvent(_ context.Context, item domainrevenue.RevenueEvent) error {
	if err := domainrevenue.ValidateRevenueEvent(item); err != nil {
		return err
	}
	return appendJSONL(s.revenueEventPath, item)
}

func (s *JSONLStore) ListRevenueEvents(_ context.Context, limit int) ([]domainrevenue.RevenueEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []domainrevenue.RevenueEvent
	if err := readJSONL(s.revenueEventPath, func(line []byte) error {
		var item domainrevenue.RevenueEvent
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	}); err != nil {
		return nil, err
	}
	return reverseLimit(items, limit), nil
}

func (s *JSONLStore) SaveHumanDecisionGateRecord(_ context.Context, item domainrevenue.HumanDecisionGateRecord) error {
	if err := domainrevenue.ValidateHumanDecisionGateRecord(item); err != nil {
		return err
	}
	return appendJSONL(s.humanDecisionPath, item)
}

func (s *JSONLStore) ListHumanDecisionGateRecords(_ context.Context, limit int) ([]domainrevenue.HumanDecisionGateRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []domainrevenue.HumanDecisionGateRecord
	if err := readJSONL(s.humanDecisionPath, func(line []byte) error {
		var item domainrevenue.HumanDecisionGateRecord
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	}); err != nil {
		return nil, err
	}
	return latestHumanDecisionRecords(items, limit), nil
}

func latestHumanDecisionRecords(items []domainrevenue.HumanDecisionGateRecord, limit int) []domainrevenue.HumanDecisionGateRecord {
	if limit <= 0 {
		limit = len(items)
	}
	seen := map[string]struct{}{}
	out := make([]domainrevenue.HumanDecisionGateRecord, 0, minRevenueLimit(limit, len(items)))
	for i := len(items) - 1; i >= 0 && len(out) < limit; i-- {
		item := items[i]
		if _, ok := seen[item.DecisionID]; ok {
			continue
		}
		seen[item.DecisionID] = struct{}{}
		out = append(out, item)
	}
	return out
}

func (s *JSONLStore) SaveDailyRoutineReport(_ context.Context, item domainrevenue.DailyRoutineReport) error {
	if err := domainrevenue.ValidateDailyRoutineReport(item); err != nil {
		return err
	}
	return appendJSONL(s.dailyRoutinePath, item)
}

func (s *JSONLStore) ListDailyRoutineReports(_ context.Context, limit int) ([]domainrevenue.DailyRoutineReport, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []domainrevenue.DailyRoutineReport
	if err := readJSONL(s.dailyRoutinePath, func(line []byte) error {
		var item domainrevenue.DailyRoutineReport
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	}); err != nil {
		return nil, err
	}
	return reverseLimit(items, limit), nil
}

func (s *JSONLStore) SaveChannelDraft(_ context.Context, item domainrevenue.ChannelDraft) error {
	if err := domainrevenue.ValidateChannelDraft(item); err != nil {
		return err
	}
	return appendJSONL(s.channelDraftPath, item)
}

func (s *JSONLStore) ListChannelDrafts(_ context.Context, limit int) ([]domainrevenue.ChannelDraft, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []domainrevenue.ChannelDraft
	if err := readJSONL(s.channelDraftPath, func(line []byte) error {
		var item domainrevenue.ChannelDraft
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	}); err != nil {
		return nil, err
	}
	return reverseLimit(items, limit), nil
}

func (s *JSONLStore) SaveExternalSendApplyRecord(_ context.Context, item domainrevenue.ExternalSendApplyRecord) error {
	if err := domainrevenue.ValidateExternalSendApplyRecord(item); err != nil {
		return err
	}
	return appendJSONL(s.externalSendPath, item)
}

func (s *JSONLStore) ListExternalSendApplyRecords(_ context.Context, limit int) ([]domainrevenue.ExternalSendApplyRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []domainrevenue.ExternalSendApplyRecord
	if err := readJSONL(s.externalSendPath, func(line []byte) error {
		var item domainrevenue.ExternalSendApplyRecord
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	}); err != nil {
		return nil, err
	}
	return reverseLimit(items, limit), nil
}

func appendJSONL(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	line, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return err
}

func readJSONL(path string, fn func([]byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if err := fn(scanner.Bytes()); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func reverseLimit[T any](items []T, limit int) []T {
	if limit <= 0 || limit > len(items) {
		limit = len(items)
	}
	out := make([]T, 0, limit)
	for i := len(items) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, items[i])
	}
	return out
}

func minRevenueLimit(a, b int) int {
	if a < b {
		return a
	}
	return b
}
