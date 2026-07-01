from __future__ import annotations

import json
import sys
import tempfile
import unittest
from datetime import date, timedelta
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "src"))

from rencrow_data import db
from rencrow_data.events import detect_events, event_severity
from rencrow_data.features import build_features, feature_config_hash
from rencrow_data.market import save_market_csv


class MarketFeatureTest(unittest.TestCase):
    def test_market_ingest_logs_price_revision_and_feature_uses_adjustment(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            price_csv = tmp_path / "prices.csv"
            price_csv.write_text(
                "\n".join(
                    [
                        "date,open,high,low,close,adj_close,volume,dividend,split",
                        "2026-01-02,100,101,99,100,50,1000,0,2",
                        "2026-01-09,102,103,101,102,102,1000,0,1",
                        "2026-01-16,104,105,103,104,104,1000,0,1",
                        "2026-01-23,106,107,105,106,106,1000,0,1",
                        "2026-01-30,108,109,107,108,108,1000,0,1",
                    ]
                ),
                encoding="utf-8",
            )
            con = db.connect(tmp_path / "rencrow.db")
            db.init_schema(con)
            db.upsert_instruments(
                con,
                [
                    {
                        "symbol": "TEST",
                        "asset_type": "ETF",
                        "venue": "TSE",
                        "currency": "JPY",
                        "first_date": "2026-01-01",
                    }
                ],
            )
            item = {"symbol": "TEST", "venue": "TSE", "currency": "JPY", "source_name": "csv_market", "fixture": str(price_csv)}
            save_market_csv(con, item, tmp_path)
            build_features(con)
            row = con.execute("SELECT close_adj_jpy, feature_config_hash FROM feature_weekly WHERE week_end='2026-01-02'").fetchone()
            close = row[0]
            self.assertEqual(close, 50.0)
            self.assertEqual(row[1], feature_config_hash())
            price_csv.write_text(price_csv.read_text(encoding="utf-8").replace("2026-01-30,108", "2026-01-30,109"), encoding="utf-8")
            save_market_csv(con, item, tmp_path)
            self.assertGreater(con.execute("SELECT COUNT(*) FROM event_log WHERE reason='price_revision'").fetchone()[0], 0)

    def test_crypto_asset_type_participates_in_feature_building(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            price_csv = tmp_path / "crypto_prices.csv"
            price_csv.write_text(
                "\n".join(
                    [
                        "date,open,high,low,close,adj_close,volume,dividend,split",
                        "2026-01-02,100,101,99,100,100,1000,0,1",
                        "2026-01-09,102,103,101,102,102,1000,0,1",
                        "2026-01-16,104,105,103,104,104,1000,0,1",
                        "2026-01-23,106,107,105,106,106,1000,0,1",
                        "2026-01-30,108,109,107,108,108,1000,0,1",
                    ]
                ),
                encoding="utf-8",
            )
            con = db.connect(tmp_path / "rencrow.db")
            db.init_schema(con)
            db.upsert_instruments(
                con,
                [
                    {
                        "symbol": "BTC-USD",
                        "asset_type": "CRYPTO",
                        "venue": "YAHOO",
                        "currency": "USD",
                        "first_date": "2026-01-01",
                    }
                ],
            )
            con.execute(
                "INSERT INTO macro_series(series_code, obs_date, value, vintage_date, release_date, source_name, fetch_id, unit) VALUES ('USDJPY_BOJ', '2026-01-01', 150, '', '2026-01-01', 'csv_macro', 1, 'JPY')"
            )
            con.commit()
            item = {"symbol": "BTC-USD", "venue": "YAHOO", "currency": "USD", "source_name": "csv_market", "fixture": str(price_csv)}
            save_market_csv(con, item, tmp_path)
            build_features(con)
            iid = con.execute("SELECT instrument_id FROM instruments WHERE symbol='BTC-USD'").fetchone()[0]
            self.assertGreater(
                con.execute("SELECT COUNT(*) FROM feature_weekly WHERE instrument_id=?", (iid,)).fetchone()[0],
                0,
            )

    def test_usd_asset_features_use_only_released_fx_for_jpy_conversion(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            price_csv = tmp_path / "usd_prices.csv"
            price_csv.write_text(
                "\n".join(
                    [
                        "date,open,high,low,close,adj_close,volume,dividend,split",
                        "2026-01-02,100,101,99,100,100,1000,0,1",
                        "2026-01-09,101,102,100,101,101,1000,0,1",
                    ]
                )
                + "\n",
                encoding="utf-8",
            )
            con = db.connect(tmp_path / "rencrow.db")
            db.init_schema(con)
            db.upsert_instruments(
                con,
                [
                    {
                        "symbol": "SPY",
                        "asset_type": "ETF",
                        "venue": "US",
                        "currency": "USD",
                        "first_date": "2026-01-01",
                    }
                ],
            )
            con.executemany(
                """
                INSERT INTO macro_series(series_code, obs_date, value, vintage_date, release_date, source_name, fetch_id, unit)
                VALUES ('USDJPY_BOJ', ?, ?, ?, ?, 'csv_macro', 1, 'JPY')
                """,
                [
                    ("2026-01-02", 150.0, "released", "2026-01-02"),
                    ("2026-01-09", 160.0, "future_release", "2026-01-20"),
                ],
            )
            con.commit()

            save_market_csv(
                con,
                {"symbol": "SPY", "venue": "US", "currency": "USD", "source_name": "csv_market", "fixture": str(price_csv)},
                tmp_path,
            )
            build_features(con)

            row = con.execute(
                """
                SELECT close_adj_jpy, ret_1w
                  FROM feature_weekly
                 WHERE week_end='2026-01-09'
                """
            ).fetchone()
            self.assertAlmostEqual(row["close_adj_jpy"], 101 * 150.0)
            self.assertAlmostEqual(row["ret_1w"], 101 / 100 - 1)

    def test_feature_builds_12w_skip1_momentum(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            con = db.connect(tmp_path / "rencrow.db")
            db.init_schema(con)
            db.upsert_instruments(
                con,
                [
                    {
                        "symbol": "1306.T",
                        "asset_type": "ETF",
                        "venue": "TSE",
                        "currency": "JPY",
                        "first_date": "2026-01-01",
                    }
                ],
            )
            price_csv = tmp_path / "prices.csv"
            price_csv.write_text(
                "\n".join(
                    [
                        "date,open,high,low,close,adj_close,volume,dividend,split",
                        "2026-01-02,100,101,99,100,100,1000,0,1",
                        "2026-01-09,101,102,100,101,101,1000,0,1",
                        "2026-01-16,102,103,101,102,102,1000,0,1",
                        "2026-01-23,103,104,102,103,103,1000,0,1",
                        "2026-01-30,104,105,103,104,104,1000,0,1",
                        "2026-02-06,105,106,104,105,105,1000,0,1",
                        "2026-02-13,106,107,105,106,106,1000,0,1",
                        "2026-02-20,107,108,106,107,107,1000,0,1",
                        "2026-02-27,108,109,107,108,108,1000,0,1",
                        "2026-03-06,109,110,108,109,109,1000,0,1",
                        "2026-03-13,110,111,109,110,110,1000,0,1",
                        "2026-03-20,111,112,110,111,111,1000,0,1",
                        "2026-03-27,112,113,111,112,112,1000,0,1",
                        "2026-04-03,113,114,112,113,113,1000,0,1",
                    ]
                )
                + "\n",
                encoding="utf-8",
            )
            item = {
                "symbol": "1306.T",
                "venue": "TSE",
                "currency": "JPY",
                "source_name": "csv_market",
                "fixture": str(price_csv),
            }
            save_market_csv(con, item, tmp_path)
            build_features(con)
            row = con.execute(
                """
                SELECT ret_12w, ret_12w_skip1
                  FROM feature_weekly
                 WHERE week_end='2026-04-03'
                """
            ).fetchone()
            self.assertAlmostEqual(row["ret_12w"], 113 / 101 - 1)
            self.assertAlmostEqual(row["ret_12w_skip1"], 112 / 100 - 1)

    def test_feature_builds_26w_momentum(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            con = db.connect(tmp_path / "rencrow.db")
            db.init_schema(con)
            db.upsert_instruments(
                con,
                [
                    {
                        "symbol": "1306.T",
                        "asset_type": "ETF",
                        "venue": "TSE",
                        "currency": "JPY",
                        "first_date": "2026-01-01",
                    }
                ],
            )
            rows = ["date,open,high,low,close,adj_close,volume,dividend,split"]
            start = date(2026, 1, 2)
            for idx in range(27):
                trade_day = start + timedelta(days=7 * idx)
                close = 100 + idx
                rows.append(f"{trade_day.isoformat()},{close},{close},{close},{close},{close},1000,0,1")
            price_csv = tmp_path / "prices.csv"
            price_csv.write_text("\n".join(rows) + "\n", encoding="utf-8")

            save_market_csv(
                con,
                {"symbol": "1306.T", "venue": "TSE", "currency": "JPY", "source_name": "csv_market", "fixture": str(price_csv)},
                tmp_path,
            )
            build_features(con)
            row = con.execute(
                """
                SELECT ret_26w
                  FROM feature_weekly
                 WHERE week_end='2026-07-03'
                """
            ).fetchone()

            self.assertAlmostEqual(row["ret_26w"], 126 / 100 - 1)

    def test_feature_builds_core_momentum_macro_and_event_columns(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            con = db.connect(tmp_path / "rencrow.db")
            db.init_schema(con)
            db.upsert_instruments(
                con,
                [
                    {
                        "symbol": "1306.T",
                        "asset_type": "ETF",
                        "venue": "TSE",
                        "currency": "JPY",
                        "first_date": "2026-01-01",
                    }
                ],
            )
            rows = ["date,open,high,low,close,adj_close,volume,dividend,split"]
            start = date(2026, 1, 2)
            for idx in range(12):
                trade_day = start + timedelta(days=7 * idx)
                close = 100 + idx
                volume = 1000 + idx * 100
                rows.append(f"{trade_day.isoformat()},{close},{close},{close},{close},{close},{volume},0,1")
            price_csv = tmp_path / "prices.csv"
            price_csv.write_text("\n".join(rows) + "\n", encoding="utf-8")
            con.executemany(
                """
                INSERT INTO macro_series(series_code, obs_date, value, vintage_date, release_date, source_name, fetch_id, unit)
                VALUES (?, ?, ?, '', ?, 'csv_macro', 1, ?)
                """,
                [
                    ("USDJPY_BOJ", "2026-03-13", 150.0, "2026-03-13", "JPY"),
                    ("USDJPY_BOJ", "2026-03-20", 153.0, "2026-03-20", "JPY"),
                    ("DGS10", "2026-03-13", 4.0, "2026-03-13", "PERCENT"),
                    ("DGS10", "2026-03-20", 4.2, "2026-03-20", "PERCENT"),
                ],
            )
            con.executemany(
                """
                INSERT INTO economic_calendar(event_date, category, event_name, source_name, importance)
                VALUES (?, ?, ?, 'csv_calendar', 'high')
                """,
                [
                    ("2026-03-20", "BOJ", "BOJ decision"),
                    ("2026-03-19", "CPI", "CPI"),
                    ("2026-03-18", "FOMC", "FOMC"),
                    ("2026-03-20", "NFP", "Nonfarm Payrolls"),
                ],
            )
            con.commit()

            save_market_csv(
                con,
                {"symbol": "1306.T", "venue": "TSE", "currency": "JPY", "source_name": "csv_market", "fixture": str(price_csv)},
                tmp_path,
            )
            build_features(con)

            row = con.execute(
                """
                SELECT ret_4w, vol_12w, drawdown_26w, ma_4w_gap, ma_12w_gap,
                       volume_change_4w, fx_ret_1w, us10y_change_1w,
                       boj_flag, cpi_flag, fomc_flag, employment_flag
                  FROM feature_weekly
                 WHERE week_end='2026-03-20'
                """
            ).fetchone()
            self.assertAlmostEqual(row["ret_4w"], 111 / 107 - 1)
            self.assertIsNotNone(row["vol_12w"])
            self.assertAlmostEqual(row["drawdown_26w"], 0.0)
            self.assertAlmostEqual(row["ma_4w_gap"], 111 / ((108 + 109 + 110 + 111) / 4) - 1)
            self.assertAlmostEqual(row["ma_12w_gap"], 111 / (sum(range(100, 112)) / 12) - 1)
            self.assertAlmostEqual(row["volume_change_4w"], 2100 / ((1700 + 1800 + 1900 + 2000) / 4) - 1)
            self.assertAlmostEqual(row["fx_ret_1w"], 153 / 150 - 1)
            self.assertAlmostEqual(row["us10y_change_1w"], 0.2)
            self.assertEqual(row["boj_flag"], 1)
            self.assertEqual(row["cpi_flag"], 1)
            self.assertEqual(row["fomc_flag"], 1)
            self.assertEqual(row["employment_flag"], 1)

    def test_calendar_event_severity_has_explicit_category_defaults(self) -> None:
        self.assertEqual(event_severity("FOMC", ""), 1.0)
        self.assertEqual(event_severity("BOJ", None), 1.0)
        self.assertEqual(event_severity("CPI", ""), 0.7)
        self.assertEqual(event_severity("NFP", ""), 0.7)
        self.assertEqual(event_severity("OTHER", ""), 0.4)
        self.assertEqual(event_severity("NFP", "critical"), 1.0)

    def test_feature_build_honors_as_of_available_data_boundary(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            price_csv = tmp_path / "prices.csv"
            price_csv.write_text(
                "\n".join(
                    [
                        "date,open,high,low,close,adj_close,volume,dividend,split",
                        "2026-01-02,100,101,99,100,100,1000,0,1",
                        "2026-01-09,101,102,100,101,101,1000,0,1",
                        "2026-01-16,102,103,101,102,102,1000,0,1",
                    ]
                )
                + "\n",
                encoding="utf-8",
            )
            con = db.connect(tmp_path / "rencrow.db")
            db.init_schema(con)
            db.upsert_instruments(
                con,
                [
                    {
                        "symbol": "1306.T",
                        "asset_type": "ETF",
                        "venue": "TSE",
                        "currency": "JPY",
                        "first_date": "2026-01-01",
                    }
                ],
            )
            save_market_csv(
                con,
                {"symbol": "1306.T", "venue": "TSE", "currency": "JPY", "source_name": "csv_market", "fixture": str(price_csv)},
                tmp_path,
            )

            count = build_features(con, as_of=date(2026, 1, 9))

            self.assertEqual(count, 2)
            self.assertEqual(
                con.execute("SELECT COUNT(*) FROM feature_weekly WHERE week_end>'2026-01-09'").fetchone()[0],
                0,
            )

    def test_feature_build_honors_symbol_and_asset_type_filters(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            con = db.connect(tmp_path / "rencrow.db")
            db.init_schema(con)
            db.upsert_instruments(
                con,
                [
                    {
                        "symbol": "AAA",
                        "asset_type": "ETF",
                        "venue": "TEST",
                        "currency": "JPY",
                        "first_date": "2026-01-01",
                    },
                    {
                        "symbol": "BBB",
                        "asset_type": "STOCK",
                        "venue": "TEST",
                        "currency": "JPY",
                        "first_date": "2026-01-01",
                    },
                ],
            )
            price_csv = tmp_path / "prices.csv"
            price_csv.write_text(
                "\n".join(
                    [
                        "date,open,high,low,close,adj_close,volume,dividend,split",
                        "2026-01-02,100,101,99,100,100,1000,0,1",
                        "2026-01-09,102,103,101,102,102,1000,0,1",
                    ]
                ),
                encoding="utf-8",
            )
            for symbol in ("AAA", "BBB"):
                save_market_csv(
                    con,
                    {"symbol": symbol, "venue": "TEST", "currency": "JPY", "source_name": "csv_market", "fixture": str(price_csv)},
                    tmp_path,
                )

            count = build_features(con, symbols=["AAA"], asset_types=["ETF"])

            self.assertEqual(count, 2)
            self.assertEqual(
                con.execute(
                    """
                    SELECT COUNT(*)
                      FROM feature_weekly f
                      JOIN instruments i ON i.instrument_id=f.instrument_id
                     WHERE i.symbol='AAA'
                    """
                ).fetchone()[0],
                2,
            )
            self.assertEqual(
                con.execute(
                    """
                    SELECT COUNT(*)
                      FROM feature_weekly f
                      JOIN instruments i ON i.instrument_id=f.instrument_id
                     WHERE i.symbol='BBB'
                    """
                ).fetchone()[0],
                0,
            )

    def test_event_detection_auto_resolves_recovered_source_with_audit_note(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            con = db.connect(tmp_path / "rencrow.db")
            db.init_schema(con)
            con.execute(
                """
                INSERT INTO source_fetch_log(source_name, endpoint, requested_at, status, error_message)
                VALUES ('csv_market', 'fixture', '2026-01-01T00:00:00+00:00', 'fail', 'boom')
                """
            )
            con.commit()

            detect_events(con)
            open_row = con.execute(
                """
                SELECT event_id, resolved_at
                  FROM event_log
                 WHERE reason='source_fetch_unresolved'
                """
            ).fetchone()
            self.assertIsNotNone(open_row)
            self.assertIsNone(open_row["resolved_at"])

            con.execute(
                """
                INSERT INTO source_fetch_log(source_name, endpoint, requested_at, status)
                VALUES ('csv_market', 'fixture', '2026-01-02T00:00:00+00:00', 'success')
                """
            )
            con.commit()
            detect_events(con)
            resolved = con.execute(
                """
                SELECT resolved_at, resolution_note
                  FROM event_log
                 WHERE event_id=?
                """,
                (open_row["event_id"],),
            ).fetchone()
            self.assertTrue(resolved["resolved_at"])
            self.assertIn("data_recovered", resolved["resolution_note"])
            self.assertIn("csv_market", resolved["resolution_note"])

    def test_calendar_event_detection_is_idempotent_per_week_event(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            con = db.connect(tmp_path / "rencrow.db")
            db.init_schema(con)
            db.upsert_instruments(
                con,
                [
                    {
                        "symbol": "1306.T",
                        "asset_type": "ETF",
                        "venue": "TSE",
                        "currency": "JPY",
                        "first_date": "2026-01-01",
                    }
                ],
            )
            iid = con.execute("SELECT instrument_id FROM instruments WHERE symbol='1306.T'").fetchone()[0]
            con.execute(
                """
                INSERT INTO feature_weekly(instrument_id, week_end, close_adj_jpy)
                VALUES (?, '2026-05-15', 100)
                """,
                (iid,),
            )
            con.execute(
                """
                INSERT INTO economic_calendar(event_date, category, event_name, source_name, importance)
                VALUES ('2026-05-15', 'CPI', 'US CPI', 'csv_calendar', 'high')
                """
            )
            con.commit()

            first_count = detect_events(con, lookback_days=0, lookahead_days=0)
            second_count = detect_events(con, lookback_days=0, lookahead_days=0)

            self.assertEqual(first_count, 1)
            self.assertEqual(second_count, 0)
            self.assertEqual(con.execute("SELECT COUNT(*) FROM event_log WHERE reason='calendar_cpi'").fetchone()[0], 1)
            context = con.execute("SELECT context_json FROM event_log WHERE reason='calendar_cpi'").fetchone()[0]
            self.assertEqual(json.loads(context)["event"]["severity_score"], 0.7)
            self.assertEqual(
                con.execute("SELECT event_risk_score FROM feature_weekly WHERE week_end='2026-05-15'").fetchone()[0],
                0.7,
            )

    def test_event_detection_week_end_limits_calendar_scope(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            con = db.connect(tmp_path / "rencrow.db")
            db.init_schema(con)
            db.upsert_instruments(
                con,
                [
                    {
                        "symbol": "1306.T",
                        "asset_type": "ETF",
                        "venue": "TSE",
                        "currency": "JPY",
                        "first_date": "2026-01-01",
                    }
                ],
            )
            iid = con.execute("SELECT instrument_id FROM instruments WHERE symbol='1306.T'").fetchone()[0]
            con.executemany(
                "INSERT INTO feature_weekly(instrument_id, week_end, close_adj_jpy) VALUES (?, ?, 100)",
                [(iid, "2026-05-15"), (iid, "2026-05-22")],
            )
            con.executemany(
                """
                INSERT INTO economic_calendar(event_date, category, event_name, source_name, importance)
                VALUES (?, ?, ?, 'csv_calendar', 'high')
                """,
                [
                    ("2026-05-15", "CPI", "US CPI"),
                    ("2026-05-22", "FOMC", "FOMC"),
                ],
            )
            con.commit()

            count = detect_events(con, lookback_days=0, lookahead_days=0, week_end=date(2026, 5, 15))

            self.assertEqual(count, 1)
            self.assertEqual(con.execute("SELECT COUNT(*) FROM event_log WHERE reason='calendar_cpi'").fetchone()[0], 1)
            self.assertEqual(con.execute("SELECT COUNT(*) FROM event_log WHERE reason='calendar_fomc'").fetchone()[0], 0)
            self.assertEqual(
                con.execute("SELECT event_risk_score FROM feature_weekly WHERE week_end='2026-05-15'").fetchone()[0],
                0.7,
            )
            self.assertEqual(
                con.execute("SELECT event_risk_score FROM feature_weekly WHERE week_end='2026-05-22'").fetchone()[0],
                0,
            )


if __name__ == "__main__":
    unittest.main()
