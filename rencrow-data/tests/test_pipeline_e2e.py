from __future__ import annotations

import gzip
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


def run_script(script: str, *args: str) -> subprocess.CompletedProcess[str]:
    cmd = [sys.executable, str(SRC / script), *args]
    return subprocess.run(cmd, cwd=REPO, text=True, capture_output=True, check=True, env={"PYTHONPATH": str(SRC)})


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
                        "first_date": "2001-01-01",
                        "source_name": "csv_market",
                        "fixture": "fixtures/prices.csv",
                    },
                    {
                        "symbol": "USDJPY_BOJ",
                        "name": "BOJ USDJPY",
                        "asset_type": "FX",
                        "venue": "BOJ",
                        "currency": "JPY",
                        "timezone": "Asia/Tokyo",
                        "active": 1,
                        "first_date": "2000-01-01",
                    },
                    {
                        "symbol": "DGS10",
                        "name": "US 10Y",
                        "asset_type": "RATE",
                        "venue": "FRED",
                        "currency": "USD",
                        "timezone": "UTC",
                        "active": 1,
                        "first_date": "2000-01-01",
                    },
                ]
            }
        ),
        encoding="utf-8",
    )
    (config / "sources.yml").write_text(json.dumps({"macro_sources": [{"source_name": "csv_macro", "fixture": "fixtures/macro.csv"}]}), encoding="utf-8")
    (config / "calendars.yml").write_text(json.dumps({"calendar_sources": [{"source_name": "csv_calendar", "fixture": "fixtures/calendar.csv"}]}), encoding="utf-8")
    return data_root, config, data_root / "data" / "rencrow.db"


