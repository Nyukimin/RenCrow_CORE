from __future__ import annotations

import json
import sqlite3
from pathlib import Path
from typing import Iterable

from .timeutil import unique_id, utcnow_iso


SCHEMA_SQL = """
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS instruments (
  instrument_id INTEGER PRIMARY KEY,
  symbol TEXT NOT NULL,
  name TEXT,
  asset_type TEXT,
  venue TEXT,
  currency TEXT,
  timezone TEXT,
  active INTEGER DEFAULT 1,
  first_date TEXT,
  last_date TEXT,
  created_at TEXT DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(symbol, venue, first_date)
);

CREATE TABLE IF NOT EXISTS source_fetch_log (
  fetch_id INTEGER PRIMARY KEY,
  source_name TEXT NOT NULL,
  endpoint TEXT,
  requested_at TEXT NOT NULL,
  finished_at TEXT,
  status TEXT NOT NULL,
  http_status INTEGER,
  rows_fetched INTEGER,
  checksum TEXT,
  retry_count INTEGER DEFAULT 0,
  error_message TEXT,
  raw_cache_path TEXT,
  usage_terms TEXT
);

CREATE TABLE IF NOT EXISTS cli_run_log (
  run_id TEXT PRIMARY KEY,
  cli_name TEXT NOT NULL,
  started_at TEXT NOT NULL,
  finished_at TEXT,
  status TEXT NOT NULL,
  target_count INTEGER DEFAULT 0,
  success_count INTEGER DEFAULT 0,
  partial_count INTEGER DEFAULT 0,
  fail_count INTEGER DEFAULT 0,
  exit_code INTEGER,
  db_path TEXT,
  snapshot_id TEXT,
  config_hash TEXT,
  detail_json TEXT
);

CREATE TABLE IF NOT EXISTS price_raw (
  instrument_id INTEGER NOT NULL,
  trade_date TEXT NOT NULL,
  open REAL,
  high REAL,
  low REAL,
  close REAL,
  adj_close REAL,
  volume REAL,
  source_name TEXT NOT NULL,
  fetch_id INTEGER,
  created_at TEXT DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (instrument_id, trade_date, source_name)
);

CREATE TABLE IF NOT EXISTS corporate_action (
  instrument_id INTEGER NOT NULL,
  action_date TEXT NOT NULL,
  action_type TEXT NOT NULL,
  value REAL,
  currency TEXT,
  source_name TEXT NOT NULL,
  fetch_id INTEGER,
  context_json TEXT,
  PRIMARY KEY (instrument_id, action_date, action_type, source_name)
);

CREATE TABLE IF NOT EXISTS macro_series (
  series_code TEXT NOT NULL,
  obs_date TEXT NOT NULL,
  value REAL,
  vintage_date TEXT NOT NULL DEFAULT '',
  release_date TEXT,
  source_name TEXT NOT NULL,
  fetch_id INTEGER,
  unit TEXT,
  PRIMARY KEY (series_code, obs_date, vintage_date, source_name)
);

CREATE TABLE IF NOT EXISTS economic_calendar (
  event_id INTEGER PRIMARY KEY,
  event_date TEXT NOT NULL,
  event_time_utc TEXT,
  country TEXT,
  category TEXT,
  event_name TEXT NOT NULL,
  source_name TEXT NOT NULL,
  importance TEXT,
  last_checked_at TEXT,
  context_json TEXT
);

CREATE TABLE IF NOT EXISTS etf_holding_snapshot (
  instrument_id INTEGER NOT NULL,
  snapshot_date TEXT NOT NULL,
  constituent_code TEXT NOT NULL,
  constituent_name TEXT,
  weight REAL,
  quantity REAL,
  sector TEXT,
  source_name TEXT NOT NULL,
  fetch_id INTEGER,
  PRIMARY KEY (instrument_id, snapshot_date, constituent_code, source_name)
);

CREATE TABLE IF NOT EXISTS feature_weekly (
  instrument_id INTEGER NOT NULL,
  week_end TEXT NOT NULL,
  close_adj_jpy REAL,
  ret_1w REAL,
  ret_4w REAL,
  ret_12w REAL,
  ret_12w_skip1 REAL,
  ret_26w REAL,
  vol_12w REAL,
  drawdown_26w REAL,
  ma_4w_gap REAL,
  ma_12w_gap REAL,
  volume_change_4w REAL,
  fx_ret_1w REAL,
  us10y_change_1w REAL,
  boj_flag INTEGER DEFAULT 0,
  cpi_flag INTEGER DEFAULT 0,
  fomc_flag INTEGER DEFAULT 0,
  employment_flag INTEGER DEFAULT 0,
  holdings_turnover REAL,
  event_risk_score REAL DEFAULT 0,
  feature_config_hash TEXT,
  source_snapshot_id INTEGER,
  created_at TEXT DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (instrument_id, week_end)
);

CREATE TABLE IF NOT EXISTS event_log (
  event_id INTEGER PRIMARY KEY,
  event_ts TEXT NOT NULL,
  scope TEXT NOT NULL,
  level TEXT NOT NULL,
  reason TEXT NOT NULL,
  value REAL,
  event_risk_score REAL,
  context_json TEXT,
  snapshot_id INTEGER,
  resolved_at TEXT,
  resolution_note TEXT
);

CREATE TABLE IF NOT EXISTS snapshot_registry (
  snapshot_id INTEGER PRIMARY KEY,
  snapshot_date TEXT NOT NULL UNIQUE,
  snapshot_path TEXT,
  db_hash TEXT,
  features_hash TEXT,
  source_summary_json TEXT,
  data_start_date TEXT,
  data_end_date TEXT,
  missing_rate REAL,
  event_state_json TEXT,
  status TEXT,
  notes TEXT,
  created_at TEXT DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS decision_log (
  decision_id INTEGER PRIMARY KEY,
  snapshot_id INTEGER,
  decision_date TEXT,
  account_scope TEXT CHECK(account_scope IS NULL OR account_scope IN ('taxable', 'paper')),
  strategy_name TEXT,
  candidate_json TEXT,
  veto_json TEXT,
  approved INTEGER DEFAULT 0,
  approver TEXT,
  approved_at TEXT,
  approval_reason TEXT,
  created_at TEXT DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS paper_trade_log (
  paper_trade_id INTEGER PRIMARY KEY,
  decision_id INTEGER,
  snapshot_id INTEGER,
  instrument_id INTEGER,
  side TEXT,
  quantity REAL,
  decision_price REAL,
  simulated_fill_price REAL,
  fill_model TEXT,
  cost_bps REAL,
  target_weight REAL,
  notional REAL,
  estimated_cost REAL,
  slippage REAL,
  status TEXT,
  created_at TEXT DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS order_log (
  order_id INTEGER PRIMARY KEY,
  decision_id INTEGER,
  broker_order_id TEXT,
  instrument_id INTEGER,
  side TEXT,
  order_type TEXT,
  quantity REAL,
  limit_price REAL,
  status TEXT,
  submitted_at TEXT,
  filled_at TEXT,
  fill_price REAL,
  error_message TEXT
);

CREATE TRIGGER IF NOT EXISTS block_order_log_insert_mvp
BEFORE INSERT ON order_log
BEGIN
  SELECT RAISE(ABORT, 'live order logging is disabled in the initial MVP');
END;

CREATE TABLE IF NOT EXISTS tax_lot_log (
  tax_lot_id INTEGER PRIMARY KEY,
  account_scope TEXT CHECK(account_scope IS NULL OR account_scope = 'taxable'),
  instrument_id INTEGER,
  acquired_date TEXT,
  quantity REAL,
  acquisition_price REAL,
  disposed_date TEXT,
  disposal_price REAL,
  realized_pnl REAL,
  source_order_id INTEGER,
  created_at TEXT DEFAULT CURRENT_TIMESTAMP
);

CREATE TRIGGER IF NOT EXISTS enforce_tax_lot_taxable_scope_insert
BEFORE INSERT ON tax_lot_log
WHEN NEW.account_scope IS NULL OR NEW.account_scope <> 'taxable'
BEGIN
  SELECT RAISE(ABORT, 'tax_lot_log is restricted to taxable account scope');
END;

CREATE TRIGGER IF NOT EXISTS enforce_tax_lot_taxable_scope_update
BEFORE UPDATE OF account_scope ON tax_lot_log
WHEN NEW.account_scope IS NULL OR NEW.account_scope <> 'taxable'
BEGIN
  SELECT RAISE(ABORT, 'tax_lot_log is restricted to taxable account scope');
END;

CREATE TABLE IF NOT EXISTS data_quality_check (
  check_id INTEGER PRIMARY KEY,
  run_id TEXT NOT NULL,
  instrument_id INTEGER,
  check_date TEXT NOT NULL,
  check_type TEXT NOT NULL,
  severity TEXT NOT NULL CHECK(severity IN ('info', 'warning', 'blocker')),
  status TEXT NOT NULL CHECK(status IN ('pass', 'fail', 'partial')),
  metric_value REAL,
  detail_json TEXT,
  created_at TEXT DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS strategy_version (
  strategy_id TEXT PRIMARY KEY,
  strategy_name TEXT,
  version TEXT,
  config_hash TEXT,
  config_json TEXT,
  active INTEGER DEFAULT 1,
  created_at TEXT DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS backtest_run (
  backtest_id TEXT PRIMARY KEY,
  strategy_id TEXT,
  snapshot_id TEXT,
  start_date TEXT,
  end_date TEXT,
  mode TEXT,
  cost_bps REAL,
  slippage_bps REAL,
  tax_mode TEXT,
  status TEXT,
  result_json TEXT,
  created_at TEXT DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS backtest_metric (
  metric_id INTEGER PRIMARY KEY,
  backtest_id TEXT,
  split_name TEXT,
  metric_name TEXT,
  metric_value REAL,
  created_at TEXT DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS risk_check_result (
  risk_check_id TEXT PRIMARY KEY,
  snapshot_id TEXT,
  strategy_id TEXT,
  decision_id TEXT,
  status TEXT,
  max_dd_check TEXT,
  weekly_loss_check TEXT,
  concentration_check TEXT,
  volatility_check TEXT,
  event_check TEXT,
  detail_json TEXT,
  created_at TEXT DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS weekly_signal (
  signal_id TEXT PRIMARY KEY,
  snapshot_id TEXT,
  strategy_id TEXT,
  week_end TEXT,
  instrument_id INTEGER,
  rank INTEGER,
  target_weight REAL,
  raw_score REAL,
  adjusted_score REAL,
  vetoed INTEGER DEFAULT 0,
  reason_json TEXT,
  created_at TEXT DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS llm_audit_log (
  llm_log_id TEXT PRIMARY KEY,
  snapshot_id TEXT,
  decision_id INTEGER,
  task_type TEXT,
  model TEXT,
  prompt_version TEXT,
  input_hash TEXT,
  output_hash TEXT,
  output_path TEXT,
  uncertainty_flag INTEGER DEFAULT 0,
  created_at TEXT DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_instruments_symbol ON instruments(symbol);
CREATE INDEX IF NOT EXISTS idx_instruments_asset_type ON instruments(asset_type);
CREATE INDEX IF NOT EXISTS idx_instruments_active ON instruments(active);
CREATE INDEX IF NOT EXISTS idx_fetch_source_requested ON source_fetch_log(source_name, requested_at);
CREATE INDEX IF NOT EXISTS idx_fetch_status ON source_fetch_log(status);
CREATE INDEX IF NOT EXISTS idx_fetch_finished ON source_fetch_log(finished_at);
CREATE INDEX IF NOT EXISTS idx_cli_run_name ON cli_run_log(cli_name);
CREATE INDEX IF NOT EXISTS idx_cli_run_started ON cli_run_log(started_at);
CREATE INDEX IF NOT EXISTS idx_cli_run_status ON cli_run_log(status);
CREATE INDEX IF NOT EXISTS idx_cli_run_snapshot ON cli_run_log(snapshot_id);
CREATE INDEX IF NOT EXISTS idx_price_trade_date ON price_raw(trade_date);
CREATE INDEX IF NOT EXISTS idx_price_fetch ON price_raw(fetch_id);
CREATE INDEX IF NOT EXISTS idx_macro_series_obs ON macro_series(series_code, obs_date);
CREATE INDEX IF NOT EXISTS idx_macro_release ON macro_series(release_date);
CREATE INDEX IF NOT EXISTS idx_calendar_date ON economic_calendar(event_date);
CREATE INDEX IF NOT EXISTS idx_calendar_category_importance ON economic_calendar(category, importance);
CREATE INDEX IF NOT EXISTS idx_feature_week_end ON feature_weekly(week_end);
CREATE INDEX IF NOT EXISTS idx_feature_risk ON feature_weekly(event_risk_score);
CREATE INDEX IF NOT EXISTS idx_event_ts ON event_log(event_ts);
CREATE INDEX IF NOT EXISTS idx_event_level ON event_log(level);
CREATE INDEX IF NOT EXISTS idx_event_reason ON event_log(reason);
CREATE INDEX IF NOT EXISTS idx_event_resolved ON event_log(resolved_at);
CREATE INDEX IF NOT EXISTS idx_snapshot_date ON snapshot_registry(snapshot_date);
CREATE INDEX IF NOT EXISTS idx_snapshot_status ON snapshot_registry(status);
CREATE INDEX IF NOT EXISTS idx_decision_date ON decision_log(decision_date);
CREATE INDEX IF NOT EXISTS idx_decision_snapshot ON decision_log(snapshot_id);
CREATE INDEX IF NOT EXISTS idx_decision_account ON decision_log(account_scope);
CREATE INDEX IF NOT EXISTS idx_quality_run ON data_quality_check(run_id);
CREATE INDEX IF NOT EXISTS idx_quality_date ON data_quality_check(check_date);
CREATE INDEX IF NOT EXISTS idx_quality_status ON data_quality_check(status, severity);
CREATE INDEX IF NOT EXISTS idx_quality_instrument ON data_quality_check(instrument_id);
CREATE INDEX IF NOT EXISTS idx_strategy_active ON strategy_version(active);
CREATE INDEX IF NOT EXISTS idx_backtest_strategy ON backtest_run(strategy_id);
CREATE INDEX IF NOT EXISTS idx_backtest_snapshot ON backtest_run(snapshot_id);
CREATE INDEX IF NOT EXISTS idx_backtest_metric_run ON backtest_metric(backtest_id);
CREATE INDEX IF NOT EXISTS idx_risk_snapshot ON risk_check_result(snapshot_id);
CREATE INDEX IF NOT EXISTS idx_risk_strategy ON risk_check_result(strategy_id);
CREATE INDEX IF NOT EXISTS idx_risk_status ON risk_check_result(status);
CREATE INDEX IF NOT EXISTS idx_weekly_signal_snapshot ON weekly_signal(snapshot_id);
CREATE INDEX IF NOT EXISTS idx_weekly_signal_strategy ON weekly_signal(strategy_id);
CREATE INDEX IF NOT EXISTS idx_weekly_signal_week ON weekly_signal(week_end);
CREATE INDEX IF NOT EXISTS idx_llm_audit_snapshot ON llm_audit_log(snapshot_id);
CREATE INDEX IF NOT EXISTS idx_llm_audit_task ON llm_audit_log(task_type);
"""


