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


def write_fixture_tree(tmp_path: Path) -> tuple[Path, Path, Path]:
    data_root = tmp_path / "rencrow-data"
    config = data_root / "config"
    fixtures = data_root / "fixtures"
    config.mkdir(parents=True)
    fixtures.mkdir(parents=True)
    (fixtures / "prices.csv").write_text((ROOT / "fixtures" / "1306T_prices.csv").read_text(encoding="utf-8"), encoding="utf-8")
    (fixtures / "macro.csv").write_text((ROOT / "fixtures" / "macro_series.csv").read_text(encoding="utf-8"), encoding="utf-8")
    (fixtures / "calendar.csv").write_text((ROOT / "fixtures" / "economic_calendar.csv").read_text(encoding="utf-8"), encoding="utf-8")
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
    (config / "sources.yml").write_text(json.dumps({"macro_sources": [{"source_name": "csv_macro", "fixture": "fixtures/macro.csv"}]}), encoding="utf-8")
    (config / "calendars.yml").write_text(json.dumps({"calendar_sources": [{"source_name": "csv_calendar", "fixture": "fixtures/calendar.csv"}]}), encoding="utf-8")
    (config / "risk_limits.yml").write_text(
        json.dumps(
            {
                "event_risk_stop_threshold": 0.9,
                "event_lookback_days": 7,
                "max_drawdown_limit": 0.99,
                "weekly_loss_limit": 0.99,
                "annualized_volatility_limit": 9.9,
                "turnover_warning_limit": 9.9,
            }
        ),
        encoding="utf-8",
    )
    return data_root, config, data_root / "data" / "rencrow.db"


class WeeklyCLIFlowTest(unittest.TestCase):
    def test_weekly_research_flow_runs_through_reports(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            data_root, config_root, db_path = write_fixture_tree(Path(td))
            run_script("01_init_db.py", "--db-path", str(db_path), "--config-root", str(config_root))
            con = sqlite3.connect(db_path)
            con.execute(
                "UPDATE strategy_version SET config_hash='unit_test_custom', config_json=? WHERE strategy_id='weekly_etf_rotation_v1'",
                (
                    json.dumps(
                        {
                            "cash_proxy": "1306.T",
                            "drawdown_penalty": 0.25,
                            "event_veto_threshold": 0.7,
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

            run_script("02_fetch_market.py", "--db-path", str(db_path), "--config-root", str(config_root), "--data-root", str(data_root))
            run_script("03_fetch_macro.py", "--db-path", str(db_path), "--config-root", str(config_root), "--data-root", str(data_root))
            run_script("04_build_features.py", "--db-path", str(db_path), "--week-end", "latest")
            run_script("05_detect_events.py", "--db-path", str(db_path), "--week-end", "latest")
            run_script(
                "08_validate_data.py",
                "--db-path",
                str(db_path),
                "--as-of",
                "2026-05-15",
                "--min-history-days",
                "140",
                "--max-missing-rate",
                "0.90",
            )
            run_script("06_make_snapshot.py", "--db-path", str(db_path), "--output-dir", str(data_root / "data" / "snapshots"), "--week-end", "latest")
            run_script("09_backtest_weekly_rotation.py", "--db-path", str(db_path), "--snapshot", "latest", "--output-dir", str(data_root / "data" / "backtests"))
            run_script("10_risk_check.py", "--db-path", str(db_path), "--snapshot", "latest", "--risk-config", str(config_root / "risk_limits.yml"))
            decision = json.loads(
                run_script(
                    "11_generate_decision.py",
                    "--db-path",
                    str(db_path),
                    "--snapshot",
                    "latest",
                    "--output-dir",
                    str(data_root / "approvals"),
                    "--json",
                ).stdout
            )
            report = json.loads(
                run_script(
                    "13_llm_report.py",
                    "--db-path",
                    str(db_path),
                    "--snapshot",
                    "latest",
                    "--decision",
                    str(decision["decision_id"]),
                    "--output-dir",
                    str(data_root / "reports"),
                    "--json",
                ).stdout
            )
            approval_path = Path(decision["approval_path"])
            approval = approval_path.read_text(encoding="utf-8")
            approval = approval.replace("approved: false", "approved: true")
            approval = approval.replace('approver: ""', "approver: unit-test")
            approval = approval.replace('approved_at: ""', "approved_at: 2026-05-16T00:00:00+00:00")
            approval = approval.replace('approval_reason: ""', "approval_reason: weekly fixture paper approval")
            approval_path.write_text(approval, encoding="utf-8")
            paper = json.loads(
                run_script(
                    "12_paper_trade.py",
                    "--db-path",
                    str(db_path),
                    "--decision",
                    str(decision["decision_id"]),
                    "--approval-file",
                    str(approval_path),
                    "--fill-model",
                    "close_next_week",
                    "--json",
                ).stdout
            )
            audit = json.loads(
                run_script(
                    "14_audit_report.py",
                    "--db-path",
                    str(db_path),
                    "--snapshot",
                    "latest",
                    "--decision",
                    str(decision["decision_id"]),
                    "--paper-latest",
                    "--output-dir",
                    str(data_root / "reports"),
                    "--json",
                ).stdout
            )

            self.assertTrue(Path(decision["approval_path"]).exists())
            self.assertTrue(Path(report["output_path"]).exists())
            self.assertTrue(Path(audit["output_path"]).exists())
            self.assertEqual(paper["decision_id"], decision["decision_id"])
            self.assertEqual(paper["status"], "simulated")
            self.assertEqual(paper["tca"]["fill_model"], "close_next_week")
            self.assertEqual(audit["decision_id"], decision["decision_id"])
            self.assertGreaterEqual(audit["paper_gate"]["paper_weeks"], 1)
            con = sqlite3.connect(db_path)
            self.assertEqual(con.execute("SELECT COUNT(*) FROM decision_log").fetchone()[0], 1)
            self.assertEqual(con.execute("SELECT COUNT(*) FROM llm_audit_log").fetchone()[0], 1)
            self.assertEqual(con.execute("SELECT COUNT(*) FROM paper_trade_log").fetchone()[0], 1)
            self.assertEqual(con.execute("SELECT COUNT(*) FROM tax_lot_log").fetchone()[0], 1)
            self.assertGreater(con.execute("SELECT COUNT(*) FROM backtest_metric").fetchone()[0], 0)


if __name__ == "__main__":
    unittest.main()
