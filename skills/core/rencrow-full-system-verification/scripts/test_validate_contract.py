#!/usr/bin/env python3
"""Regression tests for the full-system verification skill contract validator."""

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


class ValidateContractTest(unittest.TestCase):
    def test_current_skill_satisfies_contract(self) -> None:
        self.assertEqual(VALIDATOR.validate(SKILL_DIR), [])

    def test_missing_freshness_expression_fails_closed(self) -> None:
        text = (SKILL_DIR / "SKILL.md").read_text(encoding="utf-8")
        text = text.replace(
            "receipt.observed_at - 5m <= evidence.observed_at <= receipt.observed_at",
            "freshness expression removed",
            1,
        )
        errors = self._validate_text(text)
        self.assertIn(
            "evidence freshness must be bounded by receipt observed_at +/- 5m",
            errors,
        )

    def test_missing_execute_set_preflight_contract_fails_closed(self) -> None:
        text = (SKILL_DIR / "SKILL.md").read_text(encoding="utf-8")
        text = text.replace(
            "owner binaryまたは参照manifestがmissingならpreflight errorで停止し、",
            "owner binaryまたは参照manifestがmissingならvalidation resultで停止し、",
            1,
        )
        errors = self._validate_text(text)
        self.assertIn(
            "owner binary and manifest prerequisites must fail in preflight",
            errors,
        )

    def test_missing_receipt_evidence_rules_fail_closed(self) -> None:
        text = (SKILL_DIR / "SKILL.md").read_text(encoding="utf-8")
        success_rule = "`passed`と`not_applicable`は空でない`evidence_refs`を必須とする。"
        failure_rule = "`failed`、`blocked`、\n`unverified`は`evidence_refs`を空にできるが、その場合は空でない`failure_boundary`を必須とする。"
        for snippet, expected in (
            (success_rule, "`passed`と`not_applicable`は空でない`evidence_refs`を必須とする"),
            (failure_rule, "`unverified`は`evidence_refs`を空にできるが"),
        ):
            with self.subTest(rule=expected):
                errors = self._validate_text(text.replace(snippet, "receipt evidence rule removed", 1))
                self.assertTrue(
                    any(expected in error for error in errors),
                    f"validator errors={errors}",
                )

    def test_crlf_frontmatter_remains_valid(self) -> None:
        text = (SKILL_DIR / "SKILL.md").read_text(encoding="utf-8")
        errors = self._validate_text(text.replace("\n", "\r\n"))
        self.assertEqual(errors, [])

    @staticmethod
    def _validate_text(text: str) -> list[str]:
        with tempfile.TemporaryDirectory() as root:
            skill_dir = Path(root) / SKILL_DIR.name
            skill_dir.mkdir()
            (skill_dir / "SKILL.md").write_text(text, encoding="utf-8", newline="")
            return VALIDATOR.validate(skill_dir)


if __name__ == "__main__":
    unittest.main()