DEFAULT_STRATEGY_CONFIG_JSON = (
    '{"cash_proxy":"SHY","drawdown_penalty":0.25,"event_veto_threshold":0.7,"score_min":-999.0,'
    '"top_n":1,"universe":["SPY","IEF","TLT","GLD","SHY"],"volatility_penalty":0.5}'
)


def connect(db_path: str | Path) -> sqlite3.Connection:
    path = Path(db_path)
    path.parent.mkdir(parents=True, exist_ok=True)
    con = sqlite3.connect(path)
    con.row_factory = sqlite3.Row
    con.execute("PRAGMA journal_mode=WAL")
    con.execute("PRAGMA foreign_keys=ON")
    return con


def init_schema(con: sqlite3.Connection) -> None:
    con.executescript(SCHEMA_SQL)
    _ensure_column(con, "feature_weekly", "ret_12w_skip1", "REAL")
    _ensure_column(con, "feature_weekly", "ret_26w", "REAL")
    _ensure_column(con, "feature_weekly", "feature_config_hash", "TEXT")
    _ensure_column(con, "cli_run_log", "exit_code", "INTEGER")
    _ensure_column(con, "paper_trade_log", "snapshot_id", "INTEGER")
    _ensure_column(con, "paper_trade_log", "fill_model", "TEXT")
    _ensure_column(con, "paper_trade_log", "target_weight", "REAL")
    _ensure_column(con, "paper_trade_log", "notional", "REAL")
    _ensure_column(con, "paper_trade_log", "estimated_cost", "REAL")
    _ensure_column(con, "paper_trade_log", "slippage", "REAL")
    _ensure_column(con, "decision_log", "approval_reason", "TEXT")
    _ensure_column(con, "llm_audit_log", "uncertainty_flag", "INTEGER DEFAULT 0")
    _ensure_column(con, "llm_audit_log", "decision_id", "INTEGER")
    _ensure_column(con, "event_log", "snapshot_id", "INTEGER")
    _ensure_column(con, "source_fetch_log", "usage_terms", "TEXT")
    con.execute("CREATE INDEX IF NOT EXISTS idx_paper_trade_snapshot ON paper_trade_log(snapshot_id)")
    con.execute("CREATE INDEX IF NOT EXISTS idx_event_snapshot ON event_log(snapshot_id)")
    con.execute(
        """
        INSERT OR IGNORE INTO strategy_version(strategy_id, strategy_name, version, config_hash, config_json, active)
        VALUES (
          'weekly_etf_rotation_v1',
          'Weekly ETF Rotation',
          '1',
          'weekly_etf_rotation_v1_default',
          ?,
          1
        )
        """,
        (DEFAULT_STRATEGY_CONFIG_JSON,),
    )
    con.execute(
        """
        UPDATE strategy_version
           SET config_json=?
         WHERE strategy_id='weekly_etf_rotation_v1'
           AND config_hash='weekly_etf_rotation_v1_default'
        """,
        (DEFAULT_STRATEGY_CONFIG_JSON,),
    )
    con.commit()


