from __future__ import annotations

import gzip
import json
import shutil
import sqlite3
import tempfile
from pathlib import Path

from .hashing import stable_db_hash, stable_feature_hash
from .timeutil import utcnow_iso


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
                for item in data.get(key) or []:
                    if isinstance(item, dict) and item.get("source_name"):
                        names.add(str(item["source_name"]))
    return names


def _event_row_date(row: sqlite3.Row) -> str | None:
    try:
        ctx = json.loads(row["context_json"] or "{}")
    except Exception:
        ctx = {}
    if isinstance(ctx, dict):
        event_date = _context_date(ctx)
        if event_date is not None:
            return event_date
    event_ts = row["event_ts"]
    if event_ts:
        return str(event_ts)[:10]
    return None


def _event_rows_for_snapshot(con: sqlite3.Connection, snapshot_date: str, unresolved_only: bool = False) -> list[sqlite3.Row]:
    where = "WHERE resolved_at IS NULL" if unresolved_only else ""
    rows = con.execute(
        f"""
        SELECT event_id, event_ts, scope, level, reason, value, event_risk_score, context_json, resolved_at
          FROM event_log
          {where}
         ORDER BY event_id
        """
    ).fetchall()
    return [row for row in rows if (event_date := _event_row_date(row)) is not None and event_date <= snapshot_date]


def _event_state(con: sqlite3.Connection, snapshot_date: str) -> dict:
    open_rows = _event_rows_for_snapshot(con, snapshot_date, unresolved_only=True)
    counts: dict[tuple[str, str], int] = {}
    latest: list[dict[str, object]] = []
    max_risk = 0.0
    for row in open_rows:
        key = (row["level"], row["reason"])
        counts[key] = counts.get(key, 0) + 1
        if row["event_risk_score"] is not None:
            max_risk = max(max_risk, float(row["event_risk_score"]))
        latest.append(
            {
                "event_id": row["event_id"],
                "event_date": _event_row_date(row),
                "scope": row["scope"],
                "level": row["level"],
                "reason": row["reason"],
                "event_risk_score": row["event_risk_score"],
            }
        )
    open_events = [
        {"level": level, "reason": reason, "n": n}
        for (level, reason), n in sorted(counts.items(), key=lambda item: (item[0][0], item[0][1]))
    ]
    latest.sort(key=lambda item: (str(item["event_date"]), int(item["event_id"])), reverse=True)
    return {
        "as_of": snapshot_date,
        "open_event_count": len(open_rows),
        "max_open_event_risk_score": max_risk,
        "open_events": open_events,
        "latest_open_events": latest[:10],
    }


def _latest_missing_rate(con: sqlite3.Connection, snapshot_date: str) -> float:
    latest = con.execute(
        """
        SELECT MAX(check_date) AS check_date
          FROM data_quality_check
         WHERE check_type='missing'
           AND metric_value IS NOT NULL
           AND check_date<=?
        """,
        (snapshot_date,),
    ).fetchone()
    if latest is None or latest["check_date"] is None:
        return 0.0
    row = con.execute(
        """
        SELECT MAX(metric_value) AS missing_rate
          FROM data_quality_check
         WHERE check_type='missing'
           AND metric_value IS NOT NULL
           AND check_date=?
        """,
        (latest["check_date"],),
    ).fetchone()
    if row is None or row["missing_rate"] is None:
        return 0.0
    return float(row["missing_rate"])


