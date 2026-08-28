#!/usr/bin/env python3
"""Validate the remediation loop's ordering and terminal gates."""

from __future__ import annotations

import re
import sys
from pathlib import Path

ORDERED = (
    "### 1. Reproduce and RED",
    "### 2. Implement and local GREEN",
    "### 3. Pre-deploy canonical E2E",
    "### 4. Commit and Push",
    "### 5. Rebuild, restart, and deploy",
    "### 6. Post-deploy defect test",
    "## Final full check",
)
REQUIRED = (
    "WIPは常に1件",
    "rencrow-full-system-verification",
    "force pushしない",
    "source revision、pushed revision、installed artifact、service identity、実request receipt",
    "failed=0",
    "blocked=0",
    "deferred=0",
    "unverified=0",
    "フルチェックで新しい未達が出たら",
)

def validate(skill_dir: Path) -> list[str]:
    path = skill_dir / "SKILL.md"
    if not path.is_file():
        return ["SKILL.md not found"]
    text = path.read_text(encoding="utf-8")
    frontmatter = re.match(r"^---\r?\n(.*?)\r?\n---(?:\r?\n|$)", text, re.DOTALL)
    errors: list[str] = []
    if not frontmatter:
        return ["SKILL.md must start with YAML frontmatter"]
    if f"name: {skill_dir.name}" not in frontmatter.group(1):
        errors.append("frontmatter name must match directory")
    positions = [text.find(marker) for marker in ORDERED]
    if any(position < 0 for position in positions):
        errors.append("required loop stage is missing")
    elif positions != sorted(positions):
        errors.append("remediation stages are out of order")
    for term in REQUIRED:
        if term not in text:
            errors.append(f"missing terminal invariant: {term}")
    return errors

def main(argv: list[str]) -> int:
    if len(argv) != 2:
        print(f"usage: {Path(argv[0]).name} SKILL_DIRECTORY", file=sys.stderr)
        return 2
    errors = validate(Path(argv[1]))
    for error in errors:
        print(f"[FAIL] {error}")
    if errors:
        return 1
    print("[PASS] remediation loop contract")
    return 0

if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