def start_cli_run(
    con: sqlite3.Connection,
    cli_name: str,
    db_path: str | Path,
    *,
    snapshot_id: str | None = None,
    config_hash: str | None = None,
) -> dict[str, object]:
    run = {
        "run_id": unique_id("cli"),
        "cli_name": cli_name,
        "started_at": utcnow_iso(),
        "db_path": str(db_path),
        "snapshot_id": snapshot_id,
        "config_hash": config_hash,
    }
    con.execute(
        """
        INSERT INTO cli_run_log(run_id, cli_name, started_at, status, db_path, snapshot_id, config_hash)
        VALUES (?, ?, ?, 'running', ?, ?, ?)
        """,
        (run["run_id"], cli_name, run["started_at"], str(db_path), snapshot_id, config_hash),
    )
    con.commit()
    return run


def finish_cli_run(con: sqlite3.Connection, run: dict[str, object], summary: dict[str, object]) -> dict[str, object]:
    finished_at = utcnow_iso()
    summary.setdefault("cli_run_id", run["run_id"])
    summary.setdefault("run_id", run["run_id"])
    summary.setdefault("cli_name", run["cli_name"])
    summary.setdefault("started_at", run["started_at"])
    summary.setdefault("finished_at", finished_at)
    summary.setdefault("db_path", run["db_path"])
    summary.setdefault("snapshot_id", run.get("snapshot_id"))
    summary.setdefault("config_hash", run.get("config_hash"))
    status = str(summary.get("status", "success"))
    if summary.get("exit_code") is None:
        if status == "partial":
            summary["exit_code"] = 2
        elif status == "fail":
            summary["exit_code"] = 1
        else:
            summary["exit_code"] = 0
    con.execute(
        """
        UPDATE cli_run_log
           SET finished_at=?,
               status=?,
               target_count=?,
               success_count=?,
               partial_count=?,
               fail_count=?,
               exit_code=?,
               snapshot_id=?,
               config_hash=?,
               detail_json=?
         WHERE run_id=?
        """,
        (
            finished_at,
            status,
            int(summary.get("target_count") or 0),
            int(summary.get("success_count") or 0),
            int(summary.get("partial_count") or 0),
            int(summary.get("fail_count") or 0),
            int(summary.get("exit_code") or 0),
            None if summary.get("snapshot_id") is None else str(summary.get("snapshot_id")),
            None if summary.get("config_hash") is None else str(summary.get("config_hash")),
            json.dumps(summary, ensure_ascii=False, sort_keys=True, default=str),
            run["run_id"],
        ),
    )
    con.commit()
    return summary


