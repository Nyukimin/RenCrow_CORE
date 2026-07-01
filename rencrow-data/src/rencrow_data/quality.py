from __future__ import annotations

import json
import math
from dataclasses import dataclass
from datetime import date, timedelta
from typing import Iterable

from .timeutil import unique_id


@dataclass(frozen=True)
class QualityOptions:
    as_of: date
    min_history_days: int = 260
    max_missing_rate: float = 0.35
    stale_days: int = 7
    outlier_return_abs: float = 0.45
    volume_outlier_ratio: float = 10.0
    adjustment_ratio_jump: float = 0.25
    fetch_lookback_days: int = 7
    symbols: tuple[str, ...] = ()
    asset_types: tuple[str, ...] = ()


def _json(value: object) -> str:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"))


def _business_days(start: date, end: date) -> int:
    if end < start:
        return 0
    days = 0
    current = start
    while current <= end:
        if current.weekday() < 5:
            days += 1
        current += timedelta(days=1)
    return days


def _median(values: list[float]) -> float | None:
    if not values:
        return None
    ordered = sorted(values)
    midpoint = len(ordered) // 2
    if len(ordered) % 2:
        return ordered[midpoint]
    return (ordered[midpoint - 1] + ordered[midpoint]) / 2.0


def _insert_check(
    con,
    *,
    run_id: str,
    instrument_id: int | None,
    check_date: date,
    check_type: str,
    severity: str,
    status: str,
    metric_value: float | None = None,
    detail: object | None = None,
) -> None:
    con.execute(
        """
        INSERT INTO data_quality_check(run_id, instrument_id, check_date, check_type, severity, status, metric_value, detail_json)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)
        """,
        (
            run_id,
            instrument_id,
            check_date.isoformat(),
            check_type,
            severity,
            status,
            metric_value,
            None if detail is None else _json(detail),
        ),
    )


def _selected_instruments(con, options: QualityOptions) -> list:
    clauses = ["active=1"]
    params: list[object] = []
    if options.symbols:
        clauses.append("symbol IN (%s)" % ",".join("?" for _ in options.symbols))
        params.extend(options.symbols)
    if options.asset_types:
        clauses.append("asset_type IN (%s)" % ",".join("?" for _ in options.asset_types))
        params.extend(options.asset_types)
    return list(
        con.execute(
            f"""
            SELECT instrument_id, symbol, asset_type, venue, currency
              FROM instruments
             WHERE {' AND '.join(clauses)}
             ORDER BY symbol, instrument_id
            """,
            params,
        ).fetchall()
    )


def _price_rows(con, instrument_id: int, as_of: date) -> list:
    return list(
        con.execute(
            """
            SELECT trade_date, close, adj_close, volume, source_name
              FROM price_raw
             WHERE instrument_id=? AND trade_date<=?
             ORDER BY trade_date
            """,
            (instrument_id, as_of.isoformat()),
        ).fetchall()
    )


