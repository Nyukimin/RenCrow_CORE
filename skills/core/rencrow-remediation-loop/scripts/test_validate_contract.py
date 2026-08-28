#!/usr/bin/env python3
"""Regression tests for the remediation-loop skill contract."""

from __future__ import annotations

import importlib.util
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).with_name("validate_contract.py")
SKILL_DIR = SCRIPT.parent.parent
SPEC = importlib.util.spec_from_file_location("validate_contract", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
VALIDATOR = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(VALIDATOR)

class ContractTest(unittest.TestCase):
    def test_current_skill_is_valid(self) -> None:
        self.assertEqual(VALIDATOR.validate(SKILL_DIR), [])

    def test_reordered_push_and_e2e_fails(self) -> None:
        text = (SKILL_DIR / "SKILL.md").read_text(encoding="utf-8")
        text = text.replace("### 3. Pre-deploy canonical E2E", "REORDER_PLACEHOLDER", 1)
        text = text.replace("### 4. Commit and Push", "### 3. Pre-deploy canonical E2E", 1)
        text = text.replace("REORDER_PLACEHOLDER", "### 4. Commit and Push", 1)
        errors = self._validate(text)
        self.assertIn("remediation stages are out of order", errors)

    def test_missing_final_gate_fails(self) -> None:
        text = (SKILL_DIR / "SKILL.md").read_text(encoding="utf-8")
        errors = self._validate(text.replace("blocked=0", "blocked count omitted", 1))
        self.assertTrue(any("blocked=0" in error for error in errors))

    @staticmethod
    def _validate(text: str) -> list[str]:
        with tempfile.TemporaryDirectory() as root:
            skill_dir = Path(root) / SKILL_DIR.name
            skill_dir.mkdir()
            (skill_dir / "SKILL.md").write_text(text, encoding="utf-8")
            return VALIDATOR.validate(skill_dir)

if __name__ == "__main__":
    unittest.main()
