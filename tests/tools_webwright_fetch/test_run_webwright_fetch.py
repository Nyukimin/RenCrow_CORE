import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "tools" / "webwright_fetch" / "run_webwright_fetch.py"
LOCAL_PROFILE = ROOT / "tools" / "webwright_fetch" / "config_local_worker.yaml"


class RunWebwrightFetchTest(unittest.TestCase):
    def test_dry_run_uses_selected_python(self):
        with tempfile.TemporaryDirectory() as td:
            proc = subprocess.run(
                [
                    sys.executable,
                    str(SCRIPT),
                    "--task",
                    "Collect public data",
                    "--output-dir",
                    td,
                    "--python",
                    "/tmp/webwright-python",
                    "--dry-run",
                ],
                cwd=ROOT,
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                check=False,
            )

            self.assertEqual(proc.returncode, 0, proc.stderr)
            self.assertIn("/tmp/webwright-python -m webwright.run.cli", proc.stdout)
            self.assertIn("-t 'Collect public data'", proc.stdout)

    def test_dry_run_supports_uvx_package_source(self):
        with tempfile.TemporaryDirectory() as td:
            proc = subprocess.run(
                [
                    sys.executable,
                    str(SCRIPT),
                    "--task",
                    "Collect public data",
                    "--output-dir",
                    td,
                    "--uvx-binary",
                    "/usr/local/bin/uvx",
                    "--uvx-from",
                    "git+https://github.com/microsoft/Webwright.git",
                    "--dry-run",
                ],
                cwd=ROOT,
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                check=False,
            )

            self.assertEqual(proc.returncode, 0, proc.stderr)
            self.assertIn(
                "/usr/local/bin/uvx --from git+https://github.com/microsoft/Webwright.git python -m webwright.run.cli",
                proc.stdout,
            )

    def test_explicit_config_does_not_duplicate_default_base(self):
        with tempfile.TemporaryDirectory() as td:
            proc = subprocess.run(
                [
                    sys.executable,
                    str(SCRIPT),
                    "--task",
                    "Collect public data",
                    "--output-dir",
                    td,
                    "-c",
                    "base.yaml",
                    "--dry-run",
                ],
                cwd=ROOT,
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                check=False,
            )

            self.assertEqual(proc.returncode, 0, proc.stderr)
            self.assertEqual(proc.stdout.count("-c base.yaml"), 1)

    def test_local_responses_endpoint_appends_profile_and_override(self):
        with tempfile.TemporaryDirectory() as td:
            proc = subprocess.run(
                [
                    sys.executable,
                    str(SCRIPT),
                    "--task",
                    "Collect public data",
                    "--output-dir",
                    td,
                    "--local-responses-endpoint",
                    "http://192.168.1.207:8082/v1/responses",
                    "--local-model",
                    "Coder1",
                    "--dry-run",
                ],
                cwd=ROOT,
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                check=False,
            )

            self.assertEqual(proc.returncode, 0, proc.stderr)
            self.assertIn(f"-c {LOCAL_PROFILE}", proc.stdout)
            override = Path(td) / "_webwright_local_responses_override.yaml"
            self.assertTrue(override.exists())
            override_text = override.read_text(encoding="utf-8")
            self.assertIn("model_name: Coder1", override_text)
            self.assertIn("openai_endpoint: http://192.168.1.207:8082/v1/responses", override_text)
            self.assertIn("openai_api_key: dummy", override_text)

    def test_local_profile_forbids_interactive_codegen(self):
        text = LOCAL_PROFILE.read_text(encoding="utf-8")
        self.assertIn("Never use `playwright codegen`", text)
        self.assertIn("Do not write markdown", text)
        self.assertIn("headless", text)


if __name__ == "__main__":
    unittest.main()