def _check_instrument(con, run_id: str, item, options: QualityOptions) -> None:
    iid = int(item["instrument_id"])
    symbol = item["symbol"]
    asset_type = item["asset_type"]
    rows = _price_rows(con, iid, options.as_of)
    if not rows:
        if asset_type in {"FX", "RATE", "MACRO"}:
            _insert_check(
                con,
                run_id=run_id,
                instrument_id=iid,
                check_date=options.as_of,
                check_type="missing",
                severity="info",
                status="pass",
                metric_value=0.0,
                detail={"symbol": symbol, "asset_type": asset_type, "reason": "price_validation_skipped"},
            )
            return
        _insert_check(
            con,
            run_id=run_id,
            instrument_id=iid,
            check_date=options.as_of,
            check_type="missing",
            severity="blocker",
            status="fail",
            metric_value=1.0,
            detail={"symbol": symbol, "reason": "no_price_rows"},
        )
        return

    latest = date.fromisoformat(rows[-1]["trade_date"])
    stale_days = (options.as_of - latest).days
    stale_status = "fail" if stale_days > options.stale_days else "pass"
    _insert_check(
        con,
        run_id=run_id,
        instrument_id=iid,
        check_date=options.as_of,
        check_type="stale",
        severity="blocker" if stale_status == "fail" else "info",
        status=stale_status,
        metric_value=float(stale_days),
        detail={"symbol": symbol, "latest_trade_date": latest.isoformat(), "threshold_days": options.stale_days},
    )

    window_start = options.as_of - timedelta(days=max(options.min_history_days - 1, 0))
    window_rows = [row for row in rows if date.fromisoformat(row["trade_date"]) >= window_start]
    expected = _business_days(window_start, options.as_of)
    missing_rate = 0.0 if expected == 0 else max(0.0, 1.0 - (len({row["trade_date"] for row in window_rows}) / expected))
    missing_status = "fail" if missing_rate > options.max_missing_rate else "pass"
    _insert_check(
        con,
        run_id=run_id,
        instrument_id=iid,
        check_date=options.as_of,
        check_type="missing",
        severity="blocker" if missing_status == "fail" and asset_type in {"ETF", "CASH_PROXY"} else ("warning" if missing_status == "fail" else "info"),
        status=missing_status,
        metric_value=missing_rate,
        detail={
            "symbol": symbol,
            "window_start": window_start.isoformat(),
            "expected_business_days": expected,
            "observed_rows": len(window_rows),
            "threshold": options.max_missing_rate,
        },
    )

    worst_return = 0.0
    worst_return_date: str | None = None
    previous_price: float | None = None
    for row in rows:
        price = row["adj_close"] if row["adj_close"] is not None else row["close"]
        if price is None or price <= 0:
            continue
        if previous_price is not None and previous_price > 0:
            ret = float(price) / previous_price - 1.0
            if abs(ret) > abs(worst_return):
                worst_return = ret
                worst_return_date = row["trade_date"]
        previous_price = float(price)
    outlier_status = "fail" if abs(worst_return) > options.outlier_return_abs else "pass"
    _insert_check(
        con,
        run_id=run_id,
        instrument_id=iid,
        check_date=options.as_of,
        check_type="return_outlier",
        severity="blocker" if outlier_status == "fail" and asset_type in {"ETF", "CASH_PROXY"} else ("warning" if outlier_status == "fail" else "info"),
        status=outlier_status,
        metric_value=worst_return,
        detail={"symbol": symbol, "date": worst_return_date, "threshold_abs_return": options.outlier_return_abs},
    )

    worst_volume_ratio = 0.0
    worst_volume_date: str | None = None
    worst_volume_baseline: float | None = None
    positive_volumes: list[float] = []
    for row in rows:
        volume = row["volume"]
        if volume is None or volume <= 0:
            continue
        baseline = _median(positive_volumes[-20:])
        if baseline is not None and baseline > 0:
            ratio = float(volume) / baseline
            if math.isfinite(ratio) and ratio > worst_volume_ratio:
                worst_volume_ratio = ratio
                worst_volume_date = row["trade_date"]
                worst_volume_baseline = baseline
        positive_volumes.append(float(volume))
    volume_status = "fail" if worst_volume_ratio > options.volume_outlier_ratio else "pass"
    _insert_check(
        con,
        run_id=run_id,
        instrument_id=iid,
        check_date=options.as_of,
        check_type="volume_outlier",
        severity="blocker" if volume_status == "fail" and asset_type in {"ETF", "CASH_PROXY"} else ("warning" if volume_status == "fail" else "info"),
        status=volume_status,
        metric_value=worst_volume_ratio,
        detail={
            "symbol": symbol,
            "date": worst_volume_date,
            "baseline_volume": worst_volume_baseline,
            "threshold_ratio": options.volume_outlier_ratio,
        },
    )

    worst_jump = 0.0
    worst_jump_date: str | None = None
    previous_ratio: float | None = None
    for row in rows:
        close = row["close"]
        adj_close = row["adj_close"]
        if close is None or adj_close is None or close <= 0:
            continue
        ratio = float(adj_close) / float(close)
        if not math.isfinite(ratio):
            continue
        if previous_ratio is not None:
            jump = ratio / previous_ratio - 1.0 if previous_ratio else 0.0
            if abs(jump) > abs(worst_jump):
                worst_jump = jump
                worst_jump_date = row["trade_date"]
        previous_ratio = ratio
    action_exists = False
    if worst_jump_date is not None:
        action_exists = (
            con.execute(
                "SELECT 1 FROM corporate_action WHERE instrument_id=? AND action_date=? LIMIT 1",
                (iid, worst_jump_date),
            ).fetchone()
            is not None
        )
    adjustment_status = "fail" if abs(worst_jump) > options.adjustment_ratio_jump and not action_exists else "pass"
    _insert_check(
        con,
        run_id=run_id,
        instrument_id=iid,
        check_date=options.as_of,
        check_type="adjustment_anomaly",
        severity="blocker" if adjustment_status == "fail" and asset_type in {"ETF", "CASH_PROXY"} else ("warning" if adjustment_status == "fail" else "info"),
        status=adjustment_status,
        metric_value=worst_jump,
        detail={
            "symbol": symbol,
            "date": worst_jump_date,
            "threshold_ratio_jump": options.adjustment_ratio_jump,
            "corporate_action_found": action_exists,
        },
    )


