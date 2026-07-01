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


def write_config(tmp_path: Path, *, strict_risk: bool = False) -> tuple[Path, Path, Path, Path]:
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
                "event_lookback_days": 7,
                "max_drawdown_limit": 0.001 if strict_risk else 0.99,
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


def prepare_decision_inputs(tmp_path: Path, *, strict_risk: bool = False) -> tuple[Path, Path]:
    data_root, config_root, db_path, risk_config = write_config(tmp_path, strict_risk=strict_risk)
    run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(config_root))
    con = sqlite3.connect(db_path)
    con.execute(
        """
        UPDATE strategy_version
           SET config_hash='unit_test_custom', config_json=?
         WHERE strategy_id='weekly_etf_rotation_v1'
        """,
        (
            json.dumps(
                {
                    "cash_proxy": "1306.T",
                    "drawdown_penalty": 0.25,
                    "score_min": -999.0,
                    "top_n": 1,
                    "universe": ["1306.T"],
                    "volatility_penalty": 0.5,
                },
                sort_keys=True,
            ),
        ),
    )
    con.commit()
    con.close()
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
    run_script(
        "10_risk_check.py",
        "--db",
        str(db_path),
        "--snapshot",
        "latest",
        "--risk-config",
        str(risk_config),
        check=not strict_risk,
    )
    return data_root, db_path


