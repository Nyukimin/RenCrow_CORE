#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from rencrow_data import db
from rencrow_data.config import config_hash_for_paths, config_path, load_config, resolve_repo_relative_path
from rencrow_data.features import build_features
from rencrow_data.market import save_market_csv, save_market_item
from rencrow_data.universe import broad_v2, broad_v3, broad_v4, broad_v5, sync_config


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--db", "--db-path", dest="db", default="rencrow-data/data/rencrow.db")
    parser.add_argument("--config-root", default="rencrow-data/config")
    parser.add_argument("--data-root", default="rencrow-data")
    parser.add_argument("--mode", choices=("fixture", "online", "backfill", "hybrid", "incremental"), default="incremental")
    parser.add_argument("--incremental", action="store_true", help="Alias for --mode incremental.")
    parser.add_argument("--start-date", "--start", dest="start_date")
    parser.add_argument("--end-date", "--end", dest="end_date")
    parser.add_argument("--lookback-days", type=int, default=7)
    parser.add_argument("--write-config", action="store_true", default=True)
    parser.add_argument("--no-write-config", dest="write_config", action="store_false")
    parser.add_argument("--no-fetch", dest="fetch", action="store_false", default=True)
    parser.add_argument("--no-features", dest="features", action="store_false", default=True)
    parser.add_argument("--preset", default="broad_v2")
    parser.add_argument("--loop", action="store_true", default=False)
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()
    args.db = str(resolve_repo_relative_path(args.db))
    args.data_root = str(resolve_repo_relative_path(args.data_root))
    if args.incremental:
        args.mode = "incremental"

    config_file = config_path(args.config_root, "instruments.yml")
    preset_map = {
        "broad_v2": broad_v2,
        "broad_v3": broad_v3,
        "broad_v4": broad_v4,
        "broad_v5": broad_v5,
    }
    preset_sequence = {
        "cascade": (broad_v2, broad_v3, broad_v4, broad_v5),
    }

    con = db.connect(args.db)
    db.init_schema(con)
    run = db.start_cli_run(con, "07_sync_universe.py", args.db, config_hash=config_hash_for_paths([config_file]))
    allowed_presets = set(preset_map) | set(preset_sequence)
    if args.preset not in allowed_presets:
        message = f"unknown preset: {args.preset}; allowed={','.join(sorted(allowed_presets))}"
        db.fail_cli_run(con, run, error_message=message, exit_code=4)
        con.close()
        print(f"config error: {message}", file=sys.stderr)
        raise SystemExit(4)
    total_added = 0
    total_upserted = 0
    rounds = 0
    while True:
        rounds += 1
        round_added = 0
        presets = preset_sequence.get(args.preset, (preset_map[args.preset],))
        for preset in presets:
            additions = preset()
            added = 0
            if args.write_config and additions:
                added = sync_config(config_file, additions)
            config = load_config(config_file, default={"instruments": []})
            total_upserted += db.upsert_instruments(con, config.get("instruments", []))
            total_added += added
            round_added += added
        if not args.loop or round_added == 0:
            break

    fetched = 0
    fetched_items = 0
    if args.fetch:
        for item in config.get("instruments", []):
            if item.get("provider") != "yahoo":
                continue
            if args.mode == "fixture":
                if not item.get("fixture"):
                    continue
                rows, status = save_market_csv(con, item, args.data_root)
            else:
                rows, status = save_market_item(
                    con,
                    item,
                    args.data_root,
                    mode=args.mode,
                    start_date=args.start_date,
                    end_date=args.end_date,
                    lookback_days=args.lookback_days,
                )
            fetched += rows
            fetched_items += 1
            if not args.json:
                print(f"{item['symbol']}: {status} rows={rows}")
        feature_rows = build_features(con) if args.features else 0
    else:
        feature_rows = 0

    summary = {
        "cli_name": "07_sync_universe.py",
        "db_path": args.db,
        "status": "success",
        "target_count": total_upserted,
        "success_count": total_upserted,
        "partial_count": 0,
        "fail_count": 0,
        "rounds": rounds,
        "added": total_added,
        "upserted": total_upserted,
        "fetched_items": fetched_items,
        "fetched_rows": fetched,
        "feature_rows": feature_rows,
        "config_hash": config_hash_for_paths([config_file]),
    }
    summary = db.finish_cli_run(con, run, summary)
    if args.json:
        print(json.dumps(summary, ensure_ascii=False, sort_keys=True))
    else:
        print(f"universe_sync rounds={rounds} added={total_added} upserted={total_upserted} fetched_items={fetched_items} fetched_rows={fetched} feature_rows={feature_rows}")
    con.close()


if __name__ == "__main__":
    main()
