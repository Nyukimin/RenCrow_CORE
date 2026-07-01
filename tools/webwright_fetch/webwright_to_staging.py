#!/usr/bin/env python3
"""Convert Webwright output into RenCrow L1 staging JSONL.

The output intentionally uses Go struct field names for
conversation.L1StagingItem because ImportStagingItemsJSONL unmarshals directly
into that Go type.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


SECRET_PATTERNS = [
    re.compile(r"(?i)\b(api[_-]?key|authorization|bearer|cookie|set-cookie)\b"),
    re.compile(r"\bsk-[A-Za-z0-9_-]{16,}\b"),
]


def now_rfc3339() -> str:
    return datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def load_json(path: Path) -> Any:
    with path.open("r", encoding="utf-8") as fh:
        return json.load(fh)


def text_from_value(value: Any, prefix: str = "") -> list[str]:
    lines: list[str] = []
    if value is None:
        return lines
    if isinstance(value, str):
        text = value.strip()
        if text:
            lines.append(f"{prefix}{text}" if prefix else text)
        return lines
    if isinstance(value, (int, float, bool)):
        lines.append(f"{prefix}{value}" if prefix else str(value))
        return lines
    if isinstance(value, list):
        for idx, item in enumerate(value, start=1):
            item_prefix = f"{prefix}{idx}. " if prefix else f"{idx}. "
            child = text_from_value(item, item_prefix)
            lines.extend(child)
        return lines
    if isinstance(value, dict):
        title = first_string(value, ["title", "heading", "name", "label"])
        summary = first_string(value, ["summary", "result", "answer", "text", "content", "description"])
        if title and summary and title != summary:
            lines.append(f"{prefix}{title}: {summary}" if prefix else f"{title}: {summary}")
        elif title or summary:
            text = title or summary
            lines.append(f"{prefix}{text}" if prefix else text)
        for key in sorted(value.keys()):
            if key in {"title", "heading", "name", "label", "summary", "result", "answer", "text", "content", "description"}:
                continue
            if key in {"screenshots", "trajectory", "html", "raw_html"}:
                continue
            child_prefix = f"{key}: "
            lines.extend(text_from_value(value[key], child_prefix))
        return lines
    return lines


def first_string(data: dict[str, Any], keys: list[str]) -> str:
    for key in keys:
        value = data.get(key)
        if isinstance(value, str) and value.strip():
            return value.strip()
    return ""


def extract_task_id(path: Path, data: Any, explicit: str) -> str:
    if explicit:
        return explicit
    if isinstance(data, dict):
        for key in ("task_id", "id", "short_id"):
            value = data.get(key)
            if isinstance(value, str) and value.strip():
                return slug(value)
    return slug(path.stem)


def slug(value: str) -> str:
    text = re.sub(r"[^A-Za-z0-9_.:-]+", "-", value.strip())
    text = text.strip("-")
    return text or "webwright"


def collect_raw_text(data: Any) -> str:
    if isinstance(data, dict):
        for key in ("report", "result", "results", "sections", "items", "data"):
            if key in data:
                lines = text_from_value(data[key])
                if lines:
                    return "\n".join(lines)
    lines = text_from_value(data)
    return "\n".join(lines)


def collect_summary(data: Any, raw_text: str) -> str:
    if isinstance(data, dict):
        for key in ("summary", "title", "answer", "result"):
            value = data.get(key)
            if isinstance(value, str) and value.strip():
                return truncate_one_line(value, 240)
    return truncate_one_line(raw_text, 240)


def truncate_one_line(value: str, limit: int) -> str:
    text = re.sub(r"\s+", " ", value.strip())
    if len(text) <= limit:
        return text
    return text[: limit - 1].rstrip() + "…"


def detect_secret(text: str) -> bool:
    return any(pattern.search(text) for pattern in SECRET_PATTERNS)


def staging_item(args: argparse.Namespace) -> dict[str, Any]:
    input_path = args.input
    data = load_json(input_path)
    raw_text = collect_raw_text(data)
    if not raw_text.strip():
        raise ValueError("webwright report produced empty raw text")
    if detect_secret(raw_text):
        raise ValueError("webwright report appears to contain secrets or credentials")
    task_id = extract_task_id(input_path, data, args.task_id)
    fetched_at = args.fetched_at or now_rfc3339()
    source_id = args.source_id or f"webwright:{task_id}"
    event_id = args.event_id or f"webwright:{task_id}"
    summary = args.summary or collect_summary(data, raw_text)
    raw_hash = hashlib.sha256(raw_text.encode("utf-8")).hexdigest()

    meta = {
        "webwright": True,
        "tool": "webwright_fetch",
        "input_path": str(input_path),
        "task_id": task_id,
        "raw_sha256": raw_hash,
        "review_required": True,
        "auto_promote": False,
    }
    if args.output_root:
        meta["output_root"] = str(args.output_root)

    return {
        "Kind": "external_fetch",
        "Namespace": args.namespace,
        "EventID": event_id,
        "SourceID": source_id,
        "SourceURL": args.source_url,
        "FetchedAt": fetched_at,
        "RawText": raw_text,
        "SummaryDraft": summary,
        "Keywords": args.keyword,
        "LicenseNote": args.license_note,
        "ValidationStatus": "pending",
        "Meta": meta,
    }


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Convert Webwright report JSON into RenCrow L1 staging JSONL.")
    parser.add_argument("--input", type=Path, required=True, help="Webwright report.json or renderer-ready JSON.")
    parser.add_argument("--output", type=Path, required=True, help="Output L1 staging JSONL path.")
    parser.add_argument("--namespace", default="kb:webwright", help="L1 namespace, e.g. kb:news or kb:webwright.")
    parser.add_argument("--task-id", default="", help="Stable task id. Defaults to report id or input stem.")
    parser.add_argument("--event-id", default="", help="L1 staging event id. Defaults to webwright:<task-id>.")
    parser.add_argument("--source-id", default="", help="Source id. Defaults to webwright:<task-id>.")
    parser.add_argument("--source-url", default="", help="Source URL represented by this fetch.")
    parser.add_argument("--summary", default="", help="Override summary draft.")
    parser.add_argument("--license-note", default="webwright browser fetch; review source terms before promotion")
    parser.add_argument("--keyword", action="append", default=["webwright"], help="Keyword. Repeatable.")
    parser.add_argument("--fetched-at", default="", help="RFC3339 timestamp. Defaults to current UTC.")
    parser.add_argument("--output-root", type=Path, default=None, help="Webwright run output root for traceability.")
    return parser.parse_args(argv)


def main(argv: list[str]) -> int:
    args = parse_args(argv)
    try:
        item = staging_item(args)
    except Exception as exc:
        print(f"webwright_to_staging: {exc}", file=sys.stderr)
        return 1
    args.output.parent.mkdir(parents=True, exist_ok=True)
    with args.output.open("w", encoding="utf-8") as fh:
        fh.write(json.dumps(item, ensure_ascii=False, separators=(",", ":")) + "\n")
    print(str(args.output))
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