def fail_cli_run(
    con: sqlite3.Connection,
    run: dict[str, object],
    *,
    status: str = "fail",
    fail_count: int = 1,
    error_message: str = "",
    exit_code: int | None = None,
) -> dict[str, object]:
    return finish_cli_run(
        con,
        run,
        {
            "cli_name": run["cli_name"],
            "db_path": run["db_path"],
            "status": status,
            "target_count": 1,
            "success_count": 0,
            "partial_count": 0,
            "fail_count": fail_count,
            "error_message": error_message,
            "exit_code": exit_code,
        },
    )


def _ensure_column(con: sqlite3.Connection, table_name: str, column_name: str, definition: str) -> None:
    columns = {row["name"] for row in con.execute(f"PRAGMA table_info({table_name})").fetchall()}
    if column_name not in columns:
        con.execute(f"ALTER TABLE {table_name} ADD COLUMN {column_name} {definition}")


def upsert_instrument(con: sqlite3.Connection, item: dict) -> int:
    now = utcnow_iso()
    con.execute(
        """
        INSERT INTO instruments(symbol, name, asset_type, venue, currency, timezone, active, first_date, last_date, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(symbol, venue, first_date) DO UPDATE SET
          name=excluded.name,
          asset_type=excluded.asset_type,
          currency=excluded.currency,
          timezone=excluded.timezone,
          active=excluded.active,
          last_date=excluded.last_date,
          updated_at=excluded.updated_at
        """,
        (
            item["symbol"],
            item.get("name"),
            item.get("asset_type", "ETF"),
            item.get("venue", "TSE"),
            item.get("currency", "JPY"),
            item.get("timezone", "Asia/Tokyo"),
            int(item.get("active", 1)),
            item.get("first_date", ""),
            item.get("last_date"),
            now,
            now,
        ),
    )
    row = con.execute(
        "SELECT instrument_id FROM instruments WHERE symbol=? AND venue=? AND first_date=?",
        (item["symbol"], item.get("venue", "TSE"), item.get("first_date", "")),
    ).fetchone()
    if row is None:
        raise RuntimeError(f"instrument upsert failed: {item['symbol']}")
    return int(row["instrument_id"])


