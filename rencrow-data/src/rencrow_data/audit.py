from __future__ import annotations

import json
from dataclasses import dataclass
from datetime import date
from pathlib import Path

from .hashing import stable_feature_hash


@dataclass(frozen=True)
class AuditOptions:
    snapshot_id: str
    decision_id: int | None = None
    output_dir: Path | None = None
    paper_latest: bool = False


def _snapshot(con, snapshot_id: str):
    row = con.execute("SELECT * FROM snapshot_registry WHERE snapshot_id=?", (snapshot_id,)).fetchone()
    if row is None:
        raise ValueError(f"snapshot not found: {snapshot_id}")
    return row


def _feature_hash_audit(con, snapshot) -> dict[str, object]:
    expected = snapshot["features_hash"] or ""
    current = stable_feature_hash(con, snapshot["snapshot_date"]) if expected else ""
    return {
        "expected": expected,
        "current": current,
        "match": not expected or current == expected,
    }


def _json_object(value: str | None) -> dict[str, object]:
    if not value:
        return {}
    try:
        parsed = json.loads(value)
    except Exception:
        return {"parse_error": True}
    return parsed if isinstance(parsed, dict) else {"value": parsed}


def _snapshot_source_lines(source_summary: dict[str, object]) -> list[str]:
    latest_fetches = source_summary.get("latest_fetches")
    lines = [
        "",
        "### Source Summary",
        "",
        f"- as_of: {source_summary.get('as_of', '')}",
        "| source_name | status | fetch_id | rows_fetched | endpoint | usage_terms | error |",
        "|---|---|---:|---:|---|---|---|",
    ]
    if not isinstance(latest_fetches, list) or not latest_fetches:
        lines.append("| - | - | - | - | - | - | - |")
        return lines
    for row in latest_fetches:
        if not isinstance(row, dict):
            continue
        lines.append(
            "| {source_name} | {status} | {fetch_id} | {rows_fetched} | {endpoint} | {usage_terms} | {error} |".format(
                source_name=row.get("source_name", ""),
                status=row.get("status", ""),
                fetch_id=row.get("fetch_id", ""),
                rows_fetched=row.get("rows_fetched", ""),
                endpoint=row.get("endpoint", ""),
                usage_terms=row.get("usage_terms", "") or "",
                error=row.get("error_message", "") or "",
            )
        )
    return lines


def _snapshot_event_lines(event_state: dict[str, object]) -> list[str]:
    open_events = event_state.get("open_events")
    latest_events = event_state.get("latest_open_events")
    lines = [
        "",
        "### Event State",
        "",
        f"- as_of: {event_state.get('as_of', '')}",
        f"- open_event_count: {event_state.get('open_event_count', '')}",
        f"- max_open_event_risk_score: {event_state.get('max_open_event_risk_score', '')}",
        "| level | reason | count |",
        "|---|---|---:|",
    ]
    if not isinstance(open_events, list) or not open_events:
        lines.append("| - | - | - |")
    else:
        for row in open_events:
            if isinstance(row, dict):
                lines.append(f"| {row.get('level', '')} | {row.get('reason', '')} | {row.get('n', '')} |")
    lines.extend(["", "| event_id | event_date | scope | level | reason | risk_score |", "|---:|---|---|---|---|---:|"])
    if not isinstance(latest_events, list) or not latest_events:
        lines.append("| - | - | - | - | - | - |")
    else:
        for row in latest_events:
            if isinstance(row, dict):
                lines.append(
                    "| {event_id} | {event_date} | {scope} | {level} | {reason} | {risk} |".format(
                        event_id=row.get("event_id", ""),
                        event_date=row.get("event_date", ""),
                        scope=row.get("scope", ""),
                        level=row.get("level", ""),
                        reason=row.get("reason", ""),
                        risk=row.get("event_risk_score", ""),
                    )
                )
    return lines


def _snapshot_fetch_quality(con, source_summary: dict[str, object]) -> tuple[int, int]:
    latest_fetches = source_summary.get("latest_fetches")
    if isinstance(latest_fetches, list) and latest_fetches:
        failures = 0
        partials = 0
        for row in latest_fetches:
            if not isinstance(row, dict):
                continue
            if row.get("status") == "fail":
                failures += 1
            elif row.get("status") == "partial":
                partials += 1
        return failures, partials
    return (
        _count(con, "SELECT COUNT(*) FROM source_fetch_log WHERE status='fail'"),
        _count(con, "SELECT COUNT(*) FROM source_fetch_log WHERE status='partial'"),
    )


