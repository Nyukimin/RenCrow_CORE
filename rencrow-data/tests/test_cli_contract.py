from __future__ import annotations

import json
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
REPO = ROOT.parents[0]
SRC = ROOT / "src"


def run_script(script: str, *args: str, check: bool = True) -> subprocess.CompletedProcess[str]:
    cmd = [sys.executable, str(SRC / script), *args]
    return subprocess.run(cmd, cwd=REPO, text=True, capture_output=True, check=check, env={"PYTHONPATH": str(SRC)})


def run_data_dir_script(script: str, *args: str, check: bool = True) -> subprocess.CompletedProcess[str]:
    cmd = [sys.executable, str(ROOT / "src" / script), *args]
    return subprocess.run(cmd, cwd=ROOT, text=True, capture_output=True, check=check, env={"PYTHONPATH": str(SRC)})


def run_script_in_data_dir(cwd: Path, script: str, *args: str, check: bool = True) -> subprocess.CompletedProcess[str]:
    cmd = [sys.executable, str(ROOT / "src" / script), *args]
    return subprocess.run(cmd, cwd=cwd, text=True, capture_output=True, check=check, env={"PYTHONPATH": str(SRC)})


class CLIContractTest(unittest.TestCase):
    def test_default_paths_are_local_after_cd_into_data_dir(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            data_dir = Path(td) / "rencrow-data"
            config_dir = data_dir / "config"
            fixtures_dir = data_dir / "fixtures"
            config_dir.mkdir(parents=True)
            fixtures_dir.mkdir()
            (fixtures_dir / "prices.csv").write_text((ROOT / "fixtures" / "1306T_prices.csv").read_text(encoding="utf-8"), encoding="utf-8")
            (fixtures_dir / "macro.csv").write_text((ROOT / "fixtures" / "macro_series.csv").read_text(encoding="utf-8"), encoding="utf-8")
            (fixtures_dir / "calendar.csv").write_text((ROOT / "fixtures" / "economic_calendar.csv").read_text(encoding="utf-8"), encoding="utf-8")
            (config_dir / "instruments.yml").write_text(
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
                                "first_date": "2001-01-01",
                                "source_name": "csv_market",
                                "fixture": "fixtures/prices.csv",
                            }
                        ]
                    }
                ),
                encoding="utf-8",
            )
            (config_dir / "sources.yml").write_text(json.dumps({"macro_sources": [{"source_name": "csv_macro", "fixture": "fixtures/macro.csv"}]}), encoding="utf-8")
            (config_dir / "calendars.yml").write_text(json.dumps({"calendar_sources": [{"source_name": "csv_calendar", "fixture": "fixtures/calendar.csv"}]}), encoding="utf-8")

            run_script_in_data_dir(data_dir, "01_init_db.py")
            run_script_in_data_dir(data_dir, "02_fetch_market.py", "--json")
            run_script_in_data_dir(data_dir, "03_fetch_macro.py", "--json")
            run_script_in_data_dir(data_dir, "04_build_features.py", "--json")
            run_script_in_data_dir(data_dir, "05_detect_events.py", "--week-end", "2026-05-15", "--json")
            snapshot = json.loads(run_script_in_data_dir(data_dir, "06_make_snapshot.py", "--week-end", "2026-05-15", "--json").stdout)

            self.assertTrue((data_dir / "data" / "rencrow.db").exists())
            self.assertTrue((data_dir / "data" / "snapshots" / "snapshot_20260515.sqlite.gz").exists())
            self.assertFalse((data_dir / "rencrow-data").exists())
            self.assertEqual(Path(snapshot["path"]), Path("data/snapshots/snapshot_20260515.sqlite.gz"))
            self.assertTrue(snapshot["snapshot_id"])
            con = sqlite3.connect(data_dir / "data" / "rencrow.db")
            logged_snapshot_id = con.execute(
                "SELECT snapshot_id FROM cli_run_log WHERE cli_name='06_make_snapshot.py' ORDER BY started_at DESC LIMIT 1"
            ).fetchone()[0]
            con.close()
            self.assertEqual(str(logged_snapshot_id), str(snapshot["snapshot_id"]))

    def test_spec_14_2_full_fixture_integration_flow(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            db_path = Path(td) / "rencrow_test.db"

            commands = [
                ("01_init_db.py", "--db-path", str(db_path)),
                ("02_fetch_market.py", "--fixture", "fixtures/1306T_prices.csv", "--db-path", str(db_path)),
                ("03_fetch_macro.py", "--fixture", "fixtures/macro_series.csv", "--db-path", str(db_path)),
                ("04_build_features.py", "--week-end", "2026-05-15", "--db-path", str(db_path)),
                ("05_detect_events.py", "--week-end", "2026-05-15", "--db-path", str(db_path)),
                ("06_make_snapshot.py", "--week-end", "2026-05-15", "--db-path", str(db_path)),
                ("08_validate_data.py", "--as-of", "2026-05-15", "--db-path", str(db_path)),
                ("09_backtest_weekly_rotation.py", "--snapshot", "latest", "--db-path", str(db_path)),
                ("10_risk_check.py", "--snapshot", "latest", "--strategy", "weekly_etf_rotation_v1", "--db-path", str(db_path)),
                ("11_generate_decision.py", "--snapshot", "latest", "--strategy", "weekly_etf_rotation_v1", "--db-path", str(db_path)),
            ]
            for command in commands:
                run_data_dir_script(*command)

            con = sqlite3.connect(db_path)
            counts = {
                name: con.execute(f"SELECT COUNT(*) FROM {name}").fetchone()[0]
                for name in (
                    "price_raw",
                    "macro_series",
                    "feature_weekly",
                    "event_log",
                    "snapshot_registry",
                    "data_quality_check",
                    "backtest_run",
                    "risk_check_result",
                    "weekly_signal",
                    "decision_log",
                )
            }
            con.close()
            for table, count in counts.items():
                self.assertGreater(count, 0, table)

    def test_init_db_reset_is_rejected_and_logged_without_destructive_reset(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            db_path = Path(td) / "rencrow_test.db"

            result = run_data_dir_script("01_init_db.py", "--db-path", str(db_path), "--reset", "--json", check=False)

            self.assertEqual(result.returncode, 4)
            self.assertIn("--reset is disabled", result.stderr)
            con = sqlite3.connect(db_path)
            row = con.execute(
                """
                SELECT cli_name, status, fail_count, exit_code, detail_json
                  FROM cli_run_log
                 WHERE cli_name='01_init_db.py'
                 ORDER BY started_at DESC
                 LIMIT 1
                """
            ).fetchone()
            self.assertEqual(row[0], "01_init_db.py")
            self.assertEqual(row[1], "fail")
            self.assertEqual(row[2], 1)
            self.assertEqual(row[3], 4)
            self.assertEqual(json.loads(row[4])["exit_code"], 4)
            self.assertEqual(con.execute("SELECT COUNT(*) FROM instruments").fetchone()[0], 0)
            con.close()

    def test_spec_14_2_fixture_commands_work_from_data_dir(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            db_path = Path(td) / "rencrow_test.db"

            run_data_dir_script("01_init_db.py", "--db-path", str(db_path))
            market = run_data_dir_script(
                "02_fetch_market.py",
                "--fixture",
                "fixtures/1306T_prices.csv",
                "--db-path",
                str(db_path),
                "--json",
            )
            macro = run_data_dir_script(
                "03_fetch_macro.py",
                "--fixture",
                "fixtures/macro_series.csv",
                "--db-path",
                str(db_path),
                "--json",
            )

            market_summary = json.loads(market.stdout)
            macro_summary = json.loads(macro.stdout)
            self.assertEqual(market_summary["status"], "success")
            self.assertGreater(market_summary["rows_fetched"], 0)
            self.assertEqual(macro_summary["status"], "success")
            self.assertGreater(macro_summary["rows_fetched"], 0)

            con = sqlite3.connect(db_path)
            price_count = con.execute("SELECT COUNT(*) FROM price_raw").fetchone()[0]
            macro_count = con.execute("SELECT COUNT(*) FROM macro_series").fetchone()[0]
            con.close()
            self.assertGreater(price_count, 0)
            self.assertGreater(macro_count, 0)

    def test_specified_cli_argument_aliases_are_accepted(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            db_path = tmp_path / "rencrow.db"
            config_root = tmp_path / "config"
            snapshot_dir = tmp_path / "snapshots"
            config_root.mkdir()
            (config_root / "instruments.yml").write_text('{"instruments":[]}\n', encoding="utf-8")
            (config_root / "sources.yml").write_text('{"macro_sources":[]}\n', encoding="utf-8")
            (config_root / "calendars.yml").write_text('{"calendar_sources":[]}\n', encoding="utf-8")

            run_script("01_init_db.py", "--db-path", str(db_path), "--config-root", str(config_root))
            run_script(
                "02_fetch_market.py",
                "--db-path",
                str(db_path),
                "--config-root",
                str(config_root),
                "--symbols",
                "SPY,IEF",
                "--asset-types",
                "ETF",
                "--provider",
                "yahoo",
                "--start",
                "2026-01-01",
                "--end",
                "2026-01-31",
                "--incremental",
            )
            run_script(
                "03_fetch_macro.py",
                "--db-path",
                str(db_path),
                "--config-root",
                str(config_root),
                "--series",
                "DGS10",
                "--provider",
                "fred",
                "--start",
                "2026-01-01",
                "--end",
                "2026-01-31",
                "--incremental",
            )
            run_script("04_build_features.py", "--db-path", str(db_path), "--week-end", "latest", "--symbols", "SPY", "--asset-types", "ETF")
            run_script("05_detect_events.py", "--db-path", str(db_path), "--week-end", "latest", "--lookback-days", "5", "--lookahead-days", "10")
            run_script("06_make_snapshot.py", "--db-path", str(db_path), "--week-end", "latest", "--snapshot-dir", str(snapshot_dir))
            run_script("07_sync_universe.py", "--db-path", str(db_path), "--config-root", str(config_root), "--no-fetch", "--no-features", "--start", "2026-01-01", "--end", "2026-01-31", "--incremental")

    def test_build_features_week_end_limits_available_price_data(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            db_path = tmp_path / "rencrow.db"
            config_root = tmp_path / "config"
            config_root.mkdir()
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
            (config_root / "instruments.yml").write_text(
                json.dumps(
                    {
                        "instruments": [
                            {
                                "symbol": "1306.T",
                                "asset_type": "ETF",
                                "venue": "TSE",
                                "currency": "JPY",
                                "first_date": "2026-01-01",
                                "source_name": "csv_market",
                            }
                        ]
                    }
                ),
                encoding="utf-8",
            )

            run_script("01_init_db.py", "--db-path", str(db_path), "--config-root", str(config_root))
            run_script("02_fetch_market.py", "--db-path", str(db_path), "--config-root", str(config_root), "--fixture", str(price_csv))
            summary = json.loads(
                run_script("04_build_features.py", "--db-path", str(db_path), "--week-end", "2026-01-09", "--json").stdout
            )

            self.assertEqual(summary["as_of"], "2026-01-09")
            self.assertEqual(summary["feature_rows"], 2)
            con = sqlite3.connect(db_path)
            future_rows = con.execute("SELECT COUNT(*) FROM feature_weekly WHERE week_end>'2026-01-09'").fetchone()[0]
            con.close()
            self.assertEqual(future_rows, 0)

    def test_detect_events_week_end_limits_calendar_scope(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            db_path = Path(td) / "rencrow.db"
            run_script("01_init_db.py", "--db-path", str(db_path))
            con = sqlite3.connect(db_path)
            con.execute(
                """
                INSERT INTO instruments(symbol, asset_type, venue, currency, first_date)
                VALUES ('1306.T', 'ETF', 'TSE', 'JPY', '2026-01-01')
                """
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
            con.close()

            summary = json.loads(
                run_script(
                    "05_detect_events.py",
                    "--db-path",
                    str(db_path),
                    "--week-end",
                    "2026-05-15",
                    "--lookback-days",
                    "0",
                    "--lookahead-days",
                    "0",
                    "--json",
                ).stdout
            )

            self.assertEqual(summary["as_of"], "2026-05-15")
            self.assertEqual(summary["event_rows"], 1)
            self.assertEqual(summary["event_state"]["as_of"], "2026-05-15")
            self.assertEqual(summary["event_state"]["open_event_count"], 1)
            self.assertEqual(summary["event_state"]["max_open_event_risk_score"], 0.7)
            self.assertEqual(summary["event_state"]["latest_open_events"][0]["reason"], "calendar_cpi")
            con = sqlite3.connect(db_path)
            counts = dict(con.execute("SELECT reason, COUNT(*) FROM event_log WHERE reason LIKE 'calendar_%' GROUP BY reason").fetchall())
            later_risk = con.execute("SELECT event_risk_score FROM feature_weekly WHERE week_end='2026-05-22'").fetchone()[0]
            con.close()
            self.assertEqual(counts, {"calendar_cpi": 1})
            self.assertEqual(later_risk, 0)

    def test_early_cli_json_outputs_are_machine_readable_summaries(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            db_path = tmp_path / "rencrow.db"
            config_root = tmp_path / "config"
            snapshot_dir = tmp_path / "snapshots"
            config_root.mkdir()
            (config_root / "instruments.yml").write_text('{"instruments":[]}\n', encoding="utf-8")
            (config_root / "sources.yml").write_text('{"macro_sources":[]}\n', encoding="utf-8")
            (config_root / "calendars.yml").write_text('{"calendar_sources":[]}\n', encoding="utf-8")

            commands = [
                ("01_init_db.py", "--db-path", str(db_path), "--config-dir", str(config_root), "--json"),
                ("02_fetch_market.py", "--db-path", str(db_path), "--config-root", str(config_root), "--json"),
                ("03_fetch_macro.py", "--db-path", str(db_path), "--config-root", str(config_root), "--json"),
                ("04_build_features.py", "--db-path", str(db_path), "--week-end", "latest", "--json"),
                ("05_detect_events.py", "--db-path", str(db_path), "--week-end", "latest", "--lookback-days", "3", "--lookahead-days", "4", "--json"),
                ("06_make_snapshot.py", "--db-path", str(db_path), "--week-end", "latest", "--snapshot-dir", str(snapshot_dir), "--json"),
                ("07_sync_universe.py", "--db-path", str(db_path), "--config-root", str(config_root), "--no-fetch", "--no-features", "--json"),
                ("15_manual_stop.py", "--db-path", str(db_path), "--operator", "unit-test", "--reason", "manual stop drill", "--json"),
            ]
            for command in commands:
                result = run_script(*command)
                summary = json.loads(result.stdout)
                self.assertEqual(summary["cli_name"], command[0])
                self.assertIn("status", summary)
                self.assertIn("target_count", summary)
                self.assertIn("success_count", summary)
                self.assertIn("partial_count", summary)
                self.assertIn("fail_count", summary)
                self.assertIn("cli_run_id", summary)
                self.assertIn("run_id", summary)
                self.assertIn("started_at", summary)
                self.assertIn("finished_at", summary)
                if command[0] in {"01_init_db.py", "02_fetch_market.py", "03_fetch_macro.py", "04_build_features.py", "07_sync_universe.py"}:
                    self.assertTrue(summary["config_hash"])
                if command[0] == "04_build_features.py":
                    self.assertTrue(summary["feature_config_hash"])
            con = sqlite3.connect(db_path)
            rows = con.execute(
                "SELECT cli_name, status, target_count, success_count, partial_count, fail_count, exit_code, started_at, finished_at, config_hash FROM cli_run_log"
            ).fetchall()
            con.close()
            self.assertEqual(len(rows), len(commands))
            self.assertEqual(sorted(row[0] for row in rows), sorted(command[0] for command in commands))
            for row in rows:
                self.assertTrue(row[1])
                self.assertIsNotNone(row[2])
                self.assertIsNotNone(row[3])
                self.assertIsNotNone(row[4])
                self.assertIsNotNone(row[5])
                self.assertEqual(row[6], 0)
                self.assertTrue(row[7])
                self.assertTrue(row[8])
                if row[0] in {"01_init_db.py", "02_fetch_market.py", "03_fetch_macro.py", "04_build_features.py", "07_sync_universe.py"}:
                    self.assertTrue(row[9])

    def test_config_hash_changes_when_config_file_changes(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            db_path = tmp_path / "rencrow.db"
            config_root = tmp_path / "config"
            config_root.mkdir()
            instruments_path = config_root / "instruments.yml"
            instruments_path.write_text('{"instruments":[]}\n', encoding="utf-8")

            first = json.loads(
                run_script("01_init_db.py", "--db-path", str(db_path), "--config-dir", str(config_root), "--json").stdout
            )
            instruments_path.write_text(
                '{"instruments":[{"symbol":"AAA","asset_type":"ETF","venue":"TEST","currency":"JPY","first_date":"2026-01-01"}]}\n',
                encoding="utf-8",
            )
            second = json.loads(
                run_script("01_init_db.py", "--db-path", str(db_path), "--config-dir", str(config_root), "--json").stdout
            )

            self.assertNotEqual(first["config_hash"], second["config_hash"])
            con = sqlite3.connect(db_path)
            hashes = [
                row[0]
                for row in con.execute(
                    "SELECT config_hash FROM cli_run_log WHERE cli_name='01_init_db.py'"
                )
            ]
            con.close()
            self.assertEqual(set(hashes), {first["config_hash"], second["config_hash"]})

    def test_cli_run_log_closes_state_errors_as_failures(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            db_path = Path(td) / "rencrow.db"
            run_script("01_init_db.py", "--db-path", str(db_path), "--json")

            validation = run_script("08_validate_data.py", "--db-path", str(db_path), "--as-of", "latest", "--json", check=False)
            risk = run_script("10_risk_check.py", "--db-path", str(db_path), "--snapshot", "latest", "--json", check=False)

            self.assertEqual(validation.returncode, 4)
            self.assertEqual(risk.returncode, 4)
            con = sqlite3.connect(db_path)
            rows = con.execute(
                """
                SELECT cli_name, status, success_count, fail_count, exit_code, finished_at, detail_json
                  FROM cli_run_log
                 WHERE cli_name IN ('08_validate_data.py', '10_risk_check.py')
                 ORDER BY cli_name
                """
            ).fetchall()
            con.close()
            self.assertEqual(len(rows), 2)
            for row in rows:
                self.assertEqual(row[1], "fail")
                self.assertEqual(row[2], 0)
                self.assertEqual(row[3], 1)
                self.assertEqual(row[4], 4)
                self.assertTrue(row[5])
                self.assertIn("error_message", row[6])

    def test_fetch_cli_returns_partial_exit_code_for_mixed_success_and_failure(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            data_root = tmp_path / "rencrow-data"
            config_root = data_root / "config"
            fixtures = data_root / "fixtures"
            config_root.mkdir(parents=True)
            fixtures.mkdir(parents=True)
            (fixtures / "prices.csv").write_text((ROOT / "fixtures" / "1306T_prices.csv").read_text(encoding="utf-8"), encoding="utf-8")
            (fixtures / "macro.csv").write_text((ROOT / "fixtures" / "macro_series.csv").read_text(encoding="utf-8"), encoding="utf-8")
            (config_root / "instruments.yml").write_text(
                """
{
  "instruments": [
    {"symbol":"OK","asset_type":"ETF","venue":"TEST","currency":"JPY","first_date":"2026-01-01","source_name":"csv_market","fixture":"fixtures/prices.csv"},
    {"symbol":"NG","asset_type":"ETF","venue":"TEST","currency":"JPY","first_date":"2026-01-01","source_name":"csv_market","fixture":"fixtures/missing.csv"}
  ]
}
""",
                encoding="utf-8",
            )
            (config_root / "sources.yml").write_text(
                """
{
  "macro_sources": [
    {"source_name":"ok_macro","fixture":"fixtures/macro.csv"},
    {"source_name":"ng_macro","fixture":"fixtures/missing_macro.csv"}
  ]
}
""",
                encoding="utf-8",
            )
            (config_root / "calendars.yml").write_text('{"calendar_sources":[]}\n', encoding="utf-8")
            db_path = data_root / "data" / "rencrow.db"
            run_script("01_init_db.py", "--db-path", str(db_path), "--config-root", str(config_root))

            market = run_script("02_fetch_market.py", "--db-path", str(db_path), "--config-root", str(config_root), "--data-root", str(data_root), check=False)
            macro = run_script("03_fetch_macro.py", "--db-path", str(db_path), "--config-root", str(config_root), "--data-root", str(data_root), check=False)

            self.assertEqual(market.returncode, 2, market.stdout + market.stderr)
            self.assertEqual(macro.returncode, 2, macro.stdout + macro.stderr)
            con = sqlite3.connect(db_path)
            exit_codes = dict(
                con.execute(
                    """
                    SELECT cli_name, exit_code
                      FROM cli_run_log
                     WHERE cli_name IN ('02_fetch_market.py', '03_fetch_macro.py')
                    """
                ).fetchall()
            )
            con.close()
            self.assertEqual(exit_codes["02_fetch_market.py"], 2)
            self.assertEqual(exit_codes["03_fetch_macro.py"], 2)

    def test_make_daily_refresh_matches_standard_daily_cli_flow(self) -> None:
        result = subprocess.run(
            ["make", "-n", "rencrow-data-daily-refresh", "SNAPSHOT_DATE=today"],
            cwd=REPO,
            text=True,
            capture_output=True,
            check=True,
        )

        self.assertIn("02_fetch_market.py", result.stdout)
        self.assertIn("--mode incremental", result.stdout)
        self.assertIn("03_fetch_macro.py", result.stdout)
        self.assertIn("08_validate_data.py", result.stdout)
        self.assertIn("--as-of today", result.stdout)


if __name__ == "__main__":
    unittest.main()
