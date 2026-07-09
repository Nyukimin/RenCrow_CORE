package viewer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	_ "github.com/mattn/go-sqlite3"
)

type investmentStatusResponse struct {
	Available       bool                     `json:"available"`
	Status          string                   `json:"status"`
	StatusMessage   string                   `json:"status_message"`
	DBPath          string                   `json:"db_path"`
	RefreshedAt     string                   `json:"refreshed_at"`
	Summary         investmentSummary        `json:"summary"`
	Snapshot        *investmentSnapshot      `json:"snapshot,omitempty"`
	RecentSnapshots []investmentSnapshot     `json:"recent_snapshots,omitempty"`
	SourceHealth    []investmentSourceHealth `json:"source_health,omitempty"`
	FeatureRows     []investmentFeatureRow   `json:"feature_rows,omitempty"`
	EventRows       []investmentEventRow     `json:"event_rows,omitempty"`
}

type investmentSummary struct {
	SnapshotDate         string  `json:"snapshot_date,omitempty"`
	SnapshotStatus       string  `json:"snapshot_status,omitempty"`
	DBHash               string  `json:"db_hash,omitempty"`
	FeaturesHash         string  `json:"features_hash,omitempty"`
	DataStartDate        string  `json:"data_start_date,omitempty"`
	DataEndDate          string  `json:"data_end_date,omitempty"`
	MissingRate          float64 `json:"missing_rate,omitempty"`
	FeatureRows          int     `json:"feature_rows"`
	EventRows            int     `json:"event_rows"`
	OpenStopEvents       int     `json:"open_stop_events"`
	OpenWarnEvents       int     `json:"open_warn_events"`
	FailFetches          int     `json:"fail_fetches"`
	PartialFetches       int     `json:"partial_fetches"`
	StaleSources         int     `json:"stale_sources"`
	LatestFeatureWeekEnd string  `json:"latest_feature_week_end,omitempty"`
}

type investmentSnapshot struct {
	SnapshotDate  string  `json:"snapshot_date"`
	SnapshotPath  string  `json:"snapshot_path"`
	DBHash        string  `json:"db_hash"`
	FeaturesHash  string  `json:"features_hash"`
	DataStartDate string  `json:"data_start_date,omitempty"`
	DataEndDate   string  `json:"data_end_date,omitempty"`
	MissingRate   float64 `json:"missing_rate"`
	Status        string  `json:"status"`
	Notes         string  `json:"notes,omitempty"`
	CreatedAt     string  `json:"created_at,omitempty"`
}

type investmentSourceHealth struct {
	SourceName   string `json:"source_name"`
	LatestStatus string `json:"latest_status"`
	LastFetchAt  string `json:"last_fetch_at,omitempty"`
	SuccessCount int    `json:"success_count"`
	PartialCount int    `json:"partial_count"`
	FailCount    int    `json:"fail_count"`
	RowsFetched  int64  `json:"rows_fetched"`
	ErrorMessage string `json:"error_message,omitempty"`
}

type investmentFeatureRow struct {
	WeekEnd        string   `json:"week_end"`
	Instrument     string   `json:"instrument"`
	Ret1W          *float64 `json:"ret_1w,omitempty"`
	Ret12W         *float64 `json:"ret_12w,omitempty"`
	Vol12W         *float64 `json:"vol_12w,omitempty"`
	EventRiskScore *float64 `json:"event_risk_score,omitempty"`
	Flags          string   `json:"flags,omitempty"`
}

type investmentEventRow struct {
	EventTS        string   `json:"event_ts"`
	Level          string   `json:"level"`
	Scope          string   `json:"scope"`
	Reason         string   `json:"reason"`
	EventRiskScore *float64 `json:"event_risk_score,omitempty"`
	ResolvedAt     string   `json:"resolved_at,omitempty"`
}

func HandleInvestmentStatus(dbPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		resp := loadInvestmentStatus(r.Context(), resolveInvestmentDBPath(dbPath))
		w.Header().Set("Content-Type", "application/json")
		if resp.Available {
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}
}

type investmentNotifyRequest struct {
	Phase   string         `json:"phase"`
	Source  string         `json:"source"`
	Status  string         `json:"status"`
	Message string         `json:"message"`
	Meta    map[string]any `json:"meta"`
}