def _latest_decision(con, snapshot_id: str) -> int | None:
    row = con.execute(
        "SELECT decision_id FROM decision_log WHERE snapshot_id=? ORDER BY created_at DESC, decision_id DESC LIMIT 1",
        (int(snapshot_id),),
    ).fetchone()
    return None if row is None else int(row["decision_id"])


def _latest_paper_decision(con, snapshot_id: str) -> int | None:
    row = con.execute(
        """
        SELECT d.decision_id
          FROM decision_log d
          JOIN paper_trade_log p ON p.decision_id=d.decision_id
         WHERE d.snapshot_id=?
           AND d.account_scope='paper'
         ORDER BY p.created_at DESC, p.paper_trade_id DESC
         LIMIT 1
        """,
        (int(snapshot_id),),
    ).fetchone()
    return None if row is None else int(row["decision_id"])


def _count(con, sql: str, params: tuple[object, ...] = ()) -> int:
    row = con.execute(sql, params).fetchone()
    return int(row[0] or 0)


def _loads_json(text: str | None) -> object:
    if not text:
        return {}
    return json.loads(text)


def _decision_evidence(con, decision) -> dict[str, object] | None:
    if decision is None:
        return None
    candidate = _loads_json(decision["candidate_json"])
    veto = _loads_json(decision["veto_json"])
    if not isinstance(candidate, dict):
        candidate = {}
    if not isinstance(veto, dict):
        veto = {}
    week_end = candidate.get("week_end") if isinstance(candidate, dict) else None
    signals = []
    if week_end:
        signal_rows = con.execute(
            """
            SELECT w.signal_id, w.snapshot_id, w.strategy_id, w.week_end, w.instrument_id,
                   i.symbol, i.asset_type, w.rank, w.target_weight, w.raw_score, w.adjusted_score,
                   w.vetoed, w.reason_json
              FROM weekly_signal w
              LEFT JOIN instruments i ON i.instrument_id=w.instrument_id
             WHERE w.snapshot_id=?
               AND w.strategy_id=?
               AND w.week_end=?
             ORDER BY w.rank, w.signal_id
            """,
            (str(decision["snapshot_id"]), decision["strategy_name"], week_end),
        ).fetchall()
        for row in signal_rows:
            signals.append(
                {
                    "signal_id": row["signal_id"],
                    "snapshot_id": row["snapshot_id"],
                    "strategy_id": row["strategy_id"],
                    "week_end": row["week_end"],
                    "instrument_id": row["instrument_id"],
                    "symbol": row["symbol"],
                    "asset_type": row["asset_type"],
                    "rank": row["rank"],
                    "target_weight": row["target_weight"],
                    "raw_score": row["raw_score"],
                    "adjusted_score": row["adjusted_score"],
                    "vetoed": bool(row["vetoed"]),
                    "reason": _loads_json(row["reason_json"]),
                }
            )
    return {
        "decision_id": decision["decision_id"],
        "snapshot_id": decision["snapshot_id"],
        "decision_date": decision["decision_date"],
        "account_scope": decision["account_scope"],
        "strategy_name": decision["strategy_name"],
        "approved": bool(decision["approved"]),
        "approver": decision["approver"],
        "approved_at": decision["approved_at"],
        "approval_reason": decision["approval_reason"],
        "candidate": candidate,
        "veto": veto,
        "signals": signals,
    }


def _risk_for_decision(con, decision):
    if decision is None:
        return None
    row = con.execute(
        """
        SELECT *
          FROM risk_check_result
         WHERE decision_id=?
         ORDER BY created_at DESC
         LIMIT 1
        """,
        (str(decision["decision_id"]),),
    ).fetchone()
    if row is not None:
        return row
    try:
        candidate = json.loads(decision["candidate_json"] or "{}")
    except Exception:
        candidate = {}
    risk_check_id = candidate.get("risk_check_id") if isinstance(candidate, dict) else None
    if not risk_check_id:
        return None
    return con.execute(
        """
        SELECT *
          FROM risk_check_result
         WHERE risk_check_id=?
           AND snapshot_id=?
           AND strategy_id=?
         ORDER BY created_at DESC
         LIMIT 1
        """,
        (str(risk_check_id), str(decision["snapshot_id"]), decision["strategy_name"]),
    ).fetchone()


