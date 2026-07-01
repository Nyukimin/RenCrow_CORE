from __future__ import annotations

import json
import sqlite3
import subprocess
import sys
import tempfile
import unittest
import csv
from datetime import date, timedelta
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
REPO = ROOT.parents[0]
SRC = ROOT / "src"


def run_script(script: str, *args: str) -> subprocess.CompletedProcess[str]:
    cmd = [sys.executable, str(SRC / script), *args]
    return subprocess.run(cmd, cwd=REPO, text=True, capture_output=True, check=True, env={"PYTHONPATH": str(SRC)})


def write_config(tmp_path: Path) -> tuple[Path, Path, Path]:
    data_root = tmp_path / "rencrow-data"
    config = data_root / "config"
    fixtures = data_root / "fixtures"
    config.mkdir(parents=True)
    fixtures.mkdir(parents=True)
    (fixtures / "prices.csv").write_text((ROOT / "fixtures" / "1306T_prices.csv").read_text(encoding="utf-8"), encoding="utf-8")
    (config / "instruments.yml").write_text(
        json.dumps(
            {
                "instruments": [
                    {
                        "symbol": "1306.T",
                        "name": "TOPIX ETF",
                        "asset_type": "ETF",
                        "venue": "TSE",
                        "currency": "JPY",
                        "timezone": "Asia/Tokyo",
                        "active": 1,
                        "first_date": "2026-01-01",
                        "source_name": "csv_market",
                        "fixture": "fixtures/prices.csv",
                    }
                ]
            }
        ),
        encoding="utf-8",
    )
    (config / "sources.yml").write_text(json.dumps({"macro_sources": []}), encoding="utf-8")
    (config / "calendars.yml").write_text(json.dumps({"calendar_sources": []}), encoding="utf-8")
    return data_root, config, data_root / "data" / "rencrow.db"