func HandleInvestmentNotify(hub *EventHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if hub == nil {
			http.Error(w, "event hub unavailable", http.StatusServiceUnavailable)
			return
		}
		defer r.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var req investmentNotifyRequest
		if len(strings.TrimSpace(string(body))) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				http.Error(w, "invalid JSON body", http.StatusBadRequest)
				return
			}
		}
		phase := strings.TrimSpace(req.Phase)
		if phase == "" {
			phase = "refresh"
		}
		source := strings.TrimSpace(req.Source)
		if source == "" {
			source = "rencrow-data"
		}
		status := strings.TrimSpace(req.Status)
		if status == "" {
			status = "ok"
		}
		payload, _ := json.Marshal(map[string]any{
			"phase":   phase,
			"source":  source,
			"status":  status,
			"message": strings.TrimSpace(req.Message),
			"meta":    req.Meta,
		})
		hub.OnEvent(orchestrator.NewEvent(
			"investment."+phase,
			source,
			"viewer",
			string(payload),
			"INVESTMENT",
			"",
			"",
			"viewer",
			"investment",
		))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "phase": phase, "source": source})
	}
}

func resolveInvestmentDBPath(dbPath string) string {
	if trimmed := strings.TrimSpace(dbPath); trimmed != "" {
		return trimmed
	}
	if env := strings.TrimSpace(os.Getenv("RENCROW_DATA_DB")); env != "" {
		return env
	}
	return filepath.Join("rencrow-data", "data", "rencrow.db")
}

func loadInvestmentStatus(ctx context.Context, dbPath string) investmentStatusResponse {
	resp := investmentStatusResponse{
		DBPath:        dbPath,
		Status:        "unavailable",
		StatusMessage: "investment database not found",
		RefreshedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	if _, err := os.Stat(dbPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			resp.StatusMessage = "investment database not found: " + dbPath
		} else if err != nil {
			resp.StatusMessage = "investment database stat failed: " + err.Error()
		}
		return resp
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		resp.StatusMessage = "investment database open failed: " + err.Error()
		return resp
	}
	defer db.Close()

	if err := db.PingContext(ctx); err != nil {
		resp.StatusMessage = "investment database ping failed: " + err.Error()
		return resp
	}

	activeSources := activeInvestmentSources(dbPath)

	snapshot, snapshots, err := querySnapshots(ctx, db)
	if err != nil {
		resp.StatusMessage = "snapshot query failed: " + err.Error()
		return resp
	}
	sources, sourceSummary, err := querySourceHealth(ctx, db, activeSources)
	if err != nil {
		resp.StatusMessage = "source query failed: " + err.Error()
		return resp
	}
	features, featureSummary, err := queryFeatures(ctx, db)
	if err != nil {
		resp.StatusMessage = "feature query failed: " + err.Error()
		return resp
	}
	events, eventSummary, err := queryEvents(ctx, db)
	if err != nil {
		resp.StatusMessage = "event query failed: " + err.Error()
		return resp
	}

	resp.Available = true
	resp.Status = "ok"
	resp.StatusMessage = "investment data loaded"
	resp.Snapshot = snapshot
	resp.RecentSnapshots = snapshots
	resp.SourceHealth = sources
	resp.FeatureRows = features
	resp.EventRows = events
	resp.Summary = investmentSummary{
		SnapshotDate:         snapshot.SnapshotDate,
		SnapshotStatus:       snapshot.Status,
		DBHash:               snapshot.DBHash,
		FeaturesHash:         snapshot.FeaturesHash,
		DataStartDate:        snapshot.DataStartDate,
		DataEndDate:          snapshot.DataEndDate,
		MissingRate:          snapshot.MissingRate,
		FeatureRows:          featureSummary.totalRows,
		EventRows:            eventSummary.totalRows,
		OpenStopEvents:       eventSummary.openStop,
		OpenWarnEvents:       eventSummary.openWarn,
		FailFetches:          sourceSummary.failFetches,
		PartialFetches:       sourceSummary.partialFetches,
		StaleSources:         sourceSummary.staleSources,
		LatestFeatureWeekEnd: featureSummary.latestWeekEnd,
	}
	return resp
}

