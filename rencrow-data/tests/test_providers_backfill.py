from __future__ import annotations

import sqlite3
import tempfile
import unittest
from datetime import date
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
import sys

sys.path.insert(0, str(ROOT / "src"))

from rencrow_data import db
from rencrow_data.market import save_market_item
from rencrow_data.macro import ingest_macro_source
from rencrow_data.providers import fetch_fred_csv, fetch_yahoo_history


class BackfillProviderTest(unittest.TestCase):
    def test_yahoo_history_parser_extracts_price_and_actions(self) -> None:
        payload = {
            "chart": {
                "result": [
                    {
                        "timestamp": [1717200000],
                        "indicators": {
                            "quote": [{"open": [100.0], "high": [101.0], "low": [99.0], "close": [100.5], "volume": [1234]}],
                            "adjclose": [{"adjclose": [100.4]}],
                        },
                        "events": {"dividends": {"1717200000": {"amount": 1.25}}, "splits": {"1717200000": {"splitRatio": "2/1"}}},
                        "meta": {"currency": "JPY"},
                    }
                ]
            }
        }
        with patch("rencrow_data.providers._request_json", return_value=payload):
            rows, provider, meta = fetch_yahoo_history("1306.T", date(2024, 1, 1), date(2024, 1, 31))
        self.assertEqual(provider, "yahoo")
        self.assertEqual(meta["currency"], "JPY")
        self.assertEqual(rows[0]["date"], "2024-06-01")
        self.assertEqual(rows[0]["dividend"], 1.25)
        self.assertEqual(rows[0]["split"], 2.0)

    def test_fred_csv_uses_requested_date_window(self) -> None:
        captured = {}

        def fake_request_text(url, params=None, timeout=30, retries=2):
            captured["params"] = dict(params or {})
            return "DATE,DGS10\n2024-01-01,4.10\n2024-01-02,4.20\n"

        with patch("rencrow_data.providers._request_text", side_effect=fake_request_text):
            rows, provider = fetch_fred_csv("DGS10", date(2024, 1, 1), date(2024, 1, 2))
        self.assertEqual(provider, "fred")
        self.assertEqual(captured["params"]["id"], "DGS10")
        self.assertEqual(captured["params"]["cosd"], "2024-01-01")
        self.assertEqual(captured["params"]["coed"], "2024-01-02")
        self.assertEqual(rows[0]["value"], 4.1)

    def test_online_market_backfill_writes_rows_and_actions(self) -> None:
        payload = {
            "chart": {
                "result": [
                    {
                        "timestamp": [1717200000, 1717286400],
                        "indicators": {
                            "quote": [{"open": [100.0, 101.0], "high": [101.0, 102.0], "low": [99.0, 100.0], "close": [100.5, 101.5], "volume": [1000, 2000]}],
                            "adjclose": [{"adjclose": [100.4, 101.4]}],
                        },
                        "events": {"dividends": {"1717200000": {"amount": 1.0}}},
                        "meta": {"currency": "JPY"},
                    }
                ]
            }
        }
        with tempfile.TemporaryDirectory() as td:
            con = db.connect(Path(td) / "rencrow.db")
            db.init_schema(con)
            db.upsert_instruments(
                con,
                [{"symbol": "1306.T", "venue": "TSE", "asset_type": "ETF", "currency": "JPY", "first_date": "2001-01-01"}],
            )
            item = {"symbol": "1306.T", "venue": "TSE", "currency": "JPY", "provider": "yahoo", "provider_symbol": "1306.T", "source_name": "yahoo_market"}
            with patch("rencrow_data.providers._request_json", return_value=payload):
                rows, status = save_market_item(con, item, td, mode="backfill", start_date="2024-01-01", end_date="2024-12-31")
            self.assertEqual(status, "success")
            self.assertEqual(rows, 2)
            self.assertEqual(con.execute("SELECT COUNT(*) FROM price_raw").fetchone()[0], 2)
            self.assertEqual(con.execute("SELECT COUNT(*) FROM corporate_action").fetchone()[0], 1)
            self.assertEqual(con.execute("SELECT status FROM source_fetch_log ORDER BY fetch_id DESC LIMIT 1").fetchone()[0], "success")
            usage_terms = con.execute("SELECT usage_terms FROM source_fetch_log ORDER BY fetch_id DESC LIMIT 1").fetchone()[0]
            self.assertIn("yahoo_finance", usage_terms)
            self.assertIn("no_redistribution", usage_terms)

    def test_online_macro_backfill_writes_rows(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            con = db.connect(Path(td) / "rencrow.db")
            db.init_schema(con)
            source = {"source_name": "fred_macro_dgs10", "provider": "fred", "series_code": "DGS10", "first_date": "2000-01-01"}
            with patch("rencrow_data.macro.fetch_fred_csv", return_value=([{"obs_date": "2024-01-01", "value": 4.1, "release_date": "2024-01-01", "vintage_date": "", "unit": "percent"}], "fred")):
                rows, status = ingest_macro_source(con, source, td, mode="backfill", start_date="2024-01-01", end_date="2024-12-31")
            self.assertEqual(status, "success")
            self.assertEqual(rows, 1)
            self.assertEqual(con.execute("SELECT value FROM macro_series WHERE series_code='DGS10'").fetchone()[0], 4.1)
            self.assertEqual(con.execute("SELECT status FROM source_fetch_log ORDER BY fetch_id DESC LIMIT 1").fetchone()[0], "success")
            usage_terms = con.execute("SELECT usage_terms FROM source_fetch_log ORDER BY fetch_id DESC LIMIT 1").fetchone()[0]
            self.assertIn("fred_public_data", usage_terms)
            self.assertIn("cite_source", usage_terms)

    def test_incremental_mode_uses_recent_lookback(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            con = db.connect(Path(td) / "rencrow.db")
            db.init_schema(con)
            db.upsert_instruments(
                con,
                [{"symbol": "1306.T", "venue": "TSE", "asset_type": "ETF", "currency": "JPY", "first_date": "2001-01-01"}],
            )
            iid = db.instrument_id(con, "1306.T", "TSE")
            con.execute(
                """
                INSERT INTO price_raw(instrument_id, trade_date, open, high, low, close, adj_close, volume, source_name, fetch_id)
                VALUES (?, '2024-06-10', 100, 101, 99, 100, 100, 1000, 'yahoo_market', 1)
                """,
                (iid,),
            )
            con.commit()
            item = {"symbol": "1306.T", "venue": "TSE", "currency": "JPY", "provider": "yahoo", "provider_symbol": "1306.T", "source_name": "yahoo_market"}
            captured = {}

            def fake_fetch(symbol, start_date=None, end_date=None, interval="1d"):
                captured["start_date"] = start_date.isoformat() if start_date else None
                return ([{"date": "2024-06-17", "open": 101, "high": 102, "low": 100, "close": 101.5, "adj_close": 101.5, "volume": 1200, "dividend": None, "split": None}], "yahoo", {"currency": "JPY"})

            with patch("rencrow_data.market.fetch_yahoo_history", side_effect=fake_fetch):
                rows, status = save_market_item(con, item, td, mode="incremental", lookback_days=7)
            self.assertEqual(status, "success")
            self.assertEqual(rows, 1)
            self.assertEqual(captured["start_date"], "2024-06-03")
            self.assertEqual(con.execute("SELECT COUNT(*) FROM price_raw").fetchone()[0], 2)


if __name__ == "__main__":
    unittest.main()
