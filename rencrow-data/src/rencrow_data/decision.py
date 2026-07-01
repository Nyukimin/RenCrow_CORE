from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path

from .backtest import _load_strategy, _score
from .hashing import assert_snapshot_features_unchanged
from .timeutil import unique_id


DEFAULT_TRADABLE_ASSET_TYPES = ("ETF", "CASH_PROXY")


@dataclass(frozen=True)
class DecisionOptions:
    snapshot_id: str
    strategy_id: str
    risk_check_id: str
    output_dir: Path | None = None


def _json(value: object) -> str:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"))


def _snapshot_date(con, snapshot_id: str) -> str:
    row = con.execute("SELECT snapshot_date, features_hash FROM snapshot_registry WHERE snapshot_id=?", (snapshot_id,)).fetchone()
    if row is None:
        raise ValueError(f"snapshot not found: {snapshot_id}")
    assert_snapshot_features_unchanged(con, snapshot_id, row["snapshot_date"], row["features_hash"])
    return row["snapshot_date"]


def _risk_check(con, risk_check_id: str, snapshot_id: str, strategy_id: str):
    row = con.execute(
        """
        SELECT *
          FROM risk_check_result
         WHERE risk_check_id=? AND snapshot_id=? AND strategy_id=?
        """,
        (risk_check_id, snapshot_id, strategy_id),
    ).fetchone()
    if row is None:
        raise ValueError(f"risk check not found or mismatched: {risk_check_id}")
    return row


def _tradable_asset_types(config: dict[str, object]) -> tuple[str, ...]:
    configured = config.get("tradable_asset_types")
    if isinstance(configured, list) and configured:
        return tuple(str(asset_type) for asset_type in configured)
    if isinstance(configured, str) and configured.strip():
        return tuple(part.strip() for part in configured.split(",") if part.strip())
    return DEFAULT_TRADABLE_ASSET_TYPES


def _instrument_metadata(con, symbols: tuple[str, ...], tradable_asset_types: tuple[str, ...]) -> dict[int, dict[str, str]]:
    rows = con.execute(
        f"""
        SELECT instrument_id, symbol, asset_type
          FROM instruments
         WHERE symbol IN ({','.join('?' for _ in symbols)}) AND active=1
        """,
        symbols,
    ).fetchall()
    disallowed = [
        f"{row['symbol']}({row['asset_type']})"
        for row in rows
        if str(row["asset_type"]) not in tradable_asset_types
    ]
    if disallowed:
        allowed = ", ".join(tradable_asset_types)
        raise ValueError(f"strategy universe contains non-tradable asset types: {', '.join(disallowed)}; allowed={allowed}")
    return {
        int(row["instrument_id"]): {
            "symbol": str(row["symbol"]),
            "asset_type": str(row["asset_type"]),
        }
        for row in rows
    }


def _latest_feature_week(con, snapshot_date: str) -> str:
    row = con.execute(
        "SELECT MAX(week_end) AS week_end FROM feature_weekly WHERE week_end<=?",
        (snapshot_date,),
    ).fetchone()
    if row is None or row["week_end"] is None:
        raise ValueError(f"no feature_weekly rows available for snapshot date: {snapshot_date}")
    return row["week_end"]


def _rank_candidates(con, snapshot_id: str, strategy_id: str, week_end: str, config: dict[str, object]) -> list[dict[str, object]]:
    universe = tuple(str(symbol) for symbol in config.get("universe", []))
    if not universe:
        raise ValueError("strategy universe is empty")
    instrument_metadata = _instrument_metadata(con, universe, _tradable_asset_types(config))
    iid_to_symbol = {iid: item["symbol"] for iid, item in instrument_metadata.items()}
    if len(instrument_metadata) != len(universe):
        missing = sorted(set(universe) - set(iid_to_symbol.values()))
        raise ValueError(f"strategy universe symbols are missing from instruments: {', '.join(missing)}")
    rows = con.execute(
        f"""
        SELECT instrument_id, ret_12w, ret_12w_skip1, ret_26w, vol_12w, drawdown_26w, event_risk_score, close_adj_jpy
          FROM feature_weekly
         WHERE week_end=? AND instrument_id IN ({','.join('?' for _ in iid_to_symbol)})
        """,
        (week_end, *iid_to_symbol.keys()),
    ).fetchall()
    candidates: list[dict[str, object]] = []
    for row in rows:
        feature = {
            "ret_12w": row["ret_12w"],
            "ret_12w_skip1": row["ret_12w_skip1"],
            "ret_26w": row["ret_26w"],
            "vol_12w": row["vol_12w"],
            "drawdown_26w": row["drawdown_26w"],
            "event_risk_score": row["event_risk_score"],
            "close_adj_jpy": row["close_adj_jpy"],
        }
        score = _score(feature, config)
        if score is None:
            continue
        candidates.append(
            {
                "instrument_id": int(row["instrument_id"]),
                "symbol": iid_to_symbol[int(row["instrument_id"])],
                "asset_type": instrument_metadata[int(row["instrument_id"])]["asset_type"],
                "raw_score": score,
                "adjusted_score": score - float(row["event_risk_score"] or 0.0),
                "feature": feature,
            }
        )
    candidates.sort(key=lambda item: float(item["adjusted_score"]), reverse=True)
    if not candidates:
        cash_proxy = str(config.get("cash_proxy", ""))
        cash_iid = next((iid for iid, symbol in iid_to_symbol.items() if symbol == cash_proxy), None)
        if cash_iid is not None:
            candidates.append(
                {
                    "instrument_id": cash_iid,
                    "symbol": cash_proxy,
                    "asset_type": instrument_metadata[cash_iid]["asset_type"],
                    "raw_score": None,
                    "adjusted_score": None,
                    "feature": {},
                }
            )
    return candidates


