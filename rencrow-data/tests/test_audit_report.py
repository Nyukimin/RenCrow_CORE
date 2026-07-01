from __future__ import annotations

import json
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from datetime import date, timedelta
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
REPO = ROOT.parents[0]
SRC = ROOT / "src"


def run_script(script: str, *args: str, check: bool = True) -> subprocess.CompletedProcess[str]:
    cmd = [sys.executable, str(SRC / script), *args]
    return subprocess.run(cmd, cwd=REPO, text=True, capture_output=True, check=check, env={"PYTHONPATH": str(SRC)})


def insert_costed_backtest(
    con: sqlite3.Connection,
    *,
    backtest_id: str,
    snapshot_id: int,
    decision_date: str,
    final_equity: float = 1.1,
    cagr: float = 0.1,
    cost_drag: float = 0.01,
    tax_drag: float = 0.01,
) -> None:
    con.execute(
        """
        INSERT INTO backtest_run(backtest_id, strategy_id, snapshot_id, start_date, end_date, cost_bps, slippage_bps, tax_mode, status)
        VALUES (?, 'weekly_etf_rotation_v1', ?, ?, ?, 10, 5, 'approx_jp_taxable', 'success')
        """,
        (backtest_id, str(snapshot_id), decision_date, decision_date),
    )
    for metric_name, metric_value in (
        ("final_equity", final_equity),
        ("cagr", cagr),
        ("cost_drag", cost_drag),
        ("tax_drag", tax_drag),
    ):
        con.execute(
            """
            INSERT INTO backtest_metric(backtest_id, split_name, metric_name, metric_value)
            VALUES (?, 'full', ?, ?)
            """,
            (backtest_id, metric_name, metric_value),
        )


