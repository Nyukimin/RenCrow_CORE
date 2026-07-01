from __future__ import annotations

import json
from dataclasses import dataclass
from datetime import date, timedelta

from .hashing import assert_snapshot_features_unchanged
from .timeutil import unique_id


@dataclass(frozen=True)
class RiskOptions:
    snapshot_id: str
    strategy_id: str
    decision_id: str | None = None
    config: dict[str, object] | None = None


def _json(value: object) -> str:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"))


def _latest_backtest_id(con, snapshot_id: str, strategy_id: str) -> str | None:
    row = con.execute(
        """
        SELECT backtest_id
          FROM backtest_run
         WHERE snapshot_id=? AND strategy_id=? AND status='success'
         ORDER BY created_at DESC
         LIMIT 1
        """,
        (snapshot_id, strategy_id),
    ).fetchone()
    return None if row is None else row["backtest_id"]


def _metrics(con, backtest_id: str) -> dict[str, float]:
    return {
        row["metric_name"]: float(row["metric_value"])
        for row in con.execute(
            "SELECT metric_name, metric_value FROM backtest_metric WHERE backtest_id=? AND split_name='full'",
            (backtest_id,),
        )
    }


def _backtest_result(con, backtest_id: str) -> dict[str, object]:
    row = con.execute("SELECT result_json FROM backtest_run WHERE backtest_id=?", (backtest_id,)).fetchone()
    if row is None:
        return {}
    try:
        parsed = json.loads(row["result_json"] or "{}")
    except json.JSONDecodeError:
        return {}
    return parsed if isinstance(parsed, dict) else {}


def _snapshot_date(con, snapshot_id: str) -> str:
    row = con.execute("SELECT snapshot_date, features_hash FROM snapshot_registry WHERE snapshot_id=?", (snapshot_id,)).fetchone()
    if row is None:
        raise ValueError(f"snapshot not found: {snapshot_id}")
    assert_snapshot_features_unchanged(con, snapshot_id, row["snapshot_date"], row["features_hash"])
    return row["snapshot_date"]


def _data_quality_issues(con, snapshot_date: str) -> dict[str, int]:
    row = con.execute(
        """
        SELECT
          SUM(CASE WHEN severity='blocker' AND status!='pass' THEN 1 ELSE 0 END) AS blockers,
          SUM(CASE WHEN status='partial' THEN 1 ELSE 0 END) AS partials,
          SUM(CASE WHEN severity='warning' AND status!='pass' AND status!='partial' THEN 1 ELSE 0 END) AS warnings
          FROM data_quality_check
         WHERE check_date=?
        """,
        (snapshot_date,),
    ).fetchone()
    return {
        "blockers": int(row["blockers"] or 0),
        "partials": int(row["partials"] or 0),
        "warnings": int(row["warnings"] or 0),
    }


def _event_blockers(con, snapshot_date: str, lookback_days: int, threshold: float) -> tuple[int, int]:
    end = date.fromisoformat(snapshot_date)
    start = (end - timedelta(days=max(lookback_days, 0))).isoformat()
    row = con.execute(
        """
        SELECT
          SUM(
            CASE
              WHEN reason='manual_kill_switch'
                OR level='kill'
                OR level='stop'
                OR COALESCE(event_risk_score, 0) >= ?
              THEN 1 ELSE 0
            END
          ) AS stop_count,
          SUM(
            CASE
              WHEN reason='manual_kill_switch'
                OR level='kill'
              THEN 1 ELSE 0
            END
          ) AS kill_count
          FROM event_log
         WHERE resolved_at IS NULL
           AND (
             date(event_ts) BETWEEN date(?) AND date(?)
             OR reason='manual_kill_switch'
           )
        """,
        (threshold, start, snapshot_date),
    ).fetchone()
    return int(row["stop_count"] or 0), int(row["kill_count"] or 0)


