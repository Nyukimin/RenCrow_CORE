from __future__ import annotations

import csv
import json
from datetime import date, timedelta
from pathlib import Path
from typing import Any

from . import db
from .hashing import sha256_bytes
from .providers import fetch_fred_csv, fetch_yahoo_history, parse_float
from .timeutil import utcnow_iso


def _read_csv(path: str | Path) -> list[dict[str, str]]:
    with Path(path).open(newline="", encoding="utf-8") as f:
        return list(csv.DictReader(f))


def _stable_json_bytes(value: object) -> bytes:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode("utf-8")


def _usage_terms(source: dict[str, Any], provider: str | None = None, mode: str = "fixture") -> str:
    if source.get("usage_terms"):
        return str(source["usage_terms"])
    if mode == "fixture":
        return "local_fixture; internal_research_only; no_redistribution"
    if provider == "fred":
        return "fred_public_data; internal_research_only; cite_source; no_warranty"
    if provider == "yahoo":
        return "yahoo_finance; internal_research_only; no_redistribution; respect_provider_terms"
    return f"{provider or 'unknown_provider'}; internal_research_only; no_redistribution"


def latest_macro_date(con, series_code: str, source_name: str | None = None) -> date | None:
    if source_name is None:
        row = con.execute("SELECT MAX(obs_date) AS d FROM macro_series WHERE series_code=?", (series_code,)).fetchone()
    else:
        row = con.execute(
            "SELECT MAX(obs_date) AS d FROM macro_series WHERE series_code=? AND source_name=?",
            (series_code, source_name),
        ).fetchone()
    if not row or row["d"] is None:
        return None
    return date.fromisoformat(row["d"])


def ingest_macro_csv(con, source: dict[str, Any], data_root: str | Path) -> tuple[int, str]:
    return ingest_macro_source(con, source, data_root, mode="fixture")


def ingest_macro_source(
    con,
    source: dict[str, Any],
    data_root: str | Path,
    *,
    mode: str = "fixture",
    start_date: str | None = None,
    end_date: str | None = None,
    lookback_days: int = 30,
) -> tuple[int, str]:
    fixture = source.get("fixture")
    path = Path(fixture) if fixture else None
    if path is not None and not path.is_absolute():
        path = Path(data_root) / path

    source_name = source.get("source_name", "csv_macro")
    series_code = source.get("series_code") or source.get("symbol")
    fetch_id: int | None = None
    endpoint_ref = f"{mode}:{source_name}"

    try:
        if mode == "fixture":
            if path is None:
                raise ValueError(f"fixture is required for fixture macro ingest: {source_name}")
            fetch_id = db.start_fetch(con, source_name, f"csv:{path}", usage_terms=_usage_terms(source, mode="fixture"))
            endpoint_ref = f"csv:{path}"
            payload = path.read_bytes()
            rows = _read_csv(path)
            for row in rows:
                con.execute(
                    """
                    INSERT INTO macro_series(series_code, obs_date, value, vintage_date, release_date, source_name, fetch_id, unit)
                    VALUES (?, ?, ?, ?, ?, ?, ?, ?)
                    ON CONFLICT(series_code, obs_date, vintage_date, source_name) DO UPDATE SET
                      value=excluded.value, release_date=excluded.release_date, fetch_id=excluded.fetch_id, unit=excluded.unit
                    """,
                    (
                        row["series_code"],
                        row["obs_date"],
                        float(row["value"]),
                        row.get("vintage_date", ""),
                        row.get("release_date") or row["obs_date"],
                        source_name,
                        fetch_id,
                        row.get("unit"),
                    ),
                )
            con.commit()
            db.finish_fetch(con, fetch_id, "success", rows_fetched=len(rows), checksum=sha256_bytes(payload), raw_cache_path=str(path))
            return len(rows), "success"

        provider = source.get("provider")
        if not provider:
            if mode == "hybrid" and path is not None and path.exists():
                return ingest_macro_source(con, source, data_root, mode="fixture", start_date=start_date, end_date=end_date)
            raise ValueError(f"provider is required for mode={mode}: {source_name}")

        if series_code is None:
            raise ValueError(f"series_code is required for online macro ingest: {source_name}")

        fetch_id = db.start_fetch(con, source_name, f"{provider}:{series_code}", usage_terms=_usage_terms(source, provider=provider, mode=mode))
        endpoint_ref = f"{provider}:{series_code}"
        if start_date is None:
            if mode == "incremental":
                latest = latest_macro_date(con, series_code, source_name)
                if latest is not None:
                    start_date = (latest - timedelta(days=max(lookback_days, 0))).isoformat()
                else:
                    start_date = source.get("first_date")
            else:
                start_date = source.get("first_date")
        start = None if not start_date else date.fromisoformat(start_date)
        end = None if not end_date else date.fromisoformat(end_date)

        if provider == "fred":
            rows, provider_name = fetch_fred_csv(series_code, start, end)
            count = _save_macro_rows(con, rows, source_name, fetch_id, series_code, provider_name)
        elif provider == "yahoo":
            remote_symbol = source.get("provider_symbol") or series_code
            rows, provider_name, meta = fetch_yahoo_history(remote_symbol, start, end, interval="1d")
            count = 0
            scale = float(source.get("value_scale", 1.0))
            unit = source.get("unit") or meta.get("currency")
            for row in rows:
                value = parse_float(row.get("close"))
                if value is None:
                    continue
                con.execute(
                    """
                    INSERT INTO macro_series(series_code, obs_date, value, vintage_date, release_date, source_name, fetch_id, unit)
                    VALUES (?, ?, ?, ?, ?, ?, ?, ?)
                    ON CONFLICT(series_code, obs_date, vintage_date, source_name) DO UPDATE SET
                      value=excluded.value, release_date=excluded.release_date, fetch_id=excluded.fetch_id, unit=excluded.unit
                    """,
                    (
                        series_code,
                        row["date"],
                        value * scale,
                        "",
                        row["date"],
                        source_name,
                        fetch_id,
                        unit,
                    ),
                )
                count += 1
        else:
            raise ValueError(f"unsupported online macro provider: {provider}")

        con.commit()
        status = "success" if count else "partial"
        db.finish_fetch(
            con,
            fetch_id,
            status,
            rows_fetched=count,
            checksum=sha256_bytes(_stable_json_bytes({"provider": provider, "source": source_name, "series_code": series_code, "rows": count})),
            raw_cache_path=endpoint_ref,
        )
        return count, status
    except Exception as exc:
        if fetch_id is None:
            db_provider = source.get("provider")
            db_mode = "fixture" if str(endpoint_ref).startswith("csv:") else mode
            fetch_id = db.start_fetch(con, source_name, endpoint_ref, usage_terms=_usage_terms(source, provider=db_provider, mode=db_mode))
        db.finish_fetch(con, fetch_id, "fail", error_message=str(exc), raw_cache_path=str(path) if path is not None else endpoint_ref)
        return 0, "fail"


