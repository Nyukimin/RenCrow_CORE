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


def write_config(tmp_path: Path) -> tuple[Path, Path, Path, Path]:
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
    risk_config = config / "risk_limits.yml"
    risk_config.write_text(
        json.dumps(
            {
                "event_risk_stop_threshold": 0.9,
                "max_drawdown_limit": 0.99,
                "weekly_loss_limit": 0.99,
                "annualized_volatility_limit": 9.9,
                "turnover_warning_limit": 9.9,
                "asset_class_concentration_limit": 1.0,
                "single_symbol_concentration_limit": 1.0,
            }
        ),
        encoding="utf-8",
    )
    return data_root, config, data_root / "data" / "rencrow.db", risk_config


def prepare_backtest(tmp_path: Path) -> tuple[Path, Path, Path, Path]:
    data_root, config_root, db_path, risk_config = write_config(tmp_path)
    run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(config_root))
    run_script("02_fetch_market.py", "--db", str(db_path), "--config-root", str(config_root), "--data-root", str(data_root))
    run_script("04_build_features.py", "--db", str(db_path))
    run_script("06_make_snapshot.py", "--db", str(db_path), "--output-dir", str(data_root / "data" / "snapshots"), "--snapshot-date", "2026-05-16")
    run_script(
        "09_backtest_weekly_rotation.py",
        "--db",
        str(db_path),
        "--snapshot",
        "latest",
        "--symbols",
        "1306.T",
        "--output-dir",
        str(data_root / "data" / "backtests"),
    )
    return data_root, config_root, db_path, risk_config


