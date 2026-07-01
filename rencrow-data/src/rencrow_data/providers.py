from __future__ import annotations

import csv
import json
import math
import time
from datetime import date, datetime, timezone
from io import StringIO
from typing import Any

import requests


YAHOO_CHART_URL = "https://query1.finance.yahoo.com/v8/finance/chart/{symbol}"
FRED_GRAPH_URL = "https://fred.stlouisfed.org/graph/fredgraph.csv"


def _session() -> requests.Session:
    sess = requests.Session()
    sess.headers.update({"User-Agent": "Mozilla/5.0 RenCrowData/1.0"})
    return sess


def _request_text(url: str, *, params: dict[str, Any] | None = None, timeout: int = 30, retries: int = 2) -> str:
    last_exc: Exception | None = None
    with _session() as sess:
        for attempt in range(retries + 1):
            try:
                resp = sess.get(url, params=params, timeout=timeout)
                resp.raise_for_status()
                return resp.text
            except Exception as exc:  # pragma: no cover - network failure path
                last_exc = exc
                if attempt >= retries:
                    break
                time.sleep(min(2 ** attempt, 8))
    assert last_exc is not None
    raise last_exc


def _request_json(url: str, *, params: dict[str, Any] | None = None, timeout: int = 30) -> dict[str, Any]:
    text = _request_text(url, params=params, timeout=timeout)
    return json.loads(text)


def _parse_csv(text: str) -> list[dict[str, str]]:
    return list(csv.DictReader(StringIO(text)))


def parse_float(value: Any) -> float | None:
    if value in (None, "", "null", "None"):
        return None
    try:
        v = float(value)
    except Exception:
        return None
    if math.isnan(v):
        return None
    return v


def fetch_yahoo_history(symbol: str, start_date: date | None = None, end_date: date | None = None, interval: str = "1d") -> tuple[list[dict[str, Any]], str, dict[str, Any]]:
    params = {
        "period1": int(datetime(1970, 1, 1, tzinfo=timezone.utc).timestamp()) if start_date is None else int(datetime.combine(start_date, datetime.min.time(), tzinfo=timezone.utc).timestamp()),
        "period2": int(time.time()) if end_date is None else int(datetime.combine(end_date, datetime.max.time(), tzinfo=timezone.utc).timestamp()),
        "interval": interval,
        "includePrePost": "false",
        "events": "div,splits",
        "includeAdjustedClose": "true",
    }
    payload = _request_json(YAHOO_CHART_URL.format(symbol=symbol), params=params, timeout=30)
    result = payload.get("chart", {}).get("result") or []
    if not result:
        err = payload.get("chart", {}).get("error")
        raise RuntimeError(f"yahoo chart unavailable for {symbol}: {err or 'empty result'}")

    chart = result[0]
    timestamps = chart.get("timestamp") or []
    indicators = chart.get("indicators", {})
    quote = (indicators.get("quote") or [{}])[0]
    adj_close = ((indicators.get("adjclose") or [{}])[0]).get("adjclose") or []
    events = chart.get("events") or {}
    div_by_ts = {str(k): v for k, v in (events.get("dividends") or {}).items()}
    split_by_ts = {str(k): v for k, v in (events.get("splits") or {}).items()}

    rows: list[dict[str, Any]] = []
    for idx, ts in enumerate(timestamps):
        d = datetime.fromtimestamp(int(ts), tz=timezone.utc).date().isoformat()
        row = {
            "date": d,
            "open": parse_float((quote.get("open") or [None])[idx] if idx < len(quote.get("open") or []) else None),
            "high": parse_float((quote.get("high") or [None])[idx] if idx < len(quote.get("high") or []) else None),
            "low": parse_float((quote.get("low") or [None])[idx] if idx < len(quote.get("low") or []) else None),
            "close": parse_float((quote.get("close") or [None])[idx] if idx < len(quote.get("close") or []) else None),
            "adj_close": parse_float(adj_close[idx] if idx < len(adj_close) else None),
            "volume": parse_float((quote.get("volume") or [None])[idx] if idx < len(quote.get("volume") or []) else None),
            "dividend": None,
            "split": None,
        }
        ev = div_by_ts.get(str(ts))
        if ev is not None:
            row["dividend"] = parse_float(ev.get("amount"))
        ev = split_by_ts.get(str(ts))
        if ev is not None:
            split_ratio = ev.get("splitRatio")
            if isinstance(split_ratio, str) and "/" in split_ratio:
                left, right = split_ratio.split("/", 1)
                try:
                    row["split"] = float(left) / float(right)
                except Exception:
                    row["split"] = None
            else:
                row["split"] = parse_float(split_ratio)
        rows.append(row)

    meta = chart.get("meta") or {}
    return rows, "yahoo", meta


def fetch_fred_csv(series_code: str, start_date: date | None = None, end_date: date | None = None) -> tuple[list[dict[str, Any]], str]:
    params: dict[str, Any] = {"id": series_code}
    if start_date is not None:
        params["cosd"] = start_date.isoformat()
    if end_date is not None:
        params["coed"] = end_date.isoformat()
    text = _request_text(FRED_GRAPH_URL, params=params, timeout=60)
    rows = []
    for row in _parse_csv(text):
        rows.append({
            "obs_date": row.get("DATE") or row.get("date") or row.get("obs_date"),
            "value": parse_float(row.get(series_code) or row.get("value")),
            "vintage_date": "",
            "release_date": row.get("DATE") or row.get("date") or row.get("obs_date"),
            "unit": None,
        })
    return rows, "fred"
