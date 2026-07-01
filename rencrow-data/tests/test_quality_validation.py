from __future__ import annotations

import json
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from datetime import date
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
REPO = ROOT.parents[0]
SRC = ROOT / "src"


def run_script(script: str, *args: str, check: bool = True) -> subprocess.CompletedProcess[str]:
    cmd = [sys.executable, str(SRC / script), *args]
    return subprocess.run(cmd, cwd=REPO, text=True, capture_output=True, check=check, env={"PYTHONPATH": str(SRC)})


def write_config(tmp_path: Path, *, fixture_name: str = "prices.csv") -> tuple[Path, Path, Path]:
    data_root = tmp_path / "rencrow-data"
    config = data_root / "config"
    fixtures = data_root / "fixtures"
    config.mkdir(parents=True)
    fixtures.mkdir(parents=True)
    (fixtures / fixture_name).write_text((ROOT / "fixtures" / "1306T_prices.csv").read_text(encoding="utf-8"), encoding="utf-8")
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
                        "fixture": f"fixtures/{fixture_name}",
                    }
                ]
            }
        ),
        encoding="utf-8",
    )
    return data_root, config, data_root / "data" / "rencrow.db"


class QualityValidationTest(unittest.TestCase):
    def test_validate_data_records_pass_checks(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            data_root, config_root, db_path = write_config(tmp_path)
            run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(config_root))
            run_script("02_fetch_market.py", "--db", str(db_path), "--config-root", str(config_root), "--data-root", str(data_root))
            result = run_script(
                "08_validate_data.py",
                "--db",
                str(db_path),
                "--as-of",
                "2026-05-15",
                "--min-history-days",
                "140",
                "--max-missing-rate",
                "0.90",
                "--json",
            )
            summary = json.loads(result.stdout)
            self.assertEqual(summary["blockers"], 0)
            self.assertEqual(summary["instrument_count"], 1)
            self.assertGreaterEqual(summary["total_checks"], 6)

            con = sqlite3.connect(db_path)
            statuses = {
                row[0]: row[1]
                for row in con.execute(
                    "SELECT check_type, status FROM data_quality_check WHERE run_id=?",
                    (summary["run_id"],),
                ).fetchall()
            }
            self.assertEqual(statuses["stale"], "pass")
            self.assertEqual(statuses["missing"], "pass")
            self.assertEqual(statuses["return_outlier"], "pass")
            self.assertEqual(statuses["volume_outlier"], "pass")
            self.assertEqual(statuses["adjustment_anomaly"], "pass")
            self.assertEqual(statuses["fetch_status"], "pass")

    def test_validate_data_exits_3_for_blockers(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            data_root, config_root, db_path = write_config(tmp_path)
            run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(config_root))
            run_script("02_fetch_market.py", "--db", str(db_path), "--config-root", str(config_root), "--data-root", str(data_root))
            result = run_script(
                "08_validate_data.py",
                "--db",
                str(db_path),
                "--as-of",
                "2026-06-30",
                "--min-history-days",
                "140",
                "--max-missing-rate",
                "0.90",
                "--json",
                check=False,
            )
            self.assertEqual(result.returncode, 3)
            summary = json.loads(result.stdout)
            self.assertGreater(summary["blockers"], 0)

            con = sqlite3.connect(db_path)
            cli_row = con.execute(
                "SELECT status, fail_count, finished_at FROM cli_run_log WHERE run_id=?",
                (summary["cli_run_id"],),
            ).fetchone()
            self.assertEqual(cli_row[0], "fail")
            self.assertGreater(cli_row[1], 0)
            self.assertTrue(cli_row[2])
            stale = con.execute(
                """
                SELECT severity, status, metric_value
                  FROM data_quality_check
                 WHERE run_id=? AND check_type='stale'
                """,
                (summary["run_id"],),
            ).fetchone()
            self.assertEqual(stale[0], "blocker")
            self.assertEqual(stale[1], "fail")
            self.assertGreater(stale[2], 7)

    def test_fetch_failure_is_blocker(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            data_root, config_root, db_path = write_config(tmp_path)
            run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(config_root))
            run_script("02_fetch_market.py", "--db", str(db_path), "--config-root", str(config_root), "--data-root", str(data_root))
            con = sqlite3.connect(db_path)
            con.execute(
                """
                INSERT INTO source_fetch_log(source_name, endpoint, requested_at, status, error_message)
                VALUES ('csv_market', 'missing', '2026-05-14T00:00:00+00:00', 'fail', 'boom')
                """
            )
            con.commit()
            con.close()

            result = run_script(
                "08_validate_data.py",
                "--db",
                str(db_path),
                "--as-of",
                "2026-05-15",
                "--min-history-days",
                "140",
                "--max-missing-rate",
                "0.90",
                "--json",
                check=False,
            )
            self.assertEqual(result.returncode, 3)
            summary = json.loads(result.stdout)
            self.assertGreater(summary["blockers"], 0)
            con = sqlite3.connect(db_path)
            fetch_fail = con.execute(
                "SELECT severity, status FROM data_quality_check WHERE run_id=? AND check_type='fetch_fail'",
                (summary["run_id"],),
            ).fetchone()
            self.assertEqual(tuple(fetch_fail), ("blocker", "fail"))

    def test_return_outlier_is_blocker_for_etf_candidate(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            data_root, config_root, db_path = write_config(tmp_path, fixture_name="outlier_prices.csv")
            fixture = data_root / "fixtures" / "outlier_prices.csv"
            fixture.write_text(
                "\n".join(
                    [
                        "date,open,high,low,close,adj_close,volume,dividend,split",
                        "2026-05-13,100,101,99,100,100,1000,0,1",
                        "2026-05-14,190,191,189,190,190,1000,0,1",
                        "2026-05-15,191,192,190,191,191,1000,0,1",
                    ]
                )
                + "\n",
                encoding="utf-8",
            )
            run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(config_root))
            run_script("02_fetch_market.py", "--db", str(db_path), "--config-root", str(config_root), "--data-root", str(data_root))

            result = run_script(
                "08_validate_data.py",
                "--db",
                str(db_path),
                "--as-of",
                "2026-05-15",
                "--min-history-days",
                "3",
                "--max-missing-rate",
                "1.0",
                "--json",
                check=False,
            )

            self.assertEqual(result.returncode, 3)
            summary = json.loads(result.stdout)
            con = sqlite3.connect(db_path)
            row = con.execute(
                """
                SELECT severity, status, metric_value, detail_json
                  FROM data_quality_check
                 WHERE run_id=? AND check_type='return_outlier'
                """,
                (summary["run_id"],),
            ).fetchone()
            con.close()
            self.assertEqual(row[0], "blocker")
            self.assertEqual(row[1], "fail")
            self.assertAlmostEqual(row[2], 0.9)
            self.assertIn("2026-05-14", row[3])

    def test_volume_outlier_is_blocker_for_etf_candidate(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            data_root, config_root, db_path = write_config(tmp_path, fixture_name="volume_prices.csv")
            fixture = data_root / "fixtures" / "volume_prices.csv"
            fixture.write_text(
                "\n".join(
                    [
                        "date,open,high,low,close,adj_close,volume,dividend,split",
                        "2026-05-12,100,101,99,100,100,1000,0,1",
                        "2026-05-13,100,101,99,100,100,1000,0,1",
                        "2026-05-14,100,101,99,100,100,25000,0,1",
                        "2026-05-15,100,101,99,100,100,1000,0,1",
                    ]
                )
                + "\n",
                encoding="utf-8",
            )
            run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(config_root))
            run_script("02_fetch_market.py", "--db", str(db_path), "--config-root", str(config_root), "--data-root", str(data_root))

            result = run_script(
                "08_validate_data.py",
                "--db",
                str(db_path),
                "--as-of",
                "2026-05-15",
                "--min-history-days",
                "4",
                "--max-missing-rate",
                "1.0",
                "--volume-outlier-ratio",
                "10",
                "--json",
                check=False,
            )

            self.assertEqual(result.returncode, 3)
            summary = json.loads(result.stdout)
            con = sqlite3.connect(db_path)
            row = con.execute(
                """
                SELECT severity, status, metric_value, detail_json
                  FROM data_quality_check
                 WHERE run_id=? AND check_type='volume_outlier'
                """,
                (summary["run_id"],),
            ).fetchone()
            con.close()
            self.assertEqual(row[0], "blocker")
            self.assertEqual(row[1], "fail")
            self.assertAlmostEqual(row[2], 25.0)
            self.assertIn("2026-05-14", row[3])
            self.assertIn('"baseline_volume":1000.0', row[3])

    def test_adjustment_anomaly_is_blocker_without_corporate_action(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            data_root, config_root, db_path = write_config(tmp_path, fixture_name="adjustment_prices.csv")
            fixture = data_root / "fixtures" / "adjustment_prices.csv"
            fixture.write_text(
                "\n".join(
                    [
                        "date,open,high,low,close,adj_close,volume,dividend,split",
                        "2026-05-13,100,101,99,100,100,1000,0,1",
                        "2026-05-14,101,102,100,101,50.5,1000,0,1",
                        "2026-05-15,102,103,101,102,51,1000,0,1",
                    ]
                )
                + "\n",
                encoding="utf-8",
            )
            run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(config_root))
            run_script("02_fetch_market.py", "--db", str(db_path), "--config-root", str(config_root), "--data-root", str(data_root))

            result = run_script(
                "08_validate_data.py",
                "--db",
                str(db_path),
                "--as-of",
                "2026-05-15",
                "--min-history-days",
                "3",
                "--max-missing-rate",
                "1.0",
                "--json",
                check=False,
            )

            self.assertEqual(result.returncode, 3)
            summary = json.loads(result.stdout)
            con = sqlite3.connect(db_path)
            row = con.execute(
                """
                SELECT severity, status, metric_value, detail_json
                  FROM data_quality_check
                 WHERE run_id=? AND check_type='adjustment_anomaly'
                """,
                (summary["run_id"],),
            ).fetchone()
            con.close()
            self.assertEqual(row[0], "blocker")
            self.assertEqual(row[1], "fail")
            self.assertAlmostEqual(row[2], -0.5)
            self.assertIn('"corporate_action_found":false', row[3])


if __name__ == "__main__":
    unittest.main()
