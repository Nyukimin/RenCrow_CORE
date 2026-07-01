from __future__ import annotations

import csv
import json
from datetime import date, timedelta
from pathlib import Path
from typing import Any

from . import db
from .hashing import sha256_bytes
from .providers import fetch_yahoo_history, parse_float


PRICE_FIELDS = ("open", "high", "low", "close", "adj_close", "volume")


def _float_or_none(value: str | None) -> float | None:
    if value is None or value == "":
        return None
    return float(value)


def _read_csv(path: str | Path) -> list[dict[str, str]]:
    with Path(path).open(newline="", encoding="utf-8") as f:
        return list(csv.DictReader(f))


def _stable_json_bytes(value: object) -> bytes:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode("utf-8")


def _usage_terms(item: dict[str, Any], provider: str | None = None, mode: str = "fixture") -> str:
    if item.get("usage_terms"):
        return str(item["usage_terms"])
    if mode == "fixture":
        return "local_fixture; internal_research_only; no_redistribution"
    if provider == "yahoo":
        return "yahoo_finance; internal_research_only; no_redistribution; respect_provider_terms"
    return f"{provider or 'unknown_provider'}; internal_research_only; no_redistribution"


def latest_price_date(con, iid: int, source_name: str | None = None) -> date | None:
    if source_name is None:
        row = con.execute("SELECT MAX(trade_date) AS d FROM price_raw WHERE instrument_id=?", (iid,)).fetchone()
    else:
        row = con.execute(
            "SELECT MAX(trade_date) AS d FROM price_raw WHERE instrument_id=? AND source_name=?",
            (iid, source_name),
        ).fetchone()
    if not row or row["d"] is None:
        return None
    return date.fromisoformat(row["d"])


def _store_market_rows(
    con,
    *,
    iid: int,
    symbol: str,
    rows: list[dict[str, Any]],
    source_name: str,
    fetch_id: int,
    currency: str,
    context_json: str | None = None,
) -> tuple[int, int]:
    revision_count = 0
    action_count = 0
    for row in rows:
        values = {
            "instrument_id": iid,
            "trade_date": row["date"],
            "open": row.get("open"),
            "high": row.get("high"),
            "low": row.get("low"),
            "close": row.get("close"),
            "adj_close": row.get("adj_close"),
            "volume": row.get("volume"),
            "source_name": source_name,
            "fetch_id": fetch_id,
        }
        existing = con.execute(
            "SELECT open, high, low, close, adj_close, volume FROM price_raw WHERE instrument_id=? AND trade_date=? AND source_name=?",
            (iid, row["date"], source_name),
        ).fetchone()
        if existing is not None:
            old = [existing[k] for k in PRICE_FIELDS]
            new = [values[k] for k in PRICE_FIELDS]
            if old != new:
                db.log_event(
                    con,
                    "market",
                    "warn",
                    "price_revision",
                    context_json=json.dumps({"symbol": symbol, "date": row["date"], "old": old, "new": new}, ensure_ascii=False),
                )
                revision_count += 1
        con.execute(
            """
            INSERT INTO price_raw(instrument_id, trade_date, open, high, low, close, adj_close, volume, source_name, fetch_id)
            VALUES (:instrument_id, :trade_date, :open, :high, :low, :close, :adj_close, :volume, :source_name, :fetch_id)
            ON CONFLICT(instrument_id, trade_date, source_name) DO UPDATE SET
              open=excluded.open, high=excluded.high, low=excluded.low, close=excluded.close,
              adj_close=excluded.adj_close, volume=excluded.volume, fetch_id=excluded.fetch_id
            """,
            values,
        )
        dividend = row.get("dividend")
        split = row.get("split")
        if dividend:
            con.execute(
                """
                INSERT INTO corporate_action(instrument_id, action_date, action_type, value, currency, source_name, fetch_id, context_json)
                VALUES (?, ?, 'dividend', ?, ?, ?, ?, ?)
                ON CONFLICT(instrument_id, action_date, action_type, source_name) DO UPDATE SET value=excluded.value, fetch_id=excluded.fetch_id, context_json=excluded.context_json
                """,
                (iid, row["date"], dividend, currency, source_name, fetch_id, context_json or "{}"),
            )
            action_count += 1
        if split and split != 1.0:
            con.execute(
                """
                INSERT INTO corporate_action(instrument_id, action_date, action_type, value, currency, source_name, fetch_id, context_json)
                VALUES (?, ?, 'split', ?, ?, ?, ?, ?)
                ON CONFLICT(instrument_id, action_date, action_type, source_name) DO UPDATE SET value=excluded.value, fetch_id=excluded.fetch_id, context_json=excluded.context_json
                """,
                (iid, row["date"], split, currency, source_name, fetch_id, context_json or "{}"),
            )
            action_count += 1
    return revision_count, action_count