def _weekly_signal_concentration(con, snapshot_id: str, strategy_id: str, snapshot_date: str) -> dict[str, object]:
    rows = con.execute(
        """
        SELECT i.symbol,
               COALESCE(NULLIF(i.asset_type, ''), 'UNKNOWN') AS asset_type,
               SUM(ABS(COALESCE(w.target_weight, 0))) AS weight
          FROM weekly_signal w
          JOIN instruments i ON i.instrument_id=w.instrument_id
         WHERE w.snapshot_id=?
           AND w.strategy_id=?
           AND w.week_end=?
           AND COALESCE(w.vetoed, 0)=0
         GROUP BY i.symbol, COALESCE(NULLIF(i.asset_type, ''), 'UNKNOWN')
        """,
        (snapshot_id, strategy_id, snapshot_date),
    ).fetchall()
    return _concentration_from_rows(
        [
            {
                "symbol": str(row["symbol"]),
                "asset_type": str(row["asset_type"]),
                "weight": float(row["weight"] or 0.0),
            }
            for row in rows
        ],
        source="weekly_signal",
    )


def _concentration_from_rows(rows: list[dict[str, object]], *, source: str) -> dict[str, object]:
    symbol_weights: dict[str, float] = {}
    asset_weights: dict[str, float] = {}
    for row in rows:
        symbol = str(row.get("symbol") or "")
        asset_type = str(row.get("asset_type") or "UNKNOWN")
        weight = abs(float(row.get("weight") or 0.0))
        if symbol:
            symbol_weights[symbol] = symbol_weights.get(symbol, 0.0) + weight
        asset_weights[asset_type] = asset_weights.get(asset_type, 0.0) + weight
    max_symbol = max(symbol_weights, key=symbol_weights.get) if symbol_weights else None
    max_asset_type = max(asset_weights, key=asset_weights.get) if asset_weights else None
    return {
        "source": source,
        "symbol_weights": symbol_weights,
        "asset_class_weights": asset_weights,
        "max_symbol": max_symbol,
        "max_symbol_weight": symbol_weights[max_symbol] if max_symbol else 0.0,
        "max_asset_type": max_asset_type,
        "max_asset_class_weight": asset_weights[max_asset_type] if max_asset_type else 0.0,
    }


def _planned_concentration(con, backtest_result: dict[str, object], snapshot_id: str, strategy_id: str, snapshot_date: str) -> dict[str, object]:
    latest_signal = backtest_result.get("latest_signal")
    if isinstance(latest_signal, dict) and latest_signal.get("symbol"):
        return _concentration_from_rows(
            [
                {
                    "symbol": str(latest_signal.get("symbol") or ""),
                    "asset_type": str(latest_signal.get("asset_type") or "UNKNOWN"),
                    "weight": float(latest_signal.get("target_weight") or 0.0),
                }
            ],
            source="backtest_latest_signal",
        )
    return _weekly_signal_concentration(con, snapshot_id, strategy_id, snapshot_date)