class RiskCheckTest(unittest.TestCase):
    def test_risk_check_passes_with_lenient_limits(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            _, _, db_path, risk_config = prepare_backtest(Path(td))
            result = run_script(
                "10_risk_check.py",
                "--db",
                str(db_path),
                "--snapshot",
                "latest",
                "--risk-config",
                str(risk_config),
                "--json",
            )
            summary = json.loads(result.stdout)
            self.assertEqual(summary["status"], "pass")
            con = sqlite3.connect(db_path)
            row = con.execute("SELECT status FROM risk_check_result WHERE risk_check_id=?", (summary["risk_check_id"],)).fetchone()
            self.assertEqual(row[0], "pass")

    def test_changed_snapshot_feature_scope_blocks_risk_check(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            _, _, db_path, risk_config = prepare_backtest(Path(td))
            con = sqlite3.connect(db_path)
            con.execute("UPDATE feature_weekly SET ret_12w_skip1=9.0 WHERE week_end='2026-05-15'")
            con.commit()
            con.close()

            result = run_script(
                "10_risk_check.py",
                "--db",
                str(db_path),
                "--snapshot",
                "latest",
                "--risk-config",
                str(risk_config),
                "--json",
                check=False,
            )

            self.assertEqual(result.returncode, 4)
            self.assertIn("feature_weekly changed since snapshot", result.stderr)

    def test_risk_check_stops_on_strict_drawdown_limit(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            _, _, db_path, risk_config = prepare_backtest(tmp_path)
            risk_config.write_text(
                json.dumps(
                    {
                        "event_risk_stop_threshold": 0.9,
                        "max_drawdown_limit": 0.001,
                        "weekly_loss_limit": 0.99,
                        "annualized_volatility_limit": 9.9,
                        "turnover_warning_limit": 9.9,
                    }
                ),
                encoding="utf-8",
            )
            result = run_script(
                "10_risk_check.py",
                "--db",
                str(db_path),
                "--snapshot",
                "latest",
                "--risk-config",
                str(risk_config),
                "--json",
                check=False,
            )
            self.assertEqual(result.returncode, 3)
            summary = json.loads(result.stdout)
            self.assertEqual(summary["status"], "stop")
            self.assertEqual(summary["max_dd_check"], "fail")
            con = sqlite3.connect(db_path)
            cli_row = con.execute(
                "SELECT status, success_count, fail_count, finished_at FROM cli_run_log WHERE run_id=?",
                (summary["cli_run_id"],),
            ).fetchone()
            self.assertEqual(cli_row[0], "stop")
            self.assertEqual(cli_row[1], 0)
            self.assertEqual(cli_row[2], 1)
            self.assertTrue(cli_row[3])

    def test_risk_check_stops_on_weekly_loss_limit(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            _, _, db_path, risk_config = prepare_backtest(Path(td))
            con = sqlite3.connect(db_path)
            backtest_id = con.execute("SELECT backtest_id FROM backtest_run ORDER BY created_at DESC LIMIT 1").fetchone()[0]
            con.execute(
                "UPDATE backtest_metric SET metric_value=-0.05 WHERE backtest_id=? AND split_name='full' AND metric_name='worst_week'",
                (backtest_id,),
            )
            con.commit()
            con.close()
            risk_config.write_text(
                json.dumps(
                    {
                        "event_risk_stop_threshold": 0.9,
                        "max_drawdown_limit": 0.99,
                        "weekly_loss_limit": 0.01,
                        "annualized_volatility_limit": 9.9,
                        "turnover_warning_limit": 9.9,
                    }
                ),
                encoding="utf-8",
            )

            result = run_script(
                "10_risk_check.py",
                "--db",
                str(db_path),
                "--snapshot",
                "latest",
                "--risk-config",
                str(risk_config),
                "--json",
                check=False,
            )

            self.assertEqual(result.returncode, 3)
            summary = json.loads(result.stdout)
            self.assertEqual(summary["status"], "stop")
            self.assertEqual(summary["weekly_loss_check"], "fail")
            self.assertEqual(summary["max_dd_check"], "pass")
            self.assertEqual(summary["volatility_check"], "pass")

    def test_risk_check_stops_on_annualized_volatility_limit(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            _, _, db_path, risk_config = prepare_backtest(Path(td))
            con = sqlite3.connect(db_path)
            backtest_id = con.execute("SELECT backtest_id FROM backtest_run ORDER BY created_at DESC LIMIT 1").fetchone()[0]
            con.execute(
                "UPDATE backtest_metric SET metric_value=1.50 WHERE backtest_id=? AND split_name='full' AND metric_name='annualized_volatility'",
                (backtest_id,),
            )
            con.commit()
            con.close()
            risk_config.write_text(
                json.dumps(
                    {
                        "event_risk_stop_threshold": 0.9,
                        "max_drawdown_limit": 0.99,
                        "weekly_loss_limit": 0.99,
                        "annualized_volatility_limit": 0.30,
                        "turnover_warning_limit": 9.9,
                    }
                ),
                encoding="utf-8",
            )

            result = run_script(
                "10_risk_check.py",
                "--db",
                str(db_path),
                "--snapshot",
                "latest",
                "--risk-config",
                str(risk_config),
                "--json",
                check=False,
            )

            self.assertEqual(result.returncode, 3)
            summary = json.loads(result.stdout)
            self.assertEqual(summary["status"], "stop")
            self.assertEqual(summary["weekly_loss_check"], "pass")
            self.assertEqual(summary["max_dd_check"], "pass")
            self.assertEqual(summary["volatility_check"], "fail")

    def test_risk_check_reduces_on_turnover_warning(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            _, _, db_path, risk_config = prepare_backtest(Path(td))
            risk_config.write_text(
                json.dumps(
                    {
                        "event_risk_stop_threshold": 0.9,
                        "max_drawdown_limit": 0.99,
                        "weekly_loss_limit": 0.99,
                        "annualized_volatility_limit": 9.9,
                        "turnover_warning_limit": 0.0,
                    }
                ),
                encoding="utf-8",
            )
            result = run_script(
                "10_risk_check.py",
                "--db",
                str(db_path),
                "--snapshot",
                "latest",
                "--risk-config",
                str(risk_config),
                "--json",
            )
            summary = json.loads(result.stdout)
            self.assertEqual(summary["status"], "reduce")
            self.assertEqual(summary["concentration_check"], "warning")
            self.assertEqual(summary["detail"]["turnover_check"], "warning")
            self.assertIn("turnover", summary["detail"]["concentration_reasons"])

    def test_risk_check_reduces_on_asset_class_concentration(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            _, _, db_path, risk_config = prepare_backtest(Path(td))
            con = sqlite3.connect(db_path)
            snapshot = con.execute("SELECT snapshot_id, snapshot_date FROM snapshot_registry ORDER BY snapshot_id DESC LIMIT 1").fetchone()
            instrument_id = con.execute("SELECT instrument_id FROM instruments WHERE symbol='1306.T'").fetchone()[0]
            con.execute(
                """
                INSERT INTO weekly_signal(
                  signal_id, snapshot_id, strategy_id, week_end, instrument_id, rank,
                  target_weight, raw_score, adjusted_score, vetoed, reason_json
                )
                VALUES ('sig_asset_concentration', ?, 'weekly_etf_rotation_v1', ?, ?, 1, 1.0, 0.5, 0.5, 0, '{}')
                """,
                (str(snapshot[0]), snapshot[1], instrument_id),
            )
            con.commit()
            con.close()
            risk_config.write_text(
                json.dumps(
                    {
                        "event_risk_stop_threshold": 0.9,
                        "max_drawdown_limit": 0.99,
                        "weekly_loss_limit": 0.99,
                        "annualized_volatility_limit": 9.9,
                        "turnover_warning_limit": 9.9,
                        "asset_class_concentration_limit": 0.8,
                    }
                ),
                encoding="utf-8",
            )
            result = run_script(
                "10_risk_check.py",
                "--db",
                str(db_path),
                "--snapshot",
                "latest",
                "--risk-config",
                str(risk_config),
                "--json",
            )

            summary = json.loads(result.stdout)
            self.assertEqual(summary["status"], "reduce")
            self.assertEqual(summary["concentration_check"], "warning")
            self.assertEqual(summary["detail"]["asset_class_concentration_check"], "warning")
            self.assertEqual(summary["detail"]["asset_class_concentration"]["max_asset_type"], "ETF")
            self.assertEqual(summary["detail"]["asset_class_concentration"]["max_weight"], 1.0)
            self.assertEqual(summary["detail"]["planned_concentration"]["source"], "backtest_latest_signal")
            self.assertIn("asset_class_concentration", summary["detail"]["concentration_reasons"])

    def test_risk_check_reduces_on_single_symbol_concentration_before_decision(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            _, _, db_path, risk_config = prepare_backtest(Path(td))
            risk_config.write_text(
                json.dumps(
                    {
                        "event_risk_stop_threshold": 0.9,
                        "max_drawdown_limit": 0.99,
                        "weekly_loss_limit": 0.99,
                        "annualized_volatility_limit": 9.9,
                        "turnover_warning_limit": 9.9,
                        "asset_class_concentration_limit": 1.0,
                        "single_symbol_concentration_limit": 0.8,
                    }
                ),
                encoding="utf-8",
            )
            result = run_script(
                "10_risk_check.py",
                "--db",
                str(db_path),
                "--snapshot",
                "latest",
                "--risk-config",
                str(risk_config),
                "--json",
            )

            summary = json.loads(result.stdout)
            self.assertEqual(summary["status"], "reduce")
            self.assertEqual(summary["concentration_check"], "warning")
            self.assertEqual(summary["detail"]["single_symbol_concentration_check"], "warning")
            self.assertEqual(summary["detail"]["planned_concentration"]["max_symbol"], "1306.T")
            self.assertEqual(summary["detail"]["planned_concentration"]["max_symbol_weight"], 1.0)
            self.assertIn("single_symbol_concentration", summary["detail"]["concentration_reasons"])

    def test_risk_check_stops_on_data_quality_partial(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            _, _, db_path, risk_config = prepare_backtest(Path(td))
            con = sqlite3.connect(db_path)
            snapshot_date = con.execute("SELECT snapshot_date FROM snapshot_registry ORDER BY snapshot_id DESC LIMIT 1").fetchone()[0]
            con.execute(
                """
                INSERT INTO data_quality_check(run_id, instrument_id, check_date, check_type, severity, status, metric_value, detail_json)
                VALUES ('quality-partial', NULL, ?, 'fetch_partial', 'warning', 'partial', 1, '{"source":"csv_market"}')
                """,
                (snapshot_date,),
            )
            con.commit()
            con.close()

            result = run_script(
                "10_risk_check.py",
                "--db",
                str(db_path),
                "--snapshot",
                "latest",
                "--risk-config",
                str(risk_config),
                "--json",
                check=False,
            )

            self.assertEqual(result.returncode, 3)
            summary = json.loads(result.stdout)
            self.assertEqual(summary["status"], "stop")
            self.assertEqual(summary["event_check"], "fail")
            self.assertEqual(summary["detail"]["quality_partials"], 1)
            self.assertEqual(summary["detail"]["quality_blockers"], 0)

    def test_risk_check_reduces_on_data_quality_warning(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            _, _, db_path, risk_config = prepare_backtest(Path(td))
            con = sqlite3.connect(db_path)
            snapshot_date = con.execute("SELECT snapshot_date FROM snapshot_registry ORDER BY snapshot_id DESC LIMIT 1").fetchone()[0]
            con.execute(
                """
                INSERT INTO data_quality_check(run_id, instrument_id, check_date, check_type, severity, status, metric_value, detail_json)
                VALUES ('quality-warning', NULL, ?, 'missing', 'warning', 'fail', 1, '{"scope":"non_tradable"}')
                """,
                (snapshot_date,),
            )
            con.commit()
            con.close()

            result = run_script(
                "10_risk_check.py",
                "--db",
                str(db_path),
                "--snapshot",
                "latest",
                "--risk-config",
                str(risk_config),
                "--json",
            )

            summary = json.loads(result.stdout)
            self.assertEqual(summary["status"], "reduce")
            self.assertEqual(summary["event_check"], "pass")
            self.assertEqual(summary["detail"]["quality_warnings"], 1)

    def test_risk_check_kill_switch_on_stop_event(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            _, _, db_path, risk_config = prepare_backtest(Path(td))
            stop = run_script(
                "15_manual_stop.py",
                "--db",
                str(db_path),
                "--operator",
                "unit-test",
                "--reason",
                "manual risk stop",
                "--json",
            )
            stop_summary = json.loads(stop.stdout)
            self.assertEqual(stop_summary["event_reason"], "manual_kill_switch")
            result = run_script(
                "10_risk_check.py",
                "--db",
                str(db_path),
                "--snapshot",
                "latest",
                "--risk-config",
                str(risk_config),
                "--json",
                check=False,
            )
            self.assertEqual(result.returncode, 3)
            summary = json.loads(result.stdout)
            self.assertEqual(summary["status"], "kill_switch")
            self.assertEqual(summary["event_check"], "fail")
            con = sqlite3.connect(db_path)
            cli_row = con.execute(
                "SELECT status, success_count, fail_count, finished_at FROM cli_run_log WHERE run_id=?",
                (summary["cli_run_id"],),
            ).fetchone()
            self.assertEqual(cli_row[0], "kill_switch")
            self.assertEqual(cli_row[1], 0)
            self.assertEqual(cli_row[2], 1)
            self.assertTrue(cli_row[3])
            context = con.execute("SELECT context_json FROM event_log WHERE event_id=?", (stop_summary["event_id"],)).fetchone()[0]
            self.assertIn("manual risk stop", context)

    def test_risk_check_stops_on_event_risk_threshold_without_kill_switch(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            _, _, db_path, risk_config = prepare_backtest(Path(td))
            con = sqlite3.connect(db_path)
            con.execute(
                """
                INSERT INTO event_log(event_ts, scope, level, reason, event_risk_score, context_json)
                VALUES ('2026-05-16T00:00:00+00:00', 'macro', 'warn', 'calendar_cpi', 0.7, '{"event":"CPI"}')
                """
            )
            con.commit()
            con.close()
            risk_config.write_text(
                json.dumps(
                    {
                        "event_risk_stop_threshold": 0.7,
                        "event_lookback_days": 7,
                        "max_drawdown_limit": 0.99,
                        "weekly_loss_limit": 0.99,
                        "annualized_volatility_limit": 9.9,
                        "turnover_warning_limit": 9.9,
                    }
                ),
                encoding="utf-8",
            )

            result = run_script(
                "10_risk_check.py",
                "--db",
                str(db_path),
                "--snapshot",
                "latest",
                "--risk-config",
                str(risk_config),
                "--json",
                check=False,
            )

            self.assertEqual(result.returncode, 3)
            summary = json.loads(result.stdout)
            self.assertEqual(summary["status"], "stop")
            self.assertEqual(summary["event_check"], "fail")
            self.assertEqual(summary["detail"]["event_blockers"], 1)
            self.assertEqual(summary["detail"]["kill_event_blockers"], 0)

    def test_manual_stop_resolution_allows_risk_check_after_audit_note(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            _, _, db_path, risk_config = prepare_backtest(Path(td))
            stop = run_script(
                "15_manual_stop.py",
                "--db",
                str(db_path),
                "--operator",
                "unit-test",
                "--reason",
                "manual risk stop",
                "--json",
            )
            stop_summary = json.loads(stop.stdout)
            resolve = run_script(
                "15_manual_stop.py",
                "--db",
                str(db_path),
                "--operator",
                "unit-test",
                "--reason",
                "manual review cleared",
                "--resolve-event-id",
                str(stop_summary["event_id"]),
                "--json",
            )
            resolve_summary = json.loads(resolve.stdout)
            self.assertTrue(resolve_summary["resolved"])
            result = run_script(
                "10_risk_check.py",
                "--db",
                str(db_path),
                "--snapshot",
                "latest",
                "--risk-config",
                str(risk_config),
                "--json",
            )
            summary = json.loads(result.stdout)
            self.assertEqual(summary["status"], "pass")
            self.assertEqual(summary["event_check"], "pass")
            con = sqlite3.connect(db_path)
            row = con.execute(
                "SELECT resolved_at, resolution_note FROM event_log WHERE event_id=?",
                (stop_summary["event_id"],),
            ).fetchone()
            self.assertTrue(row[0])
            self.assertIn("manual review cleared", row[1])

    def test_manual_stop_resolution_input_errors_return_code_4_and_log_failure(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            _, _, db_path, _ = prepare_backtest(Path(td))
            missing = run_script(
                "15_manual_stop.py",
                "--db",
                str(db_path),
                "--operator",
                "unit-test",
                "--reason",
                "manual review cleared",
                "--resolve-event-id",
                "9999",
                "--json",
                check=False,
            )
            self.assertEqual(missing.returncode, 4)
            self.assertIn("event not found: 9999", missing.stderr)

            stop = run_script(
                "15_manual_stop.py",
                "--db",
                str(db_path),
                "--operator",
                "unit-test",
                "--reason",
                "manual risk stop",
                "--json",
            )
            stop_summary = json.loads(stop.stdout)
            run_script(
                "15_manual_stop.py",
                "--db",
                str(db_path),
                "--operator",
                "unit-test",
                "--reason",
                "manual review cleared",
                "--resolve-event-id",
                str(stop_summary["event_id"]),
                "--json",
            )
            duplicate = run_script(
                "15_manual_stop.py",
                "--db",
                str(db_path),
                "--operator",
                "unit-test",
                "--reason",
                "manual review cleared again",
                "--resolve-event-id",
                str(stop_summary["event_id"]),
                "--json",
                check=False,
            )
            self.assertEqual(duplicate.returncode, 4)
            self.assertIn("event is already resolved", duplicate.stderr)

            con = sqlite3.connect(db_path)
            rows = con.execute(
                """
                SELECT status, fail_count, detail_json
                  FROM cli_run_log
                 WHERE cli_name='15_manual_stop.py'
                   AND status='fail'
                 ORDER BY started_at
                """
            ).fetchall()
            con.close()
            self.assertEqual(len(rows), 2)
            self.assertTrue(all(row[0] == "fail" for row in rows))
            self.assertTrue(all(row[1] == 1 for row in rows))
            details = [json.loads(row[2]) for row in rows]
            self.assertTrue(all(detail["exit_code"] == 4 for detail in details))
            self.assertIn("event not found: 9999", details[0]["error_message"])
            self.assertIn("event is already resolved", details[1]["error_message"])


if __name__ == "__main__":
    unittest.main()
