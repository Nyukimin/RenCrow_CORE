from __future__ import annotations

from datetime import date, datetime, timezone
from uuid import uuid4


def utcnow_iso() -> str:
    return datetime.now(timezone.utc).replace(microsecond=0).isoformat()


def unique_id(prefix: str) -> str:
    return f"{prefix}-{utcnow_iso()}-{uuid4().hex[:12]}"


def parse_date(value: str) -> date:
    return datetime.strptime(value, "%Y-%m-%d").date()


def friday_of_week(day: date) -> date:
    return date.fromordinal(day.toordinal() + (4 - day.weekday()))


def iso(day: date) -> str:
    return day.isoformat()