class GenerateDecisionTest(unittest.TestCase):
    def test_generate_decision_writes_signal_log_and_approval_file(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            data_root, db_path = prepare_decision_inputs(Path(td))
            result = run_script(
                "11_generate_decision.py",
                "--db",
                str(db_path),
                "--snapshot",
                "latest",
                "--risk-check",
                "latest",
                "--output-dir",
                str(data_root / "approvals"),
                "--json",
            )
            summary = json.loads(result.stdout)
            self.assertFalse(summary["vetoed"])
            self.assertTrue(summary["approval_required"])
            self.assertFalse(summary["approved"])
            self.assertEqual(summary["candidates"][0]["symbol"], "1306.T")
            self.assertEqual(summary["candidates"][0]["asset_type"], "ETF")
            self.assertTrue(Path(summary["approval_path"]).exists())
            self.assertTrue(summary["approval_path"].endswith(".approval.yml"))
            self.assertTrue(Path(summary["approval_latest_path"]).exists())
            self.assertTrue(Path(summary["approval_json_path"]).exists())
            self.assertEqual(Path(summary["approval_latest_path"]).name, "latest.yml")
            approval_text = Path(summary["approval_path"]).read_text(encoding="utf-8")
            self.assertIn(f"decision_id: {summary['decision_id']}", approval_text)
            self.assertIn("approval_required: true", approval_text)
            self.assertIn("approved: false", approval_text)
            self.assertIn('approval_reason: ""', approval_text)

            con = sqlite3.connect(db_path)
            con.row_factory = sqlite3.Row
            decision = con.execute("SELECT * FROM decision_log WHERE decision_id=?", (summary["decision_id"],)).fetchone()
            self.assertIsNotNone(decision)
            self.assertEqual(decision["approved"], 0)
            self.assertIn('"approval_required":true', decision["candidate_json"])
            self.assertIn('"asset_type":"ETF"', decision["candidate_json"])
            self.assertEqual(con.execute("SELECT COUNT(*) FROM weekly_signal").fetchone()[0], 1)
            reason = con.execute("SELECT reason_json FROM weekly_signal").fetchone()[0]
            self.assertIn('"asset_type":"ETF"', reason)

    def test_generate_decision_defaults_to_latest_risk_check(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            data_root, db_path = prepare_decision_inputs(Path(td))
            result = run_script(
                "11_generate_decision.py",
                "--db",
                str(db_path),
                "--snapshot",
                "latest",
                "--output-dir",
                str(data_root / "approvals"),
                "--json",
            )
            summary = json.loads(result.stdout)
            self.assertEqual(summary["risk_status"], "pass")
            self.assertEqual(summary["candidates"][0]["symbol"], "1306.T")

    def test_same_snapshot_regenerates_same_decision_content(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            data_root, db_path = prepare_decision_inputs(Path(td))
            first = json.loads(
                run_script(
                    "11_generate_decision.py",
                    "--db",
                    str(db_path),
                    "--snapshot",
                    "latest",
                    "--risk-check",
                    "latest",
                    "--output-dir",
                    str(data_root / "approvals"),
                    "--json",
                ).stdout
            )
            second = json.loads(
                run_script(
                    "11_generate_decision.py",
                    "--db",
                    str(db_path),
                    "--snapshot",
                    "latest",
                    "--risk-check",
                    first["risk_check_id"],
                    "--output-dir",
                    str(data_root / "approvals"),
                    "--json",
                ).stdout
            )

            self.assertEqual(first["snapshot_id"], second["snapshot_id"])
            self.assertEqual(first["risk_check_id"], second["risk_check_id"])
            self.assertEqual(first["risk_status"], second["risk_status"])
            self.assertEqual(first["vetoed"], second["vetoed"])
            self.assertEqual(first["week_end"], second["week_end"])
            self.assertEqual(first["candidates"], second["candidates"])
            con = sqlite3.connect(db_path)
            linked_decision = con.execute(
                "SELECT decision_id FROM risk_check_result WHERE risk_check_id=?",
                (first["risk_check_id"],),
            ).fetchone()[0]
            con.close()
            self.assertEqual(str(linked_decision), str(first["decision_id"]))

    def test_future_data_does_not_change_past_snapshot_decision(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            data_root, db_path = prepare_decision_inputs(Path(td))
            before = json.loads(
                run_script(
                    "11_generate_decision.py",
                    "--db",
                    str(db_path),
                    "--snapshot",
                    "latest",
                    "--risk-check",
                    "latest",
                    "--output-dir",
                    str(data_root / "approvals"),
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
                    "11_generate_decision.py",
                    "--db",
                    str(db_path),
                    "--snapshot",
                    before["snapshot_id"],
                    "--risk-check",
                    before["risk_check_id"],
                    "--output-dir",
                    str(data_root / "approvals"),
                    "--json",
                ).stdout
            )

            self.assertEqual(before["week_end"], after["week_end"])
            self.assertEqual(before["candidates"], after["candidates"])

    def test_changed_snapshot_feature_scope_blocks_past_snapshot_decision(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            data_root, db_path = prepare_decision_inputs(Path(td))
            before = json.loads(
                run_script(
                    "11_generate_decision.py",
                    "--db",
                    str(db_path),
                    "--snapshot",
                    "latest",
                    "--risk-check",
                    "latest",
                    "--output-dir",
                    str(data_root / "approvals"),
                    "--json",
                ).stdout
            )
            con = sqlite3.connect(db_path)
            con.execute("UPDATE feature_weekly SET ret_12w_skip1=9.0 WHERE week_end=?", (before["week_end"],))
            con.commit()
            con.close()

            result = run_script(
                "11_generate_decision.py",
                "--db",
                str(db_path),
                "--snapshot",
                before["snapshot_id"],
                "--risk-check",
                before["risk_check_id"],
                "--output-dir",
                str(data_root / "approvals"),
                "--json",
                check=False,
            )

            self.assertEqual(result.returncode, 4)
            self.assertIn("feature_weekly changed since snapshot", result.stderr)

    def test_llm_report_failure_does_not_change_decision_content(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            data_root, db_path = prepare_decision_inputs(Path(td))
            before = json.loads(
                run_script(
                    "11_generate_decision.py",
                    "--db",
                    str(db_path),
                    "--snapshot",
                    "latest",
                    "--risk-check",
                    "latest",
                    "--output-dir",
                    str(data_root / "approvals"),
                    "--json",
                ).stdout
            )
            failed_report = run_script(
                "13_llm_report.py",
                "--db",
                str(db_path),
                "--snapshot",
                "999999",
                "--decision",
                str(before["decision_id"]),
                "--output-dir",
                str(data_root / "reports"),
                "--json",
                check=False,
            )
            self.assertNotEqual(failed_report.returncode, 0)

            after = json.loads(
                run_script(
                    "11_generate_decision.py",
                    "--db",
                    str(db_path),
                    "--snapshot",
                    before["snapshot_id"],
                    "--risk-check",
                    before["risk_check_id"],
                    "--output-dir",
                    str(data_root / "approvals"),
                    "--json",
                ).stdout
            )

            self.assertEqual(before["risk_status"], after["risk_status"])
            self.assertEqual(before["vetoed"], after["vetoed"])
            self.assertEqual(before["week_end"], after["week_end"])
            self.assertEqual(before["candidates"], after["candidates"])

    def test_generate_decision_vetoes_when_risk_stops(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            data_root, db_path = prepare_decision_inputs(Path(td), strict_risk=True)
            result = run_script(
                "11_generate_decision.py",
                "--db",
                str(db_path),
                "--snapshot",
                "latest",
                "--risk-check",
                "latest",
                "--output-dir",
                str(data_root / "approvals"),
                "--json",
            )
            summary = json.loads(result.stdout)
            self.assertTrue(summary["vetoed"])
            self.assertEqual(summary["risk_status"], "stop")
            self.assertEqual(summary["candidates"][0]["target_weight"], 0.0)
            self.assertTrue(Path(summary["approval_path"]).exists())

    def test_generate_decision_rejects_non_tradable_strategy_universe_asset_type(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            data_root, db_path = prepare_decision_inputs(Path(td))
            con = sqlite3.connect(db_path)
            con.execute(
                """
                INSERT INTO instruments(symbol, name, asset_type, venue, currency, first_date)
                VALUES ('STOCKY', 'Stock candidate', 'STOCK', 'TEST', 'JPY', '2026-01-01')
                """
            )
            con.execute(
                """
                UPDATE strategy_version
                   SET config_json=?
                 WHERE strategy_id='weekly_etf_rotation_v1'
                """,
                (
                    json.dumps(
                        {
                            "cash_proxy": "STOCKY",
                            "drawdown_penalty": 0.25,
                            "score_min": -999.0,
                            "top_n": 1,
                            "universe": ["STOCKY"],
                            "volatility_penalty": 0.5,
                        },
                        sort_keys=True,
                    ),
                ),
            )
            con.commit()
            con.close()

            result = run_script(
                "11_generate_decision.py",
                "--db",
                str(db_path),
                "--snapshot",
                "latest",
                "--risk-check",
                "latest",
                "--output-dir",
                str(data_root / "approvals"),
                "--json",
                check=False,
            )

            self.assertEqual(result.returncode, 4)
            self.assertIn("non-tradable asset types", result.stderr)
            con = sqlite3.connect(db_path)
            self.assertEqual(con.execute("SELECT COUNT(*) FROM decision_log WHERE strategy_name='weekly_etf_rotation_v1'").fetchone()[0], 0)


if __name__ == "__main__":
    unittest.main()
