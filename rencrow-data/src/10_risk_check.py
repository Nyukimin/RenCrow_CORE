#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import sys

from rencrow_data import db
from rencrow_data.config import config_hash_for_paths, load_config, resolve_repo_relative_path
from rencrow_data.risk import RiskOptions, exit_code, run_risk_check


def _resolve_snapshot(con, value: str) -> str:
    if value == "latest":
        row = con.execute(
            "SELECT snapshot_id FROM snapshot_registry ORDER BY snapshot_date DESC, snapshot_id DESC LIMIT 1"
        ).fetchone()
        if row is None:
            raise ValueError("cannot resolve --snapshot latest because snapshot_registry is empty")
        return str(row["snapshot_id"])
    row = con.execute("SELECT snapshot_id FROM snapshot_registry WHERE snapshot_id=?", (value,)).fetchone()
    if row is None:
        raise ValueError(f"snapshot not found: {value}")
    return value


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--db", "--db-path", dest="db_path", default="rencrow-data/data/rencrow.db")
    parser.add_argument("--snapshot", required=True)
    parser.add_argument("--strategy", default="weekly_etf_rotation_v1")
    parser.add_argument("--decision")
    parser.add_argument("--risk-config", default="rencrow-data/config/risk_limits.yml")
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()
    args.db_path = str(resolve_repo_relative_path(args.db_path))
    args.risk_config = str(resolve_repo_relative_path(args.risk_config))

    con = db.connect(args.db_path)
    db.init_schema(con)
    config_hash = config_hash_for_paths([args.risk_config])
    run = db.start_cli_run(con, "10_risk_check.py", args.db_path, config_hash=config_hash)
    try:
        snapshot_id = _resolve_snapshot(con, args.snapshot)
        config = load_config(args.risk_config, default={})
        result = run_risk_check(
            con,
            RiskOptions(
                snapshot_id=snapshot_id,
                strategy_id=args.strategy,
                decision_id=args.decision,
                config=config,
            ),
        )
        result.update(
            {
                "cli_name": "10_risk_check.py",
                "db_path": args.db_path,
                "exit_code": exit_code(result),
                "target_count": 1,
                "success_count": 1 if result["status"] in {"pass", "reduce"} else 0,
                "partial_count": 0,
                "fail_count": 1 if result["status"] in {"stop", "kill_switch"} else 0,
                "config_hash": config_hash,
            }
        )
        result = db.finish_cli_run(con, run, result)
    except ValueError as exc:
        db.fail_cli_run(con, run, error_message=str(exc), exit_code=4)
        print(f"data error: {exc}", file=sys.stderr)
        raise SystemExit(4)
    finally:
        con.close()

    if args.json:
        print(json.dumps(result, ensure_ascii=False, sort_keys=True))
    else:
        print(
            "risk check "
            f"id={result['risk_check_id']} status={result['status']} strategy={result['strategy_id']} "
            f"snapshot={result['snapshot_id']} max_dd={result['max_dd_check']} "
            f"weekly_loss={result['weekly_loss_check']} volatility={result['volatility_check']} "
            f"event={result['event_check']}"
        )
    raise SystemExit(exit_code(result))


if __name__ == "__main__":
    main()