def _decision_trace_evidence(decision) -> dict[str, object]:
    missing: list[str] = []
    try:
        candidate = json.loads(decision["candidate_json"] or "{}")
    except Exception:
        candidate = None
    try:
        veto = json.loads(decision["veto_json"] or "{}")
    except Exception:
        veto = None

    if not isinstance(candidate, dict):
        missing.append("candidate_json")
    else:
        if candidate.get("approval_required") is not True:
            missing.append("approval_required")
        if not candidate.get("risk_status"):
            missing.append("risk_status")
        if not candidate.get("risk_check_id"):
            missing.append("risk_check_id")
        if not candidate.get("week_end"):
            missing.append("week_end")
        if not isinstance(candidate.get("candidates"), list):
            missing.append("candidates")
    if not isinstance(veto, dict):
        missing.append("veto_json")
    elif "vetoed" not in veto:
        missing.append("vetoed")
    return {"complete": not missing, "missing": missing}


def _backtest_performance_evidence(con, snapshot_id: object) -> dict[str, object]:
    row = con.execute(
        """
        SELECT *
          FROM backtest_run
         WHERE snapshot_id=?
           AND status='success'
           AND tax_mode='approx_jp_taxable'
           AND cost_bps IS NOT NULL
           AND slippage_bps IS NOT NULL
         ORDER BY created_at DESC, backtest_id DESC
         LIMIT 1
        """,
        (str(snapshot_id),),
    ).fetchone()
    if row is None:
        return {"complete": False, "failure": "missing_tax_cost_backtest", "metrics": {}}
    metrics = {
        metric["metric_name"]: float(metric["metric_value"])
        for metric in con.execute(
            """
            SELECT metric_name, metric_value
              FROM backtest_metric
             WHERE backtest_id=?
               AND split_name='full'
               AND metric_name IN ('final_equity', 'cagr', 'cost_drag', 'tax_drag')
            """,
            (row["backtest_id"],),
        ).fetchall()
    }
    required = {"final_equity", "cagr", "cost_drag", "tax_drag"}
    missing = sorted(required - set(metrics))
    if missing:
        return {
            "complete": False,
            "failure": "missing_tax_cost_metrics",
            "backtest_id": row["backtest_id"],
            "metrics": metrics,
            "missing_metrics": missing,
        }
    degraded = metrics["final_equity"] <= 0.0 or metrics["cagr"] <= -1.0 or metrics["cost_drag"] < 0.0 or metrics["tax_drag"] < 0.0
    if degraded:
        return {
            "complete": False,
            "failure": "degraded_tax_cost_expectancy",
            "backtest_id": row["backtest_id"],
            "metrics": metrics,
        }
    return {
        "complete": True,
        "failure": None,
        "backtest_id": row["backtest_id"],
        "tax_mode": row["tax_mode"],
        "cost_bps": row["cost_bps"],
        "slippage_bps": row["slippage_bps"],
        "metrics": metrics,
    }


def _fetch_evidence(con, snapshot_row) -> bool:
    source_summary = _json_object(snapshot_row["source_summary_json"])
    latest_fetches = source_summary.get("latest_fetches")
    if isinstance(latest_fetches, list) and latest_fetches:
        return True
    snapshot_date = snapshot_row["snapshot_date"]
    return (
        _count(
            con,
            """
            SELECT COUNT(*)
              FROM data_quality_check
             WHERE check_date=?
               AND check_type IN ('fetch_status', 'fetch_fail', 'fetch_partial')
            """,
            (snapshot_date,),
        )
        > 0
    )


