#!/usr/bin/env python3
"""Fail closed when the full-system skill loses its dynamic owner contracts."""

from __future__ import annotations

import re
import sys
from pathlib import Path


TRIGGER = "システム全体を検証して"
REQUIRED_TERMS = (
    "ecosystem.yaml",
    "full-system-coverage.json",
    "every owner v2 check manifest",
    "required_phases",
    "rencrow-full-system-verification compose",
    "5つのcompose結果",
    "deterministic common",
    "execute-set",
    "--composition-dir",
    "--owner-bin-dir",
    "--workspace-root",
    "--evidence-dir",
    "--receipt-dir",
    "execution_bindings",
    "owner CLI",
    "owner CLIだけ",
    "rencrow.check-receipt.v1",
    "`passed`と`not_applicable`は空でない`evidence_refs`を必須とする",
    "`failed`、`blocked`、",
    "`unverified`は`evidence_refs`を空にできるが",
    "空でない`failure_boundary`を必須とする",
    "RFC3339 UTC",
    "receipt.observed_at - 5m <= evidence.observed_at <= receipt.observed_at",
    "inclusive window",
    "preflight error",
    "fabricate",
    "aggregate-set",
    "all_clear=true",
    "read-only",
    "実Actor",
    "blocked",
    "unverified",
    "Tailscale",
    "backup/restore",
)
FORBIDDEN_STATIC_MARKERS = (
    "Fixed mandatory check matrix",
    "complete 22-row",
    "complete 23-row",
)
REQUIRED_PATTERNS = (
    (
        r"receipt\.observed_at\s*-\s*5m\s*<=\s*evidence\.observed_at\s*<=\s*receipt\.observed_at",
        "evidence freshness must be bounded by receipt observed_at +/- 5m",
    ),
    (
        r"owner binary.*manifest.*preflight error",
        "owner binary and manifest prerequisites must fail in preflight",
    ),
)


def validate(skill_dir: Path) -> list[str]:
    path = skill_dir / "SKILL.md"
    if not path.is_file():
        return ["SKILL.md not found"]
    text = path.read_text(encoding="utf-8")
    frontmatter = re.match(r"^---\r?\n(.*?)\r?\n---(?:\r?\n|$)", text, re.DOTALL)
    if not frontmatter:
        return ["SKILL.md must start with YAML frontmatter"]
    errors: list[str] = []
    if f"name: {skill_dir.name}" not in frontmatter.group(1):
        errors.append("frontmatter name must match skill directory")
    if TRIGGER not in frontmatter.group(1):
        errors.append("frontmatter description must contain the exact trigger")
    for term in REQUIRED_TERMS:
        if term not in text:
            errors.append(f"missing dynamic contract term: {term}")
    for pattern, message in REQUIRED_PATTERNS:
        if re.search(pattern, text, re.IGNORECASE | re.DOTALL) is None:
            errors.append(message)
    for marker in FORBIDDEN_STATIC_MARKERS:
        if marker in text:
            errors.append(f"obsolete static matrix marker remains: {marker}")
    return errors


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        print(f"usage: {Path(argv[0]).name} SKILL_DIRECTORY", file=sys.stderr)
        return 2
    errors = validate(Path(argv[1]))
    if errors:
        for error in errors:
            print(f"[FAIL] {error}")
        return 1
    print("[PASS] dynamic full-system verification skill contract")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
