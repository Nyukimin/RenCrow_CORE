from __future__ import annotations

import json
from pathlib import Path
import math
from datetime import date, timedelta

from . import db
from .timeutil import parse_date, utcnow_iso


EVENT_SEVERITY_BY_IMPORTANCE = {
    "critical": 1.0,
    "high": 0.7,
    "medium": 0.4,
    "low": 0.2,
}
DEFAULT_EVENT_SEVERITY_BY_CATEGORY = {
    "FOMC": 1.0,
    "BOJ": 1.0,
    "CPI": 0.7,
    "NFP": 0.7,
    "EMPLOYMENT": 0.7,
}


def event_severity(category: str | None, importance: str | None) -> float:
    importance_key = str(importance or "").strip().lower()
    if importance_key in EVENT_SEVERITY_BY_IMPORTANCE:
        return EVENT_SEVERITY_BY_IMPORTANCE[importance_key]
    return DEFAULT_EVENT_SEVERITY_BY_CATEGORY.get(str(category or "").strip().upper(), 0.4)


def _latest_fetch_statuses(con, as_of: date | None = None):
    where = ""
    params: list[object] = []
    if as_of is not None:
        where = "WHERE requested_at<=?"
        params.append(f"{as_of.isoformat()}T23:59:59")
    return con.execute(
        f"""
        SELECT f.source_name, f.status, f.error_message, f.fetch_id
        FROM source_fetch_log f
        JOIN (
          SELECT source_name, MAX(fetch_id) AS fetch_id
          FROM source_fetch_log
          {where}
          GROUP BY source_name
        ) latest
          ON latest.source_name=f.source_name AND latest.fetch_id=f.fetch_id
        """,
        params,
    ).fetchall()


def _active_source_names() -> set[str]:
    root = Path(__file__).resolve().parents[2]
    names: set[str] = set()
    for rel in ("config/instruments.yml", "config/sources.yml", "config/calendars.yml"):
        path = root / rel
        if not path.exists():
            continue
        try:
            data = json.loads(path.read_text(encoding="utf-8"))
        except Exception:
            continue
        if isinstance(data, dict):
            for key in ("instruments", "macro_sources", "calendar_sources"):
                items = data.get(key) or []
                for item in items:
                    if isinstance(item, dict):
                        source_name = item.get("source_name")
                        if source_name:
                            names.add(str(source_name))
    return names


def _calendar_event_key(reason: str, week_end: str, event: dict) -> tuple[str, str, str, str, str, str]:
    return (
        reason,
        week_end,
        str(event.get("event_date") or ""),
        str(event.get("category") or ""),
        str(event.get("event_name") or ""),
        str(event.get("source_name") or ""),
    )


def _existing_calendar_event_keys(con) -> set[tuple[str, str, str, str, str, str]]:
    rows = con.execute(
        """
        SELECT reason, context_json
          FROM event_log
         WHERE scope='macro'
           AND reason LIKE 'calendar_%'
        """
    ).fetchall()
    keys: set[tuple[str, str, str, str, str, str]] = set()
    for row in rows:
        try:
            ctx = json.loads(row["context_json"] or "{}")
        except Exception:
            continue
        week_end = ctx.get("week_end")
        event = ctx.get("event")
        if not week_end or not isinstance(event, dict):
            continue
        keys.add(_calendar_event_key(row["reason"], str(week_end), event))
    return keys


def _event_context_date(row) -> str | None:
    try:
        ctx = json.loads(row["context_json"] or "{}")
    except Exception:
        ctx = {}
    if isinstance(ctx, dict):
        for key in ("week_end", "date", "event_ts"):
            value = ctx.get(key)
            if value:
                return str(value)[:10]
        event = ctx.get("event")
        if isinstance(event, dict):
            for key in ("event_date", "week_end", "date"):
                value = event.get(key)
                if value:
                    return str(value)[:10]
    event_ts = row["event_ts"]
    return str(event_ts)[:10] if event_ts else None


def event_state_summary(con, as_of: date | None = None) -> dict[str, object]:
    rows = con.execute(
        """
        SELECT event_id, event_ts, scope, level, reason, event_risk_score, context_json
          FROM event_log
         WHERE resolved_at IS NULL
         ORDER BY event_id
        """
    ).fetchall()
    as_of_text = None if as_of is None else as_of.isoformat()
    scoped = []
    for row in rows:
        event_date = _event_context_date(row)
        if as_of_text is None or (event_date is not None and event_date <= as_of_text):
            scoped.append((row, event_date))
    counts: dict[tuple[str, str], int] = {}
    latest: list[dict[str, object]] = []
    max_risk = 0.0
    for row, event_date in scoped:
        key = (str(row["level"]), str(row["reason"]))
        counts[key] = counts.get(key, 0) + 1
        if row["event_risk_score"] is not None:
            max_risk = max(max_risk, float(row["event_risk_score"]))
        latest.append(
            {
                "event_id": row["event_id"],
                "event_date": event_date,
                "scope": row["scope"],
                "level": row["level"],
                "reason": row["reason"],
                "event_risk_score": row["event_risk_score"],
            }
        )
    latest.sort(key=lambda item: (str(item["event_date"]), int(item["event_id"])), reverse=True)
    return {
        "as_of": as_of_text,
        "open_event_count": len(scoped),
        "max_open_event_risk_score": max_risk,
        "open_events": [
            {"level": level, "reason": reason, "n": n}
            for (level, reason), n in sorted(counts.items(), key=lambda item: (item[0][0], item[0][1]))
        ],
        "latest_open_events": latest[:10],
    }


