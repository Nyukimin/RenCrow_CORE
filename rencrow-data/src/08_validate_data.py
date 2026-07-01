#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import sys
from datetime import date

from rencrow_data import db
from rencrow_data.config import resolve_repo_relative_path
from rencrow_data.quality import QualityOptions, exit_code, parse_csv_values, validate_data


def _resolve_as_of(con, value: str) -> date:
    if value == "today":
        return date.today()
    if value == "latest":
        row = con.execute("SELECT MAX(trade_date) AS latest FROM price_raw").fetchone()
        if not row or row["latest"] is None:
            raise ValueError("cannot resolve --as-of latest because price_raw is empty")
        return date.fromisoformat(row["latest"])
    return date.fromisoformat(value)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--db", "--db-path", dest="db_path", default="rencrow-data/data/rencrow.db")
    parser.add_argument("--as-of", default="today")
    parser.add_argument("--symbols", action="append", help="Comma-separated symbols. May be repeated.")
    parser.add_argument("--asset-types", action="append", help="Comma-separated asset types. May be repeated.")
    parser.add_argument("--min-history-days", type=int, default=260)
    parser.add_argument("--max-missing-rate", type=float, default=0.35)
    parser.add_argument("--stale-days", type=int, default=7)
    parser.add_argument("--outlier-return-abs", type=float, default=0.45)
    parser.add_argument("--volume-outlier-ratio", type=float, default=10.0)
    parser.add_argument("--adjustment-ratio-jump", type=float, default=0.25)
    parser.add_argument("--fetch-lookback-days", type=int, default=7)
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()
    args.db_path = str(resolve_repo_relative_path(args.db_path))

    con = db.connect(args.db_path)
    db.init_schema(con)
    run = db.start_cli_run(con, "08_validate_data.py", args.db_path)
    try:
        as_of = _resolve_as_of(con, args.as_of)
        options = QualityOptions(
            as_of=as_of,
            min_history_days=args.min_history_days,
            max_missing_rate=args.max_missing_rate,
            stale_days=args.stale_days,
            outlier_return_abs=args.outlier_return_abs,
            volume_outlier_ratio=args.volume_outlier_ratio,
            adjustment_ratio_jump=args.adjustment_ratio_jump,
            fetch_lookback_days=args.fetch_lookback_days,
            symbols=parse_csv_values(args.symbols),
            asset_types=parse_csv_values(args.asset_types),
        )
        summary = validate_data(con, options)
        code = exit_code(summary)
        status = "fail" if code == 3 else ("partial" if code == 2 else "success")
        summary.update(
            {
                "cli_name": "08_validate_data.py",
                "db_path": args.db_path,
                "status": status,
                "exit_code": code,
                "target_count": summary["instrument_count"],
                "success_count": summary["instrument_count"] if status == "success" else 0,
                "partial_count": summary["partials"],
                "fail_count": summary["failures"],
            }
        )
        summary = db.finish_cli_run(con, run, summary)
    except ValueError as exc:
        db.fail_cli_run(con, run, error_message=str(exc), exit_code=4)
        raise
    finally:
        con.close()

    code = int(summary["exit_code"])
    if args.json:
        print(json.dumps(summary, ensure_ascii=False, sort_keys=True))
    else:
        print(
            "data validation "
            f"run_id={summary['run_id']} date={summary['check_date']} instruments={summary['instrument_count']} "
            f"checks={summary['total_checks']} blockers={summary['blockers']} warnings={summary['warnings']} "
            f"partials={summary['partials']} failures={summary['failures']}"
        )
    raise SystemExit(code)


if __name__ == "__main__":
    try:
        main()
    except ValueError as exc:
        print(f"config error: {exc}", file=sys.stderr)
        raise SystemExit(4)
