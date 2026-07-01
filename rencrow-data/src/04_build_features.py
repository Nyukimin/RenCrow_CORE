#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json

from rencrow_data import db
from rencrow_data.config import resolve_repo_relative_path
from rencrow_data.features import build_features, feature_config_hash
from rencrow_data.timeutil import parse_date


def _resolve_as_of(con, value: str | None):
    if value in (None, ""):
        return None
    if value == "latest":
        row = con.execute("SELECT MAX(trade_date) AS trade_date FROM price_raw").fetchone()
        if row is None or row["trade_date"] is None:
            return None
        return parse_date(row["trade_date"])
    return parse_date(value)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--db", "--db-path", dest="db", default="rencrow-data/data/rencrow.db")
    parser.add_argument("--week-end", help="Accepted for CLI compatibility; feature generation uses available history through the stored data.")
    parser.add_argument("--symbols", action="append", help="Comma-separated symbols. May be repeated.")
    parser.add_argument("--asset-types", action="append", help="Comma-separated asset types. May be repeated.")
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()
    args.db = str(resolve_repo_relative_path(args.db))
    con = db.connect(args.db)
    db.init_schema(con)
    config_hash = feature_config_hash()
    run = db.start_cli_run(con, "04_build_features.py", args.db, config_hash=config_hash)
    as_of = _resolve_as_of(con, args.week_end)
    count = build_features(con, symbols=args.symbols, asset_types=args.asset_types, as_of=as_of)
    summary = db.finish_cli_run(
        con,
        run,
        {
            "cli_name": "04_build_features.py",
            "db_path": args.db,
            "status": "success",
            "target_count": count,
            "success_count": count,
            "partial_count": 0,
            "fail_count": 0,
            "feature_rows": count,
            "feature_config_hash": config_hash,
            "config_hash": config_hash,
            "week_end": args.week_end,
            "as_of": None if as_of is None else as_of.isoformat(),
        },
    )
    if args.json:
        print(json.dumps(summary, ensure_ascii=False, sort_keys=True))
    else:
        print(f"feature build complete rows={count}")
    con.close()


if __name__ == "__main__":
    main()