def _paper_gate(con) -> dict[str, object]:
    paper_decisions = con.execute(
        """
        SELECT d.decision_id, d.snapshot_id, d.decision_date, d.account_scope, d.strategy_name,
               d.candidate_json, d.veto_json, d.approved, d.approver, d.approved_at,
               d.approval_reason, d.created_at, s.snapshot_date, s.source_summary_json
          FROM decision_log d
          LEFT JOIN snapshot_registry s ON s.snapshot_id=d.snapshot_id
         WHERE d.account_scope='paper'
         ORDER BY d.decision_date, d.decision_id
        """
    ).fetchall()
    paper_weeks = _count(
        con,
        """
        SELECT COUNT(DISTINCT d.decision_date)
          FROM decision_log d
          JOIN paper_trade_log p ON p.decision_id=d.decision_id
         WHERE d.account_scope='paper'
        """,
    )
    paper_dates = [
        date.fromisoformat(row["decision_date"])
        for row in con.execute(
            """
            SELECT DISTINCT d.decision_date
              FROM decision_log d
              JOIN paper_trade_log p ON p.decision_id=d.decision_id
             WHERE d.account_scope='paper'
               AND d.decision_date IS NOT NULL
             ORDER BY d.decision_date
            """
        ).fetchall()
    ]
    first_paper_date = paper_dates[0].isoformat() if paper_dates else None
    last_paper_date = paper_dates[-1].isoformat() if paper_dates else None
    paper_span_days = (paper_dates[-1] - paper_dates[0]).days if len(paper_dates) >= 2 else 0
    minimum_required_span_days = 49
    preferred_required_span_days = 77
    missing_decision_paper = _count(
        con,
        """
        SELECT COUNT(*)
          FROM decision_log d
         WHERE d.account_scope='paper'
           AND NOT EXISTS (SELECT 1 FROM paper_trade_log p WHERE p.decision_id=d.decision_id)
        """,
    )
    llm_audit_rows = _count(con, "SELECT COUNT(*) FROM llm_audit_log")
    no_trade_rows = _count(con, "SELECT COUNT(*) FROM paper_trade_log WHERE status IN ('vetoed', 'no_candidate', 'zero_weight')")
    tca_rows = _count(
        con,
        """
        SELECT COUNT(*)
          FROM paper_trade_log
         WHERE status IN ('simulated', 'filled')
           AND decision_price IS NOT NULL
           AND simulated_fill_price IS NOT NULL
           AND COALESCE(fill_model, '') <> ''
           AND cost_bps IS NOT NULL
        """,
    )
    event_veto_rows = _count(con, "SELECT COUNT(*) FROM weekly_signal WHERE vetoed=1")
    missing_logs = {
        "snapshot": 0,
        "fetch": 0,
        "validation": 0,
        "feature": 0,
        "backtest": 0,
        "performance": 0,
        "approval": 0,
        "decision_evidence": 0,
        "tca": 0,
        "risk": 0,
        "paper_trade": 0,
        "report": 0,
    }
    performance_failure_rows = 0
    missing_approval_evidence_rows = 0
    missing_decision_evidence_rows = 0
    missing_tca_evidence_rows = 0
    weeks: list[dict[str, object]] = []
    for row in paper_decisions:
        decision_id = int(row["decision_id"])
        snapshot_id = row["snapshot_id"]
        snapshot_date = row["snapshot_date"] or row["decision_date"]
        week_status = {
            "decision_id": decision_id,
            "snapshot_id": snapshot_id,
            "snapshot_date": snapshot_date,
            "fetch": False,
            "validation": False,
            "feature": False,
            "backtest": False,
            "performance": False,
            "performance_failure": None,
            "approval": False,
            "decision_evidence": False,
            "decision_evidence_missing": [],
            "tca": False,
            "risk": False,
            "paper_trade": False,
            "report": False,
            "complete": False,
            "missing": [],
        }
        if snapshot_id is None:
            missing_logs["snapshot"] += 1
            week_status["missing"] = ["snapshot"]
            weeks.append(week_status)
            continue
        fetch_count = 1 if _fetch_evidence(con, row) else 0
        validation_count = _count(con, "SELECT COUNT(*) FROM data_quality_check WHERE check_date=?", (snapshot_date,))
        feature_count = _count(con, "SELECT COUNT(*) FROM feature_weekly WHERE week_end<=?", (snapshot_date,))
        backtest_count = _count(con, "SELECT COUNT(*) FROM backtest_run WHERE snapshot_id=?", (str(snapshot_id),))
        performance = _backtest_performance_evidence(con, snapshot_id)
        decision_trace = _decision_trace_evidence(row)
        risk = _risk_for_decision(con, row)
        risk_count = 1 if risk is not None else 0
        approval_complete = (
            int(row["approved"] or 0) == 1
            and bool(str(row["approver"] or "").strip())
            and bool(str(row["approved_at"] or "").strip())
            and bool(str(row["approval_reason"] or "").strip())
        )
        paper_trade_count = _count(con, "SELECT COUNT(*) FROM paper_trade_log WHERE decision_id=?", (decision_id,))
        executed_trade_count = _count(
            con,
            "SELECT COUNT(*) FROM paper_trade_log WHERE decision_id=? AND status IN ('simulated', 'filled')",
            (decision_id,),
        )
        incomplete_tca_count = _count(
            con,
            """
            SELECT COUNT(*)
              FROM paper_trade_log
             WHERE decision_id=?
               AND status IN ('simulated', 'filled')
               AND (
                 decision_price IS NULL
                 OR simulated_fill_price IS NULL
                 OR COALESCE(fill_model, '') = ''
                 OR cost_bps IS NULL
               )
            """,
            (decision_id,),
        )
        report_count = _count(
            con,
            """
            SELECT COUNT(*)
              FROM llm_audit_log
             WHERE snapshot_id=?
               AND (decision_id IS NULL OR decision_id=?)
            """,
            (str(snapshot_id), decision_id),
        )
        week_status.update(
            {
                "fetch": fetch_count > 0,
                "validation": validation_count > 0,
                "feature": feature_count > 0,
                "backtest": backtest_count > 0,
                "performance": bool(performance["complete"]),
                "performance_failure": performance["failure"],
                "performance_evidence": performance,
                "approval": approval_complete,
                "decision_evidence": bool(decision_trace["complete"]),
                "decision_evidence_missing": decision_trace["missing"],
                "tca": executed_trade_count == 0 or incomplete_tca_count == 0,
                "risk": risk_count > 0,
                "paper_trade": paper_trade_count > 0,
                "report": report_count > 0,
            }
        )
        missing_for_week: list[str] = []
        if fetch_count == 0:
            missing_logs["fetch"] += 1
            missing_for_week.append("fetch")
        if validation_count == 0:
            missing_logs["validation"] += 1
            missing_for_week.append("validation")
        if feature_count == 0:
            missing_logs["feature"] += 1
            missing_for_week.append("feature")
        if backtest_count == 0:
            missing_logs["backtest"] += 1
            missing_for_week.append("backtest")
        if not performance["complete"]:
            missing_logs["performance"] += 1
            performance_failure_rows += 1
            missing_for_week.append("performance")
        if not approval_complete:
            missing_logs["approval"] += 1
            missing_approval_evidence_rows += 1
            missing_for_week.append("approval")
        if not decision_trace["complete"]:
            missing_logs["decision_evidence"] += 1
            missing_decision_evidence_rows += 1
            missing_for_week.append("decision_evidence")
        if incomplete_tca_count:
            missing_logs["tca"] += 1
            missing_tca_evidence_rows += incomplete_tca_count
            missing_for_week.append("tca")
        if risk_count == 0:
            missing_logs["risk"] += 1
            missing_for_week.append("risk")
        if paper_trade_count == 0:
            missing_logs["paper_trade"] += 1
            missing_for_week.append("paper_trade")
        if report_count == 0:
            missing_logs["report"] += 1
            missing_for_week.append("report")
        week_status["missing"] = missing_for_week
        week_status["complete"] = not missing_for_week
        weeks.append(week_status)
    missing_weekly_logs = sum(missing_logs.values())
    gate_failures: list[str] = []
    if paper_weeks < 8:
        gate_failures.append("paper_weeks_lt_8")
    if paper_weeks >= 8 and paper_span_days < minimum_required_span_days:
        gate_failures.append("paper_span_lt_8_weeks")
    if missing_decision_paper:
        gate_failures.append("missing_decision_paper")
    if missing_weekly_logs:
        gate_failures.append("missing_weekly_logs")
    if performance_failure_rows:
        gate_failures.append("missing_or_degraded_tax_cost_performance")
    if missing_approval_evidence_rows:
        gate_failures.append("missing_approval_evidence")
    if missing_decision_evidence_rows:
        gate_failures.append("missing_decision_evidence")
    if tca_rows == 0 or missing_tca_evidence_rows:
        gate_failures.append("missing_tca_evidence")
    if no_trade_rows == 0:
        gate_failures.append("missing_no_trade_evidence")
    if event_veto_rows == 0:
        gate_failures.append("missing_event_veto_evidence")
    if (
        paper_weeks >= 12
        and paper_span_days >= preferred_required_span_days
        and missing_decision_paper == 0
        and missing_weekly_logs == 0
        and performance_failure_rows == 0
        and missing_approval_evidence_rows == 0
        and missing_decision_evidence_rows == 0
        and tca_rows > 0
        and missing_tca_evidence_rows == 0
        and no_trade_rows > 0
        and event_veto_rows > 0
    ):
        status = "preferred_ready"
    elif (
        paper_weeks >= 8
        and paper_span_days >= minimum_required_span_days
        and missing_decision_paper == 0
        and missing_weekly_logs == 0
        and performance_failure_rows == 0
        and missing_approval_evidence_rows == 0
        and missing_decision_evidence_rows == 0
        and tca_rows > 0
        and missing_tca_evidence_rows == 0
        and no_trade_rows > 0
        and event_veto_rows > 0
    ):
        status = "minimum_ready"
    else:
        status = "not_ready"
    return {
        "status": status,
        "paper_weeks": paper_weeks,
        "first_paper_date": first_paper_date,
        "last_paper_date": last_paper_date,
        "paper_span_days": paper_span_days,
        "minimum_required_span_days": minimum_required_span_days,
        "preferred_required_span_days": preferred_required_span_days,
        "gate_failures": gate_failures,
        "missing_decision_paper": missing_decision_paper,
        "llm_audit_rows": llm_audit_rows,
        "no_trade_rows": no_trade_rows,
        "tca_rows": tca_rows,
        "event_veto_rows": event_veto_rows,
        "missing_weekly_logs": missing_weekly_logs,
        "missing_logs": missing_logs,
        "performance_failure_rows": performance_failure_rows,
        "missing_approval_evidence_rows": missing_approval_evidence_rows,
        "missing_decision_evidence_rows": missing_decision_evidence_rows,
        "missing_tca_evidence_rows": missing_tca_evidence_rows,
        "weeks": weeks,
    }