def save_market_csv(con, item: dict[str, Any], data_root: str | Path) -> tuple[int, str]:
    return save_market_item(con, item, data_root, mode="fixture")


def save_market_item(
    con,
    item: dict[str, Any],
    data_root: str | Path,
    *,
    mode: str = "fixture",
    start_date: str | None = None,
    end_date: str | None = None,
    lookback_days: int = 7,
) -> tuple[int, str]:
    symbol = item["symbol"]
    source_name = item.get("source_name", "csv")
    endpoint = item.get("fixture")
    fetch_id: int | None = None
    endpoint_ref = f"{mode}:{symbol}"
    try:
        iid = db.instrument_id(con, symbol, item.get("venue"))
        if mode == "fixture":
            if not endpoint:
                raise ValueError(f"fixture is required for MVP offline ingest: {symbol}")
            endpoint_path = Path(endpoint)
            if not endpoint_path.is_absolute():
                endpoint_path = Path(data_root) / endpoint_path
            fetch_id = db.start_fetch(con, source_name, f"csv:{endpoint_path}", usage_terms=_usage_terms(item, mode="fixture"))
            endpoint_ref = f"csv:{endpoint_path}"
            payload = endpoint_path.read_bytes()
            checksum = sha256_bytes(payload)
            rows = [{k: v for k, v in row.items()} for row in _read_csv(endpoint_path)]
            for row in rows:
                row["open"] = _float_or_none(row.get("open"))
                row["high"] = _float_or_none(row.get("high"))
                row["low"] = _float_or_none(row.get("low"))
                row["close"] = _float_or_none(row.get("close"))
                row["adj_close"] = _float_or_none(row.get("adj_close"))
                row["volume"] = _float_or_none(row.get("volume"))
                row["dividend"] = _float_or_none(row.get("dividend"))
                row["split"] = _float_or_none(row.get("split"))
            _, action_count = _store_market_rows(
                con,
                iid=iid,
                symbol=symbol,
                rows=rows,
                source_name=source_name,
                fetch_id=fetch_id,
                currency=item.get("currency", "JPY"),
            )
            con.commit()
            db.finish_fetch(con, fetch_id, "success", rows_fetched=len(rows), checksum=checksum, raw_cache_path=str(endpoint_path))
            return len(rows), "success"

        provider = item.get("provider")
        if not provider:
            if endpoint and mode == "hybrid":
                return save_market_item(con, item, data_root, mode="fixture", start_date=start_date, end_date=end_date)
            raise ValueError(f"provider is required for mode={mode}: {symbol}")

        remote_symbol = item.get("provider_symbol") or symbol
        source_name = item.get("source_name", f"{provider}_market")
        fetch_id = db.start_fetch(con, source_name, f"{provider}:{remote_symbol}", usage_terms=_usage_terms(item, provider=provider, mode=mode))
        endpoint_ref = f"{provider}:{remote_symbol}"
        if start_date is None:
            if mode == "incremental":
                latest = latest_price_date(con, iid, source_name)
                if latest is not None:
                    start_date = (latest - timedelta(days=max(lookback_days, 0))).isoformat()
                else:
                    start_date = item.get("first_date")
            else:
                start_date = item.get("first_date")
        start = None if not start_date else date.fromisoformat(start_date)
        end = None if not end_date else date.fromisoformat(end_date)
        if provider != "yahoo":
            raise ValueError(f"unsupported market provider: {provider}")
        rows, provider_name, meta = fetch_yahoo_history(remote_symbol, start, end, interval="1d")
        for row in rows:
            row["dividend"] = row.get("dividend")
            row["split"] = row.get("split")
        checksum = sha256_bytes(_stable_json_bytes({"rows": rows, "meta": meta, "provider": provider_name}))
        _store_market_rows(
            con,
            iid=iid,
            symbol=symbol,
            rows=rows,
            source_name=source_name,
            fetch_id=fetch_id,
            currency=item.get("currency", "JPY"),
            context_json=json.dumps({"provider": provider_name, "meta": meta}, ensure_ascii=False),
        )
        con.commit()
        status = "success" if rows else "partial"
        db.finish_fetch(con, fetch_id, status, rows_fetched=len(rows), checksum=checksum, raw_cache_path=f"yahoo:{remote_symbol}")
        return len(rows), status
    except Exception as exc:
        if fetch_id is None:
            db_provider = item.get("provider")
            db_mode = "fixture" if str(endpoint_ref).startswith("csv:") else mode
            fetch_id = db.start_fetch(con, source_name, endpoint_ref, usage_terms=_usage_terms(item, provider=db_provider, mode=db_mode))
        db.finish_fetch(con, fetch_id, "fail", error_message=str(exc), raw_cache_path=str(endpoint or symbol))
        return 0, "fail"


