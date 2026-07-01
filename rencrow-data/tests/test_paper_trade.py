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
                "event_lookback_days": 7,
                "max_drawdown_limit": 0.99,
                "weekly_loss_limit": 0.99,
                "annualized_volatility_limit": 9.9,
                "turnover_warning_limit": 9.9,
            }
        ),
        encoding="utf-8",
    )
    return data_root, config, data_root / "data" / "rencrow.db", risk_config


def prepare_decision(tmp_path: Path) -> tuple[Path, Path, dict[str, object]]:
    data_root, config_root, db_path, risk_config = write_config(tmp_path)
    run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(config_root))
    con = sqlite3.connect(db_path)
    con.execute(
        "UPDATE strategy_version SET config_hash='unit_test_custom', config_json=? WHERE strategy_id='weekly_etf_rotation_v1'",
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
    run_script("09_backtest_weekly_rotation.py", "--db", str(db_path), "--snapshot", "latest", "--output-dir", str(data_root / "data" / "backtests"))
    run_script("10_risk_check.py", "--db", str(db_path), "--snapshot", "latest", "--risk-config", str(risk_config))
    decision = json.loads(
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
    return data_root, db_path, decision


class PaperTradeTest(unittest.TestCase):
    def test_paper_trade_requires_approved_file(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            data_root, db_path, decision = prepare_decision(Path(td))
            result = run_script(
                "12_paper_trade.py",
                "--db",
                str(db_path),
                "--decision",
                str(decision["decision_id"]),
                "--approval-file",
                decision["approval_path"],
                "--json",
                check=False,
            )
            self.assertEqual(result.returncode, 3)
            con = sqlite3.connect(db_path)
            self.assertEqual(con.execute("SELECT COUNT(*) FROM paper_trade_log").fetchone()[0], 0)

    def test_paper_trade_records_simulated_fill_after_approval(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            data_root, db_path, decision = prepare_decision(Path(td))
            approval_path = Path(decision["approval_path"])
            approval = approval_path.read_text(encoding="utf-8")
            approval = approval.replace("approved: false", "approved: true")
            approval = approval.replace('approver: ""', "approver: unit-test")
            approval = approval.replace('approved_at: ""', "approved_at: 2026-05-16T00:00:00+00:00")
            approval = approval.replace('approval_reason: ""', "approval_reason: weekly paper approval")
            approval_path.write_text(approval, encoding="utf-8")

            result = run_script(
                "12_paper_trade.py",
                "--db",
                str(db_path),
                "--decision",
                str(decision["decision_id"]),
                "--approval-file",
                str(approval_path),
                "--capital",
                "1000000",
                "--json",
            )
            summary = json.loads(result.stdout)
            self.assertEqual(summary["status"], "simulated")
            self.assertEqual(summary["success_count"], 1)
            self.assertEqual(summary["fail_count"], 0)
            self.assertEqual(str(summary["snapshot_id"]), str(decision["snapshot_id"]))
            self.assertEqual(len(summary["trades"]), 1)
            self.assertGreater(summary["trades"][0]["quantity"], 0)
            self.assertEqual(str(summary["trades"][0]["snapshot_id"]), str(decision["snapshot_id"]))
            self.assertGreater(summary["tca"]["estimated_total_cost"], 0)
            self.assertIn("notional_weighted_abs_slippage", summary["tca"])
            self.assertEqual(summary["trades"][0]["fill_model"], "close_next_week")
            self.assertGreater(summary["trades"][0]["notional"], 0)
            self.assertGreater(summary["trades"][0]["estimated_cost"], 0)
            self.assertIsNotNone(summary["trades"][0]["slippage"])

            con = sqlite3.connect(db_path)
            row = con.execute(
                """
                SELECT side, status, quantity, snapshot_id, fill_model, target_weight, notional, estimated_cost, slippage
                  FROM paper_trade_log
                """
            ).fetchone()
            self.assertEqual(row[0], "BUY")
            self.assertEqual(row[1], "simulated")
            self.assertGreater(row[2], 0)
            self.assertEqual(str(row[3]), str(decision["snapshot_id"]))
            self.assertEqual(row[4], "close_next_week")
            self.assertGreater(row[5], 0)
            self.assertGreater(row[6], 0)
            self.assertGreater(row[7], 0)
            self.assertIsNotNone(row[8])
            cli_row = con.execute(
                "SELECT status, success_count, fail_count FROM cli_run_log WHERE run_id=?",
                (summary["cli_run_id"],),
            ).fetchone()
            self.assertEqual(cli_row[0], "simulated")
            self.assertEqual(cli_row[1], 1)
            self.assertEqual(cli_row[2], 0)
            decision_row = con.execute(
                "SELECT approved, approver, approved_at, approval_reason FROM decision_log WHERE decision_id=?",
                (decision["decision_id"],),
            ).fetchone()
            self.assertEqual(decision_row[0], 1)
            self.assertEqual(decision_row[1], "unit-test")
            self.assertEqual(decision_row[2], "2026-05-16T00:00:00+00:00")
            self.assertEqual(decision_row[3], "weekly paper approval")
            lot = con.execute("SELECT account_scope, quantity, acquisition_price FROM tax_lot_log").fetchone()
            self.assertEqual(lot[0], "taxable")
            self.assertGreater(lot[1], 0)
            self.assertGreater(lot[2], 0)

    def test_changed_snapshot_feature_scope_blocks_paper_trade(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            data_root, db_path, decision = prepare_decision(Path(td))
            con = sqlite3.connect(db_path)
            con.execute("UPDATE feature_weekly SET ret_12w_skip1=9.0 WHERE week_end=?", (decision["week_end"],))
            con.commit()
            con.close()
            approval_path = Path(decision["approval_path"])
            approval = approval_path.read_text(encoding="utf-8")
            approval = approval.replace("approved: false", "approved: true")
            approval = approval.replace('approver: ""', "approver: unit-test")
            approval = approval.replace('approved_at: ""', "approved_at: 2026-05-16T00:00:00+00:00")
            approval = approval.replace('approval_reason: ""', "approval_reason: changed feature scope should stop")
            approval_path.write_text(approval, encoding="utf-8")

            result = run_script(
                "12_paper_trade.py",
                "--db",
                str(db_path),
                "--decision",
                str(decision["decision_id"]),
                "--approval-file",
                str(approval_path),
                "--json",
                check=False,
            )

            self.assertEqual(result.returncode, 4)
            self.assertIn("feature_weekly changed since snapshot", result.stderr)
            con = sqlite3.connect(db_path)
            self.assertEqual(con.execute("SELECT COUNT(*) FROM paper_trade_log").fetchone()[0], 0)

    def test_paper_trade_accepts_yaml_approval_file(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            data_root, db_path, decision = prepare_decision(Path(td))
            approval_path = data_root / "approvals" / "latest.yml"
            approval_path.write_text(
                "\n".join(
                    [
                        f"decision_id: {decision['decision_id']}",
                        "approved: true",
                        "approver: unit-test",
                        "approved_at: 2026-05-16T00:00:00+00:00",
                        "approval_reason: weekly paper approval",
                        "",
                    ]
                ),
                encoding="utf-8",
            )

            result = run_script(
                "12_paper_trade.py",
                "--db",
                str(db_path),
                "--decision",
                str(decision["decision_id"]),
                "--approval-file",
                str(approval_path),
                "--json",
            )
            summary = json.loads(result.stdout)
            self.assertEqual(summary["status"], "simulated")

    def test_paper_trade_fill_models_use_next_session_price_assumptions(self) -> None:
        for fill_model, expected_price in (("open_next_session", 123.0), ("vwap_approx", 126.0)):
            with self.subTest(fill_model=fill_model), tempfile.TemporaryDirectory() as td:
                data_root, db_path, decision = prepare_decision(Path(td))
                instrument_id = int(decision["candidates"][0]["instrument_id"])
                con = sqlite3.connect(db_path)
                con.execute(
                    """
                    INSERT INTO price_raw(
                      instrument_id, trade_date, open, high, low, close, adj_close, volume, source_name
                    )
                    VALUES (?, '2026-05-18', 123, 129, 121, 131, 131, 1000, 'unit_test_next_session')
                    """,
                    (instrument_id,),
                )
                con.commit()
                con.close()

                approval_path = Path(decision["approval_path"])
                approval_path.write_text(
                    "\n".join(
                        [
                            f"decision_id: {decision['decision_id']}",
                            f"snapshot_id: {decision['snapshot_id']}",
                            "strategy_id: weekly_etf_rotation_v1",
                            "approved: true",
                            "approver: unit-test",
                            "approved_at: 2026-05-16T00:00:00+00:00",
                            f"approval_reason: approval via {fill_model}",
                            "candidate_symbols:",
                            "  - 1306.T",
                            "",
                        ]
                    ),
                    encoding="utf-8",
                )

                result = run_script(
                    "12_paper_trade.py",
                    "--db",
                    str(db_path),
                    "--decision",
                    str(decision["decision_id"]),
                    "--approval-file",
                    str(approval_path),
                    "--fill-model",
                    fill_model,
                    "--capital",
                    "1000000",
                    "--json",
                    check=False,
                )
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                summary = json.loads(result.stdout)
                self.assertEqual(summary["status"], "simulated")
                self.assertEqual(summary["trades"][0]["fill_model"], fill_model)
                self.assertEqual(summary["trades"][0]["simulated_fill_price"], expected_price)
                self.assertNotEqual(summary["trades"][0]["decision_price"], expected_price)

    def test_paper_trade_rejects_non_paper_decision_scope(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            data_root, db_path, decision = prepare_decision(Path(td))
            con = sqlite3.connect(db_path)
            con.execute("UPDATE decision_log SET account_scope='taxable' WHERE decision_id=?", (decision["decision_id"],))
            con.commit()
            con.close()
            approval_path = Path(decision["approval_path"])
            approval = approval_path.read_text(encoding="utf-8")
            approval = approval.replace("approved: false", "approved: true")
            approval = approval.replace('approver: ""', "approver: unit-test")
            approval = approval.replace('approved_at: ""', "approved_at: 2026-05-16T00:00:00+00:00")
            approval = approval.replace('approval_reason: ""', "approval_reason: taxable decision should not paper trade")
            approval_path.write_text(approval, encoding="utf-8")

            result = run_script(
                "12_paper_trade.py",
                "--db",
                str(db_path),
                "--decision",
                str(decision["decision_id"]),
                "--approval-file",
                str(approval_path),
                "--json",
                check=False,
            )

            self.assertEqual(result.returncode, 3)
            self.assertIn("paper account_scope", result.stderr)
            con = sqlite3.connect(db_path)
            self.assertEqual(con.execute("SELECT COUNT(*) FROM paper_trade_log").fetchone()[0], 0)
            self.assertEqual(con.execute("SELECT COUNT(*) FROM tax_lot_log").fetchone()[0], 0)

    def test_paper_trade_rejects_approval_account_scope_mismatch(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            data_root, db_path, decision = prepare_decision(Path(td))
            approval_path = Path(decision["approval_path"])
            approval = approval_path.read_text(encoding="utf-8")
            approval = approval.replace("approved: false", "approved: true")
            approval = approval.replace('approver: ""', "approver: unit-test")
            approval = approval.replace('approved_at: ""', "approved_at: 2026-05-16T00:00:00+00:00")
            approval = approval.replace('approval_reason: ""', "approval_reason: wrong account scope")
            approval += "account_scope: taxable\n"
            approval_path.write_text(approval, encoding="utf-8")

            result = run_script(
                "12_paper_trade.py",
                "--db",
                str(db_path),
                "--decision",
                str(decision["decision_id"]),
                "--approval-file",
                str(approval_path),
                "--json",
                check=False,
            )

            self.assertEqual(result.returncode, 4)
            self.assertIn("account_scope", result.stderr)
            con = sqlite3.connect(db_path)
            self.assertEqual(con.execute("SELECT COUNT(*) FROM paper_trade_log").fetchone()[0], 0)

    def test_paper_trade_records_vetoed_no_trade(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            data_root, db_path, decision = prepare_decision(Path(td))
            con = sqlite3.connect(db_path)
            con.execute(
                """
                UPDATE decision_log
                   SET veto_json=?, candidate_json=?
                 WHERE decision_id=?
                """,
                (
                    json.dumps({"vetoed": True, "risk_status": "stop"}, sort_keys=True),
                    json.dumps({"approval_required": True, "risk_status": "stop", "week_end": decision["week_end"], "candidates": decision["candidates"]}, sort_keys=True),
                    decision["decision_id"],
                ),
            )
            con.commit()
            con.close()
            approval_path = Path(decision["approval_path"])
            approval = approval_path.read_text(encoding="utf-8")
            approval = approval.replace("approved: false", "approved: true")
            approval = approval.replace('approver: ""', "approver: unit-test")
            approval = approval.replace('approved_at: ""', "approved_at: 2026-05-16T00:00:00+00:00")
            approval = approval.replace('approval_reason: ""', "approval_reason: stopped no trade")
            approval_path.write_text(approval, encoding="utf-8")

            result = run_script(
                "12_paper_trade.py",
                "--db",
                str(db_path),
                "--decision",
                str(decision["decision_id"]),
                "--approval-file",
                str(approval_path),
                "--json",
            )
            summary = json.loads(result.stdout)
            self.assertEqual(summary["status"], "vetoed")
            self.assertEqual(summary["trades"], [])
            con = sqlite3.connect(db_path)
            row = con.execute("SELECT side, quantity, status, fill_model FROM paper_trade_log WHERE decision_id=?", (decision["decision_id"],)).fetchone()
            self.assertEqual(row[0], "HOLD")
            self.assertEqual(row[1], 0)
            self.assertEqual(row[2], "vetoed")
            self.assertEqual(row[3], "close_next_week")

    def test_paper_trade_requires_approval_metadata(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            data_root, db_path, decision = prepare_decision(Path(td))
            approval_path = data_root / "approvals" / "missing_reason.yml"
            approval_path.write_text(
                "\n".join(
                    [
                        f"decision_id: {decision['decision_id']}",
                        "approved: true",
                        "approver: unit-test",
                        "approved_at: 2026-05-16T00:00:00+00:00",
                        "",
                    ]
                ),
                encoding="utf-8",
            )

            result = run_script(
                "12_paper_trade.py",
                "--db",
                str(db_path),
                "--decision",
                str(decision["decision_id"]),
                "--approval-file",
                str(approval_path),
                "--json",
                check=False,
            )
            self.assertEqual(result.returncode, 3)
            self.assertIn("approval_reason", result.stderr)

    def test_paper_trade_rejects_approval_scope_mismatch(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            data_root, db_path, decision = prepare_decision(Path(td))
            approval_path = Path(decision["approval_path"])
            approval = approval_path.read_text(encoding="utf-8")
            approval = approval.replace(f"snapshot_id: {decision['snapshot_id']}", "snapshot_id: 999999")
            approval = approval.replace("approved: false", "approved: true")
            approval = approval.replace('approver: ""', "approver: unit-test")
            approval = approval.replace('approved_at: ""', "approved_at: 2026-05-16T00:00:00+00:00")
            approval = approval.replace('approval_reason: ""', "approval_reason: wrong snapshot approval")
            approval_path.write_text(approval, encoding="utf-8")

            result = run_script(
                "12_paper_trade.py",
                "--db",
                str(db_path),
                "--decision",
                str(decision["decision_id"]),
                "--approval-file",
                str(approval_path),
                "--json",
                check=False,
            )
            self.assertEqual(result.returncode, 4)
            self.assertIn("snapshot_id", result.stderr)
            con = sqlite3.connect(db_path)
            self.assertEqual(con.execute("SELECT COUNT(*) FROM paper_trade_log").fetchone()[0], 0)


if __name__ == "__main__":
    unittest.main()