def _check_fetch_logs(con, run_id: str, options: QualityOptions) -> None:
    since = (options.as_of - timedelta(days=max(options.fetch_lookback_days, 0))).isoformat()
    rows = list(
        con.execute(
            """
            SELECT source_name, status, COUNT(*) AS count
              FROM source_fetch_log
             WHERE requested_at>=? AND status IN ('fail', 'partial')
             GROUP BY source_name, status
             ORDER BY source_name, status
            """,
            (since,),
        ).fetchall()
    )
    if not rows:
        _insert_check(
            con,
            run_id=run_id,
            instrument_id=None,
            check_date=options.as_of,
            check_type="fetch_status",
            severity="info",
            status="pass",
            metric_value=0.0,
            detail={"since": since, "failures": []},
        )
        return
    for row in rows:
        status = row["status"]
        count = int(row["count"])
        _insert_check(
            con,
            run_id=run_id,
            instrument_id=None,
            check_date=options.as_of,
            check_type=f"fetch_{status}",
            severity="blocker" if status == "fail" else "warning",
            status="fail" if status == "fail" else "partial",
            metric_value=float(count),
            detail={"since": since, "source_name": row["source_name"], "source_status": status},
        )


def validate_data(con, options: QualityOptions) -> dict[str, object]:
    run_id = unique_id("quality")
    instruments = _selected_instruments(con, options)
    for item in instruments:
        _check_instrument(con, run_id, item, options)
    _check_fetch_logs(con, run_id, options)
    con.commit()

    summary = con.execute(
        """
        SELECT
          COUNT(*) AS total,
          SUM(CASE WHEN severity='blocker' AND status!='pass' THEN 1 ELSE 0 END) AS blockers,
          SUM(CASE WHEN severity='warning' AND status!='pass' THEN 1 ELSE 0 END) AS warnings,
          SUM(CASE WHEN status='partial' THEN 1 ELSE 0 END) AS partials,
          SUM(CASE WHEN status='fail' THEN 1 ELSE 0 END) AS failures
        FROM data_quality_check
        WHERE run_id=?
        """,
        (run_id,),
    ).fetchone()
    return {
        "run_id": run_id,
        "check_date": options.as_of.isoformat(),
        "instrument_count": len(instruments),
        "total_checks": int(summary["total"] or 0),
        "blockers": int(summary["blockers"] or 0),
        "warnings": int(summary["warnings"] or 0),
        "partials": int(summary["partials"] or 0),
        "failures": int(summary["failures"] or 0),
    }


def exit_code(summary: dict[str, object]) -> int:
    if int(summary.get("blockers", 0)) > 0:
        return 3
    if int(summary.get("warnings", 0)) > 0 or int(summary.get("partials", 0)) > 0:
        return 2
    return 0


def parse_csv_values(values: Iterable[str] | None) -> tuple[str, ...]:
    if not values:
        return ()
    parsed: list[str] = []
    for value in values:
        parsed.extend(part.strip() for part in value.split(",") if part.strip())
    return tuple(parsed)