def save_market_yahoo(con, item: dict[str, Any], start_date: str | None = None, end_date: str | None = None) -> tuple[int, str]:
    symbol = item["symbol"]
    remote_symbol = item.get("provider_symbol") or symbol
    source_name = item.get("source_name", "yahoo_market")
    fetch_id = db.start_fetch(con, source_name, f"yahoo:{remote_symbol}", usage_terms=_usage_terms(item, provider="yahoo", mode="online"))
    try:
        start = None if not start_date else date.fromisoformat(start_date)
        end = None if not end_date else date.fromisoformat(end_date)
        rows, provider_name, meta = fetch_yahoo_history(remote_symbol, start, end, interval="1d")
        iid = db.instrument_id(con, symbol, item.get("venue"))
        action_count = 0
        for row in rows:
            values = {
                "instrument_id": iid,
                "trade_date": row["date"],
                "open": row["open"],
                "high": row["high"],
                "low": row["low"],
                "close": row["close"],
                "adj_close": row["adj_close"],
                "volume": row["volume"],
                "source_name": source_name,
                "fetch_id": fetch_id,
            }
            existing = con.execute(
                "SELECT open, high, low, close, adj_close, volume FROM price_raw WHERE instrument_id=? AND trade_date=? AND source_name=?",
                (iid, row["date"], source_name),
            ).fetchone()
            if existing is not None:
                old = [existing[k] for k in PRICE_FIELDS]
                new = [values[k] for k in PRICE_FIELDS]
                if old != new:
                    db.log_event(
                        con,
                        "market",
                        "warn",
                        "price_revision",
                        context_json=json.dumps({"symbol": symbol, "date": row["date"], "old": old, "new": new}, ensure_ascii=False),
                    )
            con.execute(
                """
                INSERT INTO price_raw(instrument_id, trade_date, open, high, low, close, adj_close, volume, source_name, fetch_id)
                VALUES (:instrument_id, :trade_date, :open, :high, :low, :close, :adj_close, :volume, :source_name, :fetch_id)
                ON CONFLICT(instrument_id, trade_date, source_name) DO UPDATE SET
                  open=excluded.open, high=excluded.high, low=excluded.low, close=excluded.close,
                  adj_close=excluded.adj_close, volume=excluded.volume, fetch_id=excluded.fetch_id
                """,
                values,
            )
            dividend = row.get("dividend")
            split = row.get("split")
            if dividend:
                con.execute(
                    """
                    INSERT INTO corporate_action(instrument_id, action_date, action_type, value, currency, source_name, fetch_id, context_json)
                    VALUES (?, ?, 'dividend', ?, ?, ?, ?, ?)
                    ON CONFLICT(instrument_id, action_date, action_type, source_name) DO UPDATE SET value=excluded.value, fetch_id=excluded.fetch_id, context_json=excluded.context_json
                    """,
                    (iid, row["date"], dividend, item.get("currency", "JPY"), source_name, fetch_id, json.dumps({"provider": provider_name, "meta": meta}, ensure_ascii=False)),
                )
                action_count += 1
            if split and split != 1.0:
                con.execute(
                    """
                    INSERT INTO corporate_action(instrument_id, action_date, action_type, value, currency, source_name, fetch_id, context_json)
                    VALUES (?, ?, 'split', ?, ?, ?, ?, ?)
                    ON CONFLICT(instrument_id, action_date, action_type, source_name) DO UPDATE SET value=excluded.value, fetch_id=excluded.fetch_id, context_json=excluded.context_json
                    """,
                    (iid, row["date"], split, item.get("currency", "JPY"), source_name, fetch_id, json.dumps({"provider": provider_name, "meta": meta}, ensure_ascii=False)),
                )
                action_count += 1
        con.commit()
        db.finish_fetch(con, fetch_id, "success", rows_fetched=len(rows), checksum="", raw_cache_path=f"yahoo:{remote_symbol}")
        return len(rows), "success"
    except Exception as exc:
        db.finish_fetch(con, fetch_id, "fail", error_message=str(exc), raw_cache_path=f"yahoo:{remote_symbol}")
        return 0, "fail"
