#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
from datetime import datetime

from rencrow_data import db
from rencrow_data.config import resolve_repo_relative_path
from rencrow_data.snapshot import make_snapshot


def _resolve_snapshot_date(con, snapshot_date: str | None, week_end: str | None) -> str:
    value = week_end or snapshot_date
    if value in (None, "today"):
        return datetime.utcnow().date().isoformat()
    if value == "latest":
        row = con.execute("SELECT MAX(week_end) AS week_end FROM feature_weekly").fetchone()
        if row is None or row["week_end"] is None:
            return datetime.utcnow().date().isoformat()
        return row["week_end"]
    return value


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--db", "--db-path", dest="db", default="rencrow-data/data/rencrow.db")
    parser.add_argument("--output-dir", "--snapshot-dir", dest="output_dir", default="rencrow-data/data/snapshots")
    parser.add_argument("--snapshot-date")
    parser.add_argument("--week-end")
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()
    args.db = str(resolve_repo_relative_path(args.db))
    con = db.connect(args.db)
    db.init_schema(con)
    run = db.start_cli_run(con, "06_make_snapshot.py", args.db)
    snapshot_date = _resolve_snapshot_date(con, args.snapshot_date, args.week_end)
    output_dir = resolve_repo_relative_path(args.output_dir)
    result = make_snapshot(con, args.db, output_dir, snapshot_date)
    summary = {
        "cli_name": "06_make_snapshot.py",
        "db_path": args.db,
        "status": result["status"],
        "target_count": 1,
        "success_count": 1 if result["status"] == "success" else 0,
        "partial_count": 0,
        "fail_count": 0 if result["status"] == "success" else 1,
        "snapshot_date": snapshot_date,
        **result,
    }
    summary = db.finish_cli_run(con, run, summary)
    if args.json:
        print(json.dumps(summary, ensure_ascii=False, sort_keys=True))
    else:
        print(f"snapshot {result['status']} path={result['path']} db_hash={result['db_hash']}")
    con.close()


if __name__ == "__main__":
    main()