def run_risk_check(con, options: RiskOptions) -> dict[str, object]:
    config = options.config or {}
    snapshot_date = _snapshot_date(con, options.snapshot_id)
    backtest_id = _latest_backtest_id(con, options.snapshot_id, options.strategy_id)
    if backtest_id is None:
        raise ValueError(f"successful backtest not found for strategy={options.strategy_id} snapshot={options.snapshot_id}")
    metrics = _metrics(con, backtest_id)
    backtest_result = _backtest_result(con, backtest_id)

    max_dd_limit = float(config.get("max_drawdown_limit", 0.25))
    weekly_loss_limit = float(config.get("weekly_loss_limit", 0.08))
    vol_limit = float(config.get("annualized_volatility_limit", 0.30))
    turnover_warning_limit = float(config.get("turnover_warning_limit", 0.50))
    asset_class_concentration_limit = float(config.get("asset_class_concentration_limit", 0.80))
    single_symbol_concentration_limit = float(config.get("single_symbol_concentration_limit", 1.0))
    event_threshold = float(config.get("event_risk_stop_threshold", 0.9))
    event_lookback_days = int(config.get("event_lookback_days", 7))

    max_dd_check = "fail" if metrics.get("max_dd", 0.0) < -max_dd_limit else "pass"
    weekly_loss_check = "fail" if metrics.get("worst_week", 0.0) < -weekly_loss_limit else "pass"
    volatility_check = "fail" if metrics.get("annualized_volatility", 0.0) > vol_limit else "pass"
    turnover_check = "warning" if metrics.get("turnover", 0.0) > turnover_warning_limit else "pass"
    planned_concentration = _planned_concentration(con, backtest_result, options.snapshot_id, options.strategy_id, snapshot_date)
    asset_class_concentration_check = (
        "warning" if float(planned_concentration["max_asset_class_weight"]) > asset_class_concentration_limit else "pass"
    )
    single_symbol_concentration_check = (
        "warning" if float(planned_concentration["max_symbol_weight"]) > single_symbol_concentration_limit else "pass"
    )
    concentration_reasons = [
        reason
        for reason, check in (
            ("single_symbol_concentration", single_symbol_concentration_check),
            ("asset_class_concentration", asset_class_concentration_check),
            ("turnover", turnover_check),
        )
        if check == "warning"
    ]
    concentration_check = "warning" if concentration_reasons else "pass"

    quality_issues = _data_quality_issues(con, snapshot_date)
    quality_blockers = quality_issues["blockers"]
    quality_partials = quality_issues["partials"]
    quality_warnings = quality_issues["warnings"]
    event_blockers, kill_event_blockers = _event_blockers(con, snapshot_date, event_lookback_days, event_threshold)
    event_check = "fail" if quality_blockers or quality_partials or event_blockers else "pass"

    status = "pass"
    if concentration_check == "warning" or quality_warnings:
        status = "reduce"
    if max_dd_check == "fail" or weekly_loss_check == "fail" or volatility_check == "fail" or event_check == "fail":
        status = "stop"
    if kill_event_blockers:
        status = "kill_switch"

    detail = {
        "snapshot_date": snapshot_date,
        "backtest_id": backtest_id,
        "metrics": metrics,
        "limits": {
            "max_drawdown_limit": max_dd_limit,
            "weekly_loss_limit": weekly_loss_limit,
            "annualized_volatility_limit": vol_limit,
            "turnover_warning_limit": turnover_warning_limit,
            "asset_class_concentration_limit": asset_class_concentration_limit,
            "single_symbol_concentration_limit": single_symbol_concentration_limit,
            "event_risk_stop_threshold": event_threshold,
            "event_lookback_days": event_lookback_days,
        },
        "turnover_check": turnover_check,
        "asset_class_concentration_check": asset_class_concentration_check,
        "single_symbol_concentration_check": single_symbol_concentration_check,
        "planned_concentration": planned_concentration,
        "asset_class_concentration": {
            "weights": planned_concentration["asset_class_weights"],
            "max_asset_type": planned_concentration["max_asset_type"],
            "max_weight": planned_concentration["max_asset_class_weight"],
        },
        "concentration_reasons": concentration_reasons,
        "quality_blockers": quality_blockers,
        "quality_partials": quality_partials,
        "quality_warnings": quality_warnings,
        "event_blockers": event_blockers,
        "kill_event_blockers": kill_event_blockers,
    }
    risk_check_id = unique_id("risk")
    con.execute(
        """
        INSERT INTO risk_check_result(
          risk_check_id, snapshot_id, strategy_id, decision_id, status, max_dd_check,
          weekly_loss_check, concentration_check, volatility_check, event_check, detail_json
        )
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        """,
        (
            risk_check_id,
            options.snapshot_id,
            options.strategy_id,
            options.decision_id,
            status,
            max_dd_check,
            weekly_loss_check,
            concentration_check,
            volatility_check,
            event_check,
            _json(detail),
        ),
    )
    con.commit()
    return {
        "risk_check_id": risk_check_id,
        "snapshot_id": options.snapshot_id,
        "strategy_id": options.strategy_id,
        "decision_id": options.decision_id,
        "status": status,
        "max_dd_check": max_dd_check,
        "weekly_loss_check": weekly_loss_check,
        "concentration_check": concentration_check,
        "volatility_check": volatility_check,
        "event_check": event_check,
        "detail": detail,
    }


def exit_code(result: dict[str, object]) -> int:
    return 3 if result.get("status") in {"stop", "kill_switch"} else 0
