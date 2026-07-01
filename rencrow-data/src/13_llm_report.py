#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import sys

from rencrow_data import db
from rencrow_data.config import resolve_repo_relative_path
from rencrow_data.llm_report import LLMReportOptions, build_llm_report


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


def _parse_decision(value: str | None) -> int | None:
    if not value or value == "latest":
        return None
    return int(value)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--db", "--db-path", dest="db_path", default="rencrow-data/data/rencrow.db")
    parser.add_argument("--snapshot", required=True)
    parser.add_argument("--decision")
    parser.add_argument("--task", choices=("weekly_report", "anomaly_summary", "event_summary", "spec_generation"), default="weekly_report")
    parser.add_argument("--model", default="local-deterministic")
    parser.add_argument("--prompt-version", default="weekly_report_v1")
    parser.add_argument("--output-dir", default="rencrow-data/reports")
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()
    args.db_path = str(resolve_repo_relative_path(args.db_path))

    con = db.connect(args.db_path)
    db.init_schema(con)
    run = db.start_cli_run(con, "13_llm_report.py", args.db_path)
    try:
        snapshot_id = _resolve_snapshot(con, args.snapshot)
        result = build_llm_report(
            con,
            LLMReportOptions(
                snapshot_id=snapshot_id,
                decision_id=_parse_decision(args.decision),
                task=args.task,
                model=args.model,
                prompt_version=args.prompt_version,
                output_dir=resolve_repo_relative_path(args.output_dir),
            ),
        )
        result.update(
            {
                "cli_name": "13_llm_report.py",
                "db_path": args.db_path,
                "status": "success",
                "target_count": 1,
                "success_count": 1,
                "partial_count": 0,
                "fail_count": 0,
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
            "llm report "
            f"log={result['llm_log_id']} snapshot={result['snapshot_id']} "
            f"uncertainty={result['uncertainty_flag']} output={result['output_path']}"
        )


if __name__ == "__main__":
    main()
