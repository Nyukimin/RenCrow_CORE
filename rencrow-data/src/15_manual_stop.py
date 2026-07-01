#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import sys

from rencrow_data import db
from rencrow_data.config import resolve_repo_relative_path
from rencrow_data.timeutil import utcnow_iso


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--db", "--db-path", dest="db_path", default="rencrow-data/data/rencrow.db")
    parser.add_argument("--operator", required=True)
    parser.add_argument("--reason", required=True)
    parser.add_argument("--resolve-event-id", type=int)
    parser.add_argument("--scope", default="system")
    parser.add_argument("--level", choices=("stop", "kill"), default="kill")
    parser.add_argument("--note", default="")
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()
    args.db_path = str(resolve_repo_relative_path(args.db_path))

    con = db.connect(args.db_path)
    db.init_schema(con)
    run = db.start_cli_run(con, "15_manual_stop.py", args.db_path)
    if args.resolve_event_id is not None:
        row = con.execute(
            """
            SELECT event_id, reason, resolved_at
              FROM event_log
             WHERE event_id=?
            """,
            (args.resolve_event_id,),
        ).fetchone()
        if row is None:
            message = f"event not found: {args.resolve_event_id}"
            db.fail_cli_run(con, run, error_message=message, exit_code=4)
            con.close()
            print(message, file=sys.stderr)
            raise SystemExit(4)
        if row["reason"] != "manual_kill_switch":
            message = "only manual_kill_switch events can be manually resolved"
            db.fail_cli_run(con, run, error_message=message, exit_code=4)
            con.close()
            print(message, file=sys.stderr)
            raise SystemExit(4)
        if row["resolved_at"]:
            message = "event is already resolved"
            db.fail_cli_run(con, run, error_message=message, exit_code=4)
            con.close()
            print(message, file=sys.stderr)
            raise SystemExit(4)
        resolved_at = utcnow_iso()
        resolution = {
            "operator": args.operator,
            "reason": args.reason,
            "note": args.note,
            "resolved_at": resolved_at,
            "manual_resolution": True,
            "recovery_rule": "resume only after a new risk_check pass",
        }
        con.execute(
            """
            UPDATE event_log
               SET resolved_at=?, resolution_note=?
             WHERE event_id=?
            """,
            (resolved_at, json.dumps(resolution, ensure_ascii=False, sort_keys=True), args.resolve_event_id),
        )
        con.commit()
        summary = {
            "cli_name": "15_manual_stop.py",
            "db_path": args.db_path,
            "status": "success",
            "target_count": 1,
            "success_count": 1,
            "partial_count": 0,
            "fail_count": 0,
            "event_id": args.resolve_event_id,
            "event_reason": "manual_kill_switch",
            "resolved": True,
            "resolved_at": resolved_at,
            "operator": args.operator,
            "reason": args.reason,
        }
        summary = db.finish_cli_run(con, run, summary)
        con.close()
        if args.json:
            print(json.dumps(summary, ensure_ascii=False, sort_keys=True))
        else:
            print(f"manual stop resolved event_id={args.resolve_event_id} operator={args.operator}")
        return

    context = {
        "operator": args.operator,
        "reason": args.reason,
        "note": args.note,
        "recorded_at": utcnow_iso(),
        "manual_stop": True,
        "recovery_rule": "risk_check must pass after manual review before trading resumes",
    }
    db.log_event(
        con,
        args.scope,
        args.level,
        "manual_kill_switch",
        value=1.0,
        event_risk_score=1.0,
        context_json=json.dumps(context, ensure_ascii=False, sort_keys=True),
    )
    event_id = int(con.execute("SELECT last_insert_rowid()").fetchone()[0])
    summary = {
        "cli_name": "15_manual_stop.py",
        "db_path": args.db_path,
        "status": "success",
        "target_count": 1,
        "success_count": 1,
        "partial_count": 0,
        "fail_count": 0,
        "event_id": event_id,
        "event_level": args.level,
        "event_reason": "manual_kill_switch",
        "operator": args.operator,
        "reason": args.reason,
    }
    summary = db.finish_cli_run(con, run, summary)
    con.close()
    if args.json:
        print(json.dumps(summary, ensure_ascii=False, sort_keys=True))
    else:
        print(f"manual stop recorded event_id={event_id} level={args.level} operator={args.operator}")


if __name__ == "__main__":
    main()