def detect_events(con, stale_hours: int = 48, lookback_days: int = 2, lookahead_days: int = 2, week_end: date | None = None) -> int:
    count = 0
    active_sources = _active_source_names()
    latest_fetches = _latest_fetch_statuses(con, as_of=week_end)
    current_bad = []
    for row in latest_fetches:
        if row["source_name"] in active_sources and row["status"] in ("fail", "partial"):
            current_bad.append(row)
    open_fetch_events = con.execute(
        "SELECT event_id, context_json FROM event_log WHERE level='stop' AND reason='source_fetch_unresolved' AND resolved_at IS NULL"
    ).fetchall()
    open_by_source = {}
    for row in open_fetch_events:
        try:
            ctx = json.loads(row["context_json"] or "{}")
        except Exception:
            continue
        source_name = ctx.get("source_name")
        if source_name:
            open_by_source.setdefault(source_name, []).append(int(row["event_id"]))
    current_bad_sources = {row["source_name"] for row in current_bad}
    for source_name, event_ids in open_by_source.items():
        if source_name not in current_bad_sources:
            resolved_at = utcnow_iso()
            resolution_note = json.dumps(
                {
                    "auto_resolution": True,
                    "reason": "data_recovered",
                    "source_name": source_name,
                    "resolved_at": resolved_at,
                },
                ensure_ascii=False,
                sort_keys=True,
            )
            con.execute(
                "UPDATE event_log SET resolved_at=?, resolution_note=? WHERE event_id IN (%s)" % ",".join("?" for _ in event_ids),
                [resolved_at, resolution_note, *event_ids],
            )
            count += len(event_ids)
    for row in current_bad:
        if row["source_name"] in open_by_source:
            continue
        db.log_event(
            con,
            "system",
            "stop",
            "source_fetch_unresolved",
            event_risk_score=1.0,
            context_json=json.dumps(dict(row), ensure_ascii=False),
        )
        count += 1

    existing_calendar_keys = _existing_calendar_event_keys(con)
    if week_end is None:
        weeks = con.execute("SELECT DISTINCT week_end FROM feature_weekly ORDER BY week_end").fetchall()
    else:
        weeks = con.execute("SELECT DISTINCT week_end FROM feature_weekly WHERE week_end=? ORDER BY week_end", (week_end.isoformat(),)).fetchall()
    for wrow in weeks:
        week_end = parse_date(wrow["week_end"])
        start = (week_end - timedelta(days=lookback_days)).isoformat()
        end = (week_end + timedelta(days=lookahead_days)).isoformat()
        events = con.execute(
            """
            SELECT event_date, category, event_name, source_name, importance
              FROM economic_calendar
             WHERE event_date BETWEEN ? AND ?
            """,
            (start, end),
        ).fetchall()
        max_score = 0.0
        for ev in events:
            score = event_severity(ev["category"], ev["importance"])
            level = "warn" if score < 0.9 else "stop"
            reason = f"calendar_{ev['category'].lower()}"
            event_context = dict(ev)
            event_context["severity_score"] = score
            calendar_key = _calendar_event_key(reason, week_end.isoformat(), event_context)
            if calendar_key not in existing_calendar_keys:
                db.log_event(
                    con,
                    "macro",
                    level,
                    reason,
                    event_risk_score=score,
                    context_json=json.dumps({"week_end": week_end.isoformat(), "event": event_context}, ensure_ascii=False),
                )
                existing_calendar_keys.add(calendar_key)
                count += 1
            max_score = max(max_score, score)
        if max_score:
            con.execute(
                "UPDATE feature_weekly SET event_risk_score=max(COALESCE(event_risk_score, 0), ?) WHERE week_end=?",
                (max_score, week_end.isoformat()),
            )

    price_query = """
        SELECT instrument_id, trade_date, adj_close, volume
        FROM price_raw
        {where}
        ORDER BY instrument_id, trade_date
        """
    price_params: list[object] = []
    price_where = ""
    if week_end is not None:
        price_where = "WHERE trade_date<=?"
        price_params.append(week_end.isoformat())
    price_rows = con.execute(price_query.format(where=price_where), price_params).fetchall()
    by_inst = {}
    for row in price_rows:
        by_inst.setdefault(int(row["instrument_id"]), []).append(row)
    for iid, rows in by_inst.items():
        rets = []
        vols = []
        for idx in range(1, len(rows)):
            prev = rows[idx - 1]["adj_close"]
            curr = rows[idx]["adj_close"]
            if prev in (None, 0) or curr is None:
                continue
            ret = float(curr) / float(prev) - 1.0
            rets.append(ret)
            vols.append(float(rows[idx]["volume"] or 0.0))
            if len(rets) >= 20:
                window = rets[-20:]
                mean = sum(window) / len(window)
                var = sum((x - mean) ** 2 for x in window) / max(1, len(window) - 1)
                sd = math.sqrt(var)
                z = 0.0 if sd == 0 else (ret - mean) / sd
                if abs(z) >= 2.5 or abs(ret) >= 0.05:
                    score = 1.0 if abs(ret) >= 0.05 else 0.7
                    db.log_event(
                        con,
                        "market",
                        "warn",
                        "return_spike",
                        value=ret,
                        event_risk_score=score,
                        context_json=json.dumps({"instrument_id": iid, "date": rows[idx]["trade_date"], "z": z}, ensure_ascii=False),
                    )
                count += 1
        if len(vols) >= 20 and vols[-1] > 2.0 * (sum(vols[-20:]) / 20):
            db.log_event(
                con,
                "market",
                "warn",
                "volume_spike",
                value=vols[-1],
                event_risk_score=0.5,
                context_json=json.dumps({"instrument_id": iid, "event_ts": utcnow_iso()}, ensure_ascii=False),
            )
            count += 1
    con.commit()
    return count
