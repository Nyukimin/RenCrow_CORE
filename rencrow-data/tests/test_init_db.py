from __future__ import annotations

import json
import sqlite3
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "src"))

from rencrow_data import db


class InitDBTest(unittest.TestCase):
    def test_init_db_creates_all_tables_and_blocks_nisa_scope(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            con = db.connect(tmp_path / "rencrow.db")
            db.init_schema(con)
            tables = {
                row[0]
                for row in con.execute(
                    "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"
                ).fetchall()
            }
            expected = {
                "instruments",
                "source_fetch_log",
                "cli_run_log",
                "price_raw",
                "corporate_action",
                "macro_series",
                "economic_calendar",
                "etf_holding_snapshot",
                "feature_weekly",
                "event_log",
                "snapshot_registry",
                "decision_log",
                "paper_trade_log",
                "order_log",
                "tax_lot_log",
                "data_quality_check",
                "strategy_version",
                "backtest_run",
                "backtest_metric",
                "risk_check_result",
                "weekly_signal",
                "llm_audit_log",
            }
            self.assertLessEqual(expected, tables)
            self.assertEqual(
                con.execute("SELECT COUNT(*) FROM strategy_version WHERE strategy_id='weekly_etf_rotation_v1'").fetchone()[0],
                1,
            )
            strategy_config = json.loads(
                con.execute("SELECT config_json FROM strategy_version WHERE strategy_id='weekly_etf_rotation_v1'").fetchone()[0]
            )
            self.assertEqual(strategy_config["event_veto_threshold"], 0.7)
            cli_columns = {row[1] for row in con.execute("PRAGMA table_info(cli_run_log)").fetchall()}
            self.assertIn("exit_code", cli_columns)
            paper_columns = {row[1] for row in con.execute("PRAGMA table_info(paper_trade_log)").fetchall()}
            self.assertIn("snapshot_id", paper_columns)
            self.assertIn("fill_model", paper_columns)
            self.assertIn("target_weight", paper_columns)
            self.assertIn("notional", paper_columns)
            self.assertIn("estimated_cost", paper_columns)
            self.assertIn("slippage", paper_columns)
            decision_columns = {row[1] for row in con.execute("PRAGMA table_info(decision_log)").fetchall()}
            self.assertIn("approval_reason", decision_columns)
            llm_columns = {row[1] for row in con.execute("PRAGMA table_info(llm_audit_log)").fetchall()}
            self.assertIn("uncertainty_flag", llm_columns)
            self.assertIn("decision_id", llm_columns)
            fetch_columns = {row[1] for row in con.execute("PRAGMA table_info(source_fetch_log)").fetchall()}
            self.assertIn("usage_terms", fetch_columns)
            feature_columns = {row[1] for row in con.execute("PRAGMA table_info(feature_weekly)").fetchall()}
            self.assertIn("ret_26w", feature_columns)
            self.assertIn("feature_config_hash", feature_columns)
            event_columns = {row[1] for row in con.execute("PRAGMA table_info(event_log)").fetchall()}
            self.assertIn("snapshot_id", event_columns)

            db.upsert_instruments(
                con,
                [
                    {
                        "symbol": "1306.T",
                        "name": "TOPIX ETF",
                        "asset_type": "ETF",
                        "venue": "TSE",
                        "currency": "JPY",
                        "first_date": "2001-01-01",
                    }
                ],
            )
            self.assertEqual(con.execute("SELECT COUNT(*) FROM instruments").fetchone()[0], 1)
            with self.assertRaises(sqlite3.IntegrityError):
                con.execute("INSERT INTO decision_log(decision_date, account_scope) VALUES ('2026-01-01', 'nisa')")
            with self.assertRaises(sqlite3.IntegrityError):
                con.execute("INSERT INTO order_log(decision_id, side, quantity, status) VALUES (1, 'BUY', 1, 'submitted')")
            with self.assertRaises(sqlite3.IntegrityError):
                con.execute("INSERT INTO tax_lot_log(account_scope, instrument_id, quantity) VALUES (NULL, 1, 1)")
            with self.assertRaises(sqlite3.IntegrityError):
                con.execute("INSERT INTO tax_lot_log(account_scope, instrument_id, quantity) VALUES ('nisa', 1, 1)")
            con.execute("INSERT INTO tax_lot_log(account_scope, instrument_id, quantity) VALUES ('taxable', 1, 1)")
            with self.assertRaises(sqlite3.IntegrityError):
                con.execute("UPDATE tax_lot_log SET account_scope='nisa'")

            fetch_id = db.start_fetch(con, "unit_source", "unit:endpoint")
            usage_terms = con.execute("SELECT usage_terms FROM source_fetch_log WHERE fetch_id=?", (fetch_id,)).fetchone()[0]
            self.assertIn("usage_terms_missing", usage_terms)
            self.assertIn("internal_research_only", usage_terms)
            self.assertIn("no_redistribution", usage_terms)

    def test_config_has_broad_market_and_macro_universe(self) -> None:
        instruments = json.loads((ROOT / "config" / "instruments.yml").read_text(encoding="utf-8"))["instruments"]
        symbols = {item["symbol"] for item in instruments}
        required_symbols = {
            "SPY",
            "QQQ",
            "IWM",
            "TLT",
            "GLD",
            "VOO",
            "VT",
            "VTI",
            "QQQM",
            "SPLG",
            "SMH",
            "XBI",
            "XLC",
            "XLB",
            "XLRE",
            "XOP",
            "BIL",
            "SLV",
            "USO",
            "^GSPC",
            "^IXIC",
            "^DJI",
            "^RUT",
            "^N225",
            "TSM",
            "ASML",
            "AMD",
            "ORCL",
            "COST",
            "WMT",
            "KO",
            "DIS",
            "EFA",
            "EEM",
            "DIA",
            "AGG",
            "BND",
            "IEF",
            "SHY",
            "VNQ",
            "XLF",
            "XLK",
            "XLE",
            "XLV",
            "XLI",
            "XLP",
            "XLY",
            "XLU",
            "HYG",
            "LQD",
            "TIP",
            "IVV",
            "AAPL",
            "MSFT",
            "NVDA",
            "AMZN",
            "GOOGL",
            "META",
            "TSLA",
            "JPM",
            "XOM",
            "UNH",
            "BTC-USD",
            "ETH-USD",
            "SOL-USD",
            "XRP-USD",
            "DOGE-USD",
        }
        self.assertTrue(required_symbols.issubset(symbols))
        self.assertGreaterEqual(len(symbols), 60)

        sources = json.loads((ROOT / "config" / "sources.yml").read_text(encoding="utf-8"))["macro_sources"]
        series_codes = {item["series_code"] for item in sources if "series_code" in item}
        required_series = {
            "DGS10",
            "USDJPY_BOJ",
            "VIX",
            "IRX",
            "FVX",
            "TYX",
            "DXY",
            "GC",
            "CL",
        }
        self.assertTrue(required_series.issubset(series_codes))


if __name__ == "__main__":
    unittest.main()