def _source_summary(con: sqlite3.Connection, snapshot_date: str) -> dict[str, object]:
    end_ts = f"{snapshot_date}T23:59:59"
    latest_fetches = _latest_fetches_for_snapshot(con, snapshot_date)
    scoped_sources = {row["source_name"] for row in latest_fetches if row["requested_at"] and row["requested_at"] <= end_ts}
    fallback_sources = {row["source_name"] for row in latest_fetches} - scoped_sources
    counts_rows: list[sqlite3.Row] = []
    if scoped_sources:
        counts_rows.extend(
            con.execute(
                """
                SELECT source_name, status, COUNT(*) AS n
                  FROM source_fetch_log
                 WHERE requested_at<=?
                   AND source_name IN (%s)
                 GROUP BY source_name, status
                """
                % ",".join("?" for _ in scoped_sources),
                [end_ts, *sorted(scoped_sources)],
            ).fetchall()
        )
    if fallback_sources:
        counts_rows.extend(
            con.execute(
                """
                SELECT source_name, status, COUNT(*) AS n
                  FROM source_fetch_log
                 WHERE source_name IN (%s)
                 GROUP BY source_name, status
                """
                % ",".join("?" for _ in fallback_sources),
                sorted(fallback_sources),
            ).fetchall()
        )
    counts = [
        dict(row)
        for row in sorted(counts_rows, key=lambda item: (item["source_name"], item["status"]))
    ]
    return {
        "as_of": snapshot_date,
        "counts": counts,
        "latest_fetches": [dict(row) for row in latest_fetches],
    }


def _latest_fetches_for_snapshot(con: sqlite3.Connection, snapshot_date: str) -> list[sqlite3.Row]:
    end_ts = f"{snapshot_date}T23:59:59"
    return con.execute(
        """
        WITH latest_before AS (
          SELECT source_name, MAX(fetch_id) AS fetch_id
            FROM source_fetch_log
           WHERE requested_at<=?
           GROUP BY source_name
        ),
        latest_any AS (
          SELECT source_name, MAX(fetch_id) AS fetch_id
            FROM source_fetch_log
           GROUP BY source_name
        ),
        chosen AS (
          SELECT a.source_name, COALESCE(b.fetch_id, a.fetch_id) AS fetch_id
            FROM latest_any a
            LEFT JOIN latest_before b ON b.source_name=a.source_name
        )
        SELECT f.source_name, f.status, f.fetch_id, f.endpoint, f.requested_at,
               f.finished_at, f.http_status, f.rows_fetched, f.checksum,
               f.retry_count, f.error_message, f.raw_cache_path, f.usage_terms
          FROM source_fetch_log f
          JOIN chosen
            ON chosen.source_name=f.source_name AND chosen.fetch_id=f.fetch_id
         ORDER BY f.source_name
        """,
        (end_ts,),
    ).fetchall()


def _context_date(ctx: dict) -> str | None:
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
    return None


def _event_ids_for_snapshot(con: sqlite3.Connection, snapshot_date: str) -> list[int]:
    rows = con.execute("SELECT event_id, event_ts, context_json FROM event_log WHERE snapshot_id IS NULL ORDER BY event_id").fetchall()
    event_ids: list[int] = []
    for row in rows:
        event_date = _event_row_date(row)
        if event_date is not None and event_date <= snapshot_date:
            event_ids.append(int(row["event_id"]))
    return event_ids


def _attach_snapshot_references(con: sqlite3.Connection, snapshot_id: str, snapshot_date: str) -> None:
    con.execute(
        """
        UPDATE feature_weekly
           SET source_snapshot_id=?
         WHERE week_end<=?
           AND (source_snapshot_id IS NULL OR source_snapshot_id=?)
        """,
        (snapshot_id, snapshot_date, snapshot_id),
    )
    event_ids = _event_ids_for_snapshot(con, snapshot_date)
    if event_ids:
        con.execute(
            "UPDATE event_log SET snapshot_id=? WHERE event_id IN (%s)" % ",".join("?" for _ in event_ids),
            [snapshot_id, *event_ids],
        )


