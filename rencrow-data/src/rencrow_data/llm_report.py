from __future__ import annotations

import hashlib
import json
from dataclasses import dataclass
from pathlib import Path

from .timeutil import unique_id


@dataclass(frozen=True)
class LLMReportOptions:
    snapshot_id: str
    decision_id: int | None
    task: str
    model: str = "local-deterministic"
    prompt_version: str = "weekly_report_v1"
    output_dir: Path | None = None


def _stable_json(value: object) -> str:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"))


def _sha256(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def _snapshot(con, snapshot_id: str):
    row = con.execute("SELECT * FROM snapshot_registry WHERE snapshot_id=?", (snapshot_id,)).fetchone()
    if row is None:
        raise ValueError(f"snapshot not found: {snapshot_id}")
    return row


def _decision(con, decision_id: int | None, snapshot_id: str):
    if decision_id is None:
        return con.execute(
            "SELECT * FROM decision_log WHERE snapshot_id=? ORDER BY created_at DESC, decision_id DESC LIMIT 1",
            (int(snapshot_id),),
        ).fetchone()
    row = con.execute("SELECT * FROM decision_log WHERE decision_id=?", (decision_id,)).fetchone()
    if row is None:
        raise ValueError(f"decision not found: {decision_id}")
    if str(row["snapshot_id"]) != str(snapshot_id):
        raise ValueError("decision snapshot_id does not match report snapshot")
    return row


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


def build_llm_report(con, options: LLMReportOptions) -> dict[str, object]:
    snapshot = _snapshot(con, options.snapshot_id)
    decision = _decision(con, options.decision_id, options.snapshot_id)
    risk = _risk_for_decision(con, decision)
    quality_blockers = con.execute(
        """
        SELECT COUNT(*) AS count
          FROM data_quality_check
         WHERE check_date=? AND severity='blocker' AND status!='pass'
        """,
        (snapshot["snapshot_date"],),
    ).fetchone()["count"]
    payload = {
        "snapshot": {
            "snapshot_id": options.snapshot_id,
            "snapshot_date": snapshot["snapshot_date"],
            "status": snapshot["status"],
            "db_hash": snapshot["db_hash"],
            "features_hash": snapshot["features_hash"],
        },
        "decision": None
        if decision is None
        else {
            "decision_id": decision["decision_id"],
            "account_scope": decision["account_scope"],
            "approved": decision["approved"],
            "candidate_json": json.loads(decision["candidate_json"] or "{}"),
            "veto_json": json.loads(decision["veto_json"] or "{}"),
        },
        "risk": None
        if risk is None
        else {
            "risk_check_id": risk["risk_check_id"],
            "status": risk["status"],
            "max_dd_check": risk["max_dd_check"],
            "weekly_loss_check": risk["weekly_loss_check"],
            "volatility_check": risk["volatility_check"],
            "event_check": risk["event_check"],
        },
        "quality_blockers": int(quality_blockers or 0),
    }
    input_text = _stable_json(payload)
    uncertainty = 1 if payload["quality_blockers"] or (payload["risk"] and payload["risk"]["status"] in {"stop", "kill_switch"}) else 0
    lines = [
        "# RenCrow Weekly Investment Report",
        "",
        f"- task: {options.task}",
        f"- model: {options.model}",
        f"- prompt_version: {options.prompt_version}",
        f"- snapshot_id: {options.snapshot_id}",
        f"- snapshot_date: {snapshot['snapshot_date']}",
        f"- snapshot_status: {snapshot['status']}",
        f"- quality_blockers: {payload['quality_blockers']}",
        "",
        "## Decision",
        "",
    ]
    if decision is None:
        lines.append("- decision: not_found")
    else:
        candidate = payload["decision"]["candidate_json"]  # type: ignore[index]
        veto = payload["decision"]["veto_json"]  # type: ignore[index]
        lines.extend(
            [
                f"- decision_id: {decision['decision_id']}",
                f"- approval_required: {candidate.get('approval_required')}",
                f"- approved: {decision['approved']}",
                f"- vetoed: {veto.get('vetoed')}",
                f"- risk_status: {candidate.get('risk_status')}",
            ]
        )
        for item in candidate.get("candidates", []):
            lines.append(f"- candidate: {item.get('symbol')} target_weight={item.get('target_weight')}")
    lines.extend(["", "## Risk", ""])
    if risk is None:
        lines.append("- risk: not_found")
    else:
        lines.extend(
            [
                f"- risk_check_id: {risk['risk_check_id']}",
                f"- status: {risk['status']}",
                f"- max_dd_check: {risk['max_dd_check']}",
                f"- weekly_loss_check: {risk['weekly_loss_check']}",
                f"- volatility_check: {risk['volatility_check']}",
                f"- event_check: {risk['event_check']}",
            ]
        )
    lines.extend(
        [
            "",
            "## Handling",
            "",
            "- This report is explanatory only.",
            "- It does not create orders.",
            "- If uncertainty_flag is 1, human review or no-trade handling is required.",
            f"- uncertainty_flag: {uncertainty}",
            "",
        ]
    )
    output_text = "\n".join(lines)
    output_dir = options.output_dir or Path("rencrow-data/reports")
    output_dir.mkdir(parents=True, exist_ok=True)
    output_path = output_dir / f"llm_{options.task}_snapshot_{options.snapshot_id}.md"
    output_path.write_text(output_text, encoding="utf-8")
    llm_log_id = unique_id("llm")
    con.execute(
        """
        INSERT INTO llm_audit_log(
          llm_log_id, snapshot_id, decision_id, task_type, model, prompt_version, input_hash,
          output_hash, output_path, uncertainty_flag
        )
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        """,
        (
            llm_log_id,
            options.snapshot_id,
            None if decision is None else int(decision["decision_id"]),
            options.task,
            options.model,
            options.prompt_version,
            _sha256(input_text),
            _sha256(output_text),
            str(output_path),
            uncertainty,
        ),
    )
    con.commit()
    return {
        "llm_log_id": llm_log_id,
        "snapshot_id": options.snapshot_id,
        "decision_id": None if decision is None else int(decision["decision_id"]),
        "task": options.task,
        "model": options.model,
        "prompt_version": options.prompt_version,
        "output_path": str(output_path),
        "uncertainty_flag": uncertainty,
    }
