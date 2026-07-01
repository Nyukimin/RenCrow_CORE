#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
from pathlib import Path
import sys

from rencrow_data import db
from rencrow_data.config import config_hash_for_paths, config_path, load_config, resolve_repo_relative_path


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--db", "--db-path", dest="db", default="rencrow-data/data/rencrow.db")
    parser.add_argument("--config-root", "--config-dir", dest="config_root", default="rencrow-data/config")
    parser.add_argument("--json", action="store_true")
    parser.add_argument("--reset", action="store_true", help="Refuse destructive reset in normal CLI operation.")
    args = parser.parse_args()
    args.db = str(resolve_repo_relative_path(args.db))
    con = db.connect(args.db)
    db.init_schema(con)
    instruments_path = config_path(args.config_root, "instruments.yml")
    config_hash = config_hash_for_paths([instruments_path])
    run = db.start_cli_run(con, "01_init_db.py", args.db, config_hash=config_hash)
    if args.reset:
        message = "--reset is disabled for normal investment research operation"
        db.fail_cli_run(con, run, error_message=message, exit_code=4)
        print(message, file=sys.stderr)
        con.close()
        raise SystemExit(4)
    instruments = load_config(instruments_path, default={"instruments": []})
    count = db.upsert_instruments(con, instruments.get("instruments", []))
    db.log_event(con, "system", "info", "schema_initialized", value=count, event_risk_score=0.0)
    summary = {
        "cli_name": "01_init_db.py",
        "db_path": str(Path(args.db)),
        "status": "success",
        "target_count": len(instruments.get("instruments", [])),
        "success_count": count,
        "partial_count": 0,
        "fail_count": 0,
        "schema_version": 1,
        "config_hash": config_hash,
    }
    summary = db.finish_cli_run(con, run, summary)
    if args.json:
        print(json.dumps(summary, ensure_ascii=False, sort_keys=True))
    else:
        print(f"initialized db={Path(args.db)} instruments={count}")
    con.close()


if __name__ == "__main__":
    main()