def precheck_status(con: sqlite3.Connection, snapshot_date: str) -> tuple[str, str]:
    latest_fetches = _latest_fetches_for_snapshot(con, snapshot_date)
    active_sources = _active_source_names() | {str(row["source_name"]) for row in latest_fetches if row["source_name"]}
    bad_fetch = sum(1 for row in latest_fetches if row["source_name"] in active_sources and row["status"] in ("fail", "partial"))
    stop_events = 0
    if active_sources:
        for row in _event_rows_for_snapshot(con, snapshot_date, unresolved_only=True):
            if row["level"] != "stop" or row["reason"] != "source_fetch_unresolved":
                continue
            try:
                ctx = json.loads(row["context_json"] or "{}")
            except Exception:
                continue
            if ctx.get("source_name") in active_sources:
                stop_events += 1
    high_risk = con.execute(
        "SELECT COUNT(*) FROM feature_weekly WHERE week_end<=? AND COALESCE(event_risk_score, 0) >= 0.9",
        (snapshot_date,),
    ).fetchone()[0]
    if bad_fetch or stop_events or high_risk:
        return "blocked", f"bad_fetch={bad_fetch}; stop_events={stop_events}; high_risk_features={high_risk}"
    return "success", "precheck passed"


def make_snapshot(con: sqlite3.Connection, db_path: str | Path, output_dir: str | Path, snapshot_date: str) -> dict:
    out_dir = Path(output_dir)
    out_dir.mkdir(parents=True, exist_ok=True)
    snapshot_path = out_dir / f"snapshot_{snapshot_date.replace('-', '')}.sqlite.gz"
    status, notes = precheck_status(con, snapshot_date)
    data_range = con.execute("SELECT MIN(trade_date), MAX(trade_date) FROM price_raw").fetchone()
    source_summary = _source_summary(con, snapshot_date)
    missing_rate = _latest_missing_rate(con, snapshot_date)
    event_state = _event_state(con, snapshot_date)
    created_at = utcnow_iso()

    con.execute(
        """
        INSERT INTO snapshot_registry(
          snapshot_date, snapshot_path, db_hash, features_hash, source_summary_json,
          data_start_date, data_end_date, missing_rate, event_state_json, status, notes, created_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(snapshot_date) DO UPDATE SET
          snapshot_path=excluded.snapshot_path,
          db_hash=excluded.db_hash,
          features_hash=excluded.features_hash,
          source_summary_json=excluded.source_summary_json,
          data_start_date=excluded.data_start_date,
          data_end_date=excluded.data_end_date,
          missing_rate=excluded.missing_rate,
          event_state_json=excluded.event_state_json,
          status=excluded.status,
          notes=excluded.notes,
          created_at=excluded.created_at
        """,
        (
            snapshot_date,
            str(snapshot_path),
            "",
            "",
            json.dumps(source_summary, ensure_ascii=False, sort_keys=True),
            data_range[0],
            data_range[1],
            missing_rate,
            json.dumps(event_state, ensure_ascii=False),
            status,
            notes,
            created_at,
        ),
    )
    row = con.execute("SELECT snapshot_id FROM snapshot_registry WHERE snapshot_date=?", (snapshot_date,)).fetchone()
    snapshot_id = None if row is None else str(row["snapshot_id"])
    if snapshot_id is not None:
        _attach_snapshot_references(con, snapshot_id, snapshot_date)
    db_hash = stable_db_hash(con)
    features_hash = stable_feature_hash(con, snapshot_date)
    con.execute(
        """
        UPDATE snapshot_registry
           SET db_hash=?,
               features_hash=?
         WHERE snapshot_date=?
        """,
        (db_hash, features_hash, snapshot_date),
    )
    con.commit()

    with tempfile.NamedTemporaryFile(suffix=".sqlite", delete=False) as tmp:
        tmp_path = Path(tmp.name)
    try:
        src = sqlite3.connect(db_path)
        dst = sqlite3.connect(tmp_path)
        src.backup(dst)
        dst.close()
        src.close()
        with tmp_path.open("rb") as f_in, gzip.open(snapshot_path, "wb") as f_out:
            shutil.copyfileobj(f_in, f_out)
    finally:
        tmp_path.unlink(missing_ok=True)

    return {
        "snapshot_id": snapshot_id,
        "path": str(snapshot_path),
        "status": status,
        "db_hash": db_hash,
        "features_hash": features_hash,
        "notes": notes,
    }
