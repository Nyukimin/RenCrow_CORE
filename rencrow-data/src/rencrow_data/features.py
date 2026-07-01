from __future__ import annotations

import hashlib
import json
import math
from collections import defaultdict
from datetime import date, timedelta

from . import db
from .timeutil import friday_of_week, parse_date

PRICE_ASSET_TYPES = ("ETF", "STOCK", "CASH_PROXY", "CRYPTO", "INDEX")
FEATURE_CONFIG = {
    "version": "weekly_features_v1",
    "asset_types": PRICE_ASSET_TYPES,
    "return_windows_weeks": (1, 4, 12, 26),
    "momentum_skip_weeks": 1,
    "volatility_lookback_trading_days": 60,
    "drawdown_window_weeks": 26,
    "moving_average_windows_weeks": (4, 12),
    "volume_change_window_weeks": 4,
    "event_flag_window_days": 2,
    "macro_release_boundary": "release_date<=week_end",
    "fx_series": "USDJPY_BOJ",
}


def feature_config_hash() -> str:
    payload = json.dumps(FEATURE_CONFIG, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    return hashlib.sha256(payload.encode("utf-8")).hexdigest()


def _split_filters(values: list[str] | None) -> set[str]:
    if not values:
        return set()
    result: set[str] = set()
    for value in values:
        result.update(part.strip() for part in value.split(",") if part.strip())
    return result


def _nearest_macro(con, series_code: str, obs_date: date, week_end: date) -> float | None:
    row = con.execute(
        """
        SELECT value FROM macro_series
        WHERE series_code=? AND obs_date<=? AND COALESCE(release_date, obs_date)<=?
        ORDER BY obs_date DESC, COALESCE(release_date, obs_date) DESC, vintage_date DESC, source_name DESC
        LIMIT 1
        """,
        (series_code, obs_date.isoformat(), week_end.isoformat()),
    ).fetchone()
    return None if row is None else float(row["value"])


def _event_flag(con, category: str, week_end: date) -> int:
    start = (week_end - timedelta(days=2)).isoformat()
    end = (week_end + timedelta(days=2)).isoformat()
    categories = ("NFP", "EMPLOYMENT") if category == "EMPLOYMENT" else (category,)
    row = con.execute(
        "SELECT 1 FROM economic_calendar WHERE category IN ({}) AND event_date BETWEEN ? AND ? LIMIT 1".format(
            ",".join("?" for _ in categories)
        ),
        (*categories, start, end),
    ).fetchone()
    return 1 if row else 0


def _std(values: list[float]) -> float | None:
    if len(values) < 2:
        return None
    mean = sum(values) / len(values)
    var = sum((v - mean) ** 2 for v in values) / (len(values) - 1)
    return math.sqrt(var)


def build_features(con, symbols: list[str] | None = None, asset_types: list[str] | None = None, as_of: date | None = None) -> int:
    config_hash = feature_config_hash()
    symbol_filter = _split_filters(symbols)
    asset_type_filter = _split_filters(asset_types)
    allowed_asset_types = tuple(asset_type for asset_type in PRICE_ASSET_TYPES if not asset_type_filter or asset_type in asset_type_filter)
    if not allowed_asset_types:
        return 0
    predicates = ["i.asset_type IN ({})".format(", ".join("?" for _ in allowed_asset_types))]
    params: list[object] = list(allowed_asset_types)
    if symbol_filter:
        predicates.append("i.symbol IN ({})".format(", ".join("?" for _ in symbol_filter)))
        params.extend(sorted(symbol_filter))
    if as_of is not None:
        predicates.append("p.trade_date<=?")
        params.append(as_of.isoformat())
    rows = con.execute(
        """
        SELECT i.instrument_id, i.currency, p.trade_date, p.open, p.high, p.low, p.close, p.adj_close, p.volume
        FROM price_raw p
        JOIN instruments i ON i.instrument_id=p.instrument_id
        WHERE {}
        ORDER BY i.instrument_id, p.trade_date
        """.format(" AND ".join(predicates)),
        params,
    ).fetchall()
    by_inst: dict[int, list] = defaultdict(list)
    currencies: dict[int, str] = {}
    for row in rows:
        by_inst[int(row["instrument_id"])].append(row)
        currencies[int(row["instrument_id"])] = row["currency"] or "JPY"

    count = 0
    for iid, inst_rows in by_inst.items():
        daily = []
        previous_factor = None
        for row in inst_rows:
            if row["close"] in (None, 0) or row["adj_close"] is None:
                continue
            trade_day = parse_date(row["trade_date"])
            factor = float(row["adj_close"]) / float(row["close"])
            action = con.execute(
                "SELECT 1 FROM corporate_action WHERE instrument_id=? AND action_date=? LIMIT 1",
                (iid, row["trade_date"]),
            ).fetchone()
            if previous_factor is not None and abs(factor / previous_factor - 1.0) > 0.25 and action is None:
                db.log_event(con, "market", "stop", "adjusted_factor_anomaly", factor, 1.0, f'{{"instrument_id":{iid},"date":"{row["trade_date"]}"}}')
            previous_factor = factor
            close_adj = float(row["close"]) * factor
            if currencies[iid] == "USD":
                fx = _nearest_macro(con, "USDJPY_BOJ", trade_day, friday_of_week(trade_day))
                if fx is None:
                    continue
                close_adj *= fx
            daily.append({"date": trade_day, "close": close_adj, "volume": float(row["volume"] or 0.0)})

        weekly_map = {}
        for point in daily:
            week = friday_of_week(point["date"])
            if week not in weekly_map or point["date"] > weekly_map[week]["date"]:
                weekly_map[week] = point
        weekly = [dict(v, week_end=k) for k, v in sorted(weekly_map.items())]
        closes = [w["close"] for w in weekly]
        volumes = [w["volume"] for w in weekly]
        daily_returns = []
        for idx in range(1, len(daily)):
            prev = daily[idx - 1]["close"]
            daily_returns.append(0.0 if prev == 0 else daily[idx]["close"] / prev - 1.0)

        for idx, point in enumerate(weekly):
            week_end = point["week_end"]
            close = point["close"]
            def ret(period: int) -> float | None:
                if idx < period or closes[idx - period] == 0:
                    return None
                return close / closes[idx - period] - 1.0

            def ret_skip1(period: int) -> float | None:
                if idx < period + 1 or closes[idx - period - 1] == 0:
                    return None
                return closes[idx - 1] / closes[idx - period - 1] - 1.0

            daily_cut = [d for d in daily if d["date"] <= week_end]
            returns_cut = []
            for j in range(max(1, len(daily_cut) - 60), len(daily_cut)):
                prev = daily_cut[j - 1]["close"]
                returns_cut.append(0.0 if prev == 0 else daily_cut[j]["close"] / prev - 1.0)
            vol_12w = None
            std = _std(returns_cut)
            if std is not None:
                vol_12w = std * math.sqrt(252)
            start_26 = max(0, idx - 25)
            peak = max(closes[start_26 : idx + 1])
            drawdown_26w = None if peak == 0 else close / peak - 1.0
            ma_4 = sum(closes[idx - 3 : idx + 1]) / 4 if idx >= 3 else None
            ma_12 = sum(closes[idx - 11 : idx + 1]) / 12 if idx >= 11 else None
            vol_chg = None
            if idx >= 4:
                prev_vol = sum(volumes[idx - 4 : idx]) / 4
                if prev_vol:
                    vol_chg = volumes[idx] / prev_vol - 1.0
            fx_ret = None
            fx_now = _nearest_macro(con, "USDJPY_BOJ", week_end, week_end)
            fx_prev = _nearest_macro(con, "USDJPY_BOJ", week_end - timedelta(days=7), week_end)
            if fx_now is not None and fx_prev not in (None, 0):
                fx_ret = fx_now / fx_prev - 1.0
            us10_now = _nearest_macro(con, "DGS10", week_end, week_end)
            us10_prev = _nearest_macro(con, "DGS10", week_end - timedelta(days=7), week_end)
            us10_change = us10_now - us10_prev if us10_now is not None and us10_prev is not None else None
            con.execute(
                """
                INSERT INTO feature_weekly(
                  instrument_id, week_end, close_adj_jpy, ret_1w, ret_4w, ret_12w, ret_12w_skip1, ret_26w, vol_12w,
                  drawdown_26w, ma_4w_gap, ma_12w_gap, volume_change_4w, fx_ret_1w,
                  us10y_change_1w, boj_flag, cpi_flag, fomc_flag, employment_flag,
                  holdings_turnover, event_risk_score, feature_config_hash
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, COALESCE((SELECT event_risk_score FROM feature_weekly WHERE instrument_id=? AND week_end=?), 0), ?)
                ON CONFLICT(instrument_id, week_end) DO UPDATE SET
                  close_adj_jpy=excluded.close_adj_jpy,
                  ret_1w=excluded.ret_1w,
                  ret_4w=excluded.ret_4w,
                  ret_12w=excluded.ret_12w,
                  ret_12w_skip1=excluded.ret_12w_skip1,
                  ret_26w=excluded.ret_26w,
                  vol_12w=excluded.vol_12w,
                  drawdown_26w=excluded.drawdown_26w,
                  ma_4w_gap=excluded.ma_4w_gap,
                  ma_12w_gap=excluded.ma_12w_gap,
                  volume_change_4w=excluded.volume_change_4w,
                  fx_ret_1w=excluded.fx_ret_1w,
                  us10y_change_1w=excluded.us10y_change_1w,
                  boj_flag=excluded.boj_flag,
                  cpi_flag=excluded.cpi_flag,
                  fomc_flag=excluded.fomc_flag,
                  employment_flag=excluded.employment_flag,
                  feature_config_hash=excluded.feature_config_hash
                """,
                (
                    iid,
                    week_end.isoformat(),
                    close,
                    ret(1),
                    ret(4),
                    ret(12),
                    ret_skip1(12),
                    ret(26),
                    vol_12w,
                    drawdown_26w,
                    None if ma_4 in (None, 0) else close / ma_4 - 1.0,
                    None if ma_12 in (None, 0) else close / ma_12 - 1.0,
                    vol_chg,
                    fx_ret,
                    us10_change,
                    _event_flag(con, "BOJ", week_end),
                    _event_flag(con, "CPI", week_end),
                    _event_flag(con, "FOMC", week_end),
                    _event_flag(con, "EMPLOYMENT", week_end),
                    None,
                    iid,
                    week_end.isoformat(),
                    config_hash,
                ),
            )
            count += 1
    con.commit()
    return count
