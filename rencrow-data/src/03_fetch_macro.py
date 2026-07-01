#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
from pathlib import Path

from rencrow_data import db
from rencrow_data.config import config_hash_for_paths, config_path, load_config, resolve_repo_relative_path
from rencrow_data.macro import ingest_calendar_csv, ingest_macro_csv, ingest_macro_source


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
    parser.add_argument("--fixture", help="CSV fixture path to use for every selected macro source.")
    parser.add_argument("--series", action="append", help="Comma-separated series/source names. May be repeated.")
    parser.add_argument("--provider", help="Limit macro source fetches to this provider.")
    parser.add_argument("--incremental", action="store_true", help="Alias for --mode incremental.")
    parser.add_argument("--start-date", "--start", dest="start_date")
    parser.add_argument("--end-date", "--end", dest="end_date")
    parser.add_argument("--lookback-days", type=int, default=30)
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
    sources_path = config_path(args.config_root, "sources.yml")
    calendars_path = config_path(args.config_root, "calendars.yml")
    config_hash = config_hash_for_paths([sources_path, calendars_path])
    run = db.start_cli_run(con, "03_fetch_macro.py", args.db, config_hash=config_hash)
    sources = load_config(sources_path, default={"macro_sources": []})
    calendars = load_config(calendars_path, default={"calendar_sources": []})
    series_filter = _split_filters(args.series)
    failures = 0
    total_sources = 0
    success_count = 0
    partial_count = 0
    total_rows = 0
    items = []
    for source in sources.get("macro_sources", []):
        source_keys = {source.get("series_code"), source.get("symbol"), source.get("source_name")}
        if series_filter and not (series_filter & {str(key) for key in source_keys if key}):
            continue
        if args.provider and source.get("provider") != args.provider:
            continue
        total_sources += 1
        if fixture_path is not None:
            source = dict(source)
            source["fixture"] = str(fixture_path)
            source.setdefault("source_name", "csv_macro")
        if args.mode == "fixture" or not source.get("provider"):
            rows, status = ingest_macro_csv(con, source, args.data_root)
        else:
            rows, status = ingest_macro_source(
                con,
                source,
                args.data_root,
                mode=args.mode,
                start_date=args.start_date,
                end_date=args.end_date,
                lookback_days=args.lookback_days,
            )
        total_rows += rows
        if status == "success":
            success_count += 1
        elif status == "partial":
            partial_count += 1
            failures += 1
        else:
            failures += 1
        items.append({"source_name": source.get("source_name"), "status": status, "rows": rows, "kind": "macro"})
        if not args.json:
            print(f"macro {source.get('source_name')}: {status} rows={rows}")
    for source in calendars.get("calendar_sources", []):
        source_keys = {source.get("category"), source.get("event_name"), source.get("source_name")}
        if series_filter and not (series_filter & {str(key) for key in source_keys if key}):
            continue
        total_sources += 1
        rows, status = ingest_calendar_csv(con, source, args.data_root)
        total_rows += rows
        if status == "success":
            success_count += 1
        elif status == "partial":
            partial_count += 1
            failures += 1
        else:
            failures += 1
        items.append({"source_name": source.get("source_name"), "status": status, "rows": rows, "kind": "calendar"})
        if not args.json:
            print(f"calendar {source.get('source_name')}: {status} rows={rows}")
    code = 0
    if failures and success_count == 0 and total_sources > 0:
        code = 1
    elif failures:
        code = 2
    summary = {
        "cli_name": "03_fetch_macro.py",
        "db_path": args.db,
        "mode": args.mode,
        "status": "success" if code == 0 else ("fail" if code == 1 else "partial"),
        "target_count": total_sources,
        "success_count": success_count,
        "partial_count": partial_count,
        "fail_count": failures - partial_count,
        "rows_fetched": total_rows,
        "items": items,
        "config_hash": config_hash,
    }
    summary = db.finish_cli_run(con, run, summary)
    if args.json:
        print(json.dumps(summary, ensure_ascii=False, sort_keys=True))
    elif failures:
        print(f"macro ingest complete partial_failures={failures}")
    con.close()
    raise SystemExit(code)


if __name__ == "__main__":
    main()