def ingest_calendar_csv(con, source: dict[str, Any], data_root: str | Path) -> tuple[int, str]:
    path = Path(source["fixture"])
    if not path.is_absolute():
        path = Path(data_root) / path
    fetch_id = db.start_fetch(
        con,
        source.get("source_name", "csv_calendar"),
        f"csv:{path}",
        usage_terms=_usage_terms(source, mode="fixture"),
    )
    try:
        payload = path.read_bytes()
        rows = _read_csv(path)
        for row in rows:
            con.execute(
                """
                INSERT INTO economic_calendar(event_date, event_time_utc, country, category, event_name, source_name, importance, last_checked_at, context_json)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
                """,
                (
                    row["event_date"],
                    row.get("event_time_utc"),
                    row.get("country"),
                    row["category"],
                    row["event_name"],
                    source.get("source_name", row.get("source_name", "csv_calendar")),
                    row.get("importance", "med"),
                    utcnow_iso(),
                    row.get("context_json", "{}"),
                ),
            )
        con.commit()
        db.finish_fetch(con, fetch_id, "success", rows_fetched=len(rows), checksum=sha256_bytes(payload), raw_cache_path=str(path))
        return len(rows), "success"
    except Exception as exc:
        db.finish_fetch(con, fetch_id, "fail", error_message=str(exc), raw_cache_path=str(path))
        return 0, "fail"


def ingest_macro_online(con, source: dict[str, Any]) -> tuple[int, str]:
    return ingest_macro_source(con, source, ".", mode="online")


def _save_macro_rows(con, rows: list[dict[str, Any]], source_name: str, fetch_id: int, series_code: str, provider: str) -> int:
    count = 0
    for row in rows:
        value = row.get("value")
        if value is None:
            continue
        con.execute(
            """
            INSERT INTO macro_series(series_code, obs_date, value, vintage_date, release_date, source_name, fetch_id, unit)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?)
            ON CONFLICT(series_code, obs_date, vintage_date, source_name) DO UPDATE SET
              value=excluded.value, release_date=excluded.release_date, fetch_id=excluded.fetch_id, unit=excluded.unit
            """,
            (
                series_code,
                row["obs_date"],
                float(value),
                row.get("vintage_date", ""),
                row.get("release_date") or row["obs_date"],
                source_name,
                fetch_id,
                row.get("unit") or provider,
            ),
        )
        count += 1
    return count
