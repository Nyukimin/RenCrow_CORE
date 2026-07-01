from __future__ import annotations

import hashlib
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


class LLMReportTest(unittest.TestCase):
    def _row_signature(self, con: sqlite3.Connection, table: str, where: str, params: tuple[object, ...]) -> str:
        row = con.execute(f"SELECT * FROM {table} WHERE {where}", params).fetchone()
        self.assertIsNotNone(row)
        columns = [item[1] for item in con.execute(f"PRAGMA table_info({table})").fetchall()]
        payload = {column: row[idx] for idx, column in enumerate(columns)}
        return json.dumps(payload, ensure_ascii=False, sort_keys=True)

    def test_llm_report_writes_output_and_audit_log(self) -> None:
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
                """
            )
            con.commit()
            con.close()
            result = run_script(
                "13_llm_report.py",
                "--db",
                str(db_path),
                "--snapshot",
                "1",
                "--output-dir",
                str(out_dir),
                "--json",
            )
            summary = json.loads(result.stdout)
            self.assertEqual(summary["model"], "local-deterministic")
            self.assertEqual(summary["prompt_version"], "weekly_report_v1")
            self.assertEqual(summary["uncertainty_flag"], 0)
            self.assertTrue(Path(summary["output_path"]).exists())
            con = sqlite3.connect(db_path)
            text = Path(summary["output_path"]).read_text(encoding="utf-8")
            row = con.execute(
                """
                SELECT snapshot_id, decision_id, task_type, model, prompt_version, input_hash, output_hash, output_path, uncertainty_flag
                  FROM llm_audit_log
                 WHERE llm_log_id=?
                """,
                (summary["llm_log_id"],),
            ).fetchone()
            self.assertEqual(str(row[0]), "1")
            self.assertIsNone(row[1])
            self.assertEqual(row[2], "weekly_report")
            self.assertEqual(row[3], "local-deterministic")
            self.assertEqual(row[4], "weekly_report_v1")
            self.assertRegex(row[5], r"^[0-9a-f]{64}$")
            self.assertEqual(row[6], hashlib.sha256(text.encode("utf-8")).hexdigest())
            self.assertEqual(row[7], summary["output_path"])
            self.assertEqual(row[8], 0)
            self.assertIn("uncertainty_flag: 0", text)

    def test_llm_report_sets_uncertainty_flag_for_quality_blocker(self) -> None:
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
                """
            )
            con.execute(
                """
                INSERT INTO data_quality_check(run_id, instrument_id, check_date, check_type, severity, status)
                VALUES ('run-blocker', NULL, '2026-05-16', 'missing', 'blocker', 'fail')
                """
            )
            con.commit()
            con.close()
            result = run_script(
                "13_llm_report.py",
                "--db",
                str(db_path),
                "--snapshot",
                "1",
                "--output-dir",
                str(out_dir),
                "--json",
            )
            summary = json.loads(result.stdout)
            self.assertEqual(summary["uncertainty_flag"], 1)
            con = sqlite3.connect(db_path)
            row = con.execute("SELECT uncertainty_flag FROM llm_audit_log WHERE llm_log_id=?", (summary["llm_log_id"],)).fetchone()
            self.assertEqual(row[0], 1)
            text = Path(summary["output_path"]).read_text(encoding="utf-8")
            self.assertIn("quality_blockers: 1", text)
            self.assertIn("uncertainty_flag: 1", text)

    def test_llm_report_accepts_spec_generation_task_type(self) -> None:
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
                """
            )
            con.commit()
            con.close()

            result = run_script(
                "13_llm_report.py",
                "--db",
                str(db_path),
                "--snapshot",
                "1",
                "--task",
                "spec_generation",
                "--output-dir",
                str(out_dir),
                "--json",
            )

            summary = json.loads(result.stdout)
            self.assertEqual(summary["task"], "spec_generation")
            self.assertTrue(Path(summary["output_path"]).exists())
            text = Path(summary["output_path"]).read_text(encoding="utf-8")
            self.assertIn("task: spec_generation", text)
            con = sqlite3.connect(db_path)
            row = con.execute("SELECT task_type FROM llm_audit_log WHERE llm_log_id=?", (summary["llm_log_id"],)).fetchone()
            con.close()
            self.assertEqual(row[0], "spec_generation")

    def test_llm_report_rejects_decision_from_different_snapshot(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            db_path = tmp_path / "rencrow.db"
            out_dir = tmp_path / "reports"
            run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(tmp_path / "missing_config"))
            con = sqlite3.connect(db_path)
            con.execute(
                """
                INSERT INTO snapshot_registry(snapshot_id, snapshot_date, db_hash, features_hash, status)
                VALUES (1, '2026-05-16', 'dbhash1', 'featurehash1', 'success')
                """
            )
            con.execute(
                """
                INSERT INTO snapshot_registry(snapshot_id, snapshot_date, db_hash, features_hash, status)
                VALUES (2, '2026-05-23', 'dbhash2', 'featurehash2', 'success')
                """
            )
            con.execute(
                """
                INSERT INTO decision_log(snapshot_id, decision_date, account_scope, strategy_name, candidate_json, veto_json, approved)
                VALUES (2, '2026-05-23', 'paper', 'weekly_etf_rotation_v1', '{}', '{}', 0)
                """
            )
            decision_id = con.execute("SELECT last_insert_rowid()").fetchone()[0]
            con.commit()
            con.close()

            result = run_script(
                "13_llm_report.py",
                "--db",
                str(db_path),
                "--snapshot",
                "1",
                "--decision",
                str(decision_id),
                "--output-dir",
                str(out_dir),
                "--json",
                check=False,
            )

            self.assertNotEqual(result.returncode, 0)
            self.assertIn("snapshot_id", result.stderr)
            self.assertFalse(any(out_dir.glob("*.md")))
            con = sqlite3.connect(db_path)
            self.assertEqual(con.execute("SELECT COUNT(*) FROM llm_audit_log").fetchone()[0], 0)

    def test_llm_report_uses_candidate_risk_check_id_for_regenerated_decision(self) -> None:
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
                """
            )
            for _ in range(2):
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
                VALUES ('risk-1', '1', 'weekly_etf_rotation_v1', '1', 'kill_switch', 'pass', 'pass', 'pass', 'pass', 'fail', '{}')
                """
            )
            con.commit()
            con.close()

            result = run_script(
                "13_llm_report.py",
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
            self.assertEqual(summary["decision_id"], 2)
            self.assertEqual(summary["uncertainty_flag"], 1)
            text = Path(summary["output_path"]).read_text(encoding="utf-8")
            self.assertIn("risk_check_id: risk-1", text)
            self.assertIn("status: kill_switch", text)
            self.assertIn("event_check: fail", text)

    def test_llm_report_success_does_not_mutate_decision_or_risk_result(self) -> None:
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
                """
            )
            con.execute(
                """
                INSERT INTO decision_log(snapshot_id, decision_date, account_scope, strategy_name, candidate_json, veto_json, approved)
                VALUES (1, '2026-05-16', 'paper', 'weekly_etf_rotation_v1', ?, ?, 0)
                """,
                (
                    json.dumps(
                        {
                            "approval_required": True,
                            "risk_status": "pass",
                            "risk_check_id": "risk-stable",
                            "week_end": "2026-05-16",
                            "candidates": [{"symbol": "1306.T", "target_weight": 1.0}],
                        },
                        sort_keys=True,
                    ),
                    json.dumps({"vetoed": False, "risk_status": "pass"}, sort_keys=True),
                ),
            )
            decision_id = con.execute("SELECT last_insert_rowid()").fetchone()[0]
            con.execute(
                """
                INSERT INTO risk_check_result(
                  risk_check_id, snapshot_id, strategy_id, decision_id, status,
                  max_dd_check, weekly_loss_check, concentration_check, volatility_check, event_check, detail_json
                )
                VALUES ('risk-stable', '1', 'weekly_etf_rotation_v1', ?, 'pass', 'pass', 'pass', 'pass', 'pass', 'pass', '{"stable":true}')
                """,
                (str(decision_id),),
            )
            decision_before = self._row_signature(con, "decision_log", "decision_id=?", (decision_id,))
            risk_before = self._row_signature(con, "risk_check_result", "risk_check_id=?", ("risk-stable",))
            con.commit()
            con.close()

            result = run_script(
                "13_llm_report.py",
                "--db",
                str(db_path),
                "--snapshot",
                "1",
                "--decision",
                str(decision_id),
                "--output-dir",
                str(out_dir),
                "--json",
            )
            summary = json.loads(result.stdout)
            self.assertEqual(summary["decision_id"], decision_id)

            con = sqlite3.connect(db_path)
            self.assertEqual(decision_before, self._row_signature(con, "decision_log", "decision_id=?", (decision_id,)))
            self.assertEqual(risk_before, self._row_signature(con, "risk_check_result", "risk_check_id=?", ("risk-stable",)))
            self.assertEqual(con.execute("SELECT COUNT(*) FROM llm_audit_log").fetchone()[0], 1)
            self.assertEqual(
                con.execute("SELECT decision_id FROM llm_audit_log WHERE llm_log_id=?", (summary["llm_log_id"],)).fetchone()[0],
                decision_id,
            )
            con.close()


if __name__ == "__main__":
    unittest.main()
