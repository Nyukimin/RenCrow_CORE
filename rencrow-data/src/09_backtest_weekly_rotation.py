#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import sys
from datetime import date

from rencrow_data import db
from rencrow_data.backtest import BacktestOptions, run_weekly_rotation_backtest
from rencrow_data.config import config_hash_for_paths, config_path, load_config, resolve_repo_relative_path
from rencrow_data.quality import parse_csv_values


def _parse_date(value: str | None) -> date | None:
    if not value:
        return None
    return date.fromisoformat(value)


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


def _strategy_config_hash(con, strategy_id: str) -> str | None:
    row = con.execute(
        "SELECT config_hash FROM strategy_version WHERE strategy_id=? AND active=1",
        (strategy_id,),
    ).fetchone()
    return None if row is None else row["config_hash"]


def _cost_config_values(path: str) -> dict[str, object]:
    config = load_config(path, default={})
    return {
        "cost_bps": float(config.get("cost_bps", 10.0)),
        "slippage_bps": float(config.get("slippage_bps", 0.0)),
        "tax_mode": str(config.get("tax_mode", "none")),
    }


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--db", "--db-path", dest="db_path", default="rencrow-data/data/rencrow.db")
    parser.add_argument("--snapshot", required=True)
    parser.add_argument("--strategy", default="weekly_etf_rotation_v1")
    parser.add_argument("--start")
    parser.add_argument("--end")
    parser.add_argument("--config-root", default="rencrow-data/config")
    parser.add_argument("--cost-config", default=None)
    parser.add_argument("--cost-bps", type=float, default=None)
    parser.add_argument("--slippage-bps", type=float, default=None)
    parser.add_argument("--tax-mode", choices=("none", "approx_jp_taxable"), default=None)
    parser.add_argument("--walk-forward", action="store_true")
    parser.add_argument("--output-dir", default="rencrow-data/data/backtests")
    parser.add_argument("--symbols", action="append", help="Comma-separated symbols overriding the strategy universe.")
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()
    args.db_path = str(resolve_repo_relative_path(args.db_path))
    cost_config = resolve_repo_relative_path(args.cost_config) if args.cost_config else config_path(args.config_root, "costs.yml")
    cost_values = _cost_config_values(str(cost_config))
    cost_bps = cost_values["cost_bps"] if args.cost_bps is None else args.cost_bps
    slippage_bps = cost_values["slippage_bps"] if args.slippage_bps is None else args.slippage_bps
    tax_mode = cost_values["tax_mode"] if args.tax_mode is None else args.tax_mode

    con = db.connect(args.db_path)
    db.init_schema(con)
    strategy_config_hash = _strategy_config_hash(con, args.strategy)
    cost_config_hash = config_hash_for_paths([cost_config])
    config_hash = f"{strategy_config_hash or ''}:{cost_config_hash}"
    run = db.start_cli_run(con, "09_backtest_weekly_rotation.py", args.db_path, config_hash=config_hash)
    try:
        snapshot_id = _resolve_snapshot(con, args.snapshot)
        options = BacktestOptions(
            snapshot_id=snapshot_id,
            strategy_id=args.strategy,
            start=_parse_date(args.start),
            end=_parse_date(args.end),
            cost_bps=cost_bps,
            slippage_bps=slippage_bps,
            tax_mode=tax_mode,
            mode="walk_forward" if args.walk_forward else "full",
            output_dir=resolve_repo_relative_path(args.output_dir),
            symbols=parse_csv_values(args.symbols),
        )
        result = run_weekly_rotation_backtest(con, options)
        result.update(
            {
                "cli_name": "09_backtest_weekly_rotation.py",
                "db_path": args.db_path,
                "status": "success",
                "target_count": 1,
                "success_count": 1,
                "partial_count": 0,
                "fail_count": 0,
                "config_hash": config_hash,
                "cost_config_path": str(cost_config),
            }
        )
        result = db.finish_cli_run(con, run, result)
    except KeyError as exc:
        db.fail_cli_run(con, run, error_message=str(exc), exit_code=4)
        print(f"config error: {exc}", file=sys.stderr)
        raise SystemExit(4)
    except ValueError as exc:
        db.fail_cli_run(con, run, error_message=str(exc), exit_code=3)
        print(f"data error: {exc}", file=sys.stderr)
        raise SystemExit(3)
    finally:
        con.close()

    if args.json:
        print(json.dumps(result, ensure_ascii=False, sort_keys=True))
    else:
        metrics = result["metrics"]
        print(
            "backtest success "
            f"id={result['backtest_id']} strategy={result['strategy_id']} snapshot={result['snapshot_id']} "
            f"weeks={result['weeks']} final_equity={metrics['final_equity']:.6f} "
            f"cagr={metrics['cagr']:.6f} max_dd={metrics['max_dd']:.6f}"
        )


if __name__ == "__main__":
    main()