class WeeklyRotationBacktestTest(unittest.TestCase):
    def test_backtest_cli_writes_run_metrics_and_csvs(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            data_root, config_root, db_path = write_config(tmp_path)
            out_dir = data_root / "data" / "snapshots"
            backtest_dir = data_root / "data" / "backtests"
            run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(config_root))
            run_script("02_fetch_market.py", "--db", str(db_path), "--config-root", str(config_root), "--data-root", str(data_root))
            run_script("04_build_features.py", "--db", str(db_path))
            run_script("06_make_snapshot.py", "--db", str(db_path), "--output-dir", str(out_dir), "--snapshot-date", "2026-05-16")
            result = run_script(
                "09_backtest_weekly_rotation.py",
                "--db",
                str(db_path),
                "--snapshot",
                "latest",
                "--symbols",
                "1306.T",
                "--cost-bps",
                "5",
                "--tax-mode",
                "approx_jp_taxable",
                "--output-dir",
                str(backtest_dir),
                "--json",
            )
            summary = json.loads(result.stdout)
            self.assertEqual(summary["strategy_id"], "weekly_etf_rotation_v1")
            self.assertEqual(summary["universe"], ["1306.T"])
            self.assertEqual(summary["tradable_asset_types"], ["ETF", "CASH_PROXY"])
            self.assertEqual(summary["universe_assets"], [{"symbol": "1306.T", "asset_type": "ETF"}])
            self.assertGreater(summary["weeks"], 0)
            self.assertTrue(Path(summary["equity_curve_path"]).exists())
            self.assertTrue(Path(summary["trades_path"]).exists())
            with Path(summary["equity_curve_path"]).open(encoding="utf-8", newline="") as f:
                curve_rows = list(csv.DictReader(f))
            self.assertEqual(curve_rows[0]["symbol"], "")
            first_signal_idx = next(idx for idx, row in enumerate(curve_rows) if row["signal_symbol"])
            self.assertEqual(curve_rows[first_signal_idx]["symbol"], "")
            self.assertEqual(curve_rows[first_signal_idx]["signal_symbol"], "1306.T")
            for previous, current in zip(curve_rows[first_signal_idx:], curve_rows[first_signal_idx + 1 :]):
                self.assertEqual(current["symbol"], previous["signal_symbol"])

            con = sqlite3.connect(db_path)
            con.row_factory = sqlite3.Row
            run = con.execute("SELECT * FROM backtest_run WHERE backtest_id=?", (summary["backtest_id"],)).fetchone()
            self.assertIsNotNone(run)
            self.assertEqual(run["status"], "success")
            metrics = {
                row["metric_name"]: row["metric_value"]
                for row in con.execute("SELECT metric_name, metric_value FROM backtest_metric WHERE backtest_id=?", (summary["backtest_id"],))
            }
            self.assertIn("final_equity", metrics)
            self.assertIn("max_dd", metrics)
            self.assertIn("tax_drag", metrics)
            self.assertIn("calmar", metrics)
            self.assertIn("average_holding_period", metrics)
            self.assertIn("worst_month", metrics)
            self.assertIn("recovery_months", metrics)

    def test_same_config_backtest_reproduces_metrics(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            data_root, config_root, db_path = write_config(tmp_path)
            out_dir = data_root / "data" / "snapshots"
            backtest_dir = data_root / "data" / "backtests"
            run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(config_root))
            run_script("02_fetch_market.py", "--db", str(db_path), "--config-root", str(config_root), "--data-root", str(data_root))
            run_script("04_build_features.py", "--db", str(db_path))
            run_script("06_make_snapshot.py", "--db", str(db_path), "--output-dir", str(out_dir), "--snapshot-date", "2026-05-16")

            first = json.loads(
                run_script(
                    "09_backtest_weekly_rotation.py",
                    "--db",
                    str(db_path),
                    "--snapshot",
                    "latest",
                    "--symbols",
                    "1306.T",
                    "--cost-bps",
                    "5",
                    "--slippage-bps",
                    "2",
                    "--tax-mode",
                    "approx_jp_taxable",
                    "--walk-forward",
                    "--output-dir",
                    str(backtest_dir),
                    "--json",
                ).stdout
            )
            second = json.loads(
                run_script(
                    "09_backtest_weekly_rotation.py",
                    "--db",
                    str(db_path),
                    "--snapshot",
                    "latest",
                    "--symbols",
                    "1306.T",
                    "--cost-bps",
                    "5",
                    "--slippage-bps",
                    "2",
                    "--tax-mode",
                    "approx_jp_taxable",
                    "--walk-forward",
                    "--output-dir",
                    str(backtest_dir),
                    "--json",
                ).stdout
            )

            self.assertEqual(first["metrics"], second["metrics"])
            self.assertEqual(first["split_metrics"], second["split_metrics"])
            self.assertEqual(first["universe"], second["universe"])

    def test_backtest_uses_cost_config_defaults_and_cli_overrides(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            data_root, config_root, db_path = write_config(tmp_path)
            (config_root / "costs.yml").write_text(
                json.dumps({"cost_bps": 7.0, "slippage_bps": 3.0, "tax_mode": "approx_jp_taxable"}),
                encoding="utf-8",
            )
            out_dir = data_root / "data" / "snapshots"
            backtest_dir = data_root / "data" / "backtests"
            run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(config_root))
            run_script("02_fetch_market.py", "--db", str(db_path), "--config-root", str(config_root), "--data-root", str(data_root))
            run_script("04_build_features.py", "--db", str(db_path))
            run_script("06_make_snapshot.py", "--db", str(db_path), "--output-dir", str(out_dir), "--snapshot-date", "2026-05-16")

            configured = json.loads(
                run_script(
                    "09_backtest_weekly_rotation.py",
                    "--db",
                    str(db_path),
                    "--snapshot",
                    "latest",
                    "--symbols",
                    "1306.T",
                    "--config-root",
                    str(config_root),
                    "--output-dir",
                    str(backtest_dir),
                    "--json",
                ).stdout
            )
            overridden = json.loads(
                run_script(
                    "09_backtest_weekly_rotation.py",
                    "--db",
                    str(db_path),
                    "--snapshot",
                    "latest",
                    "--symbols",
                    "1306.T",
                    "--config-root",
                    str(config_root),
                    "--cost-bps",
                    "2",
                    "--output-dir",
                    str(backtest_dir),
                    "--json",
                ).stdout
            )

            self.assertEqual(configured["cost_bps"], 7.0)
            self.assertEqual(configured["slippage_bps"], 3.0)
            self.assertEqual(configured["tax_mode"], "approx_jp_taxable")
            self.assertEqual(overridden["cost_bps"], 2.0)
            self.assertEqual(overridden["slippage_bps"], 3.0)
            self.assertEqual(overridden["tax_mode"], "approx_jp_taxable")
            con = sqlite3.connect(db_path)
            row = con.execute(
                "SELECT cost_bps, slippage_bps, tax_mode FROM backtest_run WHERE backtest_id=?",
                (configured["backtest_id"],),
            ).fetchone()
            con.close()
            self.assertEqual(row, (7.0, 3.0, "approx_jp_taxable"))

    def test_future_features_do_not_change_past_snapshot_backtest(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            data_root, config_root, db_path = write_config(tmp_path)
            out_dir = data_root / "data" / "snapshots"
            backtest_dir = data_root / "data" / "backtests"
            run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(config_root))
            run_script("02_fetch_market.py", "--db", str(db_path), "--config-root", str(config_root), "--data-root", str(data_root))
            run_script("04_build_features.py", "--db", str(db_path))
            run_script("06_make_snapshot.py", "--db", str(db_path), "--output-dir", str(out_dir), "--snapshot-date", "2026-05-16")

            before = json.loads(
                run_script(
                    "09_backtest_weekly_rotation.py",
                    "--db",
                    str(db_path),
                    "--snapshot",
                    "latest",
                    "--symbols",
                    "1306.T",
                    "--output-dir",
                    str(backtest_dir),
                    "--json",
                ).stdout
            )
            con = sqlite3.connect(db_path)
            iid = con.execute("SELECT instrument_id FROM instruments WHERE symbol='1306.T'").fetchone()[0]
            con.execute(
                """
                INSERT OR REPLACE INTO feature_weekly(
                  instrument_id, week_end, close_adj_jpy, ret_1w, ret_12w, ret_12w_skip1,
                  vol_12w, drawdown_26w, event_risk_score
                )
                VALUES (?, '2026-06-19', 999, 0.50, 9.0, 9.0, 0.01, 0.0, 0.0)
                """,
                (iid,),
            )
            con.commit()
            con.close()

            after = json.loads(
                run_script(
                    "09_backtest_weekly_rotation.py",
                    "--db",
                    str(db_path),
                    "--snapshot",
                    before["snapshot_id"],
                    "--symbols",
                    "1306.T",
                    "--end",
                    "2026-06-19",
                    "--output-dir",
                    str(backtest_dir),
                    "--json",
                ).stdout
            )

            self.assertEqual(before["end_date"], after["end_date"])
            self.assertEqual(before["weeks"], after["weeks"])
            self.assertEqual(before["metrics"], after["metrics"])
            self.assertEqual(before["latest_signal"], after["latest_signal"])

    def test_changed_snapshot_feature_scope_blocks_past_snapshot_backtest(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            data_root, config_root, db_path = write_config(tmp_path)
            out_dir = data_root / "data" / "snapshots"
            backtest_dir = data_root / "data" / "backtests"
            run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(config_root))
            run_script("02_fetch_market.py", "--db", str(db_path), "--config-root", str(config_root), "--data-root", str(data_root))
            run_script("04_build_features.py", "--db", str(db_path))
            run_script("06_make_snapshot.py", "--db", str(db_path), "--output-dir", str(out_dir), "--snapshot-date", "2026-05-16")
            con = sqlite3.connect(db_path)
            con.execute("UPDATE feature_weekly SET ret_12w_skip1=9.0 WHERE week_end='2026-05-15'")
            con.commit()
            con.close()

            result = subprocess.run(
                [
                    sys.executable,
                    str(SRC / "09_backtest_weekly_rotation.py"),
                    "--db",
                    str(db_path),
                    "--snapshot",
                    "latest",
                    "--symbols",
                    "1306.T",
                    "--output-dir",
                    str(backtest_dir),
                    "--json",
                ],
                cwd=REPO,
                text=True,
                capture_output=True,
                env={"PYTHONPATH": str(SRC)},
            )

            self.assertEqual(result.returncode, 3)
            self.assertIn("feature_weekly changed since snapshot", result.stderr)

    def test_walk_forward_backtest_writes_train_test_oos_metrics(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            data_root, config_root, db_path = write_config(tmp_path)
            out_dir = data_root / "data" / "snapshots"
            backtest_dir = data_root / "data" / "backtests"
            run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(config_root))
            run_script("02_fetch_market.py", "--db", str(db_path), "--config-root", str(config_root), "--data-root", str(data_root))
            run_script("04_build_features.py", "--db", str(db_path))
            run_script("06_make_snapshot.py", "--db", str(db_path), "--output-dir", str(out_dir), "--snapshot-date", "2026-05-16")
            result = run_script(
                "09_backtest_weekly_rotation.py",
                "--db",
                str(db_path),
                "--snapshot",
                "latest",
                "--symbols",
                "1306.T",
                "--walk-forward",
                "--output-dir",
                str(backtest_dir),
                "--json",
            )
            summary = json.loads(result.stdout)
            self.assertIn("train", summary["split_metrics"])
            self.assertIn("test", summary["split_metrics"])
            self.assertTrue(any(name.startswith("oos_") for name in summary["split_metrics"]))

            con = sqlite3.connect(db_path)
            split_names = {
                row[0]
                for row in con.execute(
                    "SELECT DISTINCT split_name FROM backtest_metric WHERE backtest_id=?",
                    (summary["backtest_id"],),
                )
            }
            self.assertIn("full", split_names)
            self.assertIn("train", split_names)
            self.assertIn("test", split_names)
            self.assertTrue(any(name.startswith("oos_") for name in split_names))

    def test_walk_forward_records_named_stress_period_metrics_when_data_overlaps(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            db_path = Path(td) / "rencrow.db"
            out_dir = Path(td) / "backtests"
            con = sqlite3.connect(db_path)
            con.row_factory = sqlite3.Row
            sys.path.insert(0, str(SRC))
            from rencrow_data import db
            from rencrow_data.backtest import BacktestOptions, run_weekly_rotation_backtest

            db.init_schema(con)
            db.upsert_instruments(
                con,
                [{"symbol": "RISK", "asset_type": "ETF", "venue": "TEST", "currency": "JPY", "first_date": "2019-01-01"}],
            )
            con.execute(
                "UPDATE strategy_version SET config_json=? WHERE strategy_id='weekly_etf_rotation_v1'",
                (
                    json.dumps(
                        {
                            "cash_proxy": "RISK",
                            "drawdown_penalty": 0.0,
                            "score_min": -999.0,
                            "top_n": 1,
                            "universe": ["RISK"],
                            "volatility_penalty": 0.0,
                        },
                        sort_keys=True,
                    ),
                ),
            )
            risk_id = db.instrument_id(con, "RISK", "TEST")
            con.execute("INSERT INTO snapshot_registry(snapshot_id, snapshot_date, status) VALUES (1, '2020-05-22', 'success')")
            first_week = date(2019, 12, 6)
            for idx in range(26):
                week = (first_week + timedelta(days=7 * idx)).isoformat()
                con.execute(
                    """
                    INSERT INTO feature_weekly(
                      instrument_id, week_end, close_adj_jpy, ret_1w, ret_12w, ret_12w_skip1,
                      vol_12w, drawdown_26w, event_risk_score
                    )
                    VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0.0)
                    """,
                    (risk_id, week, 100 + idx, -0.03 if "2020-03" in week else 0.01, 0.20, 0.10, 0.15, -0.05),
                )
            con.commit()

            result = run_weekly_rotation_backtest(
                con,
                BacktestOptions(
                    snapshot_id="1",
                    strategy_id="weekly_etf_rotation_v1",
                    mode="walk_forward",
                    output_dir=out_dir,
                ),
            )

            self.assertIn("stress_2020", result["split_metrics"])
            split_names = {
                row[0]
                for row in con.execute(
                    "SELECT DISTINCT split_name FROM backtest_metric WHERE backtest_id=?",
                    (result["backtest_id"],),
                )
            }
            self.assertIn("stress_2020", split_names)

    def test_event_veto_routes_signal_to_cash_proxy(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            db_path = tmp_path / "rencrow.db"
            out_dir = tmp_path / "backtests"
            con = sqlite3.connect(db_path)
            con.row_factory = sqlite3.Row
            sys.path.insert(0, str(SRC))
            from rencrow_data import db
            from rencrow_data.backtest import BacktestOptions, run_weekly_rotation_backtest

            db.init_schema(con)
            db.upsert_instruments(
                con,
                [
                    {"symbol": "RISK", "asset_type": "ETF", "venue": "TEST", "currency": "JPY", "first_date": "2026-01-01"},
                    {"symbol": "CASH", "asset_type": "CASH_PROXY", "venue": "TEST", "currency": "JPY", "first_date": "2026-01-01"},
                ],
            )
            con.execute(
                "UPDATE strategy_version SET config_json=? WHERE strategy_id='weekly_etf_rotation_v1'",
                (
                    json.dumps(
                        {
                            "cash_proxy": "CASH",
                            "drawdown_penalty": 0.0,
                            "event_veto_threshold": 0.9,
                            "score_min": -999.0,
                            "top_n": 1,
                            "universe": ["RISK", "CASH"],
                            "volatility_penalty": 0.0,
                        },
                        sort_keys=True,
                    ),
                ),
            )
            risk_id = db.instrument_id(con, "RISK", "TEST")
            cash_id = db.instrument_id(con, "CASH", "TEST")
            con.execute(
                """
                INSERT INTO snapshot_registry(snapshot_id, snapshot_date, status)
                VALUES (1, '2026-04-10', 'success')
                """
            )
            weeks = [
                "2026-01-02",
                "2026-01-09",
                "2026-01-16",
                "2026-01-23",
                "2026-01-30",
                "2026-02-06",
                "2026-02-13",
                "2026-02-20",
                "2026-02-27",
                "2026-03-06",
                "2026-03-13",
                "2026-03-20",
                "2026-03-27",
                "2026-04-03",
                "2026-04-10",
            ]
            for idx, week in enumerate(weeks):
                con.execute(
                    """
                    INSERT INTO feature_weekly(
                      instrument_id, week_end, close_adj_jpy, ret_1w, ret_12w, ret_12w_skip1,
                      vol_12w, drawdown_26w, event_risk_score
                    )
                    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
                    """,
                    (risk_id, week, 100 + idx, 0.02, 0.50, 0.40, 0.10, 0.0, 1.0 if week == "2026-04-03" else 0.0),
                )
                con.execute(
                    """
                    INSERT INTO feature_weekly(
                      instrument_id, week_end, close_adj_jpy, ret_1w, ret_12w, ret_12w_skip1,
                      vol_12w, drawdown_26w, event_risk_score
                    )
                    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
                    """,
                    (cash_id, week, 100.0, 0.0, 0.0, 0.0, 0.01, 0.0, 0.0),
                )
            con.commit()
            result = run_weekly_rotation_backtest(
                con,
                BacktestOptions(
                    snapshot_id="1",
                    strategy_id="weekly_etf_rotation_v1",
                    output_dir=out_dir,
                    cost_bps=10,
                    slippage_bps=5,
                    tax_mode="approx_jp_taxable",
                ),
            )
            with Path(result["equity_curve_path"]).open(encoding="utf-8", newline="") as f:
                rows = list(csv.DictReader(f))
            veto_row = next(row for row in rows if row["week_end"] == "2026-04-03")
            self.assertEqual(veto_row["event_vetoed"], "1")
            self.assertEqual(veto_row["signal_symbol"], "CASH")
            self.assertGreater(result["metrics"]["cost_drag"], 0)
            self.assertGreater(result["metrics"]["tax_drag"], 0)
            with Path(result["trades_path"]).open(encoding="utf-8", newline="") as f:
                trades = list(csv.DictReader(f))
            self.assertTrue(any(row["to_symbol"] == "CASH" and row["cost_rate"] == "0.0015" for row in trades))

    def test_backtest_rejects_non_tradable_strategy_universe_asset_type(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            db_path = tmp_path / "rencrow.db"
            con = sqlite3.connect(db_path)
            con.row_factory = sqlite3.Row
            sys.path.insert(0, str(SRC))
            from rencrow_data import db
            from rencrow_data.backtest import BacktestOptions, run_weekly_rotation_backtest

            db.init_schema(con)
            db.upsert_instruments(
                con,
                [{"symbol": "STOCKY", "asset_type": "STOCK", "venue": "TEST", "currency": "JPY", "first_date": "2026-01-01"}],
            )
            con.execute(
                "UPDATE strategy_version SET config_json=? WHERE strategy_id='weekly_etf_rotation_v1'",
                (
                    json.dumps(
                        {
                            "cash_proxy": "STOCKY",
                            "universe": ["STOCKY"],
                            "score_min": -999.0,
                        },
                        sort_keys=True,
                    ),
                ),
            )
            con.execute("INSERT INTO snapshot_registry(snapshot_id, snapshot_date, status) VALUES (1, '2026-05-16', 'success')")
            con.commit()

            with self.assertRaisesRegex(ValueError, "non-tradable asset types"):
                run_weekly_rotation_backtest(con, BacktestOptions(snapshot_id="1", strategy_id="weekly_etf_rotation_v1"))


if __name__ == "__main__":
    unittest.main()
