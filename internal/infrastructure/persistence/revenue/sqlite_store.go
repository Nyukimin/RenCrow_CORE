package revenue

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	domainrevenue "github.com/Nyukimin/RenCrow_CORE/internal/domain/revenue"
	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	if path == "" {
		path = "workspace/logs/revenue.db"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_time_format=sqlite")
	if err != nil {
		return nil, err
	}
	store := &SQLiteStore{db: db}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *SQLiteStore) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS market_research_item (
			item_id TEXT PRIMARY KEY,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS sns_post_metric (
			post_id TEXT PRIMARY KEY,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS product_catalog (
			product_id TEXT PRIMARY KEY,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS customer_voice (
			voice_id TEXT PRIMARY KEY,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS revenue_event (
			event_id TEXT PRIMARY KEY,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS opportunity (
			opportunity_id TEXT PRIMARY KEY,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS economic_task (
			task_id TEXT PRIMARY KEY,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS economic_reflection (
			reflection_id TEXT PRIMARY KEY,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS human_decision_gate (
			decision_id TEXT PRIMARY KEY,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS revenue_daily_routine_report (
			report_id TEXT PRIMARY KEY,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS channel_draft (
			draft_id TEXT PRIMARY KEY,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS external_send_apply (
			apply_id TEXT PRIMARY KEY,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) SaveMarketResearchItem(ctx context.Context, item domainrevenue.MarketResearchItem) error {
	if err := domainrevenue.ValidateMarketResearchItem(item); err != nil {
		return err
	}
	return s.save(ctx, "market_research_item", "item_id", item.ItemID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListMarketResearchItems(ctx context.Context, limit int) ([]domainrevenue.MarketResearchItem, error) {
	return listSQLiteItems[domainrevenue.MarketResearchItem](ctx, s, "market_research_item", limit)
}

func (s *SQLiteStore) SaveSNSPostMetric(ctx context.Context, item domainrevenue.SNSPostMetric) error {
	if err := domainrevenue.ValidateSNSPostMetric(item); err != nil {
		return err
	}
	return s.save(ctx, "sns_post_metric", "post_id", item.PostID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListSNSPostMetrics(ctx context.Context, limit int) ([]domainrevenue.SNSPostMetric, error) {
	return listSQLiteItems[domainrevenue.SNSPostMetric](ctx, s, "sns_post_metric", limit)
}

func (s *SQLiteStore) SaveProduct(ctx context.Context, item domainrevenue.Product) error {
	if err := domainrevenue.ValidateProduct(item); err != nil {
		return err
	}
	return s.save(ctx, "product_catalog", "product_id", item.ProductID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListProducts(ctx context.Context, limit int) ([]domainrevenue.Product, error) {
	return listSQLiteItems[domainrevenue.Product](ctx, s, "product_catalog", limit)
}

func (s *SQLiteStore) SaveCustomerVoice(ctx context.Context, item domainrevenue.CustomerVoice) error {
	if err := domainrevenue.ValidateCustomerVoice(item); err != nil {
		return err
	}
	return s.save(ctx, "customer_voice", "voice_id", item.VoiceID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListCustomerVoices(ctx context.Context, limit int) ([]domainrevenue.CustomerVoice, error) {
	return listSQLiteItems[domainrevenue.CustomerVoice](ctx, s, "customer_voice", limit)
}

func (s *SQLiteStore) SaveRevenueEvent(ctx context.Context, item domainrevenue.RevenueEvent) error {
	if err := domainrevenue.ValidateRevenueEvent(item); err != nil {
		return err
	}
	return s.save(ctx, "revenue_event", "event_id", item.EventID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListRevenueEvents(ctx context.Context, limit int) ([]domainrevenue.RevenueEvent, error) {
	return listSQLiteItems[domainrevenue.RevenueEvent](ctx, s, "revenue_event", limit)
}

func (s *SQLiteStore) SaveOpportunity(ctx context.Context, item domainrevenue.Opportunity) error {
	item = domainrevenue.NormalizeOpportunityEconomics(item)
	if err := domainrevenue.ValidateOpportunity(item); err != nil {
		return err
	}
	return s.save(ctx, "opportunity", "opportunity_id", item.OpportunityID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListOpportunities(ctx context.Context, limit int) ([]domainrevenue.Opportunity, error) {
	return listSQLiteItems[domainrevenue.Opportunity](ctx, s, "opportunity", limit)
}

func (s *SQLiteStore) SaveEconomicTask(ctx context.Context, item domainrevenue.EconomicTask) error {
	if err := domainrevenue.ValidateEconomicTask(item); err != nil {
		return err
	}
	return s.save(ctx, "economic_task", "task_id", item.TaskID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListEconomicTasks(ctx context.Context, limit int) ([]domainrevenue.EconomicTask, error) {
	return listSQLiteItems[domainrevenue.EconomicTask](ctx, s, "economic_task", limit)
}

func (s *SQLiteStore) SaveEconomicReflection(ctx context.Context, item domainrevenue.EconomicReflection) error {
	if err := domainrevenue.ValidateEconomicReflection(item); err != nil {
		return err
	}
	return s.save(ctx, "economic_reflection", "reflection_id", item.ReflectionID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListEconomicReflections(ctx context.Context, limit int) ([]domainrevenue.EconomicReflection, error) {
	return listSQLiteItems[domainrevenue.EconomicReflection](ctx, s, "economic_reflection", limit)
}

func (s *SQLiteStore) SaveHumanDecisionGateRecord(ctx context.Context, item domainrevenue.HumanDecisionGateRecord) error {
	if err := domainrevenue.ValidateHumanDecisionGateRecord(item); err != nil {
		return err
	}
	return s.save(ctx, "human_decision_gate", "decision_id", item.DecisionID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListHumanDecisionGateRecords(ctx context.Context, limit int) ([]domainrevenue.HumanDecisionGateRecord, error) {
	return listSQLiteItems[domainrevenue.HumanDecisionGateRecord](ctx, s, "human_decision_gate", limit)
}

func (s *SQLiteStore) SaveDailyRoutineReport(ctx context.Context, item domainrevenue.DailyRoutineReport) error {
	if err := domainrevenue.ValidateDailyRoutineReport(item); err != nil {
		return err
	}
	return s.save(ctx, "revenue_daily_routine_report", "report_id", item.ReportID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListDailyRoutineReports(ctx context.Context, limit int) ([]domainrevenue.DailyRoutineReport, error) {
	return listSQLiteItems[domainrevenue.DailyRoutineReport](ctx, s, "revenue_daily_routine_report", limit)
}

func (s *SQLiteStore) SaveChannelDraft(ctx context.Context, item domainrevenue.ChannelDraft) error {
	if err := domainrevenue.ValidateChannelDraft(item); err != nil {
		return err
	}
	return s.save(ctx, "channel_draft", "draft_id", item.DraftID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListChannelDrafts(ctx context.Context, limit int) ([]domainrevenue.ChannelDraft, error) {
	return listSQLiteItems[domainrevenue.ChannelDraft](ctx, s, "channel_draft", limit)
}

func (s *SQLiteStore) SaveExternalSendApplyRecord(ctx context.Context, item domainrevenue.ExternalSendApplyRecord) error {
	if err := domainrevenue.ValidateExternalSendApplyRecord(item); err != nil {
		return err
	}
	return s.save(ctx, "external_send_apply", "apply_id", item.ApplyID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListExternalSendApplyRecords(ctx context.Context, limit int) ([]domainrevenue.ExternalSendApplyRecord, error) {
	return listSQLiteItems[domainrevenue.ExternalSendApplyRecord](ctx, s, "external_send_apply", limit)
}

func (s *SQLiteStore) save(ctx context.Context, table string, idColumn string, id string, createdAt string, item any) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("revenue sqlite store is closed")
	}
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	query := fmt.Sprintf(`INSERT OR REPLACE INTO %s (%s, created_at, payload) VALUES (?, ?, ?)`, table, idColumn)
	_, err = s.db.ExecContext(ctx, query, id, createdAt, string(payload))
	return err
}

func listSQLiteItems[T any](ctx context.Context, s *SQLiteStore, table string, limit int) ([]T, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("revenue sqlite store is closed")
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT payload FROM %s ORDER BY rowid DESC LIMIT ?`, table), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []T{}
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var item T
		if err := json.Unmarshal([]byte(payload), &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

const timeFormatRFC3339Nano = "2006-01-02T15:04:05.999999999Z07:00"
