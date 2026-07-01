#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json

from rencrow_data import db
from rencrow_data.config import resolve_repo_relative_path
from rencrow_data.events import detect_events, event_state_summary
from rencrow_data.timeutil import parse_date


def _resolve_week_end(con, value: str | None):
    if value in (None, ""):
        return None
    if value == "latest":
        row = con.execute("SELECT MAX(week_end) AS week_end FROM feature_weekly").fetchone()
        if row is None or row["week_end"] is None:
            return None
        return parse_date(row["week_end"])
    return parse_date(value)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--db", "--db-path", dest="db", default="rencrow-data/data/rencrow.db")
    parser.add_argument("--week-end", help="Accepted for CLI compatibility; current detector scans stored weekly features.")
    parser.add_argument("--lookback-days", type=int, default=7, help="Calendar event lookback window around each week_end.")
    parser.add_argument("--lookahead-days", type=int, default=7, help="Calendar event lookahead window around each week_end.")
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()
    args.db = str(resolve_repo_relative_path(args.db))
    con = db.connect(args.db)
    db.init_schema(con)
    run = db.start_cli_run(con, "05_detect_events.py", args.db)
    resolved_week_end = _resolve_week_end(con, args.week_end)
    count = detect_events(con, lookback_days=args.lookback_days, lookahead_days=args.lookahead_days, week_end=resolved_week_end)
    event_state = event_state_summary(con, resolved_week_end)
    summary = db.finish_cli_run(
        con,
        run,
        {
            "cli_name": "05_detect_events.py",
            "db_path": args.db,
            "status": "success",
            "target_count": count,
            "success_count": count,
            "partial_count": 0,
            "fail_count": 0,
            "event_rows": count,
            "event_state": event_state,
            "week_end": args.week_end,
            "as_of": None if resolved_week_end is None else resolved_week_end.isoformat(),
        },
    )
    if args.json:
        print(json.dumps(summary, ensure_ascii=False, sort_keys=True))
    else:
        print(f"event detection complete rows={count}")
    con.close()


if __name__ == "__main__":
    main()