def build_audit_report(con, options: AuditOptions) -> dict[str, object]:
    snapshot = _snapshot(con, options.snapshot_id)
    if options.decision_id is not None:
        decision_id = options.decision_id
    elif options.paper_latest:
        decision_id = _latest_paper_decision(con, options.snapshot_id) or _latest_decision(con, options.snapshot_id)
    else:
        decision_id = _latest_decision(con, options.snapshot_id)
    snapshot_date = snapshot["snapshot_date"]
    feature_hash_audit = _feature_hash_audit(con, snapshot)
    source_summary = _json_object(snapshot["source_summary_json"])
    event_state = _json_object(snapshot["event_state_json"])
    fetch_failures, fetch_partials = _snapshot_fetch_quality(con, source_summary)
    quality_blockers = _count(
        con,
        "SELECT COUNT(*) FROM data_quality_check WHERE check_date=? AND severity='blocker' AND status!='pass'",
        (snapshot_date,),
    )
    decision = None if decision_id is None else con.execute("SELECT * FROM decision_log WHERE decision_id=?", (decision_id,)).fetchone()
    risk = _risk_for_decision(con, decision)
    decision_evidence = _decision_evidence(con, decision)
    paper_count = 0 if decision_id is None else _count(con, "SELECT COUNT(*) FROM paper_trade_log WHERE decision_id=?", (decision_id,))
    paper_rows = [] if decision_id is None else con.execute(
        """
        SELECT p.paper_trade_id, p.snapshot_id, p.instrument_id, i.symbol, i.asset_type, p.side, p.quantity,
               p.decision_price, p.simulated_fill_price, p.fill_model, p.cost_bps,
               p.target_weight, p.notional, p.estimated_cost, p.slippage, p.status
          FROM paper_trade_log p
          LEFT JOIN instruments i ON i.instrument_id=p.instrument_id
         WHERE p.decision_id=?
         ORDER BY p.paper_trade_id
        """,
        (decision_id,),
    ).fetchall()
    tax_lot_count = 0 if decision_id is None else _count(
        con,
        """
        SELECT COUNT(*)
          FROM tax_lot_log t
          JOIN paper_trade_log p ON p.paper_trade_id=t.source_order_id
         WHERE p.decision_id=?
        """,
        (decision_id,),
    )
    paper_gate = _paper_gate(con)
    lines = [
        "# RenCrow Investment Audit Report",
        "",
        "## Snapshot",
        "",
        f"- snapshot_id: {options.snapshot_id}",
        f"- snapshot_date: {snapshot_date}",
        f"- status: {snapshot['status']}",
        f"- db_hash: {snapshot['db_hash'] or ''}",
        f"- features_hash: {snapshot['features_hash'] or ''}",
        f"- current_features_hash: {feature_hash_audit['current']}",
        f"- features_hash_match: {str(feature_hash_audit['match']).lower()}",
        "",
        f"- missing_rate: {snapshot['missing_rate'] if snapshot['missing_rate'] is not None else ''}",
    ]
    lines.extend(_snapshot_source_lines(source_summary))
    lines.extend(_snapshot_event_lines(event_state))
    lines.extend([
        "",
        "## Data Quality",
        "",
        f"- fetch_failures_total: {fetch_failures}",
        f"- fetch_partials_total: {fetch_partials}",
        f"- quality_blockers_on_snapshot_date: {quality_blockers}",
        "",
        "## Risk",
        "",
    ])
    if risk is None:
        lines.append("- risk_check: not_found")
    else:
        detail = json.loads(risk["detail_json"] or "{}")
        lines.extend(
            [
                f"- risk_check_id: {risk['risk_check_id']}",
                f"- status: {risk['status']}",
                f"- max_dd_check: {risk['max_dd_check']}",
                f"- weekly_loss_check: {risk['weekly_loss_check']}",
                f"- volatility_check: {risk['volatility_check']}",
                f"- event_check: {risk['event_check']}",
                f"- quality_blockers: {detail.get('quality_blockers', '')}",
                f"- event_blockers: {detail.get('event_blockers', '')}",
            ]
        )
    lines.extend(["", "## Decision", ""])
    if decision is None:
        lines.append("- decision: not_found")
    else:
        candidate = decision_evidence["candidate"] if decision_evidence else {}
        veto = decision_evidence["veto"] if decision_evidence else {}
        lines.extend(
            [
                f"- decision_id: {decision['decision_id']}",
                f"- account_scope: {decision['account_scope']}",
                f"- approved: {decision['approved']}",
                f"- approver: {decision['approver'] or ''}",
                f"- approved_at: {decision['approved_at'] or ''}",
                f"- approval_reason: {decision['approval_reason'] or ''}",
                f"- approval_required: {candidate.get('approval_required')}",
                f"- vetoed: {veto.get('vetoed')}",
                f"- risk_status: {candidate.get('risk_status')}",
                f"- paper_trade_rows: {paper_count}",
                f"- tax_lot_rows: {tax_lot_count}",
                f"- candidate_json: {json.dumps(candidate, ensure_ascii=False, sort_keys=True)}",
                f"- veto_json: {json.dumps(veto, ensure_ascii=False, sort_keys=True)}",
            ]
        )
        for item in candidate.get("candidates", []):
            lines.append(
                f"- candidate: {item.get('symbol')} asset_type={item.get('asset_type', '')} "
                f"weight={item.get('target_weight')} score={item.get('adjusted_score')}"
            )
        lines.extend(
            [
                "",
                "### Decision Signal Evidence",
                "",
                "| signal_id | symbol | asset_type | rank | target_weight | adjusted_score | vetoed | reason |",
                "|---|---|---|---:|---:|---:|---|---|",
            ]
        )
        signals = decision_evidence["signals"] if decision_evidence else []
        if not signals:
            lines.append("| - | - | - | - | - | - | - | not_found |")
        for signal in signals:
            reason = json.dumps(signal["reason"], ensure_ascii=False, sort_keys=True)
            lines.append(
                "| {signal_id} | {symbol} | {asset_type} | {rank} | {target_weight} | {adjusted_score} | {vetoed} | {reason} |".format(
                    signal_id=signal["signal_id"],
                    symbol=signal["symbol"] or "",
                    asset_type=signal["asset_type"] or "",
                    rank=signal["rank"] if signal["rank"] is not None else "",
                    target_weight=signal["target_weight"] if signal["target_weight"] is not None else "",
                    adjusted_score=signal["adjusted_score"] if signal["adjusted_score"] is not None else "",
                    vetoed=str(signal["vetoed"]).lower(),
                    reason=reason,
                )
            )
    lines.extend(
        [
            "",
            "## Paper Trades",
            "",
            "| paper_trade_id | snapshot_id | symbol | asset_type | side | quantity | decision_price | simulated_fill_price | fill_model | cost_bps | target_weight | notional | estimated_cost | slippage | status |",
            "|---:|---:|---|---|---|---:|---:|---:|---|---:|---:|---:|---:|---:|---|",
        ]
    )
    if not paper_rows:
        lines.append("| - | - | - | - | - | - | - | - | - | - | - | - | - | - | not_found |")
    for row in paper_rows:
        lines.append(
            "| {paper_trade_id} | {snapshot_id} | {symbol} | {asset_type} | {side} | {quantity} | {decision_price} | {simulated_fill_price} | {fill_model} | {cost_bps} | {target_weight} | {notional} | {estimated_cost} | {slippage} | {status} |".format(
                paper_trade_id=row["paper_trade_id"],
                snapshot_id=row["snapshot_id"] if row["snapshot_id"] is not None else "",
                symbol=row["symbol"] or "",
                asset_type=row["asset_type"] or "",
                side=row["side"] or "",
                quantity=row["quantity"] if row["quantity"] is not None else "",
                decision_price=row["decision_price"] if row["decision_price"] is not None else "",
                simulated_fill_price=row["simulated_fill_price"] if row["simulated_fill_price"] is not None else "",
                fill_model=row["fill_model"] or "",
                cost_bps=row["cost_bps"] if row["cost_bps"] is not None else "",
                target_weight=row["target_weight"] if row["target_weight"] is not None else "",
                notional=row["notional"] if row["notional"] is not None else "",
                estimated_cost=row["estimated_cost"] if row["estimated_cost"] is not None else "",
                slippage=row["slippage"] if row["slippage"] is not None else "",
                status=row["status"] or "",
            )
        )
    lines.extend(
        [
            "",
            "## Paper Operation Gate",
            "",
            f"- status: {paper_gate['status']}",
            f"- paper_weeks: {paper_gate['paper_weeks']}",
            f"- first_paper_date: {paper_gate['first_paper_date'] or ''}",
            f"- last_paper_date: {paper_gate['last_paper_date'] or ''}",
            f"- paper_span_days: {paper_gate['paper_span_days']}",
            f"- minimum_required_span_days: {paper_gate['minimum_required_span_days']}",
            f"- preferred_required_span_days: {paper_gate['preferred_required_span_days']}",
            f"- gate_failures: {json.dumps(paper_gate['gate_failures'], ensure_ascii=False)}",
            f"- missing_decision_paper: {paper_gate['missing_decision_paper']}",
            f"- llm_audit_rows: {paper_gate['llm_audit_rows']}",
            f"- no_trade_rows: {paper_gate['no_trade_rows']}",
            f"- tca_rows: {paper_gate['tca_rows']}",
            f"- event_veto_rows: {paper_gate['event_veto_rows']}",
            f"- missing_weekly_logs: {paper_gate['missing_weekly_logs']}",
            f"- performance_failure_rows: {paper_gate['performance_failure_rows']}",
            f"- missing_approval_evidence_rows: {paper_gate['missing_approval_evidence_rows']}",
            f"- missing_decision_evidence_rows: {paper_gate['missing_decision_evidence_rows']}",
            f"- missing_tca_evidence_rows: {paper_gate['missing_tca_evidence_rows']}",
            f"- missing_logs: {json.dumps(paper_gate['missing_logs'], ensure_ascii=False, sort_keys=True)}",
            "",
            "### Weekly Ledger",
            "",
            "| snapshot_date | decision_id | snapshot_id | complete | missing |",
            "|---|---:|---:|---|---|",
        ]
    )
    for week in paper_gate["weeks"]:
        missing = ", ".join(str(item) for item in week["missing"]) or "-"
        lines.append(
            f"| {week['snapshot_date']} | {week['decision_id']} | {week['snapshot_id']} | {str(week['complete']).lower()} | {missing} |"
        )
    lines.append("")

    output_dir = options.output_dir or Path("rencrow-data/reports")
    output_dir.mkdir(parents=True, exist_ok=True)
    output_path = output_dir / f"audit_snapshot_{options.snapshot_id}.md"
    output_path.write_text("\n".join(lines), encoding="utf-8")
    return {
        "snapshot_id": options.snapshot_id,
        "snapshot_date": snapshot_date,
        "decision_id": decision_id,
        "output_path": str(output_path),
        "fetch_failures_total": fetch_failures,
        "fetch_partials_total": fetch_partials,
        "feature_hash_audit": feature_hash_audit,
        "quality_blockers": quality_blockers,
        "risk_status": None if risk is None else risk["status"],
        "paper_latest": options.paper_latest,
        "decision_evidence": decision_evidence,
        "paper_trades": [dict(row) for row in paper_rows],
        "paper_gate": paper_gate,
    }