func activeInvestmentSources(dbPath string) map[string]struct{} {
	root := filepath.Dir(filepath.Dir(dbPath))
	candidates := []string{
		filepath.Join(root, "config", "instruments.yml"),
		filepath.Join(root, "config", "sources.yml"),
		filepath.Join(root, "config", "calendars.yml"),
	}
	active := map[string]struct{}{}
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var parsed map[string]any
		if err := json.Unmarshal(data, &parsed); err != nil {
			continue
		}
		for _, key := range []string{"instruments", "macro_sources", "calendar_sources"} {
			items, ok := parsed[key].([]any)
			if !ok {
				continue
			}
			for _, item := range items {
				m, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if sourceName, ok := m["source_name"].(string); ok && strings.TrimSpace(sourceName) != "" {
					active[sourceName] = struct{}{}
				}
			}
		}
	}
	return active
}

type sourceHealthSummary struct {
	failFetches    int
	partialFetches int
	staleSources   int
}

type featureSummary struct {
	totalRows     int
	latestWeekEnd string
}

type eventSummary struct {
	totalRows int
	openStop  int
	openWarn  int
}

func querySnapshots(ctx context.Context, db *sql.DB) (*investmentSnapshot, []investmentSnapshot, error) {
	rows, err := db.QueryContext(ctx, `
SELECT snapshot_date, COALESCE(snapshot_path, ''), COALESCE(db_hash, ''), COALESCE(features_hash, ''),
       COALESCE(data_start_date, ''), COALESCE(data_end_date, ''), COALESCE(missing_rate, 0), COALESCE(status, ''), COALESCE(notes, ''), COALESCE(created_at, '')
  FROM snapshot_registry
 ORDER BY COALESCE(created_at, snapshot_date) DESC
 LIMIT 5`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var items []investmentSnapshot
	for rows.Next() {
		var item investmentSnapshot
		if err := rows.Scan(&item.SnapshotDate, &item.SnapshotPath, &item.DBHash, &item.FeaturesHash, &item.DataStartDate, &item.DataEndDate, &item.MissingRate, &item.Status, &item.Notes, &item.CreatedAt); err != nil {
			return nil, nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(items) == 0 {
		return &investmentSnapshot{}, items, nil
	}
	return &items[0], items, nil
}

func querySourceHealth(ctx context.Context, db *sql.DB, activeSources map[string]struct{}) ([]investmentSourceHealth, sourceHealthSummary, error) {
	rows, err := db.QueryContext(ctx, `
SELECT source_name, status, COALESCE(requested_at, ''), COALESCE(finished_at, ''), COALESCE(rows_fetched, 0), COALESCE(error_message, '')
  FROM source_fetch_log
 ORDER BY COALESCE(finished_at, requested_at) DESC
 LIMIT 300`)
	if err != nil {
		return nil, sourceHealthSummary{}, err
	}
	defer rows.Close()

	type agg struct {
		investmentSourceHealth
		latestSeen time.Time
	}
	aggs := map[string]*agg{}
	now := time.Now().UTC()
	for rows.Next() {
		var sourceName, status, requestedAt, finishedAt, errorMessage string
		var rowsFetched int64
		if err := rows.Scan(&sourceName, &status, &requestedAt, &finishedAt, &rowsFetched, &errorMessage); err != nil {
			return nil, sourceHealthSummary{}, err
		}
		if len(activeSources) > 0 {
			if _, ok := activeSources[sourceName]; !ok {
				continue
			}
		}
		key := sourceName
		item := aggs[key]
		if item == nil {
			item = &agg{investmentSourceHealth: investmentSourceHealth{SourceName: sourceName}}
			aggs[key] = item
		}
		switch strings.ToLower(status) {
		case "success":
			item.SuccessCount++
		case "partial":
			item.PartialCount++
		case "fail":
			item.FailCount++
		}
		if item.LatestStatus == "" {
			item.LatestStatus = status
			item.LastFetchAt = firstNonEmpty(finishedAt, requestedAt)
			item.RowsFetched = rowsFetched
			item.ErrorMessage = errorMessage
			if parsed, err := time.Parse(time.RFC3339, item.LastFetchAt); err == nil {
				item.latestSeen = parsed
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, sourceHealthSummary{}, err
	}

	items := make([]investmentSourceHealth, 0, len(aggs))
	summary := sourceHealthSummary{}
	for _, item := range aggs {
		if item.FailCount > 0 {
			summary.failFetches++
		}
		if item.PartialCount > 0 {
			summary.partialFetches++
		}
		if item.LastFetchAt == "" {
			summary.staleSources++
		} else if parsed, err := time.Parse(time.RFC3339, item.LastFetchAt); err != nil || now.Sub(parsed) > 48*time.Hour {
			summary.staleSources++
		}
		items = append(items, item.investmentSourceHealth)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].SourceName == items[j].SourceName {
			return items[i].LatestStatus < items[j].LatestStatus
		}
		return items[i].SourceName < items[j].SourceName
	})
	return items, summary, nil
}

func queryFeatures(ctx context.Context, db *sql.DB) ([]investmentFeatureRow, featureSummary, error) {
	rows, err := db.QueryContext(ctx, `
SELECT COALESCE(i.symbol, CAST(f.instrument_id AS TEXT)),
       COALESCE(f.week_end, ''),
       f.ret_1w, f.ret_12w, f.vol_12w, f.event_risk_score,
       f.boj_flag, f.cpi_flag, f.fomc_flag, f.employment_flag
  FROM feature_weekly f
  LEFT JOIN instruments i ON i.instrument_id = f.instrument_id
 ORDER BY f.week_end DESC, f.instrument_id
 LIMIT 50`)
	if err != nil {
		return nil, featureSummary{}, err
	}
	defer rows.Close()

	items := make([]investmentFeatureRow, 0, 50)
	summary := featureSummary{}
	for rows.Next() {
		var item investmentFeatureRow
		var ret1, ret12, vol12, risk sql.NullFloat64
		var boj, cpi, fomc, emp sql.NullInt64
		if err := rows.Scan(&item.Instrument, &item.WeekEnd, &ret1, &ret12, &vol12, &risk, &boj, &cpi, &fomc, &emp); err != nil {
			return nil, featureSummary{}, err
		}
		item.Ret1W = nullFloat64Ptr(ret1)
		item.Ret12W = nullFloat64Ptr(ret12)
		item.Vol12W = nullFloat64Ptr(vol12)
		item.EventRiskScore = nullFloat64Ptr(risk)
		item.Flags = joinFlags(map[string]sql.NullInt64{
			"BOJ":        boj,
			"CPI":        cpi,
			"FOMC":       fomc,
			"EMPLOYMENT": emp,
		})
		items = append(items, item)
		if summary.latestWeekEnd == "" {
			summary.latestWeekEnd = item.WeekEnd
		}
		summary.totalRows++
	}
	if err := rows.Err(); err != nil {
		return nil, featureSummary{}, err
	}
	return items, summary, nil
}

func queryEvents(ctx context.Context, db *sql.DB) ([]investmentEventRow, eventSummary, error) {
	rows, err := db.QueryContext(ctx, `
SELECT COALESCE(event_ts, ''), COALESCE(level, ''), COALESCE(scope, ''), COALESCE(reason, ''), event_risk_score, COALESCE(resolved_at, '')
  FROM event_log
 ORDER BY COALESCE(event_ts, '') DESC
 LIMIT 20`)
	if err != nil {
		return nil, eventSummary{}, err
	}
	defer rows.Close()

	items := make([]investmentEventRow, 0, 20)
	summary := eventSummary{}
	for rows.Next() {
		var item investmentEventRow
		var risk sql.NullFloat64
		if err := rows.Scan(&item.EventTS, &item.Level, &item.Scope, &item.Reason, &risk, &item.ResolvedAt); err != nil {
			return nil, eventSummary{}, err
		}
		item.EventRiskScore = nullFloat64Ptr(risk)
		items = append(items, item)
		summary.totalRows++
		if strings.EqualFold(item.Level, "stop") && strings.TrimSpace(item.ResolvedAt) == "" {
			summary.openStop++
		}
		if strings.EqualFold(item.Level, "warn") && strings.TrimSpace(item.ResolvedAt) == "" {
			summary.openWarn++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, eventSummary{}, err
	}
	return items, summary, nil
}

func nullFloat64Ptr(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	x := v.Float64
	return &x
}

func joinFlags(flags map[string]sql.NullInt64) string {
	items := make([]string, 0, len(flags))
	for name, value := range flags {
		if value.Valid && value.Int64 != 0 {
			items = append(items, name)
		}
	}
	sort.Strings(items)
	return strings.Join(items, ", ")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