def upsert_instruments(con: sqlite3.Connection, items: Iterable[dict]) -> int:
    count = 0
    for item in items:
        upsert_instrument(con, item)
        count += 1
    con.commit()
    return count


def instrument_id(con: sqlite3.Connection, symbol: str, venue: str | None = None) -> int:
    if venue:
        row = con.execute(
            "SELECT instrument_id FROM instruments WHERE symbol=? AND venue=? ORDER BY active DESC, instrument_id DESC LIMIT 1",
            (symbol, venue),
        ).fetchone()
    else:
        row = con.execute(
            "SELECT instrument_id FROM instruments WHERE symbol=? ORDER BY active DESC, instrument_id DESC LIMIT 1",
            (symbol,),
        ).fetchone()
    if row is None:
        raise KeyError(f"unknown instrument: {symbol}")
    return int(row["instrument_id"])


def _normalized_usage_terms(usage_terms: str | None) -> str:
    value = str(usage_terms or "").strip()
    if value:
        return value
    return "usage_terms_missing; internal_research_only; no_redistribution"


def start_fetch(con: sqlite3.Connection, source_name: str, endpoint: str, usage_terms: str | None = None) -> int:
    con.execute(
        "INSERT INTO source_fetch_log(source_name, endpoint, requested_at, status, usage_terms) VALUES (?, ?, ?, 'running', ?)",
        (source_name, endpoint, utcnow_iso(), _normalized_usage_terms(usage_terms)),
    )
    con.commit()
    return int(con.execute("SELECT last_insert_rowid()").fetchone()[0])


def finish_fetch(
    con: sqlite3.Connection,
    fetch_id: int,
    status: str,
    rows_fetched: int = 0,
    http_status: int | None = None,
    checksum: str | None = None,
    retry_count: int = 0,
    error_message: str | None = None,
    raw_cache_path: str | None = None,
) -> None:
    con.execute(
        """
        UPDATE source_fetch_log
        SET finished_at=?, status=?, http_status=?, rows_fetched=?, checksum=?, retry_count=?, error_message=?, raw_cache_path=?
        WHERE fetch_id=?
        """,
        (utcnow_iso(), status, http_status, rows_fetched, checksum, retry_count, error_message, raw_cache_path, fetch_id),
    )
    con.commit()


def log_event(
    con: sqlite3.Connection,
    scope: str,
    level: str,
    reason: str,
    value: float | None = None,
    event_risk_score: float | None = None,
    context_json: str | None = None,
) -> None:
    con.execute(
        """
        INSERT INTO event_log(event_ts, scope, level, reason, value, event_risk_score, context_json)
        VALUES (?, ?, ?, ?, ?, ?, ?)
        """,
        (utcnow_iso(), scope, level, reason, value, event_risk_score, context_json),
    )
    con.commit()