class PipelineE2ETest(unittest.TestCase):
    def test_offline_pipeline_e2e_creates_features_events_and_snapshot(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            data_root, config_root, db_path = write_fixture_tree(tmp_path)
            run_script("01_init_db.py", "--db-path", str(db_path), "--config-root", str(config_root))
            run_script("02_fetch_market.py", "--db", str(db_path), "--config-root", str(config_root), "--data-root", str(data_root))
            run_script("03_fetch_macro.py", "--db", str(db_path), "--config-root", str(config_root), "--data-root", str(data_root))
            run_script("04_build_features.py", "--db-path", str(db_path), "--week-end", "latest")
            run_script("05_detect_events.py", "--db-path", str(db_path), "--week-end", "2026-05-01")
            run_script(
                "08_validate_data.py",
                "--db",
                str(db_path),
                "--as-of",
                "2026-05-15",
                "--min-history-days",
                "140",
                "--max-missing-rate",
                "0.90",
            )
            out_dir = data_root / "data" / "snapshots"
            run_script("06_make_snapshot.py", "--db-path", str(db_path), "--output-dir", str(out_dir), "--snapshot-date", "2026-05-16")

            con = sqlite3.connect(db_path)
            con.row_factory = sqlite3.Row
            fixture_price_rows = len((ROOT / "fixtures" / "1306T_prices.csv").read_text(encoding="utf-8").strip().splitlines()) - 1
            self.assertEqual(con.execute("SELECT COUNT(*) FROM instruments").fetchone()[0], 3)
            self.assertEqual(con.execute("SELECT COUNT(*) FROM price_raw").fetchone()[0], fixture_price_rows)
            self.assertGreaterEqual(con.execute("SELECT COUNT(*) FROM macro_series").fetchone()[0], 20)
            self.assertGreater(con.execute("SELECT COUNT(*) FROM data_quality_check").fetchone()[0], 0)
            self.assertGreater(con.execute("SELECT COUNT(*) FROM feature_weekly WHERE ret_12w IS NOT NULL").fetchone()[0], 0)
            self.assertGreater(con.execute("SELECT COUNT(*) FROM event_log WHERE reason LIKE 'calendar_%'").fetchone()[0], 0)
            self.assertEqual(con.execute("SELECT MAX(event_risk_score) FROM feature_weekly").fetchone()[0], 0.7)
            snap = con.execute("SELECT * FROM snapshot_registry WHERE snapshot_date='2026-05-16'").fetchone()
            self.assertIsNotNone(snap)
            self.assertEqual(snap["status"], "success")
            self.assertTrue(snap["db_hash"])
            self.assertTrue(snap["features_hash"])
            self.assertGreater(
                con.execute("SELECT COUNT(*) FROM feature_weekly WHERE source_snapshot_id=?", (snap["snapshot_id"],)).fetchone()[0],
                0,
            )
            self.assertGreater(
                con.execute("SELECT COUNT(*) FROM event_log WHERE snapshot_id=? AND reason LIKE 'calendar_%'", (snap["snapshot_id"],)).fetchone()[0],
                0,
            )
            source_summary = json.loads(snap["source_summary_json"])
            self.assertEqual(source_summary["as_of"], "2026-05-16")
            self.assertIn("counts", source_summary)
            self.assertIn("latest_fetches", source_summary)
            latest_by_source = {row["source_name"]: row for row in source_summary["latest_fetches"]}
            self.assertEqual(latest_by_source["csv_market"]["status"], "success")
            self.assertTrue(latest_by_source["csv_market"]["endpoint"].startswith("csv:"))
            self.assertTrue(latest_by_source["csv_market"]["requested_at"])
            self.assertTrue(latest_by_source["csv_market"]["finished_at"])
            self.assertGreater(latest_by_source["csv_market"]["rows_fetched"], 0)
            self.assertTrue(latest_by_source["csv_market"]["checksum"])
            self.assertIn("internal_research_only", latest_by_source["csv_market"]["usage_terms"])
            self.assertEqual(
                snap["missing_rate"],
                con.execute(
                    """
                    SELECT MAX(metric_value)
                      FROM data_quality_check
                     WHERE check_type='missing'
                       AND check_date='2026-05-15'
                    """
                ).fetchone()[0],
            )
            event_state = json.loads(snap["event_state_json"])
            self.assertEqual(event_state["as_of"], "2026-05-16")
            self.assertGreater(event_state["open_event_count"], 0)
            self.assertGreaterEqual(event_state["max_open_event_risk_score"], 0.7)
            snapshot_path = Path(snap["snapshot_path"])
            self.assertTrue(snapshot_path.exists())
            with gzip.open(snapshot_path, "rb") as f:
                self.assertTrue(f.read(16).startswith(b"SQLite format 3"))
            snapshot_db = tmp_path / "snapshot.sqlite"
            with gzip.open(snapshot_path, "rb") as f_in, snapshot_db.open("wb") as f_out:
                f_out.write(f_in.read())
            snap_con = sqlite3.connect(snapshot_db)
            self.assertGreater(
                snap_con.execute("SELECT COUNT(*) FROM feature_weekly WHERE source_snapshot_id=?", (snap["snapshot_id"],)).fetchone()[0],
                0,
            )
            self.assertGreater(
                snap_con.execute("SELECT COUNT(*) FROM event_log WHERE snapshot_id=? AND reason LIKE 'calendar_%'", (snap["snapshot_id"],)).fetchone()[0],
                0,
            )
            snap_con.close()

    def test_snapshot_records_latest_quality_missing_rate_without_future_checks(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            data_root, config_root, db_path = write_fixture_tree(tmp_path)
            run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(config_root))
            con = sqlite3.connect(db_path)
            con.execute(
                """
                INSERT INTO data_quality_check(run_id, instrument_id, check_date, check_type, severity, status, metric_value)
                VALUES ('quality-old', NULL, '2026-05-01', 'missing', 'warning', 'fail', 0.30)
                """
            )
            con.execute(
                """
                INSERT INTO data_quality_check(run_id, instrument_id, check_date, check_type, severity, status, metric_value)
                VALUES ('quality-current', NULL, '2026-05-15', 'missing', 'warning', 'fail', 0.42)
                """
            )
            con.execute(
                """
                INSERT INTO data_quality_check(run_id, instrument_id, check_date, check_type, severity, status, metric_value)
                VALUES ('quality-future', NULL, '2026-05-22', 'missing', 'warning', 'fail', 0.99)
                """
            )
            con.commit()
            con.close()

            run_script("06_make_snapshot.py", "--db", str(db_path), "--output-dir", str(data_root / "data" / "snapshots"), "--snapshot-date", "2026-05-16")

            con = sqlite3.connect(db_path)
            missing_rate = con.execute("SELECT missing_rate FROM snapshot_registry WHERE snapshot_date='2026-05-16'").fetchone()[0]
            con.close()
            self.assertEqual(missing_rate, 0.42)

    def test_snapshot_metadata_ignores_future_fetches_features_and_events(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            data_root, config_root, db_path = write_fixture_tree(tmp_path)
            run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(config_root))
            con = sqlite3.connect(db_path)
            con.execute(
                """
                INSERT INTO source_fetch_log(source_name, endpoint, requested_at, status, rows_fetched)
                VALUES ('csv_market', 'before', '2026-05-15T10:00:00Z', 'success', 10)
                """
            )
            con.execute(
                """
                INSERT INTO source_fetch_log(source_name, endpoint, requested_at, status, error_message)
                VALUES ('csv_market', 'future', '2026-05-22T10:00:00Z', 'fail', 'future failure')
                """
            )
            con.execute(
                """
                INSERT INTO feature_weekly(instrument_id, week_end, ret_1w, event_risk_score)
                VALUES (1, '2026-05-22', 0.01, 1.0)
                """
            )
            con.execute(
                """
                INSERT INTO event_log(event_ts, scope, level, reason, event_risk_score, context_json)
                VALUES ('2026-05-15T00:00:00Z', 'macro', 'warn', 'calendar_cpi', 0.7, ?)
                """,
                (json.dumps({"week_end": "2026-05-15"}),),
            )
            con.execute(
                """
                INSERT INTO event_log(event_ts, scope, level, reason, event_risk_score, context_json)
                VALUES ('2026-05-22T00:00:00Z', 'macro', 'stop', 'calendar_fomc', 1.0, ?)
                """,
                (json.dumps({"week_end": "2026-05-22"}),),
            )
            con.commit()
            con.close()

            run_script("06_make_snapshot.py", "--db", str(db_path), "--output-dir", str(data_root / "data" / "snapshots"), "--snapshot-date", "2026-05-16")

            con = sqlite3.connect(db_path)
            con.row_factory = sqlite3.Row
            snap = con.execute("SELECT * FROM snapshot_registry WHERE snapshot_date='2026-05-16'").fetchone()
            self.assertEqual(snap["status"], "success")
            self.assertNotIn("high_risk_features=1", snap["notes"])
            source_summary = json.loads(snap["source_summary_json"])
            latest_by_source = {row["source_name"]: row for row in source_summary["latest_fetches"]}
            self.assertEqual(latest_by_source["csv_market"]["status"], "success")
            self.assertEqual(latest_by_source["csv_market"]["endpoint"], "before")
            event_state = json.loads(snap["event_state_json"])
            self.assertEqual(event_state["open_event_count"], 1)
            self.assertEqual(event_state["max_open_event_risk_score"], 0.7)
            self.assertEqual(event_state["latest_open_events"][0]["reason"], "calendar_cpi")
            self.assertEqual(
                con.execute("SELECT COUNT(*) FROM event_log WHERE snapshot_id=? AND reason='calendar_fomc'", (snap["snapshot_id"],)).fetchone()[0],
                0,
            )
            con.close()


    def test_snapshot_is_blocked_when_latest_fetch_failed_for_db_observed_source(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp_path = Path(td)
            data_root, config_root, db_path = write_fixture_tree(tmp_path)
            run_script("01_init_db.py", "--db", str(db_path), "--config-root", str(config_root))
            con = sqlite3.connect(db_path)
            con.execute(
                """
                INSERT INTO source_fetch_log(source_name, endpoint, requested_at, status, error_message)
                VALUES ('custom_research_source', 'missing', '2026-05-15T10:00:00Z', 'fail', 'boom')
                """
            )
            con.commit()
            con.close()
            run_script("06_make_snapshot.py", "--db", str(db_path), "--output-dir", str(data_root / "data" / "snapshots"), "--snapshot-date", "2026-05-16")
            con = sqlite3.connect(db_path)
            status, notes = con.execute("SELECT status, notes FROM snapshot_registry WHERE snapshot_date='2026-05-16'").fetchone()
            self.assertEqual(status, "blocked")
            self.assertIn("bad_fetch=1", notes)


if __name__ == "__main__":
    unittest.main()
