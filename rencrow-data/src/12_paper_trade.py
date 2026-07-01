#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import sys

from rencrow_data import db
from rencrow_data.config import resolve_repo_relative_path
from rencrow_data.paper import PaperTradeOptions, run_paper_trade


def _resolve_decision(con, value: str) -> int:
    if value == "latest":
        row = con.execute("SELECT decision_id FROM decision_log ORDER BY created_at DESC, decision_id DESC LIMIT 1").fetchone()
        if row is None:
            raise ValueError("cannot resolve --decision latest because decision_log is empty")
        return int(row["decision_id"])
    return int(value)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--db", "--db-path", dest="db_path", default="rencrow-data/data/rencrow.db")
    parser.add_argument("--decision", required=True)
    parser.add_argument("--approval-file", required=True)
    parser.add_argument("--fill-model", choices=("close_next_week", "open_next_session", "vwap_approx"), default="close_next_week")
    parser.add_argument("--capital", type=float, default=1_000_000.0)
    parser.add_argument("--cost-bps", type=float, default=10.0)
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()
    args.db_path = str(resolve_repo_relative_path(args.db_path))

    con = db.connect(args.db_path)
    db.init_schema(con)
    run = db.start_cli_run(con, "12_paper_trade.py", args.db_path)
    try:
        decision_id = _resolve_decision(con, args.decision)
        result = run_paper_trade(
            con,
            PaperTradeOptions(
                decision_id=decision_id,
                approval_file=resolve_repo_relative_path(args.approval_file),
                fill_model=args.fill_model,
                capital=args.capital,
                cost_bps=args.cost_bps,
            ),
        )
        result.update(
            {
                "cli_name": "12_paper_trade.py",
                "db_path": args.db_path,
                "target_count": 1,
                "success_count": 1 if result["status"] in {"simulated", "filled", "vetoed", "no_candidate"} else 0,
                "partial_count": 0,
                "fail_count": 0,
            }
        )
        result = db.finish_cli_run(con, run, result)
    except FileNotFoundError as exc:
        db.fail_cli_run(con, run, error_message=str(exc), exit_code=4)
        print(f"approval error: file not found: {exc}", file=sys.stderr)
        raise SystemExit(4)
    except PermissionError as exc:
        db.fail_cli_run(con, run, error_message=str(exc), exit_code=3)
        print(f"approval error: {exc}", file=sys.stderr)
        raise SystemExit(3)
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
            "paper trade "
            f"decision={result['decision_id']} status={result['status']} trades={len(result['trades'])} "
            f"fill_model={result['tca']['fill_model']}"
        )


if __name__ == "__main__":
    main()
