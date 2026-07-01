import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "tools" / "webwright_fetch" / "webwright_to_staging.py"


class WebwrightToStagingTest(unittest.TestCase):
    def test_converts_report_to_l1_staging_jsonl(self):
        with tempfile.TemporaryDirectory() as td:
            tmp = Path(td)
            report = tmp / "report.json"
            out = tmp / "staging.jsonl"
            report.write_text(
                json.dumps(
                    {
                        "task_id": "ai-policy",
                        "summary": "AI policy updates from public pages",
                        "sections": [
                            {"title": "Agency notice", "summary": "A public agency updated its AI guidance."},
                            {"title": "Implementation", "summary": "The guidance affects procurement review."},
                        ],
                    },
                    ensure_ascii=False,
                ),
                encoding="utf-8",
            )

            proc = subprocess.run(
                [
                    sys.executable,
                    str(SCRIPT),
                    "--input",
                    str(report),
                    "--output",
                    str(out),
                    "--namespace",
                    "kb:news",
                    "--source-url",
                    "https://example.test/search",
                    "--fetched-at",
                    "2026-05-28T00:00:00Z",
                ],
                cwd=ROOT,
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, proc.stderr)
            item = json.loads(out.read_text(encoding="utf-8").strip())
            self.assertEqual(item["Kind"], "external_fetch")
            self.assertEqual(item["Namespace"], "kb:news")
            self.assertEqual(item["EventID"], "webwright:ai-policy")
            self.assertEqual(item["SourceID"], "webwright:ai-policy")
            self.assertEqual(item["SourceURL"], "https://example.test/search")
            self.assertEqual(item["ValidationStatus"], "pending")
            self.assertIn("Agency notice", item["RawText"])
            self.assertEqual(item["SummaryDraft"], "AI policy updates from public pages")
            self.assertTrue(item["Meta"]["webwright"])
            self.assertFalse(item["Meta"]["auto_promote"])
            self.assertTrue(item["Meta"]["review_required"])

    def test_rejects_secret_like_report(self):
        with tempfile.TemporaryDirectory() as td:
            tmp = Path(td)
            report = tmp / "report.json"
            out = tmp / "staging.jsonl"
            report.write_text(json.dumps({"result": "Authorization: Bearer token"}), encoding="utf-8")

            proc = subprocess.run(
                [sys.executable, str(SCRIPT), "--input", str(report), "--output", str(out)],
                cwd=ROOT,
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                check=False,
            )
            self.assertNotEqual(proc.returncode, 0)
            self.assertIn("secrets or credentials", proc.stderr)
            self.assertFalse(out.exists())


if __name__ == "__main__":
    unittest.main()