class AuditReportTest(unittest.TestCase):
    def test_audit_report_writes_markdown(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            db_path = tmp_path / "rencrow.db"
            out_dir = tmp_path / "reports"
            run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(tmp_path / "missing_config"))
            con = sqlite3.connect(db_path)
            con.execute(
                """
                INSERT INTO snapshot_registry(
                  snapshot_id, snapshot_date, db_hash, features_hash, source_summary_json,
                  missing_rate, event_state_json, status
                )
                VALUES (1, '2026-05-16', 'dbhash', 'featurehash', ?, 0.12, ?, 'success')
                """,
                (
                    json.dumps(
                        {
                            "as_of": "2026-05-16",
                            "latest_fetches": [
                                {
                                    "source_name": "csv_market",
                                    "status": "success",
                                    "fetch_id": 7,
                                    "rows_fetched": 120,
                                    "endpoint": "csv:fixtures/prices.csv",
                                    "usage_terms": "local_fixture; internal_research_only; no_redistribution",
                                }
                            ],
                        }
                    ),
                    json.dumps(
                        {
                            "as_of": "2026-05-16",
                            "open_event_count": 1,
                            "max_open_event_risk_score": 0.7,
                            "open_events": [{"level": "warn", "reason": "calendar_cpi", "n": 1}],
                            "latest_open_events": [
                                {
                                    "event_id": 3,
                                    "event_date": "2026-05-15",
                                    "scope": "macro",
                                    "level": "warn",
                                    "reason": "calendar_cpi",
                                    "event_risk_score": 0.7,
                                }
                            ],
                        }
                    ),
                ),
            )
            con.execute(
                """
                INSERT INTO source_fetch_log(source_name, endpoint, requested_at, status, error_message)
                VALUES ('future_source', 'future:endpoint', '2026-05-22T00:00:00Z', 'fail', 'future failure')
                """,
            )
            con.commit()
            con.close()
            result = run_script(
                "14_audit_report.py",
                "--db",
                str(db_path),
                "--snapshot",
                "1",
                "--output-dir",
                str(out_dir),
                "--json",
            )
            summary = json.loads(result.stdout)
            path = Path(summary["output_path"])
            self.assertTrue(path.exists())
            text = path.read_text(encoding="utf-8")
            self.assertIn("# RenCrow Investment Audit Report", text)
            self.assertIn("snapshot_id: 1", text)
            self.assertIn("missing_rate: 0.12", text)
            self.assertIn("### Source Summary", text)
            self.assertIn("| csv_market | success | 7 | 120 | csv:fixtures/prices.csv | local_fixture; internal_research_only; no_redistribution |  |", text)
            self.assertIn("### Event State", text)
            self.assertIn("open_event_count: 1", text)
            self.assertIn("fetch_failures_total: 0", text)
            self.assertIn("fetch_partials_total: 0", text)
            self.assertIn("| warn | calendar_cpi | 1 |", text)
            self.assertIn("| 3 | 2026-05-15 | macro | warn | calendar_cpi | 0.7 |", text)
            self.assertIn("decision: not_found", text)
            self.assertIn("## Paper Trades", text)
            self.assertIn("| - | - | - | - | - | - | - | - | - | - | not_found |", text)
            self.assertIn("Paper Operation Gate", text)
            self.assertEqual(summary["paper_gate"]["status"], "not_ready")
            self.assertEqual(summary["fetch_failures_total"], 0)
            self.assertEqual(summary["fetch_partials_total"], 0)

    def test_audit_report_flags_changed_snapshot_feature_hash(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            db_path = tmp_path / "rencrow.db"
            out_dir = tmp_path / "reports"
            snapshot_dir = tmp_path / "snapshots"
            run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(tmp_path / "missing_config"))
            con = sqlite3.connect(db_path)
            con.execute(
                """
                INSERT INTO instruments(instrument_id, symbol, asset_type, venue, first_date)
                VALUES (1, '1306.T', 'ETF', 'TSE', '2026-01-01')
                """
            )
            con.execute(
                """
                INSERT INTO feature_weekly(instrument_id, week_end, close_adj_jpy, ret_12w_skip1)
                VALUES (1, '2026-05-15', 100, 0.05)
                """
            )
            con.commit()
            con.close()
            snapshot = json.loads(
                run_script(
                    "06_make_snapshot.py",
                    "--db",
                    str(db_path),
                    "--output-dir",
                    str(snapshot_dir),
                    "--snapshot-date",
                    "2026-05-16",
                    "--json",
                ).stdout
            )
            con = sqlite3.connect(db_path)
            con.execute("UPDATE feature_weekly SET ret_12w_skip1=0.99 WHERE week_end='2026-05-15'")
            con.commit()
            con.close()

            result = run_script(
                "14_audit_report.py",
                "--db",
                str(db_path),
                "--snapshot",
                str(snapshot["snapshot_id"]),
                "--output-dir",
                str(out_dir),
                "--json",
            )

            summary = json.loads(result.stdout)
            self.assertFalse(summary["feature_hash_audit"]["match"])
            self.assertEqual(summary["feature_hash_audit"]["expected"], snapshot["features_hash"])
            self.assertNotEqual(summary["feature_hash_audit"]["current"], snapshot["features_hash"])
            text = Path(summary["output_path"]).read_text(encoding="utf-8")
            self.assertIn("features_hash_match: false", text)
            self.assertIn("current_features_hash:", text)

    def test_audit_report_counts_snapshot_fetch_failures_and_partials(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            db_path = tmp_path / "rencrow.db"
            out_dir = tmp_path / "reports"
            run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(tmp_path / "missing_config"))
            con = sqlite3.connect(db_path)
            con.execute(
                """
                INSERT INTO snapshot_registry(
                  snapshot_id, snapshot_date, db_hash, features_hash, source_summary_json, status
                )
                VALUES (1, '2026-05-16', 'dbhash', 'featurehash', ?, 'blocked')
                """,
                (
                    json.dumps(
                        {
                            "as_of": "2026-05-16",
                            "latest_fetches": [
                                {
                                    "source_name": "csv_market",
                                    "status": "fail",
                                    "fetch_id": 8,
                                    "rows_fetched": 0,
                                    "endpoint": "csv:missing_prices.csv",
                                    "usage_terms": "local_fixture; internal_research_only; no_redistribution",
                                    "error_message": "missing fixture",
                                },
                                {
                                    "source_name": "csv_macro",
                                    "status": "partial",
                                    "fetch_id": 9,
                                    "rows_fetched": 3,
                                    "endpoint": "csv:macro.csv",
                                    "usage_terms": "local_fixture; internal_research_only; no_redistribution",
                                    "error_message": "one series unavailable",
                                },
                            ],
                        }
                    ),
                ),
            )
            con.commit()
            con.close()

            result = run_script(
                "14_audit_report.py",
                "--db",
                str(db_path),
                "--snapshot",
                "1",
                "--output-dir",
                str(out_dir),
                "--json",
            )

            summary = json.loads(result.stdout)
            self.assertEqual(summary["fetch_failures_total"], 1)
            self.assertEqual(summary["fetch_partials_total"], 1)
            text = Path(summary["output_path"]).read_text(encoding="utf-8")
            self.assertIn("fetch_failures_total: 1", text)
            self.assertIn("fetch_partials_total: 1", text)
            self.assertIn("| csv_market | fail | 8 | 0 | csv:missing_prices.csv | local_fixture; internal_research_only; no_redistribution | missing fixture |", text)
            self.assertIn("| csv_macro | partial | 9 | 3 | csv:macro.csv | local_fixture; internal_research_only; no_redistribution | one series unavailable |", text)

    def test_audit_report_marks_minimum_paper_gate_ready(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            db_path = tmp_path / "rencrow.db"
            out_dir = tmp_path / "reports"
            run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(tmp_path / "missing_config"))
            con = sqlite3.connect(db_path)
            for idx in range(8):
                snapshot_id = idx + 1
                decision_date = (date(2026, 5, 1) + timedelta(days=idx * 7)).isoformat()
                con.execute(
                    """
                    INSERT INTO snapshot_registry(snapshot_id, snapshot_date, db_hash, features_hash, status)
                    VALUES (?, ?, 'dbhash', 'featurehash', 'success')
                    """,
                    (snapshot_id, decision_date),
                )
                con.execute(
                    """
                    INSERT INTO data_quality_check(run_id, instrument_id, check_date, check_type, severity, status)
                    VALUES (?, NULL, ?, 'fetch_status', 'info', 'pass')
                    """,
                    (f"run-{idx}", decision_date),
                )
                con.execute(
                    """
                    INSERT INTO instruments(instrument_id, symbol, asset_type, venue, first_date)
                    VALUES (?, ?, 'ETF', 'TEST', '2026-01-01')
                    ON CONFLICT(symbol, venue, first_date) DO NOTHING
                    """,
                    (snapshot_id, f"T{idx}"),
                )
                con.execute(
                    """
                    INSERT INTO feature_weekly(instrument_id, week_end, close_adj_jpy)
                    VALUES (?, ?, 100)
                    """,
                    (snapshot_id, decision_date),
                )
                insert_costed_backtest(con, backtest_id=f"bt-{idx}", snapshot_id=snapshot_id, decision_date=decision_date)
                candidate_json = json.dumps(
                    {
                        "approval_required": True,
                        "risk_status": "pass",
                        "risk_check_id": f"risk-{idx}",
                        "week_end": decision_date,
                        "candidates": [
                            {
                                "symbol": f"T{idx}",
                                "instrument_id": snapshot_id,
                                "target_weight": 1.0,
                                "raw_score": 0.2,
                                "adjusted_score": 0.1,
                            }
                        ],
                    },
                    separators=(",", ":"),
                )
                veto_json = json.dumps({"vetoed": idx == 0, "risk_status": "pass"}, separators=(",", ":"))
                con.execute(
                    """
                    INSERT INTO decision_log(
                      snapshot_id, decision_date, account_scope, strategy_name, candidate_json,
                      veto_json, approved, approver, approved_at, approval_reason
                    )
                    VALUES (?, ?, 'paper', 'weekly_etf_rotation_v1', ?, ?, 1, 'unit-test', ?, 'weekly approval')
                    """,
                    (snapshot_id, decision_date, candidate_json, veto_json, f"{decision_date}T00:00:00+00:00"),
                )
                decision_id = con.execute("SELECT last_insert_rowid()").fetchone()[0]
                con.execute(
                    """
                    INSERT INTO risk_check_result(risk_check_id, snapshot_id, strategy_id, decision_id, status)
                    VALUES (?, ?, 'weekly_etf_rotation_v1', ?, 'pass')
                    """,
                    (f"risk-{idx}", str(snapshot_id), str(decision_id)),
                )
                if idx == 1:
                    con.execute(
                        """
                        INSERT INTO paper_trade_log(decision_id, instrument_id, side, quantity, decision_price, simulated_fill_price, fill_model, cost_bps, status)
                        VALUES (?, ?, 'BUY', 10, 100, 101, 'close_next_week', 10, 'simulated')
                        """,
                        (decision_id, snapshot_id),
                    )
                else:
                    con.execute(
                        """
                        INSERT INTO paper_trade_log(decision_id, instrument_id, side, quantity, decision_price, simulated_fill_price, fill_model, cost_bps, status)
                        VALUES (?, ?, 'HOLD', 0, NULL, NULL, 'close_next_week', 10, 'vetoed')
                        """,
                        (decision_id, snapshot_id),
                    )
                if idx == 0:
                    con.execute(
                        """
                        INSERT INTO weekly_signal(signal_id, snapshot_id, strategy_id, week_end, instrument_id, rank, target_weight, vetoed, reason_json)
                        VALUES ('signal-event-veto', ?, 'weekly_etf_rotation_v1', ?, ?, 1, 0, 1, '{"reason":"event_veto"}')
                        """,
                        (str(snapshot_id), decision_date, snapshot_id),
                    )
                con.execute(
                    """
                    INSERT INTO llm_audit_log(llm_log_id, snapshot_id, task_type, model, prompt_version, input_hash, output_hash, output_path)
                    VALUES (?, ?, 'weekly_report', 'local', 'v1', 'in', 'out', 'report.md')
                    """,
                    (f"llm-{idx}", str(snapshot_id)),
                )
            con.commit()
            con.close()
            result = run_script(
                "14_audit_report.py",
                "--db",
                str(db_path),
                "--snapshot",
                "1",
                "--output-dir",
                str(out_dir),
                "--json",
            )
            summary = json.loads(result.stdout)
            self.assertEqual(summary["paper_gate"]["status"], "minimum_ready")
            self.assertEqual(summary["paper_gate"]["paper_weeks"], 8)
            self.assertEqual(summary["paper_gate"]["paper_span_days"], 49)
            self.assertEqual(summary["paper_gate"]["gate_failures"], [])
            self.assertEqual(summary["paper_gate"]["performance_failure_rows"], 0)
            self.assertEqual(summary["paper_gate"]["missing_approval_evidence_rows"], 0)
            self.assertEqual(summary["paper_gate"]["missing_decision_evidence_rows"], 0)
            self.assertEqual(summary["paper_gate"]["tca_rows"], 1)
            self.assertEqual(summary["paper_gate"]["missing_tca_evidence_rows"], 0)
            self.assertEqual(summary["paper_gate"]["event_veto_rows"], 1)
            self.assertGreater(summary["paper_gate"]["no_trade_rows"], 0)
            self.assertEqual(summary["decision_evidence"]["approval_reason"], "weekly approval")
            self.assertEqual(summary["decision_evidence"]["candidate"]["week_end"], "2026-05-01")
            self.assertEqual(summary["decision_evidence"]["signals"][0]["asset_type"], "ETF")
            self.assertEqual(summary["decision_evidence"]["signals"][0]["reason"]["reason"], "event_veto")
            self.assertEqual(len(summary["paper_gate"]["weeks"]), 8)
            self.assertTrue(all(week["complete"] for week in summary["paper_gate"]["weeks"]))
            text = Path(summary["output_path"]).read_text(encoding="utf-8")
            self.assertIn("candidate_json:", text)
            self.assertIn("veto_json:", text)
            self.assertIn("### Decision Signal Evidence", text)
            self.assertIn("| signal-event-veto | T0 | ETF | 1 | 0.0 |  | true |", text)
            self.assertIn('"reason": "event_veto"', text)
            self.assertIn("## Paper Trades", text)
            self.assertIn("| paper_trade_id | snapshot_id | symbol | asset_type | side | quantity | decision_price | simulated_fill_price | fill_model | cost_bps | target_weight | notional | estimated_cost | slippage | status |", text)
            self.assertIn("| 1 |  | T0 | ETF | HOLD | 0.0 |", text)
            self.assertIn("close_next_week", text)
            self.assertIn("### Weekly Ledger", text)
            self.assertIn("paper_span_days: 49", text)
            self.assertIn("performance_failure_rows: 0", text)
            self.assertIn("missing_approval_evidence_rows: 0", text)
            self.assertIn("missing_decision_evidence_rows: 0", text)
            self.assertIn("tca_rows: 1", text)
            self.assertIn("missing_tca_evidence_rows: 0", text)
            self.assertIn("| snapshot_date | decision_id | snapshot_id | complete | missing |", text)
            self.assertIn("approval_reason: weekly approval", text)

    def test_audit_report_marks_preferred_paper_gate_ready_after_12_weeks(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            db_path = tmp_path / "rencrow.db"
            out_dir = tmp_path / "reports"
            run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(tmp_path / "missing_config"))
            con = sqlite3.connect(db_path)
            con.execute(
                """
                INSERT INTO instruments(instrument_id, symbol, asset_type, venue, first_date)
                VALUES (1, 'PREF', 'ETF', 'TEST', '2026-01-01')
                """
            )
            for idx in range(12):
                snapshot_id = idx + 1
                decision_date = (date(2026, 5, 1) + timedelta(days=idx * 7)).isoformat()
                risk_check_id = f"risk-preferred-{idx}"
                con.execute(
                    """
                    INSERT INTO snapshot_registry(snapshot_id, snapshot_date, db_hash, features_hash, status)
                    VALUES (?, ?, 'dbhash', 'featurehash', 'success')
                    """,
                    (snapshot_id, decision_date),
                )
                con.execute(
                    """
                    INSERT INTO data_quality_check(run_id, instrument_id, check_date, check_type, severity, status)
                    VALUES (?, NULL, ?, 'fetch_status', 'info', 'pass')
                    """,
                    (f"quality-preferred-{idx}", decision_date),
                )
                con.execute(
                    """
                    INSERT INTO feature_weekly(instrument_id, week_end, close_adj_jpy)
                    VALUES (1, ?, 100)
                    """,
                    (decision_date,),
                )
                insert_costed_backtest(con, backtest_id=f"bt-preferred-{idx}", snapshot_id=snapshot_id, decision_date=decision_date)
                candidate_json = json.dumps(
                    {
                        "approval_required": True,
                        "risk_status": "pass",
                        "risk_check_id": risk_check_id,
                        "week_end": decision_date,
                        "candidates": [
                            {
                                "symbol": "PREF",
                                "asset_type": "ETF",
                                "instrument_id": 1,
                                "target_weight": 1.0,
                                "raw_score": 0.2,
                                "adjusted_score": 0.1,
                            }
                        ],
                    },
                    separators=(",", ":"),
                )
                veto_json = json.dumps({"vetoed": idx == 0, "risk_status": "pass"}, separators=(",", ":"))
                con.execute(
                    """
                    INSERT INTO decision_log(
                      snapshot_id, decision_date, account_scope, strategy_name, candidate_json,
                      veto_json, approved, approver, approved_at, approval_reason
                    )
                    VALUES (?, ?, 'paper', 'weekly_etf_rotation_v1', ?, ?, 1, 'unit-test', ?, 'preferred weekly approval')
                    """,
                    (snapshot_id, decision_date, candidate_json, veto_json, f"{decision_date}T00:00:00+00:00"),
                )
                decision_id = con.execute("SELECT last_insert_rowid()").fetchone()[0]
                con.execute(
                    """
                    INSERT INTO risk_check_result(risk_check_id, snapshot_id, strategy_id, decision_id, status)
                    VALUES (?, ?, 'weekly_etf_rotation_v1', ?, 'pass')
                    """,
                    (risk_check_id, str(snapshot_id), str(decision_id)),
                )
                con.execute(
                    """
                    INSERT INTO paper_trade_log(decision_id, instrument_id, side, quantity, decision_price, simulated_fill_price, fill_model, cost_bps, status)
                    VALUES (?, 1, ?, ?, ?, ?, 'close_next_week', 10, ?)
                    """,
                    (
                        decision_id,
                        "HOLD" if idx == 0 else "BUY",
                        0 if idx == 0 else 10,
                        None if idx == 0 else 100,
                        None if idx == 0 else 101,
                        "vetoed" if idx == 0 else "simulated",
                    ),
                )
                if idx == 0:
                    con.execute(
                        """
                        INSERT INTO weekly_signal(signal_id, snapshot_id, strategy_id, week_end, instrument_id, rank, target_weight, vetoed, reason_json)
                        VALUES ('signal-preferred-event-veto', ?, 'weekly_etf_rotation_v1', ?, 1, 1, 0, 1, '{"reason":"event_veto"}')
                        """,
                        (str(snapshot_id), decision_date),
                    )
                con.execute(
                    """
                    INSERT INTO llm_audit_log(llm_log_id, snapshot_id, task_type, model, prompt_version, input_hash, output_hash, output_path)
                    VALUES (?, ?, 'weekly_report', 'local', 'v1', 'in', 'out', 'report.md')
                    """,
                    (f"llm-preferred-{idx}", str(snapshot_id)),
                )
            con.commit()
            con.close()

            result = run_script(
                "14_audit_report.py",
                "--db",
                str(db_path),
                "--snapshot",
                "1",
                "--output-dir",
                str(out_dir),
                "--json",
            )

            summary = json.loads(result.stdout)
            self.assertEqual(summary["paper_gate"]["status"], "preferred_ready")
            self.assertEqual(summary["paper_gate"]["paper_weeks"], 12)
            self.assertEqual(summary["paper_gate"]["paper_span_days"], 77)
            self.assertEqual(summary["paper_gate"]["gate_failures"], [])
            self.assertTrue(all(week["complete"] for week in summary["paper_gate"]["weeks"]))
            text = Path(summary["output_path"]).read_text(encoding="utf-8")
            self.assertIn("status: preferred_ready", text)
            self.assertIn("preferred_required_span_days: 77", text)

    def test_audit_report_rejects_missing_approval_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            db_path = tmp_path / "rencrow.db"
            out_dir = tmp_path / "reports"
            run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(tmp_path / "missing_config"))
            con = sqlite3.connect(db_path)
            for idx in range(8):
                snapshot_id = idx + 1
                decision_date = (date(2026, 5, 1) + timedelta(days=idx * 7)).isoformat()
                con.execute(
                    """
                    INSERT INTO snapshot_registry(snapshot_id, snapshot_date, db_hash, features_hash, status)
                    VALUES (?, ?, 'dbhash', 'featurehash', 'success')
                    """,
                    (snapshot_id, decision_date),
                )
                con.execute(
                    """
                    INSERT INTO data_quality_check(run_id, instrument_id, check_date, check_type, severity, status)
                    VALUES (?, NULL, ?, 'fetch_status', 'info', 'pass')
                    """,
                    (f"run-approval-{idx}", decision_date),
                )
                con.execute(
                    """
                    INSERT INTO instruments(instrument_id, symbol, asset_type, venue, first_date)
                    VALUES (?, ?, 'ETF', 'TEST', '2026-01-01')
                    ON CONFLICT(symbol, venue, first_date) DO NOTHING
                    """,
                    (snapshot_id, f"A{idx}"),
                )
                con.execute(
                    "INSERT INTO feature_weekly(instrument_id, week_end, close_adj_jpy) VALUES (?, ?, 100)",
                    (snapshot_id, decision_date),
                )
                insert_costed_backtest(con, backtest_id=f"bt-approval-{idx}", snapshot_id=snapshot_id, decision_date=decision_date)
                con.execute(
                    """
                    INSERT INTO decision_log(
                      snapshot_id, decision_date, account_scope, strategy_name, candidate_json,
                      veto_json, approved, approver, approved_at, approval_reason
                    )
                    VALUES (?, ?, 'paper', 'weekly_etf_rotation_v1', '{"approval_required":true}', '{"vetoed":true}', 1, ?, ?, ?)
                    """,
                    (
                        snapshot_id,
                        decision_date,
                        "" if idx == 0 else "unit-test",
                        "" if idx == 0 else f"{decision_date}T00:00:00+00:00",
                        "" if idx == 0 else "weekly approval",
                    ),
                )
                decision_id = con.execute("SELECT last_insert_rowid()").fetchone()[0]
                con.execute(
                    """
                    INSERT INTO risk_check_result(risk_check_id, snapshot_id, strategy_id, decision_id, status)
                    VALUES (?, ?, 'weekly_etf_rotation_v1', ?, 'pass')
                    """,
                    (f"risk-approval-{idx}", str(snapshot_id), str(decision_id)),
                )
                con.execute(
                    """
                    INSERT INTO paper_trade_log(decision_id, snapshot_id, instrument_id, side, quantity, fill_model, cost_bps, status)
                    VALUES (?, ?, ?, 'HOLD', 0, 'close_next_week', 10, 'vetoed')
                    """,
                    (decision_id, snapshot_id, snapshot_id),
                )
                if idx == 0:
                    con.execute(
                        """
                        INSERT INTO weekly_signal(signal_id, snapshot_id, strategy_id, week_end, instrument_id, rank, target_weight, vetoed, reason_json)
                        VALUES ('signal-approval-event-veto', ?, 'weekly_etf_rotation_v1', ?, ?, 1, 0, 1, '{"reason":"event_veto"}')
                        """,
                        (str(snapshot_id), decision_date, snapshot_id),
                    )
                con.execute(
                    """
                    INSERT INTO llm_audit_log(llm_log_id, snapshot_id, task_type, model, prompt_version, input_hash, output_hash, output_path)
                    VALUES (?, ?, 'weekly_report', 'local', 'v1', 'in', 'out', 'report.md')
                    """,
                    (f"llm-approval-{idx}", str(snapshot_id)),
                )
            con.commit()
            con.close()

            result = run_script(
                "14_audit_report.py",
                "--db",
                str(db_path),
                "--snapshot",
                "1",
                "--output-dir",
                str(out_dir),
                "--json",
            )
            summary = json.loads(result.stdout)
            first_week = summary["paper_gate"]["weeks"][0]
            self.assertEqual(summary["paper_gate"]["status"], "not_ready")
            self.assertEqual(summary["paper_gate"]["missing_approval_evidence_rows"], 1)
            self.assertIn("missing_approval_evidence", summary["paper_gate"]["gate_failures"])
            self.assertFalse(first_week["approval"])
            self.assertIn("approval", first_week["missing"])

    def test_audit_report_rejects_missing_decision_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            db_path = tmp_path / "rencrow.db"
            out_dir = tmp_path / "reports"
            run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(tmp_path / "missing_config"))
            con = sqlite3.connect(db_path)
            for idx in range(8):
                snapshot_id = idx + 1
                decision_date = (date(2026, 5, 1) + timedelta(days=idx * 7)).isoformat()
                risk_check_id = f"risk-decision-evidence-{idx}"
                con.execute(
                    """
                    INSERT INTO snapshot_registry(snapshot_id, snapshot_date, db_hash, features_hash, status)
                    VALUES (?, ?, 'dbhash', 'featurehash', 'success')
                    """,
                    (snapshot_id, decision_date),
                )
                con.execute(
                    """
                    INSERT INTO data_quality_check(run_id, instrument_id, check_date, check_type, severity, status)
                    VALUES (?, NULL, ?, 'fetch_status', 'info', 'pass')
                    """,
                    (f"run-decision-evidence-{idx}", decision_date),
                )
                con.execute(
                    """
                    INSERT INTO instruments(instrument_id, symbol, asset_type, venue, first_date)
                    VALUES (?, ?, 'ETF', 'TEST', '2026-01-01')
                    ON CONFLICT(symbol, venue, first_date) DO NOTHING
                    """,
                    (snapshot_id, f"E{idx}"),
                )
                con.execute(
                    "INSERT INTO feature_weekly(instrument_id, week_end, close_adj_jpy) VALUES (?, ?, 100)",
                    (snapshot_id, decision_date),
                )
                insert_costed_backtest(con, backtest_id=f"bt-decision-evidence-{idx}", snapshot_id=snapshot_id, decision_date=decision_date)
                candidate = {
                    "approval_required": True,
                    "risk_status": "pass",
                    "risk_check_id": risk_check_id,
                    "week_end": decision_date,
                    "candidates": [],
                }
                if idx == 0:
                    del candidate["risk_check_id"]
                con.execute(
                    """
                    INSERT INTO decision_log(
                      snapshot_id, decision_date, account_scope, strategy_name, candidate_json,
                      veto_json, approved, approver, approved_at, approval_reason
                    )
                    VALUES (?, ?, 'paper', 'weekly_etf_rotation_v1', ?, '{"vetoed":true}', 1, 'unit-test', ?, 'weekly approval')
                    """,
                    (
                        snapshot_id,
                        decision_date,
                        json.dumps(candidate, separators=(",", ":")),
                        f"{decision_date}T00:00:00+00:00",
                    ),
                )
                decision_id = con.execute("SELECT last_insert_rowid()").fetchone()[0]
                con.execute(
                    """
                    INSERT INTO risk_check_result(risk_check_id, snapshot_id, strategy_id, decision_id, status)
                    VALUES (?, ?, 'weekly_etf_rotation_v1', ?, 'pass')
                    """,
                    (risk_check_id, str(snapshot_id), str(decision_id)),
                )
                con.execute(
                    """
                    INSERT INTO paper_trade_log(decision_id, snapshot_id, instrument_id, side, quantity, fill_model, cost_bps, status)
                    VALUES (?, ?, ?, 'HOLD', 0, 'close_next_week', 10, 'vetoed')
                    """,
                    (decision_id, snapshot_id, snapshot_id),
                )
                if idx == 0:
                    con.execute(
                        """
                        INSERT INTO weekly_signal(signal_id, snapshot_id, strategy_id, week_end, instrument_id, rank, target_weight, vetoed, reason_json)
                        VALUES ('signal-decision-evidence-veto', ?, 'weekly_etf_rotation_v1', ?, ?, 1, 0, 1, '{"reason":"event_veto"}')
                        """,
                        (str(snapshot_id), decision_date, snapshot_id),
                    )
                con.execute(
                    """
                    INSERT INTO llm_audit_log(llm_log_id, snapshot_id, task_type, model, prompt_version, input_hash, output_hash, output_path)
                    VALUES (?, ?, 'weekly_report', 'local', 'v1', 'in', 'out', 'report.md')
                    """,
                    (f"llm-decision-evidence-{idx}", str(snapshot_id)),
                )
            con.commit()
            con.close()

            result = run_script(
                "14_audit_report.py",
                "--db",
                str(db_path),
                "--snapshot",
                "1",
                "--output-dir",
                str(out_dir),
                "--json",
            )
            summary = json.loads(result.stdout)
            first_week = summary["paper_gate"]["weeks"][0]
            self.assertEqual(summary["paper_gate"]["status"], "not_ready")
            self.assertEqual(summary["paper_gate"]["missing_decision_evidence_rows"], 1)
            self.assertIn("missing_decision_evidence", summary["paper_gate"]["gate_failures"])
            self.assertFalse(first_week["decision_evidence"])
            self.assertEqual(first_week["decision_evidence_missing"], ["risk_check_id"])
            self.assertIn("decision_evidence", first_week["missing"])

    def test_audit_report_rejects_missing_tca_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            db_path = tmp_path / "rencrow.db"
            out_dir = tmp_path / "reports"
            run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(tmp_path / "missing_config"))
            con = sqlite3.connect(db_path)
            for idx in range(8):
                snapshot_id = idx + 1
                decision_date = (date(2026, 5, 1) + timedelta(days=idx * 7)).isoformat()
                risk_check_id = f"risk-tca-{idx}"
                con.execute(
                    """
                    INSERT INTO snapshot_registry(snapshot_id, snapshot_date, db_hash, features_hash, status)
                    VALUES (?, ?, 'dbhash', 'featurehash', 'success')
                    """,
                    (snapshot_id, decision_date),
                )
                con.execute(
                    """
                    INSERT INTO data_quality_check(run_id, instrument_id, check_date, check_type, severity, status)
                    VALUES (?, NULL, ?, 'fetch_status', 'info', 'pass')
                    """,
                    (f"run-tca-{idx}", decision_date),
                )
                con.execute(
                    """
                    INSERT INTO instruments(instrument_id, symbol, asset_type, venue, first_date)
                    VALUES (?, ?, 'ETF', 'TEST', '2026-01-01')
                    ON CONFLICT(symbol, venue, first_date) DO NOTHING
                    """,
                    (snapshot_id, f"TCA{idx}"),
                )
                con.execute(
                    "INSERT INTO feature_weekly(instrument_id, week_end, close_adj_jpy) VALUES (?, ?, 100)",
                    (snapshot_id, decision_date),
                )
                insert_costed_backtest(con, backtest_id=f"bt-tca-{idx}", snapshot_id=snapshot_id, decision_date=decision_date)
                candidate_json = json.dumps(
                    {
                        "approval_required": True,
                        "risk_status": "pass",
                        "risk_check_id": risk_check_id,
                        "week_end": decision_date,
                        "candidates": [],
                    },
                    separators=(",", ":"),
                )
                con.execute(
                    """
                    INSERT INTO decision_log(
                      snapshot_id, decision_date, account_scope, strategy_name, candidate_json,
                      veto_json, approved, approver, approved_at, approval_reason
                    )
                    VALUES (?, ?, 'paper', 'weekly_etf_rotation_v1', ?, '{"vetoed":true}', 1, 'unit-test', ?, 'weekly approval')
                    """,
                    (snapshot_id, decision_date, candidate_json, f"{decision_date}T00:00:00+00:00"),
                )
                decision_id = con.execute("SELECT last_insert_rowid()").fetchone()[0]
                con.execute(
                    """
                    INSERT INTO risk_check_result(risk_check_id, snapshot_id, strategy_id, decision_id, status)
                    VALUES (?, ?, 'weekly_etf_rotation_v1', ?, 'pass')
                    """,
                    (risk_check_id, str(snapshot_id), str(decision_id)),
                )
                if idx == 1:
                    con.execute(
                        """
                        INSERT INTO paper_trade_log(decision_id, snapshot_id, instrument_id, side, quantity, fill_model, cost_bps, status)
                        VALUES (?, ?, ?, 'BUY', 10, 'close_next_week', 10, 'simulated')
                        """,
                        (decision_id, snapshot_id, snapshot_id),
                    )
                else:
                    con.execute(
                        """
                        INSERT INTO paper_trade_log(decision_id, snapshot_id, instrument_id, side, quantity, fill_model, cost_bps, status)
                        VALUES (?, ?, ?, 'HOLD', 0, 'close_next_week', 10, 'vetoed')
                        """,
                        (decision_id, snapshot_id, snapshot_id),
                    )
                if idx == 0:
                    con.execute(
                        """
                        INSERT INTO weekly_signal(signal_id, snapshot_id, strategy_id, week_end, instrument_id, rank, target_weight, vetoed, reason_json)
                        VALUES ('signal-tca-event-veto', ?, 'weekly_etf_rotation_v1', ?, ?, 1, 0, 1, '{"reason":"event_veto"}')
                        """,
                        (str(snapshot_id), decision_date, snapshot_id),
                    )
                con.execute(
                    """
                    INSERT INTO llm_audit_log(llm_log_id, snapshot_id, task_type, model, prompt_version, input_hash, output_hash, output_path)
                    VALUES (?, ?, 'weekly_report', 'local', 'v1', 'in', 'out', 'report.md')
                    """,
                    (f"llm-tca-{idx}", str(snapshot_id)),
                )
            con.commit()
            con.close()

            result = run_script(
                "14_audit_report.py",
                "--db",
                str(db_path),
                "--snapshot",
                "1",
                "--output-dir",
                str(out_dir),
                "--json",
            )
            summary = json.loads(result.stdout)
            second_week = summary["paper_gate"]["weeks"][1]
            self.assertEqual(summary["paper_gate"]["status"], "not_ready")
            self.assertEqual(summary["paper_gate"]["tca_rows"], 0)
            self.assertEqual(summary["paper_gate"]["missing_tca_evidence_rows"], 1)
            self.assertIn("missing_tca_evidence", summary["paper_gate"]["gate_failures"])
            self.assertFalse(second_week["tca"])
            self.assertIn("tca", second_week["missing"])

    def test_audit_report_rejects_compressed_paper_dates(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            db_path = tmp_path / "rencrow.db"
            out_dir = tmp_path / "reports"
            run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(tmp_path / "missing_config"))
            con = sqlite3.connect(db_path)
            for idx in range(8):
                snapshot_id = idx + 1
                decision_date = (date(2026, 5, 1) + timedelta(days=idx)).isoformat()
                con.execute(
                    """
                    INSERT INTO snapshot_registry(snapshot_id, snapshot_date, db_hash, features_hash, status)
                    VALUES (?, ?, 'dbhash', 'featurehash', 'success')
                    """,
                    (snapshot_id, decision_date),
                )
                con.execute(
                    """
                    INSERT INTO data_quality_check(run_id, instrument_id, check_date, check_type, severity, status)
                    VALUES (?, NULL, ?, 'fetch_status', 'info', 'pass')
                    """,
                    (f"run-compressed-{idx}", decision_date),
                )
                con.execute(
                    """
                    INSERT INTO instruments(instrument_id, symbol, asset_type, venue, first_date)
                    VALUES (?, ?, 'ETF', 'TEST', '2026-01-01')
                    ON CONFLICT(symbol, venue, first_date) DO NOTHING
                    """,
                    (snapshot_id, f"C{idx}"),
                )
                con.execute(
                    "INSERT INTO feature_weekly(instrument_id, week_end, close_adj_jpy) VALUES (?, ?, 100)",
                    (snapshot_id, decision_date),
                )
                insert_costed_backtest(con, backtest_id=f"bt-compressed-{idx}", snapshot_id=snapshot_id, decision_date=decision_date)
                con.execute(
                    """
                    INSERT INTO decision_log(snapshot_id, decision_date, account_scope, strategy_name, candidate_json, veto_json, approved)
                    VALUES (?, ?, 'paper', 'weekly_etf_rotation_v1', '{}', '{}', 1)
                    """,
                    (snapshot_id, decision_date),
                )
                decision_id = con.execute("SELECT last_insert_rowid()").fetchone()[0]
                con.execute(
                    """
                    INSERT INTO risk_check_result(risk_check_id, snapshot_id, strategy_id, decision_id, status)
                    VALUES (?, ?, 'weekly_etf_rotation_v1', ?, 'pass')
                    """,
                    (f"risk-compressed-{idx}", str(snapshot_id), str(decision_id)),
                )
                con.execute(
                    """
                    INSERT INTO paper_trade_log(decision_id, snapshot_id, instrument_id, side, quantity, fill_model, cost_bps, status)
                    VALUES (?, ?, NULL, 'HOLD', 0, 'close_next_week', 10, 'vetoed')
                    """,
                    (decision_id, snapshot_id),
                )
                con.execute(
                    """
                    INSERT INTO llm_audit_log(llm_log_id, snapshot_id, task_type, model, prompt_version, input_hash, output_hash, output_path)
                    VALUES (?, ?, 'weekly_report', 'local', 'v1', 'in', 'out', 'report.md')
                    """,
                    (f"llm-compressed-{idx}", str(snapshot_id)),
                )
            con.commit()
            con.close()
            result = run_script(
                "14_audit_report.py",
                "--db",
                str(db_path),
                "--snapshot",
                "1",
                "--output-dir",
                str(out_dir),
                "--json",
            )
            summary = json.loads(result.stdout)
            self.assertEqual(summary["paper_gate"]["paper_weeks"], 8)
            self.assertEqual(summary["paper_gate"]["paper_span_days"], 7)
            self.assertEqual(summary["paper_gate"]["status"], "not_ready")
            self.assertIn("paper_span_lt_8_weeks", summary["paper_gate"]["gate_failures"])

    def test_audit_report_requires_no_trade_and_event_veto_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            db_path = tmp_path / "rencrow.db"
            out_dir = tmp_path / "reports"
            run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(tmp_path / "missing_config"))
            con = sqlite3.connect(db_path)
            for idx in range(8):
                snapshot_id = idx + 1
                decision_date = (date(2026, 5, 1) + timedelta(days=idx * 7)).isoformat()
                con.execute(
                    """
                    INSERT INTO snapshot_registry(snapshot_id, snapshot_date, db_hash, features_hash, status)
                    VALUES (?, ?, 'dbhash', 'featurehash', 'success')
                    """,
                    (snapshot_id, decision_date),
                )
                con.execute(
                    """
                    INSERT INTO data_quality_check(run_id, instrument_id, check_date, check_type, severity, status)
                    VALUES (?, NULL, ?, 'fetch_status', 'info', 'pass')
                    """,
                    (f"run-no-evidence-{idx}", decision_date),
                )
                con.execute(
                    """
                    INSERT INTO instruments(instrument_id, symbol, asset_type, venue, first_date)
                    VALUES (?, ?, 'ETF', 'TEST', '2026-01-01')
                    ON CONFLICT(symbol, venue, first_date) DO NOTHING
                    """,
                    (snapshot_id, f"N{idx}"),
                )
                con.execute(
                    "INSERT INTO feature_weekly(instrument_id, week_end, close_adj_jpy) VALUES (?, ?, 100)",
                    (snapshot_id, decision_date),
                )
                insert_costed_backtest(con, backtest_id=f"bt-no-evidence-{idx}", snapshot_id=snapshot_id, decision_date=decision_date)
                con.execute(
                    """
                    INSERT INTO decision_log(snapshot_id, decision_date, account_scope, strategy_name, candidate_json, veto_json, approved)
                    VALUES (?, ?, 'paper', 'weekly_etf_rotation_v1', '{}', '{}', 1)
                    """,
                    (snapshot_id, decision_date),
                )
                decision_id = con.execute("SELECT last_insert_rowid()").fetchone()[0]
                con.execute(
                    """
                    INSERT INTO risk_check_result(risk_check_id, snapshot_id, strategy_id, decision_id, status)
                    VALUES (?, ?, 'weekly_etf_rotation_v1', ?, 'pass')
                    """,
                    (f"risk-no-evidence-{idx}", str(snapshot_id), str(decision_id)),
                )
                con.execute(
                    """
                    INSERT INTO paper_trade_log(decision_id, snapshot_id, instrument_id, side, quantity, fill_model, cost_bps, status)
                    VALUES (?, ?, ?, 'BUY', 1, 'close_next_week', 10, 'filled')
                    """,
                    (decision_id, snapshot_id, snapshot_id),
                )
                con.execute(
                    """
                    INSERT INTO llm_audit_log(llm_log_id, snapshot_id, task_type, model, prompt_version, input_hash, output_hash, output_path)
                    VALUES (?, ?, 'weekly_report', 'local', 'v1', 'in', 'out', 'report.md')
                    """,
                    (f"llm-no-evidence-{idx}", str(snapshot_id)),
                )
            con.commit()
            con.close()
            result = run_script(
                "14_audit_report.py",
                "--db",
                str(db_path),
                "--snapshot",
                "1",
                "--output-dir",
                str(out_dir),
                "--json",
            )
            summary = json.loads(result.stdout)
            self.assertEqual(summary["paper_gate"]["paper_weeks"], 8)
            self.assertEqual(summary["paper_gate"]["paper_span_days"], 49)
            self.assertEqual(summary["paper_gate"]["status"], "not_ready")
            self.assertIn("missing_no_trade_evidence", summary["paper_gate"]["gate_failures"])
            self.assertIn("missing_event_veto_evidence", summary["paper_gate"]["gate_failures"])

    def test_audit_report_rejects_degraded_tax_cost_performance(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            db_path = tmp_path / "rencrow.db"
            out_dir = tmp_path / "reports"
            run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(tmp_path / "missing_config"))
            con = sqlite3.connect(db_path)
            for idx in range(8):
                snapshot_id = idx + 1
                decision_date = (date(2026, 5, 1) + timedelta(days=idx * 7)).isoformat()
                con.execute(
                    """
                    INSERT INTO snapshot_registry(snapshot_id, snapshot_date, db_hash, features_hash, status)
                    VALUES (?, ?, 'dbhash', 'featurehash', 'success')
                    """,
                    (snapshot_id, decision_date),
                )
                con.execute(
                    """
                    INSERT INTO data_quality_check(run_id, instrument_id, check_date, check_type, severity, status)
                    VALUES (?, NULL, ?, 'fetch_status', 'info', 'pass')
                    """,
                    (f"run-degraded-{idx}", decision_date),
                )
                con.execute(
                    """
                    INSERT INTO instruments(instrument_id, symbol, asset_type, venue, first_date)
                    VALUES (?, ?, 'ETF', 'TEST', '2026-01-01')
                    ON CONFLICT(symbol, venue, first_date) DO NOTHING
                    """,
                    (snapshot_id, f"D{idx}"),
                )
                con.execute(
                    "INSERT INTO feature_weekly(instrument_id, week_end, close_adj_jpy) VALUES (?, ?, 100)",
                    (snapshot_id, decision_date),
                )
                insert_costed_backtest(
                    con,
                    backtest_id=f"bt-degraded-{idx}",
                    snapshot_id=snapshot_id,
                    decision_date=decision_date,
                    final_equity=0.0 if idx == 0 else 1.1,
                    cagr=-1.0 if idx == 0 else 0.1,
                )
                con.execute(
                    """
                    INSERT INTO decision_log(snapshot_id, decision_date, account_scope, strategy_name, candidate_json, veto_json, approved)
                    VALUES (?, ?, 'paper', 'weekly_etf_rotation_v1', ?, '{"vetoed":true}', 1)
                    """,
                    (
                        snapshot_id,
                        decision_date,
                        json.dumps({"approval_required": True, "risk_status": "pass", "week_end": decision_date, "candidates": []}),
                    ),
                )
                decision_id = con.execute("SELECT last_insert_rowid()").fetchone()[0]
                con.execute(
                    """
                    INSERT INTO risk_check_result(risk_check_id, snapshot_id, strategy_id, decision_id, status)
                    VALUES (?, ?, 'weekly_etf_rotation_v1', ?, 'pass')
                    """,
                    (f"risk-degraded-{idx}", str(snapshot_id), str(decision_id)),
                )
                con.execute(
                    """
                    INSERT INTO paper_trade_log(decision_id, snapshot_id, instrument_id, side, quantity, fill_model, cost_bps, status)
                    VALUES (?, ?, NULL, 'HOLD', 0, 'close_next_week', 10, 'vetoed')
                    """,
                    (decision_id, snapshot_id),
                )
                if idx == 0:
                    con.execute(
                        """
                        INSERT INTO weekly_signal(signal_id, snapshot_id, strategy_id, week_end, instrument_id, rank, target_weight, vetoed, reason_json)
                        VALUES ('signal-degraded-event-veto', ?, 'weekly_etf_rotation_v1', ?, ?, 1, 0, 1, '{"reason":"event_veto"}')
                        """,
                        (str(snapshot_id), decision_date, snapshot_id),
                    )
                con.execute(
                    """
                    INSERT INTO llm_audit_log(llm_log_id, snapshot_id, task_type, model, prompt_version, input_hash, output_hash, output_path)
                    VALUES (?, ?, 'weekly_report', 'local', 'v1', 'in', 'out', 'report.md')
                    """,
                    (f"llm-degraded-{idx}", str(snapshot_id)),
                )
            con.commit()
            con.close()
            result = run_script(
                "14_audit_report.py",
                "--db",
                str(db_path),
                "--snapshot",
                "1",
                "--output-dir",
                str(out_dir),
                "--json",
            )
            summary = json.loads(result.stdout)
            self.assertEqual(summary["paper_gate"]["paper_weeks"], 8)
            self.assertEqual(summary["paper_gate"]["paper_span_days"], 49)
            self.assertEqual(summary["paper_gate"]["status"], "not_ready")
            self.assertEqual(summary["paper_gate"]["performance_failure_rows"], 1)
            self.assertIn("missing_or_degraded_tax_cost_performance", summary["paper_gate"]["gate_failures"])

    def test_audit_report_lists_missing_weekly_logs(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            db_path = tmp_path / "rencrow.db"
            out_dir = tmp_path / "reports"
            run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(tmp_path / "missing_config"))
            con = sqlite3.connect(db_path)
            con.execute(
                """
                INSERT INTO snapshot_registry(snapshot_id, snapshot_date, db_hash, features_hash, status)
                VALUES (1, '2026-05-16', 'dbhash', 'featurehash', 'success')
                """,
            )
            con.execute(
                """
                INSERT INTO data_quality_check(run_id, instrument_id, check_date, check_type, severity, status)
                VALUES ('quality-without-fetch', NULL, '2026-05-16', 'missing', 'info', 'pass')
                """,
            )
            con.execute(
                """
                INSERT INTO decision_log(snapshot_id, decision_date, account_scope, strategy_name, candidate_json, veto_json, approved)
                VALUES (1, '2026-05-16', 'paper', 'weekly_etf_rotation_v1', '{}', '{}', 0)
                """,
            )
            con.commit()
            con.close()
            result = run_script(
                "14_audit_report.py",
                "--db",
                str(db_path),
                "--snapshot",
                "1",
                "--output-dir",
                str(out_dir),
                "--json",
            )
            summary = json.loads(result.stdout)
            self.assertEqual(summary["paper_gate"]["status"], "not_ready")
            self.assertEqual(summary["paper_gate"]["missing_decision_paper"], 1)
            self.assertEqual(len(summary["paper_gate"]["weeks"]), 1)
            week = summary["paper_gate"]["weeks"][0]
            self.assertFalse(week["complete"])
            self.assertTrue(week["validation"])
            self.assertFalse(week["fetch"])
            self.assertIn("fetch", week["missing"])
            self.assertNotIn("validation", week["missing"])
            self.assertIn("paper_trade", week["missing"])
            self.assertIn("report", week["missing"])

    def test_paper_gate_does_not_count_report_for_different_decision(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            db_path = tmp_path / "rencrow.db"
            out_dir = tmp_path / "reports"
            run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(tmp_path / "missing_config"))
            con = sqlite3.connect(db_path)
            con.execute(
                """
                INSERT INTO snapshot_registry(snapshot_id, snapshot_date, db_hash, features_hash, status)
                VALUES (1, '2026-05-16', 'dbhash', 'featurehash', 'success')
                """,
            )
            decision_ids: list[int] = []
            for idx in range(2):
                con.execute(
                    """
                    INSERT INTO decision_log(snapshot_id, decision_date, account_scope, strategy_name, candidate_json, veto_json, approved)
                    VALUES (1, '2026-05-16', 'paper', 'weekly_etf_rotation_v1', '{}', '{}', 0)
                    """,
                )
                decision_ids.append(con.execute("SELECT last_insert_rowid()").fetchone()[0])
            con.execute(
                """
                INSERT INTO llm_audit_log(
                  llm_log_id, snapshot_id, decision_id, task_type, model,
                  prompt_version, input_hash, output_hash, output_path
                )
                VALUES ('llm-for-first-decision', '1', ?, 'weekly_report', 'local', 'v1', 'in', 'out', 'report.md')
                """,
                (decision_ids[0],),
            )
            con.commit()
            con.close()

            result = run_script(
                "14_audit_report.py",
                "--db",
                str(db_path),
                "--snapshot",
                "1",
                "--output-dir",
                str(out_dir),
                "--json",
            )

            summary = json.loads(result.stdout)
            weeks = {week["decision_id"]: week for week in summary["paper_gate"]["weeks"]}
            self.assertTrue(weeks[decision_ids[0]]["report"])
            self.assertFalse(weeks[decision_ids[1]]["report"])
            self.assertIn("report", weeks[decision_ids[1]]["missing"])

    def test_audit_report_paper_latest_prefers_traded_decision(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            db_path = tmp_path / "rencrow.db"
            out_dir = tmp_path / "reports"
            run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(tmp_path / "missing_config"))
            con = sqlite3.connect(db_path)
            con.execute(
                """
                INSERT INTO snapshot_registry(snapshot_id, snapshot_date, db_hash, features_hash, status)
                VALUES (1, '2026-05-16', 'dbhash', 'featurehash', 'success')
                """,
            )
            for approved in (1, 0):
                con.execute(
                    """
                    INSERT INTO decision_log(snapshot_id, decision_date, account_scope, strategy_name, candidate_json, veto_json, approved)
                    VALUES (1, '2026-05-16', 'paper', 'weekly_etf_rotation_v1', '{}', '{}', ?)
                    """,
                    (approved,),
                )
            traded_decision_id = 1
            latest_untraded_decision_id = 2
            con.execute(
                """
                INSERT INTO paper_trade_log(decision_id, instrument_id, side, quantity, decision_price, simulated_fill_price, cost_bps, status)
                VALUES (?, NULL, 'HOLD', 0, NULL, NULL, 10, 'vetoed')
                """,
                (traded_decision_id,),
            )
            con.commit()
            con.close()

            latest_result = run_script(
                "14_audit_report.py",
                "--db",
                str(db_path),
                "--snapshot",
                "1",
                "--output-dir",
                str(out_dir),
                "--json",
            )
            latest_summary = json.loads(latest_result.stdout)
            self.assertEqual(latest_summary["decision_id"], latest_untraded_decision_id)

            paper_result = run_script(
                "14_audit_report.py",
                "--db",
                str(db_path),
                "--snapshot",
                "1",
                "--paper-latest",
                "--output-dir",
                str(out_dir),
                "--json",
            )
            paper_summary = json.loads(paper_result.stdout)
            self.assertTrue(paper_summary["paper_latest"])
            self.assertEqual(paper_summary["decision_id"], traded_decision_id)

    def test_audit_report_uses_candidate_risk_check_id_for_regenerated_decision(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            db_path = tmp_path / "rencrow.db"
            out_dir = tmp_path / "reports"
            run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(tmp_path / "missing_config"))
            con = sqlite3.connect(db_path)
            con.execute(
                """
                INSERT INTO snapshot_registry(snapshot_id, snapshot_date, db_hash, features_hash, status)
                VALUES (1, '2026-05-16', 'dbhash', 'featurehash', 'success')
                """,
            )
            for idx in range(2):
                con.execute(
                    """
                    INSERT INTO decision_log(snapshot_id, decision_date, account_scope, strategy_name, candidate_json, veto_json, approved)
                    VALUES (1, '2026-05-16', 'paper', 'weekly_etf_rotation_v1', ?, '{}', 0)
                    """,
                    (json.dumps({"approval_required": True, "risk_check_id": "risk-1", "week_end": "2026-05-16", "candidates": []}),),
                )
            con.execute(
                """
                INSERT INTO risk_check_result(
                  risk_check_id, snapshot_id, strategy_id, decision_id, status,
                  max_dd_check, weekly_loss_check, concentration_check, volatility_check, event_check, detail_json
                )
                VALUES ('risk-1', '1', 'weekly_etf_rotation_v1', '1', 'stop', 'pass', 'pass', 'pass', 'pass', 'fail', '{}')
                """,
            )
            con.commit()
            con.close()

            result = run_script(
                "14_audit_report.py",
                "--db",
                str(db_path),
                "--snapshot",
                "1",
                "--decision",
                "2",
                "--output-dir",
                str(out_dir),
                "--json",
            )
            summary = json.loads(result.stdout)
            text = Path(summary["output_path"]).read_text(encoding="utf-8")
            self.assertEqual(summary["decision_id"], 2)
            self.assertEqual(summary["risk_status"], "stop")
            self.assertIn("risk_check_id: risk-1", text)
            self.assertIn("event_check: fail", text)

    def test_paper_gate_uses_candidate_risk_check_id_for_regenerated_decision(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            db_path = tmp_path / "rencrow.db"
            out_dir = tmp_path / "reports"
            run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(tmp_path / "missing_config"))
            con = sqlite3.connect(db_path)
            for idx in range(8):
                snapshot_id = idx + 1
                decision_date = (date(2026, 5, 1) + timedelta(days=idx * 7)).isoformat()
                risk_check_id = f"risk-regen-{idx}"
                con.execute(
                    """
                    INSERT INTO snapshot_registry(snapshot_id, snapshot_date, db_hash, features_hash, status)
                    VALUES (?, ?, 'dbhash', 'featurehash', 'success')
                    """,
                    (snapshot_id, decision_date),
                )
                con.execute(
                    """
                    INSERT INTO data_quality_check(run_id, instrument_id, check_date, check_type, severity, status)
                    VALUES (?, NULL, ?, 'fetch_status', 'info', 'pass')
                    """,
                    (f"run-regen-{idx}", decision_date),
                )
                con.execute(
                    """
                    INSERT INTO instruments(instrument_id, symbol, asset_type, venue, first_date)
                    VALUES (?, ?, 'ETF', 'TEST', '2026-01-01')
                    ON CONFLICT(symbol, venue, first_date) DO NOTHING
                    """,
                    (snapshot_id, f"R{idx}"),
                )
                con.execute(
                    "INSERT INTO feature_weekly(instrument_id, week_end, close_adj_jpy) VALUES (?, ?, 100)",
                    (snapshot_id, decision_date),
                )
                insert_costed_backtest(con, backtest_id=f"bt-regen-{idx}", snapshot_id=snapshot_id, decision_date=decision_date)
                candidate_json = json.dumps(
                    {
                        "approval_required": True,
                        "risk_status": "pass",
                        "risk_check_id": risk_check_id,
                        "week_end": decision_date,
                        "candidates": [],
                    },
                    separators=(",", ":"),
                )
                if idx == 0:
                    con.execute(
                        """
                        INSERT INTO decision_log(snapshot_id, decision_date, account_scope, strategy_name, candidate_json, veto_json, approved)
                        VALUES (?, ?, 'taxable', 'weekly_etf_rotation_v1', ?, '{}', 0)
                        """,
                        (snapshot_id, decision_date, candidate_json),
                    )
                    linked_decision_id = con.execute("SELECT last_insert_rowid()").fetchone()[0]
                    con.execute(
                        """
                        INSERT INTO decision_log(
                          snapshot_id, decision_date, account_scope, strategy_name, candidate_json,
                          veto_json, approved, approver, approved_at, approval_reason
                        )
                        VALUES (?, ?, 'paper', 'weekly_etf_rotation_v1', ?, '{"vetoed":true}', 1, 'unit-test', ?, 'weekly approval')
                        """,
                        (snapshot_id, decision_date, candidate_json, f"{decision_date}T00:00:00+00:00"),
                    )
                    paper_decision_id = con.execute("SELECT last_insert_rowid()").fetchone()[0]
                else:
                    con.execute(
                        """
                        INSERT INTO decision_log(
                          snapshot_id, decision_date, account_scope, strategy_name, candidate_json,
                          veto_json, approved, approver, approved_at, approval_reason
                        )
                        VALUES (?, ?, 'paper', 'weekly_etf_rotation_v1', ?, '{"vetoed":true}', 1, 'unit-test', ?, 'weekly approval')
                        """,
                        (snapshot_id, decision_date, candidate_json, f"{decision_date}T00:00:00+00:00"),
                    )
                    paper_decision_id = con.execute("SELECT last_insert_rowid()").fetchone()[0]
                    linked_decision_id = paper_decision_id
                con.execute(
                    """
                    INSERT INTO risk_check_result(risk_check_id, snapshot_id, strategy_id, decision_id, status)
                    VALUES (?, ?, 'weekly_etf_rotation_v1', ?, 'pass')
                    """,
                    (risk_check_id, str(snapshot_id), str(linked_decision_id)),
                )
                if idx == 1:
                    con.execute(
                        """
                        INSERT INTO paper_trade_log(
                          decision_id, snapshot_id, instrument_id, side, quantity,
                          decision_price, simulated_fill_price, fill_model, cost_bps, status
                        )
                        VALUES (?, ?, ?, 'BUY', 10, 100, 101, 'close_next_week', 10, 'simulated')
                        """,
                        (paper_decision_id, snapshot_id, snapshot_id),
                    )
                else:
                    con.execute(
                        """
                        INSERT INTO paper_trade_log(decision_id, snapshot_id, instrument_id, side, quantity, fill_model, cost_bps, status)
                        VALUES (?, ?, ?, 'HOLD', 0, 'close_next_week', 10, 'vetoed')
                        """,
                        (paper_decision_id, snapshot_id, snapshot_id),
                    )
                if idx == 0:
                    con.execute(
                        """
                        INSERT INTO weekly_signal(signal_id, snapshot_id, strategy_id, week_end, instrument_id, rank, target_weight, vetoed, reason_json)
                        VALUES ('signal-regen-event-veto', ?, 'weekly_etf_rotation_v1', ?, ?, 1, 0, 1, '{"reason":"event_veto"}')
                        """,
                        (str(snapshot_id), decision_date, snapshot_id),
                    )
                con.execute(
                    """
                    INSERT INTO llm_audit_log(llm_log_id, snapshot_id, task_type, model, prompt_version, input_hash, output_hash, output_path)
                    VALUES (?, ?, 'weekly_report', 'local', 'v1', 'in', 'out', 'report.md')
                    """,
                    (f"llm-regen-{idx}", str(snapshot_id)),
                )
            con.commit()
            con.close()

            result = run_script(
                "14_audit_report.py",
                "--db",
                str(db_path),
                "--snapshot",
                "1",
                "--output-dir",
                str(out_dir),
                "--json",
            )
            summary = json.loads(result.stdout)
            first_week = summary["paper_gate"]["weeks"][0]
            self.assertEqual(summary["paper_gate"]["status"], "minimum_ready")
            self.assertTrue(first_week["risk"])
            self.assertTrue(first_week["complete"])
            self.assertNotIn("risk", first_week["missing"])


if __name__ == "__main__":
    unittest.main()
