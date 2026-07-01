package viewer

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/orchestrator"
)

func TestHandleInvestmentStatusUnavailableWhenDBMissing(t *testing.T) {
	h := HandleInvestmentStatus(filepath.Join(t.TempDir(), "missing.sqlite"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/investment/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	var resp investmentStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Available {
		t.Fatal("expected unavailable response for missing DB")
	}
	if resp.Status != "unavailable" {
		t.Fatalf("unexpected status: %s", resp.Status)
	}
}

func TestHandleInvestmentStatusReturnsSnapshotAndHealth(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "rencrow.db")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	stmts := []string{
		`CREATE TABLE instruments (instrument_id INTEGER PRIMARY KEY, symbol TEXT, name TEXT);`,
		`CREATE TABLE source_fetch_log (fetch_id INTEGER PRIMARY KEY, source_name TEXT, endpoint TEXT, requested_at TEXT, finished_at TEXT, status TEXT, http_status INTEGER, rows_fetched INTEGER, checksum TEXT, retry_count INTEGER DEFAULT 0, error_message TEXT, raw_cache_path TEXT);`,
		`CREATE TABLE feature_weekly (instrument_id INTEGER, week_end TEXT, ret_1w REAL, ret_12w REAL, vol_12w REAL, event_risk_score REAL, boj_flag INTEGER DEFAULT 0, cpi_flag INTEGER DEFAULT 0, fomc_flag INTEGER DEFAULT 0, employment_flag INTEGER DEFAULT 0);`,
		`CREATE TABLE event_log (event_id INTEGER PRIMARY KEY, event_ts TEXT, scope TEXT, level TEXT, reason TEXT, value REAL, event_risk_score REAL, context_json TEXT, resolved_at TEXT, resolution_note TEXT);`,
		`CREATE TABLE snapshot_registry (snapshot_id INTEGER PRIMARY KEY, snapshot_date TEXT, snapshot_path TEXT, db_hash TEXT, features_hash TEXT, source_summary_json TEXT, data_start_date TEXT, data_end_date TEXT, missing_rate REAL, event_state_json TEXT, status TEXT, notes TEXT, created_at TEXT);`,
		`INSERT INTO instruments(instrument_id, symbol, name) VALUES (1, '1306.T', 'NEXT FUNDS');`,
		`INSERT INTO source_fetch_log(source_name, requested_at, finished_at, status, rows_fetched, error_message) VALUES
		  ('csv_market', '2026-06-15T00:00:00Z', '2026-06-15T00:00:01Z', 'success', 20, ''),
		  ('csv_macro', '2026-06-14T00:00:00Z', '2026-06-14T00:00:01Z', 'partial', 4, 'missing row'),
		  ('csv_calendar', '2026-06-10T00:00:00Z', '2026-06-10T00:00:01Z', 'fail', 0, 'download failed');`,
		`INSERT INTO feature_weekly(instrument_id, week_end, ret_1w, ret_12w, vol_12w, event_risk_score, boj_flag, cpi_flag, fomc_flag, employment_flag) VALUES
		  (1, '2026-06-12', 0.012, 0.08, 0.15, 0.20, 1, 0, 0, 0),
		  (1, '2026-06-05', 0.006, 0.07, 0.14, 0.10, 0, 1, 0, 0);`,
		`INSERT INTO event_log(event_id, event_ts, scope, level, reason, event_risk_score, resolved_at) VALUES
		  (1, '2026-06-15T01:00:00Z', 'market', 'stop', 'required source stale', 0.95, NULL),
		  (2, '2026-06-15T01:30:00Z', 'calendar', 'warn', 'calendar close', 0.35, NULL),
		  (3, '2026-06-13T01:30:00Z', 'macro', 'info', 'macro updated', 0.05, '2026-06-13T02:00:00Z');`,
		`INSERT INTO snapshot_registry(snapshot_id, snapshot_date, snapshot_path, db_hash, features_hash, source_summary_json, data_start_date, data_end_date, missing_rate, event_state_json, status, notes, created_at) VALUES
		  (1, '2026-06-15', 'rencrow-data/data/snapshots/snapshot_20260615.sqlite.gz', 'dbhash', 'fhash', '[]', '2026-01-01', '2026-06-15', 0.01, '{}', 'success', 'ok', '2026-06-15T02:00:00Z');`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec stmt failed: %v", err)
		}
	}

	h := HandleInvestmentStatus(dbPath)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/investment/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	var resp investmentStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Available {
		t.Fatal("expected available response")
	}
	if resp.Snapshot == nil || resp.Snapshot.SnapshotDate != "2026-06-15" {
		t.Fatalf("unexpected snapshot: %#v", resp.Snapshot)
	}
	if len(resp.SourceHealth) != 3 {
		t.Fatalf("unexpected source health rows: %d", len(resp.SourceHealth))
	}
	if resp.Summary.OpenStopEvents != 1 || resp.Summary.OpenWarnEvents != 1 {
		t.Fatalf("unexpected event summary: %#v", resp.Summary)
	}
	if resp.Summary.FailFetches != 1 || resp.Summary.PartialFetches != 1 {
		t.Fatalf("unexpected source summary: %#v", resp.Summary)
	}
	if len(resp.FeatureRows) == 0 || len(resp.EventRows) == 0 {
		t.Fatalf("expected features and events to be returned: features=%d events=%d", len(resp.FeatureRows), len(resp.EventRows))
	}
}

func TestHandleInvestmentNotifyBroadcastsSSEEvent(t *testing.T) {
	hub := NewEventHub(10)
	ch := hub.Subscribe()
	defer hub.Unsubscribe(ch)

	h := HandleInvestmentNotify(hub)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/viewer/investment/notify", strings.NewReader(`{"phase":"snapshot","status":"success","source":"rencrow-data"}`))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case data := <-ch:
		var ev orchestrator.OrchestratorEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		if ev.Type != "investment.snapshot" || ev.From != "rencrow-data" {
			t.Fatalf("unexpected event: %#v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for investment notify event")
	}
}
