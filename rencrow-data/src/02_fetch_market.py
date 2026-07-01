#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
from pathlib import Path

from rencrow_data import db
from rencrow_data.config import config_hash_for_paths, config_path, load_config, resolve_repo_relative_path
from rencrow_data.market import save_market_csv, save_market_item

ALLOWED_ASSET_TYPES = ("ETF", "STOCK", "CASH_PROXY", "CRYPTO", "INDEX")


def _split_filters(values: list[str] | None) -> set[str]:
    if not values:
        return set()
    result: set[str] = set()
    for value in values:
        result.update(part.strip() for part in value.split(",") if part.strip())
    return result


def _resolve_fixture_path(value: str, data_root: str) -> Path:
    path = Path(value)
    if path.is_absolute():
        return path
    candidates = [Path.cwd() / path, Path(data_root) / path]
    data_root_path = Path(data_root)
    if data_root_path.parts[:1] == ("rencrow-data",):
        candidates.append(Path(*data_root_path.parts[1:]) / path)
    for candidate in candidates:
        if candidate.exists():
            return candidate.resolve()
    return candidates[0].resolve()


def _resolve_data_root(value: str) -> str:
    return str(resolve_repo_relative_path(value))


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--db", "--db-path", dest="db", default="rencrow-data/data/rencrow.db")
    parser.add_argument("--config-root", default="rencrow-data/config")
    parser.add_argument("--data-root", default="rencrow-data")
    parser.add_argument("--mode", choices=("fixture", "online", "backfill", "hybrid", "incremental"), default="fixture")
    parser.add_argument("--fixture", help="CSV fixture path to use for every selected instrument.")
    parser.add_argument("--symbols", action="append", help="Comma-separated symbols. May be repeated.")
    parser.add_argument("--asset-types", action="append", help="Comma-separated asset types. May be repeated.")
    parser.add_argument("--provider", help="Limit fetches to instruments using this provider.")
    parser.add_argument("--incremental", action="store_true", help="Alias for --mode incremental.")
    parser.add_argument("--start-date", "--start", dest="start_date")
    parser.add_argument("--end-date", "--end", dest="end_date")
    parser.add_argument("--lookback-days", type=int, default=7)
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()
    args.db = str(resolve_repo_relative_path(args.db))
    args.data_root = _resolve_data_root(args.data_root)
    if args.fixture:
        args.mode = "fixture"
        fixture_path = _resolve_fixture_path(args.fixture, args.data_root)
    elif args.incremental:
        args.mode = "incremental"
        fixture_path = None
    else:
        fixture_path = None

    con = db.connect(args.db)
    db.init_schema(con)
    instruments_path = config_path(args.config_root, "instruments.yml")
    config_hash = config_hash_for_paths([instruments_path])
    run = db.start_cli_run(con, "02_fetch_market.py", args.db, config_hash=config_hash)
    config = load_config(instruments_path, default={"instruments": []})
    symbol_filter = _split_filters(args.symbols)
    asset_type_filter = _split_filters(args.asset_types)
    target_count = 0
    success_count = 0
    partial_count = 0
    total = 0
    failures = 0
    items = []
    for item in config.get("instruments", []):
        if item.get("asset_type") not in ALLOWED_ASSET_TYPES:
            continue
        if symbol_filter and item.get("symbol") not in symbol_filter:
            continue
        if asset_type_filter and item.get("asset_type") not in asset_type_filter:
            continue
        if args.provider and item.get("provider") != args.provider:
            continue
        target_count += 1
        if fixture_path is not None:
            item = dict(item)
            item["fixture"] = str(fixture_path)
            item.setdefault("source_name", "csv_market")
        if args.mode == "fixture":
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
        total += rows
        if status == "success":
            success_count += 1
        elif status == "partial":
            partial_count += 1
            failures += 1
        else:
            failures += 1
        items.append({"symbol": item.get("symbol"), "status": status, "rows": rows})
        if not args.json:
            print(f"{item['symbol']}: {status} rows={rows}")
    code = 0
    if failures and success_count == 0 and target_count > 0:
        code = 1
    elif failures:
        code = 2
    summary = {
        "cli_name": "02_fetch_market.py",
        "db_path": args.db,
        "mode": args.mode,
        "status": "success" if code == 0 else ("fail" if code == 1 else "partial"),
        "target_count": target_count,
        "success_count": success_count,
        "partial_count": partial_count,
        "fail_count": failures - partial_count,
        "rows_fetched": total,
        "items": items,
        "config_hash": config_hash,
    }
    summary = db.finish_cli_run(con, run, summary)
    if args.json:
        print(json.dumps(summary, ensure_ascii=False, sort_keys=True))
    elif failures:
        print(f"market ingest complete rows={total} partial_failures={failures}")
    else:
        print(f"market ingest complete rows={total}")
    con.close()
    raise SystemExit(code)


if __name__ == "__main__":
    main()