def _approval_payload(*, decision_id: int, snapshot_id: str, strategy_id: str, status: str, candidates: list[dict[str, object]]) -> dict[str, object]:
    return {
        "decision_id": decision_id,
        "snapshot_id": snapshot_id,
        "strategy_id": strategy_id,
        "approval_required": True,
        "approved": False,
        "approver": "",
        "approved_at": "",
        "approval_reason": "",
        "risk_status": status,
        "candidate_symbols": [item["symbol"] for item in candidates],
    }


def _write_json_approval(path: Path, payload: dict[str, object]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def _yaml_scalar(value: object) -> str:
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, int):
        return str(value)
    text = str(value)
    if text == "":
        return '""'
    if any(ch in text for ch in ":#[]{}\",'") or text.lower() in {"true", "false", "null"}:
        return json.dumps(text, ensure_ascii=False)
    return text


def _write_yaml_approval(path: Path, payload: dict[str, object]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    lines: list[str] = []
    for key, value in payload.items():
        if isinstance(value, list):
            lines.append(f"{key}:")
            if value:
                lines.extend(f"  - {_yaml_scalar(item)}" for item in value)
            else:
                lines.append("  []")
        else:
            lines.append(f"{key}: {_yaml_scalar(value)}")
    path.write_text("\n".join(lines) + "\n", encoding="utf-8")


def generate_decision(con, options: DecisionOptions) -> dict[str, object]:
    config = _load_strategy(con, options.strategy_id)
    snapshot_date = _snapshot_date(con, options.snapshot_id)
    risk = _risk_check(con, options.risk_check_id, options.snapshot_id, options.strategy_id)
    week_end = _latest_feature_week(con, snapshot_date)
    risk_status = risk["status"]
    top_n = int(config.get("top_n", 1))

    candidates = _rank_candidates(con, options.snapshot_id, options.strategy_id, week_end, config)
    vetoed = 1 if risk_status in {"stop", "kill_switch"} else 0
    target_weight = 0.0 if vetoed else (0.5 if risk_status == "reduce" else 1.0)
    selected = candidates[: max(top_n, 1)]
    for idx, item in enumerate(selected, start=1):
        con.execute(
            """
            INSERT INTO weekly_signal(
              signal_id, snapshot_id, strategy_id, week_end, instrument_id, rank, target_weight,
              raw_score, adjusted_score, vetoed, reason_json
            )
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            (
                unique_id(f"signal-{idx}"),
                options.snapshot_id,
                options.strategy_id,
                week_end,
                item["instrument_id"],
                idx,
                target_weight / len(selected) if selected else 0.0,
                item["raw_score"],
                item["adjusted_score"],
                vetoed,
                _json(
                    {
                        "symbol": item["symbol"],
                        "asset_type": item["asset_type"],
                        "risk_check_id": options.risk_check_id,
                        "risk_status": risk_status,
                        "approval_required": True,
                        "veto_reason": None if not vetoed else "risk_check_blocked",
                    }
                ),
            ),
        )

    candidate_json = {
        "approval_required": True,
        "risk_check_id": options.risk_check_id,
        "risk_status": risk_status,
        "week_end": week_end,
        "candidates": [
            {
                "symbol": item["symbol"],
                "asset_type": item["asset_type"],
                "instrument_id": item["instrument_id"],
                "target_weight": target_weight / len(selected) if selected else 0.0,
                "raw_score": item["raw_score"],
                "adjusted_score": item["adjusted_score"],
            }
            for item in selected
        ],
    }
    veto_json = {
        "vetoed": bool(vetoed),
        "risk_status": risk_status,
        "max_dd_check": risk["max_dd_check"],
        "weekly_loss_check": risk["weekly_loss_check"],
        "volatility_check": risk["volatility_check"],
        "event_check": risk["event_check"],
        "concentration_check": risk["concentration_check"],
    }
    con.execute(
        """
        INSERT INTO decision_log(snapshot_id, decision_date, account_scope, strategy_name, candidate_json, veto_json, approved)
        VALUES (?, ?, 'paper', ?, ?, ?, 0)
        """,
        (int(options.snapshot_id), snapshot_date, options.strategy_id, _json(candidate_json), _json(veto_json)),
    )
    decision_id = int(con.execute("SELECT last_insert_rowid()").fetchone()[0])
    con.execute(
        """
        UPDATE risk_check_result
           SET decision_id=?
         WHERE risk_check_id=?
           AND (decision_id IS NULL OR decision_id='')
        """,
        (str(decision_id), options.risk_check_id),
    )
    con.commit()

    output_dir = options.output_dir or Path("rencrow-data/approvals")
    approval_payload = _approval_payload(
        decision_id=decision_id,
        snapshot_id=options.snapshot_id,
        strategy_id=options.strategy_id,
        status=risk_status,
        candidates=selected,
    )
    approval_path = output_dir / f"decision_{decision_id}.approval.yml"
    approval_json_path = output_dir / f"decision_{decision_id}.approval.json"
    latest_path = output_dir / "latest.yml"
    _write_yaml_approval(approval_path, approval_payload)
    _write_yaml_approval(latest_path, approval_payload)
    _write_json_approval(approval_json_path, approval_payload)
    return {
        "decision_id": decision_id,
        "snapshot_id": options.snapshot_id,
        "strategy_id": options.strategy_id,
        "risk_check_id": options.risk_check_id,
        "risk_status": risk_status,
        "approval_required": True,
        "approved": False,
        "vetoed": bool(vetoed),
        "week_end": week_end,
        "candidates": candidate_json["candidates"],
        "approval_path": str(approval_path),
        "approval_latest_path": str(latest_path),
        "approval_json_path": str(approval_json_path),
    }
